package recovery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// privateDir is a test directory only its owner can open, as document
// storage requires.
func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

const testKey = "example-recovery-key-0123456789abcdefghijklmnop"

// installation is a synthetic database with a Book and two entries, and a
// fake data directory standing in for the live one.
type installation struct {
	db    *testutil.Database
	vault *documents.Vault
	data  string
}

func newInstallation(t *testing.T) *installation {
	t.Helper()
	db := testutil.New(t)
	_, s := db.Book(t, "Example recovered Book")
	bank := db.Account(t, s, "1000", "asset")
	equity := db.Account(t, s, "3000", "equity")
	for _, key := range []string{"opening", "second"} {
		db.Call(t, s, "post_journal", ledger.PostJournal{RequestKey: key, EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example funding",
			Lines: []ledger.PostLine{{AccountID: bank.ID, Side: "debit", AmountMinor: "1250"}, {AccountID: equity.ID, Side: "credit", AmountMinor: "1250"}}})
	}
	base := t.TempDir()
	for _, dir := range []string{"originals", "keys"} {
		if err := os.Mkdir(filepath.Join(base, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	disk, err := documents.OpenDisk(filepath.Join(base, "originals"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { disk.Close() })
	keys, err := documents.OpenKeyDirectory(filepath.Join(base, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	if err := keys.Provision(db.Context, s.TenantID); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(base, "data")
	for file, body := range map[string]string{
		"configuration.json":                       `{"example":true}`,
		"database/PG_VERSION":                      "18\n",
		"database/global/1262":                     "relation",
		"database/base/5/16384":                    "relation",
		"database/base/5/16384_fsm":                "relation",
		"database/pg_wal/000000010000000000000001": "wal",
	} {
		path := filepath.Join(data, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &installation{db: db, vault: &documents.Vault{Storage: disk, Keys: keys}, data: data}
}

func (in *installation) point(t *testing.T) Point {
	return Point{
		Dir:         filepath.Join(t.TempDir(), "20261110T120000Z-0a1b2c3d"),
		Data:        in.data,
		RecoveryKey: testKey,
		Clean:       func(string) error { return nil },
		Fingerprint: func() (ledger.FactFingerprint, error) { return ledger.Fingerprint(in.db.Context, in.db.Admin) },
		WriteArchive: func(w io.Writer) error {
			r, err := age.NewScryptRecipient(testKey)
			if err != nil {
				return err
			}
			r.SetWorkFactor(10)
			c, err := age.Encrypt(w, r)
			if err != nil {
				return err
			}
			if err := in.db.Owner.WriteArchive(in.db.Context, in.vault, c, ""); err != nil {
				return err
			}
			return c.Close()
		},
		Clone: Clone,
		Sync:  FullSync,
	}
}

func TestTakeARecoveryPoint(t *testing.T) {
	in := newInstallation(t)
	p := in.point(t)
	rec, err := p.Take(filepath.Base(p.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Fingerprint.Counts["entries"] != 2 || rec.Archive.Rows["entries"] != 2 || rec.ArchiveSize == 0 {
		t.Fatalf("record %+v", rec)
	}
	opened, err := Open(p.Dir)
	if err != nil || opened.ArchiveSHA256 != rec.ArchiveSHA256 {
		t.Fatalf("Open: %+v, %v", opened, err)
	}
	// The copy is independent of the live data.
	os.WriteFile(filepath.Join(in.data, "configuration.json"), []byte("changed"), 0o600)
	if b, _ := os.ReadFile(filepath.Join(p.Dir, DataDir, "configuration.json")); string(b) != `{"example":true}` {
		t.Fatal("the copy changed with the live data")
	}
	// Relation files are listed but not hashed; everything else is hashed.
	manifest, err := BuildManifest(filepath.Join(p.Dir, DataDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range manifest {
		relation := strings.Contains(e.Path, "/base/5/") || strings.HasPrefix(e.Path, "database/global/")
		if e.Mode.IsRegular() && (e.SHA256 == "") != relation {
			t.Errorf("%s: hash %q", e.Path, e.SHA256)
		}
	}
	for _, mode := range []os.FileMode{0o700} {
		info, err := os.Stat(p.Dir)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("point directory mode %v, %v", info.Mode(), err)
		}
	}
	// The archive restores to the same facts on an empty installation.
	f, err := os.Open(filepath.Join(p.Dir, ArchiveFile))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	plain, err := Decrypt(f, testKey)
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	targetKeys, err := documents.OpenKeyDirectory(privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	provision := func(ctx context.Context, tenant string) error { return targetKeys.Provision(ctx, tenant) }
	if err := target.Owner.RestoreArchive(target.Context, &documents.Vault{Storage: disk, Keys: targetKeys}, plain, provision); err != nil {
		t.Fatal(err)
	}
	restored, err := ledger.Fingerprint(target.Context, target.Admin)
	if err != nil || restored.SHA256 != rec.Fingerprint.SHA256 {
		t.Fatalf("restored facts differ: %v", err)
	}
}

// Any failure removes the partial point, so nothing incomplete is left to
// be used by mistake.
func TestAFailedRecoveryPointLeavesNothing(t *testing.T) {
	in := newInstallation(t)
	for name, change := range map[string]func(*Point){
		"not shut down cleanly": func(p *Point) { p.Clean = func(string) error { return errors.New("in production") } },
		"copy fails":            func(p *Point) { p.Clone = func(string, string) error { return errors.New("no space") } },
		"copy incomplete": func(p *Point) {
			p.Clone = func(src, dst string) error {
				if err := Clone(src, dst); err != nil {
					return err
				}
				return os.Remove(filepath.Join(dst, "configuration.json"))
			}
		},
		"live data not cleanly shut down": func(p *Point) {
			live := p.Data
			p.Clean = func(dir string) error {
				if dir == live {
					return errors.New("in production")
				}
				return nil
			}
		},
		"copy not cleanly shut down": func(p *Point) {
			live := p.Data
			p.Clean = func(dir string) error {
				if dir != live {
					return errors.New("in recovery")
				}
				return nil
			}
		},
		"archive fails": func(p *Point) { p.WriteArchive = func(io.Writer) error { return errors.New("stopped") } },
		// The archive is written with the test key; the point expects another.
		"wrong key": func(p *Point) { p.RecoveryKey = "another-recovery-key-0123456789abcdefghijkl" },
		"counts differ": func(p *Point) {
			p.Fingerprint = func() (ledger.FactFingerprint, error) {
				f, err := ledger.Fingerprint(in.db.Context, in.db.Admin)
				f.Counts["entries"]++
				return f, err
			}
		},
		"damaged archive": func(p *Point) {
			write := p.WriteArchive
			p.WriteArchive = func(w io.Writer) error {
				var buf bytes.Buffer
				if err := write(&buf); err != nil {
					return err
				}
				b := buf.Bytes()
				b[len(b)-20] ^= 1
				_, err := w.Write(b)
				return err
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := in.point(t)
			change(&p)
			if _, err := p.Take(filepath.Base(p.Dir)); err == nil {
				t.Fatal("no error")
			}
			if _, err := os.Lstat(p.Dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("the partial point was left behind")
			}
		})
	}
}

func TestOpenRefusesAnIncompletePoint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "20261110T120000Z-0a1b2c3d")
	os.Mkdir(dir, 0o700)
	if _, err := Open(dir); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Open: %v", err)
	}
	os.WriteFile(filepath.Join(dir, VerifiedFile), []byte(`{"id":"another"}`), 0o600)
	if _, err := Open(dir); err == nil {
		t.Fatal("a record for another point was accepted")
	}
}

func TestManifestRefusesLinks(t *testing.T) {
	dir := t.TempDir()
	os.Symlink("/etc", filepath.Join(dir, "link"))
	if _, err := BuildManifest(dir); err == nil {
		t.Fatal("a link was accepted")
	}
}
