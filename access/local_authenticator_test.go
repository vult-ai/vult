package access_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/loopback"
	"github.com/vult-ai/vult/ledger"
)

// recoveringAgents also hands out the new installation's backup key.
type recoveringAgents struct{ recordedAgents }

func (*recoveringAgents) TakeBackupKey(context.Context, string) (string, error) {
	return "", nil
}

type authenticatorFixture struct {
	t       *testing.T
	f       *fixture
	origin  string
	service *access.Service
	handler http.Handler
	setup   access.PasskeySetup
	auth    *access.LocalAuthenticator
}

func newAuthenticatorFixture(t *testing.T, port string) *authenticatorFixture {
	t.Helper()
	f := fresh(t)
	host := loopback.Host
	origin := "https://" + host + ":" + port
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
	key := []byte(strings.Repeat("k", 32))
	a, err := access.NewLocalAuthenticator(p, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := access.NewHTTP(s, nil, nil, nil, access.HTTPOptions{Passkeys: p, Authenticator: a}); err == nil {
		t.Fatal("a server installation accepted local authenticator sign-in")
	}
	h, err := access.NewHTTP(s, nil, nil, nil, access.HTTPOptions{Passkeys: p, Local: true, Agents: &recoveringAgents{}, Authenticator: a})
	if err != nil {
		t.Fatal(err)
	}
	return &authenticatorFixture{t: t, f: f, origin: origin, service: s, handler: h, setup: setup, auth: a}
}

func (a *authenticatorFixture) post(path, browser string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	a.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		a.t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, a.origin+path, strings.NewReader(string(data)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", a.origin)
	r.Header.Set("X-CSRF-Token", browser)
	r.AddCookie(&http.Cookie{Name: "__Host-vult-login", Value: browser})
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	return w
}

func (a *authenticatorFixture) get(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	a.t.Helper()
	r := httptest.NewRequest(http.MethodGet, a.origin+path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, r)
	return w
}

// enroll returns the new authenticator's secret after confirming it with the
// code for the step at.
func (a *authenticatorFixture) enroll(at time.Time) (string, *http.Cookie) {
	a.t.Helper()
	browser := access.Random()
	w := a.post("/auth/authenticator/enroll/options", browser, map[string]string{"setup_token": a.setup.Token})
	var enrollment struct {
		Secret, URI, QR string
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &enrollment) != nil {
		a.t.Fatal("enrolment did not start", w.Code, w.Body.String())
	}
	w = a.post("/auth/authenticator/enroll/verify", browser, map[string]string{"code": code(a.t, enrollment.Secret, at)})
	if w.Code != http.StatusOK {
		a.t.Fatal("enrolment was not confirmed", w.Code, w.Body.String())
	}
	return enrollment.Secret, sessionCookie(a.t, w)
}

func code(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	value, err := totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == access.SessionCookie && c.MaxAge > 0 {
			return c
		}
	}
	t.Fatal("no session was issued")
	return nil
}

func signInResult(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var result struct {
		SignedIn bool   `json:"signed_in"`
		Continue string `json:"continue"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.SignedIn {
		t.Fatal("sign-in was not confirmed", w.Code, w.Body.String())
	}
	return result.Continue
}

func errorCode(w *httptest.ResponseRecorder) string {
	var result struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Message == "" {
		return ""
	}
	return result.Error
}

// awayFromStepEdge waits until a 30-second step has at least five seconds left,
// so codes chosen for the steps around now stay valid for the whole test.
func awayFromStepEdge() time.Time {
	if left := 30 - time.Now().Unix()%30; left < 5 {
		time.Sleep(time.Duration(left)*time.Second + 100*time.Millisecond)
	}
	return time.Now()
}

func TestLocalAuthenticatorEnrolsSignsInAndApprovesAgents(t *testing.T) {
	a := newAuthenticatorFixture(t, "51995")
	now := awayFromStepEdge()

	if page := a.get("/auth/login"); page.Code != http.StatusOK || strings.Contains(page.Body.String(), "authenticator app") {
		t.Fatal("the sign-in page offered an authenticator before one was set up", page.Code)
	}
	setupPage := a.get("/auth/setup").Body.String()
	for _, id := range []string{"authenticator-switch", "authenticator-setup", "authenticator-qr", "authenticator-secret", "authenticator-code", "authenticator-verify"} {
		if !strings.Contains(setupPage, `id="`+id+`"`) {
			t.Fatal("the setup page does not offer an authenticator app", id)
		}
	}
	if w := a.post("/auth/authenticator/login", access.Random(), map[string]string{"code": "123456"}); w.Code != http.StatusUnauthorized {
		t.Fatal("a code was checked before an authenticator was set up", w.Code)
	}

	browser := access.Random()
	if w := a.post("/auth/authenticator/enroll/options", browser, map[string]any{}); w.Code != http.StatusUnauthorized {
		t.Fatal("enrolment started without setup authority or a recent sign-in", w.Code)
	}
	if w := a.post("/auth/authenticator/enroll/options", browser, map[string]string{"setup_token": access.Random()}); w.Code != http.StatusUnauthorized {
		t.Fatal("enrolment started with an unknown setup code", w.Code)
	}
	w := a.post("/auth/authenticator/enroll/options", browser, map[string]string{"setup_token": a.setup.Token})
	var enrollment struct {
		Secret string `json:"secret"`
		URI    string `json:"uri"`
		QR     string `json:"qr"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &enrollment) != nil {
		t.Fatal("enrolment did not start", w.Code, w.Body.String())
	}
	uri, err := url.Parse(enrollment.URI)
	if err != nil || uri.Scheme != "otpauth" || uri.Host != "totp" || uri.Query().Get("issuer") != "Vult" || uri.Query().Get("secret") != enrollment.Secret || len(enrollment.Secret) != 32 {
		t.Fatal("the authenticator link is not a Vult time-based code", enrollment.URI)
	}
	if !strings.HasPrefix(enrollment.QR, "<svg ") || !strings.HasSuffix(enrollment.QR, "</svg>") || !strings.Contains(enrollment.QR, "<path") {
		t.Fatal("the QR code is not SVG markup")
	}

	first := code(t, enrollment.Secret, now.Add(-30*time.Second))
	if w := a.post("/auth/authenticator/enroll/verify", access.Random(), map[string]string{"code": first}); w.Code != http.StatusUnauthorized {
		t.Fatal("another browser confirmed this enrolment", w.Code)
	}
	if w := a.post("/auth/authenticator/enroll/verify", browser, map[string]string{"code": "000000"}); w.Code != http.StatusBadRequest || errorCode(w) != "invalid_code" {
		t.Fatal("a wrong enrolment code was not refused plainly", w.Code, w.Body.String())
	}
	w = a.post("/auth/authenticator/enroll/verify", browser, map[string]string{"code": first})
	if next := signInResult(t, w); next != "/auth/recovery-codes" {
		t.Fatal("a new authenticator did not lead to the recovery codes", next)
	}
	enrolled := sessionCookie(t, w)
	if id, err := a.service.Identity(a.f.db.Context, enrolled.Value); err != nil || time.Since(id.AuthenticatedAt) > time.Minute {
		t.Fatal("enrolment did not sign the owner in", err)
	}
	if w := a.post("/auth/authenticator/enroll/options", access.Random(), map[string]string{"setup_token": a.setup.Token}); w.Code != http.StatusUnauthorized {
		t.Fatal("a used setup code started another enrolment", w.Code)
	}
	if !a.auth.Ready(a.f.db.Context) {
		t.Fatal("the enrolled authenticator is not ready")
	}
	if page := a.get("/auth/login"); !strings.Contains(page.Body.String(), "Use an authenticator app") || !strings.Contains(page.Body.String(), `id="authenticator-code"`) {
		t.Fatal("the sign-in page did not offer the authenticator")
	}

	// A code already used to enrol, or to sign in, never works again.
	if w := a.post("/auth/authenticator/login", access.Random(), map[string]string{"code": first}); w.Code != http.StatusBadRequest || errorCode(w) != "invalid_code" {
		t.Fatal("a replayed code signed in", w.Code, w.Body.String())
	}
	second := code(t, enrollment.Secret, now)
	if next := signInResult(t, a.post("/auth/authenticator/login", access.Random(), map[string]string{"code": second})); next != "/" {
		t.Fatal("sign-in continued somewhere unexpected", next)
	}
	if w := a.post("/auth/authenticator/login", access.Random(), map[string]string{"code": second}); w.Code != http.StatusBadRequest {
		t.Fatal("a replayed sign-in code signed in", w.Code)
	}
	if w := a.post("/auth/authenticator/login", access.Random(), map[string]string{"code": "12345"}); w.Code != http.StatusBadRequest || errorCode(w) != "invalid_code" {
		t.Fatal("a malformed code was not refused plainly", w.Code)
	}

	// A sign-in older than five minutes cannot approve an agent. A new code
	// signs in, comes back to the same request and can approve it.
	if err := a.service.WithOwner(a.f.db.Context, enrolled.Value, a.setup.TenantID, true, func(o ledger.TenantAdmin) error {
		_, err := o.CreateBook(a.f.db.Context, "Example Book", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.f.db.Admin.Exec(a.f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(enrolled.Value)); err != nil {
		t.Fatal(err)
	}
	reader, err := access.NewOwnerDocumentReader(a.f.db.Context, a.f.db.Admin, a.origin)
	if err != nil {
		t.Fatal(err)
	}
	if reader.RecentOwner(a.f.db.Context, enrolled.Value) == nil {
		t.Fatal("a stale sign-in counted as recent")
	}
	request := strings.Repeat("A", 43)
	stale := a.get("/auth/agent?request="+request, enrolled)
	if stale.Code != http.StatusSeeOther || stale.Header().Get("Location") != "/auth/login" {
		t.Fatal("a stale sign-in reached the approval page", stale.Code)
	}
	var resume *http.Cookie
	for _, c := range stale.Result().Cookies() {
		if c.Value == request {
			resume = c
		}
	}
	if resume == nil {
		t.Fatal("the agent request was not kept for after sign-in")
	}
	w = a.post("/auth/authenticator/login", access.Random(), map[string]string{"code": code(t, enrollment.Secret, now.Add(30*time.Second))}, enrolled, resume)
	if next := signInResult(t, w); next != "/auth/agent?request="+request {
		t.Fatal("sign-in did not return to the agent request", next)
	}
	signedIn := sessionCookie(t, w)
	if reader.RecentOwner(a.f.db.Context, signedIn.Value) != nil {
		t.Fatal("an authenticator sign-in did not count as recent")
	}
	if page := a.get("/auth/agent?request="+request, signedIn); page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Connect Claude Code") {
		t.Fatal("an authenticator sign-in could not open the approval page", page.Code)
	}
	if _, err := a.service.Identity(a.f.db.Context, enrolled.Value); err == nil {
		t.Fatal("signing in again kept the previous session")
	}
}

func TestLocalAuthenticatorLocksOutAndIsRetiredByReset(t *testing.T) {
	a := newAuthenticatorFixture(t, "51994")
	now := awayFromStepEdge()
	secret, session := a.enroll(now.Add(-30 * time.Second))

	// A signed-in owner may replace the authenticator while the sign-in is
	// recent; a stale sign-in may not.
	if w := a.post("/auth/authenticator/enroll/options", access.Random(), map[string]any{}, session); w.Code != http.StatusOK {
		t.Fatal("a recently signed-in owner could not add an authenticator", w.Code, w.Body.String())
	}
	if _, err := a.f.db.Admin.Exec(a.f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(session.Value)); err != nil {
		t.Fatal(err)
	}
	if w := a.post("/auth/authenticator/enroll/options", access.Random(), map[string]any{}, session); w.Code != http.StatusUnauthorized {
		t.Fatal("a stale sign-in added an authenticator", w.Code)
	}

	for i := 0; i < 8; i++ {
		if w := a.post("/auth/authenticator/login", access.Random(), map[string]string{"code": "000000"}); w.Code != http.StatusBadRequest || errorCode(w) != "invalid_code" {
			t.Fatal("a wrong code was not refused plainly", i, w.Code)
		}
	}
	right := code(t, secret, now)
	if w := a.post("/auth/authenticator/login", access.Random(), map[string]string{"code": right}); w.Code != http.StatusTooManyRequests || errorCode(w) != "too_many_attempts" {
		t.Fatal("the right code was accepted after too many wrong ones", w.Code, w.Body.String())
	}
	if _, err := a.f.db.Admin.Exec(a.f.db.Context, "UPDATE access.local_authenticators SET window_expires_at=clock_timestamp()"); err != nil {
		t.Fatal(err)
	}
	signInResult(t, a.post("/auth/authenticator/login", access.Random(), map[string]string{"code": right}))

	// Resetting the owner's sign-in retires the authenticator with the rest.
	if _, err := a.f.operator.ResetPasskeys(a.f.db.Context, "example-owner", a.origin); err != nil {
		t.Fatal(err)
	}
	if a.auth.Ready(a.f.db.Context) {
		t.Fatal("a reset left the authenticator ready")
	}
	if page := a.get("/auth/login"); strings.Contains(page.Body.String(), "authenticator app") {
		t.Fatal("the sign-in page offered a retired authenticator")
	}
	if w := a.post("/auth/authenticator/login", access.Random(), map[string]string{"code": code(t, secret, now.Add(30*time.Second))}); w.Code == http.StatusOK {
		t.Fatal("a retired authenticator signed in")
	}
}
