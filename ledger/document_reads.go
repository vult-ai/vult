package ledger

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Evaluate expiry once for every read in the same database transaction.
const liveReadyDocument = `state='ready' AND (used_at IS NOT NULL OR expires_at IS NULL OR expires_at>transaction_timestamp())`

type DocumentPage struct {
	Documents []Document `json:"documents"`
	NextAfter int64      `json:"next_after"`
	HasMore   bool       `json:"has_more"`
}

type DocumentSearchPage struct {
	RecordedState
	DocumentPage
	Total    int    `json:"total"`
	Revision string `json:"revision"`
}

func documentInspection(ctx context.Context, tx pgx.Tx, scope Scope, input []byte) (DocumentInspection, error) {
	var q DocumentQuery
	if strictjson.Decode(input, &q) != nil || !ValidID(q.DocumentID) || q.UploadID != "" && !ValidID(q.UploadID) {
		return DocumentInspection{}, ErrInvalid
	}
	d, err := readDocument(ctx, tx, scope, q.DocumentID)
	if err != nil {
		return DocumentInspection{}, dbError(err)
	}
	if !d.live {
		d.State = "expired"
	}
	inspection := DocumentInspection{Document: d.Document}
	if q.UploadID != "" {
		if scope.Access != "write" {
			return DocumentInspection{}, ErrDenied
		}
		var storage string
		var expires time.Time
		var live bool
		err = tx.QueryRow(ctx, `SELECT storage_id,expires_at,expires_at>clock_timestamp() FROM public.document_uploads
                WHERE tenant_id=$1 AND book_id=$2 AND document_id=$3 AND id=$4 AND actor_id=$5 AND connection_id=$6`,
			scope.TenantID, scope.BookID, d.ID, q.UploadID, scope.ActorID, scope.ConnectionID).Scan(&storage, &expires, &live)
		if err != nil {
			return DocumentInspection{}, dbError(err)
		}
		state := &DocumentUploadState{ID: q.UploadID, Status: "expired"}
		var result []byte
		err = tx.QueryRow(ctx, `SELECT result_bytes FROM public.requests
                WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4 AND operation='complete_document_upload'`,
			scope.TenantID, scope.BookID, scope.ActorID, "upload:"+q.UploadID).Scan(&result)
		if err == nil {
			state.Status = "recorded"
			state.Result = result
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return DocumentInspection{}, dbError(err)
		} else if live && storage == d.storageID && d.State != "expired" && d.live {
			state.Status = "pending"
			state.ExpiresAt = &expires
		}
		inspection.Upload = state
	}
	return inspection, nil
}

func documentPage(ctx context.Context, tx pgx.Tx, scope Scope, input []byte) (DocumentPage, error) {
	var q ListQuery
	if strictjson.Decode(input, &q) != nil || q.After < 0 || q.Limit < 0 || q.Limit > 100 {
		return DocumentPage{}, ErrInvalid
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	rows, err := tx.Query(ctx, "SELECT "+documentColumns+` FROM public.documents
            WHERE tenant_id=$1 AND book_id=$2 AND sequence>$3 AND `+liveReadyDocument+`
            ORDER BY sequence LIMIT $4`, scope.TenantID, scope.BookID, q.After, q.Limit+1)
	if err != nil {
		return DocumentPage{}, dbError(err)
	}
	out, err := scanDocuments(rows)
	if err != nil {
		return DocumentPage{}, err
	}
	more := len(out) > q.Limit
	if more {
		out = out[:q.Limit]
	}
	after := q.After
	if len(out) > 0 {
		after = out[len(out)-1].Sequence
	}
	return DocumentPage{Documents: out, NextAfter: after, HasMore: more}, nil
}

func (a TenantAdmin) Documents(ctx context.Context, book string, query ListQuery) (DocumentPage, error) {
	var out DocumentPage
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		var err error
		out, err = documentPage(ctx, tx, scope, raw(query))
		return err
	})
	if err != nil {
		return DocumentPage{}, err
	}
	return out, nil
}

func scanDocuments(rows pgx.Rows) ([]Document, error) {
	defer rows.Close()
	out := []Document{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, dbError(err)
		}
		out = append(out, d.Document)
	}
	return out, dbError(rows.Err())
}

// SearchDocuments pages only matching ready originals using the same matcher
// as Book search. Total counts the full selection before the cursor.
func (a TenantAdmin) SearchDocuments(ctx context.Context, book, query string, page ListQuery, revision string) (DocumentSearchPage, error) {
	if page.After < 0 || page.Limit < 1 || page.Limit > 100 || !validViewRevision(revision) || page.After > 0 && revision == "" {
		return DocumentSearchPage{}, ErrInvalid
	}
	out := DocumentSearchPage{DocumentPage: DocumentPage{Documents: []Document{}, NextAfter: page.After}}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, header Book) error {
		state, err := captureReadState(ctx, tx, scope, header, recordedReadScope{})
		if err != nil {
			return err
		}
		out.RecordedState = state.RecordedState
		plan, err := compileSearch(ctx, tx, scope, query, BookToday(header, state.cutoff), false)
		if err != nil {
			return err
		}
		matchSQL, args := plan.sourceMatchesSQL()
		var sequences []int64
		var available int64
		if matchSQL != "" {
			matchSQL += `SELECT (SELECT count(*) FROM matches),(SELECT count(*) FROM available),
				ARRAY(SELECT sequence FROM matches WHERE sequence>` + args.add(page.After) +
				` ORDER BY sequence LIMIT ` + args.add(page.Limit+1) + `)`
			if err := tx.QueryRow(ctx, matchSQL, args...).Scan(&out.Total, &available, &sequences); err != nil {
				return dbError(err)
			}
		}
		// Within a recorded revision, ready originals can only leave the
		// population by expiring. Bind the cursor to that live population as
		// well as the query so later pages cannot mix incompatible matches.
		out.Revision = archiveHash(raw(struct {
			State, Query string
			Available    int64
		}{state.StateRevision, strings.TrimSpace(query), available}))
		if revision != "" && revision != out.Revision {
			return ErrConflict
		}
		out.HasMore = len(sequences) > page.Limit
		if out.HasMore {
			sequences = sequences[:page.Limit]
		}
		if len(sequences) == 0 {
			return nil
		}
		rows, err := tx.Query(ctx, "SELECT "+documentColumns+` FROM public.documents
			WHERE tenant_id=$1 AND book_id=$2 AND sequence=ANY($3) ORDER BY sequence`, scope.TenantID, scope.BookID, sequences)
		if err != nil {
			return dbError(err)
		}
		out.Documents, err = scanDocuments(rows)
		out.NextAfter = sequences[len(sequences)-1]
		return err
	})
	if err != nil {
		return DocumentSearchPage{}, err
	}
	return out, nil
}

func (a TenantAdmin) Document(ctx context.Context, book, id string) (DocumentInspection, error) {
	var out DocumentInspection
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		var err error
		out, err = documentInspection(ctx, tx, scope, raw(DocumentQuery{DocumentID: id}))
		return err
	})
	if err != nil {
		return DocumentInspection{}, err
	}
	return out, nil
}

// DownloadDocument holds the owner's Book read while checking authenticated EOF.
// Callers discard partial output unless this and their outer owner lease succeed.
func (a TenantAdmin) DownloadDocument(ctx context.Context, book, id string, vault *documents.Vault, dst io.Writer) error {
	if !ValidID(id) || dst == nil {
		return ErrInvalid
	}
	if vault == nil {
		return documents.ErrUnavailable
	}
	return a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		var held string
		if err := tx.QueryRow(ctx, "SELECT id::text FROM public.books WHERE tenant_id=$1 AND id=$2 FOR SHARE", scope.TenantID, scope.BookID).Scan(&held); err != nil {
			return dbError(err)
		}
		_, err := downloadOriginal(ctx, tx, scope, id, vault, dst)
		return err
	})
}

func downloadOriginal(ctx context.Context, tx pgx.Tx, scope Scope, id string, vault *documents.Vault, dst io.Writer) (Document, error) {
	doc, err := readDocument(ctx, tx, scope, id)
	if err != nil {
		return Document{}, dbError(err)
	}
	if doc.State != "ready" || !doc.live {
		return Document{}, ErrNotFound
	}
	if err := vault.Read(ctx, doc.object(scope), dst); err != nil {
		return Document{}, err
	}
	return doc.Document, nil
}
