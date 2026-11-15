package secretbox

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"github.com/vult-ai/vult/internal/strictjson"
)

// Keyring holds every key version still needed to open stored values and
// names the one that seals new values.
type Keyring struct {
	currentVersion uint32
	keys           map[uint32][]byte
}

type keyringFile struct {
	Format  int               `json:"format"`
	Current uint32            `json:"current"`
	Keys    map[string]string `json:"keys"`
}

// NewKeyring creates a keyring with one fresh random key, version 1.
func NewKeyring() (*Keyring, error) {
	k := &Keyring{keys: map[uint32][]byte{}}
	if _, err := k.Rotate(); err != nil {
		return nil, err
	}
	return k, nil
}

// Current is the version that seals new values.
func (k *Keyring) Current() uint32 { return k.currentVersion }

// Versions lists the versions this keyring can open, oldest first.
func (k *Keyring) Versions() []uint32 {
	out := make([]uint32, 0, len(k.keys))
	for v := range k.keys {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (k *Keyring) current() (uint32, []byte, error) {
	key, ok := k.keys[k.currentVersion]
	if k.currentVersion == 0 || !ok {
		return 0, nil, ErrInvalid
	}
	return k.currentVersion, key, nil
}

// Rotate adds a fresh key with the next version and makes it current. Older
// keys stay until Retire removes them, so stored values still open.
func (k *Keyring) Rotate() (uint32, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return 0, err
	}
	next := uint32(1)
	for v := range k.keys {
		if v >= next {
			next = v + 1
		}
	}
	k.keys[next], k.currentVersion = key, next
	return next, nil
}

// Retire removes an old key once nothing is sealed with it any more. The
// current key can't be retired.
func (k *Keyring) Retire(version uint32) error {
	if version == k.currentVersion {
		return errors.New("the current key can't be retired")
	}
	if key, ok := k.keys[version]; ok {
		clear(key)
		delete(k.keys, version)
	}
	return nil
}

// MarshalKeyring encodes the keyring for its Source. The result is secret.
func MarshalKeyring(k *Keyring) ([]byte, error) {
	if _, _, err := k.current(); err != nil {
		return nil, err
	}
	f := keyringFile{Format: 1, Current: k.currentVersion, Keys: map[string]string{}}
	for v, key := range k.keys {
		f.Keys[strconv.FormatUint(uint64(v), 10)] = base64.StdEncoding.EncodeToString(key)
	}
	return json.Marshal(f)
}

// ParseKeyring decodes a keyring a Source stored.
func ParseKeyring(b []byte) (*Keyring, error) {
	var f keyringFile
	if strictjson.Decode(b, &f) != nil || f.Format != 1 || f.Current == 0 || len(f.Keys) == 0 || len(f.Keys) > 64 {
		return nil, ErrInvalid
	}
	k := &Keyring{currentVersion: f.Current, keys: map[uint32][]byte{}}
	for name, encoded := range f.Keys {
		v, err := strconv.ParseUint(name, 10, 32)
		if err != nil || v == 0 || strconv.FormatUint(v, 10) != name {
			return nil, ErrInvalid
		}
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(key) != KeySize {
			return nil, ErrInvalid
		}
		k.keys[uint32(v)] = key
	}
	if _, _, err := k.current(); err != nil {
		return nil, err
	}
	return k, nil
}
