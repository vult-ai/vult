package ledger

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/legalidentity"
)

const maxBrowserRevision = 9007199254740991
const bookIdentityPageSize = 10
const MaxBookIdentityOffset = 10000

type BookIdentityChange struct {
	BookID        string               `json:"book_id"`
	Revision      *int64               `json:"revision"`
	Kind          string               `json:"kind,omitempty"`
	Identity      *legalidentity.Input `json:"identity"`
	Reason        string               `json:"reason"`
	EffectiveDate string               `json:"effective_date,omitempty"`
	RequestKey    string               `json:"idempotency_key"`
}

type BookIdentitySubject struct {
	ID       string                 `json:"id"`
	Kind     *string                `json:"kind"`
	Revision int64                  `json:"revision"`
	Identity legalidentity.Snapshot `json:"identity"`
}

type BookIdentityHistory struct {
	ID            string                 `json:"id"`
	Revision      int64                  `json:"revision"`
	PriorKind     *string                `json:"prior_kind"`
	NewKind       *string                `json:"new_kind"`
	PriorIdentity legalidentity.Snapshot `json:"prior_identity"`
	NewIdentity   legalidentity.Snapshot `json:"new_identity"`
	Actor         string                 `json:"actor"`
	Reason        string                 `json:"reason"`
	EffectiveDate *string                `json:"effective_date"`
	RecordedAt    time.Time              `json:"recorded_at"`
}

type BookIdentityView struct {
	SchemaVersion int                   `json:"schema_version"`
	Subject       BookIdentitySubject   `json:"subject"`
	History       []BookIdentityHistory `json:"history"`
	HasMore       bool                  `json:"has_more"`
	NextOffset    int                   `json:"next_offset"`
	RecordedState
}

func currentBookIdentity(ctx context.Context, tx pgx.Tx, scope Scope) (BookIdentitySubject, error) {
	out := BookIdentitySubject{ID: scope.BookID, Identity: legalidentity.Snapshot{CatalogVersion: legalidentity.CatalogVersion, Status: "unknown"}}
	err := tx.QueryRow(ctx, `SELECT revision,new_kind,new_identity FROM public.book_identity_revisions
		WHERE tenant_id=$1 AND book_id=$2 ORDER BY revision DESC LIMIT 1`, scope.TenantID, scope.BookID).Scan(&out.Revision, &out.Kind, &out.Identity)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	return out, dbError(err)
}

func readBookIdentity(ctx context.Context, tx pgx.Tx, scope Scope, book Book, offset int) (BookIdentityView, error) {
	out := BookIdentityView{SchemaVersion: 1, History: []BookIdentityHistory{}}
	subject, err := currentBookIdentity(ctx, tx, scope)
	if err != nil {
		return out, err
	}
	out.Subject = subject
	rows, err := tx.Query(ctx, `SELECT id::text,revision,prior_kind,new_kind,prior_identity,new_identity,actor_id::text,
		reason,effective_date::text,recorded_at FROM public.book_identity_revisions
		WHERE tenant_id=$1 AND book_id=$2 ORDER BY revision DESC LIMIT $3 OFFSET $4`, scope.TenantID, scope.BookID, bookIdentityPageSize+1, offset)
	if err != nil {
		return out, dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var h BookIdentityHistory
		if err := rows.Scan(&h.ID, &h.Revision, &h.PriorKind, &h.NewKind, &h.PriorIdentity, &h.NewIdentity, &h.Actor,
			&h.Reason, &h.EffectiveDate, &h.RecordedAt); err != nil {
			return out, dbError(err)
		}
		out.History = append(out.History, h)
	}
	if err := rows.Err(); err != nil {
		return out, dbError(err)
	}
	if len(out.History) > bookIdentityPageSize {
		out.History = out.History[:bookIdentityPageSize]
		out.HasMore = offset+bookIdentityPageSize <= MaxBookIdentityOffset
	}
	out.NextOffset = offset + len(out.History)
	state, err := captureReadState(ctx, tx, scope, book, recordedReadScope{AllAccepted: true})
	out.RecordedState = state.RecordedState
	return out, err
}

func (a TenantAdmin) BookIdentity(ctx context.Context, book string, offset int) (BookIdentityView, error) {
	if offset < 0 || offset > MaxBookIdentityOffset {
		return BookIdentityView{}, ErrInvalid
	}
	var out BookIdentityView
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, b Book) error {
		var err error
		out, err = readBookIdentity(ctx, tx, scope, b, offset)
		return err
	})
	return out, err
}

type BookIdentityReceipt struct {
	SchemaVersion int    `json:"schema_version"`
	BookID        string `json:"book_id"`
	Revision      int64  `json:"revision"`
	RecordedState
}

func (a TenantAdmin) ReviseBookIdentity(ctx context.Context, change BookIdentityChange) (BookIdentityReceipt, error) {
	reason := strings.TrimSpace(change.Reason)
	if change.Revision == nil || *change.Revision < 0 || *change.Revision >= maxBrowserRevision ||
		change.Identity == nil || reason == "" || utf8.RuneCountInString(reason) > 1000 || strings.ContainsFunc(reason, unicode.IsControl) ||
		(change.EffectiveDate != "" && !ValidDate(change.EffectiveDate)) {
		return BookIdentityReceipt{}, ErrInvalid
	}
	switch change.Kind {
	case "", "person", "household", "legal_entity", "trust", "estate", "fund", "informal_activity", "other":
	default:
		return BookIdentityReceipt{}, ErrInvalid
	}
	identity, err := legalidentity.Validate(*change.Identity)
	if err != nil {
		return BookIdentityReceipt{}, ErrInvalid
	}
	if len(raw(identity)) > 30000 {
		return BookIdentityReceipt{}, ErrLimit
	}
	var out BookIdentityReceipt
	err = a.withBookTransaction(ctx, change.BookID, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, b Book) error {
		var actor string
		if err := tx.QueryRow(ctx, "SELECT access.require_owner($1,true)::text", scope.TenantID).Scan(&actor); err != nil {
			return dbError(err)
		}
		if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, scope.BookID); err != nil {
			return dbError(err)
		}
		before, err := currentBookIdentity(ctx, tx, scope)
		if err != nil {
			return err
		}
		if before.Revision != *change.Revision {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.book_identity_revisions
			(tenant_id,book_id,revision,id,prior_kind,new_kind,prior_identity,new_identity,actor_id,reason,effective_date)
			VALUES($1,$2,$3,$4,$5,nullif($6,''),$7,$8,$9,$10,nullif($11,'')::date)`,
			scope.TenantID, scope.BookID, before.Revision+1, ID(), before.Kind, change.Kind,
			before.Identity, identity, actor, reason, change.EffectiveDate)
		if err != nil {
			return dbError(err)
		}
		b, err = readBook(ctx, tx, scope)
		if err != nil {
			return dbError(err)
		}
		state, err := captureReadState(ctx, tx, scope, b, recordedReadScope{AllAccepted: true})
		out = BookIdentityReceipt{1, scope.BookID, before.Revision + 1, state.RecordedState}
		return err
	})
	return out, err
}
