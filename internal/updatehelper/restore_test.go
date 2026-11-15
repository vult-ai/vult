//go:build darwin || linux

package updatehelper

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/recovery"
	"github.com/vult-ai/vult/internal/updatehistory"
	"github.com/vult-ai/vult/internal/updatestatus"
)

const restoreJob = "0123456789abcdef0123456789abcdef"

// restoreMachine is a Vult on the new version whose installer took a
// backup before upgrading, and which has recorded more since.
func restoreMachine(t *testing.T) (*machine, *Helper, string) {
	t.Helper()
	mc := newMachine(t)
	l := &mc.m.layout
	l.Recovery = filepath.Join(filepath.Dir(l.Data), "recovery")
	if err := os.Mkdir(l.Recovery, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(l.Program, "vult"), []byte("new"), 0o755)
	mc.m.running = false
	site := recovery.Site{Root: l.Recovery, Data: l.Data, Clean: mc.m.Clean, Clone: mc.m.Clone, Now: func() time.Time { return time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC) }}
	p, err := site.Take("0.8.0-rc.5", "0.8.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := site.Finish(p.ID); err != nil {
		t.Fatal(err)
	}
	// The upgrade, then a later change the restore loses.
	mc.m.setDB(map[string]string{"schema": "0008", "facts": "example and later"})
	mc.m.running = true
	h := &Helper{Layout: *l, System: mc.m, Request: l.State, Version: "0.8.0", Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }}
	if err := os.WriteFile(filepath.Join(l.State, "request.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RefreshBackups(*l); err != nil {
		t.Fatal(err)
	}
	return mc, h, p.ID
}

func askRestore(t *testing.T, h *Helper, backup string) updatestatus.RestoreStatus {
	t.Helper()
	if err := updatestatus.WriteRestoreRequest(h.Request, backup, restoreJob, time.Now()); err != nil {
		t.Fatal(err)
	}
	h.Run(context.Background())
	st, ok := updatestatus.ReadRestoreStatus(h.Request)
	if !ok || st.Job != restoreJob {
		t.Fatal("no restore status", st)
	}
	if _, asked := updatestatus.ReadRequest(h.Request); asked {
		t.Fatal("the request was not taken")
	}
	return st
}

func restoreEvents(t *testing.T, l Layout) []updatehistory.Event {
	events, err := updatehistory.Read(l.History())
	if err != nil {
		t.Fatal(err)
	}
	var out []updatehistory.Event
	for _, e := range events {
		if e.Phase == "restore" {
			out = append(out, e)
		}
	}
	return out
}

// The owner's restore puts the backup back, brings it up to this version
// and starts Vult; the data it replaced is kept, and the restore is in the
// history outside the database.
func TestRestorePutsTheBackupBackAndKeepsWhatItReplaced(t *testing.T) {
	mc, h, id := restoreMachine(t)
	b, err := updatestatus.ReadBackups(h.Request)
	if err != nil || b == nil || b.ID != id || b.Kind != "upgrade" || b.Version != "0.8.0-rc.5" || b.To != "0.8.0" || b.Size == 0 {
		t.Fatal("the app doesn't see the backup", b, err)
	}
	st := askRestore(t, h, id)
	if st.State != "done" || st.Error != "" {
		t.Fatal(st)
	}
	if d := mc.m.db(); d["facts"] != "example" || d["schema"] != "0008" {
		t.Fatal("the backup was not put back and brought up to date", d)
	}
	if !mc.m.running {
		t.Fatal("Vult was not started again")
	}
	kept, err := os.ReadFile(filepath.Join(h.Layout.replacedData(restoreJob), "db.json"))
	if err != nil || !strings.Contains(string(kept), "example and later") {
		t.Fatal("the replaced data was not kept", err)
	}
	if _, err := os.Stat(h.Layout.restoreMark()); !os.IsNotExist(err) {
		t.Fatal("the restore mark remains")
	}
	events := restoreEvents(t, h.Layout)
	if len(events) != 2 || events[0].Code != "requested" || events[1].Outcome != "restored" || events[1].Transaction != restoreJob {
		t.Fatal("the restore is not in the history", events)
	}
}

// A request for anything but the newest whole backup changes nothing.
func TestRestoreRefusesAMissingOrDamagedBackup(t *testing.T) {
	for _, damage := range []string{"other", "missing", "changed"} {
		t.Run(damage, func(t *testing.T) {
			mc, h, id := restoreMachine(t)
			backup := id
			switch damage {
			case "other":
				backup = "installer-20261101T000000Z-0a0b0c0d"
			case "missing":
				os.RemoveAll(filepath.Join(h.Layout.Recovery, id))
			case "changed":
				os.WriteFile(filepath.Join(h.Layout.Recovery, id, recovery.DataDir, "db.json"), []byte(`{"schema":"0007"}`), 0o600)
			}
			st := askRestore(t, h, backup)
			if st.State != "failed" || st.Error == "" {
				t.Fatal(st)
			}
			if d := mc.m.db(); d["facts"] != "example and later" {
				t.Fatal("the live data changed", d)
			}
			if !mc.m.running {
				t.Fatal("Vult is not running")
			}
		})
	}
}

// If the backup can't be brought up to this version, the live data goes
// back and Vult starts as it was.
func TestRestoreThatCantMigrateLeavesVultAsItWas(t *testing.T) {
	mc, h, id := restoreMachine(t)
	mc.m.failAt = "maintenance migrate"
	st := askRestore(t, h, id)
	if st.State != "failed" || !strings.Contains(st.Error, "left as it was") {
		t.Fatal(st)
	}
	if d := mc.m.db(); d["facts"] != "example and later" {
		t.Fatal("the live data was not put back", d)
	}
	if !mc.m.running {
		t.Fatal("Vult is not running")
	}
}

// A restore interrupted after the data was set aside is undone at the next
// start.
func TestInterruptedRestoreIsUndone(t *testing.T) {
	mc, h, id := restoreMachine(t)
	mc.m.crashAt = "maintenance migrate"
	if err := updatestatus.WriteRestoreRequest(h.Request, id, restoreJob, time.Now()); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("the restore was not interrupted")
			}
		}()
		h.Run(context.Background())
	}()
	mc.m.running = false
	h.Run(context.Background())
	if d := mc.m.db(); d["facts"] != "example and later" {
		t.Fatal("the live data was not put back", d)
	}
	st, _ := updatestatus.ReadRestoreStatus(h.Request)
	if st.State != "failed" || !strings.Contains(st.Error, "interrupted") || !mc.m.running {
		t.Fatal(st, mc.m.running)
	}
	var mark map[string]any
	if data, err := os.ReadFile(h.Layout.restoreMark()); err == nil {
		json.Unmarshal(data, &mark)
		t.Fatal("the restore mark remains", mark)
	}
}
