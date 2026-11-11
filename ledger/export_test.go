package ledger

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// SetBeforeFactCheck lets tests stand in for a migration that changes facts.
func SetBeforeFactCheck(f func(context.Context, pgx.Tx) error) func() {
	beforeFactCheck = f
	return func() { beforeFactCheck = nil }
}
