package ledger_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func proposingScope(t *testing.T, db *testutil.Database, book string) ledger.Scope {
	t.Helper()
	c, err := db.Owner.CreateConnection(db.Context, book, "proposing agent", "propose")
	if err != nil {
		t.Fatal(err)
	}
	return ledger.Scope{TenantID: c.TenantID, BookID: c.BookID, ActorID: c.ActorID, ConnectionID: c.ID, Access: "propose"}
}

func countEntries(t *testing.T, db *testutil.Database) int {
	t.Helper()
	var n int
	if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.entries").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A proposing agent reads and prepares entries for approval, but never
// records anything itself.
func TestProposingAgentPreparesButNeverRecords(t *testing.T) {
	db, book, _, a, b := setup(t)
	s := proposingScope(t, db, book.ID)
	db.Call(t, s, "list_accounts", struct{}{})
	p := prepareOperation(t, db, s, "propose-funding", "post_journal", post(a.ID, b.ID, "proposed-funding"))
	if p.ActorID != s.ActorID || countEntries(t, db) != 0 {
		t.Fatal("preparing a proposal recorded an entry")
	}
	for _, call := range []struct {
		method string
		input  any
	}{
		{"post_journal", post(a.ID, b.ID, "direct-funding")},
		{"create_account", ledger.CreateAccount{RequestKey: "direct-account", Code: "1100", Name: "Example savings", Class: "asset"}},
		{"execute_proposal", ledger.ExecuteProposal{ProposalID: p.ID, SHA256: p.SHA256}},
	} {
		data, _ := json.Marshal(call.input)
		if _, err := db.Store.Call(db.Context, s, call.method, data); !errors.Is(err, ledger.ErrDenied) {
			t.Fatalf("%s was not denied: %v", call.method, err)
		}
	}
	if countEntries(t, db) != 0 {
		t.Fatal("a proposing agent recorded an entry")
	}
}

// The database itself refuses a proposing connection's writes outside the
// two marked transactions, and refuses Book changes while recording one.
func TestProposingConnectionWritesOnlyWhilePreparing(t *testing.T) {
	db, book, _, _, _ := setup(t)
	s := proposingScope(t, db, book.ID)
	allowed := func(phase string) bool {
		tx, err := db.Runtime.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(db.Context)
		var ok bool
		if _, err = tx.Exec(db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),
            set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true),set_config('vult.proposing',$5,true)`,
			s.TenantID, s.BookID, s.ActorID, s.ConnectionID, phase); err != nil {
			t.Fatal(err)
		}
		if err = tx.QueryRow(db.Context, "SELECT public.ledger_allowed($1::uuid,$2::uuid,true)", s.TenantID, s.BookID).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if allowed("") || allowed("anything") || !allowed("preview") || !allowed("record") {
		t.Fatal("proposing write authority is not limited to preparing a proposal")
	}
	tx, err := db.Runtime.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(db.Context)
	if _, err = tx.Exec(db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),
        set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true),set_config('vult.proposing','record',true)`,
		s.TenantID, s.BookID, s.ActorID, s.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(db.Context, "SELECT public.ledger_lock($1,$2)", s.TenantID, s.BookID); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(db.Context, "INSERT INTO public.accounts(tenant_id,book_id,id,class) VALUES($1,$2,gen_random_uuid(),'asset')", s.TenantID, s.BookID)
	if err == nil || !strings.Contains(err.Error(), "a proposal never records changes") {
		t.Fatal("recording a proposal changed the Book", err)
	}
}

// The owner changes an agent's access in place; the agent keeps its actor and
// its next request uses the new level.
func TestOwnerChangesAgentAccess(t *testing.T) {
	db, book, s, a, b := setup(t)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.ChangeAccess(db.Context, s.ConnectionID, "propose"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Store.Call(db.Context, s, "list_accounts", json.RawMessage(`{}`)); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("a stale write scope still works after the change", err)
	}
	s.Access = "propose"
	prepareOperation(t, db, s, "after-change", "post_journal", post(a.ID, b.ID, "after-change"))
	if err := owner.ChangeAccess(db.Context, s.ConnectionID, "admin"); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("an unknown access level was accepted", err)
	}
	if err := owner.Revoke(db.Context, s.ConnectionID); err != nil {
		t.Fatal(err)
	}
	if err := owner.ChangeAccess(db.Context, s.ConnectionID, "write"); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("a disconnected agent's access changed", err)
	}
}
