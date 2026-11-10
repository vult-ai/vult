package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Agents on this computer read the owner's originals from copies in a folder
// only this user can open. Vult keeps the originals encrypted; the copies are
// plain files, so they are kept only as long as an agent may still be using
// them.

// CopyLife is how long a copy stays after it was made: longer than any agent's
// access lasts.
const CopyLife = 24 * time.Hour

// DocumentCopiesFolder returns the private folder for copies under the
// current user's home, creating it when create is set.
func DocumentCopiesFolder(create bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("Vult couldn't find the home folder for document copies")
	}
	folder := filepath.Join(home, "Library", "Application Support", "Vult", "Documents")
	if !create {
		return folder, nil
	}
	if err = os.MkdirAll(folder, 0o700); err != nil {
		return "", fmt.Errorf("Vult couldn't create %s for document copies", folder)
	}
	if err = os.Chmod(folder, 0o700); err != nil {
		return "", fmt.Errorf("Vult couldn't make %s private", folder)
	}
	return folder, nil
}

// ClearDocumentCopies removes the files in folder made more than age ago, and
// returns how many it removed. Only regular files directly in the folder are
// touched; links and folders are left alone.
func ClearDocumentCopies(folder string, age time.Duration) (int, error) {
	root, err := os.OpenRoot(folder)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-age)
	removed := 0
	var failed error
	for _, entry := range entries {
		info, err := root.Lstat(entry.Name())
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			continue
		}
		if err = root.Remove(entry.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = err
			continue
		}
		removed++
	}
	return removed, failed
}
