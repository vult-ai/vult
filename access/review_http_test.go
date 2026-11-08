package access_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

func TestPasskeySignInReturnsToExactReviewWithoutApproving(t *testing.T) {
	f := fresh(t)
	p, setup, authenticator, oldSession, _ := enrollPasskey(t, f)
	book := f.book(t, setup.TenantID, oldSession)
	var connection ledger.Connection
	if err := f.service.WithOwner(f.db.Context, oldSession, setup.TenantID, true, func(owner ledger.TenantAdmin) error {
		var err error
		connection, err = owner.CreateConnection(f.db.Context, book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: setup.TenantID, BookID: book.ID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "write"}
	a, b := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
	args, err := json.Marshal(ledger.PostJournal{RequestKey: "accepted-post", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example funding", Lines: []ledger.PostLine{
		{AccountID: a.ID, Side: "debit", AmountMinor: "1250"}, {AccountID: b.ID, Side: "credit", AmountMinor: "1250"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var proposal ledger.OperationProposal
	if json.Unmarshal(f.db.Call(t, scope, "prepare_operation", ledger.PrepareOperation{RequestKey: "prepare", Operation: "post_journal", Arguments: args}), &proposal) != nil {
		t.Fatal("invalid review fixture")
	}
	h, err := access.NewHTTP(f.service, nil, nil, nil, access.HTTPOptions{Passkeys: p})
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.New(server.Config{Origin: "https://ledger.example.com", Listen: "127.0.0.1:8080", TrustedProxies: []string{"127.0.0.1/32"}}, server.Options{
		Database: f.db.Runtime, Access: &server.Access{Sessions: f.service, HTTP: h},
		Web: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Example review</title>")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := networkRequest(f.db.Context, s.Handler, http.MethodGet, proposal.Approval.Href, "")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/auth/login?operation="+proposal.Approval.Operation {
		t.Fatal("missing sign-in did not retain exact review", w.Code)
	}
	w = networkRequest(f.db.Context, s.Handler, http.MethodGet, w.Header().Get("Location"), "")
	if w.Code != http.StatusOK || w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("review sign-in unavailable", w.Code)
	}
	nonce := responseCookie(t, w, "__Host-vult-login")
	review := responseCookie(t, w, "__Host-vult-review")
	w = passkeyPost(t, h, "/auth/passkeys/login/options", nonce, nil, struct{}{}, review)
	if w.Code != http.StatusOK {
		t.Fatal("review passkey options unavailable", w.Code)
	}
	ceremony := responseCookie(t, w, "__Host-vult-passkey")
	proof := authenticator.assertion(t, access.PasskeyChallenge{Token: ceremony.Value, Options: json.RawMessage(w.Body.Bytes())}, "https://ledger.example.com", "ledger.example.com", 1, nil)
	w = passkeyPost(t, h, "/auth/passkeys/login/verify", nonce, ceremony, map[string]any{"credential": proof}, review, &http.Cookie{Name: access.SessionCookie, Value: oldSession})
	var outcome struct {
		SignedIn bool   `json:"signed_in"`
		Continue string `json:"continue"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &outcome) != nil || !outcome.SignedIn || outcome.Continue != proposal.Approval.Href {
		t.Fatal("passkey replaced the exact review destination", w.Code)
	}
	if cleared := responseCookie(t, w, "__Host-vult-review"); cleared.MaxAge >= 0 {
		t.Fatal("sign-in retained its consumed review continuation")
	}
	session := responseCookie(t, w, access.SessionCookie)
	if _, err := f.service.Identity(f.db.Context, oldSession); !errors.Is(err, access.ErrDenied) {
		t.Fatal("review sign-in retained replaced session", err)
	}
	w = networkRequest(f.db.Context, s.Handler, http.MethodGet, outcome.Continue, "", session)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("new sign-in could not reach exact review", w.Code)
	}
	w = networkRequest(f.db.Context, s.Handler, http.MethodGet, "/api/v1/agent-owner/review?operation="+proposal.Approval.Operation, "", session)
	var state ledger.ProposalState
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Proposal.ID != proposal.ID || state.Proposal.SHA256 != proposal.SHA256 || state.Status != "approval_required" {
		t.Fatal("sign-in did not preserve an undecided exact operation", w.Code)
	}
	var rows int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT (SELECT count(*) FROM public.entries)+(SELECT count(*) FROM public.operation_decisions)+(SELECT count(*) FROM public.operation_consumptions)").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("sign-in decided or executed the operation", rows, err)
	}
}
