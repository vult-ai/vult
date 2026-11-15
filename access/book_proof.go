package access

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/ledger"
)

// ErrProofLimit is too many passkey requests for Book deletion in an hour.
var ErrProofLimit = errors.New("too many passkey requests")

// BeginBookProof asks the owner's passkey to approve one action on one Book:
// "delete" or "confirm". The assertion it answers is made for this browser
// session, this Book and this action, and is used once. A session cookie
// alone never stands in for it.
func (p *Passkeys) BeginBookProof(ctx context.Context, session, tenant, book, purpose string) (PasskeyChallenge, error) {
	if p == nil || !validToken(session) || !ledger.ValidID(tenant) || !ledger.ValidID(book) || purpose != "delete" && purpose != "confirm" {
		return PasskeyChallenge{}, ErrDenied
	}
	user, epoch, err := p.profile(ctx)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	options, ceremony, err := p.protocol.beginLogin(user)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	data, err := json.Marshal(ceremony)
	if err != nil {
		return PasskeyChallenge{}, ErrDenied
	}
	token := Random()
	if _, err := p.sessions.auth.Exec(ctx, "SELECT access.begin_book_proof($1,$2,$3,$4,$5,$6,$7,$8,$9)", p.subject, p.sessions.origin, epoch, Hash(token), Hash(session), tenant, book, purpose, data); err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "VB429" {
			return PasskeyChallenge{}, ErrProofLimit
		}
		return PasskeyChallenge{}, ErrDenied
	}
	return PasskeyChallenge{Token: token, Options: options}, nil
}

// FinishBookProof verifies the passkey's answer, with user verification,
// against the challenge made for this session. The challenge is spent
// whether or not the answer is valid. On success the proof may be spent,
// within two minutes, by the action and Book it was made for.
func (p *Passkeys) FinishBookProof(ctx context.Context, token, session string, response json.RawMessage) error {
	if p == nil || !validToken(token) || !validToken(session) || len(response) == 0 || len(response) > passkeyResponseLimit {
		return ErrDenied
	}
	user := passkeyUser{Name: p.subject}
	var epoch int64
	var data, credentials []byte
	if err := p.sessions.auth.QueryRow(ctx, "SELECT identity_id::text,epoch,ceremony,credentials FROM access.take_book_proof($1,$2,$3)", Hash(token), p.sessions.origin, Hash(session)).Scan(&user.ID, &epoch, &data, &credentials); err != nil {
		return ErrDenied
	}
	var ceremony webauthn.SessionData
	if user.ID != p.identityID || json.Unmarshal(data, &ceremony) != nil || json.Unmarshal(credentials, &user.Credentials) != nil {
		return ErrDenied
	}
	updated, err := p.protocol.login(user, ceremony, response)
	if err != nil {
		return ErrDenied
	}
	var prior []byte
	for _, credential := range user.Credentials {
		if bytes.Equal(credential.ID, updated.ID) {
			prior, err = json.Marshal(credential)
			break
		}
	}
	if err != nil || len(prior) == 0 {
		return ErrDenied
	}
	encoded, err := json.Marshal(updated)
	if err != nil || len(encoded) > 32768 {
		return ErrDenied
	}
	keyID := base64.RawURLEncoding.EncodeToString(updated.ID)
	var who string
	if err := p.sessions.auth.QueryRow(ctx, "SELECT access.finish_book_proof($1,$2,$3,$4,$5,$6,$7)::text", Hash(token), p.sessions.origin, Hash(session), keyID, prior, encoded, Hash(keyID)).Scan(&who); err != nil || who != user.ID {
		return ErrDenied
	}
	return nil
}

// BookProofs is the owner's passkey for approving Book deletion, or nil
// when this Vult signs its owner in without one.
func (h *HTTP) BookProofs() *Passkeys {
	if h == nil {
		return nil
	}
	return h.passkeys
}

// BeginReportProof asks the owner's passkey to approve one action on one
// expense report: submitting it, approving it, or both at once. The
// assertion is made for this session, this business Book, this action and
// bound, which names the report, its revision and its snapshot, and is
// used once.
func (p *Passkeys) BeginReportProof(ctx context.Context, session, tenant, book, purpose, bound string) (PasskeyChallenge, error) {
	if p == nil || !validToken(session) || !ledger.ValidID(tenant) || !ledger.ValidID(book) || purpose != "submit" && purpose != "approve" && purpose != "submit_approve" ||
		bound == "" || len(bound) > 200 {
		return PasskeyChallenge{}, ErrDenied
	}
	user, epoch, err := p.profile(ctx)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	options, ceremony, err := p.protocol.beginLogin(user)
	if err != nil {
		return PasskeyChallenge{}, err
	}
	data, err := json.Marshal(ceremony)
	if err != nil {
		return PasskeyChallenge{}, ErrDenied
	}
	token := Random()
	if _, err := p.sessions.auth.Exec(ctx, "SELECT access.begin_report_proof($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)", p.subject, p.sessions.origin, epoch, Hash(token), Hash(session), tenant, book, purpose, bound, data); err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "VB429" {
			return PasskeyChallenge{}, ErrProofLimit
		}
		return PasskeyChallenge{}, ErrDenied
	}
	return PasskeyChallenge{Token: token, Options: options}, nil
}
