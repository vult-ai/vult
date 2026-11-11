// Package updaterepo maintains a signed update repository on disk: offline
// signing keys kept as encrypted files, staged metadata, signing with the
// checks a release must pass first, and publishing in the layout that
// internal/updatetrust verifies.
package updaterepo

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"

	"github.com/vult-ai/vult/internal/strictjson"
)

// ScryptWorkFactor is the passphrase hardening for new key files (2^N).
// Tests lower it; nothing else should.
var ScryptWorkFactor = 18

const keyFormat = "vult-update-key-v1"

// MinPassphrase is the shortest passphrase accepted for a key file.
const MinPassphrase = 16

type keyFile struct {
	Format  string `json:"format"`
	KeyType string `json:"keytype"`
	Seed    string `json:"seed"`
}

// Signer is one key ready for one signing step: a key file decrypted in
// memory, or a Touch ID key in the Secure Enclave.
type Signer struct {
	ID   string
	Key  *metadata.Key
	sig  signature.Signer
	wipe func()
}

// Close forgets the key; a decrypted key file is overwritten in memory.
func (s *Signer) Close() {
	if s.wipe != nil {
		s.wipe()
		s.wipe = nil
	}
	s.sig = nil
}

func (s *Signer) signer() (signature.Signer, error) {
	if s.sig == nil {
		return nil, errors.New("signing key is closed")
	}
	return s.sig, nil
}

// PublicKey returns the TUF form of an Ed25519 public key and its key ID.
func PublicKey(public ed25519.PublicKey) (*metadata.Key, string, error) {
	key, err := metadata.KeyFromPublicKey(public)
	if err != nil {
		return nil, "", err
	}
	id, err := key.ID()
	return key, id, err
}

// NewKey generates a key and writes it to path, encrypted with passphrase.
// The file must not exist. It returns the public key and its ID.
func NewKey(path string, passphrase []byte) (*metadata.Key, string, error) {
	if len(passphrase) < MinPassphrase {
		return nil, "", fmt.Errorf("the passphrase needs at least %d characters", MinPassphrase)
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		for i := range private {
			private[i] = 0
		}
	}()
	plain, err := json.Marshal(keyFile{Format: keyFormat, KeyType: "ed25519", Seed: hex.EncodeToString(private.Seed())})
	if err != nil {
		return nil, "", err
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	recipient, err := age.NewScryptRecipient(string(passphrase))
	if err != nil {
		return nil, "", err
	}
	recipient.SetWorkFactor(ScryptWorkFactor)
	var sealed bytes.Buffer
	w, err := age.Encrypt(&sealed, recipient)
	if err != nil {
		return nil, "", err
	}
	if _, err = w.Write(plain); err == nil {
		err = w.Close()
	}
	if err != nil {
		return nil, "", err
	}
	if err := writeNew(path, sealed.Bytes(), 0o600); err != nil {
		return nil, "", err
	}
	return PublicKey(public)
}

// OpenKey decrypts a key file into memory. Call Close when done.
func OpenKey(path string, passphrase []byte) (*Signer, error) {
	sealed, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	identity, err := age.NewScryptIdentity(string(passphrase))
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(bytes.NewReader(sealed), identity)
	if err != nil {
		return nil, errors.New("the key file could not be opened with that passphrase")
	}
	plain, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return nil, errors.New("the key file is damaged")
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	var f keyFile
	if strictjson.DecodeNonNull(plain, &f, "") != nil || f.Format != keyFormat || f.KeyType != "ed25519" {
		return nil, errors.New("not a Vult update key file")
	}
	seed, err := hex.DecodeString(f.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("the key file is damaged")
	}
	private := ed25519.NewKeyFromSeed(seed)
	for i := range seed {
		seed[i] = 0
	}
	key, id, err := PublicKey(private.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}
	sig, err := signature.LoadSigner(private, crypto.Hash(0))
	if err != nil {
		return nil, err
	}
	wipe := func() {
		for i := range private {
			private[i] = 0
		}
	}
	return &Signer{ID: id, Key: key, sig: sig, wipe: wipe}, nil
}

// WritePublicKey saves a public key for use when the repository is set up.
func WritePublicKey(path string, key *metadata.Key) error {
	data, err := json.MarshalIndent(key, "", "  ")
	if err != nil {
		return err
	}
	return writeNew(path, append(data, '\n'), 0o644)
}

// ReadPublicKey reads a public key: a key file's (written by WritePublicKey)
// or a Touch ID key's (PEM, ECDSA P-256).
func ReadPublicKey(path string) (*metadata.Key, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("-----BEGIN PUBLIC KEY-----")) {
		public, err := touchIDPublicKey(data)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", path, err)
		}
		key, err := metadata.KeyFromPublicKey(public)
		if err != nil {
			return nil, "", err
		}
		id, err := key.ID()
		return key, id, err
	}
	var key metadata.Key
	if err := json.Unmarshal(data, &key); err != nil || key.Type != metadata.KeyTypeEd25519 || key.Value.PublicKey == "" {
		return nil, "", fmt.Errorf("%s is not an Ed25519 public key", path)
	}
	id, err := key.ID()
	return &key, id, err
}

// TestKey proves a restored key file opens and signs, and that its signature
// verifies against its own public key. It changes nothing.
func TestKey(path string, passphrase []byte) (string, error) {
	s, err := OpenKey(path, passphrase)
	if err != nil {
		return "", err
	}
	defer s.Close()
	probe := metadata.Targets()
	signer, err := s.signer()
	if err != nil {
		return "", err
	}
	if _, err := probe.Sign(signer); err != nil {
		return "", err
	}
	root := metadata.Root()
	if err := root.Signed.AddKey(s.Key, metadata.TARGETS); err != nil {
		return "", err
	}
	if err := root.VerifyDelegate(metadata.TARGETS, probe); err != nil {
		return "", fmt.Errorf("the key signed but its signature does not verify: %w", err)
	}
	return s.ID, nil
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}
