package fssecurity

import (
	"encoding/binary"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Require no extended ACL on the opened directory, so mode 0700 retains its
// usual meaning. A pathname check could inspect a different directory instead.
func NoExtendedACL(directory *os.File) bool {
	attributes := struct {
		Count, Reserved                       uint16
		Common, Volume, Directory, File, Fork uint32
	}{Count: 5, Common: unix.ATTR_CMN_EXTENDED_SECURITY}
	// fgetattrlist returns a uint32 length and one attrreference_t. Without an
	// ACL that reference has offset 8 and zero bytes; anything else fails closed.
	var result [12]byte
	_, _, errno := unix.Syscall6(unix.SYS_FGETATTRLIST, directory.Fd(), uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&result[0])), uintptr(len(result)), 0, 0)
	runtime.KeepAlive(directory)
	return errno == 0 && binary.NativeEndian.Uint32(result[:4]) == 12 && binary.NativeEndian.Uint32(result[4:8]) == 8 && binary.NativeEndian.Uint32(result[8:]) == 0
}
