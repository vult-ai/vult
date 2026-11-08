package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/boombuler/barcode/qr"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// ErrInvalidCode means an authenticator code did not match. It counts toward
// the owner's attempt budget.
var ErrInvalidCode = errors.New("invalid authenticator code")

// ErrTooManyAttempts means the owner's attempt budget is spent for now.
var ErrTooManyAttempts = errors.New("too many authenticator attempts")

// LocalAuthenticator lets the owner of a local installation sign in with a
// six-digit code from an authenticator app, as well as or instead of a passkey.
// Secrets are sealed with a key the database never sees.
type LocalAuthenticator struct {
	sessions   *Service
	subject    string
	identityID string
	key        []byte
	keyID      string
}

// AuthenticatorEnrollment is shown once, to the owner's browser, so they can
// add the account to their authenticator app.
type AuthenticatorEnrollment struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
	QR     string `json:"qr"`
}

// NewLocalAuthenticator adds authenticator sign-in for the passkey owner p.
func NewLocalAuthenticator(p *Passkeys, key []byte) (*LocalAuthenticator, error) {
	if p == nil || p.identityID == "" || len(key) != 32 {
		return nil, ErrDenied
	}
	k := append([]byte(nil), key...)
	return &LocalAuthenticator{sessions: p.sessions, subject: p.subject, identityID: p.identityID, key: k, keyID: Hash(string(k))}, nil
}

// Ready reports whether the owner has an authenticator to sign in with.
func (a *LocalAuthenticator) Ready(ctx context.Context) bool {
	var ready bool
	err := a.sessions.auth.QueryRow(ctx, "SELECT access.local_authenticator_ready($1,$2,$3)", a.subject, a.sessions.origin, a.keyID).Scan(&ready)
	return err == nil && ready
}

// BeginEnrollment creates a new secret for the browser to confirm. It needs the
// one-use setup code that would also register a first passkey, or a session
// signed in within the last five minutes.
func (a *LocalAuthenticator) BeginEnrollment(ctx context.Context, setup, session, browser string) (AuthenticatorEnrollment, error) {
	if !validToken(browser) || (setup == "") == (session == "") || setup != "" && !validToken(setup) || session != "" && !validToken(session) {
		return AuthenticatorEnrollment{}, ErrDenied
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Vult", AccountName: "This Mac", SecretSize: 20, Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return AuthenticatorEnrollment{}, ErrDenied
	}
	sealed, err := sealAuthenticator(a.key, a.identityID, key.Secret())
	if err != nil {
		return AuthenticatorEnrollment{}, err
	}
	var setupHash, sessionHash *string
	if setup != "" {
		h := Hash(setup)
		setupHash = &h
	} else {
		h := Hash(session)
		sessionHash = &h
	}
	if _, err := a.sessions.auth.Exec(ctx, "SELECT access.begin_local_authenticator($1,$2,$3,$4,$5,$6,$7)", a.subject, a.sessions.origin, setupHash, sessionHash, Hash(browser), sealed, a.keyID); err != nil {
		return AuthenticatorEnrollment{}, ErrDenied
	}
	image, err := qrSVG(key.URL())
	if err != nil {
		return AuthenticatorEnrollment{}, ErrDenied
	}
	return AuthenticatorEnrollment{Secret: key.Secret(), URI: key.URL(), QR: image}, nil
}

// FinishEnrollment checks the first code from the new authenticator and, when
// it matches, makes it the owner's and returns a new session.
func (a *LocalAuthenticator) FinishEnrollment(ctx context.Context, code, browser string) (string, error) {
	if !validToken(browser) {
		return "", ErrDenied
	}
	return a.check(ctx, "enroll", code, Hash(browser))
}

// SignIn checks a code from the owner's authenticator and returns a session.
func (a *LocalAuthenticator) SignIn(ctx context.Context, code string) (string, error) {
	return a.check(ctx, "login", code, "")
}

func (a *LocalAuthenticator) check(ctx context.Context, purpose, code, browser string) (string, error) {
	code = strings.TrimSpace(code)
	var browserHash *string
	if browser != "" {
		browserHash = &browser
	}
	var allowed bool
	var who, keyID *string
	var epoch *int64
	var sealed []byte
	// The attempt is spent in its own committed statement, before the code is
	// checked, so a wrong code cannot be retried for free.
	if err := a.sessions.auth.QueryRow(ctx, "SELECT allowed,identity_id::text,epoch,sealed,key_id FROM access.reserve_local_authenticator($1,$2,$3,$4)", a.subject, a.sessions.origin, purpose, browserHash).Scan(&allowed, &who, &epoch, &sealed, &keyID); err != nil {
		return "", ErrDenied
	}
	if !allowed {
		return "", ErrTooManyAttempts
	}
	if who == nil || *who != a.identityID || epoch == nil || keyID == nil || *keyID != a.keyID {
		return "", ErrDenied
	}
	secret, err := openAuthenticator(a.key, a.identityID, sealed)
	if err != nil {
		return "", ErrDenied
	}
	step := matchingStep(code, secret, time.Now())
	if step < 0 {
		return "", ErrInvalidCode
	}
	raw := Random()
	var signedIn string
	if err := a.sessions.auth.QueryRow(ctx, "SELECT access.finish_local_authenticator($1,$2,$3,$4,$5,$6,$7,$8)::text", a.identityID, a.sessions.origin, purpose, *epoch, browserHash, sealed, step, Hash(raw)).Scan(&signedIn); err != nil || signedIn != a.identityID {
		// A code already used, or one raced by another sign-in, is a wrong code.
		return "", ErrInvalidCode
	}
	return raw, nil
}

// matchingStep returns the 30-second step a six-digit code belongs to, allowing
// one step either side for clock drift, or -1 when it matches none.
func matchingStep(code, secret string, now time.Time) int64 {
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return -1
	}
	step := int64(-1)
	current := now.Unix() / 30
	for delta := int64(-1); delta <= 1; delta++ {
		candidate := current + delta
		ok, err := totp.ValidateCustom(code, secret, time.Unix(candidate*30, 0), totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && ok {
			step = candidate
		}
	}
	return step
}

// qrSVG draws a QR code as SVG markup with a four-module quiet zone.
func qrSVG(content string) (string, error) {
	code, err := qr.Encode(content, qr.M, qr.Auto)
	if err != nil {
		return "", err
	}
	bounds := code.Bounds()
	size := bounds.Dx() + 8
	var path strings.Builder
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if r, _, _, _ := code.At(x, y).RGBA(); r == 0 {
				fmt.Fprintf(&path, "M%d %dh1v1h-1z", x-bounds.Min.X+4, y-bounds.Min.Y+4)
			}
		}
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code for your authenticator app"><rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="%s"/></svg>`, size, size, size, size, path.String()), nil
}
