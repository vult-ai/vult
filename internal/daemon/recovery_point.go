package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/recovery"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// asService runs this installation's vult with args as the service account,
// in the data directory, with a fixed environment.
func asService(ctx context.Context, args []string, in io.Reader, out io.Writer) error {
	uid, gid, err := serviceIdentity()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, local.BinaryPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{gid}}}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.Dir = local.DataPath
	cmd.Stdin, cmd.Stdout = in, out
	var errOut bytes.Buffer
	cmd.Stderr = &limitedBuffer{buf: &errOut, left: 4096}
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("the maintenance step did not complete: %w", err)
	}
	return nil
}

type limitedBuffer struct {
	buf  *bytes.Buffer
	left int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > l.left {
		p = p[:l.left]
	}
	l.buf.Write(p)
	l.left -= len(p)
	return n, nil
}

// pgClean checks with pg_controldata that the database in a data directory
// was shut down cleanly.
func pgClean(dataDir string) error {
	exe := filepath.Join(local.PostgresPath, "bin", "pg_controldata")
	if err := protected(exe, 0, false); err != nil {
		return err
	}
	cmd := exec.Command(exe, "-D", filepath.Join(dataDir, "database"))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	output, err := cmd.Output()
	if err != nil {
		return errors.New("the database control file is not readable")
	}
	return controlDataClean(output)
}

func controlDataClean(output []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		name, value, ok := strings.Cut(scanner.Text(), ":")
		if ok && strings.TrimSpace(name) == "Database cluster state" {
			if state := strings.TrimSpace(value); state != "shut down" {
				return fmt.Errorf("the database is %q", state)
			}
			return nil
		}
	}
	return errors.New("the database control file has no state")
}

// recoveryRoot returns the recovery directory, creating it root-only.
func recoveryRoot() (string, error) {
	dir := local.RecoveryPath
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || st.Uid != 0 || info.Mode().Perm() != 0o700 {
		return "", errors.New("the recovery directory must be a root-only directory")
	}
	return dir, nil
}

func newRecoveryID(now time.Time) string {
	var b [4]byte
	rand.Read(b[:])
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

func requireNativeAdministrator() error {
	if !nativeSupported() || os.Geteuid() != 0 {
		return errors.New("recovery points need an administrator (sudo) on a native installation")
	}
	return nil
}

// TakeRecoveryPoint stops Vult, takes a recovery point (a copy of the data
// and an encrypted portable archive, both checked), and starts Vult again if
// it was running. It prints the point's directory and record.
func TakeRecoveryPoint(ctx context.Context, out io.Writer) error {
	if err := requireNativeAdministrator(); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return errors.New("recovery points need macOS; use an encrypted backup here")
	}
	uid, _, err := serviceIdentity()
	if err != nil {
		return err
	}
	conf, err := loadConfiguration(uid)
	if err != nil {
		return err
	}
	if conf.State != "ready" {
		return errors.New("Vult has not finished installing")
	}
	root, err := recoveryRoot()
	if err != nil {
		return err
	}
	running := nativeRunning(ctx)
	if running {
		if err := NativeStop(ctx); err != nil {
			return err
		}
	}
	rec, takeErr := takePoint(ctx, root, conf)
	if running {
		if err := nativeStart(ctx); err != nil {
			if takeErr == nil {
				return fmt.Errorf("the recovery point was taken but Vult did not start again: %w", err)
			}
			return errors.Join(takeErr, err)
		}
	}
	if takeErr != nil {
		return takeErr
	}
	return jsonReport(out, map[string]any{"status": "recovery_point_taken", "directory": filepath.Join(root, rec.ID), "record": rec})
}

func takePoint(ctx context.Context, root string, conf configuration) (recovery.Record, error) {
	if _, err := os.Lstat(filepath.Join(local.DataPath, "database", "postmaster.pid")); !errors.Is(err, os.ErrNotExist) {
		return recovery.Record{}, errors.New("the database is still running")
	}
	id := newRecoveryID(time.Now())
	point := recovery.Point{
		Dir:         filepath.Join(root, id),
		Data:        local.DataPath,
		RecoveryKey: conf.RecoveryKey,
		Clean:       pgClean,
		Fingerprint: func() (ledger.FactFingerprint, error) {
			var out bytes.Buffer
			if err := asService(ctx, []string{"_maintenance", MaintenanceFingerprint}, nil, &out); err != nil {
				return ledger.FactFingerprint{}, err
			}
			var report MaintenanceReport
			if err := strictjson.Decode(out.Bytes(), &report); err != nil {
				return ledger.FactFingerprint{}, errors.New("the fingerprint report is not readable")
			}
			if report.Applied != report.Known {
				return ledger.FactFingerprint{}, ledger.ErrNeedsMaintenance
			}
			return report.Fingerprint, nil
		},
		WriteArchive: func(w io.Writer) error {
			return asService(ctx, []string{"_maintenance", MaintenanceArchive}, nil, w)
		},
		Clone: recovery.Clone,
		Sync:  recovery.FullSync,
	}
	return point.Take(id)
}

// RestoreRecoveryPoint restores the Books in a recovery point's encrypted
// archive onto this installation, which must have no Books yet. The
// administrator supplies the recovery key; no Vult sign-in is needed. The
// whole archive is read back and checked before anything is restored.
func RestoreRecoveryPoint(ctx context.Context, archive, recoveryKey string, out io.Writer) error {
	if err := requireNativeAdministrator(); err != nil {
		return err
	}
	if _, err := recovery.VerifyArchiveFile(archive, recoveryKey); err != nil {
		return fmt.Errorf("the recovery point can't be restored: %w", err)
	}
	running := nativeRunning(ctx)
	if running {
		if err := NativeStop(ctx); err != nil {
			return err
		}
	}
	var report bytes.Buffer
	restoreErr := func() error {
		f, err := os.Open(archive)
		if err != nil {
			return err
		}
		defer f.Close()
		plain, err := recovery.Decrypt(f, recoveryKey)
		if err != nil {
			return err
		}
		return asService(ctx, []string{"_maintenance", MaintenanceRestore}, plain, &report)
	}()
	if err := nativeStart(ctx); err != nil {
		return errors.Join(restoreErr, err)
	}
	if restoreErr != nil {
		return restoreErr
	}
	var result MaintenanceReport
	if err := strictjson.Decode(report.Bytes(), &result); err != nil {
		return errors.New("the restore report is not readable")
	}
	return jsonReport(out, map[string]any{"status": "restored", "connections": "reauthorization_required", "fingerprint": result.Fingerprint})
}

func jsonReport(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
