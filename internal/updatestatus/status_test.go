package updatestatus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/updatehistory"
	"github.com/vult-ai/vult/internal/updatetxn"
)

func put(t *testing.T, path, data string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReportWithoutUpdates(t *testing.T) {
	r := Read(filepath.Join(t.TempDir(), "missing"))
	if r.Enabled || r.Summary() != "this installation is updated with vult update" {
		t.Fatalf("%+v", r)
	}
}

func TestReportReadsEverythingOffline(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "installed.json"), `{"version":"0.7.0","sha256":"aa","schema":"0008_write_gate.sql"}`)
	log := &updatehistory.Log{Path: filepath.Join(dir, "history.jsonl")}
	for _, phase := range []string{"planned", "idle"} {
		log.Append(updatehistory.Event{Phase: phase})
	}
	put(t, filepath.Join(dir, "status.json"), `{"updated":"2026-11-10T12:00:00Z","state":"idle","installed":"0.7.0","history_head":"`+updatehistory.Head(log.Path)+`"}`)
	os.MkdirAll(filepath.Join(dir, "journal"), 0o755)
	r := Read(dir)
	if !r.Enabled || r.Installed.Version != "0.7.0" || r.State != "idle" || len(r.History) != 2 || len(r.Problems) != 0 {
		t.Fatalf("%+v", r)
	}
	if r.Summary() != "0.7.0 is installed" {
		t.Fatalf("summary %q", r.Summary())
	}
}

func TestReportShowsProblems(t *testing.T) {
	dir := t.TempDir()
	log := &updatehistory.Log{Path: filepath.Join(dir, "history.jsonl")}
	log.Append(updatehistory.Event{Phase: "planned"})
	head := updatehistory.Head(log.Path)
	log.Append(updatehistory.Event{Phase: "fenced"})
	put(t, filepath.Join(dir, "status.json"), `{"updated":"2026-11-10T12:00:00Z","state":"idle","installed":"0.7.0","history_head":"`+head+`"}`)
	j := &updatetxn.Journal{Dir: filepath.Join(dir, "journal")}
	os.MkdirAll(j.Dir, 0o755)
	plan := updatetxn.Plan{ID: "x", Artifact: "a", Previous: updatetxn.Installation{Version: "0.7.0"}, Next: updatetxn.Installation{Version: "0.7.1", SHA256: "bb"}}
	j.Write(updatetxn.Record{State: updatetxn.StateMigrated, Plan: &plan, Updated: time.Now()})
	put(t, filepath.Join(dir, "journal", "fence"), "x\n")
	r := Read(dir)
	text := strings.Join(r.Problems, "\n")
	for _, want := range []string{"no installed record", "stopped at \"migrated\"", "doesn't end where"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %q", want, text)
		}
	}
	if !r.Fenced || r.Installing != "0.7.1" || !strings.HasPrefix(r.Summary(), "updating to 0.7.1") {
		t.Fatalf("%+v", r)
	}
}

func TestRequestIsWrittenInPlaceReadAndCleared(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadRequest(dir); ok {
		t.Fatal("no file should mean no request")
	}
	req := Request{Version: "0.7.1", Requested: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)}
	if err := WriteRequest(dir, req); err == nil {
		t.Fatal("wrote a request file the installer never created")
	}
	put(t, filepath.Join(dir, "request.json"), "")
	if err := WriteRequest(dir, req); err != nil {
		t.Fatal(err)
	}
	if got, ok := ReadRequest(dir); !ok || got.Version != "0.7.1" {
		t.Fatalf("read %+v, %v", got, ok)
	}
	if r := Read(dir); r.Requested != "0.7.1" || !strings.Contains(r.Summary(), "asked for") {
		t.Fatalf("report %+v", r)
	}
	if err := ClearRequest(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadRequest(dir); ok {
		t.Fatal("a cleared request was read")
	}
	if err := WriteRequest(dir, Request{Version: "../0.7.1"}); err == nil {
		t.Fatal("accepted a version that isn't one")
	}
	put(t, filepath.Join(dir, "request.json"), `{"version":"0.7.1","requested":"2026-11-12T09:00:00Z","skip_checks":true}`)
	if _, ok := ReadRequest(dir); ok {
		t.Fatal("an unexpected field was accepted")
	}
	os.Remove(filepath.Join(dir, "request.json"))
	os.Symlink("/etc/hosts", filepath.Join(dir, "request.json"))
	if err := WriteRequest(dir, req); err == nil {
		t.Fatal("wrote through a link")
	}
	if _, ok := ReadRequest(dir); ok {
		t.Fatal("read through a link")
	}
}
