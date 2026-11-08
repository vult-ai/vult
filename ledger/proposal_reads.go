package ledger

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProposalSummary is a bounded queue item. Only the exact detail read evaluates
// dependencies; a recorded approval alone never asserts current committability.
type ProposalSummary struct {
	ID                   string     `json:"id"`
	Sequence             int64      `json:"sequence"`
	BookID               string     `json:"book_id"`
	Operation            string     `json:"operation"`
	SHA256               string     `json:"sha256"`
	CreatedAt            time.Time  `json:"created_at"`
	ExpiresAt            *time.Time `json:"expires_at"`
	Description          string     `json:"description"`
	EconomicDate         string     `json:"economic_date"`
	AccountingDate       string     `json:"accounting_date"`
	Status               string     `json:"status"`
	DecisionRevision     int64      `json:"decision_revision"`
	ExecutorConnectionID string     `json:"executor_connection_id,omitempty"`
	DependencyStatus     string     `json:"dependency_status"`
}

type OperationProposalPage struct {
	Version int    `json:"version"`
	BookID  string `json:"book_id"`
	// Waiting counts every proposal in the Book that still needs the owner's
	// decision, independent of the page and its filter.
	Waiting    int64             `json:"waiting"`
	Plans      []ProposalSummary `json:"plans"`
	NextBefore int64             `json:"next_before"`
	HasMore    bool              `json:"has_more"`
}

type OperationDecisionPage struct {
	Version    int                 `json:"version"`
	BookID     string              `json:"book_id"`
	ProposalID string              `json:"plan_id"`
	Decisions  []OperationDecision `json:"decisions"`
	NextAfter  int64               `json:"next_after"`
	HasMore    bool                `json:"has_more"`
}

func (a TenantAdmin) proposalRead(ctx context.Context, book string, call func(pgx.Tx, Scope) error) error {
	if !a.valid() {
		return ErrDenied
	}
	if !ValidID(book) {
		return ErrInvalid
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback(context.Background())
	scope := Scope{TenantID: a.tenantID, BookID: book}
	if err = setScope(ctx, tx, scope); err != nil {
		return dbError(err)
	}
	var selected string
	if err = tx.QueryRow(ctx, "SELECT id::text FROM public.books WHERE tenant_id=$1 AND id=$2", a.tenantID, book).Scan(&selected); err != nil {
		return dbError(err)
	}
	if err = call(tx, scope); err != nil {
		return err
	}
	return dbError(tx.Commit(ctx))
}

// OperationProposals pages lightweight summaries without replaying financial
// history for every queue row. The owner opens one exact proposal to review it.
// With waitingOnly, the page holds only proposals still needing a decision.
func (a TenantAdmin) OperationProposals(ctx context.Context, book string, before int64, limit int, waitingOnly bool) (OperationProposalPage, error) {
	out := OperationProposalPage{Version: 1, BookID: book, Plans: []ProposalSummary{}, NextBefore: before}
	if before < 0 || limit < 1 || limit > 100 {
		return out, ErrInvalid
	}
	err := a.proposalRead(ctx, book, func(tx pgx.Tx, _ Scope) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM public.operation_proposals p WHERE p.tenant_id=$1 AND p.book_id=$2
            AND (p.expires_at IS NULL OR p.expires_at>transaction_timestamp()) AND `+proposalWaiting, a.tenantID, book).Scan(&out.Waiting); err != nil {
			return dbError(err)
		}
		rows, err := tx.Query(ctx, `SELECT p.id::text,p.sequence,p.book_id::text,p.operation,p.sha256,p.created_at,p.expires_at,
            convert_from(p.payload_bytes,'UTF8')::jsonb->'preview'->>'description',
            convert_from(p.payload_bytes,'UTF8')::jsonb->'preview'->>'economic_date',
            convert_from(p.payload_bytes,'UTF8')::jsonb->'preview'->>'accounting_date',
            CASE WHEN c.proposal_id IS NOT NULL THEN 'committed' WHEN p.expires_at<=transaction_timestamp() THEN 'expired'
                WHEN d.decision='reject' THEN 'rejected' WHEN d.decision='approve' AND coalesce(d.expires_at<=transaction_timestamp(),true) THEN 'expired'
                WHEN d.decision='approve' THEN 'approved' ELSE 'approval_required' END,
            coalesce(d.revision,0),coalesce(d.executor_connection_id::text,'')
            FROM public.operation_proposals p LEFT JOIN LATERAL (
                SELECT revision,decision,expires_at,executor_connection_id FROM public.operation_decisions d
                WHERE d.tenant_id=p.tenant_id AND d.book_id=p.book_id AND d.proposal_id=p.id ORDER BY revision DESC LIMIT 1
            ) d ON true LEFT JOIN public.operation_consumptions c
                ON c.tenant_id=p.tenant_id AND c.book_id=p.book_id AND c.proposal_id=p.id
            WHERE p.tenant_id=$1 AND p.book_id=$2 AND ($3::bigint=0 OR p.sequence<$3)
            AND (NOT $5 OR ((p.expires_at IS NULL OR p.expires_at>transaction_timestamp()) AND d.revision IS NULL))
            ORDER BY p.sequence DESC LIMIT $4`, a.tenantID, book, before, limit+1, waitingOnly)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var summary ProposalSummary
			if err = rows.Scan(&summary.ID, &summary.Sequence, &summary.BookID, &summary.Operation, &summary.SHA256, &summary.CreatedAt, &summary.ExpiresAt, &summary.Description, &summary.EconomicDate, &summary.AccountingDate, &summary.Status, &summary.DecisionRevision, &summary.ExecutorConnectionID); err != nil {
				return dbError(err)
			}
			if len(out.Plans) == limit {
				out.HasMore = true
				break
			}
			summary.DependencyStatus = "not_checked"
			out.Plans = append(out.Plans, summary)
			out.NextBefore = summary.Sequence
		}
		return dbError(rows.Err())
	})
	return out, err
}

func (a TenantAdmin) OperationProposal(ctx context.Context, book, id string) (ProposalState, error) {
	var out ProposalState
	if !ValidID(id) {
		return out, ErrInvalid
	}
	err := a.proposalRead(ctx, book, func(tx pgx.Tx, scope Scope) error {
		p, err := readProposal(ctx, tx, scope, id)
		if err != nil {
			return dbError(err)
		}
		if out, err = readProposalState(ctx, tx, scope, p, true); err == nil {
			out, err = withDuplicate(ctx, tx, scope, out)
		}
		return err
	})
	return out, err
}

func (a TenantAdmin) OperationDecisions(ctx context.Context, book, id string, after int64, limit int) (OperationDecisionPage, error) {
	out := OperationDecisionPage{Version: 1, BookID: book, ProposalID: id, Decisions: []OperationDecision{}, NextAfter: after}
	if !ValidID(id) || after < 0 || limit < 1 || limit > 100 {
		return out, ErrInvalid
	}
	err := a.proposalRead(ctx, book, func(tx pgx.Tx, _ Scope) error {
		var found bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.operation_proposals WHERE tenant_id=$1 AND book_id=$2 AND id=$3)", a.tenantID, book, id).Scan(&found); err != nil {
			return dbError(err)
		}
		if !found {
			return ErrNotFound
		}
		rows, err := tx.Query(ctx, "SELECT "+decisionColumns+" FROM public.operation_decisions WHERE tenant_id=$1 AND book_id=$2 AND proposal_id=$3 AND revision>$4 ORDER BY revision LIMIT $5", a.tenantID, book, id, after, limit+1)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			decision, err := scanDecision(rows)
			if err != nil {
				return dbError(err)
			}
			if len(out.Decisions) == limit {
				out.HasMore = true
				break
			}
			out.Decisions = append(out.Decisions, decision)
			out.NextAfter = decision.Revision
		}
		return dbError(rows.Err())
	})
	return out, err
}
