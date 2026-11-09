package access_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

func (f *fixture) network(t *testing.T, admission server.Admission) http.Handler {
	t.Helper()
	login, e := access.NewHTTP(f.service, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if e != nil {
		t.Fatal(e)
	}
	s, e := server.New(server.Config{Origin: "https://ledger.example.com", Listen: "127.0.0.1:8080", TrustedProxies: []string{"127.0.0.1/32"}}, server.Options{Database: f.db.Runtime, Access: &server.Access{Sessions: f.service, HTTP: login}, Admission: admission})
	if e != nil {
		t.Fatal(e)
	}
	return s.Handler
}
func networkRequest(ctx context.Context, h http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://ledger.example.com"+path, strings.NewReader(body)).WithContext(ctx)
	r.URL.Scheme = ""
	r.URL.Host = ""
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "ledger.example.com")
	r.Header.Set("X-Forwarded-For", "192.0.2.10")
	r.Header.Set("Origin", "https://ledger.example.com")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOAuthHTTPConsentBindsBrowserAndOwnedBook(t *testing.T) {
	f := fresh(t)
	h := f.network(t, nil)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	other, otherSession, _ := f.owner(t, "beta")
	book := f.book(t, tenant, session)
	foreign := f.book(t, other, otherSession)
	client := f.client(t)
	verifier := access.Random()
	q := url.Values{"response_type": {"code"}, "client_id": {client.ID}, "redirect_uri": {client.Redirects[0]}, "resource": {f.service.Resource()}, "code_challenge": {access.Challenge(verifier)}, "code_challenge_method": {"S256"}, "scope": {"ledger:write"}, "state": {"bound-state"}}
	w := networkRequest(ctx, h, "GET", "/oauth/authorize?"+q.Encode(), "")
	if w.Code != http.StatusSeeOther {
		t.Fatal("authorization start failed", w.Body.String())
	}
	pending := responseCookie(t, w, "__Host-vult-oauth-request")
	sessionCookie := &http.Cookie{Name: access.SessionCookie, Value: session}
	w = networkRequest(ctx, h, "GET", w.Header().Get("Location"), "", pending)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/auth/login" {
		t.Fatal("authorization skipped human sign-in")
	}
	w = networkRequest(ctx, h, "GET", "/oauth/authorize?request="+pending.Value, "", pending, sessionCookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), book.ID) || strings.Contains(w.Body.String(), foreign.ID) {
		t.Fatal("consent page escaped ownership", w.Body.String())
	}
	csrfResponse := networkRequest(ctx, h, "GET", "/auth/session", "", sessionCookie)
	var proof struct {
		CSRF string `json:"csrf"`
	}
	if json.Unmarshal(csrfResponse.Body.Bytes(), &proof) != nil || proof.CSRF == "" {
		t.Fatal("session CSRF missing")
	}
	form := url.Values{"request": {pending.Value}, "csrf": {proof.CSRF}, "book": {tenant + "/" + book.ID}, "access": {"write"}, "decision": {"allow"}}
	bad := form.Clone()
	bad.Set("csrf", access.Random())
	w = networkRequest(ctx, h, "POST", "/oauth/authorize", bad.Encode(), pending, sessionCookie)
	if w.Code != 403 {
		t.Fatal("consent accepted a foreign CSRF proof")
	}
	bad = form.Clone()
	bad.Set("book", other+"/"+foreign.ID)
	w = networkRequest(ctx, h, "POST", "/oauth/authorize", bad.Encode(), pending, sessionCookie)
	if w.Code != 403 {
		t.Fatal("consent accepted a foreign Book")
	}
	w = networkRequest(ctx, h, "POST", "/oauth/authorize", form.Encode(), pending, sessionCookie)
	if w.Code != http.StatusSeeOther {
		t.Fatal("valid consent failed", w.Body.String())
	}
	destination, _ := url.Parse(w.Header().Get("Location"))
	if destination.Query().Get("state") != "bound-state" {
		t.Fatal("client state changed")
	}
	tokenForm := url.Values{"grant_type": {"authorization_code"}, "code": {destination.Query().Get("code")}, "client_id": {client.ID}, "resource": {f.service.Resource()}, "redirect_uri": {client.Redirects[0]}, "code_verifier": {verifier}}
	w = networkRequest(ctx, h, "POST", "/oauth/token", tokenForm.Encode())
	var tokens access.Tokens
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tokens) != nil || tokens.Access == "" {
		t.Fatal("token endpoint failed", w.Body.String())
	}
	w = networkRequest(ctx, h, "POST", "/oauth/token", tokenForm.Encode(), sessionCookie)
	if w.Code != 400 {
		t.Fatal("token endpoint accepted ambient browser credentials")
	}
}

type ledgerTransport struct {
	handler  http.Handler
	token    string
	mu       sync.Mutex
	losePost bool
}

func (t *ledgerTransport) RoundTrip(in *http.Request) (*http.Response, error) {
	r := in.Clone(in.Context())
	u := *in.URL
	u.Host = ""
	u.Scheme = ""
	r.URL = &u
	r.Host = "ledger.example.com"
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header = r.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+t.token)
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", r.Host)
	r.Header.Set("X-Forwarded-For", "192.0.2.10")
	r.Header.Set("Origin", "https://agent.example.com")
	var request struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if r.Body != nil {
		b, e := io.ReadAll(r.Body)
		if e != nil {
			return nil, e
		}
		_ = json.Unmarshal(b, &request)
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	t.mu.Lock()
	lose := t.losePost && request.Method == "tools/call" && request.Params.Name == "post_journal"
	if lose {
		t.losePost = false
	}
	t.mu.Unlock()
	if lose {
		return nil, io.ErrUnexpectedEOF
	}
	return w.Result(), nil
}
func connectRemote(t *testing.T, ctx context.Context, h http.Handler, token string, lose bool) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "example-agent", Version: "1"}, nil)
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "https://ledger.example.com/mcp", HTTPClient: &http.Client{Transport: &ledgerTransport{handler: h, token: token, losePost: lose}}}, nil)
	if e != nil {
		t.Fatal("MCP connection failed", e)
	}
	t.Cleanup(func() { session.Close() })
	return session
}
func remoteTool(t *testing.T, ctx context.Context, s *mcp.ClientSession, name string, input any) json.RawMessage {
	t.Helper()
	result, e := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
	if e != nil || result.IsError {
		t.Fatal("MCP tool failed", name, e, result)
	}
	encoded, e := json.Marshal(result.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	return encoded
}

func TestHTTPMCPPreservesExactJournalAndLostReplyOutcome(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, id := f.owner(t, "alpha")
	book := f.book(t, tenant, session)
	client := f.client(t)
	tokens := f.tokens(t, client, session, tenant, book.ID, "write", "")
	h := f.network(t, nil)
	m := connectRemote(t, ctx, h, tokens.Access, true)
	catalog, e := m.ListTools(ctx, nil)
	if e != nil {
		t.Fatal("write catalog changed", e)
	}
	expectRemoteTools(t, catalog.Tools, []string{"create_account", "execute_proposal", "get_balance", "get_book", "get_exchange_rate", "get_journal", "get_operation_proposal", "get_request_outcome", "get_trial_balance", "list_accounts", "list_closed_periods", "list_journals", "post_journal", "prepare_operation", "reverse_journal", "revise_account", "search_book", "set_book_logo"})
	for _, tool := range catalog.Tools {
		if strings.Contains(tool.Name, "owner") || strings.Contains(tool.Name, "admin") {
			t.Fatal("agent gained owner tools")
		}
	}
	var debit, credit ledger.Account
	if json.Unmarshal(remoteTool(t, ctx, m, "create_account", ledger.CreateAccount{RequestKey: "cash", Class: "asset", Code: "1000", Name: "Example cash"}), &debit) != nil {
		t.Fatal("account response invalid")
	}
	if json.Unmarshal(remoteTool(t, ctx, m, "create_account", ledger.CreateAccount{RequestKey: "equity", Class: "equity", Code: "3000", Name: "Example capital"}), &credit) != nil {
		t.Fatal("account response invalid")
	}
	post := ledger.PostJournal{RequestKey: "example-post", EconomicDate: "2026-01-01", AccountingDate: "2026-01-01", Description: "Synthetic opening", Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: "12345"}, {AccountID: credit.ID, Side: "credit", AmountMinor: "12345"}}}
	if _, e = m.CallTool(ctx, &mcp.CallToolParams{Name: "post_journal", Arguments: post}); e == nil {
		t.Fatal("lost reply was reported as confirmed")
	}
	var outcome ledger.Outcome
	if json.Unmarshal(remoteTool(t, ctx, m, "get_request_outcome", ledger.OutcomeQuery{RequestKey: post.RequestKey}), &outcome) != nil || outcome.Status != "recorded" {
		t.Fatal("lost response lost retained outcome")
	}
	retry := remoteTool(t, ctx, m, "post_journal", post)
	if !bytes.Equal(retry, outcome.Result) {
		var a, b any
		_ = json.Unmarshal(retry, &a)
		_ = json.Unmarshal(outcome.Result, &b)
		aa, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		if !bytes.Equal(aa, bb) {
			t.Fatal("retry changed committed journal")
		}
	}
	var balance ledger.Balance
	if json.Unmarshal(remoteTool(t, ctx, m, "get_balance", ledger.AccountQuery{AccountID: debit.ID}), &balance) != nil || balance.DebitPositiveMinor != "12345" {
		t.Fatal("HTTP MCP changed integer money")
	}
	tooLarge, e := m.CallTool(ctx, &mcp.CallToolParams{Name: "list_journals", Arguments: ledger.ListQuery{Limit: 21}})
	if e == nil && !tooLarge.IsError {
		t.Fatal("remote journal page limit ignored")
	}
	if e = f.operator.RevokeIdentity(ctx, id); e != nil {
		t.Fatal(e)
	}
	if _, e = m.ListTools(ctx, nil); e == nil {
		t.Fatal("cached MCP session bypassed revocation")
	}
}

type denyingAdmission struct{}

func (denyingAdmission) Check(context.Context, ledger.Scope, string) error { return nil }
func (denyingAdmission) CheckTransaction(context.Context, pgx.Tx, ledger.Scope, string, json.RawMessage) error {
	return errors.New("synthetic-private-diagnostic")
}
func TestHTTPMCPReadScopeAndSanitizedAdmissionFailure(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	book := f.book(t, tenant, session)
	client := f.client(t)
	tokens := f.tokens(t, client, session, tenant, book.ID, "read", "")
	h := f.network(t, nil)
	m := connectRemote(t, ctx, h, tokens.Access, false)
	catalog, e := m.ListTools(ctx, nil)
	if e != nil {
		t.Fatal("read catalog expanded", e)
	}
	expectRemoteTools(t, catalog.Tools, []string{"get_balance", "get_book", "get_exchange_rate", "get_journal", "get_operation_proposal", "get_request_outcome", "get_trial_balance", "list_accounts", "list_closed_periods", "list_journals", "search_book"})
	result, e := m.CallTool(ctx, &mcp.CallToolParams{Name: "create_account", Arguments: ledger.CreateAccount{RequestKey: "denied", Class: "asset", Code: "1000", Name: "Example"}})
	if e == nil && !result.IsError {
		t.Fatal("read credential gained write tool")
	}
	write := f.tokens(t, client, session, tenant, book.ID, "write", "")
	blocked := connectRemote(t, ctx, f.network(t, denyingAdmission{}), write.Access, false)
	result, e = blocked.CallTool(ctx, &mcp.CallToolParams{Name: "create_account", Arguments: ledger.CreateAccount{RequestKey: "quota", Class: "asset", Code: "1000", Name: "Example"}})
	if e != nil || !result.IsError {
		t.Fatal("transactional admission did not deny")
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "synthetic-private-diagnostic") {
		t.Fatal("extension diagnostic leaked")
	}
	scope, e := f.service.ResolveAgent(ctx, write.Access)
	if e != nil {
		t.Fatal(e)
	}
	var n int
	if e = f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.accounts WHERE book_id=$1", scope.BookID).Scan(&n); e != nil || n != 0 {
		t.Fatal("denied admission wrote financial data")
	}
}

func expectRemoteTools(t *testing.T, tools []*mcp.Tool, want []string) {
	t.Helper()
	got := make([]string, len(tools))
	for i, tool := range tools {
		got[i] = tool.Name
	}
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Fatalf("remote catalog differs: got %v, want %v", got, want)
	}
}

type countingAdmission struct{}

func (countingAdmission) Check(context.Context, ledger.Scope, string) error { return nil }
func (countingAdmission) CheckTransaction(ctx context.Context, tx pgx.Tx, s ledger.Scope, _ string, _ json.RawMessage) error {
	_, e := tx.Exec(ctx, "INSERT INTO extension.usage(tenant_id,used) VALUES($1,1) ON CONFLICT(tenant_id) DO UPDATE SET used=extension.usage.used+1", s.TenantID)
	return e
}

func (countingAdmission) CheckOwnerTransaction(ctx context.Context, tx pgx.Tx, tenant, operation string, _ json.RawMessage) error {
	if operation != "create_book" {
		return nil
	}
	_, e := tx.Exec(ctx, "INSERT INTO extension.usage(tenant_id,used) VALUES($1,1) ON CONFLICT(tenant_id) DO UPDATE SET used=extension.usage.used+1", tenant)
	return e
}
func TestTransactionAdmissionDoesNotChargeRetriesOrRolledBackWrites(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	book := f.book(t, tenant, session)
	client := f.client(t)
	tokens := f.tokens(t, client, session, tenant, book.ID, "write", "")
	if _, e := f.db.Admin.Exec(ctx, "CREATE SCHEMA extension;CREATE TABLE extension.usage(tenant_id uuid PRIMARY KEY,used bigint NOT NULL)"); e != nil {
		t.Fatal(e)
	}
	role := pgx.Identifier{f.db.Role}.Sanitize()
	if _, e := f.db.Admin.Exec(ctx, "GRANT USAGE ON SCHEMA extension TO "+role+";GRANT SELECT,INSERT,UPDATE ON extension.usage TO "+role); e != nil {
		t.Fatal(e)
	}
	m := connectRemote(t, ctx, f.network(t, countingAdmission{}), tokens.Access, false)
	q := ledger.CreateAccount{RequestKey: "one", Class: "asset", Code: "1000", Name: "Example"}
	first := remoteTool(t, ctx, m, "create_account", q)
	second := remoteTool(t, ctx, m, "create_account", q)
	if !bytes.Equal(first, second) {
		t.Fatal("charged retry changed result")
	}
	q.RequestKey = "two"
	result, e := m.CallTool(ctx, &mcp.CallToolParams{Name: "create_account", Arguments: q})
	if e != nil || !result.IsError {
		t.Fatal("duplicate code unexpectedly posted")
	}
	var used int
	if e = f.db.Admin.QueryRow(ctx, "SELECT used FROM extension.usage WHERE tenant_id=$1", tenant).Scan(&used); e != nil || used != 1 {
		t.Fatal("admission charged a retry or rolled-back write", e, used)
	}
}

func TestPublicDiscoveryCORSNeverAuthenticatesCookies(t *testing.T) {
	f := fresh(t)
	h := f.network(t, nil)
	ctx := f.db.Context
	for _, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource/mcp"} {
		w := networkRequest(ctx, h, "GET", path, "")
		if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("credential-free discovery unavailable")
		}
	}
	w := networkRequest(ctx, h, "POST", "/mcp", `{}`)
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatal("MCP discovery challenge missing")
	}
	w = networkRequest(ctx, h, "POST", "/mcp", `{}`, &http.Cookie{Name: access.SessionCookie, Value: access.Random()})
	if w.Code != 403 {
		t.Fatal("MCP accepted ambient session cookie")
	}
}

func TestRegistrationMetadataCannotGrantAuthority(t *testing.T) {
	f := fresh(t)
	h := f.network(t, nil)
	register := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "/oauth/register", strings.NewReader(body)).WithContext(f.db.Context)
		r.Host = "ledger.example.com"
		r.RemoteAddr = "127.0.0.1:50000"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", r.Host)
		r.Header.Set("X-Forwarded-For", "192.0.2.10")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	body := `{"client_name":"Example native client","redirect_uris":["http://127.0.0.1:53921/callback/example"],"grant_types":["authorization_code","refresh_token"],"token_endpoint_auth_method":"none","response_types":["code"],"scope":"ledger:read ledger:write owner:admin","application_type":"native","client_id":"chosen","tenant_id":"00000000-0000-0000-0000-000000000001","jwks_uri":"http://127.0.0.1:1/private","extension":{"value":true}}`
	w := register(body)
	var client access.Client
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &client) != nil || client.ID == "" || client.ID == "chosen" || client.AuthMethod != "none" || len(client.Redirects) != 1 {
		t.Fatal("extensible native registration failed")
	}
	for _, table := range []string{"public.tenants", "public.connections", "access.owners", "access.oauth_grants", "access.oauth_credentials"} {
		var count int
		if f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM "+table).Scan(&count) != nil || count != 0 {
			t.Fatal("registration metadata granted authority", table)
		}
	}
	for _, invalid := range []string{
		`{"redirect_uris":["http://127.0.0.1:53921/callback"],"scope":"read","scope":"write"}`,
		`{"redirect_uris":["http://127.0.0.1:53921/callback"],"extension":{"value":1,"value":2}}`,
		`{"redirect_uris":["https://example.com/callback#fragment"],"application_type":"native"}`,
		`{"redirect_uris":["http://127.0.0.1:53921/callback"],"token_endpoint_auth_method":"client_secret_post"}`,
		`{"redirect_uris":["http://127.0.0.1:53921/callback"],"grant_types":["client_credentials"]}`,
		`{"redirect_uris":"http://127.0.0.1:53921/callback","application_type":"native"}`,
		`{"redirect_uris":["http://127.0.0.1:53921/callback"],"extension":"\ud800"}`,
	} {
		if w := register(invalid); w.Code != 400 {
			t.Fatal("invalid registration accepted")
		}
	}
	w = register(`{"redirect_uris":["http://127.0.0.1:53921/callback"],"application_type":"native"}`)
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &client) != nil || client.Name != "Agent" {
		t.Fatal("optional client name prevented registration")
	}
}
