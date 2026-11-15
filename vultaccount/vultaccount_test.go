package vultaccount

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// memory is a Store that keeps what it is given, as a sealed store would.
type memory struct {
	mu    sync.Mutex
	saved []byte
	saves int
}

func (m *memory) Load(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saved, nil
}
func (m *memory) Save(_ context.Context, b []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved, m.saves = append([]byte(nil), b...), m.saves+1
	return nil
}
func (m *memory) Clear(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = nil
	return nil
}

// accounts stands in for the account service: it approves every sign-in
// for one synthetic person and checks the exchange as a real one must.
type accounts struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	// codes are one-time codes waiting to be exchanged, with the challenge
	// and return address each was issued for.
	codes map[string][2]string
	// credential is the one this Vult currently holds; "" once revoked.
	credential string
	issued     int
	refreshes  int
	down       bool
}

func newAccounts(t *testing.T) *accounts {
	a := &accounts{t: t, codes: map[string][2]string{}}
	a.server = httptest.NewServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.server.Close)
	return a
}

// approve plays the person: it follows the page the Vult sent them to,
// signs in and approves, and returns the address they are sent back to.
func (a *accounts) approve(page string) (state, code string) {
	a.t.Helper()
	u, err := url.Parse(page)
	if err != nil || !strings.HasPrefix(page, a.server.URL+"/connect/authorize?") {
		a.t.Fatalf("the person was sent to %q", page)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("client_id") != ClientID || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		a.t.Fatalf("the sign-in asked for %v", q)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.issued++
	code = "code-" + q.Get("state")[:8]
	a.codes[code] = [2]string{q.Get("code_challenge"), q.Get("redirect_uri")}
	return q.Get("state"), code
}

func (a *accounts) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.down {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("client_id") != ClientID {
		http.Error(w, "{}", http.StatusBadRequest)
		return
	}
	refuse := func() { http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest) }
	switch {
	case r.URL.Path == "/connect/revoke":
		if r.Form.Get("token") == a.credential {
			a.credential = ""
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	case r.URL.Path == "/connect/token" && r.Form.Get("grant_type") == "authorization_code":
		issued, ok := a.codes[r.Form.Get("code")]
		delete(a.codes, r.Form.Get("code"))
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != issued[0] || r.Form.Get("redirect_uri") != issued[1] {
			refuse()
			return
		}
		a.credential = "credential-1"
		_ = json.NewEncoder(w).Encode(map[string]any{"refresh_token": a.credential, "access_token": "token-0", "expires_in": 300,
			"account_id": "acct-example", "account_label": "person@example.com", "agent_service": "https://agent.example.com/"})
	case r.URL.Path == "/connect/token" && r.Form.Get("grant_type") == "refresh_token":
		if a.credential == "" || r.Form.Get("refresh_token") != a.credential {
			refuse()
			return
		}
		a.refreshes++
		// The credential is replaced each time it is used.
		a.credential = "credential-" + string(rune('1'+a.refreshes))
		_ = json.NewEncoder(w).Encode(map[string]any{"refresh_token": a.credential, "access_token": "token-" + string(rune('0'+a.refreshes)), "expires_in": 300})
	default:
		http.Error(w, "{}", http.StatusNotFound)
	}
}

func connected(t *testing.T) (*Client, *accounts, *memory, *time.Time) {
	t.Helper()
	a, store := newAccounts(t), &memory{}
	c, err := Open(Config{Issuer: a.server.URL, Redirect: "https://vult.example.test/auth/vult-account/callback", Name: "Example Mac"}, store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return c, a, store, &now
}

// A person signs in on the account service's page; the Vult ends up with a
// credential of its own and knows which account it is connected to.
func TestAVultConnectsToAnAccount(t *testing.T) {
	c, a, store, _ := connected(t)
	ctx := context.Background()
	if _, err := c.Status(ctx); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("a new Vult's status = %v", err)
	}
	page, err := c.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, "vult_name=Example+Mac") {
		t.Fatalf("the account page isn't told this Vult's name: %s", page)
	}
	state, code := a.approve(page)
	got, err := c.Finish(ctx, state, code)
	if err != nil || got.Account != "acct-example" || got.Label != "person@example.com" || got.AgentService != "https://agent.example.com" || got.ConnectedAt.IsZero() {
		t.Fatalf("connected = %+v, %v", got, err)
	}
	if status, err := c.Status(ctx); err != nil || status != got {
		t.Fatalf("status = %+v, %v", status, err)
	}
	// What is kept is the Vult's own credential; the person's sign-in
	// never reached it.
	if !strings.Contains(string(store.saved), "credential-1") || strings.Contains(string(store.saved), "token-0") {
		t.Fatalf("kept = %s", store.saved)
	}
	// The same return can't be used again.
	if _, err := c.Finish(ctx, state, code); !errors.Is(err, ErrRefused) {
		t.Fatalf("a sign-in finished twice = %v", err)
	}
}

// A return this Vult didn't start, one that arrives too late, or one whose
// code was made for another sign-in connects nothing.
func TestOnlyASignInThisVultStartedIsFinished(t *testing.T) {
	c, a, store, now := connected(t)
	ctx := context.Background()
	if _, err := c.Finish(ctx, "a-state-from-somewhere-else", "code"); !errors.Is(err, ErrRefused) {
		t.Fatalf("a return nobody started = %v", err)
	}
	page, _ := c.Begin()
	state, code := a.approve(page)
	*now = now.Add(11 * time.Minute)
	if _, err := c.Finish(ctx, state, code); !errors.Is(err, ErrRefused) {
		t.Fatalf("a return ten minutes late = %v", err)
	}
	// Two sign-ins started here: the code from one doesn't finish the
	// other, because each has its own verifier.
	first, _ := c.Begin()
	second, _ := c.Begin()
	_, firstCode := a.approve(first)
	secondState, _ := a.approve(second)
	if _, err := c.Finish(ctx, secondState, firstCode); !errors.Is(err, ErrRefused) {
		t.Fatalf("a code from another sign-in = %v", err)
	}
	if store.saves != 0 {
		t.Fatal("something was kept from a sign-in that didn't finish")
	}
	// Starting sign-ins over and over keeps only a few waiting.
	for i := 0; i < 50; i++ {
		if _, err := c.Begin(); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.began) > maxPending {
		t.Fatalf("%d sign-ins are waiting", len(c.began))
	}
}

// A connected Vult presents short-lived tokens, asks for a new one only
// when the last runs out, and keeps a replaced credential.
func TestTokensAreShortLivedAndTheCredentialIsReplaced(t *testing.T) {
	c, a, store, now := connected(t)
	ctx := context.Background()
	page, _ := c.Begin()
	state, code := a.approve(page)
	if _, err := c.Finish(ctx, state, code); err != nil {
		t.Fatal(err)
	}
	token, connection, err := c.Token(ctx)
	if err != nil || token != "token-0" || connection.Account != "acct-example" || a.refreshes != 0 {
		t.Fatalf("the first token = %q %+v %v after %d refreshes", token, connection, err, a.refreshes)
	}
	*now = now.Add(6 * time.Minute)
	if token, _, err = c.Token(ctx); err != nil || token != "token-1" || a.refreshes != 1 {
		t.Fatalf("a token after the first ran out = %q %v after %d refreshes", token, err, a.refreshes)
	}
	if token, _, _ = c.Token(ctx); token != "token-1" || a.refreshes != 1 {
		t.Fatal("a token still good was asked for again")
	}
	if !strings.Contains(string(store.saved), "credential-2") {
		t.Fatalf("the replaced credential wasn't kept: %s", store.saved)
	}
	// A Vult started again has no token in hand and uses what it kept.
	again, err := Open(c.config, store)
	if err != nil {
		t.Fatal(err)
	}
	again.now = c.now
	if token, _, err = again.Token(ctx); err != nil || token != "token-2" {
		t.Fatalf("after a restart = %q %v", token, err)
	}
}

// When the account service is down a connection waits; when it says no, the
// Vult forgets the connection and the person signs in again.
func TestARefusedCredentialEndsTheConnection(t *testing.T) {
	c, a, store, now := connected(t)
	ctx := context.Background()
	page, _ := c.Begin()
	state, code := a.approve(page)
	if _, err := c.Finish(ctx, state, code); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(6 * time.Minute)
	a.mu.Lock()
	a.down = true
	a.mu.Unlock()
	if _, _, err := c.Token(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("with the account service down = %v", err)
	}
	if _, err := c.Status(ctx); err != nil {
		t.Fatal("an outage ended the connection", err)
	}
	a.mu.Lock()
	a.down, a.credential = false, ""
	a.mu.Unlock()
	if _, _, err := c.Token(ctx); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("with the credential removed on the account page = %v", err)
	}
	if store.saved != nil {
		t.Fatal("a refused credential was kept")
	}
}

// Disconnecting forgets the credential here at once and has the account
// service stop accepting it, and works when the account service is down.
func TestDisconnectingForgetsTheCredential(t *testing.T) {
	c, a, store, _ := connected(t)
	ctx := context.Background()
	page, _ := c.Begin()
	state, code := a.approve(page)
	if _, err := c.Finish(ctx, state, code); err != nil {
		t.Fatal(err)
	}
	if err := c.Disconnect(ctx); err != nil || store.saved != nil || a.credential != "" {
		t.Fatalf("disconnect = %v, kept %q, the account service still accepts %q", err, store.saved, a.credential)
	}
	if _, _, err := c.Token(ctx); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("a token after disconnecting = %v", err)
	}
	if err := c.Disconnect(ctx); err != nil {
		t.Fatal("disconnecting twice", err)
	}
	page, _ = c.Begin()
	state, code = a.approve(page)
	if _, err := c.Finish(ctx, state, code); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.down = true
	a.mu.Unlock()
	if err := c.Disconnect(ctx); err != nil || store.saved != nil {
		t.Fatalf("disconnect with the account service down = %v, kept %q", err, store.saved)
	}
}

// The account service is reached over HTTPS, or on this computer; where it
// says the agent is must be the same.
func TestOnlySafeAddressesAreUsed(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://account.example.com": true, "http://127.0.0.1:9000": true, "http://localhost:9000": true,
		"http://account.example.com": false, "https://user:pw@account.example.com": false, "https://account.example.com/?x=1": false, "ftp://account.example.com": false, "": false,
	} {
		_, err := Open(Config{Issuer: raw, Redirect: "https://vult.example.test/callback"}, &memory{})
		if (err == nil) != ok {
			t.Errorf("%q: opened = %v", raw, err == nil)
		}
	}
	if _, err := Open(Config{Issuer: "https://account.example.com", Redirect: "https://vult.example.test/callback"}, nil); err == nil {
		t.Error("opened with nowhere to keep the credential")
	}
}
