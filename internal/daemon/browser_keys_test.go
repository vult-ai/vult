package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vult-ai/vult/internal/secretbox"
)

// The installed layout: the browser folder is root's and the browser account
// can only read it, so the account keeps its keyring in its own keys folder.
// Here the test's own account stands in for the browser account.
func TestTheBrowserAccountKeepsItsKeyringInItsOwnFolder(t *testing.T) {
	base := filepath.Join(t.TempDir(), "browser")
	if err := os.Mkdir(base, 0o750); err != nil {
		t.Fatal(err)
	}
	// A keyring an older layout left beside the folder moves in.
	if err := os.WriteFile(filepath.Join(base, "keyring.read.json"), []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	if err := prepareBrowserKeys(base, uid, gid); err != nil {
		t.Fatal(err)
	}
	keys := browserKeyFolder(base)
	if data, err := os.ReadFile(filepath.Join(keys, "keyring.read.json")); err != nil || string(data) != "older" {
		t.Fatal("the older keyring wasn't moved in", err)
	}
	if _, err := os.Lstat(filepath.Join(base, "keyring.read.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the older keyring stayed beside the folder")
	}
	// Run again, as every install does: nothing changes.
	if err := prepareBrowserKeys(base, uid, gid); err != nil {
		t.Fatal("a second install", err)
	}
	// As installed, the account can't write the browser folder itself.
	if err := os.Chmod(base, 0o550); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(base, 0o750) })
	if err := checkBrowserKeys(base, uid); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	inFolder := secretbox.FileSource{Path: filepath.Join(keys, "keyring.new.json")}
	if _, err := secretbox.Ensure(ctx, inFolder); err != nil {
		t.Fatal("the keys folder doesn't take a keyring", err)
	}
	if _, err := secretbox.Ensure(ctx, inFolder); err != nil {
		t.Fatal("the stored keyring doesn't open", err)
	}
	// The browser folder itself refuses one, and says so as a key store failure.
	beside := secretbox.FileSource{Path: filepath.Join(base, "keyring.new.json")}
	if _, err := secretbox.Ensure(ctx, beside); !errors.Is(err, secretbox.ErrKeyStore) {
		t.Fatal("a keyring beside the folder", err)
	}
}

func TestTheBrowserKeyFolderMustBeTheAccountsAlone(t *testing.T) {
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	for name, setup := range map[string]func(base string) error{
		"shared with its group": func(base string) error {
			return os.Mkdir(browserKeyFolder(base), 0o770)
		},
		"a link": func(base string) error {
			other := filepath.Join(filepath.Dir(base), "elsewhere")
			if err := os.Mkdir(other, 0o700); err != nil {
				return err
			}
			return os.Symlink(other, browserKeyFolder(base))
		},
		"a file": func(base string) error {
			return os.WriteFile(browserKeyFolder(base), nil, 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "browser")
			if err := os.Mkdir(base, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := setup(base); err != nil {
				t.Fatal(err)
			}
			if err := checkBrowserKeys(base, uid); err == nil {
				t.Fatal("an unsafe key folder was accepted")
			}
			err := prepareBrowserKeys(base, uid, gid)
			if name == "shared with its group" {
				// The installer puts a real folder right.
				if err != nil || checkBrowserKeys(base, uid) != nil {
					t.Fatal("the installer didn't make the folder private", err)
				}
				return
			}
			if err == nil {
				t.Fatal("the installer accepted", name)
			}
		})
	}
	// A missing folder is reported as missing, so the service can say so.
	if err := checkBrowserKeys(filepath.Join(t.TempDir(), "browser"), uid); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a missing key folder", err)
	}
}
