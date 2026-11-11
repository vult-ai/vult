//go:build darwin || linux

package updatehelper

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/internal/updatecheck"
	"github.com/vult-ai/vult/internal/updatehistory"
	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/internal/updatetrust"
	"github.com/vult-ai/vult/internal/updatetxn"
)

// Status is what the helper last did, for the app and the diagnostic.
type Status struct {
	Updated     time.Time `json:"updated"`
	State       string    `json:"state"`             // a transaction state, or idle
	Outcome     string    `json:"outcome,omitempty"` // how the last update ended
	Reason      string    `json:"reason,omitempty"`
	Installed   string    `json:"installed"`
	Available   string    `json:"available,omitempty"` // the version being installed
	HistoryHead string    `json:"history_head,omitempty"`
}

// Helper is one run of the privileged helper.
type Helper struct {
	Layout    Layout
	System    System
	Trust     updatetrust.Config // the helper's own trust store
	Platform  string
	OSVersion string
	Version   string // this helper's version
	// Request is the folder holding the owner's install request (the
	// updater's directory).
	Request string
	// CheckRelease, when set, makes a last platform check of the downloaded
	// release before anything changes (on Ubuntu, that system files stay
	// the same).
	CheckRelease func(artifact string) error
	Now          func() time.Time
	Rand         io.Reader
}

func (h *Helper) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// Installed reads the installed record.
func Installed(l Layout) (updatetxn.Installation, error) {
	var i updatetxn.Installation
	data, err := os.ReadFile(l.Installed())
	if err != nil {
		return i, err
	}
	if err := strictjson.DecodeNonNull(data, &i, ""); err != nil || i.Version == "" {
		return i, errors.New("the installed record is not readable")
	}
	return i, nil
}

func (h *Helper) newID() (string, error) {
	source := h.Rand
	if source == nil {
		source = rand.Reader
	}
	var b [4]byte
	if _, err := io.ReadFull(source, b[:]); err != nil {
		return "", fmt.Errorf("could not create an update identifier: %w", err)
	}
	return h.now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:]), nil
}

// Run finishes or undoes any interrupted update, then, if the owner asked
// for one, installs the newest verified release. It holds the journal lock
// throughout.
func (h *Helper) Run(ctx context.Context) (Status, error) {
	for _, dir := range []string{h.Layout.State, h.Layout.Journal(), h.Layout.Staging(), h.Layout.Copies} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Status{}, err
		}
	}
	journal := &updatetxn.Journal{Dir: h.Layout.Journal()}
	unlock, err := journal.Lock()
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	history := &updatehistory.Log{Path: h.Layout.History(), Now: h.now}
	installed, err := Installed(h.Layout)
	if err != nil {
		return Status{}, fmt.Errorf("the installed record is unavailable: %w", err)
	}
	steps := &Steps{Layout: h.Layout, System: h.System, Ctx: ctx}
	engine := &updatetxn.Engine{Journal: journal, Steps: steps, Now: h.now, OnChange: func(r updatetxn.Record) {
		e := updatehistory.Event{Phase: string(r.State), Outcome: r.Outcome, Reason: r.Reason, Installed: installed.Version, Helper: h.Version}
		if r.Plan != nil {
			e.Transaction, e.Available, e.Digest = r.Plan.ID, r.Plan.Next.Version, r.Plan.Next.SHA256
			e.SchemaFrom, e.SchemaTo = r.Plan.Previous.Schema, r.Plan.Next.Schema
		}
		history.Append(e)
		h.writeStatus(r, installed.Version, "")
	}}
	if _, err := engine.Recover(); err != nil {
		return h.status(journal, installed.Version, err.Error()), err
	}
	if installed, err = Installed(h.Layout); err != nil {
		return Status{}, err
	}
	request, asked := updatestatus.ReadRequest(h.Request)
	check := updatestatus.CheckRequested(h.Request)
	if check {
		defer updatestatus.ClearCheck(h.Request)
	}
	if !asked && !check {
		return h.status(journal, installed.Version, ""), nil
	}
	// One request, one attempt: the owner presses the button again to retry.
	if asked {
		if err := updatestatus.ClearRequest(h.Request); err != nil {
			return h.status(journal, installed.Version, err.Error()), err
		}
	}
	client, release, lookupErr := h.latest(installed)
	if !asked {
		// An install request may arrive while discovery is fetching metadata.
		// Consume it here instead of relying on a second filesystem event.
		request, asked = updatestatus.ReadRequest(h.Request)
		if !asked {
			if lookupErr != nil {
				return h.status(journal, installed.Version, lookupErr.Error()), lookupErr
			}
			return h.status(journal, installed.Version, ""), nil
		}
		if err := updatestatus.ClearRequest(h.Request); err != nil {
			return h.status(journal, installed.Version, err.Error()), err
		}
	}
	defer0 := func(code, reason string) (Status, error) {
		history.Append(updatehistory.Event{Phase: "idle", Outcome: updatetxn.OutcomeDeferred, Code: code, Reason: reason, Installed: installed.Version, Available: request.Version, Helper: h.Version})
		return h.status(journal, installed.Version, reason), nil
	}
	if lookupErr != nil {
		return defer0("not_ready", lookupErr.Error())
	}
	if release == nil {
		return defer0("not_ready", "Vult is up to date")
	}
	held, err := engine.Held(release.Contract.Version)
	if err != nil {
		return h.status(journal, installed.Version, err.Error()), err
	}
	if held {
		return defer0("held", "this release failed here three times; Vult waits for a newer one")
	}
	plan, err := h.prepare(installed, client, release)
	if err != nil {
		return defer0("not_ready", err.Error())
	}
	_, err = engine.Run(plan)
	if err != nil {
		return h.status(journal, installed.Version, err.Error()), err
	}
	now, _ := Installed(h.Layout)
	return h.status(journal, now.Version, ""), nil
}

// prepare downloads the verified release into the helper's own staging and
// checks the program archive before anything changes.
func (h *Helper) prepare(installed updatetxn.Installation, client *updatetrust.Client, r *updatetrust.Release) (updatetxn.Plan, error) {
	id, err := h.newID()
	if err != nil {
		return updatetxn.Plan{}, err
	}
	c := r.Contract
	// A crash may leave this transaction's incomplete download behind.
	// Run holds the staging directory's journal lock, so no download is active.
	partial := filepath.Join(h.Layout.Staging(), r.SHA256+"."+path.Base(r.Name)+".partial")
	if err := os.Remove(partial); err != nil && !errors.Is(err, os.ErrNotExist) {
		return updatetxn.Plan{}, err
	}
	artifact, err := client.Download(*r, h.Layout.Staging())
	if err != nil {
		return updatetxn.Plan{}, fmt.Errorf("the download didn't match its signed fingerprint: %w", err)
	}
	if h.Layout.ArchivePrefix == "" {
		if err := updatecheck.ValidateArchive(artifact); err != nil {
			os.Remove(artifact)
			return updatetxn.Plan{}, err
		}
	}
	if h.CheckRelease != nil {
		if err := h.CheckRelease(artifact); err != nil {
			os.Remove(artifact)
			return updatetxn.Plan{}, err
		}
	}
	return updatetxn.Plan{
		ID: id, Artifact: artifact, Previous: installed,
		Next: updatetxn.Installation{Version: c.Version, SHA256: r.SHA256, Schema: c.SchemaTo},
	}, nil
}

// latest fetches only signed metadata. Both discovery and installation use
// the same verification and compatibility checks; discovery never stops Vult.
func (h *Helper) latest(installed updatetxn.Installation) (client *updatetrust.Client, release *updatetrust.Release, err error) {
	defer func() {
		status := updatestatus.ReleaseStatus{Current: installed.Version, Checked: h.now().UTC()}
		if err != nil {
			status.Reason = err.Error()
		} else if release != nil {
			status.Latest = release.Contract.Version
		}
		if writeErr := updatestatus.WriteReleaseStatus(h.Layout.State, status); writeErr != nil && err == nil {
			err = writeErr
		}
	}()
	client, err = updatetrust.Open(h.Trust)
	if err != nil {
		return nil, nil, fmt.Errorf("the update information couldn't be verified: %w", err)
	}
	r, _, err := client.Latest(h.Platform, installed.Version)
	if err != nil {
		return nil, nil, fmt.Errorf("the update information couldn't be verified: %w", err)
	}
	if r == nil {
		return client, nil, nil
	}
	c := r.Contract
	if c.MinimumOS != "" && !updatecheck.AtLeast(h.OSVersion, c.MinimumOS) {
		return nil, nil, fmt.Errorf("Vult %s needs an operating system at %s or later", c.Version, c.MinimumOS)
	}
	if updatetrust.Newer(h.Version, c.MinimumUpdater) {
		return nil, nil, fmt.Errorf("Vult %s needs a newer updater first", c.Version)
	}
	if !slices.Contains(c.SchemaFrom, installed.Schema) {
		return nil, nil, fmt.Errorf("Vult %s can't update a database at %s; install an earlier release first", c.Version, installed.Schema)
	}
	return client, r, nil
}

func (h *Helper) status(j *updatetxn.Journal, installed, reason string) Status {
	r, err := j.Read()
	if err != nil {
		r = updatetxn.Record{State: "unknown", Reason: err.Error()}
	}
	return h.writeStatus(r, installed, reason)
}

func (h *Helper) writeStatus(r updatetxn.Record, installed, reason string) Status {
	s := Status{Updated: h.now().UTC(), State: string(r.State), Outcome: r.Outcome, Reason: r.Reason, Installed: installed}
	if reason != "" {
		s.Reason = reason
	}
	if r.Plan != nil {
		s.Available = r.Plan.Next.Version
	}
	s.HistoryHead = updatehistory.Head(h.Layout.History())
	if data, err := json.Marshal(s); err == nil {
		updatetxn.WriteDurably(h.Layout.Status(), data, 0o644)
	}
	return s
}
