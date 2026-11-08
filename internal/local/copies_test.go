package local

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentsGetAPrivateFolderForDocumentCopies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	folder, err := DocumentCopiesFolder(true)
	if err != nil {
		t.Fatal(err)
	}
	if folder != filepath.Join(home, "Library", "Application Support", "Vult", "Documents") {
		t.Fatal("unexpected folder", folder)
	}
	info, err := os.Stat(folder)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatal("the folder is not private", err)
	}
	if again, err := DocumentCopiesFolder(true); err != nil || again != folder {
		t.Fatal("an existing folder was not reused", err)
	}
}

func TestOldDocumentCopiesAreClearedAndNothingElse(t *testing.T) {
	folder := t.TempDir()
	write := func(name string, age time.Duration) string {
		path := filepath.Join(folder, name)
		if err := os.WriteFile(path, []byte("statement"), 0o600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
		return path
	}
	old := write("old.pdf", 2*CopyLife)
	recent := write("recent.pdf", time.Minute)
	outside := filepath.Join(t.TempDir(), "elsewhere.pdf")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	old2 := time.Now().Add(-2 * CopyLife)
	_ = os.Chtimes(outside, old2, old2)
	if err := os.Symlink(outside, filepath.Join(folder, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(folder, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}

	removed, err := ClearDocumentCopies(folder, CopyLife)
	if err != nil || removed != 1 {
		t.Fatal("expected one old copy removed", removed, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("an old copy was kept")
	}
	for _, path := range []string{recent, outside, filepath.Join(folder, "nested")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("a file that isn't an old copy was removed", path)
		}
	}
	if _, err := os.Lstat(filepath.Join(folder, "link.pdf")); err != nil {
		t.Fatal("a link was removed")
	}

	if removed, err = ClearDocumentCopies(folder, 0); err != nil || removed != 1 {
		t.Fatal("expected every remaining copy removed", removed, err)
	}
	if removed, err = ClearDocumentCopies(filepath.Join(folder, "missing"), 0); err != nil || removed != 0 {
		t.Fatal("a missing folder is not an error", err)
	}
}
