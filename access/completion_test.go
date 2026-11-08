package access_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestOwnerCompletionRejectsClockExpiredReadsAndWrites(t *testing.T) {
	for _, writing := range []bool{false, true} {
		name := "read"
		if writing {
			name = "write"
		}
		t.Run(name, func(t *testing.T) {
			f := fresh(t)
			tenant, session, _ := f.owner(t, "example")
			original := f.book(t, tenant, session)
			if err := f.service.WithOwner(f.db.Context, session, tenant, false, func(owner ledger.TenantAdmin) error {
				books, err := owner.Books(f.db.Context)
				if err != nil || len(books) != 1 || books[0].ID != original.ID {
					t.Fatal("live owner control could not read its Book", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var expires time.Time
			if err := f.db.Admin.QueryRow(f.db.Context, "UPDATE access.sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE token_hash=$1 RETURNING expires_at", access.Hash(session)).Scan(&expires); err != nil {
				t.Fatal(err)
			}
			called := false
			created := ledger.Book{}
			err := f.service.WithOwner(f.db.Context, session, tenant, writing, func(owner ledger.TenantAdmin) error {
				called = true
				if writing {
					var err error
					created, err = owner.CreateBook(f.db.Context, "Example uncommitted Book", "USD", 2)
					if err != nil {
						return err
					}
				}
				if wait := time.Until(expires) + 5*time.Millisecond; wait > 0 {
					time.Sleep(wait)
				}
				if !writing {
					// Row policies now hide the Book. A successful empty read must
					// not be presented as the owner's complete, authorized result.
					books, err := owner.Books(f.db.Context)
					if err != nil || len(books) != 0 {
						t.Fatal("expiry did not exercise clock-sensitive row policies", err)
					}
				}
				return nil
			})
			if !called || !errors.Is(err, access.ErrDenied) {
				t.Fatal("expired callback completed successfully", err, called)
			}
			var books int
			if err = f.db.Admin.QueryRow(f.db.Context, "SELECT count(*)FROM public.books WHERE tenant_id=$1", tenant).Scan(&books); err != nil || books != 1 {
				t.Fatal("expired write committed or original Book changed", err)
			}
			if writing && !ledger.ValidID(created.ID) {
				t.Fatal("write control expired before its provisional effect")
			}
		})
	}
}

func TestOwnerCompletionRequiresFreshProofOnlyWhenRequested(t *testing.T) {
	f := fresh(t)
	tenant, session, _ := f.owner(t, "example")
	original := f.book(t, tenant, session)
	var expires time.Time
	if err := f.db.Admin.QueryRow(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '5 minutes'+interval '1 second' WHERE token_hash=$1 RETURNING authenticated_at+interval '5 minutes'", access.Hash(session)).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	called := false
	err := f.service.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
		called = true
		if _, err := owner.CreateBook(f.db.Context, "Example uncommitted Book", "USD", 2); err != nil {
			return err
		}
		if wait := time.Until(expires) + 5*time.Millisecond; wait > 0 {
			time.Sleep(wait)
		}
		return nil
	})
	if !called || !errors.Is(err, access.ErrDenied) {
		t.Fatal("write completed after required proof expired", err, called)
	}
	if err = f.service.WithOwner(f.db.Context, session, tenant, false, func(owner ledger.TenantAdmin) error {
		books, err := owner.Books(f.db.Context)
		if err != nil || len(books) != 1 || books[0].ID != original.ID {
			t.Fatal("expired proof blocked a valid read or retained an uncommitted write", err)
		}
		return nil
	}); err != nil {
		t.Fatal("read unexpectedly required fresh proof", err)
	}
}

func TestOwnerCompletionChecksMissingBookSettingsOutcome(t *testing.T) {
	f := fresh(t)
	tenant, session, _ := f.owner(t, "example")
	book := f.book(t, tenant, session)
	live, err := f.service.BookSettingsOutcome(f.db.Context, session, tenant, book.ID, "missing")
	if err != nil || live.Status != "unresolved" {
		t.Fatal("live missing-outcome control failed", err)
	}
	trace := &expireOwnerRead{queryPrefix: "SELECT 'recorded','revise_book_settings',result_bytes FROM public.book_revisions"}
	if err = f.db.Admin.QueryRow(f.db.Context, "UPDATE access.sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE token_hash=$1 RETURNING expires_at", access.Hash(session)).Scan(&trace.expires); err != nil {
		t.Fatal(err)
	}
	config := f.owners.Config()
	config.ConnConfig.Tracer = trace
	owners, err := pgxpool.NewWithConfig(f.db.Context, config)
	if err != nil {
		t.Fatal(err)
	}
	defer owners.Close()
	service, err := access.New(f.auth, owners, "https://ledger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.BookSettingsOutcome(f.db.Context, session, tenant, book.ID, "missing")
	if !errors.Is(err, access.ErrDenied) || trace.reads.Load() != 1 {
		t.Fatal("expired outcome lookup was reported as unresolved", err, trace.reads.Load())
	}
}
