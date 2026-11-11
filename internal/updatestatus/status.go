// Package updatestatus reads what the updater has recorded, straight from
// its files: no network, no ledger, no database. It works when Vult itself
// can't start, which is when it's most needed.
package updatestatus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/vult-ai/vult/internal/updatehistory"
	"github.com/vult-ai/vult/internal/updatetxn"
)

// Report is everything the diagnostic shows.
type Report struct {
	Enabled     bool                    `json:"enabled"` // this installation updates itself
	Installed   *updatetxn.Installation `json:"installed,omitempty"`
	State       string                  `json:"state,omitempty"` // the transaction state, idle when none
	Installing  string                  `json:"installing,omitempty"`
	LastOutcome string                  `json:"last_outcome,omitempty"`
	LastReason  string                  `json:"last_reason,omitempty"`
	Fenced      bool                    `json:"fenced"` // writes are stopped for an update
	Helper      *HelperStatus           `json:"helper,omitempty"`
	History     []updatehistory.Event   `json:"history,omitempty"`   // newest last, at most RecentEvents
	Requested   string                  `json:"requested,omitempty"` // a version the owner asked to install
	Release     *ReleaseStatus          `json:"release,omitempty"`
	Checking    bool                    `json:"checking,omitempty"`
	Problems    []string                `json:"problems,omitempty"`
}

// HelperStatus is the helper's last status.
type HelperStatus struct {
	Updated     time.Time `json:"updated"`
	State       string    `json:"state"`
	Outcome     string    `json:"outcome,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Installed   string    `json:"installed"`
	Available   string    `json:"available,omitempty"`
	HistoryHead string    `json:"history_head,omitempty"`
}

// RecentEvents is how much history the report carries.
const RecentEvents = 20

func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(data) > 1<<20 {
		return true, errors.New("too large")
	}
	return true, json.Unmarshal(data, v)
}

// Read builds the report from the updater's directory. Unreadable files are
// problems in the report, not errors: the diagnostic always says something.
func Read(dir string) Report {
	var r Report
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return r
	}
	r.Enabled = true
	var release ReleaseStatus
	if ok, err := readJSON(filepath.Join(dir, "release.json"), &release); err == nil && ok {
		r.Release = &release
	}
	if req, ok := ReadRequest(dir); ok {
		r.Requested = req.Version
	}
	problem := func(format string, args ...any) { r.Problems = append(r.Problems, fmt.Sprintf(format, args...)) }

	var installed updatetxn.Installation
	if ok, err := readJSON(filepath.Join(dir, "installed.json"), &installed); err != nil {
		problem("the installed record is not readable: %v", err)
	} else if ok {
		r.Installed = &installed
	} else {
		problem("there is no installed record; reinstall Vult to repair it")
	}

	journal := updatetxn.Journal{Dir: filepath.Join(dir, "journal")}
	if rec, err := journal.Read(); err != nil {
		problem("the update journal is not readable: %v", err)
	} else {
		r.State, r.LastOutcome, r.LastReason = string(rec.State), rec.Outcome, rec.Reason
		if rec.Plan != nil {
			r.Installing = rec.Plan.Next.Version
		}
		if rec.State != updatetxn.StateIdle {
			problem("an update to %s stopped at %q; the helper finishes or undoes it when it next runs (at boot, or `sudo launchctl kickstart system/ai.vult.update-helper`)", r.Installing, rec.State)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "journal", "fence")); err == nil {
		r.Fenced = true
	}

	var helper HelperStatus
	if ok, err := readJSON(filepath.Join(dir, "status.json"), &helper); err != nil {
		problem("the helper's status is not readable: %v", err)
	} else if ok {
		r.Helper = &helper
	}

	history := filepath.Join(dir, "history.jsonl")
	events, err := updatehistory.Read(history)
	if err != nil {
		problem("the update history was changed outside Vult: %v", err)
	}
	if r.Helper != nil && r.Helper.HistoryHead != "" && r.Helper.HistoryHead != updatehistory.Head(history) {
		problem("the update history doesn't end where the helper last wrote it")
	}
	if len(events) > RecentEvents {
		events = events[len(events)-RecentEvents:]
	}
	r.History = events
	return r
}

// Summary is one line for people.
func (r Report) Summary() string {
	switch {
	case !r.Enabled:
		return "this installation is updated with vult update"
	case r.State != "" && r.State != string(updatetxn.StateIdle):
		return fmt.Sprintf("updating to %s: %s", r.Installing, r.State)
	case r.Requested != "":
		return fmt.Sprintf("an update to %s was asked for and starts shortly", r.Requested)
	case r.LastOutcome == updatetxn.OutcomeRestored || r.LastOutcome == updatetxn.OutcomeFailed:
		return "the last update didn't finish; the previous version is running"
	case r.Installed != nil:
		return fmt.Sprintf("%s is installed", r.Installed.Version)
	}
	return "status unknown"
}
