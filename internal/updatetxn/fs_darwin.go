package updatetxn

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// FullSync writes f through to stable storage. On macOS fsync only reaches
// the drive's cache; F_FULLFSYNC asks the drive to write it out.
func FullSync(f *os.File) error {
	_, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0)
	return err
}

// SwapDirs exchanges two directories in one step: at every instant each path
// names one complete directory. Both must be on the same APFS volume.
func SwapDirs(a, b string) error {
	if err := unix.RenamexNp(a, b, unix.RENAME_SWAP); err != nil {
		return &os.LinkError{Op: "swap", Old: a, New: b, Err: err}
	}
	if err := SyncDir(filepath.Dir(a)); err != nil {
		return err
	}
	if filepath.Dir(a) != filepath.Dir(b) {
		return SyncDir(filepath.Dir(b))
	}
	return nil
}
