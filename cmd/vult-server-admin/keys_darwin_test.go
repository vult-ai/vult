package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
)

func TestOfflineKeyCreationRejectsInheritedReadableACL(t *testing.T) {
	db := testutil.New(t)
	tenant, err := db.Owner.EnsureTenant(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	dsn, input := filepath.Join(directory, "operator.dsn"), filepath.Join(directory, "input.json")
	outputDirectory := filepath.Join(directory, "output")
	if err = os.Mkdir(outputDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(dsn, []byte(testutil.DSN(db.Admin)), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(input, []byte(`{"tenant_id":"`+tenant+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/chmod", "+a", "everyone allow read,file_inherit,directory_inherit", outputDirectory)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if err = command.Run(); err != nil {
		t.Fatal("synthetic inherited ACL setup failed", err)
	}
	output := filepath.Join(outputDirectory, "keys.json")
	var console bytes.Buffer
	if err = run(db.Context, []string{"document-key", "--dsn-file", dsn, "--input", input, "--output", output}, &console); err == nil {
		t.Fatal("new secret written into inherited readable ACL")
	}
	if _, err = os.Lstat(output); !os.IsNotExist(err) || console.Len() != 0 {
		t.Fatal("failed provisioning left output or console content", err)
	}
}
