package access_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

func TestOwnerDirectWritesReachOnlyTheOwnersTables(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "example")
	book := f.book(t, tenant, session)
	var actor string
	if err := f.db.Admin.QueryRow(ctx, "SELECT identity_id::text FROM access.owners WHERE tenant_id=$1", tenant).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	// The owner's own account changes still record.
	if err := f.service.WithOwner(ctx, session, tenant, true, func(owner ledger.TenantAdmin) error {
		_, err := owner.AccountChange(ctx, book.ID, actor, "apply_starter_chart", json.RawMessage(`{"request_key":"chart","template":"business-v1"}`))
		return err
	}); err != nil {
		t.Fatal("the owner could not apply a starter chart", err)
	}
	var cash string
	if err := f.db.Admin.QueryRow(ctx, "SELECT id::text FROM public.accounts WHERE book_id=$1 AND role='cash' ORDER BY code LIMIT 1", book.ID).Scan(&cash); err != nil {
		t.Fatal(err)
	}
	// A verified statement document, added by a write agent.
	var c ledger.Connection
	if err := f.service.WithOwner(ctx, session, tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(ctx, book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: tenant, BookID: book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { disk.Close() })
	key, err := documents.NewKeyFile(tenant)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.ParseKeyring(key)
	if err != nil {
		t.Fatal(err)
	}
	store := f.db.Store
	store.Documents = &documents.Vault{Storage: disk, Keys: keys}
	data := []byte("date,amount\n2026-03-10,-100\n")
	sum := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	prepared, err := store.Call(ctx, scope, "add_document", mustJSON(t, ledger.AddDocument{RequestKey: "statement", Name: "statement.csv", MediaType: "text/csv", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}))
	if err != nil || json.Unmarshal(prepared, &intake) != nil {
		t.Fatal("document preparation failed", err)
	}
	if _, err = store.Upload(ctx, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	// Statements are imported by agents; the owner path never writes one.
	insert := func() error {
		tx, err := f.owners.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err = tx.Exec(ctx, `SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin','https://ledger.example.com',true),
 set_config('vult.tenant_id',$2,true),set_config('vult.book_id',$3,true),set_config('vult.actor_id',$4,true)`, access.Hash(session), tenant, book.ID, actor); err != nil {
			return err
		}
		var allowed bool
		if err = tx.QueryRow(ctx, "SELECT public.ledger_allowed($1,$2,true)", tenant, book.ID).Scan(&allowed); err != nil || !allowed {
			t.Fatal("the fixture is not the owner acting directly", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.statements(tenant_id,book_id,id,account_id,kind,currency,scale,document_id,period_start,period_end,
 opening_balance_minor,closing_balance_minor,evidence_state,actor_id,receipt_id) VALUES($1,$2,$3,$4,'cash','USD',2,$5,'2026-03-01','2026-03-31',0,0,'complete',$6,$7)`,
			tenant, book.ID, ledger.ID(), cash, intake.Document.ID, actor, ledger.ID())
		return err
	}
	var pg *pgconn.PgError
	if err := insert(); !errors.As(err, &pg) || pg.Code != "42501" || !strings.Contains(pg.Message, "row-level security") {
		t.Fatal("a direct owner insert outside the owner's tables was not refused", err)
	}
	// Control: without the owner scope, the same insert passes row security.
	if _, err := f.db.Admin.Exec(ctx, "DROP POLICY owner_change_scope ON public.statements"); err != nil {
		t.Fatal(err)
	}
	if err := insert(); err != nil {
		t.Fatal("the refused insert was not otherwise valid", err)
	}
}
