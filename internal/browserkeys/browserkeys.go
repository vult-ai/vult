// Package browserkeys keeps the browser service's keyring folder: a folder
// of the browser account's own inside the browser folder, which belongs to
// root. The installer and every update prepare it; the service checks it.
package browserkeys

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/vult-ai/vult/internal/fssecurity"
)

// The browser service keeps its keyring, which seals the owner's bank keys,
// access tokens and AI provider key, in a folder of its own inside the
// browser folder. The browser folder belongs to root and the browser account
// can only read it; the keys folder belongs to the browser account alone, so
// the service can create and replace its key files there.
const browserKeysName = "keys"

func Folder(base string) string { return filepath.Join(base, browserKeysName) }

// keyFiles are the files a keyring source keeps in its folder: a keyring
// sealed by the Secure Enclave with its enclave key, or a plain keyring file
// on a Mac without one.
var keyFiles = []string{"keyring.read.sealed", "enclave.read.key", "keyring.read.json"}

// Prepare runs as root when Vult is installed or updated. It makes
// the keys folder the browser account's alone and moves in any key files
// found beside it in the browser folder, keeping one already in the folder.
func Prepare(base string, uid, gid uint32) error {
	dir := Folder(base)
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(dir, 0o700); err != nil {
			return errors.New("protected browser key folder unavailable")
		}
		info, err = os.Lstat(dir)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("protected browser key folder required")
	}
	if err = ownPrivate(dir, info, uid, gid, 0o700); err != nil {
		return errors.New("protected browser key folder ownership unavailable")
	}
	for _, name := range keyFiles {
		from, to := filepath.Join(base, name), filepath.Join(dir, name)
		found, err := os.Lstat(from)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !found.Mode().IsRegular() {
			return fmt.Errorf("unexpected key file %s in the browser folder", name)
		}
		if _, err = os.Lstat(to); err == nil {
			continue
		}
		if err = os.Rename(from, to); err != nil {
			return fmt.Errorf("key file %s couldn't be moved into the key folder", name)
		}
		if err = ownPrivate(to, found, uid, gid, 0o600); err != nil {
			return errors.New("protected key file ownership unavailable")
		}
	}
	return Check(base, uid)
}

// ownPrivate gives a folder or file to the account, private to it, through
// the one it opened, which must still be the one that was checked.
func ownPrivate(path string, checked os.FileInfo, uid, gid uint32, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(opened, checked) {
		return errors.New("changed while being protected")
	}
	if err = f.Chown(int(uid), int(gid)); err != nil {
		return err
	}
	return f.Chmod(mode)
}

// Check reports whether the keys folder is a real folder that only
// the given account can use.
func Check(base string, uid uint32) error {
	dir := Folder(base)
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("browser key folder unavailable: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || stat.Uid != uid || info.Mode().Perm() != 0o700 {
		return errors.New("browser key folder ownership or permissions are unsafe")
	}
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("browser key folder unavailable: %w", err)
	}
	defer f.Close()
	if !fssecurity.NoExtendedACL(f) {
		return errors.New("browser key folder has an access control list")
	}
	return nil
}
