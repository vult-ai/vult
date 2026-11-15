package agentconfig

import (
	"errors"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func ownedBook(t *testing.T) (*testutil.Database, ledger.Book, ledger.TenantAdmin, string) {
	t.Helper()
	db := testutil.New(t)
	book, _ := db.Book(t, "Example Book")
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureBookOwner(db.Context, book.ID); err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := db.Admin.QueryRow(db.Context, "SELECT identity_id::text FROM access.owners WHERE tenant_id=$1", book.TenantID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	return db, book, owner, actor
}

// With no agent connected to the Vult there is nothing to turn on, and
// nothing is kept for the Book.
func TestAnAgentThatIsNotConnectedCannotBeTurnedOn(t *testing.T) {
	db, book, owner, actor := ownedBook(t)
	s := &Service{Store: db.Store}
	var refused *ledger.RefusedError
	if _, err := s.Save(db.Context, owner, book.ID, actor, Input{Enabled: true}); !errors.As(err, &refused) {
		t.Fatalf("an agent that isn't connected was turned on: %v", err)
	}
	if _, err := owner.AgentSettings(db.Context, book.ID); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatalf("settings were kept for an agent that isn't there: %v", err)
	}
	if books, err := s.Enabled(db.Context); err != nil || len(books) != 0 {
		t.Fatalf("enabled = %+v, %v", books, err)
	}
}

// A connected agent is turned on and off in a Book, through a proposing
// connection made for it.
func TestAConnectedAgentIsTurnedOnAndOff(t *testing.T) {
	db, book, owner, actor := ownedBook(t)
	ctx := db.Context
	s := &Service{Store: db.Store, Provided: &Provided{Name: "the example agent", About: "An example service's agent."}}
	saved, err := s.Save(ctx, owner, book.ID, actor, Input{Enabled: true})
	if err != nil || !saved.Enabled || saved.ConnectionID == "" || saved.Revision != 1 {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	books, err := s.Enabled(ctx)
	if err != nil || len(books) != 1 || books[0].BookID != book.ID {
		t.Fatalf("enabled = %+v, %v", books, err)
	}
	loaded, err := s.Load(ctx, book.TenantID, book.ID)
	if err != nil || !loaded.Enabled || loaded.Scope.ConnectionID != saved.ConnectionID || loaded.Scope.Access != "propose" {
		t.Fatalf("loaded = %+v, %v", loaded, err)
	}
	// A change from an older reading is refused.
	if _, err := s.Save(ctx, owner, book.ID, actor, Input{Enabled: false}); !errors.Is(err, ledger.ErrConflict) {
		t.Fatalf("a stale change = %v", err)
	}
	if saved, err = s.Save(ctx, owner, book.ID, actor, Input{Enabled: false, ExpectedRevision: saved.Revision}); err != nil || saved.Enabled {
		t.Fatalf("turned off = %+v, %v", saved, err)
	}
	if books, _ := s.Enabled(ctx); len(books) != 0 {
		t.Fatal("a turned-off agent is listed")
	}
	// Once the agent is no longer connected, a Book's agent can still be
	// turned off, but not on.
	s.Provided = nil
	if _, err := s.Save(ctx, owner, book.ID, actor, Input{Enabled: true, ExpectedRevision: saved.Revision}); err == nil {
		t.Fatal("an agent that is no longer connected was turned on")
	}
}
