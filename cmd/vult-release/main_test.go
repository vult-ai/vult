package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/updaterepo"
	"github.com/vult-ai/vult/internal/updatetrust"
)

func init() { updaterepo.ScryptWorkFactor = 10 }

type scripted struct {
	passphrase string
	confirm    bool
	shown      []string
}

func (s *scripted) Passphrase(string, bool) ([]byte, error) { return []byte(s.passphrase), nil }
func (s *scripted) Confirm(summary string) bool {
	s.shown = append(s.shown, summary)
	return s.confirm
}

func TestCeremonyAndFirstReleaseThroughTheCommand(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	repo := filepath.Join(dir, "repo")
	os.Mkdir(keys, 0o700)
	now := func() time.Time { return time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC) }
	p := &scripted{passphrase: "a long test passphrase", confirm: true}
	cmd := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(args, &out, p, now); err != nil {
			t.Fatalf("vult-release %s: %v", strings.Join(args, " "), err)
		}
		return out.String()
	}
	k := func(name string) string { return filepath.Join(keys, name+".key") }
	for _, name := range []string{"K1", "K2", "K3", "T1", "T2", "T3", "S", "B", "N", "M"} {
		cmd("key", "new", "--out", k(name))
	}
	// Restore test from a second copy of a key file.
	copyB := filepath.Join(dir, "copy-b-S.key")
	data, _ := os.ReadFile(k("S"))
	os.WriteFile(copyB, data, 0o600)
	if out := cmd("key", "test", "--key", copyB); !strings.Contains(out, "Restore test passed") {
		t.Fatal(out)
	}
	pub := func(names ...string) string {
		var out []string
		for _, n := range names {
			out = append(out, k(n)+".pub")
		}
		return strings.Join(out, ",")
	}
	cmd("init", "--repo", repo, "--root-keys", pub("K1", "K2", "K3"), "--targets-keys", pub("T1", "T2", "T3"),
		"--release-keys", pub("S"), "--rebuild-key", pub("B"), "--snapshot-key", pub("N"), "--timestamp-key", pub("M"))
	for _, step := range [][2]string{{"root", "K1"}, {"root", "K2"}, {"targets", "T1"}, {"targets", "T2"},
		{"releases", "S"}, {"holds", "S"}, {"rebuild", "B"}, {"updater", "K1"}, {"updater", "K2"}} {
		cmd("sign", "--repo", repo, "--role", step[0], "--key", k(step[1]))
	}
	cmd("freshen", "--repo", repo, "--snapshot-key", k("N"), "--timestamp-key", k("M"))
	cmd("publish", "--repo", repo)

	artifact := filepath.Join(dir, "payload.tar")
	os.WriteFile(artifact, bytes.Repeat([]byte("payload"), 100), 0o644)
	contract := filepath.Join(dir, "contract.json")
	c := updatetrust.Contract{
		Version: "0.7.1", Platform: updatetrust.PlatformMacOS, MinimumOS: "26.0",
		SchemaFrom: []string{"0007"}, SchemaTo: "0008", Migrations: map[string]string{},
		MinimumUpdater: "1.0.0",
		PostgreSQL:     updatetrust.PostgreSQL{Major: 18, Version: "18.6", SourceSHA256: strings.Repeat("b", 64)},
		Source:         updatetrust.Source{Repository: "example/vult", Commit: strings.Repeat("c", 40), Tag: "v0.7.1"},
		Build:          updatetrust.Build{Method: updatetrust.BuildIdentical, InputsSHA256: strings.Repeat("d", 64)},
	}
	b, _ := json.Marshal(c)
	os.WriteFile(contract, b, 0o644)

	cmd("release", "add", "--repo", repo, "--artifact", artifact, "--contract", contract)
	var out bytes.Buffer
	if err := run([]string{"sign", "--repo", repo, "--role", "releases", "--key", k("S")}, &out, p, now); err == nil {
		t.Fatal("the release was signed before the rebuild agreed")
	}
	cmd("rebuild", "add", "--repo", repo, "--artifact", artifact, "--contract", contract)
	cmd("sign", "--repo", repo, "--role", "rebuild", "--key", k("B"))
	p.shown = nil
	cmd("sign", "--repo", repo, "--role", "releases", "--key", k("S"))
	if len(p.shown) != 1 || !strings.Contains(p.shown[0], "NEW Vult 0.7.1") || !strings.Contains(p.shown[0], "rebuild agrees") {
		t.Fatalf("the owner was shown %q", p.shown)
	}
	cmd("freshen", "--repo", repo, "--snapshot-key", k("N"), "--timestamp-key", k("M"))
	cmd("publish", "--repo", repo)
	if out := cmd("verify", "--repo", repo, "--platform", "macos-arm64", "--installed", "0.7.0"); !strings.Contains(out, "would install 0.7.1") {
		t.Fatal(out)
	}

	// Declining at the summary signs nothing.
	cmd("hold", "--repo", repo, "--version", "0.7.1")
	p.confirm = false
	if err := run([]string{"sign", "--repo", repo, "--role", "holds", "--key", k("S")}, &out, p, now); err == nil {
		t.Fatal("signed after the owner declined")
	}
}

// Only the automatic signers may run without a person at the keyboard.
func TestUnattendedSigningIsOnlyForTheAutomaticSigners(t *testing.T) {
	now := func() time.Time { return time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC) }
	for _, args := range [][]string{
		{"sign", "--repo", t.TempDir(), "--role", "releases", "--key", "k", "--passphrase-env", "P", "--unattended"},
		{"sign", "--repo", t.TempDir(), "--role", "root", "--key", "k", "--passphrase-env", "P"},
		{"sign", "--repo", t.TempDir(), "--role", "holds", "--key", "k", "--passphrase-env", "P"},
		{"hold", "--repo", t.TempDir(), "--version", "0.7.1", "--unattended"},
		{"sign", "--repo", t.TempDir(), "--role", "rebuild", "--key", "k", "--unattended"},
	} {
		if err := run(args, io.Discard, nil, now); err == nil || !strings.Contains(err.Error(), "only for the rebuild") {
			t.Errorf("%v: %v", args, err)
		}
	}
	t.Setenv("VULT_TEST_PASS", "a long test passphrase")
	key := filepath.Join(t.TempDir(), "rebuild.key")
	if err := run([]string{"key", "new", "--out", key, "--passphrase-env", "VULT_TEST_PASS"}, io.Discard, nil, now); err != nil {
		t.Fatalf("creating an automatic key: %v", err)
	}
	if err := run([]string{"key", "test", "--key", key, "--passphrase-env", "VULT_TEST_PASS"}, io.Discard, nil, now); err != nil {
		t.Fatalf("testing an automatic key: %v", err)
	}
}
