//go:build vult_updates

package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/internal/updatetxn"
)

func TestInstallerRequestsDiscoveryAndReadsTheResult(t *testing.T) {
	dir := t.TempDir()
	data, _ := json.Marshal(updatetxn.Installation{Version: "0.7.0", Schema: "0008"})
	if err := os.WriteFile(filepath.Join(dir, "installed.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "check.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	files := updaterFiles{dir}
	if report := files.Report(); !report.Checking || !updatestatus.CheckRequested(dir) {
		t.Fatalf("no discovery was requested: %+v", report)
	}
	if err := updatestatus.WriteReleaseStatus(dir, updatestatus.ReleaseStatus{Current: "0.7.0", Latest: "0.7.1", Checked: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := updatestatus.ClearCheck(dir); err != nil {
		t.Fatal(err)
	}
	if report := files.Report(); report.Checking || report.Release == nil || report.Release.Latest != "0.7.1" || updatestatus.CheckRequested(dir) {
		t.Fatalf("the completed discovery was not reused: %+v", report)
	}
}
