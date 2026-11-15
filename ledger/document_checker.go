package ledger

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
)

// What Vult's document checker needs from the Book, and how it records what
// it found. The checker runs as Vult for the owner, never as an agent: an
// agent can't mark a check passed by saying so.

// DocumentFacts is what a statement is compared with.
type DocumentFacts struct {
	BookName  string
	LegalName string
	Currency  string
	Scale     int
	Accounts  []StatementAccountFact
	// Holders and Addresses were read from the Book's earlier statements
	// and accepted, newest first.
	Holders   []string
	Addresses []string
}

// StatementAccountFact is a Book account that takes statements, with the
// bank account linked to it, if any.
type StatementAccountFact struct {
	AccountID   string
	Name        string
	Kind        string // cash or card
	Institution string
	Mask        string
}

// DocumentFacts reads what the checker compares a document with.
func (a TenantAdmin) DocumentFacts(ctx context.Context, book string) (DocumentFacts, error) {
	var f DocumentFacts
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, b Book) error {
		var err error
		f, err = documentFacts(ctx, tx, s, b)
		return err
	})
	return f, err
}

func documentFacts(ctx context.Context, tx pgx.Tx, s Scope, b Book) (DocumentFacts, error) {
	var f DocumentFacts
	err := func() error {
		f.BookName, f.Currency, f.Scale = b.Name, b.Currency, b.Scale
		_ = tx.QueryRow(ctx, `SELECT coalesce(r.new_identity->>'legal_name','') FROM public.book_identity_revisions r
 WHERE r.tenant_id=$1 AND r.book_id=$2 ORDER BY r.revision DESC LIMIT 1`, s.TenantID, s.BookID).Scan(&f.LegalName)
		rows, err := tx.Query(ctx, `SELECT a.id::text,coalesce(a.name,''),a.role,coalesce(c.institution_name,''),coalesce(ba.mask,'')
 FROM public.accounts a
 LEFT JOIN public.bank_accounts ba ON ba.tenant_id=a.tenant_id AND ba.book_id=a.book_id AND ba.account_id=a.id
 LEFT JOIN public.bank_connections c ON c.tenant_id=ba.tenant_id AND c.book_id=ba.book_id AND c.id=ba.connection_id
 WHERE a.tenant_id=$1 AND a.book_id=$2 AND a.role IN ('cash','card') ORDER BY a.id, ba.mask`, s.TenantID, s.BookID)
		if err != nil {
			return dbError(err)
		}
		for rows.Next() {
			var x StatementAccountFact
			if err := rows.Scan(&x.AccountID, &x.Name, &x.Kind, &x.Institution, &x.Mask); err != nil {
				rows.Close()
				return dbError(err)
			}
			f.Accounts = append(f.Accounts, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return dbError(err)
		}
		accepted, err := tx.Query(ctx, `SELECT c.check_name,c.found FROM public.document_checks c WHERE c.tenant_id=$1 AND c.book_id=$2
 AND c.check_name IN ('statement_holder','statement_address') AND c.result IN ('ok','note') AND c.found<>''
 ORDER BY c.sequence DESC LIMIT 200`, s.TenantID, s.BookID)
		if err != nil {
			return dbError(err)
		}
		defer accepted.Close()
		seen := map[string]bool{}
		for accepted.Next() {
			var check, found string
			if err := accepted.Scan(&check, &found); err != nil {
				return dbError(err)
			}
			if seen[check+found] {
				continue
			}
			seen[check+found] = true
			if check == CheckStatementHolder {
				f.Holders = append(f.Holders, found)
			} else {
				f.Addresses = append(f.Addresses, found)
			}
		}
		return dbError(accepted.Err())
	}()
	return f, err
}

func (a TenantAdmin) asVult(ctx context.Context, book, actor string, call func(pgx.Tx, Scope, Book) error) error {
	if !ValidID(actor) {
		return ErrInvalid
	}
	return a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, b Book) error {
		scope.ActorID = actor
		if err := setScope(ctx, tx, scope); err != nil {
			return dbError(err)
		}
		return call(tx, scope, b)
	})
}

// WriteDocumentCheck records one check, committed at once so the owner's
// screen sees it as it happens.
func (a TenantAdmin) WriteDocumentCheck(ctx context.Context, book, actor string, c DocumentCheck) error {
	if !ValidID(c.DocumentID) {
		return ErrInvalid
	}
	return a.asVult(ctx, book, actor, func(tx pgx.Tx, s Scope, _ Book) error {
		return noteCheck(ctx, tx, s, c)
	})
}

// CheckStatementNumbers runs the plain-code checks on a statement as read,
// before anyone imports it, and records them.
func (a TenantAdmin) CheckStatementNumbers(ctx context.Context, book, actor string, q StatementReading) ([]DocumentCheck, error) {
	if !ValidID(q.DocumentID) || !ValidID(q.AccountID) || !ValidDate(q.PeriodStart) || !ValidDate(q.PeriodEnd) || q.PeriodStart > q.PeriodEnd {
		return nil, ErrInvalid
	}
	for _, l := range q.Lines {
		if !ValidDate(l.Date) {
			return nil, ErrInvalid
		}
	}
	var out []DocumentCheck
	err := a.asVult(ctx, book, actor, func(tx pgx.Tx, s Scope, b Book) error {
		var err error
		out, err = checkNumbers(ctx, tx, s, b, q)
		return err
	})
	return out, err
}

func checkNumbers(ctx context.Context, tx pgx.Tx, s Scope, b Book, q StatementReading) ([]DocumentCheck, error) {
	var out []DocumentCheck
	err := func() error {
		var name, currency string
		var scale int
		err := tx.QueryRow(ctx, `SELECT coalesce(a.name,''),coalesce(a.currency,$4),coalesce(a.native_scale,$5) FROM public.accounts a
 WHERE a.tenant_id=$1 AND a.book_id=$2 AND a.id=$3 AND a.role IN ('cash','card')`, s.TenantID, s.BookID, q.AccountID, b.Currency, b.Scale).Scan(&name, &currency, &scale)
		if err != nil {
			if err == pgx.ErrNoRows {
				return ErrInvalid
			}
			return dbError(err)
		}
		if strings.TrimSpace(name) == "" {
			name = "This account"
		}
		derived := ""
		if q.OpeningDerived {
			derived = "The export gives only a closing balance; the opening was worked out from its lines."
		}
		checks, err := numberChecks(ctx, tx, s, q, "", name, scale, currency, derived, false)
		if err != nil {
			return err
		}
		for _, c := range checks {
			if err := noteCheck(ctx, tx, s, c); err != nil {
				return err
			}
		}
		out = checks
		return nil
	}()
	return out, err
}

// RecordReceiptAsVult records what Vult read from a receipt and checks it.
func (a TenantAdmin) RecordReceiptAsVult(ctx context.Context, book, actor string, q RecordReceiptDetails) ([]DocumentCheck, error) {
	if !ValidID(q.DocumentID) || q.Date != "" && !ValidDate(q.Date) || len(q.Vendor) > 200 {
		return nil, ErrInvalid
	}
	if q.AmountMinor != "" {
		if _, ok := minorInt(q.AmountMinor); !ok {
			return nil, ErrInvalid
		}
	}
	var out []DocumentCheck
	err := a.asVult(ctx, book, actor, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = receiptChecks(ctx, tx, s, q)
		return err
	})
	return out, err
}

// SaveDocumentReading keeps what Vult read from a document, as Vult.
func (a TenantAdmin) SaveDocumentReading(ctx context.Context, book, actor string, r DocumentReading) error {
	return a.asVult(ctx, book, actor, func(tx pgx.Tx, s Scope, _ Book) error { return noteReading(ctx, tx, s, r) })
}

// LatestDocumentReading is a document's current reading.
func (a TenantAdmin) LatestDocumentReading(ctx context.Context, book, document string) (DocumentReading, bool, error) {
	var r DocumentReading
	var ok bool
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		r, ok, err = latestReading(ctx, tx, s, document)
		return err
	})
	return r, ok, err
}

// AgentRequest is how Vult checks a reading an agent proposed, inside that
// agent's own request: it reads the Book and records the verified reading
// and Vult's checks in the request's scope. It is Vult's code, never an
// agent tool; agents only propose.
type AgentRequest struct {
	Store Store
	Scope Scope
}

func (v AgentRequest) run(ctx context.Context, fn func(pgx.Tx, Scope, Book) error) error {
	if v.Scope.Access == "read" {
		return ErrDenied
	}
	_, err := v.Store.transact(ctx, v.Scope, "list_document_checks", nil, func(tx pgx.Tx) (json.RawMessage, error) {
		b, err := readBook(ctx, tx, v.Scope)
		if err != nil {
			return nil, dbError(err)
		}
		return nil, fn(tx, v.Scope, b)
	})
	return err
}

func (v AgentRequest) Facts(ctx context.Context) (DocumentFacts, error) {
	var f DocumentFacts
	err := v.run(ctx, func(tx pgx.Tx, s Scope, b Book) error {
		var err error
		f, err = documentFacts(ctx, tx, s, b)
		return err
	})
	return f, err
}

func (v AgentRequest) Write(ctx context.Context, c DocumentCheck) error {
	return v.run(ctx, func(tx pgx.Tx, s Scope, _ Book) error { return noteCheck(ctx, tx, s, c) })
}

func (v AgentRequest) Numbers(ctx context.Context, q StatementReading) ([]DocumentCheck, error) {
	var out []DocumentCheck
	err := v.run(ctx, func(tx pgx.Tx, s Scope, b Book) error {
		var err error
		out, err = checkNumbers(ctx, tx, s, b, q)
		return err
	})
	return out, err
}

func (v AgentRequest) Receipt(ctx context.Context, q RecordReceiptDetails) ([]DocumentCheck, error) {
	var out []DocumentCheck
	err := v.run(ctx, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = receiptChecks(ctx, tx, s, q)
		return err
	})
	return out, err
}

func (v AgentRequest) Reading(ctx context.Context, document string) (DocumentReading, bool, error) {
	var r DocumentReading
	var ok bool
	err := v.run(ctx, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		r, ok, err = latestReading(ctx, tx, s, document)
		return err
	})
	return r, ok, err
}

// SaveReading keeps a verified reading, named for the agent that proposed
// it.
func (v AgentRequest) SaveReading(ctx context.Context, r DocumentReading) error {
	return v.run(ctx, func(tx pgx.Tx, s Scope, _ Book) error {
		if r.Reader == "" {
			_ = tx.QueryRow(ctx, "SELECT left(coalesce(name,''),120) FROM public.connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3",
				s.TenantID, s.BookID, s.ConnectionID).Scan(&r.Reader)
		}
		return noteReading(ctx, tx, s, r)
	})
}
