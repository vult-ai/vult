package filebridge

import (
	"github.com/vult-ai/vult/internal/fssecurity"
	"os"
	"os/exec"
	"testing"
)

func TestExportFolderACLDoesNotBypassPrivateMode(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if !fssecurity.NoExtendedACL(file) {
		t.Fatal("positive private-directory ACL control failed")
	}
	command := exec.Command("/bin/chmod", "+a", "everyone allow add_file,delete_child", directory)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if err = command.Run(); err != nil {
		t.Fatal("synthetic ACL setup failed", err)
	}
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("ACL control did not retain private mode", err)
	}
	if fssecurity.NoExtendedACL(file) {
		t.Fatal("extended ACL bypassed private export mode")
	}
	if files, err := Open(&exampleTransport{}, "", directory); err == nil {
		files.Close()
		t.Fatal("ACL-writable export directory accepted")
	}
}
