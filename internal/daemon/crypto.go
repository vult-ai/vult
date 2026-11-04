package daemon

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"filippo.io/age"
	"io"
)

func secret() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func encryptBackup(plain []byte, key string) ([]byte, error) {
	if len(key) < 32 || len(plain) > 64<<20 {
		return nil, errors.New("backup exceeds supported bounds")
	}
	recipient, e := age.NewScryptRecipient(key)
	if e != nil {
		return nil, e
	}
	recipient.SetWorkFactor(18)
	var out bytes.Buffer
	w, e := age.Encrypt(&out, recipient)
	if e != nil {
		return nil, e
	}
	if _, e = w.Write(plain); e != nil {
		return nil, e
	}
	if e = w.Close(); e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}
func decryptBackup(ciphertext []byte, key string) ([]byte, error) {
	if len(key) < 32 || len(ciphertext) > 66<<20 {
		return nil, errors.New("invalid encrypted backup")
	}
	identity, e := age.NewScryptIdentity(key)
	if e != nil {
		return nil, e
	}
	identity.SetMaxWorkFactor(18)
	r, e := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if e != nil {
		return nil, errors.New("backup authentication failed")
	}
	b, e := io.ReadAll(io.LimitReader(r, (64<<20)+1))
	if e != nil || len(b) > 64<<20 {
		return nil, errors.New("backup authentication failed")
	}
	return b, nil
}
