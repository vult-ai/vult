package ledger

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/jackc/pgx/v5"
)

type DocumentUse struct {
	EntryID        string `json:"entry_id"`
	AccountingDate string `json:"accounting_date"`
	Description    string `json:"description"`
	Reversed       bool   `json:"reversed"`
	Citation       *struct {
		Location Location `json:"location"`
		Revision int64    `json:"revision"`
	} `json:"citation,omitempty"`
	Evidence *struct {
		Role string `json:"role"`
	} `json:"evidence,omitempty"`
}

type DocumentUsePage struct {
	SchemaVersion int           `json:"schema_version"`
	Uses          []DocumentUse `json:"uses"`
	NextAfter     int64         `json:"next_after"`
	HasMore       bool          `json:"has_more"`
	Revision      string        `json:"revision"`
}

// Use cursors keep reversal group, relation kind and sequence in a JSON-safe
// integer. They are opaque addresses, not offsets or accounting quantities.
const maxDocumentUseCursor int64 = 1<<53 - 1

func (a TenantAdmin) DocumentEntries(ctx context.Context, book, document string, query ListQuery, revision string) (DocumentUsePage, error) {
	out := DocumentUsePage{SchemaVersion: 1, Uses: []DocumentUse{}, NextAfter: query.After}
	if !ValidID(document) || query.After < 0 || query.After > maxDocumentUseCursor || query.Limit < 1 || query.Limit > 100 {
		return DocumentUsePage{}, ErrInvalid
	}
	if revision != "" {
		if n, err := strconv.ParseInt(revision, 10, 64); err != nil || n < 0 || strconv.FormatInt(n, 10) != revision {
			return DocumentUsePage{}, ErrInvalid
		}
	}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		if _, err := readDocument(ctx, tx, scope, document); err != nil {
			return dbError(err)
		}
		if err := tx.QueryRow(ctx, "SELECT write_epoch::text FROM public.books WHERE tenant_id=$1 AND id=$2", scope.TenantID, book).Scan(&out.Revision); err != nil {
			return dbError(err)
		}
		if revision != "" && out.Revision != revision {
			return ErrConflict
		}
		rows, err := tx.Query(ctx, `WITH links AS (
            SELECT c.sequence,0 AS kind,c.entry_id,jsonb_build_object('location',c.location,'revision',c.revision) AS citation,NULL::jsonb AS evidence
            FROM public.source_citations c WHERE c.tenant_id=$1 AND c.book_id=$2 AND c.document_id=$3
            AND NOT EXISTS(SELECT 1 FROM public.source_citations newer WHERE newer.tenant_id=c.tenant_id AND newer.book_id=c.book_id
                AND newer.source_id=c.source_id AND newer.revision>c.revision)
            UNION ALL
            SELECT e.sequence,1,e.entry_id,NULL::jsonb,jsonb_build_object('role',e.role)
            FROM public.evidence_links e WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.document_id=$3 AND e.action='attach'
            AND NOT EXISTS(SELECT 1 FROM public.evidence_links d WHERE d.tenant_id=e.tenant_id AND d.book_id=e.book_id AND d.link_id=e.link_id AND d.revision=2)
        ), uses AS (
            SELECT l.*,j.accounting_date::text,j.description,EXISTS(SELECT 1 FROM public.entries r
                WHERE r.tenant_id=$1 AND r.book_id=$2 AND r.reversal_of=j.id) AS reversed
            FROM links l JOIN public.entries j ON j.tenant_id=$1 AND j.book_id=$2 AND j.id=l.entry_id
        ) SELECT sequence,kind,entry_id::text,accounting_date,description,reversed,citation,evidence
          FROM uses WHERE (reversed::int,sequence,kind)>($4::int,$5::bigint,$6::int)
          ORDER BY reversed,sequence,kind LIMIT $7`, scope.TenantID, book, document,
			query.After&1, query.After/4, query.After/2&1, query.Limit+1)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			if len(out.Uses) == query.Limit {
				out.HasMore = true
				break
			}
			var use DocumentUse
			var sequence, kind int64
			var citation, evidence []byte
			if err := rows.Scan(&sequence, &kind, &use.EntryID, &use.AccountingDate, &use.Description, &use.Reversed, &citation, &evidence); err != nil {
				return dbError(err)
			}
			if sequence < 1 || sequence > maxDocumentUseCursor/4 {
				return ErrLimit
			}
			if citation != nil && json.Unmarshal(citation, &use.Citation) != nil || evidence != nil && json.Unmarshal(evidence, &use.Evidence) != nil {
				return ErrInvalid
			}
			out.Uses = append(out.Uses, use)
			out.NextAfter = sequence*4 + kind*2
			if use.Reversed {
				out.NextAfter++
			}
		}
		return dbError(rows.Err())
	})
	if err != nil {
		return DocumentUsePage{}, err
	}
	return out, nil
}
