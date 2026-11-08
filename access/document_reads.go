package access

import (
	"context"
	"io"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

// OwnerDocumentReader serves owner document transfers through the native
// key-holding service. It exposes no installation or Book administration.
type OwnerDocumentReader struct {
	db          *pgxpool.Pool
	origin      string
	uploadSlots chan struct{}
}

func NewOwnerDocumentReader(ctx context.Context, db *pgxpool.Pool, origin string) (*OwnerDocumentReader, error) {
	u, err := url.Parse(origin)
	if db == nil || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return nil, ErrDenied
	}
	var operator bool
	if err := db.QueryRow(ctx, "SELECT public.ledger_import_owner() AND current_user=session_user").Scan(&operator); err != nil || !operator {
		return nil, ErrDenied
	}
	return &OwnerDocumentReader{db: db, origin: origin, uploadSlots: make(chan struct{}, 4)}, nil
}

// readDocumentOwner uses the operator's existing pool while requiring the same
// live browser session and owner lease as a restricted browser connection.
func (o *OwnerDocumentReader) readDocumentOwner(ctx context.Context, session, book string, read func(ledger.TenantAdmin) error) error {
	if o == nil || !ledger.ValidID(book) || !validToken(session) {
		return ledger.ErrDenied
	}
	tenant, err := ownerResourceTenant(ctx, o.db, session, o.origin, "book", book)
	if err != nil {
		return err
	}
	return withOwnerPool(ctx, o.db, o.origin, session, tenant, false, false, "", func(_ pgx.Tx, owner ledger.TenantAdmin, _ string) error {
		return read(owner)
	})
}

func (o *OwnerDocumentReader) Document(ctx context.Context, session, book, id string) (ledger.DocumentInspection, error) {
	var out ledger.DocumentInspection
	err := o.readDocumentOwner(ctx, session, book, func(owner ledger.TenantAdmin) error {
		var err error
		out, err = owner.Document(ctx, book, id)
		return err
	})
	if err != nil {
		return ledger.DocumentInspection{}, err
	}
	return out, nil
}

// Download keeps keys in the operator service. This grants no native
// administration: the supplied browser session must still own the exact Book.
func (o *OwnerDocumentReader) Download(ctx context.Context, session, book, id string, vault *documents.Vault, dst io.Writer) error {
	if o == nil || !ledger.ValidID(book) {
		return ledger.ErrDenied
	}
	tenant, err := ownerResourceTenant(ctx, o.db, session, o.origin, "book", book)
	if err != nil {
		return err
	}
	// Keep the original available for the copy without holding locks on the
	// session or identity. The caller must check the final result.
	if err = readOwnerSnapshot(ctx, o.db, o.origin, session, tenant, pgx.ReadWrite, func(owner ledger.TenantAdmin) error {
		return owner.DownloadDocument(ctx, book, id, vault, dst)
	}); err != nil {
		return err
	}
	current, err := ownerResourceTenant(ctx, o.db, session, o.origin, "book", book)
	if err != nil {
		return err
	}
	if current != tenant {
		return ErrDenied
	}
	return nil
}
