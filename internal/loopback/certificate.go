// Package loopback defines the installation-specific local browser identity.
package loopback

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"time"
)

var ErrCertificate = errors.New("local browser certificate unavailable")

// Host is the browser address of Vult on this Mac or Linux computer, served on
// the installation's own port. A name of its own, not plain localhost, keeps
// Vult's cookies and passkeys apart from every other local web app, because
// browsers share cookies across a hostname's ports.
const Host = "vult.localhost"

// ValidHostname reports whether host is Vult's local browser address.
func ValidHostname(host string) bool { return host == Host }

// InstallationID returns a new random identity for one installation. It names
// the installation's private database logins; it is not part of the address.
func InstallationID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", ErrCertificate
	}
	return hex.EncodeToString(b), nil
}

// ValidInstallationID reports whether id is an installation identity.
func ValidInstallationID(id string) bool {
	_, err := hex.DecodeString(id)
	return err == nil && len(id) == 32 && id == strings.ToLower(id)
}

// LegacyInstallationID returns the identity carried by an address from before
// 0.5.1, vult-<identity>.localhost, so an upgrade keeps the installation's
// database logins while moving it to Host.
func LegacyInstallationID(host string) (string, bool) {
	if !strings.HasPrefix(host, "vult-") || !strings.HasSuffix(host, ".localhost") {
		return "", false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(host, "vult-"), ".localhost")
	return id, ValidInstallationID(id)
}

// NewCertificate creates a self-signed server leaf for one local hostname. It
// has no CA or certificate-signing authority. Trust installation is separate.
func NewCertificate(host string) ([]byte, []byte, error) {
	if !ValidHostname(host) {
		return nil, nil, ErrCertificate
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, ErrCertificate
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, ErrCertificate
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(2, 0, 0), BasicConstraintsValid: true, IsCA: false, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, ErrCertificate
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, ErrCertificate
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), nil
}

func ParseCertificate(host string, certificate, key []byte) (tls.Certificate, error) {
	if !ValidHostname(host) || len(certificate) > 32768 || len(key) > 16384 {
		return tls.Certificate{}, ErrCertificate
	}
	block, rest := pem.Decode(certificate)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return tls.Certificate{}, ErrCertificate
	}
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil || len(pair.Certificate) != 1 {
		return tls.Certificate{}, ErrCertificate
	}
	leaf, err := ParsePublicCertificate(host, pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, ErrCertificate
	}
	private, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok || private.Curve != elliptic.P256() {
		return tls.Certificate{}, ErrCertificate
	}
	pair.Leaf = leaf
	return pair, nil
}

// ParsePublicCertificate validates the exact non-CA local leaf without reading
// its private key. It is suitable for certificate approval and pinned clients.
func ParsePublicCertificate(host string, der []byte) (*x509.Certificate, error) {
	if !ValidHostname(host) || len(der) > 32768 {
		return nil, ErrCertificate
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, ErrCertificate
	}
	key, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() || leaf.IsCA || !leaf.BasicConstraintsValid || leaf.KeyUsage != x509.KeyUsageDigitalSignature || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || len(leaf.UnknownExtKeyUsage) != 0 || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != host || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 || !bytes.Equal(leaf.RawIssuer, leaf.RawSubject) || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) || leaf.VerifyHostname(host) != nil || leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) != nil {
		return nil, ErrCertificate
	}
	return leaf, nil
}
