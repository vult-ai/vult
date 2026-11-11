// Package updatetxn installs a verified release as one journaled transaction.
// Every state is written durably before its action runs, so whatever stopped
// the transaction (a crash, a power cut, a kill), the next start finds the
// journal and either finishes the update or puts the previous program and
// data back. Until the transaction reaches StateOpen nothing the update did
// is kept unless it fully succeeded; from StateOpen on, new data is kept and
// recovery only goes forward.
package updatetxn

import (
	"errors"
	"fmt"
	"time"
)

// State is a step of the transaction. The journal records the state before
// its action starts, so a recorded state means "this action may have run in
// part".
type State string

const (
	StateIdle          State = "idle"
	StatePlanned       State = "planned"        // the plan is fixed; the release is staged
	StatePrechecked    State = "prechecked"     // space, power, timing
	StateFenced        State = "fenced"         // writes stopped, services stopped
	StateRecoveryPoint State = "recovery-point" // copy of the program and data taken
	StateInstalled     State = "installed"      // new program swapped in
	StateMigrated      State = "migrated"       // database moved to the new schema
	StateCommitted     State = "committed"      // the new installation is recorded
	StateReopening     State = "reopening"      // older journals: services may have started
	StateOpen          State = "open"           // writes accepted; no going back
	StateCleaned       State = "cleaned"        // leftovers removed
	StateRestoring     State = "restoring"      // putting the previous pair back
)

// Outcomes the journal records when a transaction ends.
const (
	OutcomeInstalled = "installed" // the new release is running
	OutcomeDeferred  = "deferred"  // not now; nothing changed
	OutcomeFailed    = "failed"    // stopped before changing anything
	OutcomeRestored  = "restored"  // the previous program and data are back
)

// MaxAttempts is how many times one release may fail before this
// installation holds it until a newer release appears.
const MaxAttempts = 3

// Installation is what is installed: the release and the database schema.
type Installation struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Schema  string `json:"schema"`
}

// Plan is fixed when the transaction opens and never changes during it.
type Plan struct {
	ID       string       `json:"id"`
	Artifact string       `json:"artifact"` // the staged, verified copy
	Previous Installation `json:"previous"`
	Next     Installation `json:"next"`
}

func (p Plan) check() error {
	if p.ID == "" || p.Artifact == "" || p.Next.Version == "" || p.Next.SHA256 == "" || p.Previous.Version == "" {
		return errors.New("the plan is incomplete")
	}
	return nil
}

// Record is the journal's content.
type Record struct {
	State   State     `json:"state"`
	Plan    *Plan     `json:"plan,omitempty"`
	Outcome string    `json:"outcome,omitempty"` // set when State is idle
	Reason  string    `json:"reason,omitempty"`
	Updated time.Time `json:"updated"`
	// Attempts counts failed attempts per release version.
	Attempts map[string]int `json:"attempts,omitempty"`
}

// ErrDefer marks a precheck or fence failure that means "not now": the
// update waits and the attempt doesn't count against the release.
var ErrDefer = errors.New("deferred")

// ErrHeld means this release has failed MaxAttempts times here.
var ErrHeld = errors.New("held after repeated failures")

// ErrBusy means another transaction is in the journal.
var ErrBusy = errors.New("another update is in progress")

// Steps does the work. Every method must be safe to call again after it was
// interrupted part way through, or after it already finished: recovery calls
// them again without knowing how far they got.
type Steps interface {
	// Discard removes the staged release.
	Discard(Plan) error
	// Precheck returns an ErrDefer-wrapped error when now isn't a good time.
	Precheck(Plan) error
	// Fence durably blocks service startup, then stops the services and
	// waits for in-flight writes.
	Fence(Plan) error
	// Unfence clears the stop state and starts the services on whichever
	// program is live.
	Unfence(Plan) error
	// TakeRecoveryPoint returns nil only once the recovery point is complete,
	// verified and durable.
	TakeRecoveryPoint(Plan) error
	DiscardRecoveryPoint(Plan) error
	// Install makes the staged program live; Uninstall makes the previous one
	// live again. Each does nothing if it is already so.
	Install(Plan) error
	Uninstall(Plan) error
	// Migrate moves the database to the new schema in one database
	// transaction, validating before it commits.
	Migrate(Plan) error
	// Migrated reports whether the database is at the new schema with its
	// validation recorded.
	Migrated(Plan) (bool, error)
	// Record durably writes the installed state.
	Record(Installation) error
	// RestoreData replaces the data with the recovery point's copy, keeping
	// the replaced data aside.
	RestoreData(Plan) error
	// Cleanup removes what the transaction no longer needs.
	Cleanup(Plan) error
}

// Engine runs and recovers transactions. One engine at a time may use a
// journal; the caller holds the journal's lock.
type Engine struct {
	Journal *Journal
	Steps   Steps
	Now     func() time.Time
	// OnChange, if set, is called after each durable journal change.
	OnChange func(Record)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) write(r Record) error {
	r.Updated = e.now().UTC()
	if err := e.Journal.Write(r); err != nil {
		return err
	}
	if e.OnChange != nil {
		e.OnChange(r)
	}
	return nil
}

// enter records state s for plan p, keeping the attempt counts.
func (e *Engine) enter(cur *Record, s State) error {
	next := Record{State: s, Plan: cur.Plan, Attempts: cur.Attempts}
	if err := e.write(next); err != nil {
		return err
	}
	*cur = next
	return nil
}

// finish records the end of a transaction.
func (e *Engine) finish(cur *Record, outcome, reason string) error {
	attempts := map[string]int{}
	for v, n := range cur.Attempts {
		attempts[v] = n
	}
	if cur.Plan != nil {
		switch outcome {
		case OutcomeFailed, OutcomeRestored:
			attempts[cur.Plan.Next.Version]++
		case OutcomeInstalled:
			attempts = map[string]int{}
		}
	}
	if len(attempts) == 0 {
		attempts = nil
	}
	next := Record{State: StateIdle, Outcome: outcome, Reason: reason, Attempts: attempts}
	if err := e.write(next); err != nil {
		return err
	}
	*cur = next
	return nil
}

// Held reports whether version has failed too often to try again here.
func (e *Engine) Held(version string) (bool, error) {
	r, err := e.Journal.Read()
	if err != nil {
		return false, err
	}
	return r.Attempts[version] >= MaxAttempts, nil
}

// Run installs p. The journal must be idle: call Recover first. The returned
// outcome says what happened; an error means the transaction couldn't finish
// either way and is left in the journal for Recover.
func (e *Engine) Run(p Plan) (string, error) {
	if err := p.check(); err != nil {
		return "", err
	}
	cur, err := e.Journal.Read()
	if err != nil {
		return "", err
	}
	if cur.State != StateIdle {
		return "", ErrBusy
	}
	if cur.Attempts[p.Next.Version] >= MaxAttempts {
		return "", fmt.Errorf("%s: %w", p.Next.Version, ErrHeld)
	}
	cur.Plan = &p
	if err := e.enter(&cur, StatePlanned); err != nil {
		return "", err
	}
	if err := e.enter(&cur, StatePrechecked); err != nil {
		return "", err
	}
	if err := e.Steps.Precheck(p); err != nil {
		return e.abandon(&cur, err, false)
	}
	if err := e.enter(&cur, StateFenced); err != nil {
		return "", err
	}
	if err := e.Steps.Fence(p); err != nil {
		return e.abandon(&cur, err, true)
	}
	if err := e.enter(&cur, StateRecoveryPoint); err != nil {
		return "", err
	}
	if err := e.Steps.TakeRecoveryPoint(p); err != nil {
		return e.abandonPoint(&cur, err)
	}
	return e.forward(&cur, StateInstalled)
}

// forward runs the transaction from state s (Installed or later) to the end,
// restoring the previous pair if a step fails before StateOpen.
func (e *Engine) forward(cur *Record, s State) (string, error) {
	p := *cur.Plan
	steps := []struct {
		state State
		run   func() error
	}{
		{StateInstalled, func() error { return e.Steps.Install(p) }},
		{StateMigrated, func() error { return e.Steps.Migrate(p) }},
		{StateCommitted, func() error { return e.Steps.Record(p.Next) }},
	}
	started := false
	for _, step := range steps {
		if step.state == s {
			started = true
		}
		if !started {
			continue
		}
		if err := e.enter(cur, step.state); err != nil {
			return "", err
		}
		if err := step.run(); err != nil {
			return e.restore(cur, fmt.Sprintf("%s: %v", step.state, err))
		}
	}
	return e.open(cur)
}

// open is past the point of no return: errors leave the journal at
// StateOpen or StateCleaned so Recover tries again, and never restore.
func (e *Engine) open(cur *Record) (string, error) {
	p := *cur.Plan
	if cur.State != StateOpen && cur.State != StateCleaned {
		if err := e.enter(cur, StateOpen); err != nil {
			return "", err
		}
	}
	if cur.State == StateOpen {
		if err := e.Steps.Unfence(p); err != nil {
			return "", fmt.Errorf("the update is installed but Vult could not reopen: %w", err)
		}
		if err := e.enter(cur, StateCleaned); err != nil {
			return "", err
		}
	}
	if err := e.Steps.Cleanup(p); err != nil {
		return "", fmt.Errorf("the update is installed but cleanup did not finish: %w", err)
	}
	if err := e.finish(cur, OutcomeInstalled, ""); err != nil {
		return "", err
	}
	return OutcomeInstalled, nil
}

// abandon ends a transaction that changed nothing. A deferral doesn't count
// as a failed attempt.
func (e *Engine) abandon(cur *Record, cause error, fenced bool) (string, error) {
	p := *cur.Plan
	if fenced {
		if err := e.Steps.Unfence(p); err != nil {
			return "", err
		}
	}
	if err := e.Steps.Discard(p); err != nil {
		return "", err
	}
	outcome := OutcomeFailed
	if errors.Is(cause, ErrDefer) {
		outcome = OutcomeDeferred
	}
	if err := e.finish(cur, outcome, cause.Error()); err != nil {
		return "", err
	}
	return outcome, nil
}

func (e *Engine) abandonPoint(cur *Record, cause error) (string, error) {
	if err := e.Steps.DiscardRecoveryPoint(*cur.Plan); err != nil {
		return "", err
	}
	return e.abandon(cur, fmt.Errorf("recovery point: %w", cause), true)
}

// restore puts the previous program, data and installed record back. Every
// step is repeatable, so a crash during restore restarts it from the top.
func (e *Engine) restore(cur *Record, reason string) (string, error) {
	p := *cur.Plan
	if cur.State != StateRestoring {
		// The reason is kept in the journal so it survives a crash.
		next := Record{State: StateRestoring, Plan: cur.Plan, Attempts: cur.Attempts, Reason: reason}
		if err := e.write(next); err != nil {
			return "", err
		}
		*cur = next
	}
	for _, step := range []func() error{
		func() error { return e.Steps.Fence(p) },
		func() error { return e.Steps.Uninstall(p) },
		func() error { return e.Steps.RestoreData(p) },
		func() error { return e.Steps.Record(p.Previous) },
		func() error { return e.Steps.Unfence(p) },
		func() error { return e.Steps.Discard(p) },
	} {
		if err := step(); err != nil {
			return "", fmt.Errorf("restoring the previous version did not finish: %w", err)
		}
	}
	if err := e.finish(cur, OutcomeRestored, cur.Reason); err != nil {
		return "", err
	}
	return OutcomeRestored, nil
}

// Recover finishes whatever transaction the journal holds: it goes forward
// if the update is past its commit point or provably complete, and otherwise
// puts things back. It returns "" when the journal was idle.
func (e *Engine) Recover() (string, error) {
	cur, err := e.Journal.Read()
	if err != nil {
		return "", err
	}
	if cur.State == StateIdle {
		return "", nil
	}
	if cur.Plan == nil {
		return "", fmt.Errorf("the journal is at %q without a plan", cur.State)
	}
	p := *cur.Plan
	interrupted := errors.New("interrupted")
	switch cur.State {
	case StatePlanned, StatePrechecked:
		return e.abandon(&cur, fmt.Errorf("%w: %w", interrupted, ErrDefer), false)
	case StateFenced:
		return e.abandon(&cur, fmt.Errorf("%w: %w", interrupted, ErrDefer), true)
	case StateRecoveryPoint:
		return e.abandonPoint(&cur, interrupted)
	case StateInstalled:
		return e.restore(&cur, "interrupted while installing")
	case StateMigrated:
		done, err := e.Steps.Migrated(p)
		if err != nil {
			// Can't tell: leave everything fenced and the journal as it is.
			return "", fmt.Errorf("could not tell whether the database update finished: %w", err)
		}
		if !done {
			return e.restore(&cur, "interrupted while updating the database")
		}
		return e.forward(&cur, StateCommitted)
	case StateCommitted:
		return e.forward(&cur, StateCommitted)
	case StateReopening:
		// Services in an older transaction may already have accepted writes.
		return e.open(&cur)
	case StateOpen, StateCleaned:
		return e.open(&cur)
	case StateRestoring:
		return e.restore(&cur, cur.Reason)
	}
	return "", fmt.Errorf("the journal has an unknown state %q", cur.State)
}
