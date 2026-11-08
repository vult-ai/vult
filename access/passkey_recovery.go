package access

import "context"

// Recover consumes one recovery code and returns browser-bound setup authority,
// never a session. Existing credentials and sign-ins are invalidated atomically;
// sign-in requires registering and verifying a replacement passkey.
func (p *Passkeys) Recover(ctx context.Context, code, browser string) (string, error) {
	if len(code) == 0 || len(code) > 128 || !validToken(browser) {
		return "", ErrDenied
	}
	setup := Random()
	var accepted bool
	if err := p.sessions.auth.QueryRow(ctx, "SELECT access.recover_passkey($1,$2,$3,$4,$5,$6)", p.subject, p.sessions.origin, p.identityID, Hash(code), Hash(setup), Hash(browser)).Scan(&accepted); err != nil || !accepted {
		return "", ErrDenied
	}
	return setup, nil
}
