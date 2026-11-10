package ledger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func evidencePage(t *testing.T, db *testutil.Database, s ledger.Scope, q ledger.EntryDocumentsQuery) ([]ledger.EvidenceLink, int64, bool) {
	t.Helper()
	var page struct {
		Evidence []ledger.EvidenceLink `json:"evidence"`
		Next     int64                 `json:"next_evidence_after"`
		More     bool                  `json:"evidence_has_more"`
	}
	if json.Unmarshal(db.Call(t, s, "get_entry_documents", q), &page) != nil {
		t.Fatal("invalid evidence page")
	}
	return page.Evidence, page.Next, page.More
}

func missingEvidence(t *testing.T, db *testutil.Database, s ledger.Scope, q ledger.MissingEvidenceQuery) []string {
	t.Helper()
	var page struct {
		Entries []struct {
			ID string `json:"id"`
		} `json:"entries"`
	}
	if json.Unmarshal(db.Call(t, s, "list_entries_missing_evidence", q), &page) != nil {
		t.Fatal("invalid missing-evidence page")
	}
	ids := []string{}
	for _, e := range page.Entries {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestEvidenceIsSeparateFromSourcesAndKeepsDetachHistory(t *testing.T) {
	db, s, _ := documentFixture(t)
	statement := readyOriginal(t, db, s, "statement")
	receipt := readyOriginal(t, db, s, "receipt")
	entry := postCitedJournal(t, db, s, "purchase", []ledger.SourceInput{{DocumentID: statement.ID}})
	before := db.Call(t, s, "get_journal", ledger.EntryQuery{EntryID: entry.ID})
	if ids := missingEvidence(t, db, s, ledger.MissingEvidenceQuery{}); len(ids) != 1 || ids[0] != entry.ID {
		t.Fatal("source alone counted as evidence")
	}
	if err := db.ClosePeriod(db.Context, s.BookID, "2026-01-01", "2026-01-31"); err != nil {
		t.Fatal(err)
	}
	db.Call(t, s, "attach_evidence", ledger.AttachEvidence{RequestKey: "statement-link", EntryID: entry.ID, DocumentID: statement.ID, Role: "statement", Reason: "Bank-side record"})
	if ids := missingEvidence(t, db, s, ledger.MissingEvidenceQuery{Role: "receipt"}); len(ids) != 1 {
		t.Fatal("statement counted as a receipt")
	}
	if ids := missingEvidence(t, db, s, ledger.MissingEvidenceQuery{}); len(ids) != 0 {
		t.Fatal("explicit statement evidence was invisible")
	}
	request := ledger.AttachEvidence{RequestKey: "receipt-link", EntryID: entry.ID, DocumentID: receipt.ID, Role: "receipt", Reason: "Supporting purchase receipt"}
	attached := db.Call(t, s, "attach_evidence", request)
	var link ledger.EvidenceLink
	if json.Unmarshal(attached, &link) != nil || link.Action != "attach" || link.ActorID != s.ActorID {
		t.Fatal("evidence link was misattributed")
	}
	if ids := missingEvidence(t, db, s, ledger.MissingEvidenceQuery{Role: "receipt"}); len(ids) != 0 {
		t.Fatal("attached receipt remained missing")
	}
	detach := ledger.DetachEvidence{RequestKey: "detach", LinkID: link.LinkID, Reason: "Receipt belongs to another purchase"}
	detached := db.Call(t, s, "detach_evidence", detach)
	if replay := db.Call(t, s, "detach_evidence", detach); !bytes.Equal(replay, detached) {
		t.Fatal("detach retry changed its result")
	}
	if replay := db.Call(t, s, "attach_evidence", request); !bytes.Equal(replay, attached) {
		t.Fatal("old attach result changed after detach")
	}
	if ids := missingEvidence(t, db, s, ledger.MissingEvidenceQuery{Role: "receipt"}); len(ids) != 1 {
		t.Fatal("replaying an old attach reactivated a detached link")
	}
	current, _, _ := evidencePage(t, db, s, ledger.EntryDocumentsQuery{EntryID: entry.ID})
	history, _, _ := evidencePage(t, db, s, ledger.EntryDocumentsQuery{EntryID: entry.ID, History: true})
	if len(current) != 1 || current[0].Role != "statement" || len(history) != 3 || history[2].Action != "detach" || history[2].Reason != detach.Reason {
		t.Fatal("detach erased or corrupted history")
	}
	ageDocumentFixture(t, db, receipt.ID)
	if count, err := db.Owner.ExpireBookDocuments(db.Context, s.BookID, db.Store.Documents.Storage, 100); err != nil || count != 0 {
		t.Fatal("formerly attached original expired", count, err)
	}
	if after := db.Call(t, s, "get_journal", ledger.EntryQuery{EntryID: entry.ID}); !bytes.Equal(before, after) {
		t.Fatal("evidence changed financial accounting")
	}
}

func TestEvidenceBoundariesAndExactRequestReuse(t *testing.T) {
	db, s, _ := documentFixture(t)
	doc := readyOriginal(t, db, s, "receipt")
	entry := postCitedJournal(t, db, s, "purchase", nil)
	q := ledger.AttachEvidence{RequestKey: "attach", EntryID: entry.ID, DocumentID: doc.ID, Role: "receipt", Reason: "Supporting receipt"}
	first := db.Call(t, s, "attach_evidence", q)
	if replay := db.Call(t, s, "attach_evidence", q); !bytes.Equal(first, replay) {
		t.Fatal("attach retry changed its result")
	}
	q.RequestKey = "duplicate"
	input, _ := json.Marshal(q)
	if _, err := db.Store.Call(db.Context, s, "attach_evidence", input); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("duplicate active attachment accepted", err)
	}
	_, other := db.Book(t, "Other example Book")
	foreign := readyOriginal(t, db, other, "foreign")
	q.RequestKey, q.DocumentID = "foreign", foreign.ID
	input, _ = json.Marshal(q)
	if _, err := db.Store.Call(db.Context, s, "attach_evidence", input); err == nil {
		t.Fatal("foreign Book evidence accepted")
	}
	connection, err := db.Owner.CreateConnection(db.Context, s.BookID, "Example reader", "read")
	if err != nil {
		t.Fatal(err)
	}
	reader := ledger.Scope{TenantID: connection.TenantID, BookID: connection.BookID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "read"}
	if _, err := db.Store.Call(db.Context, reader, "attach_evidence", input); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("read grant attached evidence", err)
	}
	links, _, _ := evidencePage(t, db, reader, ledger.EntryDocumentsQuery{EntryID: entry.ID})
	if len(links) != 1 {
		t.Fatal("reader could not inspect authorized evidence")
	}
	if err := db.Owner.Revoke(db.Context, reader.ConnectionID); err != nil {
		t.Fatal(err)
	}
	input, _ = json.Marshal(ledger.EntryDocumentsQuery{EntryID: entry.ID})
	if _, err := db.Store.Call(db.Context, reader, "get_entry_documents", input); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("revoked reader saw evidence", err)
	}
}

func TestMissingEvidenceFiltersAndHistoryPagination(t *testing.T) {
	db, s, _ := documentFixture(t)
	first := postCitedJournal(t, db, s, "first", nil)
	second := postCitedJournal(t, db, s, "second", nil)
	if ids := missingEvidence(t, db, s, ledger.MissingEvidenceQuery{From: "2026-01-01", Through: "2026-01-31", AccountID: first.Lines[0].AccountID, Limit: 1}); len(ids) != 1 || ids[0] != first.ID {
		t.Fatal("account/date/page filters failed")
	}
	if ids := missingEvidence(t, db, s, ledger.MissingEvidenceQuery{After: first.Sequence}); len(ids) != 1 || ids[0] != second.ID {
		t.Fatal("missing-evidence continuation skipped an entry")
	}
	if ids := missingEvidence(t, db, s, ledger.MissingEvidenceQuery{From: "2026-02-01"}); len(ids) != 0 {
		t.Fatal("accounting date filter ignored")
	}
	if ids := missingEvidence(t, db, s, ledger.MissingEvidenceQuery{AccountID: ledger.ID()}); len(ids) != 0 {
		t.Fatal("foreign account filter leaked entries")
	}
	doc := readyOriginal(t, db, s, "receipt")
	attached := db.Call(t, s, "attach_evidence", ledger.AttachEvidence{RequestKey: "attach", EntryID: first.ID, DocumentID: doc.ID, Role: "receipt", Reason: "Supporting purchase receipt"})
	var link ledger.EvidenceLink
	if json.Unmarshal(attached, &link) != nil {
		t.Fatal("invalid link")
	}
	db.Call(t, s, "detach_evidence", ledger.DetachEvidence{RequestKey: "detach", LinkID: link.LinkID, Reason: "Correct evidence association"})
	page, next, more := evidencePage(t, db, s, ledger.EntryDocumentsQuery{EntryID: first.ID, History: true, Limit: 1})
	if len(page) != 1 || !more {
		t.Fatal("history was not bounded")
	}
	page, _, more = evidencePage(t, db, s, ledger.EntryDocumentsQuery{EntryID: first.ID, History: true, EvidenceAfter: next, Limit: 1})
	if len(page) != 1 || more || page[0].Action != "detach" {
		t.Fatal("history continuation lost detach")
	}
	db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: second.ID, AccountingDate: "2026-02-01", Reason: "Correct duplicate entry"})
	if ids := missingEvidence(t, db, s, ledger.MissingEvidenceQuery{}); len(ids) != 1 || ids[0] != first.ID {
		t.Fatal("reversal bookkeeping appeared in missing-evidence results")
	}
}

func TestEvidenceHistoryCannotBeEditedOrDeleted(t *testing.T) {
	db, s, _ := documentFixture(t)
	doc := readyOriginal(t, db, s, "receipt")
	entry := postCitedJournal(t, db, s, "purchase", nil)
	db.Call(t, s, "attach_evidence", ledger.AttachEvidence{RequestKey: "attach", EntryID: entry.ID, DocumentID: doc.ID, Role: "receipt", Reason: "Supporting receipt"})
	for _, query := range []string{"UPDATE public.evidence_links SET reason='different' WHERE entry_id=$1", "DELETE FROM public.evidence_links WHERE entry_id=$1"} {
		tx, err := db.Runtime.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`, s.TenantID, s.BookID, s.ActorID, s.ConnectionID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(db.Context, query, entry.ID); err == nil {
			t.Fatal("evidence history was mutable")
		}
		tx.Rollback(context.Background())
	}
}
