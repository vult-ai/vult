package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func ownerPost(path, session, body string) *http.Request {
	r := browserRequest(http.MethodPost, path, session)
	r.Body = io.NopCloser(strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", configuration().Origin)
	r.Header.Set("X-CSRF-Token", access.Hash("csrf:"+session))
	return r
}

func TestBrowserCreatesABookInTheOwnersWorkspace(t *testing.T) {
	f, s := browserServer(t)
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		return w
	}
	body := `{"request_key":"new-book","name":"Example Personal","currency":"EUR","scale":2}`
	w := serve(ownerPost(booksPath, f.session, body))
	var created webBook
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &created) != nil || !ledger.ValidID(created.ID) || created.Name != "Example Personal" || created.Currency != "EUR" || created.Scale != 2 {
		t.Fatalf("create returned %d %s", w.Code, w.Body.String())
	}
	retry := serve(ownerPost(booksPath, f.session, body))
	var again webBook
	if retry.Code != http.StatusOK || json.Unmarshal(retry.Body.Bytes(), &again) != nil || again.ID != created.ID {
		t.Fatalf("retry created another Book: %d %s", retry.Code, retry.Body.String())
	}
	if w := serve(ownerPost(booksPath, f.session, strings.Replace(body, "EUR", "PLN", 1))); w.Code != http.StatusConflict {
		t.Fatalf("changed request under the same key returned %d", w.Code)
	}
	list := serve(browserRequest(http.MethodGet, booksPath, f.session))
	var listed struct {
		Books []webBook `json:"books"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil {
		t.Fatalf("list returned %d", list.Code)
	}
	count := 0
	for _, b := range listed.Books {
		if b.ID == created.ID {
			count++
		}
	}
	if count != 1 || len(listed.Books) != 2 {
		t.Fatalf("listed %d Books, new Book %d times", len(listed.Books), count)
	}
	for name, bad := range map[string]string{
		"no-name":         `{"request_key":"a","name":"  ","currency":"EUR","scale":2}`,
		"lowercase":       `{"request_key":"b","name":"X","currency":"eur","scale":2}`,
		"scale":           `{"request_key":"c","name":"X","currency":"EUR","scale":5}`,
		"no-key":          `{"name":"X","currency":"EUR","scale":2}`,
		"unknown-field":   `{"request_key":"d","name":"X","currency":"EUR","scale":2,"tenant_id":"` + f.tenant + `"}`,
		"long-name-bytes": `{"request_key":"e","name":"` + strings.Repeat("x", 121) + `","currency":"EUR","scale":2}`,
	} {
		if w := serve(ownerPost(booksPath, f.session, bad)); w.Code != http.StatusBadRequest {
			t.Fatalf("%s returned %d", name, w.Code)
		}
	}
	noProof := ownerPost(booksPath, f.session, `{"request_key":"f","name":"X","currency":"EUR","scale":2}`)
	noProof.Header.Del("X-CSRF-Token")
	if w := serve(noProof); w.Code != http.StatusForbidden {
		t.Fatalf("create without CSRF proof returned %d", w.Code)
	}
}

func TestBrowserClosesAPeriodPermanentlyForOwnerAndAgents(t *testing.T) {
	f, s := browserServer(t)
	ctx := f.db.Context
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		return w
	}
	var c ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(ctx, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	cash, income := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "4000", "income")
	posted := f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "january", EconomicDate: "2026-01-15", AccountingDate: "2026-01-15", Description: "Example January income", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "1000"}, {AccountID: income.ID, Side: "credit", AmountMinor: "1000"}}})
	var entry struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(posted, &entry) != nil || !ledger.ValidID(entry.ID) {
		t.Fatalf("posting returned %s", posted)
	}

	periods := "/api/v1/periods?book_id=" + f.book.ID
	if w := serve(browserRequest(http.MethodGet, periods, f.session)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"periods":[]`) {
		t.Fatalf("empty periods returned %d %s", w.Code, w.Body.String())
	}
	body := `{"request_key":"close-january","book_id":"` + f.book.ID + `","from":"2026-01-01","through":"2026-01-31"}`
	if w := serve(ownerPost(periodsClosePath, f.session, body)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"closed"`) {
		t.Fatalf("close returned %d %s", w.Code, w.Body.String())
	}
	if w := serve(ownerPost(periodsClosePath, f.session, body)); w.Code != http.StatusOK {
		t.Fatalf("retrying the same close returned %d", w.Code)
	}
	overlap := `{"request_key":"close-overlap","book_id":"` + f.book.ID + `","from":"2026-01-20","through":"2026-02-10"}`
	if w := serve(ownerPost(periodsClosePath, f.session, overlap)); w.Code == http.StatusOK {
		t.Fatal("an overlapping close was accepted")
	}
	w := serve(browserRequest(http.MethodGet, periods, f.session))
	var listed struct {
		Periods []ledger.ClosedPeriod `json:"periods"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Periods) != 1 || listed.Periods[0].From != "2026-01-01" || listed.Periods[0].Through != "2026-01-31" {
		t.Fatalf("closed periods returned %d %s", w.Code, w.Body.String())
	}

	// The agent sees the lock and cannot post or reverse inside it.
	var agentView struct {
		Periods []ledger.ClosedPeriod `json:"periods"`
	}
	if json.Unmarshal(f.db.Call(t, scope, "list_closed_periods", struct{}{}), &agentView) != nil || len(agentView.Periods) != 1 {
		t.Fatal("agent could not read the closed period")
	}
	inside, _ := json.Marshal(ledger.PostJournal{RequestKey: "late", EconomicDate: "2026-01-20", AccountingDate: "2026-01-20", Description: "Example late entry", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "5"}, {AccountID: income.ID, Side: "credit", AmountMinor: "5"}}})
	if _, err := f.db.Store.Call(ctx, scope, "post_journal", inside); err == nil {
		t.Fatal("a posting dated inside the closed period was accepted")
	}
	after, _ := json.Marshal(ledger.PostJournal{RequestKey: "february", EconomicDate: "2026-02-02", AccountingDate: "2026-02-02", Description: "Example February income", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "5"}, {AccountID: income.ID, Side: "credit", AmountMinor: "5"}}})
	if _, err := f.db.Store.Call(ctx, scope, "post_journal", after); err != nil {
		t.Fatalf("a posting after the closed period failed: %v", err)
	}

	// Another owner's Book stays out of reach.
	foreign := strings.Replace(body, f.book.ID, ledger.ID(), 1)
	if w := serve(ownerPost(periodsClosePath, f.session, strings.Replace(foreign, "close-january", "close-foreign", 1))); w.Code != http.StatusNotFound && w.Code != http.StatusForbidden {
		t.Fatalf("closing an unknown Book returned %d", w.Code)
	}
}

func TestBookTypeAndTimezoneAreChosenOnce(t *testing.T) {
	f, s := browserServer(t)
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		return w
	}
	revise := func(key string, revision int, extra string) *httptest.ResponseRecorder {
		body := `{"book_id":"` + f.book.ID + `","idempotency_key":"` + key + `","revision":` + fmt.Sprint(revision) + `,"name":"Example Book","founder":""` + extra + `}`
		return serve(ownerPost("/api/v1/settings", f.session, body))
	}
	if w := revise("set", 0, `,"accounting_timezone":"Europe/Warsaw","profile":"business"`); w.Code != http.StatusOK {
		t.Fatalf("choosing the type and timezone returned %d %s", w.Code, w.Body.String())
	}
	for key, extra := range map[string]string{
		"timezone": `,"accounting_timezone":"America/New_York"`,
		"profile":  `,"profile":"personal"`,
		"clear":    `,"accounting_timezone":"","profile":""`,
	} {
		if w := revise(key, 1, extra); w.Code != http.StatusConflict {
			t.Fatalf("changing a chosen %s returned %d, want 409", key, w.Code)
		}
	}
	// Other settings still change.
	body := `{"book_id":"` + f.book.ID + `","idempotency_key":"rename-only","revision":1,"name":"Example Book renamed","founder":""}`
	if w := serve(ownerPost("/api/v1/settings", f.session, body)); w.Code != http.StatusOK {
		t.Fatalf("renaming returned %d %s", w.Code, w.Body.String())
	}
}
