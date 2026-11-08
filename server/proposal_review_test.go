package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/operationref"
	"github.com/vult-ai/vult/ledger"
)

func TestBrowserProposalReviewAddressPreservesScopeAndDoesNotDecide(t *testing.T) {
	f, s, _, p := browserProposal(t)
	ref := operationref.Encode(f.book.ID, p.ID)
	if p.Approval.Flow != "page" || p.Approval.Operation != ref || p.Approval.Href != "/auth/approve?operation="+ref {
		t.Fatal("proposal lacks its exact browser review address")
	}
	read := func(path, session string, change func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		r := browserRequest(http.MethodGet, path, session)
		if session == "" {
			r.Header.Del("Cookie")
		}
		if change != nil {
			change(r)
		}
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		return w
	}
	w := read(p.Approval.Href, "", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/auth/login?operation="+ref || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("review sign-in lost its exact proposal", w.Code)
	}
	w = read(p.Approval.Href, f.session, nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("owned review page unavailable or cacheable", w.Code)
	}
	w = read("/api/v1/agent-owner/review?operation="+ref, f.session, nil)
	var detail ledger.ProposalState
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail.Version != 1 || detail.Proposal.ID != p.ID || detail.Proposal.SHA256 != p.SHA256 || detail.Status != "approval_required" {
		t.Fatal("review address substituted its exact operation", w.Code)
	}
	for _, tc := range []struct {
		path   string
		change func(*http.Request)
		status int
	}{
		{p.Approval.Href, func(r *http.Request) { r.Header.Set("Authorization", "Bearer example") }, http.StatusForbidden},
		{p.Approval.Href, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: access.SessionCookie, Value: f.session}) }, http.StatusForbidden},
		{p.Approval.Href, func(r *http.Request) { r.Method = http.MethodPost }, http.StatusForbidden},
		{p.Approval.Href + "&operation=" + ref, nil, http.StatusBadRequest},
		{p.Approval.Href + "&return_to=https://example.com", nil, http.StatusBadRequest},
		{"/auth/approve?operation=bad", nil, http.StatusBadRequest},
		{"/auth/approve?operation=" + operationref.Encode(ledger.ID(), p.ID), nil, http.StatusNotFound},
		{"/auth/approve?operation=" + operationref.Encode(f.book.ID, ledger.ID()), nil, http.StatusNotFound},
		{"/api/v1/agent-owner/review?operation=" + ref, func(r *http.Request) { r.Header.Set("Authorization", "Bearer example") }, http.StatusForbidden},
		{"/api/v1/agent-owner/review?operation=" + operationref.Encode(ledger.ID(), p.ID), nil, http.StatusNotFound},
	} {
		if w := read(tc.path, f.session, tc.change); w.Code != tc.status {
			t.Fatal("review scope or address denial changed", w.Code, tc.status)
		}
	}
	foreign := f.identity
	foreign.Subject = "another-owner"
	if _, err := f.operator.BindOwner(f.db.Context, "", foreign); err != nil {
		t.Fatal(err)
	}
	foreignSession, err := f.sessions.IssueSession(f.db.Context, foreign)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.Approval.Href, "/api/v1/agent-owner/review?operation=" + ref} {
		if w := read(path, foreignSession, nil); w.Code != http.StatusNotFound {
			t.Fatal("review address conferred another owner's authority", w.Code)
		}
	}
	var rows int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT (SELECT count(*) FROM public.entries)+(SELECT count(*) FROM public.operation_decisions)+(SELECT count(*) FROM public.operation_consumptions)").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("reviewing or signing in decided or posted", rows, err)
	}
}
