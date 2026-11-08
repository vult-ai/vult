package main

import (
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/loopback"
)

func TestLinuxBrowserOpenerNeverReceivesSetupAuthority(t *testing.T) {
	origin := "https://vult-0123456789abcdef0123456789abcdef.localhost:51999"
	code := strings.Repeat("a", 43)
	public, secret, err := publicBrowserDestination(origin + "/auth/setup#setup=" + code)
	if err != nil || public != origin+"/auth/setup" || secret != code || strings.Contains(public, code) || strings.ContainsAny(public, "?#") {
		t.Fatal("setup authority reached the public browser destination", err)
	}
	public, secret, err = publicBrowserDestination(origin)
	if err != nil || public != origin || secret != "" {
		t.Fatal("ordinary browser destination changed", err)
	}
	for _, input := range []string{origin + "/auth/setup", origin + "/auth/setup?setup=" + code, origin + "/auth/setup#setup=" + code + "&setup=" + code, "https://other.example.com:51999/auth/setup#setup=" + code} {
		if _, _, err := publicBrowserDestination(input); err == nil {
			t.Fatal("an unsafe browser launch was accepted")
		}
	}
}

func TestLinuxReadinessPinsTheProtectedLeafWithoutSkippingTLSVerification(t *testing.T) {
	host, err := loopback.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	cert, key, err := loopback.NewCertificate(host)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := loopback.ParseCertificate(host, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	config, err := pinnedBrowserTLS(host, pair.Leaf)
	if err != nil || config.InsecureSkipVerify || config.MinVersion != tls.VersionTLS13 || config.ServerName != host || config.RootCAs == nil {
		t.Fatal("local readiness weakened normal HTTPS verification", err)
	}
	if err := config.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{pair.Leaf}}); err != nil {
		t.Fatal("exact protected leaf was rejected", err)
	}
	otherCert, otherKey, err := loopback.NewCertificate(host)
	if err != nil {
		t.Fatal(err)
	}
	other, err := loopback.ParseCertificate(host, otherCert, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, peers := range [][]*x509.Certificate{nil, {other.Leaf}, {pair.Leaf, pair.Leaf}} {
		if err := config.VerifyConnection(tls.ConnectionState{PeerCertificates: peers}); err == nil {
			t.Fatal("substituted or broadened certificate chain was accepted")
		}
	}
	if _, err := pinnedBrowserTLS("other.example.com", pair.Leaf); err == nil {
		t.Fatal("protected leaf trusted a different authority")
	}
}
