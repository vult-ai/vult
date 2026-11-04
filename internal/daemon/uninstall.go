package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
)

const programRoot = "/Library/PrivilegedHelperTools/ai.vult.ledger"
const launchDaemon = "/Library/LaunchDaemons/ai.vult.ledger.plist"
const programManifest = "program-files.json"

type removalManifest struct {
	Format             string            `json:"format"`
	Version            string            `json:"version"`
	Files              map[string]string `json:"files"`
	LaunchDaemonSHA256 string            `json:"launch_daemon_sha256"`
}
type removalPaths struct{ Code, Plist, Shortcut, Retained string }
type removalResult struct {
	Removed int
	Kept    []string
}

func nativeRemovalPaths() removalPaths {
	return removalPaths{programRoot, launchDaemon, "/usr/local/bin/vult", filepath.Dir(local.DataPath)}
}
func inside(base, path string) bool {
	rel, e := filepath.Rel(base, path)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
func (p removalPaths) validate() error {
	for _, path := range []string{p.Code, p.Plist, p.Retained} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return errors.New("invalid program removal path")
		}
	}
	for _, path := range []string{p.Code, p.Plist, p.Shortcut} {
		if path != "" && (!filepath.IsAbs(path) || inside(p.Retained, path) || inside(path, p.Retained)) {
			return errors.New("program removal overlaps retained storage")
		}
	}
	return nil
}
func programPath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || strings.Contains(name, "\\") {
		return "", errors.New("invalid managed program path")
	}
	if name != "vult" && name != "LICENSE" && name != "NOTICE" && name != "THIRD_PARTY_NOTICES.md" && !strings.HasPrefix(name, "postgres/") {
		return "", errors.New("unknown managed program path")
	}
	path := filepath.Join(root, name)
	if !inside(root, path) {
		return "", errors.New("managed path escapes program directory")
	}
	for parent := filepath.Dir(path); inside(root, parent); parent = filepath.Dir(parent) {
		info, e := os.Lstat(parent)
		if e != nil {
			if os.IsNotExist(e) {
				break
			}
			return "", e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("managed directory is not a real directory")
		}
		if parent == root {
			break
		}
	}
	return path, nil
}
func matchesProgramFile(path, want string, uid uint32) (bool, error) {
	if len(want) != 64 {
		return false, errors.New("invalid managed checksum")
	}
	if _, e := hex.DecodeString(want); e != nil || strings.ToLower(want) != want {
		return false, errors.New("invalid managed checksum")
	}
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		if os.IsNotExist(e) {
			return false, nil
		}
		return false, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return false, e
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uid || stat.Nlink != 1 || info.Mode().Perm()&0022 != 0 || info.Size() > 128<<20 {
		return false, nil
	}
	h := sha256.New()
	if _, e = io.Copy(h, io.LimitReader(f, (128<<20)+1)); e != nil {
		return false, e
	}
	return hex.EncodeToString(h.Sum(nil)) == want, nil
}
func readRemovalManifest(p removalPaths) (removalManifest, error) {
	var m removalManifest
	if e := p.validate(); e != nil {
		return m, e
	}
	f, e := os.OpenFile(filepath.Join(p.Code, programManifest), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return m, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return m, errors.New("invalid program inventory")
	}
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil || strictjson.Decode(b, &m) != nil || m.Format != "vult-program-files-v1" || m.Version != Version || len(m.Files) == 0 || len(m.Files) > 4096 {
		return m, errors.New("unsupported program inventory")
	}
	if _, ok := m.Files["vult"]; !ok {
		return m, errors.New("program inventory is incomplete")
	}
	for name, sum := range m.Files {
		if _, e = programPath(p.Code, name); e != nil {
			return m, e
		}
		if len(sum) != 64 {
			return m, errors.New("invalid managed checksum")
		}
		if _, e = hex.DecodeString(sum); e != nil || strings.ToLower(sum) != sum {
			return m, errors.New("invalid managed checksum")
		}
	}
	if len(m.LaunchDaemonSHA256) != 64 {
		return m, errors.New("missing launch daemon checksum")
	}
	if _, e = hex.DecodeString(m.LaunchDaemonSHA256); e != nil {
		return m, e
	}
	return m, nil
}

// Removal is a whitelist of unchanged program files. No recursive deletion is
// used, and storage, credentials, system identities and unknown files stay put.
func removeProgram(p removalPaths, uid uint32, stop func() error) (removalResult, error) {
	r := removalResult{Kept: []string{}}
	m, e := readRemovalManifest(p)
	if e != nil {
		return r, e
	}
	if e = stop(); e != nil {
		return r, e
	}
	names := []string{}
	for name := range m.Files {
		if name != "vult" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	names = append(names, "vult")
	directories := map[string]bool{p.Code: true}
	remove := func(path, sum string) error {
		match, e := matchesProgramFile(path, sum, uid)
		if e != nil {
			return e
		}
		if !match {
			if _, e = os.Lstat(path); e == nil {
				r.Kept = append(r.Kept, path)
			}
			return nil
		}
		if e = os.Remove(path); e != nil {
			return e
		}
		r.Removed++
		return nil
	}
	if e = remove(p.Plist, m.LaunchDaemonSHA256); e != nil {
		return r, e
	}
	for _, name := range names {
		path, e := programPath(p.Code, name)
		if e != nil {
			return r, e
		}
		if e = remove(path, m.Files[name]); e != nil {
			return r, e
		}
		for parent := filepath.Dir(path); inside(p.Code, parent); parent = filepath.Dir(parent) {
			directories[parent] = true
			if parent == p.Code {
				break
			}
		}
	}
	if p.Shortcut != "" {
		info, e := os.Lstat(p.Shortcut)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			stat, ok := info.Sys().(*syscall.Stat_t)
			target, _ := os.Readlink(p.Shortcut)
			if ok && stat.Uid == uid && target == filepath.Join(p.Code, "vult") {
				if e = os.Remove(p.Shortcut); e != nil {
					return r, e
				}
			}
		}
	}
	if e = os.Remove(filepath.Join(p.Code, programManifest)); e != nil {
		return r, e
	}
	dirs := []string{}
	for dir := range directories {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		if e = os.Remove(dir); e != nil && !os.IsNotExist(e) {
			r.Kept = append(r.Kept, dir)
		}
	}
	return r, nil
}

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
	parents := map[string]bool{filepath.Dir(p.Plist): true, p.Code: true}
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
