package local

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLinuxLocalPathsSeparateProgramsFromRetainedData(t *testing.T) {
	for _, path := range []string{BinaryPath, PostgresPath} {
		if !strings.HasPrefix(path, "/opt/vult/") || filepath.Clean(path) != path {
			t.Fatal("program path is outside the protected installation", path)
		}
	}
	for _, path := range []string{DataPath, BrowserPath, SocketPath, PostgresSocketPath} {
		if !strings.HasPrefix(path, "/var/lib/vult/") || filepath.Clean(path) != path {
			t.Fatal("retained path is outside private service state", path)
		}
	}
	if DataPath == BrowserPath || PostgresSocketPath == filepath.Dir(SocketPath) || ServiceUser == BrowserUser || ServiceUser == "root" || BrowserUser == "root" {
		t.Fatal("local authority boundaries overlap")
	}
	if len(SocketPath) >= 108 {
		t.Fatal("socket path exceeds the Linux pathname socket bound")
	}
}
