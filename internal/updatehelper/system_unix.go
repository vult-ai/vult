//go:build darwin || linux

package updatehelper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/recovery"
	"golang.org/x/sys/unix"
)

const maxReport = 1 << 20

// unixSystem is what macOS and Linux share: running the live program's
// maintenance step as the service account, the health check, the database
// checks, copying data and free space.
type unixSystem struct {
	Layout Layout
}

func fixedEnv() []string { return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"} }

func (m unixSystem) asService(ctx context.Context, args []string, out io.Writer) error {
	u, err := user.Lookup(local.ServiceUser)
	if err != nil {
		return errors.New("the service account is missing")
	}
	uid, err1 := strconv.ParseUint(u.Uid, 10, 32)
	gid, err2 := strconv.ParseUint(u.Gid, 10, 32)
	if err1 != nil || err2 != nil || uid == 0 || gid == 0 {
		return errors.New("the service account is not valid")
	}
	// Only fixed argument vectors, and only the program in the live
	// program directory, which this helper installed or verified.
	cmd := exec.CommandContext(ctx, filepath.Join(m.Layout.Program, "vult"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{uint32(gid)}}}
	cmd.Env = fixedEnv()
	cmd.Dir = m.Layout.Data
	cmd.Stdout = out
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if len(msg) > 2000 {
			msg = msg[:2000]
		}
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

type cappedBuffer struct{ bytes.Buffer }

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.Len()+len(p) > maxReport {
		return 0, errors.New("report too large")
	}
	return c.Buffer.Write(p)
}

func (m unixSystem) Maintenance(ctx context.Context, command string) ([]byte, error) {
	switch command {
	case "migrate", "validate":
	default:
		return nil, errors.New("not a maintenance command the helper runs")
	}
	var out cappedBuffer
	if err := m.asService(ctx, []string{"_maintenance", command}, &out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (m unixSystem) Healthy(ctx context.Context) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		client, err := local.NewClient()
		if err == nil {
			probe, done := context.WithTimeout(ctx, 3*time.Second)
			var raw json.RawMessage
			raw, err = client.Call(probe, "status", "", struct{}{})
			done()
			if err == nil && len(raw) > 0 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("the ledger did not become ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (m unixSystem) DatabaseStopped(dataDir string) error {
	if _, err := os.Lstat(filepath.Join(dataDir, "database", "postmaster.pid")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("the database is still running")
	}
	return nil
}

func (m unixSystem) Clean(dataDir string) error {
	cmd := exec.Command(filepath.Join(m.Layout.Program, "postgres", "bin", "pg_controldata"), "-D", filepath.Join(dataDir, "database"))
	cmd.Env = fixedEnv()
	out, err := cmd.Output()
	if err != nil {
		return errors.New("the database control file is not readable")
	}
	for _, line := range strings.Split(string(out), "\n") {
		if name, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(name) == "Database cluster state" {
			if state := strings.TrimSpace(value); state != "shut down" {
				return fmt.Errorf("the database is %q", state)
			}
			return nil
		}
	}
	return errors.New("the database control file has no state")
}

func (m unixSystem) Clone(src, dst string) error { return recovery.Clone(src, dst) }

func (m unixSystem) FreeBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// waitForLedgerStop waits until the ledger has released its lock, which it
// holds until it and its database have stopped.
func (m unixSystem) waitForLedgerStop(ctx context.Context) error {
	lock, err := os.OpenFile(filepath.Join(m.Layout.Data, "service.lock"), os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the ledger did not stop")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}
