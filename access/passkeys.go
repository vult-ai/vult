package access

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/go-webauthn/webauthn/webauthn"
)

// Passkeys verifies a configured built-in owner's credentials. It never accepts
// tenant membership or an identity selected by an unauthenticated caller.
type Passkeys struct {
	sessions   *Service
	protocol   *passkeyProtocol
	subject    string
	identityID string
}

type PasskeyChallenge struct {
	Token   string
	Options json.RawMessage
}

func NewPasskeys(ctx context.Context, s *Service, subject string) (*Passkeys, error) {
	if s == nil || len(subject) == 0 || len(subject) > 80 {
		return nil, ErrDenied
	}
	protocol, err := newPasskeyProtocol(s.origin)
	if err != nil {
		return nil, err
	}
	p := &Passkeys{sessions: s, protocol: protocol, subject: subject}
	user, _, err := p.profile(ctx)
	if err != nil {
		return nil, err
	}
	p.identityID = user.ID
	return p, nil
}

func (p *Passkeys) profile(ctx context.Context) (passkeyUser, int64, error) {
	user := passkeyUser{Name: p.subject}
	var epoch int64
	var credentials []byte
	if err := p.sessions.auth.QueryRow(ctx, "SELECT identity_id::text,epoch,credentials FROM access.passkey_profile($1,$2)", p.subject, p.sessions.origin).Scan(&user.ID, &epoch, &credentials); err != nil {
		return passkeyUser{}, 0, ErrDenied
	}
	if json.Unmarshal(credentials, &user.Credentials) != nil || !validPasskeyUser(user) || p.identityID != "" && user.ID != p.identityID {
		return passkeyUser{}, 0, ErrDenied
	}
	return user, epoch, nil
}

// HasPasskey reports whether the owner has registered a passkey.
func (p *Passkeys) HasPasskey(ctx context.Context) bool {
	user, _, err := p.profile(ctx)
	return err == nil && len(user.Credentials) > 0
}

func (p *Passkeys) BeginRegistration(ctx context.Context, setup, browser string) (PasskeyChallenge, error) {
	if !validToken(setup) || !validToken(browser) {
		return PasskeyChallenge{}, ErrDenied
	}
	return p.begin(ctx, "registration", setup, browser)
}
func (p *Passkeys) BeginLogin(ctx context.Context, browser string) (PasskeyChallenge, error) {
	if !validToken(browser) {
		return PasskeyChallenge{}, ErrDenied
	}
	return p.begin(ctx, "login", "", browser)
}
func (p *Passkeys) begin(ctx context.Context, intent, setup, browser string) (PasskeyChallenge, error) {
	user, epoch, err := p.profile(ctx)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	var options json.RawMessage
	var session webauthn.SessionData
	if intent == "registration" {
		options, session, err = p.protocol.beginRegistration(user)
	} else {
		options, session, err = p.protocol.beginLogin(user)
	}
	if err != nil {
		return PasskeyChallenge{}, err
	}
	data, err := json.Marshal(session)
	if err != nil {
		return PasskeyChallenge{}, ErrDenied
	}
	token := Random()
	var setupHash *string
	if setup != "" {
		hash := Hash(setup)
		setupHash = &hash
	}
	if _, err := p.sessions.auth.Exec(ctx, "SELECT access.begin_passkey($1,$2,$3,$4,$5,$6,$7,$8)", p.subject, p.sessions.origin, epoch, Hash(token), Hash(browser), intent, setupHash, data); err != nil {
		return PasskeyChallenge{}, ErrDenied
	}
	return PasskeyChallenge{Token: token, Options: options}, nil
}

func (p *Passkeys) FinishRegistration(ctx context.Context, token, browser string, response json.RawMessage) (string, error) {
	return p.finish(ctx, "registration", token, browser, response)
}
func (p *Passkeys) FinishLogin(ctx context.Context, token, browser string, response json.RawMessage) (string, error) {
	return p.finish(ctx, "login", token, browser, response)
}
func (p *Passkeys) finish(ctx context.Context, intent, token, browser string, response json.RawMessage) (string, error) {
	if !validToken(token) || !validToken(browser) || len(response) == 0 || len(response) > passkeyResponseLimit {
		return "", ErrDenied
	}
	var user passkeyUser
	user.Name = p.subject
	var epoch int64
	var data, credentials []byte
	// Claim in its own committed statement. An invalid assertion consumes the
	// ceremony too; signature validation cannot reopen a nonce by rolling back.
	if err := p.sessions.auth.QueryRow(ctx, "SELECT identity_id::text,epoch,ceremony,credentials FROM access.take_passkey($1,$2,$3,$4)", Hash(token), p.sessions.origin, Hash(browser), intent).Scan(&user.ID, &epoch, &data, &credentials); err != nil {
		return "", ErrDenied
	}
	var session webauthn.SessionData
	if user.ID != p.identityID || json.Unmarshal(data, &session) != nil || json.Unmarshal(credentials, &user.Credentials) != nil {
		return "", ErrDenied
	}
	var updated webauthn.Credential
	var err error
	if intent == "registration" {
		updated, err = p.protocol.registration(user, session, response)
	} else {
		updated, err = p.protocol.login(user, session, response)
	}
	if err != nil {
		return "", err
	}
	var prior []byte
	if intent == "login" {
		for _, credential := range user.Credentials {
			if bytes.Equal(credential.ID, updated.ID) {
				prior, err = json.Marshal(credential)
				break
			}
		}
		if err != nil || len(prior) == 0 {
			return "", ErrDenied
		}
	}
	encoded, err := json.Marshal(updated)
	if err != nil || len(encoded) > 32768 {
		return "", ErrDenied
	}
	raw := Random()
	keyID := base64.RawURLEncoding.EncodeToString(updated.ID)
	var who string
	if err := p.sessions.auth.QueryRow(ctx, "SELECT access.finish_passkey($1,$2,$3,$4,$5,$6,$7,$8,$9)::text", Hash(token), p.sessions.origin, Hash(browser), intent, keyID, prior, encoded, Hash(raw), Hash(keyID)).Scan(&who); err != nil || who != user.ID {
		return "", ErrDenied
	}
	return raw, nil
}
