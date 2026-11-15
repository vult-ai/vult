//go:build darwin || linux

package updatehelper

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/recovery"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/internal/updatehistory"
	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/internal/updatetxn"
)

// Putting a backup back, when the owner asks from Vult settings. The app
// checks the owner signed in recently and confirmed; this helper treats the
// request as untrusted and checks the backup itself. It puts back only the
// newest backup, keeps the data it replaces, brings the copy up to the
// installed version and starts Vult again. Everything recorded after the
// backup was taken is lost from the live data, though the replaced data is
// kept beside it.

// restoreMark records a restore in progress, so an interrupted one is
// finished or undone at the next start.
func (l Layout) restoreMark() string { return filepath.Join(l.State, "restoring.json") }

// replacedData is where the data a restore replaced is kept.
func (l Layout) replacedData(job string) string { return l.Data + ".replaced-" + job }

type restoring struct {
	Job      string `json:"job"`
	Backup   string `json:"backup"`
	Replaced string `json:"replaced"`
}

// latestBackup is the newest backup this Vult can put back: one the
// installer took before an upgrade that completed, or the one the Update
// button took before the last update.
func latestBackup(l Layout) (updatestatus.Backup, string, bool) {
	var best updatestatus.Backup
	var dir string
	found := false
	if l.Recovery != "" {
		if p, ok := (recovery.Site{Root: l.Recovery}).Latest(); ok {
			best, dir, found = updatestatus.Backup{ID: p.ID, Kind: "upgrade", Created: p.Created, Version: p.From, To: p.To, Size: p.Size}, filepath.Join(l.Recovery, p.ID), true
		}
	}
	if p, err := LastUpdate(l); err == nil {
		created, err := time.Parse("20060102T150405Z", strings.SplitN(p.ID, "-", 2)[0])
		if err == nil && (!found || created.After(best.Created)) {
			point := l.point(p.ID)
			best = updatestatus.Backup{ID: p.ID, Kind: "update", Created: created, Version: p.Previous.Version, To: p.Next.Version, Size: dataSize(filepath.Join(point, recovery.DataDir))}
			dir, found = point, true
		}
	}
	return best, dir, found
}

func dataSize(dir string) int64 {
	var total int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// RefreshBackups writes the newest backup where the app reads it.
func RefreshBackups(l Layout) error {
	b, _, ok := latestBackup(l)
	if !ok {
		return updatestatus.WriteBackups(l.State, updatestatus.Backups{})
	}
	return updatestatus.WriteBackups(l.State, updatestatus.Backups{Latest: &b})
}

// checkBackup returns the folder holding the backup's copy of the data,
// once the copy checks out. Its database's clean shutdown is checked once
// Vult is stopped.
func (h *Helper) checkBackup(id string) (string, error) {
	b, dir, ok := latestBackup(h.Layout)
	if !ok || b.ID != id {
		return "", errors.New("that backup is no longer the newest one; open Vult settings again")
	}
	if b.Kind == "upgrade" {
		if _, err := (recovery.Site{Root: h.Layout.Recovery}).Open(id); err != nil {
			return "", err
		}
	} else if _, err := os.Stat(filepath.Join(dir, completeMark)); err != nil {
		return "", errors.New("the backup is incomplete")
	}
	return filepath.Join(dir, recovery.DataDir), nil
}

func (h *Helper) restore(ctx context.Context, history *updatehistory.Log, installed string, req updatestatus.Request) error {
	st := updatestatus.RestoreStatus{Job: req.Job, Backup: req.Restore, State: "running", Requested: req.Requested}
	step := func(text string) {
		st.Step, st.Updated = text, h.now().UTC()
		updatestatus.WriteRestoreStatus(h.Request, st)
	}
	finish := func(err error) error {
		st.Step, st.Updated = "", h.now().UTC()
		event := updatehistory.Event{Phase: "restore", Transaction: req.Job, Available: req.Restore, Installed: installed, Helper: h.Version}
		if err != nil {
			st.State, st.Error = "failed", err.Error()
			event.Outcome, event.Reason = "failed", err.Error()
		} else {
			st.State = "done"
			event.Outcome = "restored"
		}
		updatestatus.WriteRestoreStatus(h.Request, st)
		history.Append(event)
		RefreshBackups(h.Layout)
		return err
	}
	history.Append(updatehistory.Event{Phase: "restore", Transaction: req.Job, Code: "requested", Available: req.Restore, Installed: installed, Helper: h.Version})
	step("Checking the backup")
	source, err := h.checkBackup(req.Restore)
	if err != nil {
		return finish(err)
	}
	step("Stopping Vult")
	if err := h.System.StopServices(ctx); err != nil {
		return finish(errors.Join(errors.New("Vult couldn't be stopped, so nothing was changed"), h.System.StartServices(ctx)))
	}
	if err := h.System.DatabaseStopped(h.Layout.Data); err != nil {
		return finish(errors.Join(errors.New("the database didn't stop, so nothing was changed"), h.System.StartServices(ctx)))
	}
	if err := h.System.Clean(source); err != nil {
		return finish(errors.Join(errors.New("the backup's database was not shut down cleanly, so nothing was changed"), h.System.StartServices(ctx)))
	}
	step("Putting the backup back")
	mark := restoring{Job: req.Job, Backup: req.Restore, Replaced: h.Layout.replacedData(req.Job)}
	data, _ := json.Marshal(mark)
	if err := updatetxn.WriteDurably(h.Layout.restoreMark(), data, 0o600); err != nil {
		return finish(errors.Join(err, h.System.StartServices(ctx)))
	}
	if err := os.Rename(h.Layout.Data, mark.Replaced); err != nil {
		os.Remove(h.Layout.restoreMark())
		return finish(errors.Join(errors.New("the data couldn't be set aside, so nothing was changed"), h.System.StartServices(ctx)))
	}
	if err := h.System.Clone(source, h.Layout.Data); err != nil {
		return finish(h.putBack(ctx, mark, errors.New("the backup couldn't be copied, so Vult was left as it was")))
	}
	step("Bringing the backup up to this version")
	if _, err := h.System.Maintenance(ctx, "migrate"); err != nil {
		return finish(h.putBack(ctx, mark, errors.New("the backup couldn't be brought up to this version, so Vult was left as it was")))
	}
	step("Starting Vult")
	os.Remove(h.Layout.restoreMark())
	if err := h.System.StartServices(ctx); err != nil {
		return finish(errors.New("the backup was put back but Vult didn't start; restart this Mac, or ask for help"))
	}
	return finish(nil)
}

// putBack undoes a restore that didn't complete: the data it set aside
// becomes the live data again, and Vult starts.
func (h *Helper) putBack(ctx context.Context, mark restoring, cause error) error {
	if _, err := os.Lstat(mark.Replaced); err == nil {
		failed := mark.Replaced + ".unused"
		if _, err := os.Lstat(h.Layout.Data); err == nil {
			if err := os.Rename(h.Layout.Data, failed); err != nil {
				return errors.Join(cause, err)
			}
		}
		if err := os.Rename(mark.Replaced, h.Layout.Data); err != nil {
			return errors.Join(cause, err)
		}
	}
	os.Remove(h.Layout.restoreMark())
	return errors.Join(cause, h.System.StartServices(ctx))
}

// recoverRestore undoes a restore that was interrupted: the data it set
// aside becomes the live data again, and Vult starts.
func (h *Helper) recoverRestore(ctx context.Context, history *updatehistory.Log, installed string) error {
	data, err := os.ReadFile(h.Layout.restoreMark())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var mark restoring
	if err != nil || strictjson.Decode(data, &mark) != nil || mark.Replaced != h.Layout.replacedData(mark.Job) || !updatestatus.ValidJob(mark.Job) {
		return errors.New("the record of an interrupted restore is not readable")
	}
	st, _ := updatestatus.ReadRestoreStatus(h.Request)
	st.Job, st.Backup, st.Updated, st.Step = mark.Job, mark.Backup, h.now().UTC(), ""
	result := h.putBack(ctx, mark, errors.New("the restore was interrupted, so Vult was left as it was"))
	st.State, st.Error = "failed", result.Error()
	updatestatus.WriteRestoreStatus(h.Request, st)
	history.Append(updatehistory.Event{Phase: "restore", Transaction: mark.Job, Outcome: "failed", Reason: result.Error(), Installed: installed, Helper: h.Version})
	return nil
}
