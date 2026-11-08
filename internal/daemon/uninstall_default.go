//go:build !linux

package daemon

import (
	"context"
	"errors"
	"fmt"
	"github.com/vult-ai/vult/internal/local"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func Uninstall(ctx context.Context, out, errout io.Writer) error {
	if runtime.GOOS != "darwin" {
		return errors.New("native uninstall requires macOS")
	}
	for _, path := range []string{"/Library", "/Library/PrivilegedHelperTools", programRoot} {
		if e := protected(path, 0, true); e != nil {
			return e
		}
	}
	if e := protected(local.BinaryPath, 0, false); e != nil {
		return e
	}
	fmt.Fprintf(out, "Uninstall removes Vult's software and startup entry.\nBooks, credentials and recovery key stay at %s\nThe _vultledger account is retained for reinstall. Backups stay wherever you saved them.\n", filepath.Dir(local.DataPath))
	command := local.BinaryPath + " _uninstall"
	script := fmt.Sprintf("do shell script %q with administrator privileges with prompt %q", command, "Remove Vult software and keep all Books and backups.")
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
	cmd.Stdout = out
	cmd.Stderr = errout
	if e := cmd.Run(); e != nil {
		return errors.New("software removal did not complete; retained storage was not deleted")
	}
	// Agents' plain copies of originals go with the software; the encrypted
	// originals stay in the Books.
	if folder, e := local.DocumentCopiesFolder(false); e == nil {
		if _, e = local.ClearDocumentCopies(folder, 0); e != nil {
			fmt.Fprintf(errout, "Some document copies could not be removed from %s\n", folder)
		} else if os.Remove(folder) == nil {
			fmt.Fprintf(out, "Agents' document copies were removed from %s\n", folder)
		}
	}
	return nil
}

func NativeStop(ctx context.Context) error {
	if runtime.GOOS != "darwin" || os.Geteuid() != 0 {
		return errors.New("native administrator control required")
	}
	for _, path := range []string{"/Library", "/Library/PrivilegedHelperTools", programRoot} {
		if e := protected(path, 0, true); e != nil {
			return e
		}
	}
	uid, gid, e := serviceIdentity()
	if e != nil {
		return e
	}
	if e = installationData(uid); e != nil {
		return e
	}
	run := func(name string, args ...string) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
		return cmd.Run()
	}
	if run("/bin/launchctl", "print", "system/ai.vult.browser") == nil {
		m, err := readRemovalManifest(nativeRemovalPaths())
		if err != nil || m.BrowserDaemonSHA256 == "" {
			return errors.New("unrecognized browser startup entry; no service was stopped")
		}
		if err := protected(filepath.Dir(browserStartup), 0, true); err != nil {
			return err
		}
		if err := protected(browserStartup, 0, false); err != nil {
			return err
		}
		if err := verifyBrowserStartup(nativeRemovalPaths(), m, 0); err != nil {
			return err
		}
		if err = run("/bin/launchctl", "bootout", "system/ai.vult.browser"); err != nil {
			return errors.New("could not stop the browser service")
		}
	}
	if run("/bin/launchctl", "print", "system/ai.vult.ledger") == nil {
		if e = run("/bin/launchctl", "bootout", "system/ai.vult.ledger"); e != nil {
			return errors.New("could not stop the ledger service")
		}
	}
	lock, e := os.OpenFile(filepath.Join(local.DataPath, "service.lock"), os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if e == nil {
		defer lock.Close()
		deadline := time.Now().Add(40 * time.Second)
		for {
			if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e == nil {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("ledger service has not stopped")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	} else if !os.IsNotExist(e) {
		return e
	}
	pid := filepath.Join(local.DataPath, "database", "postmaster.pid")
	if _, e = os.Lstat(pid); os.IsNotExist(e) {
		return nil
	} else if e != nil {
		return e
	}
	ctl := filepath.Join(PostgresPath, "bin/pg_ctl")
	if e = protected(ctl, 0, false); e != nil {
		return e
	}
	m, e := readRemovalManifest(nativeRemovalPaths())
	if e != nil {
		return e
	}
	match, e := matchesProgramFile(ctl, m.Files["postgres/bin/pg_ctl"], 0)
	if e != nil || !match {
		return errors.New("database control program does not match the installed inventory")
	}
	cmd := exec.CommandContext(ctx, ctl, "-D", filepath.Join(local.DataPath, "database"), "-m", "fast", "-w", "-t", "30", "stop")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TMPDIR=" + local.DataPath}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid, Groups: []uint32{gid}}}
	if e = cmd.Run(); e != nil {
		return errors.New("database did not stop; no program files were removed")
	}
	return nil
}

func UninstallNative(ctx context.Context, out io.Writer) error {
	if runtime.GOOS != "darwin" || os.Geteuid() != 0 {
		return errors.New("native administrator removal required")
	}
	p := nativeRemovalPaths()
	for _, path := range []string{"/Library", "/Library/PrivilegedHelperTools", p.Code} {
		if e := protected(path, 0, true); e != nil {
			return e
		}
	}
	if e := protected(filepath.Join(p.Code, programManifest), 0, false); e != nil {
		return e
	}
	m, e := readRemovalManifest(p)
	if e != nil {
		return e
	}
	parents := map[string]bool{filepath.Dir(p.Startup): true, p.Code: true}
	for name := range m.Files {
		path, e := programPath(p.Code, name)
		if e != nil {
			return e
		}
		for parent := filepath.Dir(path); inside(p.Code, parent); parent = filepath.Dir(parent) {
			parents[parent] = true
			if parent == p.Code {
				break
			}
		}
	}
	for parent := range parents {
		if _, e := os.Lstat(parent); os.IsNotExist(e) {
			continue
		}
		if e := protected(parent, 0, true); e != nil {
			return e
		}
	}
	// A shell shortcut is optional, and is touched only under protected parents.
	for _, path := range []string{"/usr", "/usr/local", "/usr/local/bin"} {
		if protected(path, 0, true) != nil {
			p.Shortcut = ""
			break
		}
	}
	stopCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	r, e := removeProgram(p, 0, func() error { return NativeStop(stopCtx) })
	if e != nil {
		return e
	}
	cmd := exec.CommandContext(ctx, "/usr/sbin/pkgutil", "--forget", "ai.vult.ledger")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	_ = cmd.Run() // Forgetting an absent receipt is harmless; it never removes files.
	fmt.Fprintf(out, "Vult software removed. Books and credentials remain at %s\nBackups and the _vultledger account were not removed.\n", p.Retained)
	if len(r.Kept) > 0 {
		fmt.Fprintln(out, "Modified or additional program files were kept:")
		for _, path := range r.Kept {
			fmt.Fprintln(out, path)
		}
	}
	return nil
}
