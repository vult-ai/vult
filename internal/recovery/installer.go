package recovery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// The installer takes a point before it updates an existing Vult's
// database: a checked copy of the stopped data, kept beside the other
// recovery points. If the update fails the installer puts the copy back.
// Only the newest InstallersKept installer points are kept; points the
// owner took with `vult recovery-point` are never removed.
const (
	InstallerFile  = "installer.json" // written last, once the copy checked out
	InstallersKept = 2
)

// InstallerPoint is the record written last into an installer point.
type InstallerPoint struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	From    string    `json:"from,omitempty"` // the version installed before, when known
	To      string    `json:"to"`
	Size    int64     `json:"size"` // bytes of data the copy holds
	// Updated is set once the update it was taken for completed.
	Updated bool `json:"updated"`
}

// Site is where installer points are taken from and kept, so tests can
// stand in for the installed paths.
type Site struct {
	Root  string // the recovery directory
	Data  string // the live data directory, with Vult stopped
	Clean func(dataDir string) error
	Clone func(src, dst string) error
	Now   func() time.Time
}

func (s Site) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// take copies the stopped data into a new point and checks the copy: both
// databases were shut down cleanly and every file matches. The record is
// written last, so a point without one was interrupted and is never used.
func (s Site) Take(from, to string) (InstallerPoint, error) {
	if err := s.Clean(s.Data); err != nil {
		return InstallerPoint{}, fmt.Errorf("the database was not shut down cleanly: %w", err)
	}
	p := InstallerPoint{ID: "installer-" + NewID(s.now()), Created: s.now().UTC(), From: from, To: to}
	dir := filepath.Join(s.Root, p.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return InstallerPoint{}, err
	}
	fail := func(err error) (InstallerPoint, error) {
		os.RemoveAll(dir)
		return InstallerPoint{}, err
	}
	copyDir := filepath.Join(dir, DataDir)
	if err := s.Clone(s.Data, copyDir); err != nil {
		return fail(err)
	}
	if err := s.Clean(copyDir); err != nil {
		return fail(fmt.Errorf("the copy's database is not cleanly shut down: %w", err))
	}
	live, err := BuildManifest(s.Data)
	if err != nil {
		return fail(err)
	}
	copied, err := BuildManifest(copyDir)
	if err != nil {
		return fail(err)
	}
	if !slices.Equal(live, copied) {
		return fail(errors.New("the copy doesn't match the data it was taken from"))
	}
	for _, e := range copied {
		p.Size += e.Size
	}
	manifest, err := json.Marshal(copied)
	if err != nil {
		return fail(err)
	}
	if err := writeSynced(filepath.Join(dir, ManifestFile), manifest); err != nil {
		return fail(err)
	}
	if err := s.writeRecord(p); err != nil {
		return fail(err)
	}
	return p, nil
}

func (s Site) writeRecord(p InstallerPoint) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return writeSynced(filepath.Join(s.Root, p.ID, InstallerFile), data)
}

// open reads a complete installer point and checks its copy still matches
// the manifest taken with it.
func (s Site) Open(id string) (InstallerPoint, error) {
	var p InstallerPoint
	if !strings.HasPrefix(id, "installer-") || filepath.Base(id) != id {
		return p, errors.New("not an installer recovery point")
	}
	dir := filepath.Join(s.Root, id)
	data, err := os.ReadFile(filepath.Join(dir, InstallerFile))
	if err != nil {
		return p, ErrIncomplete
	}
	if err := json.Unmarshal(data, &p); err != nil || p.ID != id {
		return p, ErrIncomplete
	}
	var want []Entry
	manifest, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil || json.Unmarshal(manifest, &want) != nil {
		return p, ErrIncomplete
	}
	have, err := BuildManifest(filepath.Join(dir, DataDir))
	if err != nil || !slices.Equal(have, want) {
		return p, errors.New("the recovery point's copy changed")
	}
	return p, nil
}

// restore puts a point's copy back as the live data. The data it replaces is
// kept inside the point, so nothing is deleted.
func (s Site) Restore(id string) error {
	p, err := s.Open(id)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.Root, p.ID)
	if err := s.Clean(filepath.Join(dir, DataDir)); err != nil {
		return err
	}
	replaced := filepath.Join(dir, "replaced-"+NewID(s.now()))
	if err := os.Rename(s.Data, replaced); err != nil {
		return err
	}
	if err := s.Clone(filepath.Join(dir, DataDir), s.Data); err != nil {
		// Leave the data as it was rather than without any.
		if back := os.Rename(replaced, s.Data); back != nil {
			return errors.Join(err, back)
		}
		return err
	}
	restored, err := BuildManifest(s.Data)
	if err != nil {
		return err
	}
	want, err := BuildManifest(filepath.Join(dir, DataDir))
	if err != nil {
		return err
	}
	if !slices.Equal(restored, want) {
		return errors.New("the restored data doesn't match the recovery point")
	}
	return nil
}

// finish marks the point's update as completed and removes all but the
// newest installer points. Incomplete installer points go too.
func (s Site) Finish(id string) (InstallerPoint, error) {
	p, err := s.Open(id)
	if err != nil {
		return p, err
	}
	p.Updated = true
	if err := s.writeRecord(p); err != nil {
		return p, err
	}
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return p, err
	}
	var complete []InstallerPoint
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "installer-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Root, e.Name(), InstallerFile))
		var q InstallerPoint
		if err != nil || json.Unmarshal(data, &q) != nil || q.ID != e.Name() {
			if err := os.RemoveAll(filepath.Join(s.Root, e.Name())); err != nil {
				return p, err
			}
			continue
		}
		complete = append(complete, q)
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].Created.After(complete[j].Created) })
	keep := map[string]bool{p.ID: true}
	for _, q := range complete {
		if len(keep) >= InstallersKept {
			break
		}
		keep[q.ID] = true
	}
	for _, q := range complete {
		if !keep[q.ID] {
			if err := os.RemoveAll(filepath.Join(s.Root, q.ID)); err != nil {
				return p, err
			}
		}
	}
	return p, nil
}

// latest returns the newest installer point whose update completed.
func (s Site) Latest() (InstallerPoint, bool) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return InstallerPoint{}, false
	}
	var best InstallerPoint
	found := false
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "installer-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Root, e.Name(), InstallerFile))
		var q InstallerPoint
		if err != nil || json.Unmarshal(data, &q) != nil || q.ID != e.Name() || !q.Updated {
			continue
		}
		if !found || q.Created.After(best.Created) {
			best, found = q, true
		}
	}
	return best, found
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := FullSync(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return FullSync(d)
}

// NewID names a recovery point by when it was taken, plus a random suffix.
func NewID(now time.Time) string {
	var b [4]byte
	rand.Read(b[:])
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}
