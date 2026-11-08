package access

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/ledger"
)

// ReviseBookSettings holds the live owner lease through the retained outcome or
// new revision. A retry does not require another fresh proof or admission charge.
func (s *Service) ReviseBookSettings(ctx context.Context, session, tenant string, q ledger.ReviseBookSettings, admit OwnerAdmission) (json.RawMessage, error) {
	var result json.RawMessage
	err := s.withOwner(ctx, session, tenant, true, false, "", func(_ pgx.Tx, owner ledger.TenantAdmin, who string) error {
		var err error
		result, err = owner.ReviseBook(ctx, who, q, func(ctx context.Context, tx pgx.Tx) error {
			if admit == nil {
				return nil
			}
			input, marshalErr := json.Marshal(q)
			if marshalErr != nil {
				return ledger.ErrInvalid
			}
			return admit(ctx, tx, tenant, "revise_book_settings", input)
		})
		return err
	})
	return result, err
}

func (s *Service) BookSettingsOutcome(ctx context.Context, session, tenant, book, key string) (ledger.Outcome, error) {
	if !ledger.ValidID(book) || len(key) == 0 || len(key) > 128 {
		return ledger.Outcome{}, ledger.ErrInvalid
	}
	result := ledger.Outcome{Status: "unresolved"}
	err := s.withOwner(ctx, session, tenant, false, false, "", func(tx pgx.Tx, _ ledger.TenantAdmin, who string) error {
		err := tx.QueryRow(ctx, `SELECT 'recorded','revise_book_settings',result_bytes FROM public.book_revisions
 WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4 AND logo_actor IS NULL`, tenant, book, who, key).Scan(&result.Status, &result.Operation, &result.Result)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return result, err
}

// SetBookLogo changes Book presentation under a live owner lease.
func (s *Service) SetBookLogo(ctx context.Context, session, tenant, book string, change ledger.BookLogoChange, admit OwnerAdmission) (json.RawMessage, error) {
	var result json.RawMessage
	err := s.withOwner(ctx, session, tenant, true, false, "", func(tx pgx.Tx, owner ledger.TenantAdmin, _ string) error {
		if admit != nil {
			if err := admit(ctx, tx, tenant, "set_book_logo", change.Request("")); err != nil {
				return ledger.ErrDenied
			}
		}
		var err error
		result, err = owner.SetBookLogo(ctx, book, change)
		return err
	})
	return result, err
}
