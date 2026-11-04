package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestNativeSocketConnectionPreservesSpacesAndCredentials(t *testing.T) {
	for _, database := range []string{"postgres", "vult"} {
		password := "example+password/@:with&reserved=characters"
		config, e := nativePoolConfig("vult_owner", password, database)
		if e != nil {
			t.Fatal(e)
		}
		c := config.ConnConfig
		if c.Host != filepath.Join(local.DataPath, "postgres") || c.Port != 5432 || c.Database != database || c.User != "vult_owner" || c.Password != password {
			t.Fatal("native connection target or credential encoding changed")
		}
		if c.TLSConfig != nil || len(c.Fallbacks) != 0 {
			t.Fatal("private socket connection acquired a network fallback")
		}
	}
}

func writeFixture(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, data, mode); e != nil {
		t.Fatal(e)
	}
}
func fixtureBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func checksum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func programFixture(t *testing.T) removalPaths {
	t.Helper()
	dir := t.TempDir()
	p := removalPaths{Code: filepath.Join(dir, "program"), Plist: filepath.Join(dir, "startup", "ledger.plist"), Shortcut: filepath.Join(dir, "bin", "vult"), Retained: filepath.Join(dir, "storage")}
	m := removalManifest{Format: "vult-program-files-v1", Version: Version, Files: map[string]string{}}
	for _, name := range []string{"vult", "LICENSE", "postgres/bin/pg_ctl", "postgres/share/config.sample"} {
		data := []byte("synthetic program: " + name)
		writeFixture(t, filepath.Join(p.Code, name), data, 0755)
		m.Files[name] = checksum(data)
	}
	plist := []byte("synthetic startup definition")
	writeFixture(t, p.Plist, plist, 0644)
	m.LaunchDaemonSHA256 = checksum(plist)
	b, e := json.Marshal(m)
	if e != nil {
		t.Fatal(e)
	}
	writeFixture(t, filepath.Join(p.Code, programManifest), b, 0644)
	if e = os.MkdirAll(filepath.Dir(p.Shortcut), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(filepath.Join(p.Code, "vult"), p.Shortcut); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"installation", "data/journal", "data/configuration.json", "data/database/PG_VERSION", "backups/books.age"} {
		writeFixture(t, filepath.Join(p.Retained, name), []byte("retained: "+name), 0600)
	}
	return p
}
func retainedBytes(t *testing.T, p removalPaths) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	e := filepath.WalkDir(p.Retained, func(path string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !entry.IsDir() {
			out[path] = fixtureBytes(t, path)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func assertRetained(t *testing.T, p removalPaths, before map[string][]byte) {
	t.Helper()
	after := retainedBytes(t, p)
	if len(before) != len(after) {
		t.Fatal("retained file set changed")
	}
	for path, want := range before {
		if !bytes.Equal(after[path], want) {
			t.Fatal("retained file changed", filepath.Base(path))
		}
	}
}

func TestSoftwareRemovalKeepsStorageKeysBackupsAndIdentity(t *testing.T) {
	p := programFixture(t)
	before := retainedBytes(t, p)
	stopped := false
	r, e := removeProgram(p, uint32(os.Geteuid()), func() error { stopped = true; return nil })
	if e != nil || !stopped || r.Removed != 5 || len(r.Kept) != 0 {
		t.Fatal("software removal failed", r, e)
	}
	for _, path := range []string{p.Code, p.Plist, p.Shortcut} {
		if _, e := os.Lstat(path); !os.IsNotExist(e) {
			t.Fatal("program path remains", path, e)
		}
	}
	assertRetained(t, p, before)
}

func TestSoftwareRemovalPreservesModifiedAndUnrecognizedFiles(t *testing.T) {
	p := programFixture(t)
	before := retainedBytes(t, p)
	modified := filepath.Join(p.Code, "postgres/share/config.sample")
	want := []byte("locally modified program file")
	if e := os.WriteFile(modified, want, 0755); e != nil {
		t.Fatal(e)
	}
	unknown := filepath.Join(p.Code, "extra-backup.age")
	writeFixture(t, unknown, []byte("additional backup"), 0600)
	hardlink := filepath.Join(p.Code, "LICENSE")
	outside := filepath.Join(filepath.Dir(p.Code), "retained-license")
	if e := os.Link(hardlink, outside); e != nil {
		t.Fatal(e)
	}
	r, e := removeProgram(p, uint32(os.Geteuid()), func() error { return nil })
	if e != nil || len(r.Kept) == 0 {
		t.Fatal("modified files were not retained", e)
	}
	if !bytes.Equal(fixtureBytes(t, modified), want) || string(fixtureBytes(t, unknown)) != "additional backup" || !bytes.Equal(fixtureBytes(t, hardlink), fixtureBytes(t, outside)) {
		t.Fatal("unrecognized or modified file changed")
	}
	assertRetained(t, p, before)
}

func TestStopFailureDoesNotRemoveProgramFiles(t *testing.T) {
	p := programFixture(t)
	before := fixtureBytes(t, filepath.Join(p.Code, "vult"))
	if _, e := removeProgram(p, uint32(os.Geteuid()), func() error { return errors.New("stop failed") }); e == nil {
		t.Fatal("stop failure was ignored")
	}
	if !bytes.Equal(fixtureBytes(t, filepath.Join(p.Code, "vult")), before) {
		t.Fatal("program changed before stop completed")
	}
	fixtureBytes(t, p.Plist)
	fixtureBytes(t, filepath.Join(p.Code, programManifest))
}

func TestUnsafeInventoryCannotStartRemoval(t *testing.T) {
	for _, kind := range []string{"traversal", "unrecognized", "bad-checksum", "linked-parent", "retained-storage"} {
		t.Run(kind, func(t *testing.T) {
			p := programFixture(t)
			var m removalManifest
			if e := json.Unmarshal(fixtureBytes(t, filepath.Join(p.Code, programManifest)), &m); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "traversal":
				m.Files["postgres/../../storage/data/journal"] = checksum([]byte("ignored"))
			case "unrecognized":
				m.Files["data/journal"] = checksum([]byte("ignored"))
			case "bad-checksum":
				m.Files["vult"] = "invalid"
			case "linked-parent":
				if e := os.Rename(filepath.Join(p.Code, "postgres/share"), filepath.Join(p.Retained, "share")); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(filepath.Join(p.Retained, "share"), filepath.Join(p.Code, "postgres/share")); e != nil {
					t.Fatal(e)
				}
			}
			b, _ := json.Marshal(m)
			if e := os.WriteFile(filepath.Join(p.Code, programManifest), b, 0644); e != nil {
				t.Fatal(e)
			}
			before := retainedBytes(t, p)
			if kind == "retained-storage" {
				p.Code = p.Retained
			}
			stopped := false
			if _, e := removeProgram(p, uint32(os.Geteuid()), func() error { stopped = true; return nil }); e == nil || stopped {
				t.Fatal("unsafe inventory started removal")
			}
			assertRetained(t, p, before)
		})
	}
}

func TestReinstallKeepsConfigurationAndReadinessCheckpoint(t *testing.T) {
	dir := t.TempDir()
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	c, e := ensureConfiguration(dir, uid, gid)
	if e != nil || c.State != "new" {
		t.Fatal("fresh configuration failed", e)
	}
	path := filepath.Join(dir, "configuration.json")
	if e = markConfigurationReady(path, c); e != nil {
		t.Fatal(e)
	}
	before := fixtureBytes(t, path)
	after, e := ensureConfiguration(dir, uid, gid)
	if e != nil || after.State != "ready" || after.AdminPassword != c.AdminPassword || after.RuntimePassword != c.RuntimePassword || after.RecoveryKey != c.RecoveryKey || !bytes.Equal(fixtureBytes(t, path), before) {
		t.Fatal("reinstall changed credentials or readiness", e)
	}
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	writeFixture(t, filepath.Join(dir, "database/PG_VERSION"), []byte("18\n"), 0600)
	if _, e := ensureConfiguration(dir, uid, gid); e == nil {
		t.Fatal("missing configuration silently regenerated credentials")
	}
}

func TestMissingOrUnexpectedStorageRequiresRecovery(t *testing.T) {
	for _, kind := range []string{"missing-ready", "empty-ready", "nonempty-new", "unsupported-major", "linked-directory", "linked-version", "unknown-state"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "database")
			state := "ready"
			switch kind {
			case "empty-ready":
				if e := os.Mkdir(path, 0700); e != nil {
					t.Fatal(e)
				}
			case "nonempty-new":
				state = "new"
				writeFixture(t, filepath.Join(path, "unrecognized"), []byte("keep"), 0600)
			case "unsupported-major":
				writeFixture(t, filepath.Join(path, "PG_VERSION"), []byte("17\n"), 0600)
			case "linked-directory", "linked-version":
				other := filepath.Join(root, "retained")
				writeFixture(t, filepath.Join(other, "PG_VERSION"), []byte("18\n"), 0600)
				if kind == "linked-directory" {
					if e := os.Symlink(other, path); e != nil {
						t.Fatal(e)
					}
				} else {
					if e := os.Mkdir(path, 0700); e != nil {
						t.Fatal(e)
					}
					if e := os.Symlink(filepath.Join(other, "PG_VERSION"), filepath.Join(path, "PG_VERSION")); e != nil {
						t.Fatal(e)
					}
				}
			case "unknown-state":
				state = ""
			}
			if init, e := needsDatabaseInitialization(path, state); e == nil || init {
				t.Fatal("unsafe storage allowed initialization")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "fresh")
	if init, e := needsDatabaseInitialization(path, "new"); e != nil || !init {
		t.Fatal("fresh storage rejected", e)
	}
	writeFixture(t, filepath.Join(path, "PG_VERSION"), []byte("18\n"), 0600)
	if init, e := needsDatabaseInitialization(path, "ready"); e != nil || init {
		t.Fatal("existing cluster was not reused", e)
	}
	for _, kind := range []string{"ledger database", "runtime role"} {
		if requireExisting("ready", kind, false) == nil || requireExisting("ready", kind, true) != nil || requireExisting("new", kind, false) != nil {
			t.Fatal("catalog recovery rule failed", kind)
		}
	}
}

// These filesystem fixtures model software replacement. The ledger is a real
// isolated PostgreSQL database; launchd, system-account creation and native
// administrator dialogs still need installed macOS qualification.
func TestSoftwareReinstallRetainsExactJournalAndCanPostAgain(t *testing.T) {
	db := testutil.New(t)
	book, scope := db.Book(t, "Example continuity")
	debit := db.Account(t, scope, "1000", "asset")
	credit := db.Account(t, scope, "3000", "equity")
	request := ledger.PostJournal{RequestKey: "opening", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example funding", Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: "1250"}, {AccountID: credit.ID, Side: "credit", AmountMinor: "1250"}}}
	result := db.Call(t, scope, "post_journal", request)
	before, e := db.Owner.Snapshot(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	p := programFixture(t)
	if _, e = removeProgram(p, uint32(os.Geteuid()), func() error { return nil }); e != nil {
		t.Fatal(e)
	}
	writeFixture(t, filepath.Join(p.Code, "vult"), []byte("replacement program"), 0755)
	if e = verifyOrInitializeSchema(db.Context, db.Admin, db.Role, "ready"); e != nil {
		t.Fatal(e)
	}
	after, e := db.Owner.Snapshot(db.Context)
	if e != nil {
		t.Fatal(e)
	}
	for name, want := range before.Tables {
		if !bytes.Equal(want, after.Tables[name]) {
			t.Fatal("software reinstall changed a recorded row", name)
		}
	}
	if retry := db.Call(t, scope, "post_journal", request); !bytes.Equal(retry, result) {
		t.Fatal("reinstall changed the retry receipt")
	}
	request.RequestKey = "next"
	db.Call(t, scope, "post_journal", request)
	if verification, e := db.Owner.Verify(db.Context, book.ID); e != nil || verification.Entries != 2 {
		t.Fatal("ledger could not continue after reinstall", e)
	}
}

func TestInitializedSchemaIsNeverRecreatedOnStartup(t *testing.T) {
	db := testutil.New(t)
	if _, e := db.Admin.Exec(db.Context, "DROP TABLE public.schema_receipts"); e != nil {
		t.Fatal(e)
	}
	if e := verifyOrInitializeSchema(db.Context, db.Admin, db.Role, "ready"); e == nil {
		t.Fatal("missing schema was accepted")
	}
	var exists bool
	if e := db.Admin.QueryRow(db.Context, "SELECT to_regclass('public.schema_receipts') IS NOT NULL").Scan(&exists); e != nil || exists {
		t.Fatal("startup recreated the missing schema", e)
	}
}
