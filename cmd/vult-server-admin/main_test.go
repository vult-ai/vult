package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestOfflineStatusBrokenPipeCannotTerminateCompletedCommand(t *testing.T) {
	if os.Getenv("VULT_TEST_STATUS_PIPE") == "1" {
		os.Args = []string{"vult-server-admin", "--help"}
		main()
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	read.Close()
	defer write.Close()
	child := exec.Command(executable, "-test.run=^TestOfflineStatusBrokenPipeCannotTerminateCompletedCommand$")
	child.Env = append(os.Environ(), "VULT_TEST_STATUS_PIPE=1")
	child.Stdout = write
	var diagnostics bytes.Buffer
	child.Stderr = &diagnostics
	if err = child.Run(); err != nil {
		t.Fatal("offline status pipe terminated the command", err, diagnostics.String())
	}
}

func TestOfflineSetupKeepsSecretsPrivateAndDoesNotOverwrite(t *testing.T) {
	db := testutil.New(t)
	directory := t.TempDir()
	dsn := filepath.Join(directory, "operator.dsn")
	input := filepath.Join(directory, "input.json")
	output := filepath.Join(directory, "result.json")
	key := filepath.Join(directory, "credential.key")
	if e := os.WriteFile(dsn, []byte(testutil.DSN(db.Admin)), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600); e != nil {
		t.Fatal(e)
	}
	q := request{Username: "example", Passphrase: "Example horse battery staple 7428", KeyFile: key}
	encoded, _ := json.Marshal(q)
	if e := os.WriteFile(input, encoded, 0600); e != nil {
		t.Fatal(e)
	}
	var stdout bytes.Buffer
	args := []string{"init", "--dsn-file", dsn, "--input", input, "--output", output}
	if e := run(db.Context, args, &stdout); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(output)
	if e != nil {
		t.Fatal(e)
	}
	var enrollment access.Enrollment
	if json.Unmarshal(b, &enrollment) != nil || !ledger.ValidID(enrollment.TenantID) || len(enrollment.RecoveryCodes) != 8 {
		t.Fatal("enrollment output incomplete")
	}
	u, e := url.Parse(enrollment.AuthenticatorURI)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = totp.GenerateCode(u.Query().Get("secret"), time.Now()); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(output)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("enrollment file permissions unsafe")
	}
	if strings.Contains(stdout.String(), enrollment.RecoveryCodes[0]) || strings.Contains(stdout.String(), q.Passphrase) || strings.Contains(stdout.String(), u.Query().Get("secret")) {
		t.Fatal("enrollment secrets reached console")
	}
	if run(db.Context, args, &stdout) == nil {
		t.Fatal("previous enrollment output overwritten")
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(b, after) {
		t.Fatal("existing output changed")
	}
}

func TestOfflineCommandRejectsInvalidInputWithoutNetwork(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"init"}, {"migrate", "--dsn-file", "missing", "--input", "missing", "--output", "missing"}} {
		if run(context.Background(), args, &bytes.Buffer{}) == nil {
			t.Fatal("invalid command accepted")
		}
	}
}

func TestOfflineDocumentKeyIsPrivateAndNeverOverwritten(t *testing.T) {
	db := testutil.New(t)
	tenant, err := db.Owner.EnsureTenant(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	dsn, input, output := filepath.Join(directory, "operator.dsn"), filepath.Join(directory, "input.json"), filepath.Join(directory, "document-keys.json")
	if err = os.WriteFile(dsn, []byte(testutil.DSN(db.Admin)), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(request{TenantID: tenant})
	if err = os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"document-key", "--dsn-file", dsn, "--input", input, "--output", output}
	var console bytes.Buffer
	if err = run(db.Context, args, &console); err != nil {
		t.Fatal(err)
	}
	keys, err := documents.OpenKeyring(output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = keys.Identity(db.Context, tenant, "primary"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(console.Bytes(), []byte("AGE-SECRET")) {
		t.Fatal("document key reached console")
	}
	if err = run(db.Context, args, &console); err == nil {
		t.Fatal("existing document keys overwritten")
	}
	after, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("retry changed retained keys", err)
	}
}
