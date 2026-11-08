package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/importarchive"
)

type closedStatus struct{}

func (closedStatus) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestOfflineImportInspectionNeedsNoDatabaseAndPreservesInput(t *testing.T) {
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	file, err := w.Create("source.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(`{"schema":1,"book":{},"accounts":[],"records":[],"artifacts":[]}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	input, output := filepath.Join(dir, "synthetic-source.zip"), filepath.Join(dir, "inspection.json")
	if err := os.WriteFile(input, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var status bytes.Buffer
	if err := run(context.Background(), []string{"inspect-import", "--archive", input, "--output", output}, &status); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("inspection report is not private", err)
	}
	data, err := os.ReadFile(output)
	var report importarchive.Report
	if err != nil || json.Unmarshal(data, &report) != nil || report.AccountingStatus != "not_checked" || report.SourceSHA256 == "" || report.ArchiveSHA256 == "" {
		t.Fatal("inspection report claimed unsupported accounting", err)
	}
	after, err := os.ReadFile(input)
	if err != nil || !bytes.Equal(after, buffer.Bytes()) {
		t.Fatal("inspection modified the original", err)
	}
	if strings.Contains(status.String(), input) || strings.Contains(status.String(), output) {
		t.Fatal("private paths escaped to status output")
	}
	if err := run(context.Background(), []string{"inspect-import", "--archive", input, "--output", output}, &status); err == nil {
		t.Fatal("inspection overwrote an existing report")
	}
	second := filepath.Join(dir, "second-inspection.json")
	if err := run(context.Background(), []string{"inspect-import", "--archive", input, "--output", second}, closedStatus{}); err != nil {
		t.Fatal("lost status output changed a completed inspection outcome", err)
	}
	secondData, err := os.ReadFile(second)
	if err != nil || !bytes.Equal(secondData, data) {
		t.Fatal("closed status writer lost the durable private report", err)
	}
}

func TestOfflineImportInspectionCleansFailureAndRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	input, output := filepath.Join(dir, "private-source.zip"), filepath.Join(dir, "report.json")
	if err := os.WriteFile(input, []byte("invalid synthetic package"), 0600); err != nil {
		t.Fatal(err)
	}
	var status bytes.Buffer
	err := run(context.Background(), []string{"inspect-import", "--archive", input, "--output", output}, &status)
	if err == nil || strings.Contains(err.Error(), input) || strings.Contains(err.Error(), "invalid synthetic package") {
		t.Fatal("bad package leaked its path or contents")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("a failed inspection published a report", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("failed inspection retained scratch output", err)
	}
	link := filepath.Join(dir, "linked.zip")
	if err := os.Symlink(input, link); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"inspect-import", "--archive", link, "--output", output}, &status); err == nil {
		t.Fatal("linked input was followed")
	}
}
