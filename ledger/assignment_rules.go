package ledger

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// AssignmentRule is where the owner said charges paid from a Book go: one
// bank item, or every charge from a counterparty (on one bank account, or
// any). Target is a linked Book's id, or "personal" for this Book itself.
type AssignmentRule struct {
	ID              string `json:"id"`
	TransactionID   string `json:"transaction_id,omitempty"`
	BankAccountID   string `json:"bank_account_id,omitempty"`
	CounterpartyKey string `json:"counterparty_key,omitempty"`
	Target          string `json:"target"`
	// Source is owner (made on screen), answer (from the owner's answer to a
	// question) or reassigned (the owner moved a charge to another Book).
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

// AssignmentRuleChange makes a rule. A rule for the same item, or the same
// counterparty on the same bank account, replaces it.
type AssignmentRuleChange struct {
	BookID          string `json:"book_id"`
	TransactionID   string `json:"transaction_id,omitempty"`
	BankAccountID   string `json:"bank_account_id,omitempty"`
	CounterpartyKey string `json:"counterparty_key,omitempty"`
	Target          string `json:"target"`
}

func validAssignmentTarget(target string) bool { return target == "personal" || ValidID(target) }

func readAssignmentRules(ctx context.Context, tx pgx.Tx, s Scope) ([]AssignmentRule, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,coalesce(transaction_id::text,''),coalesce(bank_account_id::text,''),coalesce(counterparty_key,''),target,source,created_at
 FROM public.assignment_rules WHERE tenant_id=$1 AND book_id=$2 ORDER BY created_at DESC,id LIMIT 1000`, s.TenantID, s.BookID)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []AssignmentRule{}
	for rows.Next() {
		var v AssignmentRule
		if err := rows.Scan(&v.ID, &v.TransactionID, &v.BankAccountID, &v.CounterpartyKey, &v.Target, &v.Source, &v.CreatedAt); err != nil {
			return nil, dbError(err)
		}
		out = append(out, v)
	}
	return out, dbError(rows.Err())
}

// setAssignmentRule replaces the Book's rule for the same item or
// counterparty with this one.
func setAssignmentRule(ctx context.Context, tx pgx.Tx, s Scope, actor string, q AssignmentRuleChange, source string) error {
	item := q.TransactionID != ""
	if item == (q.CounterpartyKey != "") || item && (!ValidID(q.TransactionID) || q.BankAccountID != "") ||
		q.BankAccountID != "" && !ValidID(q.BankAccountID) || len(q.CounterpartyKey) > 200 || !validAssignmentTarget(q.Target) {
		return ErrInvalid
	}
	nullable := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	if item {
		if _, err := tx.Exec(ctx, "DELETE FROM public.assignment_rules WHERE tenant_id=$1 AND book_id=$2 AND transaction_id=$3", s.TenantID, s.BookID, q.TransactionID); err != nil {
			return linkError(err)
		}
	} else if _, err := tx.Exec(ctx, `DELETE FROM public.assignment_rules WHERE tenant_id=$1 AND book_id=$2 AND counterparty_key=left(public.rule_key($3),200)
 AND bank_account_id IS NOT DISTINCT FROM $4::uuid`, s.TenantID, s.BookID, q.CounterpartyKey, nullable(q.BankAccountID)); err != nil {
		return linkError(err)
	}
	_, err := tx.Exec(ctx, `INSERT INTO public.assignment_rules(tenant_id,book_id,id,transaction_id,bank_account_id,counterparty_key,target,source,actor_id)
 VALUES($1,$2,$3,$4::uuid,$5::uuid,$6,$7,$8,$9)`, s.TenantID, s.BookID, ID(), nullable(q.TransactionID), nullable(q.BankAccountID), nullable(q.CounterpartyKey), q.Target, source, actor)
	return linkError(err)
}

// AssignmentRules lists a Book's assignment rules for the owner.
func (a TenantAdmin) AssignmentRules(ctx context.Context, book string) ([]AssignmentRule, error) {
	var out []AssignmentRule
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readAssignmentRules(ctx, tx, s)
		return err
	})
	return out, err
}

// SetAssignmentRule makes, or replaces, one of a Book's assignment rules.
func (a TenantAdmin) SetAssignmentRule(ctx context.Context, actor string, q AssignmentRuleChange) ([]AssignmentRule, error) {
	var out []AssignmentRule
	err := a.withRuleOwner(ctx, q.BookID, actor, func(tx pgx.Tx, s Scope) error {
		if err := setAssignmentRule(ctx, tx, s, actor, q, "owner"); err != nil {
			return err
		}
		var err error
		out, err = readAssignmentRules(ctx, tx, s)
		return err
	})
	return out, err
}

// DeleteAssignmentRule removes one of a Book's assignment rules.
func (a TenantAdmin) DeleteAssignmentRule(ctx context.Context, actor, book, rule string) error {
	if !ValidID(rule) {
		return ErrInvalid
	}
	return a.withRuleOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		tag, err := tx.Exec(ctx, "DELETE FROM public.assignment_rules WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, rule)
		if err != nil {
			return linkError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}
