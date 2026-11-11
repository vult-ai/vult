//go:build !darwin && !linux

package recovery

import (
	"errors"
	"os"
)

// Clone needs APFS; elsewhere a recovery point has only its archive.
func Clone(src, dst string) error {
	return errors.New("copy-on-write copies need macOS")
}

// FullSync writes f through to stable storage.
func FullSync(f *os.File) error { return f.Sync() }
