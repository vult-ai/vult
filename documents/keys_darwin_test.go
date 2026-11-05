package documents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func syntheticReadACL(t *testing.T, path string) {
	t.Helper()
	command := exec.Command("/bin/chmod", "+a", "everyone allow read", path)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if err := command.Run(); err != nil {
		t.Fatal("synthetic read ACL setup failed", err)
	}
}

func TestKeyFilesAndDirectoriesRejectReadableExtendedACL(t *testing.T) {
	tenant := "11111111-1111-4111-8111-111111111111"
	data, err := NewKeyFile(tenant)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "keys.json")
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = OpenKeyring(file); err != nil {
		t.Fatal("positive private key control failed", err)
	}
	syntheticReadACL(t, file)
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("ACL control changed private mode", err)
	}
	if _, err = OpenKeyring(file); err != ErrUnavailable {
		t.Fatal("ACL-readable key file accepted", err)
	}
	directory := t.TempDir()
	if err = os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	keys, err := OpenKeyDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = keys.Provision(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	syntheticReadACL(t, filepath.Join(directory, keyObject(tenant)))
	if _, err = keys.Identity(context.Background(), tenant, "primary"); err != ErrUnavailable {
		t.Fatal("key directory released an ACL-readable secret", err)
	}
	syntheticReadACL(t, directory)
	if err = keys.Provision(context.Background(), "22222222-2222-4222-8222-222222222222"); err != ErrUnavailable {
		t.Fatal("shared key directory provisioned a new secret", err)
	}
	keys.Close()
	if keys, err = OpenKeyDirectory(directory); err != ErrUnavailable {
		if keys != nil {
			keys.Close()
		}
		t.Fatal("ACL-shared key directory accepted", err)
	}
}
