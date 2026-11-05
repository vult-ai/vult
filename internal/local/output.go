package local

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/vult-ai/vult/internal/fssecurity"
	"golang.org/x/sys/unix"
)

// WriteNewStream stages in a private directory on the destination filesystem.
// Publication links from the verified directory descriptor, so a writer in the
// parent cannot substitute a different staging pathname. It never overwrites.
func WriteNewStream(path string, write func(io.Writer) error) error {
	if write == nil {
		return errors.New("new output path required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil || path == "" {
		return errors.New("new output path required")
	}
	parent, name := filepath.Dir(absolute), filepath.Base(absolute)
	if name == "." || name == string(filepath.Separator) {
		return errors.New("new output path required")
	}
	destination, err := os.OpenRoot(parent)
	if err != nil {
		return errors.New("output directory unavailable")
	}
	defer destination.Close()
	if _, err = destination.Lstat(name); err == nil || !os.IsNotExist(err) {
		return errors.New("new output path required")
	}
	directory, err := destination.Open(".")
	if err != nil {
		return errors.New("output directory unavailable")
	}
	defer directory.Close()
	staging, err := os.MkdirTemp(parent, ".vult-output-")
	if err != nil {
		return errors.New("private output staging unavailable")
	}
	info, err := os.Lstat(staging)
	if err != nil {
		return errors.New("private output staging unavailable")
	}
	root, err := os.OpenRoot(staging)
	if err != nil {
		os.Remove(staging)
		return errors.New("private output staging unavailable")
	}
	defer func() {
		root.Remove("content")
		current, err := os.Lstat(staging)
		if err == nil && os.SameFile(info, current) {
			os.Remove(staging)
		}
		root.Close()
	}()
	opened, err := root.Open(".")
	if err != nil {
		return errors.New("private output staging unavailable")
	}
	defer opened.Close()
	actual, err := opened.Stat()
	if err != nil || !os.SameFile(info, actual) || actual.Mode().Perm()&0077 != 0 || !fssecurity.NoExtendedACL(opened) {
		return errors.New("private output staging required")
	}
	stat, ok := actual.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("private output staging required")
	}
	file, err := root.OpenFile("content", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return errors.New("private output file unavailable")
	}
	defer file.Close()
	if !fssecurity.NoExtendedACL(file) {
		return errors.New("private output file required")
	}
	if err = write(file); err != nil {
		return err
	}
	if file.Sync() != nil || file.Close() != nil {
		return errors.New("output incomplete; no file published")
	}
	if err = unix.Linkat(int(opened.Fd()), "content", int(directory.Fd()), name, 0); err != nil {
		return errors.New("new output path required; existing files were preserved")
	}
	if err = directory.Sync(); err != nil {
		return errors.New("output exists but durability is unconfirmed; inspect it before retrying")
	}
	return nil
}
