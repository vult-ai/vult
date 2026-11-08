package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestOfflinePasskeySetupKeepsAuthorityPrivateAndRecoveryBound(t *testing.T) {
	db := testutil.New(t)
	directory := t.TempDir()
	dsn, input, output := filepath.Join(directory, "operator.dsn"), filepath.Join(directory, "input.json"), filepath.Join(directory, "result.json")
	if err := os.WriteFile(dsn, []byte(testutil.DSN(db.Admin)), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(request{Username: "example-owner", Origin: "https://ledger.example.com"})
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	args := []string{"passkey-setup", "--dsn-file", dsn, "--input", input, "--output", output}
	if err := run(db.Context, args, &stdout); err != nil {
		t.Fatal(err)
	}
	var enrollment struct {
		TenantID string   `json:"tenant_id"`
		SetupURL string   `json:"setup_url"`
		Expires  int      `json:"expires_in_seconds"`
		Codes    []string `json:"recovery_codes"`
	}
	stored, err := os.ReadFile(output)
	if err != nil || json.Unmarshal(stored, &enrollment) != nil || !ledger.ValidID(enrollment.TenantID) || enrollment.Expires != 300 || len(enrollment.Codes) != 8 {
		t.Fatal("private enrollment output incomplete", err)
	}
	link, err := url.Parse(enrollment.SetupURL)
	if err != nil || link.RawQuery != "" || link.Path != "/auth/setup" || !strings.HasPrefix(link.Fragment, "setup=") {
		t.Fatal("setup authority reached the request query", err)
	}
	if strings.Contains(stdout.String(), enrollment.SetupURL) || strings.Contains(stdout.String(), enrollment.Codes[0]) {
		t.Fatal("setup authority reached the console")
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("enrollment result is not private", err)
	}
	if run(db.Context, args, &stdout) == nil {
		t.Fatal("existing enrollment result overwritten")
	}
	after, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(stored, after) {
		t.Fatal("existing result changed", err)
	}
	recoveryOutput := filepath.Join(directory, "recovery.json")
	if err := run(db.Context, []string{"passkey-recover", "--dsn-file", dsn, "--input", input, "--output", recoveryOutput}, &stdout); err != nil {
		t.Fatal(err)
	}
	var recovery struct {
		TenantID string   `json:"tenant_id"`
		SetupURL string   `json:"setup_url"`
		Codes    []string `json:"recovery_codes"`
	}
	data, err = os.ReadFile(recoveryOutput)
	if err != nil || json.Unmarshal(data, &recovery) != nil || recovery.TenantID != enrollment.TenantID || recovery.SetupURL == enrollment.SetupURL || len(recovery.Codes) != 8 {
		t.Fatal("recovery changed ownership or reused setup authority", err)
	}
	if strings.Contains(stdout.String(), recovery.SetupURL) || strings.Contains(stdout.String(), recovery.Codes[0]) {
		t.Fatal("recovery authority reached the console")
	}
}
