package ledger

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// ownerAccountMethods are the account changes the owner makes directly in
// the app. Each one is recorded exactly as an agent's would be, with the
// owner as its actor.
var ownerAccountMethods = map[string]bool{"revise_account": true, "retire_account": true, "reactivate_account": true, "apply_starter_chart": true,
	"create_payee": true, "revise_payee": true, "set_entry_payee": true,
	"mark_statement_duplicate": true, "match_statement": true, "revoke_statement_match": true, "record_statement_adjustment": true, "review_statement": true,
	"close_period": true, "reopen_period": true}

// AccountChange records one owner account change in the selected Book. The
// owner needs a fresh sign-in; the database checks it again.
func (a TenantAdmin) AccountChange(ctx context.Context, book, actor, method string, input json.RawMessage) (json.RawMessage, error) {
	if !ownerAccountMethods[method] || !ValidID(actor) {
		return nil, ErrDenied
	}
	var result json.RawMessage
	err := a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		scope.ActorID = actor
		if err := setScope(ctx, tx, scope); err != nil {
			return dbError(err)
		}
		var allowed bool
		if err := tx.QueryRow(ctx, "SELECT public.ledger_import_owner() OR access.owner_decision_allowed($1,$2)", scope.TenantID, scope.BookID).Scan(&allowed); err != nil || !allowed {
			return ErrFreshOwnerProof
		}
		if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, scope.BookID); err != nil {
			return dbError(err)
		}
		var err error
		result, err = dispatch(ctx, tx, scope, method, input, nil)
		return err
	})
	return result, err
}

// Statements lists the Book's statements for the owner, newest first.
func (a TenantAdmin) Statements(ctx context.Context, book, account string) ([]Statement, error) {
	var out []Statement
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		var err error
		out, err = readStatements(ctx, tx, scope, account)
		return err
	})
	return out, err
}

// StatementReconciliation compares one statement with the Book, with the
// suggested matches the owner can accept.
func (a TenantAdmin) StatementReconciliation(ctx context.Context, book, statement string) (StatementReconciliation, []StatementSuggestion, error) {
	var out StatementReconciliation
	var suggestions []StatementSuggestion
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		var err error
		if out, err = readStatementReconciliation(ctx, tx, scope, statement); err != nil {
			return err
		}
		suggestions, err = suggestStatementMatchesFromReport(ctx, tx, scope, out)
		return err
	})
	return out, suggestions, err
}
