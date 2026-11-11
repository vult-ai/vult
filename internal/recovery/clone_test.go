//go:build darwin || linux

package recovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// The copy is whole, independent of the original, and keeps permissions;
// links are never copied.
func TestCloneCopiesTheDataFolder(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data")
	os.MkdirAll(filepath.Join(src, "database", "base"), 0o700)
	os.WriteFile(filepath.Join(src, "database", "base", "1"), []byte("before"), 0o600)
	os.WriteFile(filepath.Join(src, "configuration.json"), []byte("{}"), 0o400)
	dst := filepath.Join(filepath.Dir(src), "copy")
	if err := Clone(src, dst); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "database", "base", "1"), []byte("after"), 0o600)
	if b, _ := os.ReadFile(filepath.Join(dst, "database", "base", "1")); string(b) != "before" {
		t.Fatal("the copy changed with the original")
	}
	for path, want := range map[string]os.FileMode{"configuration.json": 0o400, "database": 0o700, "database/base/1": 0o600} {
		info, err := os.Stat(filepath.Join(dst, path))
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v, %v", path, info, err)
		}
	}
	if err := Clone(src, dst); err == nil {
		t.Fatal("copied over an existing folder")
	}
	os.Symlink("/etc", filepath.Join(src, "link"))
	other := filepath.Join(filepath.Dir(src), "other")
	err := Clone(src, other)
	if isLinux() {
		if err == nil {
			t.Fatal("a link was copied")
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		if target, err := os.Readlink(filepath.Join(other, "link")); err != nil || target != "/etc" {
			t.Fatalf("link was followed or changed: %q, %v", target, err)
		}
	}
}

func TestClonePreservesReadOnlyDirectoriesAndFiles(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data")
	os.MkdirAll(filepath.Join(src, "readonly", "nested"), 0o700)
	os.WriteFile(filepath.Join(src, "readonly", "nested", "value"), []byte("kept"), 0o400)
	os.Chmod(filepath.Join(src, "readonly", "nested"), 0o500)
	os.Chmod(filepath.Join(src, "readonly"), 0o500)
	defer os.Chmod(filepath.Join(src, "readonly"), 0o700)
	defer os.Chmod(filepath.Join(src, "readonly", "nested"), 0o700)
	dst := filepath.Join(t.TempDir(), "copy")
	if err := Clone(src, dst); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(dst, "readonly"), 0o700)
	defer os.Chmod(filepath.Join(dst, "readonly", "nested"), 0o700)
	if data, err := os.ReadFile(filepath.Join(dst, "readonly", "nested", "value")); err != nil || string(data) != "kept" {
		t.Fatalf("read-only data was not copied: %q, %v", data, err)
	}
	for _, name := range []string{"readonly", "readonly/nested"} {
		info, err := os.Stat(filepath.Join(dst, name))
		if err != nil || info.Mode().Perm() != 0o500 {
			t.Fatalf("%s lost its permissions: %v, %v", name, info, err)
		}
	}
}

func TestPointRejectsChangedBytesOfTheSameSize(t *testing.T) {
	src := filepath.Join(t.TempDir(), "data")
	os.Mkdir(src, 0o700)
	os.WriteFile(filepath.Join(src, "configuration.json"), []byte(`{"value":"before"}`), 0o600)
	p := Point{Dir: filepath.Join(t.TempDir(), "point"), Data: src,
		Clean: func(string) error { return nil },
		Fingerprint: func() (ledger.FactFingerprint, error) {
			t.Fatal("corrupted data passed the manifest comparison")
			return ledger.FactFingerprint{}, nil
		},
		Clone: func(src, dst string) error {
			if err := Clone(src, dst); err != nil {
				return err
			}
			file := filepath.Join(dst, "configuration.json")
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			return os.WriteFile(file, []byte(strings.Replace(string(data), "before", "after!", 1)), 0o600)
		},
	}
	if _, err := p.Take("example"); err == nil {
		t.Fatal("a same-sized corrupted copy was accepted")
	}
	if _, err := os.Lstat(p.Dir); !os.IsNotExist(err) {
		t.Fatal("a corrupted recovery point was kept", err)
	}
}
