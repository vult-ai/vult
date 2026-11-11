//go:build darwin || linux

package updatehelper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func ubuntuArchive(t *testing.T, inventory string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range map[string]string{"opt/vult/vult": "new", "opt/vult/program-files.json": inventory, "etc/systemd/system/ai.example.ledger.service": "unit"} {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	file := filepath.Join(t.TempDir(), "vult.tar.gz")
	os.WriteFile(file, buf.Bytes(), 0o600)
	return file
}

func TestUbuntuUpdatesLeaveSystemFilesAlone(t *testing.T) {
	l := Layout{Program: t.TempDir(), ArchivePrefix: "opt/vult/"}
	installed := `{"version":"0.7.0","external_files":{"/etc/systemd/system/ai.example.ledger.service":"aa","/usr/share/polkit-1/actions/ai.example.policy":"bb"}}`
	os.WriteFile(filepath.Join(l.Program, "program-files.json"), []byte(installed), 0o644)
	check := SameSystemFiles(l)
	same := `{"version":"0.7.1","external_files":{"/etc/systemd/system/ai.example.ledger.service":"aa","/usr/share/polkit-1/actions/ai.example.policy":"bb"}}`
	if err := check(ubuntuArchive(t, same)); err != nil {
		t.Fatalf("same system files: %v", err)
	}
	for name, inventory := range map[string]string{
		"a changed unit": `{"version":"0.7.1","external_files":{"/etc/systemd/system/ai.example.ledger.service":"cc","/usr/share/polkit-1/actions/ai.example.policy":"bb"}}`,
		"a new file":     `{"version":"0.7.1","external_files":{"/etc/systemd/system/ai.example.ledger.service":"aa","/usr/share/polkit-1/actions/ai.example.policy":"bb","/etc/x":"dd"}}`,
		"a removed file": `{"version":"0.7.1","external_files":{"/etc/systemd/system/ai.example.ledger.service":"aa"}}`,
	} {
		if err := check(ubuntuArchive(t, inventory)); !errors.Is(err, ErrNeedsInstaller) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := check(ubuntuArchive(t, "not json")); err == nil || errors.Is(err, ErrNeedsInstaller) {
		t.Errorf("an unreadable inventory: %v", err)
	}
}
