package ledger

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

const maxBookAliases = 256

type BookAddress struct {
	BookID   string   `json:"book_id"`
	Code     string   `json:"code"`
	Slug     string   `json:"slug"`
	Revision int64    `json:"revision"`
	Aliases  []string `json:"aliases"`
}

type BookAddressView struct {
	SchemaVersion int `json:"schema_version"`
	BookAddress
	RecordedState
}

const bookAddressColumns = "book_id::text,code,slug,revision,aliases"

func scanBookAddress(row pgx.Row) (BookAddress, error) {
	var a BookAddress
	err := row.Scan(&a.BookID, &a.Code, &a.Slug, &a.Revision, &a.Aliases)
	return a, dbError(err)
}

func (a *BookAddress) includeCode() {
	a.Aliases = append([]string{strings.ToLower(a.Code)}, a.Aliases...)
}

func (a *BookAddress) hideAliases(conflicts map[string]bool) {
	if conflicts[a.Slug] {
		a.Slug = ""
	}
	a.Aliases = slices.DeleteFunc(a.Aliases, func(alias string) bool { return conflicts[alias] })
}

// DisambiguateBookAddresses uses fixed codes for aliases that became ambiguous
// when separately owned Books were brought into the same owner catalog.
func DisambiguateBookAddresses(addresses []BookAddress) {
	first := map[string]string{}
	conflicts := map[string]bool{}
	for _, a := range addresses {
		for _, alias := range a.Aliases {
			if id, ok := first[alias]; ok && id != a.BookID {
				conflicts[alias] = true
			} else {
				first[alias] = a.BookID
			}
		}
	}
	for i := range addresses {
		addresses[i].hideAliases(conflicts)
	}
}

func bookAliasConflicts(ctx context.Context, tx pgx.Tx, scope Scope, aliases []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(aliases) == 0 {
		return out, nil
	}
	var found []string
	if err := tx.QueryRow(ctx, "SELECT access.book_conflicting_aliases($1,$2,$3)", scope.TenantID, scope.BookID, aliases).Scan(&found); err != nil {
		return nil, dbError(err)
	}
	for _, alias := range found {
		out[alias] = true
	}
	return out, nil
}

func (a TenantAdmin) BookAddressesBounded(ctx context.Context, limit int) ([]BookAddress, error) {
	if !a.valid() {
		return nil, ErrDenied
	}
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := a.db.Query(ctx, "SELECT "+bookAddressColumns+" FROM public.book_addresses WHERE tenant_id=$1 ORDER BY book_id LIMIT $2", a.tenantID, limit+1)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []BookAddress{}
	for rows.Next() {
		address, err := scanBookAddress(rows)
		if err != nil {
			return nil, err
		}
		address.includeCode()
		out = append(out, address)
	}
	if len(out) > limit {
		return nil, ErrLimit
	}
	return out, dbError(rows.Err())
}

func (a TenantAdmin) BookAddress(ctx context.Context, book string) (BookAddressView, error) {
	out := BookAddressView{SchemaVersion: 1}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, b Book) error {
		address, err := scanBookAddress(tx.QueryRow(ctx, "SELECT "+bookAddressColumns+" FROM public.book_addresses WHERE tenant_id=$1 AND book_id=$2", scope.TenantID, scope.BookID))
		if err != nil {
			return err
		}
		conflicts, err := bookAliasConflicts(ctx, tx, scope, address.Aliases)
		if err != nil {
			return err
		}
		address.hideAliases(conflicts)
		address.includeCode()
		out.BookAddress = address
		state, err := captureReadState(ctx, tx, scope, b, recordedReadScope{AllAccepted: true})
		out.RecordedState = state.RecordedState
		return err
	})
	return out, err
}

func (a TenantAdmin) SetBookAddress(ctx context.Context, book, slug string, revision int64) (BookAddressView, error) {
	if revision < 0 || revision > maxBrowserRevision {
		return BookAddressView{}, ErrInvalid
	}
	slug = strings.ToLower(strings.TrimSpace(slug))
	out := BookAddressView{SchemaVersion: 1}
	err := a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, b Book) error {
		if slug != "" {
			var valid bool
			if err := tx.QueryRow(ctx, "SELECT public.book_slug_valid($1)", slug).Scan(&valid); err != nil {
				return dbError(err)
			}
			if !valid {
				return ErrInvalid
			}
		}
		var actor string
		if err := tx.QueryRow(ctx, "SELECT access.require_owner($1,true)::text", scope.TenantID).Scan(&actor); err != nil {
			return dbError(err)
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "book-address:"+actor); err != nil {
			return dbError(err)
		}
		var held string
		if err := tx.QueryRow(ctx, "SELECT id::text FROM public.books WHERE tenant_id=$1 AND id=$2 FOR NO KEY UPDATE",
			scope.TenantID, scope.BookID).Scan(&held); err != nil {
			return dbError(err)
		}
		address, err := scanBookAddress(tx.QueryRow(ctx, "SELECT "+bookAddressColumns+" FROM public.book_addresses WHERE tenant_id=$1 AND book_id=$2 FOR UPDATE", scope.TenantID, scope.BookID))
		if err != nil {
			return err
		}
		newAlias := slug != "" && !slices.Contains(address.Aliases, slug)
		candidates := address.Aliases
		if newAlias {
			candidates = append(slices.Clone(address.Aliases), slug)
		}
		conflicts, err := bookAliasConflicts(ctx, tx, scope, candidates)
		if err != nil {
			return err
		}
		if conflicts[slug] {
			return ErrConflict
		}
		if address.Slug != slug {
			if revision != address.Revision || revision == maxBrowserRevision {
				return ErrConflict
			}
			if len(candidates) > maxBookAliases {
				return ErrLimit
			}
			address.Aliases = candidates
			if _, err = tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, scope.BookID); err != nil {
				return dbError(err)
			}
			address.Slug, address.Revision = slug, revision+1
			if _, err = tx.Exec(ctx, `UPDATE public.book_addresses SET slug=$3,revision=$4,aliases=$5 WHERE tenant_id=$1 AND book_id=$2`,
				scope.TenantID, scope.BookID, address.Slug, address.Revision, address.Aliases); err != nil {
				return dbError(err)
			}
		}
		address.hideAliases(conflicts)
		address.includeCode()
		out.BookAddress = address
		b, err = readBook(ctx, tx, scope)
		if err != nil {
			return dbError(err)
		}
		state, err := captureReadState(ctx, tx, scope, b, recordedReadScope{AllAccepted: true})
		out.RecordedState = state.RecordedState
		return err
	})
	return out, err
}
