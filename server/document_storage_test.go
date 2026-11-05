package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vult-ai/vult/documents"
)

func TestDiskDocumentConfigurationFailsClosedWithoutKeys(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	keys := filepath.Join(t.TempDir(), "keys.json")
	config := &DocumentStorage{Directory: root, KeyFile: keys}
	if vault, closer, err := OpenDocuments(config); err != ErrConfiguration || vault != nil || closer != nil {
		t.Fatal("missing keys silently initialized storage")
	}
	data, err := documents.NewKeyFile("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keys, data, 0600); err != nil {
		t.Fatal(err)
	}
	vault, closer, err := OpenDocuments(config)
	if err != nil || vault == nil || closer == nil {
		t.Fatal("private document storage unavailable", err)
	}
	closer.Close()
	if err = os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if vault, closer, err := OpenDocuments(config); err != ErrConfiguration || vault != nil || closer != nil {
		t.Fatal("shared document directory accepted")
	}
}

func TestServerKeyDirectoryLookupNeverProvisionsKeys(t *testing.T) {
	objects, keys := t.TempDir(), t.TempDir()
	for _, path := range []string{objects, keys} {
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	config := &DocumentStorage{Directory: objects, KeyDirectory: keys}
	vault, closer, err := OpenDocuments(config)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	if _, err = vault.Keys.Current(context.Background(), "11111111-1111-4111-8111-111111111111"); err != documents.ErrUnavailable {
		t.Fatal("runtime lookup silently provisioned keys", err)
	}
	if entries, err := os.ReadDir(keys); err != nil || len(entries) != 0 {
		t.Fatal("runtime created key files", err)
	}
	config.KeyFile = filepath.Join(keys, "keys.json")
	invalidVault, invalidCloser, err := OpenDocuments(config)
	if invalidCloser != nil {
		defer invalidCloser.Close()
	}
	if err != ErrConfiguration || invalidVault != nil || invalidCloser != nil {
		t.Fatal("ambiguous key providers accepted")
	}
}
