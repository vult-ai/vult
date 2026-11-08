package daemon

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/vult-ai/vult/internal/fssecurity"
	"github.com/vult-ai/vult/internal/loopback"
	"github.com/vult-ai/vult/internal/strictjson"
)

func browserTrustRecord(c browserConfiguration) ([]byte, error) {
	if !c.valid() {
		return nil, errors.New("local browser certificate is unavailable")
	}
	block, _ := pem.Decode([]byte(c.Certificate))
	return json.Marshal(struct {
		Format         string `json:"format"`
		Hostname       string `json:"hostname"`
		CertificateDER []byte `json:"certificate_der"`
	}{"vult-browser-trust-v1", c.Hostname, block.Bytes})
}

func readPublicBrowserCertificate(path string, uid uint32) (*x509.Certificate, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, loopback.ErrCertificate
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, loopback.ErrCertificate
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uid || stat.Nlink != 1 || info.Mode().Perm()&0022 != 0 || info.Size() > 65536 || !fssecurity.NoExtendedACL(file) {
		return nil, loopback.ErrCertificate
	}
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 {
		return nil, loopback.ErrCertificate
	}
	var record struct {
		Format         string `json:"format"`
		Hostname       string `json:"hostname"`
		CertificateDER []byte `json:"certificate_der"`
	}
	if strictjson.Decode(data, &record) != nil || record.Format != "vult-browser-trust-v1" {
		return nil, loopback.ErrCertificate
	}
	return loopback.ParsePublicCertificate(record.Hostname, record.CertificateDER)
}

func installedBrowserCertificate() (*x509.Certificate, error) {
	for _, parent := range dataParents() {
		if err := protected(parent, 0, true); err != nil {
			return nil, loopback.ErrCertificate
		}
	}
	return readPublicBrowserCertificate(browserTrustIdentity, 0)
}

// BrowserCertificate reads only the protected public leaf for this hostname.
// It never imports trust or exposes the browser's private credentials.
func BrowserCertificate(host string) (*x509.Certificate, error) {
	leaf, err := installedBrowserCertificate()
	if err != nil || leaf.DNSNames[0] != host {
		return nil, loopback.ErrCertificate
	}
	return leaf, nil
}
