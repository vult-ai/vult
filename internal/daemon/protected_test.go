package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedPathsRejectUnsafeMetadata(t *testing.T) {
	for _, kind := range []string{"file", "directory", "missing", "wrong-owner", "group-write", "other-write", "file-symlink", "directory-symlink", "hardlink", "wrong-type"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "protected")
			uid := uint32(os.Geteuid())
			directory := kind == "directory" || kind == "directory-symlink"
			if directory {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if kind != "missing" {
				if err := os.WriteFile(path, []byte("synthetic installation file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "wrong-owner":
				uid++
			case "group-write", "other-write":
				mode := os.FileMode(0620)
				if kind == "other-write" {
					mode = 0602
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			case "file-symlink", "directory-symlink":
				if err := os.Rename(path, path+".target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".target", path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".copy"); err != nil {
					t.Fatal(err)
				}
			case "wrong-type":
				directory = true
			}
			err := protected(path, uid, directory)
			wantValid := kind == "file" || kind == "directory"
			if (err == nil) != wantValid {
				t.Fatalf("protected path accepted=%t, want %t: %v", err == nil, wantValid, err)
			}
		})
	}
}
