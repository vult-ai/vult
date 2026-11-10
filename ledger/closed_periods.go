package ledger

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ClosedPeriod is an accounting period with its close and reopen history.
// Closes kept from before closes were decisions remain, marked as such.
type ClosedPeriod = AccountingPeriod

func readClosedPeriods(ctx context.Context, tx pgx.Tx, scope Scope) ([]ClosedPeriod, error) {
	return readPeriods(ctx, tx, scope, "", "")
}

// ClosedPeriods lists a Book's accounting periods, oldest first, closed and
// reopened alike: a reopening stays visible.
func (a TenantAdmin) ClosedPeriods(ctx context.Context, book string) ([]ClosedPeriod, error) {
	return a.Periods(ctx, book)
}
