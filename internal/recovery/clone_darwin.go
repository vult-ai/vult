package recovery

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Clone makes dst an APFS copy-on-write copy of the directory src: instant,
// and sharing storage until either side changes. dst must not exist.
func Clone(src, dst string) error {
	if err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW); err != nil {
		return &os.LinkError{Op: "clone", Old: src, New: dst, Err: err}
	}
	d, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer d.Close()
	return FullSync(d)
}

// FullSync writes f through to stable storage, not just the drive's cache.
func FullSync(f *os.File) error {
	_, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0)
	return err
}
