package daemon

import (
	"errors"
	"os"
	"syscall"

	"github.com/vult-ai/vult/internal/fssecurity"
)

func protectedACL(path string, before os.FileInfo) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("installation access controls could not be verified")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !os.SameFile(before, info) || !fssecurity.NoExtendedACL(f) {
		return errors.New("unsupported installation access control list")
	}
	return nil
}

func codeParents() []string {
	return []string{"/opt", "/opt/vult"}
}

func dataParents() []string {
	return []string{"/var", "/var/lib", "/var/lib/vult"}
}
