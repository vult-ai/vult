package documents

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyDirectoryProvisionsExplicitlyAndRetainsExactKeys(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	tenant := "11111111-1111-4111-8111-111111111111"
	keys, err := OpenKeyDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = keys.Current(ctx, tenant); err != ErrUnavailable {
		t.Fatal("missing key silently generated", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("read created a key", err)
	}
	if err = keys.Provision(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, keyObject(tenant)))
	if err != nil {
		t.Fatal(err)
	}
	if err = keys.Provision(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if err = keys.Close(); err != nil {
		t.Fatal(err)
	}
	keys, err = OpenKeyDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer keys.Close()
	if _, err = keys.Identity(ctx, tenant, "primary"); err != nil {
		t.Fatal("restart lost key", err)
	}
	after, err := os.ReadFile(filepath.Join(root, keyObject(tenant)))
	if err != nil || string(before) != string(after) {
		t.Fatal("provision replaced existing key", err)
	}
	if err = os.WriteFile(filepath.Join(root, keyObject(tenant)), []byte("invalid key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = keys.Provision(ctx, tenant); err != ErrUnavailable {
		t.Fatal("corrupt key was replaced", err)
	}
	after, err = os.ReadFile(filepath.Join(root, keyObject(tenant)))
	if err != nil || string(after) != "invalid key" {
		t.Fatal("unreadable key changed", err)
	}
}
