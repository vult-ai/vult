package ledger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestOwnerJournalReadKeepsExactMeasurementsAndReversals(t *testing.T) {
	db, book, scope, debit, credit := setup(t)
	q := post(debit.ID, credit.ID, "large")
	for i := range q.Lines {
		q.Lines[i].AmountMinor = "9007199254740993"
	}
	var large, rounded, reversal ledger.Entry
	if json.Unmarshal(db.Call(t, scope, "post_journal", q), &large) != nil {
		t.Fatal("invalid large entry fixture")
	}
	cash := measuredAccount(t, db, scope, "1200", "JPY", 0, true)
	zero := measuredPost(cash, credit.ID, "rounded", "1", "0")
	quoteOn(&zero, "0.001")
	if json.Unmarshal(db.Call(t, scope, "post_journal", zero), &rounded) != nil || json.Unmarshal(db.Call(t, scope, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: rounded.ID, AccountingDate: "2026-02-01", Reason: "Example correction"}), &reversal) != nil {
		t.Fatal("invalid measured entry fixtures")
	}
	pool, counts := traceQueries(t, db.Admin)
	owner, err := (ledger.Admin{DB: pool}).ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []ledger.Entry{large, rounded, reversal} {
		counts.reads.Store(0)
		got, err := owner.Journal(db.Context, book.ID, entry.ID)
		if err != nil || got.Version != 1 || got.BookID != book.ID || got.Currency != "USD" || got.Scale != 2 || !bytes.Equal(value(got.Entry), value(entry)) {
			t.Fatal("complete journal read changed accepted measurements or references", err)
		}
		if counts.reads.Load() != 2 || counts.locks.Load() != 0 {
			t.Fatal("single entry read scanned or locked unrelated journal history", counts.reads.Load(), counts.locks.Load())
		}
	}
	_, other, _ := tenantOwner(t, db)
	for _, candidate := range []struct{ book, entry string }{{book.ID, ledger.ID()}, {other.ID, large.ID}} {
		if got, err := owner.Journal(db.Context, candidate.book, candidate.entry); !errors.Is(err, ledger.ErrNotFound) || got.Entry.ID != "" {
			t.Fatal("missing or foreign entry returned data", err)
		}
	}
	if _, err := owner.Journal(db.Context, book.ID, "invalid"); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("invalid entry ID accepted", err)
	}
}

func TestOwnerJournalDocumentsKeepRolesHistoryAndIndependentCursors(t *testing.T) {
	db, scope, _ := documentFixture(t)
	a, b := readyOriginal(t, db, scope, "statement"), readyOriginal(t, db, scope, "receipt")
	entry := postCitedJournal(t, db, scope, "cited", []ledger.SourceInput{{DocumentID: a.ID, Location: ledger.Location{Page: locationNumber(1)}}, {DocumentID: b.ID, Location: ledger.Location{Page: locationNumber(2)}}})
	var first, second ledger.EvidenceLink
	if json.Unmarshal(db.Call(t, scope, "attach_evidence", ledger.AttachEvidence{RequestKey: "statement-link", EntryID: entry.ID, DocumentID: a.ID, Role: "statement", Reason: "Example statement"}), &first) != nil || json.Unmarshal(db.Call(t, scope, "attach_evidence", ledger.AttachEvidence{RequestKey: "receipt-link", EntryID: entry.ID, DocumentID: b.ID, Role: "receipt", Reason: "Example receipt"}), &second) != nil {
		t.Fatal("invalid evidence fixtures")
	}
	owner, err := db.Owner.ForTenant(db.Context, scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	type page struct {
		Sources       []ledger.SourceCitation `json:"sources"`
		Evidence      []ledger.EvidenceLink   `json:"evidence"`
		SourceAfter   int64                   `json:"next_source_after"`
		EvidenceAfter int64                   `json:"next_evidence_after"`
		SourceMore    bool                    `json:"source_has_more"`
		EvidenceMore  bool                    `json:"evidence_has_more"`
	}
	read := func(q ledger.EntryDocumentsQuery) page {
		t.Helper()
		got, err := owner.JournalDocuments(db.Context, scope.BookID, q)
		if err != nil || got.Version != 1 || got.BookID != scope.BookID || got.EntryID != entry.ID || !bytes.Equal(got.Documents, db.Call(t, scope, "get_entry_documents", q)) {
			t.Fatal("owner and agent document facts differ", err)
		}
		var result page
		if json.Unmarshal(got.Documents, &result) != nil {
			t.Fatal("invalid typed document read")
		}
		return result
	}
	firstPage := read(ledger.EntryDocumentsQuery{EntryID: entry.ID, Limit: 1})
	if len(firstPage.Sources) != 1 || len(firstPage.Evidence) != 1 || !firstPage.SourceMore || !firstPage.EvidenceMore || firstPage.Sources[0].DocumentID != a.ID || firstPage.Evidence[0].Role != "statement" {
		t.Fatal("independent source/evidence first pages were flattened")
	}
	last := read(ledger.EntryDocumentsQuery{EntryID: entry.ID, SourceAfter: firstPage.SourceAfter, EvidenceAfter: firstPage.EvidenceAfter, Limit: 1})
	if len(last.Sources) != 1 || len(last.Evidence) != 1 || last.SourceMore || last.EvidenceMore || last.Sources[0].DocumentID != b.ID || last.Evidence[0].Role != "receipt" {
		t.Fatal("independent source/evidence cursors changed the last page")
	}
	db.Call(t, scope, "detach_evidence", ledger.DetachEvidence{RequestKey: "detach", LinkID: second.LinkID, Reason: "Example corrected association"})
	current := read(ledger.EntryDocumentsQuery{EntryID: entry.ID, Limit: 100})
	if len(current.Sources) != 2 || len(current.Evidence) != 1 || current.Evidence[0].LinkID != first.LinkID || current.Evidence[0].Role != "statement" {
		t.Fatal("source citation became receipt evidence or detachment disappeared")
	}
	history := read(ledger.EntryDocumentsQuery{EntryID: entry.ID, History: true, Limit: 100})
	if len(history.Evidence) != 3 || history.Evidence[2].Action != "detach" || history.Evidence[2].LinkID != second.LinkID {
		t.Fatal("append-only evidence history lost")
	}
	for _, q := range []ledger.EntryDocumentsQuery{{EntryID: "invalid"}, {EntryID: entry.ID, Limit: 101}, {EntryID: entry.ID, SourceAfter: -1}} {
		if got, err := owner.JournalDocuments(db.Context, scope.BookID, q); !errors.Is(err, ledger.ErrInvalid) || got.Documents != nil {
			t.Fatal("invalid document read returned data", err)
		}
	}
	if got, err := owner.JournalDocuments(db.Context, scope.BookID, ledger.EntryDocumentsQuery{EntryID: ledger.ID()}); !errors.Is(err, ledger.ErrNotFound) || got.Documents != nil {
		t.Fatal("missing entry returned relationships", err)
	}
}
