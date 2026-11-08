package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNSSDatabaseCreatesOnlyAnEmptySelectedProfile(t *testing.T) {
	home := t.TempDir()
	path, exists, err := nssDatabase(home, false)
	if err != nil || exists || path != filepath.Join(home, ".local/share/pki/nssdb") {
		t.Fatal("new profile selection changed", path, exists, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("read-only selection created files", err)
	}
	path, exists, err = nssDatabase(home, true)
	if err != nil || exists {
		t.Fatal(err)
	}
	for _, name := range []string{"cert9.db", "key4.db"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte("synthetic database"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	selected, exists, err := nssDatabase(home, true)
	if err != nil || !exists || selected != path {
		t.Fatal("existing profile was reinitialized", err)
	}
	if string(fixtureBytes(t, filepath.Join(path, "key4.db"))) != "synthetic database" {
		t.Fatal("existing profile contents changed")
	}
	legacy := filepath.Join(home, ".pki/nssdb")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cert9.db", "key4.db"} {
		if err := os.WriteFile(filepath.Join(legacy, name), []byte("synthetic legacy database"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	selected, exists, err = nssDatabase(home, false)
	if err != nil || !exists || selected != legacy {
		t.Fatal("the existing legacy Chrome database lost precedence", err)
	}
}

func TestNSSDatabaseRefusesUnsafeOrIncompleteProfiles(t *testing.T) {
	for _, kind := range []string{"unknown-data", "shared-key", "linked-key", "symlink-cert", "shared-directory"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			path, _, err := nssDatabase(home, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"cert9.db", "key4.db"} {
				if err := os.WriteFile(filepath.Join(path, name), []byte("retained synthetic database"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "unknown-data":
				if err := os.Remove(filepath.Join(path, "cert9.db")); err != nil {
					t.Fatal(err)
				}
			case "shared-key":
				if err := os.Chmod(filepath.Join(path, "key4.db"), 0644); err != nil {
					t.Fatal(err)
				}
			case "linked-key":
				if err := os.Link(filepath.Join(path, "key4.db"), filepath.Join(home, "other-key")); err != nil {
					t.Fatal(err)
				}
			case "symlink-cert":
				if err := os.Rename(filepath.Join(path, "cert9.db"), filepath.Join(home, "cert")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(home, "cert"), filepath.Join(path, "cert9.db")); err != nil {
					t.Fatal(err)
				}
			case "shared-directory":
				if err := os.Chmod(path, 0777); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := nssDatabase(home, true); err == nil {
				t.Fatal("unsafe or incomplete profile was accepted")
			}
			if string(fixtureBytes(t, filepath.Join(path, "key4.db"))) != "retained synthetic database" {
				t.Fatal("failure replaced an existing key database")
			}
		})
	}
}

func TestNSSTrustRequiresOnlyServerPeerAuthority(t *testing.T) {
	text := "Certificate:\n    Certificate Trust Flags:\n        SSL Flags:\n            Terminal Record\n            Trusted\n        Email Flags:\n        Object Signing Flags:\n"
	if !nssPeerTrust([]byte(text)) {
		t.Fatal("exact P,, server trust was rejected")
	}
	for _, bad := range []string{"", strings.Replace(text, "Terminal Record", "Valid CA", 1), strings.Replace(text, "Email Flags:", "Email Flags:\n            Trusted", 1), text + "Certificate Distrust Dates:\n", strings.Replace(text, "Trusted", "Trusted CA", 1)} {
		if nssPeerTrust([]byte(bad)) {
			t.Fatal("broader or unknown certificate trust was accepted")
		}
	}
}
