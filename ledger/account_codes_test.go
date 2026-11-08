package ledger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestOptionalAccountCodesPreserveIdentityAndReceipts(t *testing.T) {
	db := testutil.New(t)
	book, scope := db.Book(t, "Example Book")
	input := map[string]any{"request_key": "uncoded", "class": "asset", "name": "Example asset"}
	first := db.Call(t, scope, "create_account", input)
	var debit, credit ledger.Account
	if json.Unmarshal(first, &debit) != nil || debit.Code != "" || !ledger.ValidID(debit.ID) {
		t.Fatal("account without a code was not preserved")
	}
	if json.Unmarshal(db.Call(t, scope, "create_account", ledger.CreateAccount{RequestKey: "second", Class: "equity", Name: "Example equity"}), &credit) != nil || credit.Code != "" || debit.ID == credit.ID {
		t.Fatal("empty codes became a shared identity")
	}
	posted := db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, "funding"))
	var entry ledger.Entry
	if json.Unmarshal(posted, &entry) != nil {
		t.Fatal("invalid journal fixture")
	}
	db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: "assign", AccountID: debit.ID, ExpectedRevision: 1, Code: "1000", Name: debit.Name})
	if _, err := db.Store.Call(db.Context, scope, "revise_account", value(ledger.ReviseAccount{RequestKey: "duplicate", AccountID: credit.ID, ExpectedRevision: 1, Code: "1000", Name: credit.Name})); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("nonempty code uniqueness lost", err)
	}
	for _, fields := range []map[string]any{
		{"request_key": "omitted", "account_id": debit.ID, "expected_revision": 2, "name": debit.Name},
		{"request_key": "null", "account_id": debit.ID, "expected_revision": 2, "name": debit.Name, "code": nil},
	} {
		if _, err := db.Store.Call(db.Context, scope, "revise_account", value(fields)); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatal("omitted or null code silently cleared a label", err)
		}
	}
	cleared := db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: "clear", AccountID: debit.ID, ExpectedRevision: 2, Name: debit.Name})
	var current ledger.Account
	if json.Unmarshal(cleared, &current) != nil || current.ID != debit.ID || current.Code != "" || current.Revision != 3 {
		t.Fatal("explicit code removal changed identity")
	}
	input["code"] = ""
	if retry := db.Call(t, scope, "create_account", input); !bytes.Equal(first, retry) {
		t.Fatal("omitted and empty creation code changed the canonical retry")
	}
	if got := db.Call(t, scope, "get_journal", ledger.EntryQuery{EntryID: entry.ID}); !bytes.Equal(posted, got) {
		t.Fatal("code changes rewrote posted effects")
	}
	for _, code := range []any{nil, " ", "bad code", strings.Repeat("a", 33), 1000} {
		fields := map[string]any{"request_key": "invalid", "class": "asset", "name": "Example invalid", "code": code}
		if _, err := db.Store.Call(db.Context, scope, "create_account", value(fields)); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatal("invalid optional code accepted", err)
		}
	}
	before, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	if err = target.Owner.Restore(target.Context, before); err != nil {
		t.Fatal("uncoded accounts could not be restored", err)
	}
	after, err := target.Owner.Snapshot(target.Context)
	if err != nil || !bytes.Equal(value(before.Tables), value(after.Tables)) {
		t.Fatal("restore changed uncoded accounts or receipts", err)
	}
	want, err := db.Owner.Export(db.Context, book.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := target.Owner.Export(target.Context, book.ID)
	if err != nil || !bytes.Equal(value(want), value(got)) {
		t.Fatal("uncoded export changed after restore", err)
	}
	vault, _ := restoreVault(t)
	var archive bytes.Buffer
	if err = db.Owner.WriteArchive(db.Context, vault, &archive, book.ID); err != nil {
		t.Fatal("uncoded accounts could not be archived", err)
	}
	archived := testutil.New(t)
	destination, _ := restoreVault(t)
	if err = archived.Owner.RestoreArchive(archived.Context, destination, bytes.NewReader(archive.Bytes()), nil); err != nil {
		t.Fatal("uncoded archive could not be restored", err)
	}
	got, err = archived.Owner.Export(archived.Context, book.ID)
	if err != nil || !bytes.Equal(value(want), value(got)) {
		t.Fatal("portable archive changed uncoded accounts", err)
	}
}

func TestOptionalAccountCodeConstraintRejectsInvalidDirectWrites(t *testing.T) {
	db, _, scope, _, _ := setup(t)
	for _, code := range []string{" ", "bad code", strings.Repeat("a", 33)} {
		tx := scopedTransaction(t, db, scope, pgx.ReadCommitted)
		id := ledger.ID()
		if _, err := tx.Exec(db.Context, "INSERT INTO public.accounts(tenant_id,book_id,id,class)VALUES($1,$2,$3,'asset')", scope.TenantID, scope.BookID, id); err != nil {
			t.Fatal(err)
		}
		_, err := tx.Exec(db.Context, "INSERT INTO public.account_revisions(tenant_id,book_id,account_id,revision,code,name,actor_id)VALUES($1,$2,$3,1,$4,'Example account',$5)", scope.TenantID, scope.BookID, id, code, scope.ActorID)
		var check *pgconn.PgError
		// The BEFORE trigger projects the revision onto accounts, whose
		// existing syntax constraint rejects the change before insertion.
		if !errors.As(err, &check) || check.Code != "23514" || check.ConstraintName != "accounts_code_check" {
			t.Fatal("database did not enforce code syntax", err)
		}
		if err = tx.Rollback(db.Context); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOptionalAccountCodesUpgradePreservesReleasedAccounts(t *testing.T) {
	db := testutil.NewWithInitializer(t, releasedSchema)
	book, scope := db.Book(t, "Example previous Book")
	debit := db.ReleasedAccount(t, scope, "1000", "asset")
	credit := db.ReleasedAccount(t, scope, "3000", "equity")
	q := post(debit.ID, credit.ID, "previous-entry")
	receipt := db.ReleasedPost(t, scope, q)
	before := db.ReleasedSnapshot(t)
	if err := ledger.Upgrade(db.Context, db.Admin, db.Role); err != nil {
		t.Fatal(err)
	}
	after, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	testutil.AssertReleasedSnapshotUpgrade(t, before, after, book)
	if retry := db.Call(t, scope, "post_journal", q); !bytes.Equal(retry, receipt) {
		t.Fatal("upgrade changed the receipt")
	}
	var account ledger.Account
	if json.Unmarshal(db.Call(t, scope, "create_account", ledger.CreateAccount{RequestKey: "uncoded", Class: "expense", Name: "Example expense"}), &account) != nil ||
		account.Code != "" || !ledger.ValidID(account.ID) {
		t.Fatal("upgraded Book cannot add an uncoded account")
	}
}
