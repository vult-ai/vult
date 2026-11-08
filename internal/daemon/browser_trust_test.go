package daemon

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserTrustIdentityContainsOnlyThePublicLeaf(t *testing.T) {
	c, err := newBrowserConfiguration(51999)
	if err != nil {
		t.Fatal(err)
	}
	data, err := browserTrustRecord(c)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Format         string `json:"format"`
		Hostname       string `json:"hostname"`
		CertificateDER []byte `json:"certificate_der"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil || len(keys) != 3 {
		t.Fatal("unexpected public identity fields", err)
	}
	if record.Format != "vult-browser-trust-v1" || record.Hostname != c.Hostname {
		t.Fatal("public identity changed its authority")
	}
	leaf, err := x509.ParseCertificate(record.CertificateDER)
	if err != nil || leaf.IsCA || leaf.VerifyHostname(c.Hostname) != nil || len(leaf.DNSNames) != 1 {
		t.Fatal("public identity is not the exact local server leaf", err)
	}
	for _, secret := range []string{c.Key, c.AuthPassword, c.OwnerPassword} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("public identity disclosed a credential")
		}
	}
	for _, mode := range []os.FileMode{0400, 0644} {
		path := filepath.Join(t.TempDir(), "identity.json")
		if err := writeBrowserFileMode(path, data, uint32(os.Geteuid()), uint32(os.Getegid()), mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("incorrect identity file permissions", err)
		}
		leaf, err := readPublicBrowserCertificate(path, uint32(os.Geteuid()))
		if err != nil || leaf.DNSNames[0] != c.Hostname {
			t.Fatal("protected public certificate could not be read", err)
		}
	}
}

func TestPublicBrowserIdentityRejectsUnsafeFilesAndSubstitutions(t *testing.T) {
	c, err := newBrowserConfiguration(51999)
	if err != nil {
		t.Fatal(err)
	}
	data, err := browserTrustRecord(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"shared-write", "symlink", "hardlink", "foreign-owner", "unknown-field", "foreign-host", "wrong-format", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "identity.json")
			if err := os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}
			uid := uint32(os.Geteuid())
			switch kind {
			case "shared-write":
				if err := os.Chmod(path, 0664); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".target", path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".copy"); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				uid++
			case "unknown-field", "foreign-host", "wrong-format":
				var record map[string]json.RawMessage
				if err := json.Unmarshal(data, &record); err != nil {
					t.Fatal(err)
				}
				if kind == "unknown-field" {
					record["private_key"] = json.RawMessage(`"not permitted"`)
				}
				if kind == "foreign-host" {
					record["hostname"] = json.RawMessage(`"other.example.com"`)
				}
				if kind == "wrong-format" {
					record["format"] = json.RawMessage(`"unknown"`)
				}
				changed, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, changed, 0644); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.WriteFile(path, data[:len(data)/2], 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readPublicBrowserCertificate(path, uid); err == nil {
				t.Fatal("unsafe public identity was accepted")
			}
		})
	}
}

func TestBrowserTrustIdentityRejectsInvalidPrivateConfiguration(t *testing.T) {
	c, err := newBrowserConfiguration(51999)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*browserConfiguration){
		func(c *browserConfiguration) { c.Hostname = "other.example.com" },
		func(c *browserConfiguration) { c.Certificate = c.Key },
		func(c *browserConfiguration) { c.Key = "" },
	} {
		bad := c
		change(&bad)
		if _, err := browserTrustRecord(bad); err == nil {
			t.Fatal("invalid identity was published for trust approval")
		}
	}
}
