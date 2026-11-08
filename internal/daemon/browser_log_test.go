package daemon

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestBrowserLogRetainsDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "browser.log")
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	if err := prepareBrowserLog(path, uid, gid); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0600 || stat.Uid != uid || stat.Gid != gid || stat.Nlink != 1 {
		t.Fatal("diagnostic ownership or permissions changed")
	}
	if err := os.WriteFile(path, []byte("example diagnostic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareBrowserLog(path, uid, gid); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "example diagnostic\n" {
		t.Fatal("existing diagnostic was changed", err)
	}
}

func TestBrowserLogRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "fifo", "shared-mode", "wrong-owner", "wrong-group"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path, retained := filepath.Join(dir, "browser.log"), filepath.Join(dir, "retained")
			if err := os.WriteFile(retained, []byte("retain this diagnostic\n"), 0600); err != nil {
				t.Fatal(err)
			}
			uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(retained, path)
			case "hardlink":
				err = os.Link(retained, path)
			case "directory":
				err = os.Mkdir(path, 0700)
			case "fifo":
				err = syscall.Mkfifo(path, 0600)
			default:
				err = os.WriteFile(path, []byte("retain this diagnostic\n"), 0600)
				if err == nil {
					switch kind {
					case "shared-mode":
						err = os.Chmod(path, 0644)
					case "wrong-owner":
						uid++
					case "wrong-group":
						gid++
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareBrowserLog(path, uid, gid); err == nil {
				t.Fatal("unsafe diagnostic was accepted")
			}
			data, err := os.ReadFile(retained)
			if err != nil || string(data) != "retain this diagnostic\n" {
				t.Fatal("linked diagnostic was changed", err)
			}
			if kind == "shared-mode" || kind == "wrong-owner" || kind == "wrong-group" {
				data, err = os.ReadFile(path)
				if err != nil || string(data) != "retain this diagnostic\n" {
					t.Fatal("rejected diagnostic was changed", err)
				}
			}
		})
	}
}
