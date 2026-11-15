//go:build darwin || linux

package updatehelper

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

// Every update prepares the browser's keys folder as the installer does: an
// update through the app gives a Vult installed before the keys folder
// existed a folder of the browser account's own, with the keyring left
// beside it moved in, and again changes nothing. A computer without the
// browser service is left alone.
func TestEveryUpdatePreparesTheBrowsersKeys(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	asked := ""
	lookupAccount = func(name string) (*user.User, error) {
		asked = name
		return me, nil
	}
	t.Cleanup(func() { lookupAccount = user.Lookup })

	missing := filepath.Join(t.TempDir(), "browser")
	if err := prepareBrowserKeys(missing, "_vultbrowser"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("a browser folder was made where there was none")
	}

	base := filepath.Join(t.TempDir(), "browser")
	if err := os.Mkdir(base, 0o750); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(base, "keyring.read.sealed")
	if err := os.WriteFile(old, []byte("sealed"), 0o640); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := prepareBrowserKeys(base, "_vultbrowser"); err != nil {
			t.Fatal(err)
		}
	}
	if asked != "_vultbrowser" {
		t.Fatal("asked for", asked)
	}
	keys := filepath.Join(base, "keys")
	info, err := os.Stat(keys)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("keys folder", info, err)
	}
	moved, err := os.Stat(filepath.Join(keys, "keyring.read.sealed"))
	if err != nil || moved.Mode().Perm() != 0o600 {
		t.Fatal("the keyring wasn't moved in", err)
	}
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Fatal("the keyring is still beside the keys folder")
	}
	// A keys folder that isn't a real folder stops the update rather than
	// being used.
	other := filepath.Join(t.TempDir(), "browser")
	if err := os.Mkdir(other, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(other, "keys")); err != nil {
		t.Fatal(err)
	}
	if err := prepareBrowserKeys(other, "_vultbrowser"); err == nil {
		t.Fatal("a linked keys folder was used")
	}
}
