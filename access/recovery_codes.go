package access

import (
	"context"
	"crypto/rand"
	"encoding/base64"

	"github.com/jackc/pgx/v5"
)

func addRecoveryCodes(ctx context.Context, tx pgx.Tx, identity string) ([]string, error) {
	codes := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		buffer := make([]byte, 16)
		if _, err := rand.Read(buffer); err != nil {
			return nil, ErrDenied
		}
		code := base64.RawURLEncoding.EncodeToString(buffer)
		if _, err := tx.Exec(ctx, "INSERT INTO access.recovery_codes(identity_id,code_hash) VALUES($1,$2)", identity, Hash(code)); err != nil {
			return nil, ErrDenied
		}
		codes = append(codes, code)
	}
	return codes, nil
}
