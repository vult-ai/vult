package fssecurity

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestNoExtendedACLRejectsAccessAndDefaultEntries(t *testing.T) {
	for _, variant := range []string{"file", "directory", "default-directory"} {
		t.Run(variant, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private")
			mode := os.FileMode(0600)
			if variant != "file" {
				mode = 0700
				if err := os.Mkdir(path, mode); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("synthetic private file"), mode); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if !NoExtendedACL(file) {
				t.Fatal("ACL-free positive control was rejected")
			}
			acl := []byte{2, 0, 0, 0}
			for _, entry := range []struct {
				tag, permissions uint16
				id               uint32
			}{{1, uint16(mode >> 6), ^uint32(0)}, {2, 7, 12345}, {4, 0, ^uint32(0)}, {16, 0, ^uint32(0)}, {32, 0, ^uint32(0)}} {
				acl = binary.LittleEndian.AppendUint16(acl, entry.tag)
				acl = binary.LittleEndian.AppendUint16(acl, entry.permissions)
				acl = binary.LittleEndian.AppendUint32(acl, entry.id)
			}
			attribute := "system.posix_acl_access"
			if variant == "default-directory" {
				attribute = "system.posix_acl_default"
			}
			if err := unix.Fsetxattr(int(file.Fd()), attribute, acl, 0); err != nil {
				t.Fatal("synthetic filesystem must support POSIX ACL checks", err)
			}
			info, err := file.Stat()
			if err != nil || info.Mode().Perm() != mode {
				t.Fatal("masked ACL changed private mode", err)
			}
			if NoExtendedACL(file) {
				t.Fatal("an extended ACL was accepted despite its zero mask")
			}
			if variant == "default-directory" {
				child, err := os.OpenFile(filepath.Join(path, "child"), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal(err)
				}
				defer child.Close()
				if NoExtendedACL(child) {
					t.Fatal("an inherited extended ACL was accepted")
				}
			}
			if err := unix.Fremovexattr(int(file.Fd()), attribute); err != nil {
				t.Fatal(err)
			}
			if !NoExtendedACL(file) {
				t.Fatal("ACL removal did not restore the positive control")
			}
		})
	}
}

func TestNoExtendedACLRejectsUnavailableDescriptors(t *testing.T) {
	if NoExtendedACL(nil) {
		t.Fatal("nil descriptor accepted")
	}
	file, err := os.CreateTemp(t.TempDir(), "private-")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if NoExtendedACL(file) {
		t.Fatal("closed descriptor accepted")
	}
}
