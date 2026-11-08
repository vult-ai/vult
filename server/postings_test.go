package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestBrowserPostingsRequireLiveOwnerAndCompleteBookScope(t *testing.T) {
	f, s := browserServer(t)
	ctx := f.db.Context
	var connection ledger.Connection
	var otherBook ledger.Book
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		connection, err = owner.CreateConnection(ctx, f.book.ID, "Example writer", "write")
		if err != nil {
			return err
		}
		otherBook, err = owner.CreateBook(ctx, "Example separate Book", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	foreignIdentity := access.Identity{Issuer: f.identity.Issuer, Subject: "separate-owner", AuthenticatedAt: time.Now().UTC()}
	foreignTenant, err := f.operator.BindOwner(ctx, "", foreignIdentity)
	if err != nil {
		t.Fatal(err)
	}
	foreignSession, err := f.sessions.IssueSession(ctx, foreignIdentity)
	if err != nil {
		t.Fatal(err)
	}
	var foreignBook ledger.Book
	if err = f.sessions.WithOwner(ctx, foreignSession, foreignTenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		foreignBook, err = owner.CreateBook(ctx, "Example foreign Book", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "write"}
	call := func(method string, input any, result any) {
		t.Helper()
		data, _ := json.Marshal(input)
		out, err := f.db.Store.Call(ctx, scope, method, data)
		if err != nil || json.Unmarshal(out, result) != nil {
			t.Fatal("journal fixture failed", err)
		}
	}
	var debit, credit ledger.Account
	call("create_account", ledger.CreateAccount{RequestKey: "debit", Class: "asset", Code: "1000", Name: "Example bank"}, &debit)
	call("create_account", ledger.CreateAccount{RequestKey: "credit", Class: "equity", Code: "3000", Name: "Example equity"}, &credit)
	var entry ledger.Entry
	call("post_journal", ledger.PostJournal{RequestKey: "funding", EconomicDate: "2026-01-01", AccountingDate: "2026-01-01", Description: "Example funding", Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: "9007199254740993"}, {AccountID: credit.ID, Side: "credit", AmountMinor: "9007199254740993"}}}, &entry)
	path := func(book, ids string) string {
		return "/api/v1/transaction-postings?" + url.Values{"book_id": {book}, "ids": {ids}}.Encode()
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path(f.book.ID, entry.ID), f.session))
	var result ledger.ScopedPostings
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Entries) != 1 || result.Entries[0].Lines[0].AccountID != debit.ID || result.Entries[0].Lines[0].AmountMinor != "9007199254740993" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session posting projection differs", w.Code)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{path(f.book.ID, entry.ID+","+ledger.ID()), http.StatusNotFound},
		{path(otherBook.ID, entry.ID), http.StatusNotFound},
		{path(foreignBook.ID, entry.ID), http.StatusNotFound},
		{path(ledger.ID(), entry.ID), http.StatusNotFound},
		{path(f.book.ID, entry.ID+","+entry.ID), http.StatusBadRequest},
		{path(f.book.ID, "invalid"), http.StatusBadRequest},
		{path(f.book.ID, entry.ID) + "&ids=" + entry.ID, http.StatusBadRequest},
		{path(f.book.ID, entry.ID) + "&tenant_id=" + f.tenant, http.StatusBadRequest},
		{path(f.book.ID, strings.Repeat("a", 8200)), http.StatusBadRequest},
	} {
		w = httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, tc.path, f.session))
		if w.Code != tc.status || strings.Contains(w.Body.String(), entry.ID) || strings.Contains(w.Body.String(), "9007199254740993") {
			t.Fatal("rejected selection leaked partial postings", w.Code, tc.status)
		}
	}
	r := browserRequest(http.MethodGet, path(f.book.ID, entry.ID), f.session)
	r.Header.Set("Authorization", "Bearer example")
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("bearer token reached owner projection", w.Code)
	}
	if err := f.sessions.EndSession(ctx, f.session); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path(f.book.ID, entry.ID), f.session))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("ended session retained posting access", w.Code)
	}
}
