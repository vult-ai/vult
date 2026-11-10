package access_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/loopback"
	"github.com/vult-ai/vult/ledger"
)

type recordedAgents struct {
	decided []string
}

func (a *recordedAgents) AgentClient(_ context.Context, session, request string) (string, error) {
	if request == strings.Repeat("x", 43) {
		return "", errors.New("ended")
	}
	return "Claude Code", nil
}

func (a *recordedAgents) DecideAgent(_ context.Context, session, request, book, access string, deny bool) error {
	a.decided = append(a.decided, strings.Join([]string{request, book, access, map[bool]string{true: "deny", false: "allow"}[deny]}, " "))
	return nil
}

func TestLocalAgentApprovalNeedsARecentPasskeySignIn(t *testing.T) {
	f := fresh(t)
	host := loopback.Host
	origin := "https://" + host + ":51997"
	s, err := access.New(f.auth, f.owners, origin)
	if err != nil {
		t.Fatal(err)
	}
	setup, err := f.operator.InitializePasskeys(f.db.Context, "", "example-owner", origin)
	if err != nil {
		t.Fatal(err)
	}
	p, err := access.NewPasskeys(f.db.Context, s, "example-owner")
	if err != nil {
		t.Fatal(err)
	}
	agents := &recordedAgents{}
	if _, err := access.NewHTTP(s, nil, nil, nil, access.HTTPOptions{Passkeys: p, Agents: agents}); err == nil {
		t.Fatal("a server installation accepted local agent approvals")
	}
	h, err := access.NewHTTP(s, nil, nil, nil, access.HTTPOptions{Passkeys: p, Local: true, Agents: agents})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.IssueSession(f.db.Context, access.Identity{Issuer: access.BuiltinIssuer, Subject: "example-owner", AuthenticatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	var book ledger.Book
	if err := s.WithOwner(f.db.Context, raw, setup.TenantID, true, func(o ledger.TenantAdmin) error {
		var err error
		book, err = o.CreateBook(f.db.Context, "Example Book", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request := strings.Repeat("A", 43)
	do := func(method, path string, form url.Values, session string, originHeader bool) *httptest.ResponseRecorder {
		t.Helper()
		var r *http.Request
		if form != nil {
			r = httptest.NewRequest(method, origin+path, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r = httptest.NewRequest(method, origin+path, nil)
		}
		if originHeader {
			r.Header.Set("Origin", origin)
		}
		if session != "" {
			r.AddCookie(&http.Cookie{Name: access.SessionCookie, Value: session})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	page := do(http.MethodGet, "/auth/agent?request="+request, nil, raw, false)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Connect Claude Code") || !strings.Contains(page.Body.String(), "Example Book") {
		t.Fatal("the approval page did not show the agent and the Book", page.Code, page.Body.String())
	}
	match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
	if len(match) != 2 {
		t.Fatal("the approval form carries no form token")
	}
	allow := url.Values{"csrf": {match[1]}, "request": {request}, "book": {book.ID}, "access": {"write"}, "decision": {"allow"}}
	forged := url.Values{"csrf": {"forged"}, "request": {request}, "book": {book.ID}, "access": {"write"}, "decision": {"allow"}}
	if w := do(http.MethodPost, "/auth/agent", allow, raw, false); w.Code != http.StatusForbidden {
		t.Fatal("an approval without the page's origin was accepted", w.Code)
	}
	if w := do(http.MethodPost, "/auth/agent", forged, raw, true); w.Code != http.StatusForbidden {
		t.Fatal("an approval with a forged form token was accepted", w.Code)
	}
	if len(agents.decided) != 0 {
		t.Fatal("a refused form reached the ledger service", agents.decided)
	}
	if w := do(http.MethodPost, "/auth/agent", allow, raw, true); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatal("the owner could not approve the agent", w.Code, w.Body.String())
	}
	if len(agents.decided) != 1 || agents.decided[0] != request+" "+book.ID+" write allow" {
		t.Fatal("the approval did not reach the ledger service as chosen", agents.decided)
	}
	if w := do(http.MethodGet, "/auth/agent?request="+strings.Repeat("x", 43), nil, raw, false); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "Ask the agent to try again") {
		t.Fatal("an ended request did not explain what to do", w.Code)
	}

	// A sign-in from ten minutes ago must sign in with the passkey again and
	// come straight back to the same request.
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(raw)); err != nil {
		t.Fatal(err)
	}
	stale := do(http.MethodGet, "/auth/agent?request="+request, nil, raw, false)
	if stale.Code != http.StatusSeeOther || stale.Header().Get("Location") != "/auth/login" {
		t.Fatal("a stale sign-in reached the approval page", stale.Code, stale.Header().Get("Location"))
	}
	var resume *http.Cookie
	for _, c := range stale.Result().Cookies() {
		if c.Value == request {
			resume = c
		}
	}
	if resume == nil || !resume.HttpOnly || !resume.Secure {
		t.Fatal("the request was not kept for after sign-in")
	}
	if w := do(http.MethodPost, "/auth/agent", allow, raw, true); w.Code != http.StatusSeeOther || len(agents.decided) != 1 {
		t.Fatal("a stale sign-in approved an agent", w.Code, agents.decided)
	}
	if w := do(http.MethodGet, "/auth/agent?request=short", nil, raw, false); w.Code != http.StatusBadRequest {
		t.Fatal("a malformed request was accepted", w.Code)
	}
}
