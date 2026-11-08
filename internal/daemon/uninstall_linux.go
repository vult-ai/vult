package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vult-ai/vult/internal/fssecurity"
	"github.com/vult-ai/vult/internal/local"
	"golang.org/x/sys/unix"
)

type nativeUnitState struct {
	State string
	PID   uint32
}

func parseNativeUnit(data []byte, path, user string) (nativeUnitState, error) {
	var state nativeUnitState
	properties := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return state, errors.New("service metadata is unavailable")
		}
		if _, exists := properties[key]; exists {
			return state, errors.New("service metadata is ambiguous")
		}
		properties[key] = value
	}
	for _, key := range []string{"LoadState", "FragmentPath", "DropInPaths", "NeedDaemonReload", "User", "Group", "ActiveState", "MainPID", "ControlGroup"} {
		if _, exists := properties[key]; !exists {
			return state, errors.New("service metadata is incomplete")
		}
	}
	pid, err := strconv.ParseUint(properties["MainPID"], 10, 32)
	if err != nil || properties["LoadState"] != "loaded" || properties["FragmentPath"] != path || properties["DropInPaths"] != "" || properties["NeedDaemonReload"] != "no" || properties["User"] != user || properties["Group"] != user {
		return state, errors.New("service definition differs from the protected installation")
	}
	state = nativeUnitState{State: properties["ActiveState"], PID: uint32(pid)}
	switch state.State {
	case "active", "activating", "deactivating", "reloading":
	case "inactive", "failed":
		if state.PID != 0 {
			return state, errors.New("stopped service still has a main process")
		}
	default:
		return state, errors.New("service state is unavailable")
	}
	expected := "/system.slice/" + filepath.Base(path)
	if group := properties["ControlGroup"]; group != expected && !(group == "" && state.PID == 0 && (state.State == "inactive" || state.State == "failed")) {
		return state, errors.New("service process group differs from the protected installation")
	}
	return state, nil
}

func systemctl(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	output, err := command.Output()
	if err != nil || len(output) > 16384 {
		return nil, errors.New("protected service control did not complete")
	}
	return output, nil
}

func nativeUnit(ctx context.Context, path, user string) (nativeUnitState, error) {
	data, err := systemctl(ctx, "show", "--property=LoadState,FragmentPath,DropInPaths,NeedDaemonReload,User,Group,ActiveState,MainPID,ControlGroup", filepath.Base(path))
	if err != nil {
		return nativeUnitState{}, err
	}
	return parseNativeUnit(data, path, user)
}

func nativeStartupLinks(p removalPaths) []string {
	directory := filepath.Join(filepath.Dir(p.Startup), "multi-user.target.wants")
	return []string{filepath.Join(directory, "ai.vult.ledger.service"), filepath.Join(directory, "ai.vult.browser.service")}
}

func verifyStartupLink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return errors.New("startup link differs from the protected installation")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	target, err := os.Readlink(path)
	expected := filepath.Join(filepath.Dir(filepath.Dir(path)), filepath.Base(path))
	if !ok || stat.Uid != 0 || err != nil || target != expected {
		return errors.New("startup link differs from the protected installation")
	}
	return nil
}

func linuxInstallationControl() (removalManifest, error) {
	p := nativeRemovalPaths()
	if !nativeSupported() || os.Geteuid() != 0 {
		return removalManifest{}, errors.New("native administrator control required")
	}
	if err := installationCode(); err != nil {
		return removalManifest{}, err
	}
	m, err := readRemovalManifest(p)
	if err != nil || m.Format != "vult-program-files-linux-v1" {
		return m, errors.New("unrecognized Linux installation; no service was stopped")
	}
	if err := protected(filepath.Join(p.Code, programManifest), 0, false); err != nil {
		return m, err
	}
	for _, name := range []string{"vult", "LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md", "postgres/bin/postgres", "postgres/bin/initdb", "postgres/bin/pg_ctl"} {
		if _, ok := m.Files[name]; !ok {
			return m, errors.New("installed program inventory is incomplete")
		}
	}
	match, err := matchesProgramFile(local.BinaryPath, m.Files["vult"], 0)
	if err != nil || !match {
		return m, errors.New("installed control program changed; no service was stopped")
	}
	external, err := managedExternalFiles(p, m)
	if err != nil {
		return m, err
	}
	for path, sum := range external {
		for parent := filepath.Dir(path); parent != "/"; parent = filepath.Dir(parent) {
			if err := protected(parent, 0, true); err != nil {
				return m, err
			}
		}
		match, err := matchesProgramFile(path, sum, 0)
		if err != nil || !match {
			return m, errors.New("installed service or policy changed; no service was stopped")
		}
		if err := protected(path, 0, false); err != nil {
			return m, err
		}
	}
	for _, path := range nativeStartupLinks(p) {
		if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
			if err := protected(filepath.Dir(path), 0, true); err != nil {
				return m, err
			}
		}
		if err := verifyStartupLink(path); err != nil {
			return m, err
		}
	}
	var stat unix.Statfs_t
	if err := unix.Statfs("/sys/fs/cgroup", &stat); err != nil || stat.Type != unix.CGROUP2_SUPER_MAGIC {
		return m, errors.New("native service control requires the unified process controller")
	}
	return m, nil
}

func cgroupStopped(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || len(data) > 1024 {
		return errors.New("service process cleanup could not be verified")
	}
	seen := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 0 && fields[0] == "populated" {
			if seen || len(fields) != 2 || fields[1] != "0" {
				return errors.New("service still has running processes")
			}
			seen = true
		}
	}
	if !seen {
		return errors.New("service process cleanup could not be verified")
	}
	return nil
}

func NativeStop(ctx context.Context) error {
	if _, err := linuxInstallationControl(); err != nil {
		return err
	}
	uid, _, err := serviceIdentity()
	if err != nil {
		return err
	}
	if err := installationData(uid); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	units := []struct{ path, user string }{{browserStartup, local.BrowserUser}, {ledgerStartup, local.ServiceUser}}
	// Preflight both loaded definitions before stopping either service.
	for _, unit := range units {
		if _, err := nativeUnit(ctx, unit.path, unit.user); err != nil {
			return err
		}
	}
	for _, unit := range units {
		if _, err := systemctl(ctx, "stop", filepath.Base(unit.path)); err != nil {
			return err
		}
		state, err := nativeUnit(ctx, unit.path, unit.user)
		if err != nil || state.PID != 0 || (state.State != "inactive" && state.State != "failed") {
			return errors.New("service did not stop; program files were preserved")
		}
		if err := cgroupStopped(filepath.Join("/sys/fs/cgroup/system.slice", filepath.Base(unit.path), "cgroup.events")); err != nil {
			return err
		}
	}
	lock, err := os.OpenFile(filepath.Join(local.DataPath, "service.lock"), os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("service stop could not be verified")
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return errors.New("service stop could not be verified")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uid || stat.Nlink != 1 || info.Mode().Perm() != 0600 || !fssecurity.NoExtendedACL(lock) || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return errors.New("ledger service still owns its storage lock")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return nil
}

func Uninstall(ctx context.Context, out, errout io.Writer) error {
	if !nativeSupported() {
		return errors.New("native uninstall requires a supported installation")
	}
	if err := nativeStartupReady(); err != nil {
		return err
	}
	if err := installationCode(); err != nil {
		return err
	}
	fmt.Fprintln(out, "Administrator approval removes the program and its services. Books, keys and backups are kept.")
	command := exec.CommandContext(ctx, "/usr/bin/sudo", "--", local.BinaryPath, "_uninstall")
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, out, errout
	if err := command.Run(); err != nil {
		return errors.New("software removal did not complete; retained storage was not deleted")
	}
	return nil
}

func UninstallNative(ctx context.Context, out io.Writer) error {
	if !nativeSupported() || os.Geteuid() != 0 {
		return errors.New("native administrator removal required")
	}
	if err := protected("/run", 0, true); err != nil {
		return err
	}
	lock, err := lockProgramControl("/run/vult-install.lock", 0)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := nativeStartupReady(); err != nil {
		return err
	}
	if _, err := linuxInstallationControl(); err != nil {
		return err
	}
	p := nativeRemovalPaths()
	for _, path := range []string{"/usr", "/usr/local", "/usr/local/bin"} {
		if protected(path, 0, true) != nil {
			p.Shortcut = ""
			break
		}
	}
	result, err := removeProgram(p, 0, func() error {
		if err := NativeStop(ctx); err != nil {
			return err
		}
		for _, path := range nativeStartupLinks(p) {
			if err := verifyStartupLink(path); err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return errors.New("startup entries could not be removed; program files were preserved")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	_, reloadErr := systemctl(ctx, "daemon-reload")
	fmt.Fprintf(out, "Vult software removed. Books and credentials remain at %s\nBackups, certificate trust and both service accounts were not removed.\n", p.Retained)
	if len(result.Kept) != 0 {
		fmt.Fprintln(out, "Modified or additional program files were kept:")
		for _, path := range result.Kept {
			fmt.Fprintln(out, path)
		}
	}
	if reloadErr != nil {
		return errors.New("program removal completed; refresh the system service manager before reinstalling")
	}
	return nil
}

func lockProgramControl(path string, uid uint32) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errors.New("program control lock is unavailable")
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, errors.New("program control lock is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uid || stat.Nlink != 1 || info.Mode().Perm() != 0600 || !fssecurity.NoExtendedACL(file) {
		file.Close()
		return nil, errors.New("program control lock has unsafe ownership or permissions")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another installer or software removal is active; no files were changed")
	}
	return file, nil
}
