package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

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
