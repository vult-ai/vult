package access

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/ledger"
)

type BookViewSelection struct {
	Period string        `json:"period"`
	Range  BookViewRange `json:"range"`
}

type BookViewRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func bookViewScope(identity string) string {
	return Hash("book-view:" + identity)
}

// BookViewPreferences returns the current owner's scope and their selections
// for the Books in the catalog.
func (s *Service) BookViewPreferences(ctx context.Context, raw string, books []string) (string, map[string]BookViewSelection, error) {
	if !validToken(raw) {
		return "", nil, ErrDenied
	}
	rows, err := s.auth.Query(ctx, `SELECT identity_id::text,book_id::text,period,range_from,range_to
		FROM access.owner_book_views($1,$2,$3::text[]::uuid[])`, Hash(raw), s.origin, books)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	views := make(map[string]BookViewSelection)
	var identity string
	for rows.Next() {
		var book, period *string
		var from, to *time.Time
		if err := rows.Scan(&identity, &book, &period, &from, &to); err != nil {
			return "", nil, err
		}
		if book != nil {
			views[*book] = BookViewSelection{
				Period: *period,
				Range:  BookViewRange{From: from.Format(time.DateOnly), To: to.Format(time.DateOnly)},
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	if identity == "" {
		return "", nil, ErrDenied
	}
	return bookViewScope(identity), views, nil
}

func (s *Service) SaveBookViewPreference(ctx context.Context, raw, book, scope string, selection BookViewSelection) error {
	from, to := selection.Range.From, selection.Range.To
	if scope == "" || selection.Period == "" || !ledger.ValidDate(from) || !ledger.ValidDate(to) || from > to {
		return ledger.ErrInvalid
	}
	tenant, err := s.BookTenant(ctx, raw, book)
	if err != nil {
		return err
	}
	// A display choice uses the current owner session without fresh sign-in.
	return s.withOwner(ctx, raw, tenant, true, false, "", func(tx pgx.Tx, _ ledger.TenantAdmin, identity string) error {
		if scope != bookViewScope(identity) {
			return ErrDenied
		}
		_, err := tx.Exec(ctx, `INSERT INTO access.book_view_preferences AS saved
			(identity_id,tenant_id,book_id,period,range_from,range_to) VALUES($1,$2,$3,$4,$5::text::date,$6::text::date)
			ON CONFLICT(identity_id,book_id) DO UPDATE
			SET period=excluded.period,range_from=excluded.range_from,range_to=excluded.range_to
			WHERE (saved.period,saved.range_from,saved.range_to)
				IS DISTINCT FROM (excluded.period,excluded.range_from,excluded.range_to)`,
			identity, tenant, book, selection.Period, from, to)
		var p *pgconn.PgError
		if errors.As(err, &p) {
			switch p.Code {
			case "23503":
				return ledger.ErrNotFound
			case "23514":
				return ledger.ErrInvalid
			}
		}
		return err
	})
}
