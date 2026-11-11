//go:build darwin || linux

package updatehelper

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/vult-ai/vult/internal/updatetxn"
)

func TestUndoPutsBackTheLastUpdate(t *testing.T) {
	mc := newMachine(t)
	if _, err := Undo(context.Background(), mc.m.layout, mc.m, mc.plan.ID); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("undo before any update: %v", err)
	}
	if outcome, err := mc.engine().Run(mc.plan); err != nil || outcome != updatetxn.OutcomeInstalled {
		t.Fatalf("Run = %q, %v", outcome, err)
	}
	// Something recorded on the new version is lost by undo, as the owner
	// is told before confirming.
	d := mc.m.db()
	d["facts"] = "changed after the update"
	mc.m.setDB(d)
	mc.m.opened = false // going back is deliberate here
	installed, err := Undo(context.Background(), mc.m.layout, mc.m, mc.plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Version != "0.6.0" || mc.m.live() != "old" || mc.m.db()["schema"] != "0007" || mc.m.db()["facts"] != "example" || !mc.m.running {
		t.Fatalf("after undo: %+v, program %s, db %v", installed, mc.m.live(), mc.m.db())
	}
	if _, err := os.Stat(mc.m.layout.Undo()); err == nil {
		t.Fatal("undo can be repeated")
	}
	if _, err := Undo(context.Background(), mc.m.layout, mc.m, mc.plan.ID); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("second undo: %v", err)
	}
}

// An undo interrupted by a crash is finished when the helper next starts.
func TestInterruptedUndoIsFinishedAtStart(t *testing.T) {
	for _, point := range []string{"stop", "Uninstall:after", "RestoreData:before", "RestoreData:after", "Record:after", "start"} {
		t.Run(point, func(t *testing.T) {
			mc := newMachine(t)
			if _, err := mc.engine().Run(mc.plan); err != nil {
				t.Fatal(err)
			}
			mc.m.opened = false
			mc.m.crashAt = point
			if _, _, crashed := call(func() (string, error) {
				_, err := undo(mc.m.layout, crashingSteps{&Steps{Layout: mc.m.layout, System: mc.m}, mc.m}, mc.plan.ID)
				return "", err
			}); !crashed {
				t.Fatal("not on the undo path")
			}
			if _, err := mc.engine().Recover(); err != nil {
				t.Fatal(err)
			}
			if mc.m.live() != "old" || mc.m.db()["schema"] != "0007" || !mc.m.running {
				t.Fatalf("program %s, db %v, running %v", mc.m.live(), mc.m.db(), mc.m.running)
			}
		})
	}
}

func TestUndoAfterFailedStartupRequiresTheConfirmedUpdate(t *testing.T) {
	mc := newMachine(t)
	mc.m.failAt = "healthy"
	if _, err := mc.engine().Run(mc.plan); err == nil {
		t.Fatal("startup failure was hidden")
	}
	if _, err := Undo(context.Background(), mc.m.layout, mc.m, "another-update"); err == nil {
		t.Fatal("undo accepted confirmation for a different update")
	}
	if mc.m.live() != "new" {
		t.Fatal("the wrong confirmation changed the program")
	}
	mc.m.opened = false // the administrator explicitly accepts losing later writes
	if _, err := Undo(context.Background(), mc.m.layout, mc.m, mc.plan.ID); err != nil {
		t.Fatal(err)
	}
	if mc.m.live() != "old" || mc.m.db()["schema"] != "0007" {
		t.Fatal("the failed update could not be explicitly undone")
	}
}

func TestFailedLaterUpdateCannotOfferAnOverwrittenUndoProgram(t *testing.T) {
	mc := newMachine(t)
	if _, err := mc.engine().Run(mc.plan); err != nil {
		t.Fatal(err)
	}
	if _, err := LastUpdate(mc.m.layout); err != nil {
		t.Fatal(err)
	}
	next := mc.plan
	next.ID = "20261112T120000Z-0a1b2c3d"
	next.Previous = mc.plan.Next
	next.Next = updatetxn.Installation{Version: "0.7.1", SHA256: "cc", Schema: "0008"}
	next.Artifact = releaseArchive(t, mc.m.layout.Staging())
	mc.m.failAt = "maintenance migrate"
	if outcome, err := mc.engine().Run(next); err != nil || outcome != updatetxn.OutcomeRestored {
		t.Fatalf("%s, %v", outcome, err)
	}
	if _, err := LastUpdate(mc.m.layout); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("offered an older undo after its program was replaced: %v", err)
	}
}

func TestPackageReinstallCannotReuseAnEarlierUndo(t *testing.T) {
	mc := newMachine(t)
	if _, err := mc.engine().Run(mc.plan); err != nil {
		t.Fatal(err)
	}
	installed := mc.plan.Next
	installed.SHA256 = "package"
	if err := (&Steps{Layout: mc.m.layout, System: mc.m}).Record(installed); err != nil {
		t.Fatal(err)
	}
	if _, err := Undo(context.Background(), mc.m.layout, mc.m, mc.plan.ID); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("a package reinstall reused an earlier undo: %v", err)
	}
	if mc.m.live() != "new" || mc.m.db()["schema"] != "0008" || !mc.m.running {
		t.Fatal("refusing stale undo changed Vult")
	}
}
