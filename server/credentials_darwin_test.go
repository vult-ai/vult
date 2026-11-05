package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPrivateCredentialReaderRejectsReadableACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential.key")
	if err := os.WriteFile(path, []byte("synthetic private credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(path, 128); err != nil {
		t.Fatal("positive credential control failed", err)
	}
	command := exec.Command("/bin/chmod", "+a", "everyone allow read", path)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateFile(path, 128); err != ErrConfiguration {
		t.Fatal("ACL-readable private credential accepted", err)
	}
}
