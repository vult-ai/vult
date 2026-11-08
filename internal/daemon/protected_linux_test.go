package daemon

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxInstallationUsesProtectedPrefixes(t *testing.T) {
	if !reflect.DeepEqual(codeParents(), []string{"/opt", "/opt/vult"}) ||
		!reflect.DeepEqual(dataParents(), []string{"/var", "/var/lib", "/var/lib/vult"}) {
		t.Fatal("native installation acquired an unexpected protected prefix")
	}
}

func TestProtectedLinuxRejectsExtendedACLAndReplacedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "protected")
	if err := os.WriteFile(path, []byte("synthetic program"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := protected(path, uint32(os.Geteuid()), false); err != nil {
		t.Fatal("ordinary protected file was rejected", err)
	}
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := protectedACL(path, before); err == nil {
		t.Fatal("ACL verification followed a replaced pathname")
	}
	// Linux POSIX ACL v2: owner, named user, group, mask and other. A zero
	// mask leaves mode 0600 unchanged, even though a named entry is present.
	acl := []byte{2, 0, 0, 0}
	for _, entry := range []struct {
		tag, permissions uint16
		id               uint32
	}{{1, 6, ^uint32(0)}, {2, 6, 12345}, {4, 0, ^uint32(0)}, {16, 0, ^uint32(0)}, {32, 0, ^uint32(0)}} {
		acl = binary.LittleEndian.AppendUint16(acl, entry.tag)
		acl = binary.LittleEndian.AppendUint16(acl, entry.permissions)
		acl = binary.LittleEndian.AppendUint32(acl, entry.id)
	}
	if err := unix.Setxattr(path, "system.posix_acl_access", acl, 0); err != nil {
		t.Fatal("synthetic filesystem must support POSIX ACL checks", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("ACL positive control changed private mode", err)
	}
	if err := protected(path, uint32(os.Geteuid()), false); err == nil {
		t.Fatal("masked extended ACL was accepted")
	}
}
