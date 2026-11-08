package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestReportPostingsSQLRequiresLiveScopedOwner(t *testing.T) {
	f := documentServer(t)
	ctx := f.db.Context
	connection, err := f.db.Owner.CreateConnection(ctx, f.book.ID, "Example recorder", "write")
	if err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: connection.ID, ActorID: connection.ActorID, Access: "write"}
	debit, credit := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{
		RequestKey: "example", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example posting",
		Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: "125"}, {AccountID: credit.ID, Side: "credit", AmountMinor: "125"}},
	})
	const query = `SELECT count(*) FROM access.report_postings($1,$2,DATE '2026-12-31',clock_timestamp(),NULL,NULL,NULL,NULL)`
	scopeOwner := func(t *testing.T, tx pgx.Tx, tenant, book, session string) {
		t.Helper()
		if _, err := tx.Exec(ctx, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),
            set_config('vult.owner_session',$3,true),set_config('vult.owner_origin',$4,true)`,
			tenant, book, access.Hash(session), configuration().Origin); err != nil {
			t.Fatal(err)
		}
	}
	denied := func(err error) bool {
		var failure *pgconn.PgError
		return errors.As(err, &failure) && failure.Code == "42501"
	}
	foreignTenant := ledger.ID()
	for _, tc := range []struct {
		name                  string
		pool                  *pgxpool.Pool
		tenant, book, session string
		allow                 bool
	}{
		{"owner", f.owners, f.tenant, f.book.ID, f.session, true},
		{"wrong-book", f.owners, f.tenant, ledger.ID(), f.session, false},
		{"wrong-tenant", f.owners, foreignTenant, f.book.ID, f.session, false},
		{"missing-session", f.owners, f.tenant, f.book.ID, "", false},
		{"runtime-role", f.db.Runtime, f.tenant, f.book.ID, f.session, false},
		{"auth-role", f.auth, f.tenant, f.book.ID, f.session, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := tc.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			selected := tc.book
			if tc.name == "wrong-book" {
				selected = f.book.ID
			}
			scopeOwner(t, tx, tc.tenant, selected, tc.session)
			var count int
			err = tx.QueryRow(ctx, query, tc.tenant, tc.book).Scan(&count)
			if tc.allow && (err != nil || count != 2) || !tc.allow && (!denied(err) || count != 0) {
				t.Fatal("report function authorization differs", count, err)
			}
		})
	}

	t.Run("expiry-during-read", func(t *testing.T) {
		session, err := f.sessions.IssueSession(ctx, f.identity)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := f.owners.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		scopeOwner(t, tx, f.tenant, f.book.ID, session)
		var reader int
		if err = tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&reader); err != nil {
			t.Fatal(err)
		}
		lock, err := f.db.Admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback(context.Background())
		var blocker int
		if err = lock.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blocker); err != nil {
			t.Fatal(err)
		}
		if _, err = lock.Exec(ctx, "LOCK TABLE public.lines IN ACCESS EXCLUSIVE MODE"); err != nil {
			t.Fatal(err)
		}
		var expires time.Time
		if err = f.db.Admin.QueryRow(ctx, `UPDATE access.sessions SET expires_at=clock_timestamp()+interval '2 seconds'
            WHERE token_hash=$1 RETURNING expires_at`, access.Hash(session)).Scan(&expires); err != nil {
			t.Fatal(err)
		}
		readCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		done := make(chan struct{})
		var count int
		var readErr error
		go func() {
			defer close(done)
			readErr = tx.QueryRow(readCtx, query, f.tenant, f.book.ID).Scan(&count)
		}()
		defer func() {
			cancel()
			<-done
		}()
		for {
			var blocked, live bool
			if err = f.db.Admin.QueryRow(readCtx, "SELECT $1=ANY(pg_blocking_pids($2)),clock_timestamp()<$3", blocker, reader, expires).Scan(&blocked, &live); err != nil {
				t.Fatal(err)
			}
			if !live {
				t.Fatal("posting read did not enter the locked table before expiry")
			}
			if blocked {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err = lock.Exec(readCtx, "SELECT pg_sleep(greatest(0,extract(epoch FROM $1::timestamptz-clock_timestamp()))+0.01)", expires); err != nil {
			t.Fatal(err)
		}
		if err = lock.Commit(readCtx); err != nil {
			t.Fatal(err)
		}
		<-done
		if !denied(readErr) || count != 0 {
			t.Fatal("expired owner received materialized report postings", count, readErr)
		}
	})
}

func TestBrowserFinancialReportsEmptyFullAndMalformedScopes(t *testing.T) {
	f, s := browserServer(t)
	base := "/api/v1/financial-reports"
	query := url.Values{"book_id": {f.book.ID}, "report": {"profit_loss"}, "from": {"2026-01-01"}, "to": {"2026-12-31"}, "include_csv": {"false"}}
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		return w
	}
	w := get(base + "?" + query.Encode())
	var empty ledger.FinancialReport
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &empty) != nil || empty.SchemaVersion != 1 || empty.Reconciliation["income"] != "0" || empty.Reconciliation["expenses"] != "0" || empty.Rows == nil || empty.Status != "partial" || empty.Scope.Coverage != "unknown" || empty.Scope.FiscalStart != "" {
		t.Fatal("valid empty report became a failure or claimed complete history", w.Code)
	}
	var connection ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		connection, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: connection.ID, ActorID: connection.ActorID, Access: "write"}
	cash, revenue := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "4000", "income")
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "sale", EconomicDate: "2025-12-31", AccountingDate: "2026-01-02", Description: "Example recorded sale", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "9007199254740993"}, {AccountID: revenue.ID, Side: "credit", AmountMinor: "9007199254740993"}}})
	w = get(base + "?" + query.Encode())
	var full ledger.FinancialReport
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &full) != nil || full.Book.ID != f.book.ID || full.Book.Currency != "USD" || full.Book.Scale != 2 || full.Reconciliation["income"] != "9007199254740993" || full.ExportCSV != "" || !full.ExportAvailable || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("full recorded report changed exact values or scope", w.Code)
	}
	contributions := url.Values{}
	for key, values := range query {
		if key != "include_csv" {
			contributions[key] = values
		}
	}
	for key, value := range map[string]string{"digest": full.Digest, "row_id": "total:result", "column_id": "current", "knowledge_cutoff": full.Scope.KnowledgeCutoff, "generated_at": full.Scope.GeneratedAt} {
		contributions.Set(key, value)
	}
	w = get(base + "/contributions?" + contributions.Encode())
	var parts struct {
		Digest string `json:"result_digest"`
		Total  string `json:"total_minor"`
		Items  []struct {
			Account string `json:"account_id"`
		} `json:"contributions"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &parts) != nil || parts.Digest != full.Digest || parts.Total != "9007199254740993" || len(parts.Items) != 1 || parts.Items[0].Account != revenue.ID {
		t.Fatal("report drilldown is not attributable", w.Code)
	}
	months := url.Values{}
	for key, values := range query {
		months[key] = values
	}
	months.Set("months_from", "2026-01-01")
	months.Set("months_to", "2026-12-31")
	w = get(base + "/months?" + months.Encode())
	var set ledger.FinancialReportMonths
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &set) != nil || set.SchemaVersion != 1 || len(set.Months) != 12 || set.Months[0].Reconciliation["income"] != "9007199254740993" || set.Months[1].Columns[1].Period.From != "2026-02-01" {
		t.Fatal("shared-snapshot monthly reports differ", w.Code)
	}
	w = get(base + "/export-permission?book_id=" + f.book.ID)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"allowed":true`) {
		t.Fatal("owner export permission unavailable", w.Code)
	}
	foreignTenant, foreignBook := ledger.ID(), ledger.ID()
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.tenants(id) VALUES($1)", foreignTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,'Example separate Book','USD',2)", foreignTenant, foreignBook); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		code int
	}{
		{base + "?book_id=" + foreignBook + "&report=trial_balance&as_of=2026-12-31", http.StatusNotFound},
		{base + "/export-permission?book_id=" + foreignBook, http.StatusNotFound},
		{base + "?" + query.Encode() + "&book_id=" + f.book.ID, http.StatusBadRequest},
		{base + "?" + query.Encode() + "&tenant_id=" + f.tenant, http.StatusBadRequest},
		{base + "?" + query.Encode() + "&from=2026-02-31", http.StatusBadRequest},
		{base + "?" + query.Encode() + "&account_ids=invalid", http.StatusBadRequest},
		{base + "?" + query.Encode() + "&include_csv=maybe", http.StatusBadRequest},
		{base + "?" + query.Encode() + "&unknown=%zz", http.StatusBadRequest},
		{base + "/months?" + query.Encode(), http.StatusBadRequest},
		{base + "/contributions?" + query.Encode(), http.StatusBadRequest},
		{base + "/export-permission?book_id=" + f.book.ID + "&extra=1", http.StatusBadRequest},
	} {
		w = get(tc.path)
		if w.Code != tc.code || strings.Contains(w.Body.String(), "9007199254740993") || strings.Contains(w.Body.String(), revenue.ID) {
			t.Fatal("rejected report leaked financial facts", w.Code, tc.code)
		}
	}
	for _, endpoint := range []string{base + "?" + query.Encode(), base + "/months?" + months.Encode(), base + "/contributions?" + contributions.Encode(), base + "/export-permission?book_id=" + f.book.ID} {
		for _, token := range []bool{false, true} {
			r := browserRequest(http.MethodGet, endpoint, "")
			want := http.StatusUnauthorized
			if token {
				r = browserRequest(http.MethodGet, endpoint, f.session)
				r.Header.Set("Authorization", "Bearer example")
				want = http.StatusForbidden
			}
			w = httptest.NewRecorder()
			s.Handler.ServeHTTP(w, r)
			if w.Code != want || strings.Contains(w.Body.String(), revenue.ID) {
				t.Fatal("non-session principal reached owner report", w.Code, want)
			}
		}
	}
}
