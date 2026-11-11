//go:build darwin || linux

package updatehelper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vult-ai/vult/internal/recovery"
	"github.com/vult-ai/vult/internal/updatetxn"
)

// Layout is where everything lives. Production paths are in DefaultLayout;
// tests point it at a temporary directory.
type Layout struct {
	Program string // the live program directory
	// TrustFiles, when set, is the folder holding the shipped root.json and
	// repository.json; otherwise they are in Trust().
	TrustFiles string
	// ArchivePrefix selects the program within a release archive that also
	// carries other files ("opt/vult/" on Ubuntu; empty on macOS).
	ArchivePrefix string
	Data          string // the ledger's data directory
	Copies        string // root-only copies of the data taken before updates
	State         string // the helper's own directory
}

func (l Layout) Journal() string { return filepath.Join(l.State, "journal") }
func (l Layout) Staging() string { return filepath.Join(l.State, "staging") }
func (l Layout) Trust() string   { return filepath.Join(l.State, "trust") }

// Shipped is the folder holding the root.json and repository.json that came
// with the installer.
func (l Layout) Shipped() string {
	if l.TrustFiles != "" {
		return l.TrustFiles
	}
	return l.Trust()
}
func (l Layout) Installed() string      { return filepath.Join(l.State, "installed.json") }
func (l Layout) Status() string         { return filepath.Join(l.State, "status.json") }
func (l Layout) History() string        { return filepath.Join(l.State, "history.jsonl") }
func (l Layout) point(id string) string { return filepath.Join(l.Copies, id) }

// failedData is where the data replaced by a restore is kept.
func (l Layout) failedData(id string) string { return l.Data + ".failed-" + id }

// System is everything the helper asks of the operating system. The real
// one runs launchctl and the installed program; tests replace it.
type System interface {
	// Maintenance runs the live program's `vult _maintenance command` as
	// the service account and returns its report.
	Maintenance(ctx context.Context, command string) ([]byte, error)
	// StopServices stops Vult the usual way: requests in progress finish
	// first. StartServices starts it again.
	StopServices(ctx context.Context) error
	StartServices(ctx context.Context) error
	// Healthy reports whether the ledger answers on its socket.
	Healthy(ctx context.Context) error
	// DatabaseStopped reports whether PostgreSQL is stopped for dataDir.
	DatabaseStopped(dataDir string) error
	// Clean reports whether the database in dataDir was shut down cleanly.
	Clean(dataDir string) error
	// Clone copies a directory copy-on-write.
	Clone(src, dst string) error
	// FreeBytes is the space free on the volume holding path.
	FreeBytes(path string) (uint64, error)
}

// Steps carries out the transaction's steps on this Mac. Every method can
// be called again after an interruption.
type Steps struct {
	Layout Layout
	System System
	Ctx    context.Context
}

var _ updatetxn.Steps = (*Steps)(nil)

func (s *Steps) ctx() context.Context {
	if s.Ctx != nil {
		return s.Ctx
	}
	return context.Background()
}

func (s *Steps) program() updatetxn.ProgramDir {
	return updatetxn.ProgramDir{Live: s.Layout.Program, Prefix: s.Layout.ArchivePrefix}
}

func (s *Steps) Discard(p updatetxn.Plan) error {
	if err := os.Remove(p.Artifact); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.program().RemoveNext()
}

const gib = 1 << 30

// Precheck makes sure there is room for the new program, the database's
// growth while it updates, and some to spare.
func (s *Steps) Precheck(p updatetxn.Plan) error {
	artifact, err := os.Stat(p.Artifact)
	if err != nil {
		return err
	}
	data, err := treeSize(s.Layout.Data)
	if err != nil {
		return err
	}
	need := uint64(artifact.Size()) + 2*uint64(data) + gib
	free, err := s.System.FreeBytes(s.Layout.Data)
	if err != nil {
		return err
	}
	if free < need {
		return fmt.Errorf("%w: the update needs %d MB free and the Mac has %d MB", updatetxn.ErrDefer, need>>20, free>>20)
	}
	return nil
}

func treeSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// Fence stops Vult; saves in progress finish first.
func (s *Steps) Fence(p updatetxn.Plan) error {
	if err := (&updatetxn.Journal{Dir: s.Layout.Journal()}).BlockStartup(); err != nil {
		return err
	}
	if err := s.System.StopServices(s.ctx()); err != nil {
		return err
	}
	return s.System.DatabaseStopped(s.Layout.Data)
}

// Unfence starts whichever program and data are live and waits until Vult
// answers.
func (s *Steps) Unfence(p updatetxn.Plan) error {
	i, err := Installed(s.Layout)
	if err != nil {
		return err
	}
	if i == p.Next {
		// Undo must survive even if startup or a later completion step fails.
		if err := rememberUpdate(s.Layout, p); err != nil {
			return err
		}
	}
	if err := (&updatetxn.Journal{Dir: s.Layout.Journal()}).AllowStartup(); err != nil {
		return err
	}
	if err := s.System.StartServices(s.ctx()); err != nil {
		return err
	}
	return s.System.Healthy(s.ctx())
}

// TakeRecoveryPoint makes an instant copy of the stopped data, checks it is
// whole and cleanly shut down, and marks it complete last.
func (s *Steps) TakeRecoveryPoint(p updatetxn.Plan) error {
	dir := s.Layout.point(p.ID)
	if _, err := os.Stat(filepath.Join(dir, completeMark)); err == nil {
		return nil
	}
	// An interrupted copy is never used.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := s.System.Clean(s.Layout.Data); err != nil {
		return fmt.Errorf("the database was not shut down cleanly: %w", err)
	}
	copyDir := filepath.Join(dir, recovery.DataDir)
	if err := s.System.Clone(s.Layout.Data, copyDir); err != nil {
		return err
	}
	if err := s.System.Clean(copyDir); err != nil {
		return fmt.Errorf("the copy is not cleanly shut down: %w", err)
	}
	live, err := recovery.BuildManifest(s.Layout.Data)
	if err != nil {
		return err
	}
	copied, err := recovery.BuildManifest(copyDir)
	if err != nil {
		return err
	}
	if !slices.Equal(live, copied) {
		return errors.New("the copy doesn't match the data")
	}
	return updatetxn.WriteDurably(filepath.Join(dir, completeMark), []byte(p.ID+"\n"), 0o600)
}

// completeMark is written last into a copy that checked out.
const completeMark = "complete"

func (s *Steps) DiscardRecoveryPoint(p updatetxn.Plan) error {
	return os.RemoveAll(s.Layout.point(p.ID))
}

func (s *Steps) Install(p updatetxn.Plan) error {
	// Installing replaces .previous, so an earlier undo record can no longer
	// promise that its matching program is available, even if this update fails.
	if err := forgetUpdate(s.Layout); err != nil {
		return err
	}
	return s.program().Install(p.Artifact, p.Next.SHA256)
}

func (s *Steps) Uninstall(p updatetxn.Plan) error {
	return s.program().Uninstall(p.Next.SHA256)
}

// Migrate runs the new program's maintenance step, which updates the
// database in one transaction and checks no recorded fact changed, then
// checks the result.
func (s *Steps) Migrate(p updatetxn.Plan) error {
	if _, err := s.System.Maintenance(s.ctx(), "migrate"); err != nil {
		return err
	}
	data, err := s.System.Maintenance(s.ctx(), "validate")
	if err != nil {
		return err
	}
	schema, err := maintenanceSchema(data)
	if err != nil {
		return err
	}
	if schema != p.Next.Schema {
		return fmt.Errorf("the release requires schema %s but maintenance reported %s", p.Next.Schema, schema)
	}
	return nil
}

func maintenanceSchema(data []byte) (string, error) {
	var report struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(data, &report); err != nil || report.Schema == "" {
		return "", errors.New("maintenance did not report the database schema")
	}
	return report.Schema, nil
}

// errBehind is how the maintenance step reports a database at an earlier
// schema; anything else means the state can't be told.
const errBehind = "needs this version's maintenance step"

func (s *Steps) Migrated(p updatetxn.Plan) (bool, error) {
	data, err := s.System.Maintenance(s.ctx(), "validate")
	if err == nil {
		schema, err := maintenanceSchema(data)
		return schema == p.Next.Schema, err
	}
	if strings.Contains(err.Error(), errBehind) {
		return false, nil
	}
	return false, err
}

func (s *Steps) Record(i updatetxn.Installation) error {
	data, err := json.Marshal(i)
	if err != nil {
		return err
	}
	return updatetxn.WriteDurably(s.Layout.Installed(), data, 0o644)
}

// RestoreData puts the copy of the data back, keeping the replaced data
// beside it.
func (s *Steps) RestoreData(p updatetxn.Plan) error {
	if err := s.System.StopServices(s.ctx()); err != nil {
		return err
	}
	clone := filepath.Join(s.Layout.point(p.ID), recovery.DataDir)
	failed := s.Layout.failedData(p.ID)
	cloneThere, err := exists(clone)
	if err != nil {
		return err
	}
	if !cloneThere {
		// Already moved into place by an earlier attempt.
		if done, err := exists(failed); err != nil {
			return err
		} else if !done {
			return errors.New("the copy of the data is missing")
		}
		if done, err := exists(s.Layout.Data); err != nil {
			return err
		} else if !done {
			return errors.New("the restored data is missing")
		}
	} else {
		if dataThere, err := exists(s.Layout.Data); err != nil {
			return err
		} else if dataThere {
			if err := os.Rename(s.Layout.Data, failed); err != nil {
				return err
			}
		}
		if err := os.Rename(clone, s.Layout.Data); err != nil {
			return err
		}
	}
	// Both sides of the cross-directory rename must survive a reboot before
	// services start. Repeat the syncs when recovering an already-finished move.
	if err := updatetxn.SyncDir(filepath.Dir(s.Layout.Data)); err != nil {
		return err
	}
	return updatetxn.SyncDir(filepath.Dir(clone))
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Cleanup removes the downloaded release and every older copy of the data.
// This update's copy and the previous program stay until the next update, so
// `sudo vult update undo` can still put them back.
func (s *Steps) Cleanup(p updatetxn.Plan) error {
	if err := s.Discard(p); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.Layout.Copies)
	if err != nil {
		return err
	}
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || id >= p.ID {
			continue
		}
		marker := filepath.Join(s.Layout.point(id), completeMark)
		info, err := os.Lstat(marker)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != int64(len(id)+1) {
			continue
		}
		data, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		if string(data) != id+"\n" {
			continue
		}
		// Only a completed point identifies data this updater set aside.
		// Never sweep sibling folders or unmarked copies by their names.
		if err := os.RemoveAll(s.Layout.failedData(id)); err != nil {
			return err
		}
		if err := updatetxn.SyncDir(filepath.Dir(s.Layout.Data)); err != nil {
			return err
		}
		if err := os.RemoveAll(s.Layout.point(id)); err != nil {
			return err
		}
	}
	return updatetxn.SyncDir(s.Layout.Copies)
}
