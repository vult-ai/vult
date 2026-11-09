package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/loopback"
)

func TestLocalBrowserAddressesCannotOpenAnotherAuthority(t *testing.T) {
	origin := "https://vult.localhost:51999"
	setup := origin + "/auth/setup#setup=" + strings.Repeat("a", 43)
	if _, err := localBrowserAddress(origin, false); err != nil {
		t.Fatal(err)
	}
	if _, err := localBrowserAddress(setup, true); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"https://vult-0123456789abcdef0123456789abcdef.localhost:51999", "http://localhost:51999", "https://ledger.example.com:51999", "javascript:alert(1)", origin + "?token=secret", origin + "/other", origin + "#setup=secret", strings.Replace(origin, ":51999", ":443", 1), strings.Replace(origin, "https://", "https://user@", 1)} {
		if _, err := localBrowserAddress(raw, false); err == nil {
			t.Fatal("unsafe browser destination accepted")
		}
	}
	for _, raw := range []string{origin, setup + "&setup=" + strings.Repeat("b", 43), setup + "&return=https://other.example.com", strings.Replace(setup, "/auth/setup", "/other", 1), setup + "%0a", origin + "/auth/setup?setup=" + strings.Repeat("a", 43)} {
		if _, err := localBrowserAddress(raw, true); err == nil {
			t.Fatal("unsafe enrollment destination accepted")
		}
	}
}

func browserTestCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	cert, key, err := loopback.NewCertificate(loopback.Host)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := loopback.ParseCertificate(loopback.Host, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func TestBrowserReadinessPinsTheProtectedLeafWithoutSkippingTLSVerification(t *testing.T) {
	pair := browserTestCertificate(t)
	config, err := pinnedBrowserTLS(loopback.Host, pair.Leaf)
	if err != nil || config.InsecureSkipVerify || config.MinVersion != tls.VersionTLS13 || config.ServerName != loopback.Host || config.RootCAs == nil {
		t.Fatal("local readiness weakened normal HTTPS verification", err)
	}
	if err := config.VerifyConnection(tls.ConnectionState{PeerCertificates: []*x509.Certificate{pair.Leaf}}); err != nil {
		t.Fatal("exact protected leaf was rejected", err)
	}
	other := browserTestCertificate(t)
	for _, peers := range [][]*x509.Certificate{nil, {other.Leaf}, {pair.Leaf, pair.Leaf}} {
		if err := config.VerifyConnection(tls.ConnectionState{PeerCertificates: peers}); err == nil {
			t.Fatal("substituted or broadened certificate chain was accepted")
		}
	}
	if _, err := pinnedBrowserTLS("other.example.com", pair.Leaf); err == nil {
		t.Fatal("protected leaf trusted a different authority")
	}
	if _, err := pinnedBrowserTLS(loopback.Host, nil); err == nil {
		t.Fatal("missing protected leaf was accepted")
	}
}

func browserReadinessServer(t *testing.T, pair tls.Certificate, status int) *url.URL {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Method != http.MethodGet || r.TLS.ServerName != loopback.Host {
			t.Error("readiness request changed its route or authority")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
	}))
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}
	s.StartTLS()
	t.Cleanup(s.Close)
	_, port, err := net.SplitHostPort(s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	origin, err := localBrowserAddress("https://"+loopback.Host+":"+port, false)
	if err != nil {
		t.Fatal(err)
	}
	return origin
}

func TestBrowserReadinessRequiresTheExpectedHTTPSStatus(t *testing.T) {
	pair := browserTestCertificate(t)
	config, err := pinnedBrowserTLS(loopback.Host, pair.Leaf)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusNoContent, http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			origin := browserReadinessServer(t, pair, status)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			err := waitLocalBrowserHTTPS(ctx, origin, config, "/readyz")
			if (err == nil) != (status == http.StatusNoContent) {
				t.Fatalf("readiness response %d: %v", status, err)
			}
		})
	}
}

func TestBrowserReadinessRejectsAForeignCertificate(t *testing.T) {
	protected := browserTestCertificate(t)
	origin := browserReadinessServer(t, browserTestCertificate(t), http.StatusNoContent)
	config, err := pinnedBrowserTLS(loopback.Host, protected.Leaf)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = waitLocalBrowserHTTPS(ctx, origin, config, "/readyz")
	var certificateError *tls.CertificateVerificationError
	if !errors.As(err, &certificateError) {
		t.Fatalf("foreign certificate did not fail verification: %v", err)
	}
}

func TestBrowserReadinessStopsWaitingWhenTheServiceIsUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	origin, err := localBrowserAddress("https://"+loopback.Host+":"+port, false)
	if err != nil {
		t.Fatal(err)
	}
	config, err := pinnedBrowserTLS(loopback.Host, browserTestCertificate(t).Leaf)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := waitLocalBrowserHTTPS(ctx, origin, config, "/readyz"); err == nil || ctx.Err() == nil {
		t.Fatalf("unavailable service did not stop at the caller deadline: %v", err)
	}
}

func TestMacBrowserOpenStillUsesSystemTrust(t *testing.T) {
	if runtime.GOOS != "darwin" {
		return
	}
	config, err := localBrowserTLSConfig(loopback.Host)
	if err != nil || config.InsecureSkipVerify || config.RootCAs != nil || config.VerifyConnection != nil {
		t.Fatal("normal browser opening no longer requires system trust", err)
	}
}
