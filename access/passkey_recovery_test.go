package access_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestPasskeyRecoveryCodeRequiresReplacementAndPreservesBooks(t *testing.T) {
	f := fresh(t)
	p, setup, original, session, browser := enrollPasskey(t, f)
	f.book(t, setup.TenantID, session)
	before, err := f.db.Owner.Snapshot(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := p.BeginLogin(f.db.Context, browser)
	if err != nil {
		t.Fatal(err)
	}
	token, err := p.Recover(f.db.Context, setup.RecoveryCodes[0], browser)
	if err != nil || token == "" {
		t.Fatal("saved recovery code was not accepted", err)
	}
	if _, err := f.service.Identity(f.db.Context, session); !errors.Is(err, access.ErrDenied) {
		t.Fatal("recovery retained the earlier sign-in", err)
	}
	proof := original.assertion(t, pending, "https://ledger.example.com", "ledger.example.com", 1, nil)
	if _, err := p.FinishLogin(f.db.Context, pending.Token, browser, proof); !errors.Is(err, access.ErrDenied) {
		t.Fatal("recovery retained a pending assertion", err)
	}
	if _, err := p.Recover(f.db.Context, setup.RecoveryCodes[0], browser); !errors.Is(err, access.ErrDenied) {
		t.Fatal("recovery code was reusable", err)
	}
	if _, err := p.BeginRegistration(f.db.Context, token, access.Random()); !errors.Is(err, access.ErrDenied) {
		t.Fatal("replacement setup transferred to another browser", err)
	}
	if _, err := p.BeginLogin(f.db.Context, browser); !errors.Is(err, access.ErrDenied) {
		t.Fatal("recovery alone enabled passkey sign-in", err)
	}
	challenge, err := p.BeginRegistration(f.db.Context, token, browser)
	if err != nil {
		t.Fatal(err)
	}
	replacement := newPasskeyAuthenticator(t)
	newSession, err := p.FinishRegistration(f.db.Context, challenge.Token, browser, replacement.registration(t, challenge, "https://ledger.example.com", "ledger.example.com"))
	if err != nil {
		t.Fatal("replacement passkey did not complete recovery", err)
	}
	if _, err := f.service.Identity(f.db.Context, newSession); err != nil {
		t.Fatal("replacement sign-in unavailable", err)
	}
	after, err := f.db.Owner.Snapshot(f.db.Context)
	if err != nil || before.Format != after.Format || before.SchemaSHA256 != after.SchemaSHA256 || !reflect.DeepEqual(before.Tables, after.Tables) {
		t.Fatal("recovery changed financial records", err)
	}
	var remaining, recovered, active int
	if err := f.db.Admin.QueryRow(f.db.Context, `SELECT
 (SELECT count(*) FROM access.recovery_codes),
 (SELECT count(*) FROM access.passkey_events WHERE kind='recovery'),
 (SELECT count(*) FROM access.passkey_credentials WHERE revoked_at IS NULL)`).Scan(&remaining, &recovered, &active); err != nil || remaining != 7 || recovered != 1 || active != 1 {
		t.Fatal("recovery did not retain unused codes and its audit event", err)
	}
}

func TestPasskeyRecoveryDurableAttemptBudget(t *testing.T) {
	f := fresh(t)
	p, setup, _, session, browser := enrollPasskey(t, f)
	// Registration already consumed one attempt in the same shared window.
	for i := 0; i < 31; i++ {
		if _, err := p.Recover(f.db.Context, "invalid", browser); !errors.Is(err, access.ErrDenied) {
			t.Fatal("invalid recovery code accepted", err)
		}
	}
	if _, err := p.Recover(f.db.Context, setup.RecoveryCodes[0], browser); !errors.Is(err, access.ErrDenied) {
		t.Fatal("recovery bypassed its exhausted attempt budget", err)
	}
	var attempts, codes int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT attempt_count,(SELECT count(*) FROM access.recovery_codes) FROM access.passkey_profiles").Scan(&attempts, &codes); err != nil || attempts != 32 || codes != 8 {
		t.Fatal("failed attempts rolled back or consumed valid codes", err)
	}
	if _, err := f.service.Identity(f.db.Context, session); err != nil {
		t.Fatal("failed recovery revoked the owner", err)
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.passkey_profiles SET attempt_until=clock_timestamp()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	token, err := p.Recover(f.db.Context, setup.RecoveryCodes[0], browser)
	if err != nil {
		t.Fatal("valid recovery stayed locked after the attempt window", err)
	}
	if _, err := p.BeginRegistration(f.db.Context, token, browser); err != nil {
		t.Fatal("valid recovery left no budget for its replacement credential", err)
	}
	for _, pool := range []*pgxpool.Pool{f.db.Runtime, f.owners} {
		var accepted bool
		err := pool.QueryRow(f.db.Context, "SELECT access.recover_passkey($1,$2,$3,$4,$5,$6)", "example-owner", "https://ledger.example.com", ledger.ID(), access.Hash(setup.RecoveryCodes[1]), access.Hash(access.Random()), access.Hash(browser)).Scan(&accepted)
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatal("non-authentication role was not denied recovery permission", err)
		}
	}
}

func TestPasskeyRecoverySerializesConcurrentCodeUse(t *testing.T) {
	f := fresh(t)
	_, setup, _, _, browser := enrollPasskey(t, f)
	config := f.auth.Config().Copy()
	config.MaxConns, config.MinConns = 2, 2
	pool, err := pgxpool.NewWithConfig(f.db.Context, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s, err := access.New(pool, f.owners, "https://ledger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	p, err := access.NewPasskeys(f.db.Context, s, "example-owner")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := p.Recover(f.db.Context, setup.RecoveryCodes[0], browser)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, access.ErrDenied) {
			t.Fatal(err)
		}
	}
	var liveSetups, events int
	if err := f.db.Admin.QueryRow(f.db.Context, `SELECT
 (SELECT count(*) FROM access.passkey_setups WHERE consumed_at IS NULL),
 (SELECT count(*) FROM access.passkey_events WHERE kind='recovery')`).Scan(&liveSetups, &events); err != nil || winners != 1 || liveSetups != 1 || events != 1 {
		t.Fatal("concurrent recovery did not have one accepted outcome", winners, err)
	}
}

func TestPasskeyRecoveryHTTPRequiresBrowserBinding(t *testing.T) {
	f := fresh(t)
	p, setup, _, session, _ := enrollPasskey(t, f)
	h, err := access.NewHTTP(f.service, nil, nil, nil, access.HTTPOptions{Passkeys: p})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://ledger.example.com/auth/recover", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "revokes all existing passkeys") || w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("recovery page omitted its effect or safe headers", w.Code)
	}
	nonce := responseCookie(t, w, "__Host-vult-login")
	w = passkeyPost(t, h, "/auth/passkeys/recover", nil, nil, map[string]string{"code": setup.RecoveryCodes[0]})
	if w.Code != http.StatusForbidden {
		t.Fatal("unbound recovery request reached the owner", w.Code)
	}
	w = passkeyPost(t, h, "/auth/passkeys/recover", nonce, nil, map[string]string{"code": setup.RecoveryCodes[0], "tenant_id": setup.TenantID})
	if w.Code != http.StatusBadRequest {
		t.Fatal("recovery accepted caller-selected membership", w.Code)
	}
	w = passkeyPost(t, h, "/auth/passkeys/recover", nonce, nil, map[string]string{"code": setup.RecoveryCodes[0]})
	var response struct {
		Setup string `json:"setup_token"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Setup) != 43 || strings.Contains(w.Body.String(), setup.RecoveryCodes[0]) || w.Header().Get("Location") != "" {
		t.Fatal("browser recovery leaked its code or omitted replacement setup", w.Code)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == access.SessionCookie && cookie.Value != "" {
			t.Fatal("recovery code directly signed the owner in")
		}
	}
	if _, err := f.service.Identity(f.db.Context, session); !errors.Is(err, access.ErrDenied) {
		t.Fatal("HTTP recovery did not revoke prior sign-in", err)
	}
	w = passkeyPost(t, h, "/auth/passkeys/register/options", nonce, nil, map[string]string{"setup_token": response.Setup})
	if w.Code != http.StatusOK {
		t.Fatal("recovery browser could not start replacement registration", w.Code)
	}
}

func TestOwnersReceiveTheirRecoveryCodesOnceInTheBrowser(t *testing.T) {
	f := fresh(t)
	setup, err := f.operator.InitializePasskeys(f.db.Context, "", "example-owner", "https://ledger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	// A code an operator was given by an earlier version of setup.
	operatorCode := access.Random()
	if _, err := f.db.Admin.Exec(f.db.Context, "INSERT INTO access.recovery_codes(identity_id,code_hash) SELECT identity_id,$1 FROM access.passkey_profiles", access.Hash(operatorCode)); err != nil {
		t.Fatal(err)
	}
	p, err := access.NewPasskeys(f.db.Context, f.service, "example-owner")
	if err != nil {
		t.Fatal(err)
	}
	browser := access.Random()
	register := func(token string) string {
		t.Helper()
		challenge, err := p.BeginRegistration(f.db.Context, token, browser)
		if err != nil {
			t.Fatal(err)
		}
		session, err := p.FinishRegistration(f.db.Context, challenge.Token, browser, newPasskeyAuthenticator(t).registration(t, challenge, "https://ledger.example.com", "ledger.example.com"))
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	session := register(setup.Token)
	codes, err := p.IssueRecoveryCodes(f.db.Context, session)
	if err != nil || len(codes) != 8 {
		t.Fatal("the owner did not receive recovery codes after setup", err)
	}
	if again, err := p.IssueRecoveryCodes(f.db.Context, session); err != nil || len(again) != 0 {
		t.Fatal("recovery codes were shown twice", len(again), err)
	}
	// The owner's codes replace every earlier one.
	if _, err := p.Recover(f.db.Context, operatorCode, browser); !errors.Is(err, access.ErrDenied) {
		t.Fatal("a code given to the operator still works", err)
	}

	// Recovering with a code gives the owner a fresh set for the new passkey,
	// and only a sign-in from the last few minutes can receive it.
	token, err := p.Recover(f.db.Context, codes[0], browser)
	if err != nil {
		t.Fatal(err)
	}
	session = register(token)
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '11 minutes' WHERE token_hash=$1", access.Hash(session)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.IssueRecoveryCodes(f.db.Context, session); !errors.Is(err, access.ErrDenied) {
		t.Fatal("an older sign-in received recovery codes", err)
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp() WHERE token_hash=$1", access.Hash(session)); err != nil {
		t.Fatal(err)
	}
	fresh, err := p.IssueRecoveryCodes(f.db.Context, session)
	if err != nil || len(fresh) != 8 {
		t.Fatal("recovery did not give the owner a fresh set", err)
	}
	if _, err := p.Recover(f.db.Context, codes[1], browser); !errors.Is(err, access.ErrDenied) {
		t.Fatal("a code from before recovery still works", err)
	}

	// The operator's reset is separate: it ends every sign-in and code, and the
	// owner again receives new codes in the browser.
	reset, err := f.operator.ResetPasskeys(f.db.Context, "example-owner", "https://ledger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Identity(f.db.Context, session); !errors.Is(err, access.ErrDenied) {
		t.Fatal("the operator's reset kept an earlier sign-in", err)
	}
	if _, err := p.Recover(f.db.Context, fresh[0], browser); !errors.Is(err, access.ErrDenied) {
		t.Fatal("the operator's reset kept the owner's codes", err)
	}
	if after, err := p.IssueRecoveryCodes(f.db.Context, register(reset.Token)); err != nil || len(after) != 8 {
		t.Fatal("the owner did not receive codes after the operator's reset", err)
	}
}
