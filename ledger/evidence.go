package ledger

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/strictjson"
)

type AttachEvidence struct {
	RequestKey string `json:"request_key"`
	EntryID    string `json:"entry_id"`
	DocumentID string `json:"document_id"`
	Role       string `json:"role"`
	Reason     string `json:"reason"`
}

type DetachEvidence struct {
	RequestKey string `json:"request_key"`
	LinkID     string `json:"link_id"`
	Reason     string `json:"reason"`
}

type EvidenceLink struct {
	ID         string    `json:"id"`
	LinkID     string    `json:"link_id"`
	Revision   int64     `json:"revision"`
	Sequence   int64     `json:"sequence"`
	EntryID    string    `json:"entry_id"`
	DocumentID string    `json:"document_id"`
	Role       string    `json:"role"`
	Action     string    `json:"action"`
	ActorID    string    `json:"actor_id"`
	ReceiptID  string    `json:"receipt_id"`
	Reason     string    `json:"reason"`
	RecordedAt time.Time `json:"recorded_at"`
}

type MissingEvidenceQuery struct {
	AccountID string `json:"account_id,omitempty"`
	From      string `json:"from,omitempty"`
	Through   string `json:"through,omitempty"`
	Role      string `json:"role,omitempty"`
	After     int64  `json:"after,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

func evidenceMethod(method string) bool {
	return method == "attach_evidence" || method == "detach_evidence" || method == "list_entries_missing_evidence"
}

func evidenceRole(role string) bool {
	switch role {
	case "receipt", "invoice", "contract", "statement", "other":
		return true
	}
	return false
}

const evidenceColumns = `id::text,link_id::text,revision,sequence,entry_id::text,document_id::text,
    role,action,actor_id::text,receipt_id::text,reason,recorded_at`

func scanEvidence(row pgx.Row) (EvidenceLink, error) {
	var e EvidenceLink
	err := row.Scan(&e.ID, &e.LinkID, &e.Revision, &e.Sequence, &e.EntryID, &e.DocumentID, &e.Role, &e.Action, &e.ActorID, &e.ReceiptID, &e.Reason, &e.RecordedAt)
	return e, err
}

func (s Store) evidenceCall(ctx context.Context, tx pgx.Tx, scope Scope, method string, input []byte) (json.RawMessage, error) {
	switch method {
	case "attach_evidence":
		var q AttachEvidence
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.EntryID) || !ValidID(q.DocumentID) ||
			!evidenceRole(q.Role) || len(q.Reason) < 1 || len(q.Reason) > 4000 {
			return nil, ErrInvalid
		}
		return write(ctx, tx, scope, method, q.RequestKey, raw(q), s.AdmitWrite, func(receipt string) (json.RawMessage, error) {
			var active bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.evidence_links e
                WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.entry_id=$3 AND e.document_id=$4 AND e.role=$5
                AND e.revision=1 AND NOT EXISTS(SELECT 1 FROM public.evidence_links detached
                    WHERE detached.tenant_id=e.tenant_id AND detached.book_id=e.book_id AND detached.link_id=e.link_id AND detached.revision=2))`,
				scope.TenantID, scope.BookID, q.EntryID, q.DocumentID, q.Role).Scan(&active)
			if err != nil {
				return nil, err
			}
			if active {
				return nil, ErrConflict
			}
			id := ID()
			e, err := scanEvidence(tx.QueryRow(ctx, `INSERT INTO public.evidence_links
                (tenant_id,book_id,id,link_id,revision,sequence,entry_id,document_id,role,action,actor_id,receipt_id,reason)
                VALUES($1,$2,$3,$3,1,1,$4,$5,$6,'attach',$7,$8,$9) RETURNING `+evidenceColumns,
				scope.TenantID, scope.BookID, id, q.EntryID, q.DocumentID, q.Role, scope.ActorID, receipt, q.Reason))
			return raw(e), err
		})
	case "detach_evidence":
		var q DetachEvidence
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.LinkID) || len(q.Reason) < 1 || len(q.Reason) > 4000 {
			return nil, ErrInvalid
		}
		return write(ctx, tx, scope, method, q.RequestKey, raw(q), s.AdmitWrite, func(receipt string) (json.RawMessage, error) {
			prior, err := scanEvidence(tx.QueryRow(ctx, "SELECT "+evidenceColumns+` FROM public.evidence_links
                WHERE tenant_id=$1 AND book_id=$2 AND link_id=$3 ORDER BY revision DESC LIMIT 1`, scope.TenantID, scope.BookID, q.LinkID))
			if err != nil {
				return nil, err
			}
			if prior.Revision != 1 {
				return nil, ErrConflict
			}
			e, err := scanEvidence(tx.QueryRow(ctx, `INSERT INTO public.evidence_links
                (tenant_id,book_id,id,link_id,revision,sequence,entry_id,document_id,role,action,actor_id,receipt_id,reason)
                VALUES($1,$2,$3,$4,2,1,$5,$6,$7,'detach',$8,$9,$10) RETURNING `+evidenceColumns,
				scope.TenantID, scope.BookID, ID(), q.LinkID, prior.EntryID, prior.DocumentID, prior.Role, scope.ActorID, receipt, q.Reason))
			return raw(e), err
		})
	case "list_entries_missing_evidence":
		var q MissingEvidenceQuery
		if strictjson.Decode(input, &q) != nil || q.AccountID != "" && !ValidID(q.AccountID) ||
			q.From != "" && !ValidDate(q.From) || q.Through != "" && !ValidDate(q.Through) ||
			q.From != "" && q.Through != "" && q.From > q.Through || q.Role != "" && !evidenceRole(q.Role) ||
			q.After < 0 || q.Limit < 0 || q.Limit > 100 {
			return nil, ErrInvalid
		}
		if q.Limit == 0 {
			q.Limit = 20
		}
		rows, err := tx.Query(ctx, `SELECT e.id::text,e.sequence,e.economic_date::text,e.accounting_date::text,e.description
            FROM public.entries e WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.sequence>$3 AND e.reversal_of IS NULL
            AND NOT EXISTS(SELECT 1 FROM public.entries r WHERE r.tenant_id=e.tenant_id AND r.book_id=e.book_id AND r.reversal_of=e.id)
            AND ($4='' OR EXISTS(SELECT 1 FROM public.lines l WHERE l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id AND l.account_id::text=$4))
            AND ($5='' OR e.accounting_date>=nullif($5,'')::date) AND ($6='' OR e.accounting_date<=nullif($6,'')::date)
            AND NOT EXISTS(SELECT 1 FROM public.evidence_links a WHERE a.tenant_id=e.tenant_id AND a.book_id=e.book_id
                AND a.entry_id=e.id AND a.revision=1 AND ($7='' OR a.role=$7)
                AND NOT EXISTS(SELECT 1 FROM public.evidence_links d WHERE d.tenant_id=a.tenant_id AND d.book_id=a.book_id AND d.link_id=a.link_id AND d.revision=2))
            ORDER BY e.sequence LIMIT $8`, scope.TenantID, scope.BookID, q.After, q.AccountID, q.From, q.Through, q.Role, q.Limit+1)
		if err != nil {
			return nil, dbError(err)
		}
		defer rows.Close()
		type entrySummary struct {
			ID             string `json:"id"`
			Sequence       int64  `json:"sequence"`
			EconomicDate   string `json:"economic_date"`
			AccountingDate string `json:"accounting_date"`
			Description    string `json:"description"`
		}
		out := []entrySummary{}
		for rows.Next() {
			var e entrySummary
			if err := rows.Scan(&e.ID, &e.Sequence, &e.EconomicDate, &e.AccountingDate, &e.Description); err != nil {
				return nil, dbError(err)
			}
			out = append(out, e)
		}
		if rows.Err() != nil {
			return nil, dbError(rows.Err())
		}
		more := len(out) > q.Limit
		if more {
			out = out[:q.Limit]
		}
		after := q.After
		if len(out) > 0 {
			after = out[len(out)-1].Sequence
		}
		return raw(struct {
			Entries   []entrySummary `json:"entries"`
			NextAfter int64          `json:"next_after"`
			HasMore   bool           `json:"has_more"`
		}{out, after, more}), nil
	}
	return nil, ErrInvalid
}

func readEvidencePage(ctx context.Context, tx pgx.Tx, s Scope, q EntryDocumentsQuery) ([]EvidenceLink, int64, bool, error) {
	rows, err := tx.Query(ctx, "SELECT "+evidenceColumns+` FROM public.evidence_links e
        WHERE tenant_id=$1 AND book_id=$2 AND entry_id=$3 AND sequence>$4
        AND ($5 OR (revision=1 AND NOT EXISTS(SELECT 1 FROM public.evidence_links detached
            WHERE detached.tenant_id=e.tenant_id AND detached.book_id=e.book_id AND detached.link_id=e.link_id AND detached.revision=2)))
        ORDER BY sequence LIMIT $6`, s.TenantID, s.BookID, q.EntryID, q.EvidenceAfter, q.History, q.Limit+1)
	if err != nil {
		return nil, 0, false, dbError(err)
	}
	defer rows.Close()
	out := []EvidenceLink{}
	for rows.Next() {
		e, err := scanEvidence(rows)
		if err != nil {
			return nil, 0, false, dbError(err)
		}
		out = append(out, e)
	}
	if rows.Err() != nil {
		return nil, 0, false, dbError(rows.Err())
	}
	more := len(out) > q.Limit
	if more {
		out = out[:q.Limit]
	}
	after := q.EvidenceAfter
	if len(out) > 0 {
		after = out[len(out)-1].Sequence
	}
	return out, after, more, nil
}
