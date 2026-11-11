//go:build darwin || linux

package updatehelper

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/internal/updatetxn"
)

// Undo returns the most recent update's program and copy of the data,
// which stay until the next update.
func (l Layout) Undo() string { return filepath.Join(l.State, "undo.json") }

// ErrNothingToUndo means no update can be put back.
var ErrNothingToUndo = errors.New("there is no update to undo")

// LastUpdate reads what undo would put back.
func LastUpdate(l Layout) (updatetxn.Plan, error) {
	r, err := (&updatetxn.Journal{Dir: l.Journal()}).Read()
	if err != nil {
		return updatetxn.Plan{}, err
	}
	var p updatetxn.Plan
	switch r.State {
	case updatetxn.StateOpen, updatetxn.StateCleaned, updatetxn.StateReopening:
		// An update that could accept writes is never rolled back
		// automatically, but the administrator can explicitly undo it.
		if r.Plan == nil {
			return p, errors.New("the update journal has no plan")
		}
		p = *r.Plan
	case updatetxn.StateIdle:
		data, err := os.ReadFile(l.Undo())
		if errors.Is(err, os.ErrNotExist) {
			return p, ErrNothingToUndo
		}
		if err != nil {
			return p, err
		}
		if err := strictjson.DecodeNonNull(data, &p, ""); err != nil {
			return p, errors.New("the record of the last update is not readable")
		}
	default:
		return p, updatetxn.ErrBusy
	}
	if p.ID == "" {
		return updatetxn.Plan{}, errors.New("the record of the last update is not readable")
	}
	installed, err := Installed(l)
	if err != nil {
		return p, err
	}
	if installed != p.Next {
		return p, ErrNothingToUndo
	}
	live, err := updatetxn.Release(l.Program)
	if err != nil {
		return p, err
	}
	if live != p.Next.SHA256 {
		return p, ErrNothingToUndo
	}
	if _, err := os.Stat(filepath.Join(l.point(p.ID), completeMark)); err != nil {
		return updatetxn.Plan{}, errors.New("the copy of the data from before the last update is missing")
	}
	return p, nil
}

func rememberUpdate(l Layout, p updatetxn.Plan) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return updatetxn.WriteDurably(l.Undo(), data, 0o600)
}

func forgetUpdate(l Layout) error {
	if err := os.Remove(l.Undo()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return updatetxn.SyncDir(l.State)
}

// Undo puts back the program and data from before the last update. Anything
// recorded since that update is lost; callers confirm that with the owner
// first. It records the intent in the journal before acting, so if it is
// interrupted the helper finishes it at the next start.
func Undo(ctx context.Context, l Layout, sys System, confirmedID string) (updatetxn.Installation, error) {
	return undo(l, &Steps{Layout: l, System: sys, Ctx: ctx}, confirmedID)
}

func undo(l Layout, steps updatetxn.Steps, confirmedID string) (updatetxn.Installation, error) {
	journal := &updatetxn.Journal{Dir: l.Journal()}
	unlock, err := journal.Lock()
	if err != nil {
		return updatetxn.Installation{}, err
	}
	defer unlock()
	current, err := journal.Read()
	if err != nil {
		return updatetxn.Installation{}, err
	}
	p, err := LastUpdate(l)
	if err != nil {
		return updatetxn.Installation{}, err
	}
	if p.ID != confirmedID {
		return updatetxn.Installation{}, errors.New("the last update changed; confirm undo again")
	}
	// Nothing to swap in: the restore swaps the previous program back.
	p.Artifact = filepath.Join(l.Staging(), "none")
	if err := journal.Write(updatetxn.Record{State: updatetxn.StateRestoring, Plan: &p, Reason: "undone by the owner", Attempts: current.Attempts}); err != nil {
		return updatetxn.Installation{}, err
	}
	engine := &updatetxn.Engine{Journal: journal, Steps: steps}
	if _, err := engine.Recover(); err != nil {
		return updatetxn.Installation{}, err
	}
	if err := forgetUpdate(l); err != nil {
		return updatetxn.Installation{}, err
	}
	return Installed(l)
}
