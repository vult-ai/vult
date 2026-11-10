package access

import (
	"context"
	"crypto/rand"
	"encoding/base64"

	"github.com/jackc/pgx/v5"
)

// newRecoveryCodes makes eight single-use recovery codes and their hashes,
// the only form in which they're stored.
func newRecoveryCodes() (codes, hashes []string, err error) {
	for i := 0; i < 8; i++ {
		buffer := make([]byte, 16)
		if _, err := rand.Read(buffer); err != nil {
			return nil, nil, ErrDenied
		}
		code := base64.RawURLEncoding.EncodeToString(buffer)
		codes, hashes = append(codes, code), append(hashes, Hash(code))
	}
	return codes, hashes, nil
}

func addRecoveryCodes(ctx context.Context, tx pgx.Tx, identity string) ([]string, error) {
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	for _, hash := range hashes {
		if _, err := tx.Exec(ctx, "INSERT INTO access.recovery_codes(identity_id,code_hash) VALUES($1,$2)", identity, hash); err != nil {
			return nil, ErrDenied
		}
	}
	return codes, nil
}

// IssueRecoveryCodes gives the owner the recovery codes owed after setting up
// sign-in, an operator's reset or recovery with a code: a new set, replacing
// every earlier code. Only a session that signed in within the last few
// minutes receives them, once. It returns no codes when none are owed.
func (p *Passkeys) IssueRecoveryCodes(ctx context.Context, session string) ([]string, error) {
	if !validToken(session) {
		return nil, ErrDenied
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return nil, err
	}
	var issued bool
	if err := p.sessions.auth.QueryRow(ctx, "SELECT access.issue_recovery_codes($1,$2,$3)", Hash(session), p.sessions.origin, hashes).Scan(&issued); err != nil {
		return nil, ErrDenied
	}
	if !issued {
		return nil, nil
	}
	return codes, nil
}
