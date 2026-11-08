package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/vult-ai/vult/internal/strictjson"
)

const programManifest = "program-files.json"

type removalManifest struct {
	Format              string            `json:"format"`
	Version             string            `json:"version"`
	Files               map[string]string `json:"files"`
	LaunchDaemonSHA256  string            `json:"launch_daemon_sha256"`
	BrowserDaemonSHA256 string            `json:"browser_daemon_sha256,omitempty"`
	ExternalFiles       map[string]string `json:"external_files,omitempty"`
}
type removalPaths struct{ Code, Startup, Policy, Shortcut, Retained string }
type removalResult struct {
	Removed int
	Kept    []string
}

func inside(base, path string) bool {
	rel, e := filepath.Rel(base, path)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
func (p removalPaths) validate() error {
	for _, path := range []string{p.Code, p.Startup, p.Retained} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return errors.New("invalid program removal path")
		}
	}
	for _, path := range []string{p.Code, p.Startup, p.Policy, p.Shortcut} {
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
	switch name {
	case "vult", "LICENSE", "NOTICE", "THIRD_PARTY_NOTICES.md",
		"Vult Browser Setup.app/Contents/Info.plist",
		"Vult Browser Setup.app/Contents/MacOS/browser-setup",
		"Vult Browser Setup.app/Contents/_CodeSignature/CodeResources":
	default:
		if !strings.HasPrefix(name, "postgres/") {
			return "", errors.New("unknown managed program path")
		}
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
	if e != nil || strictjson.Decode(b, &m) != nil || (m.Format != "vult-program-files-v1" && m.Format != "vult-program-files-linux-v1") || m.Version != Version || len(m.Files) == 0 || len(m.Files) > 4096 {
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
	if _, e = managedExternalFiles(p, m); e != nil {
		return m, e
	}
	return m, nil
}

func managedExternalFiles(p removalPaths, m removalManifest) (map[string]string, error) {
	files := map[string]string{}
	if m.Format == "vult-program-files-linux-v1" {
		if p.Policy == "" || m.LaunchDaemonSHA256 != "" || m.BrowserDaemonSHA256 != "" || len(m.ExternalFiles) != 3 {
			return nil, errors.New("invalid Linux program inventory")
		}
		for source, destination := range map[string]string{
			"/etc/systemd/system/ai.vult.ledger.service":       p.Startup,
			"/etc/systemd/system/ai.vult.browser.service":      filepath.Join(filepath.Dir(p.Startup), "ai.vult.browser.service"),
			"/usr/share/polkit-1/actions/ai.vult.owner.policy": p.Policy,
		} {
			files[destination] = m.ExternalFiles[source]
		}
	} else {
		if len(m.ExternalFiles) != 0 || p.Policy != "" {
			return nil, errors.New("unexpected external program files")
		}
		files[p.Startup] = m.LaunchDaemonSHA256
		if m.BrowserDaemonSHA256 != "" {
			files[filepath.Join(filepath.Dir(p.Startup), "ai.vult.browser.plist")] = m.BrowserDaemonSHA256
		}
	}
	for _, sum := range files {
		if len(sum) != 64 || strings.ToLower(sum) != sum {
			return nil, errors.New("invalid startup checksum")
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, errors.New("invalid startup checksum")
		}
	}
	return files, nil
}

func verifyBrowserStartup(p removalPaths, m removalManifest, uid uint32) error {
	if m.BrowserDaemonSHA256 == "" {
		return errors.New("unrecognized browser startup entry; no service was stopped")
	}
	match, err := matchesProgramFile(filepath.Join(filepath.Dir(p.Startup), "ai.vult.browser.plist"), m.BrowserDaemonSHA256, uid)
	if err != nil || !match {
		return errors.New("browser startup entry changed; no service was stopped")
	}
	return nil
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
	external, e := managedExternalFiles(p, m)
	if e != nil {
		return r, e
	}
	paths := make([]string, 0, len(external))
	for path := range external {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if e = remove(path, external[path]); e != nil {
			return r, e
		}
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
