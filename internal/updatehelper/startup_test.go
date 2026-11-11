//go:build darwin || linux

package updatehelper

import (
	"context"
	"os"
	"testing"

	"github.com/vult-ai/vult/internal/updatetxn"
)

type writingSystem struct{ *fakeMac }

func (m writingSystem) StartServices(ctx context.Context) error {
	if err := m.fakeMac.StartServices(ctx); err != nil {
		return err
	}
	if m.live() == "new" {
		d := m.db()
		d["acknowledged"] = "a synthetic write"
		m.setDB(d)
	}
	return nil
}

func TestFailedStartupPreservesAcknowledgedWritesAndUndo(t *testing.T) {
	mc := newMachine(t)
	mc.m.failAt = "healthy"
	engine := &updatetxn.Engine{Journal: mc.journal, Steps: &Steps{Layout: mc.m.layout, System: writingSystem{mc.m}}}
	if _, err := engine.Run(mc.plan); err == nil {
		t.Fatal("startup failure was hidden")
	}
	if r, _ := mc.journal.Read(); r.State != updatetxn.StateOpen {
		t.Fatalf("journal at %s", r.State)
	}
	if mc.m.live() != "new" || mc.m.db()["acknowledged"] != "a synthetic write" {
		t.Fatal("an acknowledged write was rolled back")
	}
	if p, err := LastUpdate(mc.m.layout); err != nil || p.ID != mc.plan.ID {
		t.Fatalf("undo unavailable after failed startup: %+v, %v", p, err)
	}
	if _, err := engine.Recover(); err != nil {
		t.Fatal(err)
	}
	if mc.m.db()["acknowledged"] != "a synthetic write" {
		t.Fatal("recovery discarded an acknowledged write")
	}
}

func TestUndoIsDurableBeforeServicesCanStart(t *testing.T) {
	mc := newMachine(t)
	engine := mc.engine()
	engine.OnChange = func(r updatetxn.Record) {
		if r.State == updatetxn.StateOpen {
			if err := os.Mkdir(mc.m.layout.Undo(), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := engine.Run(mc.plan); err == nil {
		t.Fatal("an unwritable undo record was ignored")
	}
	if mc.m.running || mc.m.opened {
		t.Fatal("services started without durable undo information")
	}
	if err := mc.journal.StartupReady(); err == nil {
		t.Fatal("startup was unblocked without durable undo information")
	}
	if err := os.Remove(mc.m.layout.Undo()); err != nil {
		t.Fatal(err)
	}
	if _, err := mc.engine().Recover(); err != nil {
		t.Fatal(err)
	}
	if p, err := LastUpdate(mc.m.layout); err != nil || p.ID != mc.plan.ID {
		t.Fatalf("recovery did not preserve undo: %+v, %v", p, err)
	}
}

func TestIncompleteUpdateBlocksServiceStartupAfterReboot(t *testing.T) {
	for _, point := range []string{"Install:after", "Migrate:after", "Record:after", "Unfence:before"} {
		t.Run(point, func(t *testing.T) {
			mc := newMachine(t)
			mc.m.crashAt = point
			if _, _, crashed := call(func() (string, error) { return mc.engine().Run(mc.plan) }); !crashed {
				t.Fatal("crash point was not reached")
			}
			if err := (&updatetxn.Journal{Dir: mc.m.layout.Journal()}).StartupReady(); err == nil {
				t.Fatal("services could race recovery at boot")
			}
			if _, err := mc.engine().Recover(); err != nil {
				t.Fatal(err)
			}
			if err := mc.journal.StartupReady(); err != nil {
				t.Fatal(err)
			}
			mc.settled(t)
		})
	}
}
