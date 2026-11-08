package fssecurity

import (
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// NoExtendedACL checks the opened object, including inherited directory ACLs.
// Unknown filesystems and errors fail closed rather than trusting mode bits.
func NoExtendedACL(file *os.File) bool {
	if file == nil {
		return false
	}
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		_, err := unix.Fgetxattr(int(file.Fd()), name, nil)
		runtime.KeepAlive(file)
		if !errors.Is(err, unix.ENODATA) {
			return false
		}
	}
	return true
}
