//go:build darwin || linux

package updatehelper

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"

	"github.com/vult-ai/vult/internal/updaterepo"
	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/internal/updatetrust"
	"github.com/vult-ai/vult/internal/updatetxn"
)

// A real signed update repository with throwaway keys, served from a folder.

var repoNow = time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)

const (
	repoBase   = "https://repository.invalid"
	passphrase = "a long test passphrase"
)

func init() { updaterepo.ScryptWorkFactor = 10 }

type folderFetcher struct{ root string }

func (f folderFetcher) DownloadFile(url string, maxLength int64, _ time.Duration) ([]byte, error) {
	rel, ok := strings.CutPrefix(url, repoBase+"/")
	if !ok {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: 404, URL: url}
	}
	data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: 404, URL: url}
	}
	if int64(len(data)) > maxLength {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: "too long"}
	}
	return data, nil
}

type signedRepo struct {
	t    *testing.T
	repo updaterepo.Repo
	keys map[string]string
}

func (r *signedRepo) signer(name string) *updaterepo.Signer {
	s, err := updaterepo.OpenKey(r.keys[name], []byte(passphrase))
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(s.Close)
	return s
}

func (r *signedRepo) sign(role, key string) {
	r.t.Helper()
	if err := r.repo.Sign(role, r.signer(key), func(string) bool { return true }); err != nil {
		r.t.Fatalf("sign %s: %v", role, err)
	}
}

func (r *signedRepo) publish() {
	r.t.Helper()
	if err := r.repo.Freshen(r.signer("N"), r.signer("M"), repoNow); err != nil {
		r.t.Fatal(err)
	}
	if err := r.repo.Publish(repoNow); err != nil {
		r.t.Fatal(err)
	}
}

func newSignedRepo(t *testing.T) *signedRepo {
	t.Helper()
	dir := t.TempDir()
	r := &signedRepo{t: t, keys: map[string]string{}}
	pub := map[string]*metadata.Key{}
	for _, name := range []string{"K1", "K2", "K3", "T1", "T2", "T3", "S", "B", "N", "M"} {
		file := filepath.Join(dir, name+".key")
		k, _, err := updaterepo.NewKey(file, []byte(passphrase))
		if err != nil {
			t.Fatal(err)
		}
		r.keys[name], pub[name] = file, k
	}
	repo, err := updaterepo.Init(filepath.Join(dir, "repo"), updaterepo.Keys{
		Root: []*metadata.Key{pub["K1"], pub["K2"], pub["K3"]}, Targets: []*metadata.Key{pub["T1"], pub["T2"], pub["T3"]},
		Release: []*metadata.Key{pub["S"]}, Rebuild: pub["B"], Snapshot: pub["N"], Timestamp: pub["M"],
	}, repoNow)
	if err != nil {
		t.Fatal(err)
	}
	r.repo = repo
	for _, s := range [][2]string{{"root", "K1"}, {"root", "K2"}, {"targets", "T1"}, {"targets", "T2"}, {"releases", "S"}, {"holds", "S"}, {"rebuild", "B"}, {"updater", "K1"}, {"updater", "K2"}} {
		r.sign(s[0], s[1])
	}
	r.publish()
	return r
}

func releaseContract(version, schemaFrom string) updatetrust.Contract {
	return updatetrust.Contract{
		Version: version, Platform: updatetrust.PlatformMacOS, MinimumOS: "26.0",
		SchemaFrom: []string{schemaFrom}, SchemaTo: "0008", MinimumUpdater: "0.7.0",
		PostgreSQL: updatetrust.PostgreSQL{Major: 18, Version: "18.6", SourceSHA256: strings.Repeat("b", 64)},
		Source:     updatetrust.Source{Repository: "example/vult", Commit: strings.Repeat("c", 40), Tag: "v" + version},
		Build:      updatetrust.Build{Method: updatetrust.BuildIdentical, InputsSHA256: strings.Repeat("d", 64)},
	}
}

func newProgram(t *testing.T) []byte {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	w.WriteHeader(&tar.Header{Name: "vult", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg})
	w.Write([]byte("new"))
	w.Close()
	return b.Bytes()
}

// release adds a release both the release key and the rebuild sign.
func (r *signedRepo) release(c updatetrust.Contract, payload []byte) {
	r.t.Helper()
	file := filepath.Join(r.t.TempDir(), "payload.tar")
	os.WriteFile(file, payload, 0o644)
	if _, err := r.repo.AddRelease(file, c, repoNow); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.repo.AddRebuild(file, c, repoNow); err != nil {
		r.t.Fatal(err)
	}
	r.sign(updatetrust.RoleRebuild, "B")
	r.sign(updatetrust.RoleReleases, "S")
	r.publish()
}

func (r *signedRepo) trust(t *testing.T) updatetrust.Config {
	first, err := os.ReadFile(filepath.Join(r.repo.Dir, "published", "metadata", "1.root.json"))
	if err != nil {
		t.Fatal(err)
	}
	return updatetrust.Config{InitialRoot: first, MetadataDir: t.TempDir(), MetadataURL: repoBase + "/metadata", TargetsURL: repoBase + "/targets",
		Fetcher: folderFetcher{root: filepath.Join(r.repo.Dir, "published")}, Now: func() time.Time { return repoNow }}
}

func requested(t *testing.T, version string) string {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "request.json"), nil, 0o644)
	if err := updatestatus.WriteRequest(dir, updatestatus.Request{Version: version}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Pressing Update installs the newest release both signatures agree on.
func TestUpdateButtonInstallsAVerifiedRelease(t *testing.T) {
	r := newSignedRepo(t)
	r.release(releaseContract("0.7.0", "0007"), newProgram(t))
	mc := newMachine(t)
	h := helperFor(mc, requested(t, "0.7.0"))
	h.Trust = r.trust(t)
	status, err := h.Run(context.Background())
	if err != nil || status.Installed != "0.7.0" {
		t.Fatalf("%+v, %v", status, err)
	}
	os.Remove(mc.plan.Artifact) // the made-up plan's file, not used here
	if mc.m.live() != "new" || mc.m.db()["schema"] != "0008" || !mc.m.running {
		t.Fatal("not installed")
	}
	if staged, _ := os.ReadDir(mc.m.layout.Staging()); len(staged) != 0 {
		t.Fatalf("download left in staging: %v", staged)
	}
	installed, _ := Installed(mc.m.layout)
	if installed.Schema != "0008" || len(installed.SHA256) != 64 {
		t.Fatalf("recorded %+v", installed)
	}
}

type countingFetcher struct {
	folderFetcher
	paths []string
	after func()
}

func (f *countingFetcher) DownloadFile(url string, limit int64, timeout time.Duration) ([]byte, error) {
	f.paths = append(f.paths, url)
	if f.after != nil {
		f.after()
	}
	return f.folderFetcher.DownloadFile(url, limit, timeout)
}

func TestUbuntuDiscoversVerifiedReleasesWithoutInstalling(t *testing.T) {
	r := newSignedRepo(t)
	c := releaseContract("0.7.0", "0007")
	c.Platform, c.MinimumOS = updatetrust.PlatformUbuntu, "24.04"
	r.release(c, newProgram(t))
	mc := newMachine(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "check.json"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := updatestatus.RequestCheck(dir, "0.6.0", repoNow); err != nil {
		t.Fatal(err)
	}
	h := helperFor(mc, dir)
	h.Platform, h.OSVersion = updatetrust.PlatformUbuntu, "24.04"
	h.Now = func() time.Time { return repoNow }
	h.Trust = r.trust(t)
	fetcher := &countingFetcher{folderFetcher: folderFetcher{root: filepath.Join(r.repo.Dir, "published")}}
	h.Trust.Fetcher = fetcher
	status, err := h.Run(context.Background())
	if err != nil || status.Installed != "0.6.0" || status.State != "idle" {
		t.Fatalf("%+v, %v", status, err)
	}
	for _, path := range fetcher.paths {
		if strings.Contains(path, "/targets/") {
			t.Fatalf("discovery downloaded an artifact: %s", path)
		}
	}
	report := updatestatus.Read(mc.m.layout.State)
	if report.Release == nil || report.Release.Latest != "0.7.0" || !report.Release.Fresh("0.6.0", repoNow) {
		t.Fatalf("no verified offer: %+v", report.Release)
	}
	if updatestatus.CheckRequested(dir) || mc.m.live() != "old" || !mc.m.running || mc.m.opened {
		t.Fatal("checking installed or stopped Vult, or left its request pending")
	}
	if _, err := LastUpdate(mc.m.layout); err != ErrNothingToUndo {
		t.Fatalf("checking created an update: %v", err)
	}
}

func TestLocallyHeldReleaseIsNotDownloadedAgain(t *testing.T) {
	r := newSignedRepo(t)
	r.release(releaseContract("0.7.0", "0007"), newProgram(t))
	mc := newMachine(t)
	if err := mc.journal.Write(updatetxn.Record{State: updatetxn.StateIdle, Attempts: map[string]int{"0.7.0": updatetxn.MaxAttempts}}); err != nil {
		t.Fatal(err)
	}
	h := helperFor(mc, requested(t, "0.7.0"))
	h.Trust = r.trust(t)
	fetcher := &countingFetcher{folderFetcher: folderFetcher{root: filepath.Join(r.repo.Dir, "published")}}
	h.Trust.Fetcher = fetcher
	status, err := h.Run(context.Background())
	if err != nil || !strings.Contains(status.Reason, "failed here three times") {
		t.Fatalf("%+v, %v", status, err)
	}
	for _, url := range fetcher.paths {
		if strings.Contains(url, "/targets/") {
			t.Fatal("downloaded a locally held release")
		}
	}
	if mc.m.live() != "old" || !mc.m.running {
		t.Fatal("a locally held release changed Vult")
	}
}

func TestInterruptedDownloadDoesNotBlockTheNextAttempt(t *testing.T) {
	r := newSignedRepo(t)
	payload := newProgram(t)
	r.release(releaseContract("0.7.0", "0007"), payload)
	mc := newMachine(t)
	partial := filepath.Join(mc.m.layout.Staging(), fmt.Sprintf("%x.%s.partial", sha256.Sum256(payload), path.Base(updatetrust.TargetName("0.7.0", updatetrust.PlatformMacOS))))
	if err := os.WriteFile(partial, []byte("an interrupted download"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := helperFor(mc, requested(t, "0.7.0"))
	h.Trust = r.trust(t)
	status, err := h.Run(context.Background())
	if err != nil || status.Installed != "0.7.0" {
		t.Fatalf("%+v, %v", status, err)
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("the interrupted download remained", err)
	}
}

func TestInstallRequestDuringDiscoveryIsHandledInTheSameRun(t *testing.T) {
	r := newSignedRepo(t)
	r.release(releaseContract("0.7.0", "0007"), newProgram(t))
	mc := newMachine(t)
	dir := t.TempDir()
	for _, name := range []string{"request.json", "check.json"} {
		os.WriteFile(filepath.Join(dir, name), nil, 0o644)
	}
	if _, err := updatestatus.RequestCheck(dir, "0.6.0", repoNow); err != nil {
		t.Fatal(err)
	}
	h := helperFor(mc, dir)
	h.Trust = r.trust(t)
	wrote := false
	fetcher := &countingFetcher{folderFetcher: folderFetcher{root: filepath.Join(r.repo.Dir, "published")}, after: func() {
		if !wrote {
			wrote = true
			if err := updatestatus.WriteRequest(dir, updatestatus.Request{Version: "0.7.0"}); err != nil {
				t.Fatal(err)
			}
		}
	}}
	h.Trust.Fetcher = fetcher
	status, err := h.Run(context.Background())
	if err != nil || status.Installed != "0.7.0" || mc.m.live() != "new" {
		t.Fatalf("the owner's request was stranded: %+v, %v", status, err)
	}
	if _, pending := updatestatus.ReadRequest(dir); pending {
		t.Fatal("the install request remained pending")
	}
}

func TestUnavailableRandomnessCannotReuseARecoveryIdentifier(t *testing.T) {
	r := newSignedRepo(t)
	r.release(releaseContract("0.7.0", "0007"), newProgram(t))
	mc := newMachine(t)
	h := helperFor(mc, requested(t, "0.7.0"))
	h.Trust = r.trust(t)
	h.Rand = bytes.NewReader(nil)
	fetcher := &countingFetcher{folderFetcher: folderFetcher{root: filepath.Join(r.repo.Dir, "published")}}
	h.Trust.Fetcher = fetcher
	status, err := h.Run(context.Background())
	if err != nil || !strings.Contains(status.Reason, "identifier") {
		t.Fatalf("%+v, %v", status, err)
	}
	for _, url := range fetcher.paths {
		if strings.Contains(url, "/targets/") {
			t.Fatal("downloaded a release without a safe transaction identifier")
		}
	}
	if mc.m.live() != "old" || !mc.m.running {
		t.Fatal("failed randomness changed Vult")
	}
}

func TestUpdateButtonRefusesWhatMustNotInstall(t *testing.T) {
	for name, c := range map[string]struct {
		setup  func(*signedRepo)
		reason string
	}{
		"held": {func(r *signedRepo) {
			r.release(releaseContract("0.7.0", "0007"), newProgram(t))
			r.repo.Hold("0.7.0", repoNow)
			r.sign(updatetrust.RoleHolds, "S")
			r.publish()
		}, "up to date"},
		"another database": {func(r *signedRepo) {
			r.release(releaseContract("0.7.0", "0006"), newProgram(t))
		}, "can't update a database"},
		"changed download": {func(r *signedRepo) {
			r.release(releaseContract("0.7.0", "0007"), newProgram(t))
			files, _ := filepath.Glob(filepath.Join(r.repo.Dir, "published", "targets", "release", "*"))
			for _, f := range files {
				data, _ := os.ReadFile(f)
				data[len(data)-1] ^= 1
				os.WriteFile(f, data, 0o644)
			}
		}, "fingerprint"},
		"unsafe archive": {func(r *signedRepo) {
			var payload bytes.Buffer
			w := tar.NewWriter(&payload)
			w.WriteHeader(&tar.Header{Name: "../outside", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg})
			w.Write([]byte("new"))
			w.Close()
			r.release(releaseContract("0.7.0", "0007"), payload.Bytes())
		}, "unsafe path"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSignedRepo(t)
			c.setup(r)
			mc := newMachine(t)
			h := helperFor(mc, requested(t, "0.7.0"))
			h.Trust = r.trust(t)
			status, err := h.Run(context.Background())
			if err != nil || !strings.Contains(status.Reason, c.reason) {
				t.Fatalf("%+v, %v; want %q", status, err, c.reason)
			}
			if mc.m.live() != "old" || !mc.m.running {
				t.Fatal("something changed")
			}
			if j, _ := (&updatetxn.Journal{Dir: mc.m.layout.Journal()}).Read(); j.State != updatetxn.StateIdle {
				t.Fatalf("journal at %s", j.State)
			}
		})
	}
}
