package access

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/ledger"
)

// DecideOperation retains a human decision. An approval posts the exact
// prepared entry in the same transaction. Exact retries recover their decision
// and posted entry without another fresh sign-in.
func (s *Service) DecideOperation(ctx context.Context, session, tenant string, q ledger.DecideOperation, admit ledger.DecisionAdmission) (ledger.OperationDecisionResult, error) {
	var decision ledger.OperationDecisionResult
	err := s.withOwner(ctx, session, tenant, true, false, "", func(_ pgx.Tx, owner ledger.TenantAdmin, who string) error {
		var err error
		decision, err = owner.DecideOperation(ctx, who, q, admit)
		return err
	})
	if errors.Is(err, ledger.ErrDenied) {
		return decision, ErrDenied
	}
	return decision, err
}
