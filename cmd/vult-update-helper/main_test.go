//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vult-ai/vult/internal/updatehelper"
)

// The helper attack surface: it runs only from launchd, as root, with no
// arguments, and trusts only its own root-owned trust store.
func TestHelperRunsOnlyFromLaunchd(t *testing.T) {
	if err := launchedByLaunchd([]string{"/path/vult-update-helper"}, 1, 0); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		args       []string
		ppid, euid int
	}{
		"an argument":          {[]string{"helper", "--install"}, 1, 0},
		"an empty argument":    {[]string{"helper", ""}, 1, 0},
		"started by a user":    {[]string{"helper"}, 4242, 0},
		"not root":             {[]string{"helper"}, 1, 501},
		"no program name even": {nil, 1, 0},
	} {
		if err := launchedByLaunchd(c.args, c.ppid, c.euid); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTrustStoreMustNameAnHTTPSRepository(t *testing.T) {
	l := updatehelper.Layout{State: t.TempDir()}
	os.MkdirAll(l.Trust(), 0o700)
	if _, err := trustConfig(l); err == nil {
		t.Fatal("accepted a trust store without a root")
	}
	os.WriteFile(filepath.Join(l.Trust(), "root.json"), []byte(`{}`), 0o600)
	for _, repo := range []string{
		`{"metadata_url":"http://updates.invalid/m","targets_url":"https://updates.invalid/t"}`,
		`{"metadata_url":"https://updates.invalid/m"}`,
		`{"metadata_url":"https://updates.invalid/m","targets_url":"https://updates.invalid/t","extra":1}`,
		`not json`,
	} {
		os.WriteFile(filepath.Join(l.Trust(), "repository.json"), []byte(repo), 0o600)
		if _, err := trustConfig(l); err == nil {
			t.Errorf("accepted %s", repo)
		}
	}
	os.WriteFile(filepath.Join(l.Trust(), "repository.json"), []byte(`{"metadata_url":"https://updates.invalid/m","targets_url":"https://updates.invalid/t"}`), 0o600)
	if c, err := trustConfig(l); err != nil || c.MetadataDir != filepath.Join(l.Trust(), "metadata") {
		t.Fatalf("%+v, %v", c, err)
	}
}
