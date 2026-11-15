package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// copyTree stands in for the copy-on-write clone in tests.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func pointFixture(t *testing.T) (Site, *time.Time) {
	t.Helper()
	dir := t.TempDir()
	s := Site{Root: filepath.Join(dir, "recovery"), Data: filepath.Join(dir, "data"), Clone: copyTree}
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	s.Clean = func(d string) error {
		if _, err := os.Stat(filepath.Join(d, "database", "postmaster.pid")); err == nil {
			return errors.New("running")
		}
		return nil
	}
	for name, body := range map[string]string{
		"configuration.json":    "synthetic configuration",
		"database/PG_VERSION":   "18",
		"database/base/1/16384": "synthetic relation",
		"documents/a.age":       "synthetic document",
	} {
		writeFixture(t, filepath.Join(s.Data, name), []byte(body), 0o600)
	}
	if err := os.Mkdir(s.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	return s, &now
}

// An update takes a checked copy first, and a failed update can be undone
// from it with every file as it was.
func TestInstallerPointPutsBackTheDataAfterAFailedUpdate(t *testing.T) {
	s, _ := pointFixture(t)
	before, err := BuildManifest(s.Data)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Take("0.8.0-rc.5", "0.8.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.From != "0.8.0-rc.5" || p.To != "0.8.0" || p.Updated {
		t.Fatal(p)
	}
	// The update fails part way: a file changed, one added, one gone.
	writeFixture(t, filepath.Join(s.Data, "database/base/1/16384"), []byte("half written"), 0o600)
	writeFixture(t, filepath.Join(s.Data, "database/base/1/16385"), []byte("new relation"), 0o600)
	if err := os.Remove(filepath.Join(s.Data, "documents/a.age")); err != nil {
		t.Fatal(err)
	}
	if err := s.Restore(p.ID); err != nil {
		t.Fatal(err)
	}
	after, err := BuildManifest(s.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatal("restored file set differs", after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatal("restored file differs", after[i].Path)
		}
	}
	if string(fixtureBytes(t, filepath.Join(s.Data, "database/base/1/16384"))) != "synthetic relation" {
		t.Fatal("relation file not put back")
	}
	// The data it replaced is kept, not deleted.
	kept, _ := filepath.Glob(filepath.Join(s.Root, p.ID, "replaced-*", "database/base/1/16385"))
	if len(kept) != 1 {
		t.Fatal("the replaced data was not kept")
	}
}

func TestInstallerPointRefusesARunningOrChangedCopy(t *testing.T) {
	s, _ := pointFixture(t)
	writeFixture(t, filepath.Join(s.Data, "database/postmaster.pid"), []byte("1"), 0o600)
	if _, err := s.Take("", "0.8.0"); err == nil {
		t.Fatal("a running database was copied")
	}
	if entries, _ := os.ReadDir(s.Root); len(entries) != 0 {
		t.Fatal("a refused point left files")
	}
	os.Remove(filepath.Join(s.Data, "database/postmaster.pid"))
	p, err := s.Take("", "0.8.0")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(s.Root, p.ID, DataDir, "configuration.json"), []byte("changed"), 0o600)
	if err := s.Restore(p.ID); err == nil {
		t.Fatal("a changed copy was restored")
	}
	if string(fixtureBytes(t, filepath.Join(s.Data, "configuration.json"))) != "synthetic configuration" {
		t.Fatal("the live data changed")
	}
	if err := s.Restore("../" + p.ID); err == nil {
		t.Fatal("a path outside the recovery directory was accepted")
	}
}

// Each completed update keeps its point; only the two newest installer
// points stay, interrupted ones go, and the owner's own points are kept.
func TestInstallerPointsKeepTheNewestTwo(t *testing.T) {
	s, now := pointFixture(t)
	writeFixture(t, filepath.Join(s.Root, "20261101T000000Z-0a0b0c0d", VerifiedFile), []byte("{}"), 0o600)
	writeFixture(t, filepath.Join(s.Root, "installer-interrupted", "data/configuration.json"), []byte("partial"), 0o600)
	var ids []string
	for i := 0; i < 4; i++ {
		*now = now.Add(time.Hour)
		p, err := s.Take("", "0.8.0")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Finish(p.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	entries, _ := os.ReadDir(s.Root)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := map[string]bool{"20261101T000000Z-0a0b0c0d": true, ids[2]: true, ids[3]: true}
	if len(names) != len(want) {
		t.Fatal("kept", names)
	}
	for _, n := range names {
		if !want[n] {
			t.Fatal("kept", names)
		}
	}
	latest, ok := s.Latest()
	if !ok || latest.ID != ids[3] || !latest.Updated {
		t.Fatal(latest, ok)
	}
}

// A point whose update hasn't completed is not reported as a backup taken
// before an update.
func TestUnfinishedInstallerPointIsNotReported(t *testing.T) {
	s, _ := pointFixture(t)
	if _, err := s.Take("", "0.8.0"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Latest(); ok {
		t.Fatal("an unfinished point was reported")
	}
}

// The same round trip with the file system's own clone.
func TestInstallerPointRoundTripWithTheFileSystemClone(t *testing.T) {
	s, _ := pointFixture(t)
	s.Clone = Clone
	p, err := s.Take("", "0.8.0")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(s.Data, "configuration.json"), []byte("changed by a failed update"), 0o600)
	if err := s.Restore(p.ID); err != nil {
		t.Fatal(err)
	}
	if string(fixtureBytes(t, filepath.Join(s.Data, "configuration.json"))) != "synthetic configuration" {
		t.Fatal("not put back")
	}
	if info, err := os.Stat(s.Data); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("the data directory lost its protection", info.Mode(), err)
	}
}

func writeFixture(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func fixtureBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
