// Package secretbox encrypts bank access secrets before they reach the
// database: provider keys, access tokens and setup links.
//
// Values are sealed with AES-256-GCM. The associated data binds each value to
// what it is and where it belongs (its purpose, tenant, Book and connection),
// so a sealed value copied to another row, Book or purpose does not open.
// Every sealed value names the key version that sealed it, so the key can be
// replaced while older values are still readable and then re-sealed.
//
// The key never lives in PostgreSQL. A Source keeps it somewhere else: the
// macOS Keychain on a Mac, or a private file outside the database volume on a
// server. Losing the key loses only bank access, which is restored by
// connecting the bank again; the Book itself is not encrypted with it.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strings"
)

var (
	// ErrSealed means a value could not be opened: it was changed, copied to
	// another binding, or sealed with a key this keyring doesn't hold.
	ErrSealed = errors.New("sealed value cannot be opened")
	// ErrUnknownKey means the keyring holds no key of the version a value names.
	ErrUnknownKey = errors.New("sealed with a key this keyring does not hold")
	// ErrInvalid means a binding or keyring is malformed.
	ErrInvalid = errors.New("invalid secret binding or keyring")
)

const (
	// KeySize is the AES-256 key length in bytes.
	KeySize   = 32
	format    = 1
	nonceSize = 12
	header    = 1 + 4 + nonceSize
	// MaxPlaintext bounds a single sealed value.
	MaxPlaintext = 64 << 10
)

// Binding is what a sealed value belongs to. Every field is part of the
// associated data; none of them is secret.
type Binding struct {
	// Purpose names the kind of value, such as "bank.access_token" or
	// "document.key". A value never opens under another purpose.
	Purpose      string
	TenantID     string
	BookID       string
	ConnectionID string
}

func (b Binding) valid() bool {
	if b.Purpose == "" || b.TenantID == "" || b.BookID == "" || b.ConnectionID == "" {
		return false
	}
	for _, s := range []string{b.Purpose, b.TenantID, b.BookID, b.ConnectionID} {
		if len(s) > 128 || strings.ContainsAny(s, "\x00\n") {
			return false
		}
	}
	return true
}

// associated is the exact associated data: a format tag, the key version and
// each binding field length-prefixed, so no two bindings share an encoding.
func (b Binding) associated(version uint32) []byte {
	out := []byte("vult-secretbox\x00")
	out = binary.BigEndian.AppendUint32(out, version)
	for _, s := range []string{b.Purpose, b.TenantID, b.BookID, b.ConnectionID} {
		out = binary.BigEndian.AppendUint16(out, uint16(len(s)))
		out = append(out, s...)
	}
	return out
}

// Seal encrypts plaintext with the keyring's current key.
func Seal(k *Keyring, b Binding, plaintext []byte) ([]byte, error) {
	if k == nil || !b.valid() || len(plaintext) > MaxPlaintext {
		return nil, ErrInvalid
	}
	version, key, err := k.current()
	if err != nil {
		return nil, err
	}
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, header, header+len(plaintext)+aead.Overhead())
	out[0] = format
	binary.BigEndian.PutUint32(out[1:5], version)
	if _, err := rand.Read(out[5:header]); err != nil {
		return nil, err
	}
	return aead.Seal(out, out[5:header], plaintext, b.associated(version)), nil
}

// Open decrypts a value sealed for exactly this binding.
func Open(k *Keyring, b Binding, sealed []byte) ([]byte, error) {
	if k == nil || !b.valid() {
		return nil, ErrInvalid
	}
	version, err := Version(sealed)
	if err != nil {
		return nil, err
	}
	key, ok := k.keys[version]
	if !ok {
		return nil, ErrUnknownKey
	}
	aead, err := gcm(key)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, sealed[5:header], sealed[header:], b.associated(version))
	if err != nil {
		return nil, ErrSealed
	}
	return plain, nil
}

// Reseal opens a value and seals it again with the current key. It returns
// the value unchanged when the current key already sealed it.
func Reseal(k *Keyring, b Binding, sealed []byte) ([]byte, bool, error) {
	version, err := Version(sealed)
	if err != nil {
		return nil, false, err
	}
	if current, _, err := k.current(); err != nil {
		return nil, false, err
	} else if version == current {
		if _, err := Open(k, b, sealed); err != nil {
			return nil, false, err
		}
		return sealed, false, nil
	}
	plain, err := Open(k, b, sealed)
	if err != nil {
		return nil, false, err
	}
	defer clear(plain)
	out, err := Seal(k, b, plain)
	return out, err == nil, err
}

// Version reads the key version a sealed value names, without opening it.
func Version(sealed []byte) (uint32, error) {
	if len(sealed) < header+16 || sealed[0] != format {
		return 0, ErrSealed
	}
	v := binary.BigEndian.Uint32(sealed[1:5])
	if v == 0 {
		return 0, ErrSealed
	}
	return v, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrInvalid
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithNonceSize(block, nonceSize)
}
