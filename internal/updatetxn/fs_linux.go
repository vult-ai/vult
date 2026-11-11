package updatetxn

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// FullSync writes f through to stable storage.
func FullSync(f *os.File) error { return f.Sync() }

// SwapDirs exchanges two directories in one step: at every instant each path
// names one complete directory. Both must be on the same file system.
func SwapDirs(a, b string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE); err != nil {
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
