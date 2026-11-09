package access

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/ledger"
)

// Owner actions taken in the local browser run in the ledger service, which
// holds the operator pool. Each one checks the owner's live browser session
// against the database itself, and requires a passkey sign-in from the last
// five minutes, so a signed-in browser left open is not enough.

// OwnerTenant is the one tenant the session owns on a local installation.
func (o *OwnerDocumentReader) OwnerTenant(ctx context.Context, session string) (string, error) {
	if o == nil || !validToken(session) {
		return "", ErrDenied
	}
	rows, err := o.db.Query(ctx, "SELECT access.session_tenants($1,$2)::text LIMIT 2", Hash(session), o.origin)
	if err != nil {
		return "", ErrDenied
	}
	tenants, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(tenants) != 1 {
		return "", ErrDenied
	}
	return tenants[0], nil
}

// CreateBook creates a Book for the owner of a fresh local browser session.
func (o *OwnerDocumentReader) CreateBook(ctx context.Context, session, name, currency string, scale int) (ledger.Book, error) {
	tenant, err := o.OwnerTenant(ctx, session)
	if err != nil {
		return ledger.Book{}, err
	}
	var book ledger.Book
	err = withOwnerPool(ctx, o.db, o.origin, session, tenant, true, true, "local-book:"+tenant, func(_ pgx.Tx, owner ledger.TenantAdmin, _ string) error {
		var err error
		book, err = owner.CreateBook(ctx, name, currency, scale)
		return err
	})
	return book, err
}

// ConnectAgent gives an agent on this computer access to one Book. An agent
// that already has a live connection with the same name, Book and access keeps
// it, so its recorded work stays with the same actor.
func (o *OwnerDocumentReader) ConnectAgent(ctx context.Context, session, book, name, access string) (ledger.Connection, error) {
	name = strings.TrimSpace(name)
	if !ledger.ValidID(book) || name == "" || len(name) > 80 || !ledger.ValidAccess(access) {
		return ledger.Connection{}, ledger.ErrInvalid
	}
	tenant, err := ownerResourceTenant(ctx, o.db, session, o.origin, "book", book)
	if err != nil {
		return ledger.Connection{}, err
	}
	var connection ledger.Connection
	err = withOwnerPool(ctx, o.db, o.origin, session, tenant, true, true, "local-agent:"+tenant, func(_ pgx.Tx, owner ledger.TenantAdmin, _ string) error {
		live, err := owner.ActiveConnectionsBounded(ctx, 1000)
		if err != nil {
			return err
		}
		for _, c := range live {
			if c.BookID == book && c.Name == name && c.Access == access && c.RevokedAt == nil {
				connection = c
				return nil
			}
		}
		connection, err = owner.CreateConnection(ctx, book, name, access)
		return err
	})
	return connection, err
}

// RecentOwner checks that the session owns this installation and signed in
// with the passkey in the last five minutes.
func (o *OwnerDocumentReader) RecentOwner(ctx context.Context, session string) error {
	tenant, err := o.OwnerTenant(ctx, session)
	if err != nil {
		return err
	}
	return withOwnerPool(ctx, o.db, o.origin, session, tenant, false, true, "", func(pgx.Tx, ledger.TenantAdmin, string) error { return nil })
}

// OwnsBook reports whether a live owner session may read the Book.
func (o *OwnerDocumentReader) OwnsBook(ctx context.Context, session, book string) error {
	return o.readDocumentOwner(ctx, session, book, func(ledger.TenantAdmin) error { return nil })
}
