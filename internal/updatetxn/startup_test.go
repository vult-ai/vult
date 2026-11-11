package updatetxn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStartupBlockSurvivesRestart(t *testing.T) {
	j := &Journal{Dir: t.TempDir()}
	if err := j.StartupReady(); err != nil {
		t.Fatal(err)
	}
	if err := j.BlockStartup(); err != nil {
		t.Fatal(err)
	}
	restarted := &Journal{Dir: j.Dir}
	if err := restarted.StartupReady(); err == nil {
		t.Fatal("services could start before recovery")
	}
	if err := restarted.AllowStartup(); err != nil {
		t.Fatal(err)
	}
	if err := restarted.StartupReady(); err != nil {
		t.Fatal(err)
	}
}

func TestStartupBlockFailsClosed(t *testing.T) {
	j := &Journal{Dir: filepath.Join(t.TempDir(), "not-a-directory")}
	if err := os.WriteFile(j.Dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := j.StartupReady(); err == nil {
		t.Fatal("unreadable startup state was allowed")
	}
}

func TestOlderReopeningStateNeverRollsBack(t *testing.T) {
	w := newWorld(t)
	w.live, w.data, w.installed, w.opened = "new", "new", w.plan.Next, true
	if err := w.journal.Write(Record{State: StateReopening, Plan: &w.plan}); err != nil {
		t.Fatal(err)
	}
	w.failAt = "Unfence"
	if _, err := w.engine().Recover(); err == nil {
		t.Fatal("startup failure was hidden")
	}
	if r, _ := w.journal.Read(); r.State != StateOpen {
		t.Fatalf("recovery left %s", r.State)
	}
	if _, err := w.engine().Recover(); err != nil {
		t.Fatal(err)
	}
	if w.settled() != "new" {
		t.Fatal("went back after writes could have been accepted")
	}
}
