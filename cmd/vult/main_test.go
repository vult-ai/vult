package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicHelpAndVersionNeedNoInstallation(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--version"}} {
		var out, errout bytes.Buffer
		if e := run(args, &out, &errout); e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(out.String(), "0.2.0") || errout.Len() != 0 {
			t.Fatal("invalid help or version output")
		}
	}
}
func TestMalformedCommandsFailBeforeServiceAccess(t *testing.T) {
	for _, args := range [][]string{{"help", "extra"}, {"--version", "extra"}, {"_authorize", "bad"}, {"_stop", "extra"}, {"_uninstall", "extra"}, {"uninstall", "--data"}, {"uninstall", "--purge"}, {"unknown"}, {"book", "create", "--name", "Example"}, {"connection", "create", "--book", "bad"}, {"mcp", "--connection", "bad"}, {"period", "close", "--book", "bad"}, {"restore"}, {"backup"}, {"export", "--book", "bad"}} {
		var out, errout bytes.Buffer
		if e := run(args, &out, &errout); e == nil || out.Len() != 0 {
			t.Fatal("invalid command succeeded", args)
		}
	}
}
func TestExportsNeverOverwriteExistingFilesOrSymlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "export.json")
	if e := writeNew(path, []byte("first")); e != nil {
		t.Fatal(e)
	}
	if e := writeNew(path, []byte("second")); e == nil {
		t.Fatal("overwrote an existing file")
	}
	link := filepath.Join(dir, "link.json")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if e := writeNew(link, []byte("third")); e == nil {
		t.Fatal("followed an output symlink")
	}
	b, e := os.ReadFile(path)
	if e != nil || string(b) != "first" {
		t.Fatal("output changed")
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("output permissions are unsafe")
	}
}
