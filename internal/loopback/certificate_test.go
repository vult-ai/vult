package loopback

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestLocalCertificateHasOneHostnameAndNoSigningAuthority(t *testing.T) {
	host := Host
	if !ValidHostname(host) {
		t.Fatal("local hostname is not valid")
	}
	certificate, key, err := NewCertificate(host)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := ParseCertificate(host, certificate, key)
	if err != nil {
		t.Fatal(err)
	}
	if pair.Leaf.IsCA || pair.Leaf.KeyUsage&x509.KeyUsageCertSign != 0 || len(pair.Leaf.DNSNames) != 1 || pair.Leaf.DNSNames[0] != host {
		t.Fatal("local server leaf has wider authority")
	}
	if public, err := ParsePublicCertificate(host, pair.Leaf.Raw); err != nil || !public.Equal(pair.Leaf) {
		t.Fatal("public-only validation changed the local leaf", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(pair.Leaf)
	if _, err := pair.Leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: roots}); err != nil {
		t.Fatal("explicit local leaf trust did not verify", err)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "other.example.com"}, DNSNames: []string{"other.example.com"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	derived, err := x509.CreateCertificate(rand.Reader, template, pair.Leaf, &other.PublicKey, pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	child, err := x509.ParseCertificate(derived)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Verify(x509.VerifyOptions{DNSName: "other.example.com", Roots: roots}); err == nil {
		t.Fatal("trusting the local leaf authorized another hostname")
	}
}

func TestLocalCertificateRejectsBroaderExpiredAndMismatchedInputs(t *testing.T) {
	host := Host
	certificate, key, err := NewCertificate(host)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := ParseCertificate(host, certificate, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name  string
		alter func(*x509.Certificate)
	}{
		{"ca", func(c *x509.Certificate) { c.IsCA = true; c.KeyUsage |= x509.KeyUsageCertSign }},
		{"extra-host", func(c *x509.Certificate) { c.DNSNames = append(c.DNSNames, "other.example.com") }},
		{"client-use", func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} }},
		{"expired", func(c *x509.Certificate) {
			c.NotBefore = time.Now().Add(-2 * time.Hour)
			c.NotAfter = time.Now().Add(-time.Hour)
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			template := *pair.Leaf
			change.alter(&template)
			der, err := x509.CreateCertificate(rand.Reader, &template, &template, pair.Leaf.PublicKey, pair.PrivateKey)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseCertificate(host, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key); !errors.Is(err, ErrCertificate) {
				t.Fatal("unsafe certificate accepted", err)
			}
			if _, err := ParsePublicCertificate(host, der); !errors.Is(err, ErrCertificate) {
				t.Fatal("unsafe public leaf was accepted", err)
			}
		})
	}
	// Addresses from before 0.5.1 are no longer served.
	otherHost := "vult-0123456789abcdef0123456789abcdef.localhost"
	_, otherKey, err := NewCertificate(Host)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		host             string
		certificate, key []byte
	}{{otherHost, certificate, key}, {host, certificate, otherKey}, {host, append(append([]byte{}, certificate...), certificate...), key}, {host, []byte("invalid"), key}} {
		if _, err := ParseCertificate(input.host, input.certificate, input.key); !errors.Is(err, ErrCertificate) {
			t.Fatal("mismatched local identity accepted", err)
		}
	}
	for _, host := range []string{"localhost", "vult-0123456789abcdef0123456789abcdef.localhost", "vult-short.localhost", "vult-0123456789abcdef0123456789abcdef.localhost.example.com", "vult-0123456789ABCDEF0123456789abcdef.localhost", "vult-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz.localhost", "*.localhost"} {
		if ValidHostname(host) {
			t.Fatal("non-installation host accepted", host)
		}
	}
}

// Installations keep a random identity apart from the shared address; addresses
// from before 0.5.1 still yield theirs so an upgrade keeps its database logins.
func TestInstallationIdentityIsSeparateFromTheAddress(t *testing.T) {
	id, err := InstallationID()
	if err != nil || !ValidInstallationID(id) || ValidHostname(id) {
		t.Fatal("installation identity unavailable", err)
	}
	if other, _ := InstallationID(); other == id {
		t.Fatal("installation identities repeat")
	}
	if got, ok := LegacyInstallationID("vult-" + id + ".localhost"); !ok || got != id {
		t.Fatal("an old address lost its installation identity")
	}
	for _, host := range []string{Host, "localhost", "vult-short.localhost", "vult-" + id + ".localhost.example.com", "vult-" + strings.ToUpper(id) + ".localhost"} {
		if _, ok := LegacyInstallationID(host); ok {
			t.Fatal("not an old installation address", host)
		}
	}
}
