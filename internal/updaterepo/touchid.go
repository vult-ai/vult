package updaterepo

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// TouchIDProgram is the helper that signs with the Secure Enclave key.
const TouchIDProgram = "vult-release-touchid"

func touchIDPublicKey(pemData []byte) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("not a PEM public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	public, ok := parsed.(*ecdsa.PublicKey)
	if !ok || public.Curve != elliptic.P256() {
		return nil, errors.New("not an ECDSA P-256 public key")
	}
	return public, nil
}

// touchIDSigner signs through the helper program. Every signature it returns
// has been checked against the key's public half first, so a misbehaving
// helper can't make vult-release attach a signature that doesn't verify.
type touchIDSigner struct {
	public *ecdsa.PublicKey
	sign   func(message []byte) ([]byte, error)
}

func (t touchIDSigner) PublicKey(...signature.PublicKeyOption) (crypto.PublicKey, error) {
	return t.public, nil
}

func (t touchIDSigner) SignMessage(message io.Reader, _ ...signature.SignOption) ([]byte, error) {
	msg, err := io.ReadAll(io.LimitReader(message, 4<<20))
	if err != nil {
		return nil, err
	}
	sig, err := t.sign(msg)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(msg)
	if !ecdsa.VerifyASN1(t.public, digest[:], sig) {
		return nil, errors.New("the Touch ID key returned a signature that does not verify")
	}
	return sig, nil
}

// OpenTouchID prepares the Secure Enclave key whose handle is keyFile (with
// its public key at keyFile.pub). Each signature asks for Touch ID, showing
// reason.
func OpenTouchID(program, keyFile, reason string) (*Signer, error) {
	pemData, err := os.ReadFile(keyFile + ".pub")
	if err != nil {
		return nil, err
	}
	public, err := touchIDPublicKey(pemData)
	if err != nil {
		return nil, err
	}
	run := func(message []byte) ([]byte, error) {
		cmd := exec.Command(program, "sign", keyFile, reason)
		cmd.Stdin = bytes.NewReader(message)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("Touch ID signing did not complete: %s", strings.TrimSpace(errOut.String()))
		}
		return base64.StdEncoding.DecodeString(strings.TrimSpace(out.String()))
	}
	return newTouchIDSigner(public, run)
}

func newTouchIDSigner(public *ecdsa.PublicKey, sign func([]byte) ([]byte, error)) (*Signer, error) {
	key, err := metadata.KeyFromPublicKey(public)
	if err != nil {
		return nil, err
	}
	id, err := key.ID()
	if err != nil {
		return nil, err
	}
	return &Signer{ID: id, Key: key, sig: touchIDSigner{public: public, sign: sign}}, nil
}
