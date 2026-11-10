package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
		if c.Host != local.PostgresSocketPath || c.Port != 5432 || c.Database != database || c.User != "vult_owner" || c.Password != password {
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
	p := removalPaths{Code: filepath.Join(dir, "program"), Startup: filepath.Join(dir, "startup", "ledger.plist"), Shortcut: filepath.Join(dir, "bin", "vult"), Retained: filepath.Join(dir, "storage")}
	m := removalManifest{Format: "vult-program-files-v1", Version: Version, Files: map[string]string{}}
	for _, name := range []string{"vult", "LICENSE", "postgres/bin/pg_ctl", "postgres/share/config.sample",
		"Vult Browser Setup.app/Contents/Info.plist", "Vult Browser Setup.app/Contents/MacOS/browser-setup",
		"Vult Browser Setup.app/Contents/_CodeSignature/CodeResources"} {
		data := []byte("synthetic program: " + name)
		writeFixture(t, filepath.Join(p.Code, name), data, 0755)
		m.Files[name] = checksum(data)
	}
	plist := []byte("synthetic startup definition")
	writeFixture(t, p.Startup, plist, 0644)
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
	for _, name := range []string{"installation", "browser-identity.json", "data/journal", "data/configuration.json", "data/database/PG_VERSION", "data/originals/example-object", "data/document-keys/example-key", "backups/books.age"} {
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
	if e != nil || !stopped || r.Removed != 8 || len(r.Kept) != 0 {
		t.Fatal("software removal failed", r, e)
	}
	for _, path := range []string{p.Code, p.Startup, p.Shortcut} {
		if _, e := os.Lstat(path); !os.IsNotExist(e) {
			t.Fatal("program path remains", path, e)
		}
	}
	assertRetained(t, p, before)
}

func TestSoftwareRemovalKeepsBrowserIdentityAndRemovesOnlyKnownStartup(t *testing.T) {
	p := programFixture(t)
	var m removalManifest
	if err := json.Unmarshal(fixtureBytes(t, filepath.Join(p.Code, programManifest)), &m); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(filepath.Dir(p.Startup), "ai.vult.browser.plist")
	startup := []byte("synthetic browser startup")
	writeFixture(t, plist, startup, 0644)
	m.BrowserDaemonSHA256 = checksum(startup)
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Code, programManifest), data, 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"browser-installation", "data/browser.json", "data/browser-state.json", "browser/key.pem", "browser/auth.dsn"} {
		writeFixture(t, filepath.Join(p.Retained, name), []byte("retained synthetic browser identity"), 0400)
	}
	before := retainedBytes(t, p)
	if _, err := removeProgram(p, uint32(os.Geteuid()), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Fatal("inventoried browser startup remains", err)
	}
	assertRetained(t, p, before)
}

func TestSoftwareRemovalRejectsChangedBrowserStartupBeforeStopping(t *testing.T) {
	for _, variant := range []string{"unchanged", "modified", "hardlink", "symlink", "unrecognized"} {
		t.Run(variant, func(t *testing.T) {
			p := programFixture(t)
			path := filepath.Join(filepath.Dir(p.Startup), "ai.vult.browser.plist")
			data := []byte("synthetic browser startup")
			writeFixture(t, path, data, 0644)
			m := removalManifest{BrowserDaemonSHA256: checksum(data)}
			switch variant {
			case "modified":
				if err := os.WriteFile(path, []byte("another startup definition"), 0644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".copy"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".copy"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".copy", path); err != nil {
					t.Fatal(err)
				}
			case "unrecognized":
				m.BrowserDaemonSHA256 = ""
			}
			stopped := false
			if err := verifyBrowserStartup(p, m, uint32(os.Geteuid())); err == nil {
				stopped = true
			}
			if stopped != (variant == "unchanged") {
				t.Fatal("changed or unrecognized browser startup passed the stop preflight")
			}
		})
	}
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
	fixtureBytes(t, p.Startup)
	fixtureBytes(t, filepath.Join(p.Code, programManifest))
}

func TestSoftwareRemovalPreservesModifiedSetupApplication(t *testing.T) {
	p := programFixture(t)
	before := retainedBytes(t, p)
	executable := filepath.Join(p.Code, "Vult Browser Setup.app/Contents/MacOS/browser-setup")
	modified := []byte("modified synthetic setup executable")
	if err := os.WriteFile(executable, modified, 0755); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(p.Code, "Vult Browser Setup.app/Contents/custom.json")
	writeFixture(t, unknown, []byte("unrecognized synthetic settings"), 0600)
	if _, err := removeProgram(p, uint32(os.Geteuid()), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fixtureBytes(t, executable), modified) || string(fixtureBytes(t, unknown)) != "unrecognized synthetic settings" {
		t.Fatal("modified or unrecognized application file changed")
	}
	assertRetained(t, p, before)
}

func TestUnsafeInventoryCannotStartRemoval(t *testing.T) {
	for _, kind := range []string{"traversal", "unrecognized", "unrecognized-app", "bad-checksum", "linked-parent", "linked-app-parent", "retained-storage"} {
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
			case "unrecognized-app":
				m.Files["Vult Browser Setup.app/Contents/Helpers/unknown"] = checksum([]byte("ignored"))
			case "bad-checksum":
				m.Files["vult"] = "invalid"
			case "linked-parent":
				if e := os.Rename(filepath.Join(p.Code, "postgres/share"), filepath.Join(p.Retained, "share")); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(filepath.Join(p.Retained, "share"), filepath.Join(p.Code, "postgres/share")); e != nil {
					t.Fatal(e)
				}
			case "linked-app-parent":
				source := filepath.Join(p.Code, "Vult Browser Setup.app/Contents/MacOS")
				destination := filepath.Join(p.Retained, "browser-executable")
				if err := os.Rename(source, destination); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(destination, source); err != nil {
					t.Fatal(err)
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

func releasedSchema(ctx context.Context, db *pgxpool.Pool, role string, names []string) error {
	releasedHashes := map[string]string{
		"0001_ledger.sql":        "1f2a80022713d3b913555391319685f3042916b437260912a7e1c8743f53e451",
		"0002_server_access.sql": "733594d4b3559f30f8f97ed2ca3595b8f8cf6d2adf4b2cbd0213a32da5f4814f",
		"0003_documents.sql":     "1427a42e8c4d90d0fa5f3de6d8ce56c9467e243a336f86758e5d98dd8704fbe6",
	}
	if _, e := db.Exec(ctx, "CREATE TABLE public.schema_receipts(filename text PRIMARY KEY,sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),applied_at timestamptz NOT NULL DEFAULT clock_timestamp());REVOKE ALL ON public.schema_receipts FROM PUBLIC"); e != nil {
		return e
	}
	for _, name := range names {
		b, e := os.ReadFile("../../ledger/migrations/" + name)
		if e != nil {
			return e
		}
		if checksum(b) != releasedHashes[name] {
			return errors.New("released migration changed")
		}
		if _, e = db.Exec(ctx, string(b)); e != nil {
			return e
		}
		if _, e = db.Exec(ctx, "INSERT INTO public.schema_receipts(filename,sha256) VALUES($1,$2)", name, checksum(b)); e != nil {
			return e
		}
	}
	r := pgx.Identifier{role}.Sanitize()
	statements := []string{
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
		"GRANT USAGE ON SCHEMA public TO " + r,
		"GRANT SELECT ON public.books,public.accounts,public.account_revisions,public.entries,public.lines,public.requests,public.closed_periods,public.connections,public.schema_receipts TO " + r,
		"GRANT INSERT ON public.accounts,public.account_revisions,public.entries,public.lines,public.requests TO " + r,
		"GRANT UPDATE(write_epoch) ON public.books TO " + r,
		"GRANT UPDATE(id) ON public.connections TO " + r,
		"GRANT EXECUTE ON FUNCTION public.ledger_context(uuid,uuid),public.ledger_allowed(uuid,uuid,boolean),public.ledger_lock(uuid,uuid),public.ledger_import_owner() TO " + r,
	}
	if len(names) >= 2 {
		statements = append(statements, "GRANT USAGE ON SCHEMA access TO "+r,
			"GRANT EXECUTE ON FUNCTION access.owner_allowed(uuid),access.owner_write_allowed(uuid),access.delegation_allowed(uuid,boolean),access.require_agent(text,text,uuid,uuid,uuid,uuid,text) TO "+r)
	}
	if len(names) >= 3 {
		statements = append(statements,
			"GRANT SELECT,INSERT ON public.documents,public.document_uploads TO "+r,
			"GRANT UPDATE(state,storage_id,key_id,ready_at,expires_at) ON public.documents TO "+r,
			"GRANT UPDATE(used_at) ON public.documents TO "+r,
			"GRANT SELECT,INSERT ON public.source_citations TO "+r,
			"GRANT SELECT,INSERT ON public.evidence_links TO "+r,
			"GRANT EXECUTE ON FUNCTION public.document_use(uuid,uuid,uuid) TO "+r,
			"GRANT EXECUTE ON FUNCTION public.ledger_validate_balances(uuid,uuid,uuid) TO "+r)
	}
	for _, statement := range statements {
		if _, e := db.Exec(ctx, statement); e != nil {
			return e
		}
	}
	return nil
}

func TestNativeStartupUpgradesReleasedLedgerWithoutChangingRecords(t *testing.T) {
	for _, baseline := range []struct {
		name  string
		files []string
	}{
		{"v0.1.0", []string{"0001_ledger.sql"}},
		{"v0.2.0", []string{"0001_ledger.sql", "0002_server_access.sql"}},
		{"v0.3.0", []string{"0001_ledger.sql", "0002_server_access.sql", "0003_documents.sql"}},
	} {
		t.Run(baseline.name, func(t *testing.T) {
			db := testutil.NewWithInitializer(t, func(ctx context.Context, pool *pgxpool.Pool, role string) error {
				return releasedSchema(ctx, pool, role, baseline.files)
			})
			book, scope := db.Book(t, "Example retained Book")
			a, b := db.ReleasedAccount(t, scope, "1000", "asset"), db.ReleasedAccount(t, scope, "3000", "equity")
			q := ledger.PostJournal{RequestKey: "retained", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example funding", Lines: []ledger.PostLine{{AccountID: a.ID, Side: "debit", AmountMinor: "1250"}, {AccountID: b.ID, Side: "credit", AmountMinor: "1250"}}}
			result := db.ReleasedPost(t, scope, q)
			if e := db.ClosePeriod(db.Context, book.ID, "2026-01-01", "2026-01-31"); e != nil {
				t.Fatal(e)
			}
			before := db.ReleasedSnapshot(t)
			var e error
			if e = ledger.Ready(db.Context, db.Admin); e == nil {
				t.Fatal("test did not start with an older installation")
			}
			var code string
			for i := 0; i < 2; i++ {
				if e = verifyOrInitializeSchema(db.Context, db.Admin, db.Role, "ready"); e != nil {
					t.Fatal("initialized ledger could not upgrade", e)
				}
				if e = ledger.Ready(db.Context, db.Admin); e != nil {
					t.Fatal("upgraded schema is incomplete", e)
				}
				after, e := db.Owner.Snapshot(db.Context)
				if e != nil {
					t.Fatal("upgraded snapshot unavailable", e)
				}
				fixed := testutil.AssertReleasedSnapshotUpgrade(t, before, after, book)
				if i > 0 && fixed != code {
					t.Fatal("a second startup changed the Book's fixed address")
				}
				code = fixed
				if retry := db.Call(t, scope, "post_journal", q); !bytes.Equal(retry, result) {
					t.Fatal("upgrade changed the existing actor or retry outcome")
				}
			}
			fresh := testutil.New(t)
			if schemaDefinition(t, db) != schemaDefinition(t, fresh) {
				t.Fatal("upgraded schema differs from a fresh installation")
			}
			q.RequestKey, q.AccountingDate = "continue", "2026-02-01"
			db.Call(t, scope, "post_journal", q)
			if v, e := db.Owner.Verify(db.Context, book.ID); e != nil || v.Entries != 2 {
				t.Fatal("upgraded ledger could not continue", e)
			}
		})
	}
}

func schemaDefinition(t *testing.T, db *testutil.Database) string {
	t.Helper()
	const query = `WITH objects AS (
 SELECT 'relation' AS kind, n.nspname||'.'||c.relname AS name,
 jsonb_build_array(c.relkind,c.relrowsecurity,c.relforcerowsecurity,c.relacl)::text AS definition
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname IN ('public','access') AND c.relkind IN ('r','S')
 UNION ALL
 SELECT 'column',n.nspname||'.'||c.relname||'.'||a.attname,
 jsonb_build_array(a.attnum,format_type(a.atttypid,a.atttypmod),a.attnotnull,a.attidentity,
 a.attgenerated,a.attacl,pg_get_expr(d.adbin,d.adrelid))::text
 FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid
 JOIN pg_namespace n ON n.oid=c.relnamespace
 LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum
 WHERE n.nspname IN ('public','access') AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped
 UNION ALL
 SELECT 'constraint',n.nspname||'.'||c.relname||'.'||k.conname,
 jsonb_build_array(pg_get_constraintdef(k.oid),k.convalidated)::text
 FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid
 JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('public','access')
 UNION ALL
 SELECT 'index',n.nspname||'.'||c.relname,pg_get_indexdef(c.oid)
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname IN ('public','access') AND c.relkind='i'
 UNION ALL
 SELECT 'function',n.nspname||'.'||p.proname||'('||pg_get_function_identity_arguments(p.oid)||')',
 jsonb_build_array(pg_get_functiondef(p.oid),p.proacl)::text
 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
 WHERE n.nspname IN ('public','access')
 UNION ALL
 SELECT 'policy',n.nspname||'.'||c.relname||'.'||p.polname,
 jsonb_build_array(p.polcmd,p.polpermissive,p.polroles,
 pg_get_expr(p.polqual,p.polrelid),pg_get_expr(p.polwithcheck,p.polrelid))::text
 FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid
 JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname IN ('public','access')
 UNION ALL
 SELECT 'trigger',n.nspname||'.'||c.relname||'.'||g.tgname,
 jsonb_build_array(pg_get_triggerdef(g.oid),g.tgenabled)::text
 FROM pg_trigger g JOIN pg_class c ON c.oid=g.tgrelid
 JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname IN ('public','access') AND NOT g.tgisinternal
 UNION ALL
 SELECT 'namespace',nspname,coalesce(nspacl::text,'')
 FROM pg_namespace WHERE nspname IN ('public','access')
 ) SELECT jsonb_agg(jsonb_build_array(kind,name,definition) ORDER BY kind,name)::text FROM objects`
	var definition string
	if e := db.Admin.QueryRow(db.Context, query).Scan(&definition); e != nil {
		t.Fatal("schema definition unavailable", e)
	}
	return strings.ReplaceAll(definition, db.Role, "runtime_role")
}

func TestNativeUpgradeRejectsUnrecognizedReceipts(t *testing.T) {
	for _, variation := range []string{"empty", "modified", "gap", "unknown"} {
		t.Run(variation, func(t *testing.T) {
			db := testutil.New(t)
			statement := map[string]string{
				"empty":    "DELETE FROM public.schema_receipts",
				"modified": "UPDATE public.schema_receipts SET sha256=repeat('0',64) WHERE filename='0001_ledger.sql'",
				"gap":      "DELETE FROM public.schema_receipts WHERE filename='0001_ledger.sql'",
				"unknown":  "INSERT INTO public.schema_receipts(filename,sha256) VALUES('9999_unknown.sql',repeat('0',64))",
			}[variation]
			if _, e := db.Admin.Exec(db.Context, statement); e != nil {
				t.Fatal(e)
			}
			var before, after string
			snapshot := "SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY filename)::text,'[]') FROM public.schema_receipts r"
			if db.Admin.QueryRow(db.Context, snapshot).Scan(&before) != nil {
				t.Fatal("receipt snapshot unavailable")
			}
			if verifyOrInitializeSchema(db.Context, db.Admin, db.Role, "ready") == nil {
				t.Fatal("unrecognized history was upgraded")
			}
			if db.Admin.QueryRow(db.Context, snapshot).Scan(&after) != nil || before != after {
				t.Fatal("rejected upgrade changed the receipts")
			}
		})
	}
}
