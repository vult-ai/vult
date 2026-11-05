package ledger

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Location identifies an exact declared place, or the whole original when empty.
// Vult records it without opening or interpreting the document.
type Location struct {
	Page *int `json:"page,omitempty"`
	Row  *int `json:"row,omitempty"`
	Line *int `json:"line,omitempty"`
}

func (l Location) valid() bool {
	for _, n := range []*int{l.Page, l.Row, l.Line} {
		if n != nil && (*n < 1 || *n > 999999999) {
			return false
		}
	}
	return true
}

type SourceInput struct {
	DocumentID string   `json:"document_id"`
	Location   Location `json:"location"`
}

type SourceCitation struct {
	ID         string    `json:"id"`
	SourceID   string    `json:"source_id"`
	Revision   int64     `json:"revision"`
	Sequence   int64     `json:"sequence"`
	EntryID    string    `json:"entry_id"`
	Position   int       `json:"position"`
	DocumentID string    `json:"document_id"`
	Location   Location  `json:"location"`
	ActorID    string    `json:"actor_id"`
	ReceiptID  string    `json:"receipt_id"`
	Reason     string    `json:"reason"`
	RecordedAt time.Time `json:"recorded_at"`
}

type CorrectSourceCitation struct {
	RequestKey       string   `json:"request_key"`
	SourceID         string   `json:"source_id"`
	ExpectedRevision int64    `json:"expected_revision"`
	DocumentID       string   `json:"document_id"`
	Location         Location `json:"location"`
	Reason           string   `json:"reason"`
}

type EntryDocumentsQuery struct {
	EntryID       string `json:"entry_id"`
	SourceAfter   int64  `json:"source_after,omitempty"`
	EvidenceAfter int64  `json:"evidence_after,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	History       bool   `json:"history,omitempty"`
}

type SourceUsageQuery struct {
	DocumentID string   `json:"document_id"`
	Location   Location `json:"location"`
	After      int64    `json:"after,omitempty"`
	Limit      int      `json:"limit,omitempty"`
}

func citationMethod(method string) bool {
	return method == "correct_source_citation" || method == "get_entry_documents" || method == "get_source_usage"
}

func validateSources(sources []SourceInput) error {
	if len(sources) > 32 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, s := range sources {
		if !ValidID(s.DocumentID) || !s.Location.valid() {
			return ErrInvalid
		}
		key := string(raw(s))
		if seen[key] {
			return ErrInvalid
		}
		seen[key] = true
	}
	return nil
}

func insertSource(ctx context.Context, tx pgx.Tx, s Scope, entry, receipt string, position int, source SourceInput) error {
	id := ID()
	_, err := tx.Exec(ctx, `INSERT INTO public.source_citations
        (tenant_id,book_id,id,source_id,revision,sequence,entry_id,position,document_id,location,actor_id,receipt_id)
        VALUES($1,$2,$3,$3,1,1,$4,$5,$6,$7::jsonb,$8,$9)`, s.TenantID, s.BookID, id, entry, position, source.DocumentID, string(raw(source.Location)), s.ActorID, receipt)
	return err
}

const citationColumns = `id::text,source_id::text,revision,sequence,entry_id::text,position,document_id::text,
    location,actor_id::text,receipt_id::text,reason,recorded_at`

func scanCitation(row pgx.Row) (SourceCitation, error) {
	var c SourceCitation
	var location []byte
	err := row.Scan(&c.ID, &c.SourceID, &c.Revision, &c.Sequence, &c.EntryID, &c.Position, &c.DocumentID, &location, &c.ActorID, &c.ReceiptID, &c.Reason, &c.RecordedAt)
	if err == nil && strictjson.Decode(location, &c.Location) != nil {
		return c, ErrInvalid
	}
	return c, err
}

func (s Store) citationCall(ctx context.Context, tx pgx.Tx, scope Scope, method string, input []byte) (json.RawMessage, error) {
	switch method {
	case "correct_source_citation":
		var q CorrectSourceCitation
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.SourceID) || !ValidID(q.DocumentID) ||
			q.ExpectedRevision < 1 || !q.Location.valid() || len(q.Reason) < 1 || len(q.Reason) > 4000 {
			return nil, ErrInvalid
		}
		return write(ctx, tx, scope, method, q.RequestKey, raw(q), s.AdmitWrite, func(receipt string) (json.RawMessage, error) {
			prior, err := scanCitation(tx.QueryRow(ctx, "SELECT "+citationColumns+` FROM public.source_citations
                WHERE tenant_id=$1 AND book_id=$2 AND source_id=$3 ORDER BY revision DESC LIMIT 1`, scope.TenantID, scope.BookID, q.SourceID))
			if err != nil {
				return nil, err
			}
			if prior.Revision != q.ExpectedRevision {
				return nil, ErrConflict
			}
			updated, err := scanCitation(tx.QueryRow(ctx, `INSERT INTO public.source_citations
                (tenant_id,book_id,id,source_id,revision,sequence,entry_id,position,document_id,location,actor_id,receipt_id,reason)
                VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$9::jsonb,$10,$11,$12) RETURNING `+citationColumns,
				scope.TenantID, scope.BookID, ID(), q.SourceID, prior.Revision+1, prior.EntryID, prior.Position, q.DocumentID, string(raw(q.Location)), scope.ActorID, receipt, q.Reason))
			return raw(updated), err
		})
	case "get_entry_documents":
		var q EntryDocumentsQuery
		if strictjson.Decode(input, &q) != nil || !ValidID(q.EntryID) || q.SourceAfter < 0 || q.EvidenceAfter < 0 || q.Limit < 0 || q.Limit > 100 {
			return nil, ErrInvalid
		}
		if q.Limit == 0 {
			q.Limit = 20
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND id=$3)", scope.TenantID, scope.BookID, q.EntryID).Scan(&exists); err != nil {
			return nil, dbError(err)
		}
		if !exists {
			return nil, ErrNotFound
		}
		rows, err := tx.Query(ctx, "SELECT "+citationColumns+` FROM public.source_citations c
            WHERE tenant_id=$1 AND book_id=$2 AND entry_id=$3 AND sequence>$4
            AND ($5 OR NOT EXISTS(SELECT 1 FROM public.source_citations newer
                WHERE newer.tenant_id=c.tenant_id AND newer.book_id=c.book_id AND newer.source_id=c.source_id AND newer.revision>c.revision))
            ORDER BY sequence LIMIT $6`, scope.TenantID, scope.BookID, q.EntryID, q.SourceAfter, q.History, q.Limit+1)
		if err != nil {
			return nil, dbError(err)
		}
		defer rows.Close()
		out := []SourceCitation{}
		for rows.Next() {
			c, err := scanCitation(rows)
			if err != nil {
				return nil, dbError(err)
			}
			out = append(out, c)
		}
		if rows.Err() != nil {
			return nil, dbError(rows.Err())
		}
		more := len(out) > q.Limit
		if more {
			out = out[:q.Limit]
		}
		after := q.SourceAfter
		if len(out) > 0 {
			after = out[len(out)-1].Sequence
		}
		links, evidenceAfter, evidenceMore, err := readEvidencePage(ctx, tx, scope, q)
		if err != nil {
			return nil, err
		}
		return raw(struct {
			Sources           []SourceCitation `json:"sources"`
			NextAfter         int64            `json:"next_source_after"`
			HasMore           bool             `json:"source_has_more"`
			Evidence          []EvidenceLink   `json:"evidence"`
			EvidenceNextAfter int64            `json:"next_evidence_after"`
			EvidenceHasMore   bool             `json:"evidence_has_more"`
		}{out, after, more, links, evidenceAfter, evidenceMore}), nil
	case "get_source_usage":
		var q SourceUsageQuery
		if strictjson.Decode(input, &q) != nil || !ValidID(q.DocumentID) || !q.Location.valid() || q.After < 0 || q.Limit < 0 || q.Limit > 100 {
			return nil, ErrInvalid
		}
		if q.Limit == 0 {
			q.Limit = 20
		}
		// A current citation in an unreversed entry counts once, even if several
		// citation relationships in that entry happen to identify the same place.
		const matches = ` FROM public.entries e WHERE e.tenant_id=$1 AND e.book_id=$2
            AND NOT EXISTS(SELECT 1 FROM public.entries reversal WHERE reversal.tenant_id=e.tenant_id
                AND reversal.book_id=e.book_id AND reversal.reversal_of=e.id)
            AND EXISTS(SELECT 1 FROM public.source_citations c
                WHERE c.tenant_id=e.tenant_id AND c.book_id=e.book_id AND c.entry_id=e.id
                AND c.document_id=$3 AND c.location=$4::jsonb
                AND NOT EXISTS(SELECT 1 FROM public.source_citations newer WHERE newer.tenant_id=c.tenant_id
                    AND newer.book_id=c.book_id AND newer.source_id=c.source_id AND newer.revision>c.revision))`
		var count int64
		if err := tx.QueryRow(ctx, "SELECT count(*)"+matches, scope.TenantID, scope.BookID, q.DocumentID, string(raw(q.Location))).Scan(&count); err != nil {
			return nil, dbError(err)
		}
		rows, err := tx.Query(ctx, "SELECT e.id::text,e.sequence"+matches+" AND e.sequence>$5 ORDER BY e.sequence LIMIT $6", scope.TenantID, scope.BookID, q.DocumentID, string(raw(q.Location)), q.After, q.Limit+1)
		if err != nil {
			return nil, dbError(err)
		}
		defer rows.Close()
		type entryRef struct {
			ID       string `json:"id"`
			Sequence int64  `json:"sequence"`
		}
		out := []entryRef{}
		for rows.Next() {
			var v entryRef
			if err := rows.Scan(&v.ID, &v.Sequence); err != nil {
				return nil, dbError(err)
			}
			out = append(out, v)
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
			Count     int64      `json:"active_entry_count"`
			Reused    bool       `json:"reused"`
			Entries   []entryRef `json:"entries"`
			NextAfter int64      `json:"next_after"`
			HasMore   bool       `json:"has_more"`
		}{count, count > 1, out, after, more}), nil
	}
	return nil, ErrInvalid
}
