package ledger_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestScopedPostingsPreserveExactEffectsAndStableAccounts(t *testing.T) {
	db := testutil.New(t)
	owner, book, scope := tenantOwner(t, db)
	_, other, otherScope := tenantOwner(t, db)
	debit, credit := db.Account(t, scope, "1000", "asset"), db.Account(t, scope, "3000", "equity")
	foreignDebit, foreignCredit := db.Account(t, otherScope, "1000", "asset"), db.Account(t, otherScope, "3000", "equity")
	var first, second, foreign ledger.Entry
	q := post(debit.ID, credit.ID, "first")
	for i := range q.Lines {
		q.Lines[i].AmountMinor = "9007199254740993"
	}
	json.Unmarshal(db.Call(t, scope, "post_journal", q), &first)
	json.Unmarshal(db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, "second")), &second)
	json.Unmarshal(db.Call(t, otherScope, "post_journal", post(foreignDebit.ID, foreignCredit.ID, "foreign")), &foreign)
	wantIDs := []string{second.ID, first.ID}
	before, err := owner.JournalPostings(db.Context, book.ID, wantIDs)
	if err != nil || len(before.Entries) != 2 || before.BookID != book.ID || before.Currency != "USD" || before.Scale != 2 {
		t.Fatal("selected postings unavailable", err)
	}
	if wantIDs[0] != second.ID {
		t.Fatal("reader mutated caller order")
	}
	for _, entry := range before.Entries {
		original := first
		if entry.ID == second.ID {
			original = second
		}
		if len(entry.Lines) != len(original.Lines) {
			t.Fatal("incomplete posted entry")
		}
		for i, line := range entry.Lines {
			l := original.Lines[i]
			if line.ID != l.ID || line.AccountID != l.AccountID || line.Side != l.Side || line.AmountMinor != l.AmountMinor {
				t.Fatal("projection changed a posted effect")
			}
		}
	}
	hash := before.ContentSHA256
	before.ContentSHA256 = ""
	state := before.RecordedState
	before.RecordedState = ledger.RecordedState{}
	digest := sha256.Sum256(value(before))
	before.ContentSHA256 = hash
	before.RecordedState = state
	if hex.EncodeToString(digest[:]) != hash {
		t.Fatal("projection digest does not identify its contents")
	}
	db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: "rename", AccountID: debit.ID, ExpectedRevision: 1, Code: "1010", Name: "Example current name"})
	after, err := owner.JournalPostings(db.Context, book.ID, []string{first.ID, second.ID})
	if err != nil || before.StateRevision == "" || after.StateRevision == before.StateRevision || after.ContentSHA256 != before.ContentSHA256 || !bytes.Equal(value(before.Entries), value(after.Entries)) {
		t.Fatal("request order or editable account attributes changed posted identities", err)
	}
	for _, ids := range [][]string{{first.ID, foreign.ID}, {first.ID, ledger.ID()}} {
		if got, err := owner.JournalPostings(db.Context, book.ID, ids); !errors.Is(err, ledger.ErrNotFound) || len(got.Entries) != 0 {
			t.Fatal("mixed request exposed a partial or foreign entry", err)
		}
	}
	if _, err = owner.JournalPostings(db.Context, other.ID, []string{foreign.ID}); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal("read crossed its tenant", err)
	}
	for _, ids := range [][]string{nil, {first.ID, first.ID}, {"invalid"}, make([]string, 101)} {
		if _, err = owner.JournalPostings(db.Context, book.ID, ids); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatal("invalid bounded selection accepted", err)
		}
	}
}

func TestScopedPostingsKeepZeroBookEffectsVisible(t *testing.T) {
	db, book, scope, _, equity := setup(t)
	cash := measuredAccount(t, db, scope, "1200", "JPY", 0, true)
	q := measuredPost(cash, equity.ID, "rounded-native", "1", "0")
	quoteOn(&q, "0.001")
	var entry ledger.Entry
	if json.Unmarshal(db.Call(t, scope, "post_journal", q), &entry) != nil {
		t.Fatal("invalid journal fixture")
	}
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := owner.JournalPostings(db.Context, book.ID, []string{entry.ID})
	if err != nil || len(projection.Entries) != 1 || len(projection.Entries[0].Lines) != 2 {
		t.Fatal("zero Book entry disappeared", err)
	}
	for _, line := range projection.Entries[0].Lines {
		if line.AmountMinor != "0" {
			t.Fatal("zero was changed to a positive amount")
		}
	}
}

func TestScopedPostingsReadOneBoundedLineSet(t *testing.T) {
	db, book, scope, debit, credit := setup(t)
	ids := []string{}
	for i := range 100 {
		var entry ledger.Entry
		q := post(debit.ID, credit.ID, "selected-"+ledger.ID())
		q.Description = "Example selected entry"
		if json.Unmarshal(db.Call(t, scope, "post_journal", q), &entry) != nil {
			t.Fatal("invalid fixture", i)
		}
		ids = append(ids, entry.ID)
	}
	pool, counts := traceQueries(t, db.Admin)
	owner, err := (ledger.Admin{DB: pool}).ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := owner.JournalPostings(db.Context, book.ID, ids)
	if err != nil || len(projection.Entries) != 100 || counts.reads.Load() != 1 || counts.locks.Load() != 0 {
		t.Fatal("reader duplicated queries, acquired a write lock or truncated its selection", err, counts.reads.Load(), counts.locks.Load())
	}
}

func TestScopedPostingsEnforceLineBoundaryWithoutTruncation(t *testing.T) {
	db, book, scope, debit, credit := setup(t)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for range 40 {
		q := post(debit.ID, credit.ID, "many-lines-"+ledger.ID())
		pair := append([]ledger.PostLine{}, q.Lines...)
		q.Lines = nil
		for range 125 {
			q.Lines = append(q.Lines, pair...)
		}
		var entry ledger.Entry
		if json.Unmarshal(db.Call(t, scope, "post_journal", q), &entry) != nil {
			t.Fatal("invalid large entry fixture")
		}
		ids = append(ids, entry.ID)
	}
	result, err := owner.JournalPostings(db.Context, book.ID, ids)
	if err != nil || len(result.Entries) != 40 {
		t.Fatal("exact boundary rejected", err)
	}
	count := 0
	for _, entry := range result.Entries {
		count += len(entry.Lines)
	}
	if count != 10000 {
		t.Fatal("boundary read truncated posted lines", count)
	}
	var extra ledger.Entry
	if json.Unmarshal(db.Call(t, scope, "post_journal", post(debit.ID, credit.ID, "extra")), &extra) != nil {
		t.Fatal("invalid extra entry fixture")
	}
	ids = append(ids, extra.ID)
	if result, err = owner.JournalPostings(db.Context, book.ID, ids); !errors.Is(err, ledger.ErrLimit) || len(result.Entries) != 0 {
		t.Fatal("over-limit selection returned partial postings", err)
	}
	ids = append(ids, ledger.ID())
	if result, err = owner.JournalPostings(db.Context, book.ID, ids); !errors.Is(err, ledger.ErrNotFound) || len(result.Entries) != 0 {
		t.Fatal("line limit obscured a missing selection", err)
	}
}
