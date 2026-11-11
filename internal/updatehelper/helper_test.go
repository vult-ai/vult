//go:build darwin || linux

package updatehelper

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/updatehistory"
	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/internal/updatetxn"
)

func helperFor(mc *machine, request string) *Helper {
	return &Helper{Layout: mc.m.layout, System: mc.m, Platform: "macos-arm64", OSVersion: "26.1", Version: "0.7.0", Request: request,
		Now: func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }}
}

// At start-up the helper finishes or undoes an interrupted update, with or
// without a request.
func TestRunFinishesAnInterruptedUpdate(t *testing.T) {
	mc := newMachine(t)
	mc.m.crashAt = "Migrate:after"
	call(func() (string, error) { return mc.engine().Run(mc.plan) })
	mc.m.running = false
	status, err := helperFor(mc, t.TempDir()).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != string(updatetxn.StateIdle) || status.Installed != "0.7.0" {
		t.Fatalf("status %+v", status)
	}
	if mc.settled(t) != "new" {
		t.Fatal("the interrupted update was not finished")
	}
	if p, err := LastUpdate(mc.m.layout); err != nil || p.ID != mc.plan.ID {
		t.Fatalf("recovered update cannot be undone: %s, %v", p.ID, err)
	}
	events, err := updatehistory.Read(mc.m.layout.History())
	if err != nil || len(events) == 0 || status.HistoryHead != updatehistory.Head(mc.m.layout.History()) {
		t.Fatalf("history %d events, %v", len(events), err)
	}
}

// Nothing happens unless the owner asks.
func TestRunWithoutARequestChangesNothing(t *testing.T) {
	mc := newMachine(t)
	status, err := helperFor(mc, t.TempDir()).Run(context.Background())
	if err != nil || status.Installed != "0.6.0" || mc.m.live() != "old" {
		t.Fatalf("%+v, %v", status, err)
	}
}

// A request is used once: if the release can't be verified, nothing
// changes, the reason is recorded, and the owner presses the button again.
func TestRequestIsTakenOnceAndUnverifiableUpdatesChangeNothing(t *testing.T) {
	mc := newMachine(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "request.json"), nil, 0o644)
	if err := updatestatus.WriteRequest(dir, updatestatus.Request{Version: "0.7.1"}); err != nil {
		t.Fatal(err)
	}
	h := helperFor(mc, dir)
	h.Trust.MetadataDir = filepath.Join(t.TempDir(), "metadata") // no trusted root
	status, err := h.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.Reason, "couldn't be verified") || mc.m.live() != "old" || !mc.m.running {
		t.Fatalf("%+v", status)
	}
	if _, ok := updatestatus.ReadRequest(dir); ok {
		t.Fatal("the request was left to be retried without the owner")
	}
	events, _ := updatehistory.Read(mc.m.layout.History())
	if len(events) == 0 || events[len(events)-1].Outcome != updatetxn.OutcomeDeferred {
		t.Fatalf("history %+v", events)
	}
}
