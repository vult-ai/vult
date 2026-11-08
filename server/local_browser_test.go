package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/loopback"
)

func localBrowserConfig(t *testing.T, listen string) (Config, []byte) {
	t.Helper()
	host, err := loopback.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	certificate, key, err := loopback.NewCertificate(host)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certFile, keyFile := filepath.Join(directory, "browser.crt"), filepath.Join(directory, "browser.key")
	if os.WriteFile(certFile, certificate, 0600) != nil || os.WriteFile(keyFile, key, 0600) != nil {
		t.Fatal("local certificate fixture unavailable")
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		t.Fatal(err)
	}
	return Config{Origin: "https://" + host + ":" + port, Listen: listen, LocalBrowser: true, LocalCertificateFile: certFile, LocalKeyFile: keyFile, Authentication: &Authentication{Mode: "passkey", Username: "example-owner"}}, certificate
}

func TestLocalBrowserTLSIngressAndDisabledAgentRoutes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	config, certificate := localBrowserConfig(t, listener.Addr().String())
	s, err := New(config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if s.TLSConfig == nil || s.TLSConfig.MinVersion != tls.VersionTLS13 {
		t.Fatal("local browser permits a plaintext listener")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, s, listener) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error("local listener cleanup failed", err)
		}
	}()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		t.Fatal("local trust fixture invalid")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	for _, path := range []string{"/healthz", "/mcp", "/oauth/token", "/.well-known/oauth-authorization-server", "/owner/tenants/example/books", "/identity/options"} {
		response, err := client.Get(config.Origin + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		want := http.StatusNotFound
		if path == "/healthz" {
			want = http.StatusNoContent
		}
		if response.StatusCode != want || response.Header.Get("Access-Control-Allow-Origin") != "" || len(response.Cookies()) != 0 {
			t.Fatal("local browser enabled a remote agent or identity route", path, response.StatusCode)
		}
	}
	for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Origin"} {
		r, err := http.NewRequest(http.MethodGet, config.Origin+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set(header, "https://other.example.com")
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatal("local listener accepted forwarded or foreign authority", header, response.StatusCode)
		}
	}
	plain, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	plain.SetDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(plain, "GET /healthz HTTP/1.1\r\nHost: "+strings.TrimPrefix(config.Origin, "https://")+"\r\nConnection: close\r\n\r\n")
	data, _ := io.ReadAll(io.LimitReader(plain, 4096))
	if strings.Contains(string(data), "204 No Content") || strings.Contains(string(data), "Set-Cookie") {
		t.Fatal("local browser served plaintext HTTP")
	}
}

func TestLocalBrowserRejectsNetworkBindingsAndCertificateConfusion(t *testing.T) {
	config, _ := localBrowserConfig(t, "127.0.0.1:51999")
	for _, change := range []struct {
		name  string
		apply func(*Config)
	}{
		{"public-bind", func(c *Config) { c.Listen = "0.0.0.0:51999" }},
		{"private-bind", func(c *Config) { c.Listen = "10.0.0.1:51999" }},
		{"ipv6-any", func(c *Config) { c.Listen = "[::]:51999"; c.PrivateIngress = true }},
		{"proxy", func(c *Config) { c.TrustedProxies = []string{"127.0.0.1/32"} }},
		{"bare-localhost", func(c *Config) { c.Origin = "https://localhost:51999" }},
		{"remote-host", func(c *Config) { c.Origin = "https://ledger.example.com:51999" }},
		{"port-mismatch", func(c *Config) { c.Listen = "127.0.0.1:51998" }},
		{"no-key", func(c *Config) { c.LocalKeyFile = "" }},
		{"no-passkey", func(c *Config) { c.Authentication = &Authentication{Mode: "builtin"} }},
		{"external-identity", func(c *Config) { c.Authentication = &Authentication{Mode: "external"} }},
		{"oidc", func(c *Config) { c.Authentication = &Authentication{Mode: "oidc"} }},
	} {
		t.Run(change.name, func(t *testing.T) {
			c := config
			change.apply(&c)
			if _, err := New(c, Options{}); !errors.Is(err, ErrConfiguration) {
				t.Fatal("unsafe local browser configuration accepted", err)
			}
		})
	}
	s, err := New(config, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.Host = strings.TrimPrefix(config.Origin, "https://")
	r.RemoteAddr = "127.0.0.1:12345"
	r.TLS = &tls.ConnectionState{HandshakeComplete: true, ServerName: strings.Split(r.Host, ":")[0]}
	valid := httptest.NewRecorder()
	s.Handler.ServeHTTP(valid, r)
	if valid.Code != http.StatusNoContent {
		t.Fatal("local transport fixture is not valid", valid.Code)
	}
	for _, variant := range []string{"no-tls", "wrong-sni", "foreign-peer", "wrong-host"} {
		request := r.Clone(context.Background())
		request.TLS = &tls.ConnectionState{HandshakeComplete: true, ServerName: strings.Split(r.Host, ":")[0]}
		switch variant {
		case "no-tls":
			request.TLS = nil
		case "wrong-sni":
			request.TLS.ServerName = "other.localhost"
		case "foreign-peer":
			request.RemoteAddr = "198.51.100.2:12345"
		case "wrong-host":
			request.Host = "other.localhost:51999"
		}
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, request)
		if w.Code != http.StatusForbidden {
			t.Fatal("local transport guard accepted another peer or origin", variant, w.Code)
		}
	}
	wide, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	_, actualPort, err := net.SplitHostPort(wide.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	s.Addr = "127.0.0.1:" + actualPort
	if err := Serve(context.Background(), s, wide); !errors.Is(err, ErrConfiguration) {
		t.Fatal("actual non-loopback listener accepted", err)
	}
	if err := os.Chmod(config.LocalKeyFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config, Options{}); !errors.Is(err, ErrConfiguration) {
		t.Fatal("publicly readable TLS key accepted", err)
	}
}
