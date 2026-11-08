package server

import (
	"crypto/tls"
	"net/url"
	"path/filepath"

	"github.com/vult-ai/vult/internal/loopback"
)

func localBrowserTLS(c Config) (*tls.Config, error) {
	if !c.LocalBrowser {
		return nil, nil
	}
	if !filepath.IsAbs(c.LocalCertificateFile) || !filepath.IsAbs(c.LocalKeyFile) {
		return nil, ErrConfiguration
	}
	certificate, err := ReadPrivateFile(c.LocalCertificateFile, 32768)
	if err != nil {
		return nil, ErrConfiguration
	}
	key, err := ReadPrivateFile(c.LocalKeyFile, 16384)
	if err != nil {
		return nil, ErrConfiguration
	}
	defer clear(key)
	origin, err := url.Parse(c.Origin)
	if err != nil {
		return nil, ErrConfiguration
	}
	pair, err := loopback.ParseCertificate(origin.Hostname(), certificate, key)
	if err != nil {
		return nil, ErrConfiguration
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, NextProtos: []string{"http/1.1"}}, nil
}
