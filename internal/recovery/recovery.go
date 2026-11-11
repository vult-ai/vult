// Package recovery takes and restores recovery points: a copy of the
// installation's data taken while Vult is stopped, so it can be put back if
// an update goes wrong, and an encrypted portable archive that restores the
// Books onto a newly installed Vult on another Mac.
package recovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/vult-ai/vult/ledger"
)

// File names inside a recovery point.
const (
	DataDir      = "data"          // the copy of the data directory
	ManifestFile = "manifest.json" // what the copy contains
	ArchiveFile  = "books.age"     // the encrypted portable archive
	VerifiedFile = "verified.json" // written last, once both parts check out
)

// ErrIncomplete means a recovery point has no verification record: it was
// interrupted and must never be used.
var ErrIncomplete = errors.New("the recovery point is incomplete and can't be used")

// Entry is one file or directory in a data copy's manifest.
type Entry struct {
	Path   string      `json:"path"`
	Mode   fs.FileMode `json:"mode"`
	Size   int64       `json:"size"`
	SHA256 string      `json:"sha256,omitempty"` // every file except database relation files
}

// relationFile matches PostgreSQL table and index files, which are many and
// large; they're covered by the clean-shutdown check instead of hashing.
var relationFile = regexp.MustCompile(`^database/(base/[0-9]+|global)/[0-9]+(_(fsm|vm|init))?(\.[0-9]+)?$`)

// BuildManifest lists dir, hashing every regular file except relation files.
// It refuses links and special files: a data directory has neither.
func BuildManifest(dir string) ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := Entry{Path: rel, Mode: info.Mode()}
		switch {
		case info.IsDir():
		case info.Mode().IsRegular():
			e.Size = info.Size()
			if !relationFile.MatchString(rel) {
				if e.SHA256, err = hashFile(path); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("%s is not a plain file or directory", rel)
		}
		entries = append(entries, e)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, err
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Record is the verification record written last into a recovery point.
type Record struct {
	ID             string                 `json:"id"`
	Created        time.Time              `json:"created"`
	ManifestSHA256 string                 `json:"manifest_sha256"`
	ArchiveSHA256  string                 `json:"archive_sha256"`
	ArchiveSize    int64                  `json:"archive_size"`
	Fingerprint    ledger.FactFingerprint `json:"fingerprint"`
	Archive        ledger.ArchiveSummary  `json:"archive"`
}

// Point is a recovery point being taken.
type Point struct {
	Dir         string // the recovery point's own directory; must not exist
	Data        string // the live data directory, with Vult stopped
	RecoveryKey string // the installation's recovery key, which encrypts the archive
	// Clean reports whether the database in a data directory was shut down
	// cleanly (pg_controldata).
	Clean func(dataDir string) error
	// Fingerprint returns the live database's fact fingerprint.
	Fingerprint func() (ledger.FactFingerprint, error)
	// WriteArchive writes the encrypted portable archive of the live
	// installation.
	WriteArchive func(io.Writer) error
	// Clone copies a directory (copy-on-write where the file system can).
	Clone func(src, dst string) error
	Sync  func(*os.File) error
	Now   func() time.Time
}

// Take takes the recovery point. Vult must be stopped. On any error the
// partial point is removed, so a point on disk without its record was
// interrupted (Remove it; never use it).
func (p Point) Take(id string) (rec Record, err error) {
	if err := os.Mkdir(p.Dir, 0o700); err != nil {
		return Record{}, err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(p.Dir)
		}
	}()
	if err := p.Clean(p.Data); err != nil {
		return Record{}, fmt.Errorf("the database was not shut down cleanly: %w", err)
	}
	copyDir := filepath.Join(p.Dir, DataDir)
	if err := p.Clone(p.Data, copyDir); err != nil {
		return Record{}, err
	}
	if err := p.Clean(copyDir); err != nil {
		return Record{}, fmt.Errorf("the copy's database is not cleanly shut down: %w", err)
	}
	source, err := BuildManifest(p.Data)
	if err != nil {
		return Record{}, err
	}
	copied, err := BuildManifest(copyDir)
	if err != nil {
		return Record{}, err
	}
	if !slices.Equal(source, copied) {
		return Record{}, errors.New("the copy doesn't match the data it was taken from")
	}
	manifest, err := json.Marshal(copied)
	if err != nil {
		return Record{}, err
	}
	if err := p.writeFile(ManifestFile, manifest); err != nil {
		return Record{}, err
	}
	fingerprint, err := p.Fingerprint()
	if err != nil {
		return Record{}, err
	}
	size, sum, err := p.writeArchive()
	if err != nil {
		return Record{}, err
	}
	summary, err := VerifyArchiveFile(filepath.Join(p.Dir, ArchiveFile), p.RecoveryKey)
	if err != nil {
		return Record{}, fmt.Errorf("the archive did not read back: %w", err)
	}
	if err := matches(summary, fingerprint); err != nil {
		return Record{}, err
	}
	manifestSum := sha256.Sum256(manifest)
	rec = Record{ID: id, Created: p.now().UTC(), ManifestSHA256: hex.EncodeToString(manifestSum[:]), ArchiveSHA256: sum, ArchiveSize: size, Fingerprint: fingerprint, Archive: summary}
	data, err := json.Marshal(rec)
	if err != nil {
		return Record{}, err
	}
	if err := p.writeFile(VerifiedFile, data); err != nil {
		return Record{}, err
	}
	return rec, nil
}

func (p Point) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// matches checks the archive holds as many of each fact as the database.
func matches(summary ledger.ArchiveSummary, f ledger.FactFingerprint) error {
	for table, n := range f.Counts {
		if summary.Rows[table] != n {
			return fmt.Errorf("the archive has %d %s, the database %d", summary.Rows[table], table, n)
		}
	}
	return nil
}

func (p Point) syncFile(f *os.File) error {
	if p.Sync != nil {
		return p.Sync(f)
	}
	return f.Sync()
}

func (p Point) writeFile(name string, data []byte) error {
	f, err := os.OpenFile(filepath.Join(p.Dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = p.syncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return p.syncDir()
}

func (p Point) syncDir() error {
	d, err := os.Open(p.Dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return p.syncFile(d)
}

func (p Point) writeArchive() (int64, string, error) {
	f, err := os.OpenFile(filepath.Join(p.Dir, ArchiveFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	counter := &countWriter{}
	err = p.WriteArchive(io.MultiWriter(f, h, counter))
	if err == nil {
		err = p.syncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, "", err
	}
	return counter.n, hex.EncodeToString(h.Sum(nil)), p.syncDir()
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(b []byte) (int, error) { c.n += int64(len(b)); return len(b), nil }

// Decrypt opens an encrypted archive with the recovery key.
func Decrypt(src io.Reader, recoveryKey string) (io.Reader, error) {
	identity, err := age.NewScryptIdentity(recoveryKey)
	if err != nil {
		return nil, err
	}
	identity.SetMaxWorkFactor(18)
	plain, err := age.Decrypt(src, identity)
	if err != nil {
		return nil, errors.New("the recovery key doesn't open this archive")
	}
	return plain, nil
}

// VerifyArchiveFile decrypts the archive and reads all of it back.
func VerifyArchiveFile(path, recoveryKey string) (ledger.ArchiveSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return ledger.ArchiveSummary{}, err
	}
	defer f.Close()
	plain, err := Decrypt(f, recoveryKey)
	if err != nil {
		return ledger.ArchiveSummary{}, err
	}
	summary, err := ledger.VerifyArchive(plain)
	if err != nil {
		return summary, err
	}
	// age authenticates each chunk; reading to the end checks the last one.
	if n, err := io.Copy(io.Discard, plain); err != nil || n != 0 {
		return summary, errors.New("the archive has trailing data")
	}
	return summary, nil
}

// Open reads a recovery point's record, refusing a point without one.
func Open(dir string) (Record, error) {
	data, err := os.ReadFile(filepath.Join(dir, VerifiedFile))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrIncomplete
	}
	if err != nil {
		return Record{}, err
	}
	var rec Record
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil || rec.ID == "" || !strings.EqualFold(rec.ID, filepath.Base(dir)) {
		return Record{}, errors.New("the recovery point's record is not readable")
	}
	return rec, nil
}
