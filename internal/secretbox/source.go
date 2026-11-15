package secretbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrNoKeyring means the source holds no keyring yet.
var ErrNoKeyring = errors.New("no keyring stored")

// ErrKeyStore means the keyring couldn't be kept where its source keeps it,
// such as a folder the service can't write. Nothing was sealed with it. The
// wrapped error says why.
var ErrKeyStore = errors.New("the keyring couldn't be kept")

// Source keeps the keyring outside the database.
type Source interface {
	Load(context.Context) (*Keyring, error)
	Store(context.Context, *Keyring) error
	// Describe names where the keyring lives, for status and errors. It never
	// includes key material.
	Describe() string
}

// Ensure loads the keyring, creating and storing a new one the first time.
func Ensure(ctx context.Context, s Source) (*Keyring, error) {
	k, err := s.Load(ctx)
	if !errors.Is(err, ErrNoKeyring) {
		return k, err
	}
	if k, err = NewKeyring(); err != nil {
		return nil, err
	}
	if err = s.Store(ctx, k); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyStore, err)
	}
	// Read it back so a source that silently dropped the write is caught now,
	// before anything is sealed with a key that would be lost.
	return s.Load(ctx)
}

// FileSource keeps the keyring in one private file, for servers. Put it
// outside the database volume and its backups, such as a mounted secret.
type FileSource struct{ Path string }

func (f FileSource) Describe() string { return "file " + f.Path }

func (f FileSource) Load(context.Context) (*Keyring, error) {
	if !filepath.IsAbs(f.Path) {
		return nil, fmt.Errorf("keyring file must be an absolute path")
	}
	info, err := os.Lstat(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoKeyring
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("keyring file must be a regular file readable only by its owner")
	}
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, err
	}
	defer clear(b)
	return ParseKeyring(bytes.TrimSpace(b))
}

// Store replaces the file atomically, readable only by its owner.
func (f FileSource) Store(_ context.Context, k *Keyring) error {
	if !filepath.IsAbs(f.Path) {
		return fmt.Errorf("keyring file must be an absolute path")
	}
	b, err := MarshalKeyring(k)
	if err != nil {
		return err
	}
	defer clear(b)
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".keyring-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0o600); err == nil {
		if _, err = tmp.Write(b); err == nil {
			err = tmp.Sync()
		}
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.Path)
}

// KeychainSource keeps the keyring as a generic password in a macOS keychain,
// through /usr/bin/security. The secret is passed on standard input, never as
// a command argument other processes could read.
type KeychainSource struct {
	Service string
	Account string
	// Keychain is a keychain file path; empty means the caller's default.
	// The local service, which runs as root, uses the System keychain.
	Keychain string
	// Run is replaced in tests; nil runs /usr/bin/security.
	Run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

// SystemKeychain is the keychain the local service uses.
const SystemKeychain = "/Library/Keychains/System.keychain"

func (k KeychainSource) Describe() string {
	where := "the default keychain"
	if k.Keychain != "" {
		where = k.Keychain
	}
	return "keychain item " + k.Service + " in " + where
}

func (k KeychainSource) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if k.Run != nil {
		return k.Run(ctx, stdin, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		// 44 is errSecItemNotFound's exit status from security(1).
		if errors.As(err, &exit) && exit.ExitCode() == 44 {
			return nil, ErrNoKeyring
		}
		return nil, fmt.Errorf("keychain unavailable")
	}
	return out.Bytes(), nil
}

func (k KeychainSource) valid() bool {
	ok := func(s string) bool {
		return s != "" && len(s) <= 128 && !strings.ContainsAny(s, "\x00\n\r\"\\ ")
	}
	return ok(k.Service) && ok(k.Account) && (k.Keychain == "" || ok(k.Keychain) && filepath.IsAbs(k.Keychain))
}

func (k KeychainSource) Load(ctx context.Context) (*Keyring, error) {
	if !k.valid() {
		return nil, ErrInvalid
	}
	args := []string{"find-generic-password", "-s", k.Service, "-a", k.Account, "-w"}
	if k.Keychain != "" {
		args = append(args, k.Keychain)
	}
	out, err := k.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	defer clear(out)
	return ParseKeyring(bytes.TrimSpace(out))
}

func (k KeychainSource) Store(ctx context.Context, ring *Keyring) error {
	if !k.valid() {
		return ErrInvalid
	}
	b, err := MarshalKeyring(ring)
	if err != nil {
		return err
	}
	defer clear(b)
	// Passed as hex (-X) so the JSON needs no quoting in security's
	// interactive command line.
	hex := fmt.Sprintf("%x", b)
	command := fmt.Sprintf("add-generic-password -U -s %s -a %s -X %s", k.Service, k.Account, hex)
	if k.Keychain != "" {
		command += " " + k.Keychain
	}
	line := []byte(command + "\n")
	defer clear(line)
	_, err = k.run(ctx, line, "-i")
	return err
}

// Cached loads a keyring once and keeps it in memory. Rotation replaces it.
type Cached struct {
	Source Source
	mu     sync.Mutex
	ring   *Keyring
}

// Keyring returns the keyring, creating one in the source the first time.
func (c *Cached) Keyring(ctx context.Context) (*Keyring, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ring != nil {
		return c.ring, nil
	}
	ring, err := Ensure(ctx, c.Source)
	if err != nil {
		return nil, err
	}
	c.ring = ring
	return ring, nil
}

// Rotate stores a keyring with a new current key and starts using it. Stored
// values keep opening with their own versions until they are re-sealed.
func (c *Cached) Rotate(ctx context.Context) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ring, err := Ensure(ctx, c.Source)
	if err != nil {
		return 0, err
	}
	version, err := ring.Rotate()
	if err != nil {
		return 0, err
	}
	if err = c.Source.Store(ctx, ring); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrKeyStore, err)
	}
	if ring, err = c.Source.Load(ctx); err != nil || ring.Current() != version {
		return 0, errors.New("the rotated keyring was not stored")
	}
	c.ring = ring
	return version, nil
}
