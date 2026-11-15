package daemon

import (
	"path/filepath"
	"strings"
	"testing"
)

// The installed program recognizes every file the package puts in its
// program folder, or it can't stop itself to upgrade or uninstall.
func TestProgramPathRecognizesEveryPackagedFile(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"vult", "vult-enclave", "vult-ocr", "LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md",
		"Vult Browser Setup.app/Contents/Info.plist", "postgres/bin/pg_controldata"} {
		if path, err := programPath(root, name); err != nil || path != filepath.Join(root, name) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := programPath(root, "unexpected-helper"); err == nil {
		t.Fatal("an unknown file was recognized")
	}
}

// The package build checks its inventory with the program it built.
func TestCheckProgramInventory(t *testing.T) {
	good := `{"format":"vult-program-files-v1","version":"` + Version + `","files":{"vult":"` + strings.Repeat("a", 64) + `","vult-enclave":"` + strings.Repeat("b", 64) + `"},"launch_daemon_sha256":"` + strings.Repeat("c", 64) + `"}`
	if err := CheckProgramInventory([]byte(good)); err != nil {
		t.Fatal(err)
	}
	bad := strings.Replace(good, `"vult-enclave"`, `"unexpected-helper"`, 1)
	if err := CheckProgramInventory([]byte(bad)); err == nil || !strings.Contains(err.Error(), "unexpected-helper") {
		t.Fatal("an unknown program file passed", err)
	}
}
