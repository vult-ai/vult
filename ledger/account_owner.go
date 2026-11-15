package ledger

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5"
)

// ownerAccountMethods are the account changes the owner makes directly in
// the app. Each one is recorded exactly as an agent's would be, with the
// owner as its actor.
var ownerAccountMethods = map[string]bool{"create_account": true, "revise_account": true, "retire_account": true, "reactivate_account": true, "apply_starter_chart": true,
	"create_payee": true, "revise_payee": true, "set_entry_payee": true,
	"mark_statement_duplicate": true, "match_statement": true, "revoke_statement_match": true, "record_statement_adjustment": true, "review_statement": true,
	"close_period": true, "reopen_period": true,
	"link_bank_activity": true, "unlink_bank_activity": true, "record_bank_transfer": true,
	"link_bank_account": true, "disconnect_bank": true, "pause_bank_connection": true, "resume_bank_connection": true, "mark_bank_duplicate": true,
	// Undo is a reversing entry, linked to the original, never a deletion.
	"reverse_journal": true, "record_by_rule": true}

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
		// Undoing half of a pair reverses its partner too, in the other
		// linked Book, in this transaction.
		var partner *EntryPair
		var undo ReverseJournal
		if method == "reverse_journal" && json.Unmarshal(input, &undo) == nil && ValidID(undo.EntryID) {
			var err error
			if partner, err = readEntryPair(ctx, tx, scope, undo.EntryID); err != nil {
				return err
			}
			if partner != nil && partner.RepaidBy != "" {
				return errRepaid(partner)
			}
		}
		books := []string{scope.BookID}
		if partner != nil {
			books = append(books, partner.BookID)
			slices.Sort(books)
		}
		for _, book := range books {
			if err := setScope(ctx, tx, Scope{TenantID: scope.TenantID, BookID: book, ActorID: actor}); err != nil {
				return dbError(err)
			}
			if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, book); err != nil {
				return dbError(err)
			}
		}
		if err := setScope(ctx, tx, scope); err != nil {
			return dbError(err)
		}
		var err error
		if partner == nil {
			result, err = dispatch(ctx, tx, scope, method, input, nil)
			return err
		}
		if result, err = dispatch(withPairUndo(ctx), tx, scope, method, input, nil); err != nil {
			return err
		}
		return reversePartner(ctx, tx, Scope{TenantID: scope.TenantID, BookID: partner.BookID, ActorID: actor}, undo, partner.EntryID)
	})
	return result, err
}

// reversePartner reverses the other entry of a pair the owner is undoing,
// with the same date and reason. The checks the first reversal deferred run
// first, while the session is still in its Book; the partner's reversal
// isn't counted as a correction of the agent's kind of activity.
func reversePartner(ctx context.Context, tx pgx.Tx, scope Scope, undo ReverseJournal, entry string) error {
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return dbError(err)
	}
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
		return dbError(err)
	}
	if err := setScope(ctx, tx, scope); err != nil {
		return dbError(err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('vult.pair_partner','on',true)"); err != nil {
		return dbError(err)
	}
	undo.EntryID = entry
	if _, err := dispatch(withPairUndo(ctx), tx, scope, "reverse_journal", raw(undo), nil); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return dbError(err)
	}
	// Checked: later changes in the same transaction defer theirs again.
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); err != nil {
		return dbError(err)
	}
	_, err := tx.Exec(ctx, "SELECT set_config('vult.pair_partner','',true)")
	return dbError(err)
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
