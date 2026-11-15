package secretbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Level says what a keyring may be used for.
type Level string

const (
	// LevelRead keys open what Vult needs to read, such as bank access
	// tokens. They unlock in the background.
	LevelRead Level = "read"
	// LevelAct is reserved for keys whose every use needs the person's
	// presence for one specific action. Nothing uses it yet.
	LevelAct Level = "act"
)

// ErrPresenceRequired means a key needs the person's presence, which no
// background use can supply.
var ErrPresenceRequired = errors.New("this key needs the person's presence")

// ErrNoEnclave means this Mac has no Secure Enclave.
var ErrNoEnclave = errors.New("no Secure Enclave")

// EnclaveSource keeps a keyring in a folder, sealed by this Mac's Secure
// Enclave through the vult-enclave helper. A copied file, disk or backup
// can't be opened on another Mac, so the files may be backed up: restoring
// on the same Mac keeps working, and a new Mac reconnects.
type EnclaveSource struct {
	Dir    string
	Helper string
	Level  Level
	// Run is replaced in tests; nil runs Helper.
	Run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

func (e EnclaveSource) Describe() string {
	return "keyring in " + e.Dir + " sealed by this Mac's Secure Enclave"
}

func (e EnclaveSource) files() (sealed, key string) {
	return filepath.Join(e.Dir, "keyring."+string(e.Level)+".sealed"), filepath.Join(e.Dir, "enclave."+string(e.Level)+".key")
}

func (e EnclaveSource) run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if e.Run != nil {
		return e.Run(ctx, stdin, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Helper, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	cmd.Stdin = bytes.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			switch exit.ExitCode() {
			case 3:
				return nil, ErrNoEnclave
			case 4:
				return nil, ErrPresenceRequired
			}
		}
		return nil, fmt.Errorf("the Secure Enclave helper failed: %s", bytes.TrimSpace(errOut.Bytes()))
	}
	return out.Bytes(), nil
}

func (e EnclaveSource) valid() error {
	if e.Level == LevelAct {
		return ErrPresenceRequired
	}
	if e.Level != LevelRead || !filepath.IsAbs(e.Dir) || !filepath.IsAbs(e.Helper) {
		return ErrInvalid
	}
	return nil
}

func (e EnclaveSource) Load(ctx context.Context) (*Keyring, error) {
	if err := e.valid(); err != nil {
		return nil, err
	}
	sealed, key := e.files()
	data, err := readPrivate(sealed)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoKeyring
	}
	if err != nil {
		return nil, err
	}
	plain, err := e.run(ctx, data, "open", string(e.Level), key)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return ParseKeyring(plain)
}

func (e EnclaveSource) Store(ctx context.Context, k *Keyring) error {
	if err := e.valid(); err != nil {
		return err
	}
	plain, err := MarshalKeyring(k)
	if err != nil {
		return err
	}
	defer clear(plain)
	sealed, key := e.files()
	out, err := e.run(ctx, plain, "seal", string(e.Level), key)
	if err != nil {
		return err
	}
	return writePrivate(sealed, out)
}

// MacSource chooses where a Mac keeps a keyring: sealed by the Secure
// Enclave when the Mac has one, otherwise in a locked file left out of Time
// Machine, since nothing ties that file to this Mac. A keyring already kept
// one way stays that way.
func MacSource(ctx context.Context, dir, helper string, level Level) (Source, error) {
	if level == LevelAct {
		return nil, ErrPresenceRequired
	}
	plain := filepath.Join(dir, "keyring."+string(level)+".json")
	if _, err := os.Lstat(plain); err == nil {
		return excludedFile{FileSource{Path: plain}}, nil
	}
	enclave := EnclaveSource{Dir: dir, Helper: helper, Level: level}
	if _, err := enclave.run(ctx, nil, "available"); err == nil {
		return enclave, nil
	} else if !errors.Is(err, ErrNoEnclave) {
		return nil, err
	}
	return excludedFile{FileSource{Path: plain}}, nil
}

// excludedFile is a keyring file kept out of Time Machine backups.
type excludedFile struct{ FileSource }

func (f excludedFile) Store(ctx context.Context, k *Keyring) error {
	if err := f.FileSource.Store(ctx, k); err != nil {
		return err
	}
	// The exclusion travels with the file; a failure leaves the file usable.
	_ = exec.CommandContext(ctx, "/usr/bin/tmutil", "addexclusion", f.Path).Run()
	return nil
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("keyring file must be a regular file readable only by its owner")
	}
	return os.ReadFile(path)
}

func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".keyring-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0o600); err == nil {
		if _, err = tmp.Write(data); err == nil {
			err = tmp.Sync()
		}
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
