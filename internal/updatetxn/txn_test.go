package updatetxn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// crash stands for the process dying: the test recovers it and starts a new
// engine on the same journal and world.
type crash struct{ at string }

// world is a model of the machine: which program is live, what the data is,
// and what the services are doing. It checks the transaction's promises as
// it goes.
type world struct {
	t         *testing.T
	plan      Plan
	journal   *Journal
	live      string // "old" or "new"
	data      string // "old" or "new"
	cloneData string // "" when there is no recovery point
	installed Installation
	staged    bool
	fenced    bool
	running   bool
	opened    bool // writes were accepted on the new pair
	restored  bool // the recovery point's data was moved into place

	crashAt   string // "Step:before" or "Step:after"
	failAt    string // step that returns an error
	failErr   error
	migratedE error
	calls     []string
}

func (w *world) hit(step, when string) {
	if w.crashAt == step+":"+when {
		w.crashAt = ""
		panic(crash{step + ":" + when})
	}
}

func (w *world) do(step string, effect func()) error {
	w.calls = append(w.calls, step)
	w.hit(step, "before")
	if w.failAt == step {
		w.failAt = ""
		if w.failErr != nil {
			return w.failErr
		}
		return errors.New(step + " failed")
	}
	effect()
	w.hit(step, "after")
	return nil
}

func (w *world) Discard(Plan) error  { return w.do("Discard", func() { w.staged = false }) }
func (w *world) Precheck(Plan) error { return w.do("Precheck", func() {}) }
func (w *world) Fence(Plan) error {
	return w.do("Fence", func() { w.fenced, w.running = true, false })
}
func (w *world) Unfence(Plan) error {
	return w.do("Unfence", func() {
		consistentOld := w.live == "old" && w.data == "old" && w.installed == w.plan.Previous
		consistentNew := w.live == "new" && w.data == "new" && w.installed == w.plan.Next
		if !consistentOld && !consistentNew {
			w.t.Errorf("writes reopened on a mixed pair: program %s, data %s, recorded %s", w.live, w.data, w.installed.Version)
		}
		if consistentNew {
			if r, err := w.journal.Read(); err != nil || r.State != StateOpen {
				w.t.Errorf("writes started before the durable open state: %s, %v", r.State, err)
			}
			w.opened = true
		}
		w.fenced, w.running = false, true
	})
}
func (w *world) TakeRecoveryPoint(Plan) error {
	return w.do("TakeRecoveryPoint", func() {
		if !w.fenced {
			w.t.Error("recovery point taken while writes were open")
		}
		w.cloneData = w.data
	})
}
func (w *world) DiscardRecoveryPoint(Plan) error {
	return w.do("DiscardRecoveryPoint", func() { w.cloneData = "" })
}
func (w *world) Install(Plan) error {
	return w.do("Install", func() {
		if !w.fenced || w.cloneData == "" {
			w.t.Error("installed without stopping writes and taking a recovery point")
		}
		w.live = "new"
	})
}
func (w *world) Uninstall(Plan) error {
	return w.do("Uninstall", func() {
		if w.opened {
			w.t.Error("went back after writes were accepted on the new pair")
		}
		if w.running || !w.fenced {
			w.t.Error("swapped the program back while services were running")
		}
		w.live = "old"
	})
}
func (w *world) Migrate(Plan) error {
	return w.do("Migrate", func() {
		if w.live != "new" || !w.fenced || w.running {
			w.t.Error("migrated without the new program installed and writes stopped")
		}
		w.data = "new"
	})
}
func (w *world) Migrated(Plan) (bool, error) {
	w.calls = append(w.calls, "Migrated")
	if w.migratedE != nil {
		return false, w.migratedE
	}
	return w.data == "new", nil
}
func (w *world) Record(i Installation) error { return w.do("Record", func() { w.installed = i }) }
func (w *world) RestoreData(Plan) error {
	return w.do("RestoreData", func() {
		if w.opened {
			w.t.Error("restored data after writes were accepted on the new pair")
		}
		if w.running || !w.fenced {
			w.t.Error("replaced the data while services were running")
		}
		if w.restored {
			return
		}
		if w.cloneData == "" {
			w.t.Error("restored data without a recovery point")
		}
		// The recovery point's copy is moved into place, so it is used up.
		w.data, w.cloneData, w.restored = w.cloneData, "", true
	})
}
func (w *world) Cleanup(Plan) error {
	return w.do("Cleanup", func() { w.staged, w.cloneData = false, "" })
}

var stepNames = []string{"Precheck", "Fence", "TakeRecoveryPoint", "Install", "Migrate", "Record", "Unfence", "Cleanup", "Discard", "DiscardRecoveryPoint", "Uninstall", "RestoreData"}

func newWorld(t *testing.T) *world {
	t.Helper()
	plan := Plan{
		ID:       "tx1",
		Artifact: "/staging/release.tar",
		Previous: Installation{Version: "0.6.0", SHA256: "aa", Schema: "0007"},
		Next:     Installation{Version: "0.7.0", SHA256: "bb", Schema: "0008"},
	}
	return &world{t: t, plan: plan, journal: &Journal{Dir: t.TempDir()}, live: "old", data: "old", installed: plan.Previous, staged: true, running: true}
}

func (w *world) engine() *Engine { return &Engine{Journal: w.journal, Steps: w} }

// call runs f and reports whether it crashed.
func call(f func() (string, error)) (outcome string, err error, crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(crash); !ok {
				panic(r)
			}
			crashed = true
		}
	}()
	outcome, err = f()
	return
}

// settled checks the machine is in one of the two allowed end states and
// returns which.
func (w *world) settled() string {
	w.t.Helper()
	r, err := w.journal.Read()
	if err != nil {
		w.t.Fatal(err)
	}
	if r.State != StateIdle {
		w.t.Fatalf("journal left at %s", r.State)
	}
	if w.fenced || !w.running || w.staged || w.cloneData != "" {
		w.t.Errorf("not settled: fenced %v, running %v, staged %v, recovery point %q left", w.fenced, w.running, w.staged, w.cloneData)
	}
	switch {
	case w.live == "new" && w.data == "new" && w.installed == w.plan.Next:
		return "new"
	case w.live == "old" && w.data == "old" && w.installed == w.plan.Previous:
		return "old"
	}
	w.t.Fatalf("mixed end state: program %s, data %s, recorded %s", w.live, w.data, w.installed.Version)
	return ""
}

func TestInstallsARelease(t *testing.T) {
	w := newWorld(t)
	var states []State
	e := w.engine()
	e.OnChange = func(r Record) { states = append(states, r.State) }
	outcome, err := e.Run(w.plan)
	if err != nil || outcome != OutcomeInstalled {
		t.Fatalf("Run = %q, %v", outcome, err)
	}
	if w.settled() != "new" {
		t.Fatal("not installed cleanly")
	}
	want := []State{StatePlanned, StatePrechecked, StateFenced, StateRecoveryPoint, StateInstalled, StateMigrated, StateCommitted, StateOpen, StateCleaned, StateIdle}
	if fmt.Sprint(states) != fmt.Sprint(want) {
		t.Fatalf("states %v, want %v", states, want)
	}
}

// A crash before or after any step, followed by recovery, leaves either the
// whole previous pair or the whole new pair, never a mix, and never goes
// back once writes were accepted on the new pair.
func TestRecoversFromACrashAnywhere(t *testing.T) {
	for _, step := range stepNames {
		for _, when := range []string{"before", "after"} {
			t.Run(step+"/"+when, func(t *testing.T) {
				w := newWorld(t)
				w.crashAt = step + ":" + when
				_, err, crashed := call(func() (string, error) { return w.engine().Run(w.plan) })
				if !crashed {
					if w.crashAt == "" {
						t.Fatal("crash point was hit but not reported")
					}
					// The happy path never reaches this step.
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				outcome, err := w.engine().Recover()
				if err != nil {
					t.Fatalf("Recover: %v", err)
				}
				end := w.settled()
				if (end == "new") != (outcome == OutcomeInstalled) {
					t.Fatalf("outcome %q but the %s pair is live", outcome, end)
				}
				migratedBeforeCrash := step == "Migrate" && when == "after" || step == "Record" || step == "Unfence" || step == "Cleanup"
				if migratedBeforeCrash && end != "new" {
					t.Fatalf("a crash after the database update went back to the previous pair")
				}
			})
		}
	}
}

// A second crash while recovering from the first still ends settled.
func TestRecoversFromACrashDuringRecovery(t *testing.T) {
	for _, first := range stepNames {
		for _, second := range stepNames {
			for _, when := range []string{"before", "after"} {
				w := newWorld(t)
				w.crashAt = first + ":after"
				if _, _, crashed := call(func() (string, error) { return w.engine().Run(w.plan) }); !crashed {
					continue
				}
				w.crashAt = second + ":" + when
				_, err, crashed := call(func() (string, error) { return w.engine().Recover() })
				if !crashed && err != nil {
					t.Fatalf("%s then %s:%s: %v", first, second, when, err)
				}
				w.crashAt = ""
				if _, err := w.engine().Recover(); err != nil {
					t.Fatalf("%s then %s:%s: %v", first, second, when, err)
				}
				w.settled()
			}
		}
	}
}

func TestStepFailures(t *testing.T) {
	cases := []struct {
		step    string
		err     error
		outcome string
		end     string
		counted bool
	}{
		{"Precheck", fmt.Errorf("on battery: %w", ErrDefer), OutcomeDeferred, "old", false},
		{"Fence", fmt.Errorf("a writer won't stop: %w", ErrDefer), OutcomeDeferred, "old", false},
		{"Fence", nil, OutcomeFailed, "old", true},
		{"TakeRecoveryPoint", nil, OutcomeFailed, "old", true},
		{"Install", nil, OutcomeRestored, "old", true},
		{"Migrate", nil, OutcomeRestored, "old", true},
		{"Record", nil, OutcomeRestored, "old", true},
	}
	for _, c := range cases {
		t.Run(c.step+"/"+c.outcome, func(t *testing.T) {
			w := newWorld(t)
			w.failAt, w.failErr = c.step, c.err
			outcome, err := w.engine().Run(w.plan)
			if err != nil || outcome != c.outcome {
				t.Fatalf("Run = %q, %v; want %q", outcome, err, c.outcome)
			}
			if end := w.settled(); end != c.end {
				t.Fatalf("ended on the %s pair", end)
			}
			r, _ := w.journal.Read()
			if counted := r.Attempts["0.7.0"] == 1; counted != c.counted {
				t.Fatalf("attempts %v", r.Attempts)
			}
			if r.Reason == "" {
				t.Fatal("no reason recorded")
			}
		})
	}
}

// Once writes are accepted on the new pair a failure is reported and
// retried, and recovery goes forward.
func TestNeverGoesBackAfterOpening(t *testing.T) {
	w := newWorld(t)
	w.failAt = "Cleanup"
	if _, err := w.engine().Run(w.plan); err == nil {
		t.Fatal("a cleanup failure was not reported")
	}
	if r, _ := w.journal.Read(); r.State != StateCleaned {
		t.Fatalf("journal at %s", r.State)
	}
	if outcome, err := w.engine().Recover(); err != nil || outcome != OutcomeInstalled {
		t.Fatalf("Recover = %q, %v", outcome, err)
	}
	if w.settled() != "new" {
		t.Fatal("went back")
	}
}

func TestHoldsAReleaseThatKeepsFailing(t *testing.T) {
	w := newWorld(t)
	e := w.engine()
	for i := 0; i < MaxAttempts; i++ {
		w.failAt = "Migrate"
		w.staged = true
		if outcome, err := e.Run(w.plan); err != nil || outcome != OutcomeRestored {
			t.Fatalf("attempt %d: %q, %v", i, outcome, err)
		}
	}
	if held, _ := e.Held("0.7.0"); !held {
		t.Fatal("not held")
	}
	if _, err := e.Run(w.plan); !errors.Is(err, ErrHeld) {
		t.Fatalf("Run after %d failures: %v", MaxAttempts, err)
	}
	newer := w.plan
	newer.Next = Installation{Version: "0.7.1", SHA256: "cc", Schema: "0008"}
	w.plan, w.staged = newer, true
	if outcome, err := e.Run(newer); err != nil || outcome != OutcomeInstalled {
		t.Fatalf("a newer release: %q, %v", outcome, err)
	}
	if r, _ := w.journal.Read(); r.Attempts != nil {
		t.Fatalf("attempts kept after a success: %v", r.Attempts)
	}
}

func TestRefusesASecondTransaction(t *testing.T) {
	w := newWorld(t)
	w.crashAt = "Install:after"
	call(func() (string, error) { return w.engine().Run(w.plan) })
	if _, err := w.engine().Run(w.plan); !errors.Is(err, ErrBusy) {
		t.Fatalf("Run over an open transaction: %v", err)
	}
	unlock, err := w.journal.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := w.journal.Lock(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lock: %v", err)
	}
}

// If recovery can't tell whether the database update finished it changes
// nothing: writes stay stopped and the journal keeps its state.
func TestWaitsWhenTheDatabaseStateIsUnknown(t *testing.T) {
	w := newWorld(t)
	w.crashAt = "Migrate:before"
	call(func() (string, error) { return w.engine().Run(w.plan) })
	w.migratedE = errors.New("database unreachable")
	w.calls = nil
	if _, err := w.engine().Recover(); err == nil {
		t.Fatal("no error")
	}
	if len(w.calls) != 1 || !w.fenced {
		t.Fatalf("acted without knowing: %v, fenced %v", w.calls, w.fenced)
	}
	if r, _ := w.journal.Read(); r.State != StateMigrated {
		t.Fatalf("journal at %s", r.State)
	}
	w.migratedE = nil
	if _, err := w.engine().Recover(); err != nil {
		t.Fatal(err)
	}
	if w.settled() != "old" {
		t.Fatal("an unfinished database update was kept")
	}
}

func TestRejectsAnIncompletePlan(t *testing.T) {
	w := newWorld(t)
	p := w.plan
	p.Next.SHA256 = ""
	if _, err := w.engine().Run(p); err == nil {
		t.Fatal("accepted")
	}
}

func TestJournalRefusesDamage(t *testing.T) {
	j := &Journal{Dir: t.TempDir()}
	if r, err := j.Read(); err != nil || r.State != StateIdle {
		t.Fatalf("empty journal: %v, %v", r, err)
	}
	for name, content := range map[string]string{
		"not json":      "{",
		"unknown field": `{"state":"idle","extra":1}`,
		"no state":      `{"updated":"2026-01-01T00:00:00Z"}`,
	} {
		os.WriteFile(filepath.Join(j.Dir, journalFile), []byte(content), 0o644)
		if _, err := j.Read(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	os.WriteFile(filepath.Join(j.Dir, journalFile), []byte(`{"state":"sideways","plan":{"id":"x","artifact":"a","previous":{"version":"1","sha256":"","schema":""},"next":{"version":"2","sha256":"b","schema":""}},"updated":"2026-01-01T00:00:00Z"}`), 0o644)
	if _, err := (&Engine{Journal: j, Steps: newWorld(t)}).Recover(); err == nil {
		t.Error("unknown state accepted")
	}
	os.WriteFile(filepath.Join(j.Dir, journalFile), []byte(`{"state":"installed","updated":"2026-01-01T00:00:00Z"}`), 0o644)
	if _, err := (&Engine{Journal: j, Steps: newWorld(t)}).Recover(); err == nil {
		t.Error("a state without a plan accepted")
	}
}
