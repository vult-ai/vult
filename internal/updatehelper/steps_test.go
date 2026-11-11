//go:build darwin || linux

package updatehelper

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/recovery"
	"github.com/vult-ai/vult/internal/updatetxn"
)

type crash struct{ at string }

// schemaOf is the database each fake program expects.
var schemaOf = map[string]string{"old": "0007", "new": "0008"}

// fakeMac models the services and the database; the program and data
// directories are real files, and copies are real APFS clones.
type fakeMac struct {
	t       *testing.T
	layout  Layout
	running bool
	crashAt string
	failAt  string
	opened  bool // the new version answered
	free    uint64
}

func (m *fakeMac) hit(name string) error {
	if m.crashAt == name {
		m.crashAt = ""
		panic(crash{name})
	}
	if m.failAt == name {
		m.failAt = ""
		return errors.New(name + " failed")
	}
	return nil
}

func (m *fakeMac) live() string {
	b, err := os.ReadFile(filepath.Join(m.layout.Program, "vult"))
	if err != nil {
		m.t.Fatalf("no live program: %v", err)
	}
	return string(b)
}

func (m *fakeMac) db() map[string]string {
	b, err := os.ReadFile(filepath.Join(m.layout.Data, "db.json"))
	if err != nil {
		m.t.Fatalf("no database: %v", err)
	}
	var d map[string]string
	json.Unmarshal(b, &d)
	return d
}

func (m *fakeMac) setDB(d map[string]string) {
	b, _ := json.Marshal(d)
	if err := os.WriteFile(filepath.Join(m.layout.Data, "db.json"), b, 0o600); err != nil {
		m.t.Fatal(err)
	}
}

func (m *fakeMac) Maintenance(_ context.Context, command string) ([]byte, error) {
	if err := m.hit("maintenance " + command); err != nil {
		return nil, err
	}
	d := m.db()
	switch command {
	case "migrate":
		if m.running || m.live() != "new" {
			m.t.Error("migrated without the new program and with Vult running")
		}
		d["schema"] = "0008"
		m.setDB(d)
	case "validate":
		if d["schema"] != schemaOf[m.live()] {
			return nil, errors.New("the database needs this version's maintenance step before Vult can start")
		}
	default:
		m.t.Errorf("unexpected maintenance command %q", command)
	}
	return json.Marshal(map[string]string{"schema": d["schema"]})
}

func TestMaintenanceMustReachTheSignedTargetSchema(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupted), func(t *testing.T) {
			mc := newMachine(t)
			mc.plan.Next.Schema = "0009"
			var outcome string
			var err error
			if interrupted {
				mc.m.crashAt = "Migrate:before"
				if _, _, crashed := call(func() (string, error) { return mc.engine().Run(mc.plan) }); !crashed {
					t.Fatal("migration was not interrupted")
				}
				if _, err := mc.m.Maintenance(context.Background(), "migrate"); err != nil {
					t.Fatal(err)
				}
				outcome, err = mc.engine().Recover()
			} else {
				outcome, err = mc.engine().Run(mc.plan)
			}
			if err != nil || outcome != updatetxn.OutcomeRestored {
				t.Fatalf("%s, %v", outcome, err)
			}
			if mc.m.opened || mc.m.live() != "old" || mc.m.db()["schema"] != "0007" {
				t.Fatal("a mismatched schema was opened")
			}
		})
	}
}

func (m *fakeMac) StopServices(context.Context) error {
	if err := m.hit("stop"); err != nil {
		return err
	}
	m.running = false
	return nil
}

func (m *fakeMac) StartServices(context.Context) error {
	if err := (&updatetxn.Journal{Dir: m.layout.Journal()}).StartupReady(); err != nil {
		m.t.Error("started while startup was blocked")
		return err
	}
	if err := m.hit("start"); err != nil {
		return err
	}
	if d := m.db(); d["schema"] != schemaOf[m.live()] {
		m.t.Errorf("started program %s on a %s database", m.live(), d["schema"])
	}
	m.running = true
	if m.live() == "new" {
		m.opened = true
	}
	return nil
}

func (m *fakeMac) Healthy(context.Context) error {
	if err := m.hit("healthy"); err != nil {
		return err
	}
	if m.live() == "new" {
		m.opened = true
	}
	return nil
}

func (m *fakeMac) DatabaseStopped(string) error {
	if m.running {
		return errors.New("still running")
	}
	return nil
}

func (m *fakeMac) Clean(string) error {
	if m.running {
		return errors.New("in production")
	}
	return nil
}

func (m *fakeMac) Clone(src, dst string) error {
	if err := m.hit("clone"); err != nil {
		return err
	}
	return recovery.Clone(src, dst)
}

func (m *fakeMac) FreeBytes(string) (uint64, error) { return m.free, nil }

// crashingSteps adds crash points before and after every step.
type crashingSteps struct {
	*Steps
	m *fakeMac
}

func (c crashingSteps) around(name string, f func() error) error {
	if err := c.m.hit(name + ":before"); err != nil {
		return err
	}
	if err := f(); err != nil {
		return err
	}
	return c.m.hit(name + ":after")
}

func (c crashingSteps) Discard(p updatetxn.Plan) error {
	return c.around("Discard", func() error { return c.Steps.Discard(p) })
}
func (c crashingSteps) Precheck(p updatetxn.Plan) error {
	return c.around("Precheck", func() error { return c.Steps.Precheck(p) })
}
func (c crashingSteps) Fence(p updatetxn.Plan) error {
	return c.around("Fence", func() error { return c.Steps.Fence(p) })
}
func (c crashingSteps) Unfence(p updatetxn.Plan) error {
	return c.around("Unfence", func() error { return c.Steps.Unfence(p) })
}
func (c crashingSteps) TakeRecoveryPoint(p updatetxn.Plan) error {
	return c.around("TakeRecoveryPoint", func() error { return c.Steps.TakeRecoveryPoint(p) })
}
func (c crashingSteps) DiscardRecoveryPoint(p updatetxn.Plan) error {
	return c.around("DiscardRecoveryPoint", func() error { return c.Steps.DiscardRecoveryPoint(p) })
}
func (c crashingSteps) Install(p updatetxn.Plan) error {
	return c.around("Install", func() error { return c.Steps.Install(p) })
}
func (c crashingSteps) Uninstall(p updatetxn.Plan) error {
	return c.around("Uninstall", func() error { return c.Steps.Uninstall(p) })
}
func (c crashingSteps) Migrate(p updatetxn.Plan) error {
	return c.around("Migrate", func() error { return c.Steps.Migrate(p) })
}
func (c crashingSteps) Record(i updatetxn.Installation) error {
	return c.around("Record", func() error { return c.Steps.Record(i) })
}
func (c crashingSteps) RestoreData(p updatetxn.Plan) error {
	return c.around("RestoreData", func() error { return c.Steps.RestoreData(p) })
}
func (c crashingSteps) Cleanup(p updatetxn.Plan) error {
	return c.around("Cleanup", func() error { return c.Steps.Cleanup(p) })
}

func releaseArchive(t *testing.T, dir string) string {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	w.WriteHeader(&tar.Header{Name: "vult", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg})
	w.Write([]byte("new"))
	w.Close()
	path := filepath.Join(dir, "release.tar")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type machine struct {
	m       *fakeMac
	plan    updatetxn.Plan
	journal *updatetxn.Journal
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	base := t.TempDir()
	l := Layout{
		Program: filepath.Join(base, "programs", "ai.example.ledger"),
		Data:    filepath.Join(base, "ledger", "data"),
		Copies:  filepath.Join(base, "updater", "copies"),
		State:   filepath.Join(base, "updater"),
	}
	for _, d := range []string{l.Program, l.Data, filepath.Join(l.Data, "database"), l.Copies, l.Journal(), l.Staging()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(l.Program, "vult"), []byte("old"), 0o755)
	os.WriteFile(filepath.Join(l.Data, "database", "PG_VERSION"), []byte("18\n"), 0o600)
	m := &fakeMac{t: t, layout: l, running: true, free: 1 << 40}
	m.setDB(map[string]string{"schema": "0007", "facts": "example"})
	previous := updatetxn.Installation{Version: "0.6.0", SHA256: "aa", Schema: "0007"}
	if err := (&Steps{Layout: l, System: m}).Record(previous); err != nil {
		t.Fatal(err)
	}
	plan := updatetxn.Plan{ID: "20261110T120000Z-0a1b2c3d", Artifact: releaseArchive(t, l.Staging()), Previous: previous,
		Next: updatetxn.Installation{Version: "0.7.0", SHA256: "bb", Schema: "0008"}}
	return &machine{m: m, plan: plan, journal: &updatetxn.Journal{Dir: l.Journal()}}
}

func (mc *machine) engine() *updatetxn.Engine {
	return &updatetxn.Engine{Journal: mc.journal, Steps: crashingSteps{&Steps{Layout: mc.m.layout, System: mc.m}, mc.m}}
}

func call(f func() (string, error)) (out string, err error, crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(crash); !ok {
				panic(r)
			}
			crashed = true
		}
	}()
	out, err = f()
	return
}

// settled checks the Mac ended on the whole previous or the whole new
// installation, running, with nothing left over, and never went back once
// the new version had answered.
func (mc *machine) settled(t *testing.T) string {
	t.Helper()
	m, l := mc.m, mc.m.layout
	r, err := mc.journal.Read()
	if err != nil || r.State != updatetxn.StateIdle {
		t.Fatalf("journal at %v, %v", r.State, err)
	}
	d := m.db()
	installed, err := Installed(l)
	if err != nil {
		t.Fatal(err)
	}
	if !m.running || d["facts"] != "example" {
		t.Errorf("not settled: %v, running %v", d, m.running)
	}
	for _, leftover := range []string{mc.plan.Artifact, l.Program + ".next"} {
		if _, err := os.Lstat(leftover); err == nil {
			t.Errorf("%s left behind", filepath.Base(leftover))
		}
	}
	switch {
	case m.live() == "new" && d["schema"] == "0008" && installed == mc.plan.Next:
		return "new"
	case m.live() == "old" && d["schema"] == "0007" && installed == mc.plan.Previous:
		if m.opened {
			t.Error("went back after the new version had answered")
		}
		return "old"
	}
	t.Fatalf("mixed: program %s, database %s, recorded %s", m.live(), d["schema"], installed.Version)
	return ""
}

func TestHelperInstallsARelease(t *testing.T) {
	mc := newMachine(t)
	outcome, err := mc.engine().Run(mc.plan)
	if err != nil || outcome != updatetxn.OutcomeInstalled {
		t.Fatalf("Run = %q, %v", outcome, err)
	}
	if mc.settled(t) != "new" {
		t.Fatal("not installed")
	}
	if _, err := os.Stat(filepath.Join(mc.m.layout.Copies, mc.plan.ID, completeMark)); err != nil {
		t.Fatal("this update's copy of the data was not kept for undo")
	}
	if b, _ := os.ReadFile(filepath.Join(mc.m.layout.Program+".previous", "vult")); string(b) != "old" {
		t.Fatal("the previous program was not kept for undo")
	}
}

var crashPoints = func() []string {
	var points []string
	for _, step := range []string{"Precheck", "Fence", "TakeRecoveryPoint", "Install", "Migrate", "Record", "Unfence", "Cleanup", "Uninstall", "RestoreData", "DiscardRecoveryPoint", "Discard"} {
		points = append(points, step+":before", step+":after")
	}
	return append(points, "maintenance migrate", "maintenance validate", "clone", "stop", "start", "healthy")
}()

// A crash anywhere, then recovery, ends on a whole installation, on real
// files.
func TestHelperRecoversFromACrashAnywhere(t *testing.T) {
	for _, point := range crashPoints {
		t.Run(strings.ReplaceAll(point, " ", "_"), func(t *testing.T) {
			mc := newMachine(t)
			mc.m.crashAt = point
			if _, _, crashed := call(func() (string, error) { return mc.engine().Run(mc.plan) }); !crashed {
				if mc.m.crashAt == "" {
					t.Fatal("crashed but not reported")
				}
				return // not on the path this run takes
			}
			outcome, err := mc.engine().Recover()
			if err != nil {
				t.Fatalf("Recover: %v", err)
			}
			end := mc.settled(t)
			if (end == "new") != (outcome == updatetxn.OutcomeInstalled) {
				t.Fatalf("outcome %q on the %s installation", outcome, end)
			}
		})
	}
}

// A second crash during recovery still ends settled.
func TestHelperRecoversFromACrashDuringRecovery(t *testing.T) {
	for _, first := range []string{"Install:after", "Migrate:after", "maintenance migrate", "Unfence:before", "start", "Record:after"} {
		for _, second := range crashPoints {
			mc := newMachine(t)
			mc.m.crashAt = first
			if _, _, crashed := call(func() (string, error) { return mc.engine().Run(mc.plan) }); !crashed {
				t.Fatalf("%s did not crash", first)
			}
			mc.m.crashAt = second
			call(func() (string, error) { return mc.engine().Recover() })
			mc.m.crashAt = ""
			if _, err := mc.engine().Recover(); err != nil {
				t.Fatalf("%s then %s: %v", first, second, err)
			}
			if t.Failed() {
				return
			}
			mc.settled(t)
		}
	}
}

func TestHelperStepFailures(t *testing.T) {
	for _, c := range []struct {
		fail, outcome string
	}{
		{"stop", updatetxn.OutcomeFailed},
		{"clone", updatetxn.OutcomeFailed},
		{"maintenance migrate", updatetxn.OutcomeRestored},
		{"maintenance validate", updatetxn.OutcomeRestored},
	} {
		t.Run(strings.ReplaceAll(c.fail, " ", "_"), func(t *testing.T) {
			mc := newMachine(t)
			mc.m.failAt = c.fail
			outcome, err := mc.engine().Run(mc.plan)
			if err != nil || outcome != c.outcome {
				t.Fatalf("Run = %q, %v; want %q", outcome, err, c.outcome)
			}
			if end := mc.settled(t); end != "old" {
				t.Fatalf("ended on %s", end)
			}
		})
	}
}

func TestPrecheckWaitsForSpace(t *testing.T) {
	mc := newMachine(t)
	mc.m.free = 1 << 20
	if err := (&Steps{Layout: mc.m.layout, System: mc.m}).Precheck(mc.plan); !errors.Is(err, updatetxn.ErrDefer) {
		t.Fatalf("Precheck on a full disk: %v", err)
	}
}

// A copy that doesn't match the data is never kept.
func TestRecoveryCopyMustMatch(t *testing.T) {
	mc := newMachine(t)
	mc.m.running = false
	steps := &Steps{Layout: mc.m.layout, System: partialClone{mc.m}}
	if err := steps.TakeRecoveryPoint(mc.plan); err == nil {
		t.Fatal("an incomplete copy was accepted")
	}
	if _, err := os.Stat(filepath.Join(mc.m.layout.Copies, mc.plan.ID, completeMark)); err == nil {
		t.Fatal("an incomplete copy was marked complete")
	}
}

type partialClone struct{ *fakeMac }

func (p partialClone) Clone(src, dst string) error {
	if err := recovery.Clone(src, dst); err != nil {
		return err
	}
	return os.Remove(filepath.Join(dst, "db.json"))
}

// Cleanup keeps this update's copy for undo, removes older ones and data
// set aside by updates that didn't finish, and never touches anything else.
func TestCleanupKeepsOnlyThisUpdatesCopy(t *testing.T) {
	mc := newMachine(t)
	l := mc.m.layout
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("2026010%dT000000Z-00000000", i)
		os.MkdirAll(filepath.Join(l.Copies, id, recovery.DataDir), 0o700)
		os.WriteFile(filepath.Join(l.Copies, id, completeMark), []byte(id+"\n"), 0o600)
	}
	os.MkdirAll(filepath.Join(l.Copies, mc.plan.ID, recovery.DataDir), 0o700)
	os.MkdirAll(l.Data+".failed-20260101T000000Z-00000000", 0o700)
	steps := &Steps{Layout: l, System: mc.m}
	if err := steps.Cleanup(mc.plan); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(l.Copies)
	if len(entries) != 1 || entries[0].Name() != mc.plan.ID {
		t.Fatalf("kept %v", entries)
	}
	if _, err := os.Stat(l.Data + ".failed-20260101T000000Z-00000000"); err == nil {
		t.Fatal("old set-aside data kept")
	}
	if _, err := os.Stat(l.Data); err != nil {
		t.Fatal("the live data was touched")
	}
}

func TestCleanupPreservesUnrecognizedCopiesAndSiblingFolders(t *testing.T) {
	mc := newMachine(t)
	l := mc.m.layout
	var keep []string
	for _, name := range []string{".manual-backup", "20260101T000000Z-missing", "20260102T000000Z-wrong", "20260103T000000Z-link"} {
		dir := l.point(name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		keep = append(keep, dir, l.failedData(name))
		if err := os.Mkdir(l.failedData(name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(l.point("20260102T000000Z-wrong"), completeMark), []byte("20260102T000000Z-other\n"), 0o600)
	marker := filepath.Join(t.TempDir(), "marker")
	os.WriteFile(marker, []byte("20260103T000000Z-link\n"), 0o600)
	os.Symlink(marker, filepath.Join(l.point("20260103T000000Z-link"), completeMark))
	keep = append(keep, l.Data+".failed-manual-backup")
	os.Mkdir(keep[len(keep)-1], 0o700)
	if err := (&Steps{Layout: l, System: mc.m}).Cleanup(mc.plan); err != nil {
		t.Fatal(err)
	}
	for _, name := range keep {
		if _, err := os.Lstat(name); err != nil {
			t.Errorf("cleanup removed an unrecognized folder: %s, %v", filepath.Base(name), err)
		}
	}
}

type corruptClone struct{ *fakeMac }

func (c corruptClone) Clone(src, dst string) error {
	if err := recovery.Clone(src, dst); err != nil {
		return err
	}
	file := filepath.Join(dst, "db.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return os.WriteFile(file, []byte(strings.Replace(string(data), "example", "changed", 1)), 0o600)
}

func TestRecoveryCopyRejectsChangedBytesOfTheSameSize(t *testing.T) {
	mc := newMachine(t)
	mc.m.running = false
	steps := &Steps{Layout: mc.m.layout, System: corruptClone{mc.m}}
	if err := steps.TakeRecoveryPoint(mc.plan); err == nil {
		t.Fatal("a same-sized corrupted copy was accepted")
	}
	if _, err := os.Stat(filepath.Join(mc.m.layout.point(mc.plan.ID), completeMark)); err == nil {
		t.Fatal("a corrupted copy was marked complete")
	}
}

func TestRestoreRefusesMissingLiveDataAndCopy(t *testing.T) {
	mc := newMachine(t)
	if err := os.Rename(mc.m.layout.Data, mc.m.layout.failedData(mc.plan.ID)); err != nil {
		t.Fatal(err)
	}
	steps := &Steps{Layout: mc.m.layout, System: mc.m}
	if err := steps.RestoreData(mc.plan); err == nil {
		t.Fatal("a missing copy and missing restored data were treated as a completed restore")
	}
}
