package access_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func setting[T any](v T) *T { return &v }

func TestBookSettingsKeepOriginalHeaderAndShareAcceptedNames(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "example")
	book := f.book(t, tenant, session)
	q := ledger.ReviseBookSettings{BookID: book.ID, RequestKey: "settings", Name: "Example renamed Book", Founder: "Example owner",
		Profile: setting("personal"), StartDate: setting("2026-01-01"), FiscalYearStartMonth: setting(4), FiscalYearStartDay: setting(1), AccountingTimezone: setting("America/New_York")}
	result, err := f.service.ReviseBookSettings(ctx, session, tenant, q, nil)
	var want ledger.Book
	if err != nil || json.Unmarshal(result, &want) != nil || want.Name != q.Name || want.MetadataRevision == nil || *want.MetadataRevision != 1 {
		t.Fatal("accepted settings missing", err)
	}
	var connection ledger.Connection
	if err = f.service.WithOwner(ctx, session, tenant, true, func(o ledger.TenantAdmin) error {
		var e error
		connection, e = o.CreateConnection(ctx, book.ID, "Example reader", "read")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	data, err := f.db.Store.Call(ctx, ledger.Scope{TenantID: tenant, BookID: book.ID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "read"}, "get_book", json.RawMessage(`{}`))
	if err != nil || !bytes.Equal(data, result) {
		t.Fatal("agent read did not use the accepted settings", err)
	}
	if err = f.service.WithOwner(ctx, session, tenant, false, func(o ledger.TenantAdmin) error {
		list, e := o.Books(ctx)
		one, oneErr := o.Book(ctx, book.ID)
		if e != nil || oneErr != nil || len(list) != 1 || list[0].Name != q.Name || one.Name != q.Name {
			t.Fatal("owner readers disagree", e, oneErr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	books, err := f.db.Owner.Books(ctx)
	if err != nil || len(books) != 1 || books[0].Name != q.Name {
		t.Fatal("native listing retained a stale name", err)
	}
	var name string
	if err = f.auth.QueryRow(ctx, "SELECT name FROM access.consent_books($1,$2) WHERE book_id=$3", access.Hash(session), "https://ledger.example.com", book.ID).Scan(&name); err != nil || name != q.Name {
		t.Fatal("consent retained a stale name", err)
	}
	if err = f.db.Admin.QueryRow(ctx, "SELECT name FROM public.books WHERE id=$1", book.ID).Scan(&name); err != nil || name != book.Name {
		t.Fatal("settings overwrote the original header", err)
	}
	second := ledger.ReviseBookSettings{BookID: book.ID, RequestKey: "rename-again", Revision: 1, Name: "Example current Book", Founder: ""}
	data, err = f.service.ReviseBookSettings(ctx, session, tenant, second, nil)
	var updated ledger.Book
	if err != nil || json.Unmarshal(data, &updated) != nil || updated.Profile != want.Profile || updated.AccountingTimezone != want.AccountingTimezone || updated.StartDate != want.StartDate || updated.Founder != "" || updated.CreatedAt != want.CreatedAt {
		t.Fatal("omission erased accepted facts or changed the original date", err)
	}
	second.RequestKey, second.Revision = "clear-optional", 2
	second.Profile, second.StartDate, second.AccountingTimezone = setting(""), setting(""), setting("")
	second.FiscalYearStartMonth, second.FiscalYearStartDay = setting(0), setting(0)
	data, err = f.service.ReviseBookSettings(ctx, session, tenant, second, nil)
	updated = ledger.Book{}
	if err != nil || json.Unmarshal(data, &updated) != nil || updated.AccountingTimezone != "" || updated.StartDate != "" || updated.Profile != "" || updated.FiscalYearStartMonth != nil || updated.FiscalYearStartDay != nil {
		t.Fatal("explicit clearing invented replacement defaults", err)
	}
	var financialRows int
	if err = f.db.Admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM public.entries)+(SELECT count(*) FROM public.requests)").Scan(&financialRows); err != nil || financialRows != 0 {
		t.Fatal("settings manufactured financial effects", err)
	}
}

func TestBookSettingsConcurrentRetryAndStaleSaveRemainDistinct(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, identity := f.owner(t, "example")
	book := f.book(t, tenant, session)
	other := f.book(t, tenant, session)
	config := f.owners.Config().Copy()
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s, err := access.New(f.auth, pool, "https://ledger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	q := ledger.ReviseBookSettings{BookID: book.ID, RequestKey: "rename", Name: "Example accepted Book", Founder: "Example owner"}
	var calls atomic.Int64
	admit := func(context.Context, pgx.Tx, string, string, json.RawMessage) error { calls.Add(1); return nil }
	results := make([]json.RawMessage, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.ReviseBookSettings(ctx, session, tenant, q, admit)
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || calls.Load() != 1 || !bytes.Equal(results[0], results[1]) {
		t.Fatal("retry duplicated a revision or admission", errs, calls.Load())
	}
	q2 := q
	q2.RequestKey, q2.Name = "stale-save", "Example stale name"
	if _, err = s.ReviseBookSettings(ctx, session, tenant, q2, admit); !errors.Is(err, ledger.ErrConflict) || calls.Load() != 1 {
		t.Fatal("a stale save replaced a newer revision", err)
	}
	q2 = q
	q2.BookID = other.ID
	if _, err = s.ReviseBookSettings(ctx, session, tenant, q2, admit); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("a key changed its target Book", err)
	}
	if _, err = f.db.Admin.Exec(ctx, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(session)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReviseBookSettings(ctx, session, tenant, q, admit); err != nil || !bytes.Equal(got, results[0]) || calls.Load() != 1 {
		t.Fatal("retained retry required a new proof or admission", err)
	}
	q2.RequestKey, q2.BookID, q2.Revision = "new-effect", book.ID, 1
	if _, err = s.ReviseBookSettings(ctx, session, tenant, q2, admit); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("a new effect accepted stale authentication", err)
	}
	outcome, err := s.BookSettingsOutcome(ctx, session, tenant, book.ID, q.RequestKey)
	if err != nil || outcome.Status != "recorded" || !bytes.Equal(outcome.Result, results[0]) {
		t.Fatal("lost response could not be resolved", err)
	}
	for _, key := range []string{"missing", q.RequestKey} {
		outcome, err = s.BookSettingsOutcome(ctx, session, tenant, other.ID, key)
		if err != nil || outcome.Status != "unresolved" {
			t.Fatal("outcome lookup crossed a Book", err)
		}
	}
	if err = f.operator.RevokeIdentity(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReviseBookSettings(ctx, session, tenant, q, admit); !errors.Is(err, access.ErrDenied) {
		t.Fatal("revoked owner retrieved or changed settings", err)
	}
	if _, err = s.BookSettingsOutcome(ctx, session, tenant, book.ID, q.RequestKey); !errors.Is(err, access.ErrDenied) {
		t.Fatal("outcome lookup bypassed revocation", err)
	}
}

func TestBookSettingsValidateFactsAndRollbackAdmission(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "example")
	book := f.book(t, tenant, session)
	base := ledger.ReviseBookSettings{BookID: book.ID, RequestKey: "settings", Name: "Example Book", Founder: ""}
	for _, tc := range []struct {
		name   string
		change func(*ledger.ReviseBookSettings)
	}{
		{"blank-name", func(q *ledger.ReviseBookSettings) { q.Name = " \t\n" }},
		{"long-name", func(q *ledger.ReviseBookSettings) { q.Name = strings.Repeat("a", 121) }},
		{"invalid-text", func(q *ledger.ReviseBookSettings) { q.Founder = string([]byte{0xff}) }},
		{"profile", func(q *ledger.ReviseBookSettings) { q.Profile = setting("other") }},
		{"date", func(q *ledger.ReviseBookSettings) { q.StartDate = setting("2026-02-30") }},
		{"partial-calendar", func(q *ledger.ReviseBookSettings) { q.FiscalYearStartMonth = setting(1) }},
		{"leap-calendar", func(q *ledger.ReviseBookSettings) {
			q.FiscalYearStartMonth, q.FiscalYearStartDay = setting(2), setting(29)
		}},
		{"mixed-clear", func(q *ledger.ReviseBookSettings) {
			q.FiscalYearStartMonth, q.FiscalYearStartDay = setting(0), setting(1)
		}},
		{"host-timezone", func(q *ledger.ReviseBookSettings) { q.AccountingTimezone = setting("Local") }},
		{"unknown-timezone", func(q *ledger.ReviseBookSettings) { q.AccountingTimezone = setting("Unknown/Example") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			tc.change(&q)
			if _, err := f.service.ReviseBookSettings(ctx, session, tenant, q, nil); !errors.Is(err, ledger.ErrInvalid) {
				t.Fatal("invalid settings accepted", err)
			}
		})
	}
	if _, err := f.db.Admin.Exec(ctx, "CREATE TABLE public.settings_usage(id integer); GRANT INSERT ON public.settings_usage TO "+pgx.Identifier{f.roles[1]}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	admit := func(ctx context.Context, tx pgx.Tx, gotTenant, operation string, input json.RawMessage) error {
		if gotTenant != tenant || operation != "revise_book_settings" || !json.Valid(input) {
			t.Fatal("admission received an unbound operation")
		}
		_, err := tx.Exec(ctx, "INSERT INTO public.settings_usage VALUES(1)")
		if err != nil {
			t.Fatal(err)
		}
		return errors.New("synthetic quota denial")
	}
	if _, err := f.service.ReviseBookSettings(ctx, session, tenant, base, admit); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("denied admission recorded settings", err)
	}
	var count int
	if err := f.db.Admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM public.book_revisions)+(SELECT count(*) FROM public.settings_usage)+(SELECT count(*) FROM access.owner_requests)").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed admission retained a partial effect", err)
	}
	if _, err := f.service.ReviseBookSettings(ctx, session, tenant, base, nil); err != nil {
		t.Fatal("failed attempt consumed the request key", err)
	}
}

func TestBookSettingsSnapshotKeepsHistoryAndRejectsTampering(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "example")
	book := f.book(t, tenant, session)
	q := ledger.ReviseBookSettings{BookID: book.ID, RequestKey: "first", Name: "Example first name", Founder: "Example owner", AccountingTimezone: setting("Europe/Warsaw")}
	if _, err := f.service.ReviseBookSettings(ctx, session, tenant, q, nil); err != nil {
		t.Fatal(err)
	}
	q.RequestKey, q.Revision, q.Name = "second", 1, "Example current name"
	if _, err := f.service.ReviseBookSettings(ctx, session, tenant, q, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.db.Owner.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err = os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	key, err := documents.NewKeyFile(tenant)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.ParseKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	vault := &documents.Vault{Storage: disk, Keys: keys}
	var archive bytes.Buffer
	if err = f.db.Owner.WriteArchive(ctx, vault, &archive, book.ID); err != nil {
		t.Fatal("archive omitted Book revisions", err)
	}
	archiveTarget := testutil.New(t)
	if err = archiveTarget.Owner.RestoreArchive(archiveTarget.Context, vault, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal("archive settings restore failed", err)
	}
	archived, err := archiveTarget.Owner.Snapshot(archiveTarget.Context)
	if err != nil || !bytes.Equal(archived.Tables["book_revisions"], snapshot.Tables["book_revisions"]) {
		t.Fatal("archive changed accepted settings", err)
	}
	for _, mode := range []string{"valid", "broken-chain", "request-mismatch", "result-mismatch", "host-timezone", "unknown-timezone", "original-header", "missing-history"} {
		t.Run(mode, func(t *testing.T) {
			copy := snapshot
			copy.Tables = map[string]json.RawMessage{}
			for name, data := range snapshot.Tables {
				copy.Tables[name] = append(json.RawMessage{}, data...)
			}
			var rows []map[string]json.RawMessage
			if json.Unmarshal(copy.Tables["book_revisions"], &rows) != nil || len(rows) != 2 {
				t.Fatal("revision history missing")
			}
			first, second := 0, 1
			if string(rows[first]["revision"]) != "1" {
				first, second = second, first
			}
			switch mode {
			case "broken-chain":
				rows[second]["prior_name"] = json.RawMessage(`"Another name"`)
			case "request-mismatch":
				rows[first]["name"] = json.RawMessage(`"Another name"`)
			case "result-mismatch":
				rows[first]["founder"] = json.RawMessage(`"Another owner"`)
			case "host-timezone", "unknown-timezone":
				zone := "Local"
				if mode == "unknown-timezone" {
					zone = "Unknown/Example"
				}
				for _, row := range rows {
					row["accounting_timezone"], _ = json.Marshal(zone)
					for _, field := range []string{"request_bytes", "result_bytes"} {
						var stored string
						var content map[string]any
						if json.Unmarshal(row[field], &stored) != nil || !strings.HasPrefix(stored, `\x`) {
							t.Fatal("invalid retained bytes")
						}
						encoded, err := hex.DecodeString(strings.TrimPrefix(stored, `\x`))
						if err != nil || json.Unmarshal(encoded, &content) != nil {
							t.Fatal("invalid retained bytes", err)
						}
						content["accounting_timezone"] = zone
						encoded, _ = json.Marshal(content)
						row[field], _ = json.Marshal(`\x` + hex.EncodeToString(encoded))
					}
				}
			case "original-header":
				var headers []map[string]json.RawMessage
				json.Unmarshal(copy.Tables["books"], &headers)
				headers[0]["name"] = json.RawMessage(`"Rewritten original"`)
				copy.Tables["books"], _ = json.Marshal(headers)
			case "missing-history":
				rows = rows[second : second+1]
			}
			copy.Tables["book_revisions"], _ = json.Marshal(rows)
			target := testutil.New(t)
			err := target.Owner.Restore(target.Context, copy)
			if mode == "valid" {
				if err != nil {
					t.Fatal("settings restore failed", err)
				}
				after, err := target.Owner.Snapshot(target.Context)
				if err != nil {
					t.Fatal(err)
				}
				for name, data := range snapshot.Tables {
					if !bytes.Equal(data, after.Tables[name]) {
						t.Fatal("restore changed preserved rows", name)
					}
				}
				list, err := target.Owner.Books(target.Context)
				if err != nil || len(list) != 1 || list[0].Name != q.Name {
					t.Fatal("restored current settings unavailable", err)
				}
			} else {
				if err == nil {
					t.Fatal("corrupt metadata restored", mode)
				}
				var occupied bool
				if e := target.Admin.QueryRow(target.Context, "SELECT EXISTS(SELECT 1 FROM public.books)").Scan(&occupied); e != nil || occupied {
					t.Fatal("rejected restore left partial state", e)
				}
			}
		})
	}
}

func TestBookSettingsDoNotGrantAgentsOrOtherOwnersAdministration(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "example")
	book := f.book(t, tenant, session)
	other, otherSession, _ := f.owner(t, "other")
	q := ledger.ReviseBookSettings{BookID: book.ID, RequestKey: "settings", Name: "Example renamed Book"}
	if _, err := f.service.ReviseBookSettings(ctx, otherSession, tenant, q, nil); !errors.Is(err, access.ErrDenied) {
		t.Fatal("foreign owner selected a tenant", err)
	}
	if _, err := f.service.ReviseBookSettings(ctx, otherSession, other, q, nil); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("foreign Book settings accepted", err)
	}
	if err := f.service.WithOwner(ctx, session, tenant, true, func(o ledger.TenantAdmin) error {
		_, err := o.ReviseBook(ctx, ledger.ID(), q, nil)
		if !errors.Is(err, ledger.ErrDenied) {
			t.Fatal("supplied actor replaced the live owner", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var connection ledger.Connection
	if err := f.service.WithOwner(ctx, session, tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		connection, err = o.CreateConnection(ctx, book.ID, "Example agent", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: tenant, BookID: book.ID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "write"}
	input, _ := json.Marshal(q)
	if _, err := f.db.Store.Call(ctx, scope, "revise_book_settings", input); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("agent received owner administration", err)
	}
	if _, err := f.db.Runtime.Exec(ctx, "INSERT INTO public.book_revisions(tenant_id) SELECT $1::uuid WHERE false", tenant); err == nil {
		t.Fatal("runtime role gained metadata insert privilege")
	}
}

func TestBookSettingsTimezoneCatalogCannotBeShadowed(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "example")
	book := f.book(t, tenant, session)
	tx, err := f.owners.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `CREATE TEMP TABLE pg_timezone_names(name text); INSERT INTO pg_timezone_names VALUES('Unknown/Example')`); err != nil {
		t.Fatal("restricted owner could not construct the shadow fixture", err)
	}
	var shadow string
	if err = tx.QueryRow(ctx, "SELECT name FROM pg_timezone_names").Scan(&shadow); err != nil || shadow != "Unknown/Example" {
		t.Fatal("negative control did not shadow the unqualified name", err)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin',$2,true),
 set_config('vult.tenant_id',$3,true),set_config('vult.book_id',$4,true)`, access.Hash(session), "https://ledger.example.com", tenant, book.ID); err != nil {
		t.Fatal(err)
	}
	var actor string
	if err = tx.QueryRow(ctx, "SELECT access.require_owner($1,true)::text", tenant).Scan(&actor); err != nil {
		t.Fatal("negative control lacked an owner lease", err)
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('vult.actor_id',$1,true)", actor); err != nil {
		t.Fatal(err)
	}
	q := ledger.ReviseBookSettings{BookID: book.ID, RequestKey: "settings", Name: book.Name, AccountingTimezone: setting("Unknown/Example")}
	book.MetadataRevision, book.AccountingTimezone = setting(int64(1)), *q.AccountingTimezone
	input, _ := json.Marshal(q)
	result, _ := json.Marshal(book)
	_, err = tx.Exec(ctx, `INSERT INTO public.book_revisions(tenant_id,book_id,revision,actor_id,request_key,prior_name,name,accounting_timezone,request_bytes,result_bytes)
 VALUES($1,$2,1,$3,$4,$5,$5,$6,$7,$8)`, tenant, book.ID, actor, q.RequestKey, q.Name, *q.AccountingTimezone, input, result)
	var failure *pgconn.PgError
	if !errors.As(err, &failure) || failure.Code != "23514" || failure.Message != "unsupported accounting timezone" {
		t.Fatal("temporary catalog bypassed timezone validation", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.book_revisions").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed timezone control recorded a revision", err)
	}
}

func TestBookSettingsOperatorStatusUsesTheRealRoleCatalog(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "example")
	book := f.book(t, tenant, session)
	for _, role := range []string{"runtime", "owner"} {
		t.Run(role, func(t *testing.T) {
			pool := f.db.Runtime
			if role == "owner" {
				pool = f.owners
			}
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(ctx, `CREATE TEMP TABLE pg_roles(rolname text,rolsuper boolean); INSERT INTO pg_roles VALUES(session_user,true)`); err != nil {
				t.Fatal(err)
			}
			var fake, actual, operator bool
			if err = tx.QueryRow(ctx, `SELECT (SELECT rolsuper FROM pg_roles WHERE rolname=session_user),
 (SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname=session_user),public.ledger_import_owner()`).Scan(&fake, &actual, &operator); err != nil || !fake || actual || operator {
				t.Fatal("temporary role table changed installation authority", err)
			}
			if _, err = tx.Exec(ctx, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.restore_book',$3,true)`, tenant, book.ID, tenant+"/"+book.ID); err != nil {
				t.Fatal(err)
			}
			var want string
			if role == "runtime" {
				_, err = tx.Exec(ctx, "SELECT public.ledger_validate_balances($1,$2,$3)", tenant, book.ID, ledger.ID())
				want = "selected Book write access required"
			} else {
				if _, err = tx.Exec(ctx, `SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin',$2,true)`, access.Hash(session), "https://ledger.example.com"); err != nil {
					t.Fatal(err)
				}
				var actor string
				if err = tx.QueryRow(ctx, "SELECT access.require_owner($1,true)::text", tenant).Scan(&actor); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(ctx, "SELECT set_config('vult.actor_id',$1,true)", actor); err != nil {
					t.Fatal(err)
				}
				q := ledger.ReviseBookSettings{BookID: book.ID, RequestKey: "settings", Name: book.Name}
				book.MetadataRevision = setting(int64(1))
				input, _ := json.Marshal(q)
				result, _ := json.Marshal(book)
				_, err = tx.Exec(ctx, `INSERT INTO public.book_revisions(tenant_id,book_id,revision,actor_id,request_key,prior_name,name,request_bytes,result_bytes)
 VALUES($1,$2,1,$3,$4,$5,$5,$6,$7)`, tenant, book.ID, ledger.ID(), q.RequestKey, q.Name, input, result)
				want = "authenticated owner required"
			}
			var failure *pgconn.PgError
			if !errors.As(err, &failure) || failure.Code != "42501" || failure.Message != want {
				t.Fatal("spoofed operator predicate bypassed its authority check", role, err)
			}
		})
	}
}
