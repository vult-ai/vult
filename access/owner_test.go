package access_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestOwnerRequestConcurrentRetryCreatesOneBook(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	c := f.owners.Config().Copy()
	c.MaxConns = 2
	pool, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	s, e := access.New(f.auth, pool, "https://ledger.example.com")
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int64
	var wg sync.WaitGroup
	results := make([]json.RawMessage, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.OwnerRequest(ctx, session, tenant, "example-book", "create_book", json.RawMessage(`{"name":"Example","currency":"USD","scale":2}`), nil, func(o ledger.TenantAdmin) (json.RawMessage, error) {
				calls.Add(1)
				time.Sleep(100 * time.Millisecond)
				book, e := o.CreateBook(ctx, "Example", "USD", 2)
				b, _ := json.Marshal(book)
				return b, e
			})
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || calls.Load() != 1 || !bytes.Equal(results[0], results[1]) {
		t.Fatal("concurrent retry duplicated its effect", errs, calls.Load())
	}
	outcome, e := s.OwnerOutcome(ctx, session, tenant, "example-book")
	if e != nil || outcome.Status != "recorded" || !bytes.Equal(outcome.Result, results[0]) {
		t.Fatal("owner outcome was not durable", e)
	}
	if _, e = s.OwnerRequest(ctx, session, tenant, "example-book", "create_book", json.RawMessage(`{"name":"Other"}`), nil, func(ledger.TenantAdmin) (json.RawMessage, error) {
		t.Fatal("conflicting key applied a change")
		return nil, nil
	}); !errors.Is(e, ledger.ErrConflict) {
		t.Fatal("conflicting owner request accepted")
	}
	if _, e = f.db.Admin.Exec(ctx, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(session)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.OwnerRequest(ctx, session, tenant, "example-book", "create_book", json.RawMessage(`{"name":"Example","currency":"USD","scale":2}`), nil, func(ledger.TenantAdmin) (json.RawMessage, error) {
		t.Fatal("retained request ran again")
		return nil, nil
	}); e != nil {
		t.Fatal("retained result required fresh MFA", e)
	}
	if _, e = s.OwnerRequest(ctx, session, tenant, "new-book", "create_book", json.RawMessage(`{}`), nil, func(ledger.TenantAdmin) (json.RawMessage, error) {
		t.Fatal("stale MFA applied a new change")
		return nil, nil
	}); !errors.Is(e, access.ErrDenied) {
		t.Fatal("new owner effect accepted stale MFA")
	}
}

func ownerHTTP(h http.Handler, method, path, body, session, proof string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://ledger.example.com"+path, strings.NewReader(body))
	r.URL.Scheme = ""
	r.URL.Host = ""
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "ledger.example.com")
	r.Header.Set("X-Forwarded-For", "192.0.2.10")
	r.Header.Set("Origin", "https://ledger.example.com")
	r.Header.Set("Content-Type", "application/json")
	if session != "" {
		r.AddCookie(&http.Cookie{Name: access.SessionCookie, Value: session})
	}
	if proof != "" {
		r.Header.Set("X-CSRF-Token", proof)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestOwnerHTTPSeparatesHumanScopeAndRetainsOutcomes(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	other, otherSession, _ := f.owner(t, "beta")
	foreign := f.book(t, other, otherSession)
	h := f.network(t, nil)
	w := networkRequest(ctx, h, "GET", "/auth/session", "", &http.Cookie{Name: access.SessionCookie, Value: session})
	var info struct {
		CSRF string `json:"csrf"`
	}
	if json.Unmarshal(w.Body.Bytes(), &info) != nil || info.CSRF == "" {
		t.Fatal("CSRF missing")
	}
	base := "/owner/tenants/" + tenant
	body := `{"request_key":"create-one","name":"Example","currency":"USD","scale":2}`
	w = ownerHTTP(h, "POST", base+"/books", body, session, access.Random())
	if w.Code != 403 {
		t.Fatal("owner change accepted invalid CSRF")
	}
	w = ownerHTTP(h, "POST", "/owner/tenants/"+other+"/books", body, session, info.CSRF)
	if w.Code != 403 {
		t.Fatal("owner change escaped tenant")
	}
	w = ownerHTTP(h, "POST", base+"/books", body, session, info.CSRF)
	if w.Code != 200 {
		t.Fatal("book creation failed", w.Body.String())
	}
	first := append([]byte(nil), w.Body.Bytes()...)
	var book ledger.Book
	if json.Unmarshal(first, &book) != nil || !ledger.ValidID(book.ID) {
		t.Fatal("Book result invalid")
	}
	w = ownerHTTP(h, "POST", base+"/books", body, session, info.CSRF)
	if w.Code != 200 || !bytes.Equal(first, w.Body.Bytes()) {
		t.Fatal("owner HTTP retry changed result")
	}
	w = ownerHTTP(h, "POST", base+"/books", strings.Replace(body, `"Example"`, `"Changed"`, 1), session, info.CSRF)
	if w.Code != 409 {
		t.Fatal("reused owner key changed input")
	}
	w = ownerHTTP(h, "GET", base+"/requests/create-one", "", session, "")
	var outcome ledger.Outcome
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &outcome) != nil || outcome.Status != "recorded" {
		t.Fatal("owner outcome lookup failed")
	}
	w = ownerHTTP(h, "GET", base+"/requests/missing", "", session, "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &outcome) != nil || outcome.Status != "unresolved" {
		t.Fatal("missing outcome invented rejection")
	}
	w = ownerHTTP(h, "GET", base+"/books", "", session, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), foreign.ID) {
		t.Fatal("owner Book list leaked tenant")
	}
	closePath := base + "/books/" + book.ID + "/close-period"
	closeBody := `{"request_key":"close-one","from":"2026-01-01","through":"2026-01-31"}`
	for i := 0; i < 2; i++ {
		w = ownerHTTP(h, "POST", closePath, closeBody, session, info.CSRF)
		if w.Code != 200 {
			t.Fatal("period close/retry failed", w.Body.String())
		}
	}
	client := f.client(t)
	tokens := f.tokens(t, client, session, tenant, book.ID, "write", "")
	scope, e := f.service.ResolveAgent(ctx, tokens.Access)
	if e != nil {
		t.Fatal(e)
	}
	w = ownerHTTP(h, "POST", base+"/connections/"+scope.ConnectionID+"/revoke", `{"request_key":"revoke-one"}`, session, info.CSRF)
	if w.Code != 200 {
		t.Fatal("owner revoke failed", w.Body.String())
	}
	if _, e = f.service.ResolveAgent(ctx, tokens.Access); !errors.Is(e, access.ErrDenied) {
		t.Fatal("revoked bearer still worked")
	}
	w = ownerHTTP(h, "POST", base+"/books", body, "", "")
	if w.Code != 403 {
		t.Fatal("agent/no-session request became an owner")
	}
}

func TestOwnerReadTransactionCannotWriteAndBoundedListsNeverTruncate(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	f.book(t, tenant, session)
	f.book(t, tenant, session)
	if e := f.service.WithOwner(ctx, session, tenant, false, func(o ledger.TenantAdmin) error {
		if _, e := o.BooksBounded(ctx, 1); !errors.Is(e, ledger.ErrLimit) {
			t.Fatal("bounded Book list silently truncated")
		}
		_, e := o.CreateBook(ctx, "Denied", "USD", 2)
		return e
	}); !errors.Is(e, ledger.ErrDenied) {
		t.Fatal("read-only owner lease changed a Book", e)
	}
}

func TestOwnerAdmissionChargesOnlyCommittedNewRequests(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	if _, e := f.db.Admin.Exec(ctx, "CREATE SCHEMA extension;CREATE TABLE extension.usage(tenant_id uuid PRIMARY KEY,used bigint NOT NULL)"); e != nil {
		t.Fatal(e)
	}
	role := pgx.Identifier{f.roles[1]}.Sanitize()
	if _, e := f.db.Admin.Exec(ctx, "GRANT USAGE ON SCHEMA extension TO "+role+";GRANT SELECT,INSERT,UPDATE ON extension.usage TO "+role); e != nil {
		t.Fatal(e)
	}
	h := f.network(t, countingAdmission{})
	response := networkRequest(ctx, h, "GET", "/auth/session", "", &http.Cookie{Name: access.SessionCookie, Value: session})
	var info struct {
		CSRF string `json:"csrf"`
	}
	if json.Unmarshal(response.Body.Bytes(), &info) != nil || info.CSRF == "" {
		t.Fatal("CSRF missing")
	}
	base := "/owner/tenants/" + tenant + "/books"
	input := `{"request_key":"one","name":"Example","currency":"USD","scale":2}`
	for i := 0; i < 2; i++ {
		w := ownerHTTP(h, "POST", base, input, session, info.CSRF)
		if w.Code != 200 {
			t.Fatal("owner admission/retry failed", w.Body.String())
		}
	}
	w := ownerHTTP(h, "POST", base, `{"request_key":"bad","name":"Example","currency":"invalid","scale":2}`, session, info.CSRF)
	if w.Code != 400 {
		t.Fatal("invalid owner effect did not fail")
	}
	var used int
	if e := f.db.Admin.QueryRow(ctx, "SELECT used FROM extension.usage WHERE tenant_id=$1", tenant).Scan(&used); e != nil || used != 1 {
		t.Fatal("owner retry or failed change retained a quota charge", e, used)
	}
}
