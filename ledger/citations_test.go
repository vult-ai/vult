package ledger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func locationNumber(n int) *int { return &n }

func readyOriginal(t *testing.T, db *testutil.Database, s ledger.Scope, key string) ledger.Document {
	t.Helper()
	data := []byte("synthetic original " + key)
	intake := prepareDocument(t, db, s, key, data)
	result, err := db.Store.Upload(db.Context, s, intake.UploadID, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var doc ledger.Document
	if json.Unmarshal(result, &doc) != nil {
		t.Fatal("invalid document result")
	}
	return doc
}

func citedRequest(t *testing.T, db *testutil.Database, s ledger.Scope, key string, sources []ledger.SourceInput) ledger.PostJournal {
	t.Helper()
	a := db.Account(t, s, "1000", "asset")
	b := db.Account(t, s, "3000", "equity")
	return ledger.PostJournal{RequestKey: key, EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example opening",
		Lines: []ledger.PostLine{{AccountID: a.ID, Side: "debit", AmountMinor: "1234"}, {AccountID: b.ID, Side: "credit", AmountMinor: "1234"}}, Sources: sources}
}

func postCitedJournal(t *testing.T, db *testutil.Database, s ledger.Scope, key string, sources []ledger.SourceInput) ledger.Entry {
	t.Helper()
	var entry ledger.Entry
	if json.Unmarshal(db.Call(t, s, "post_journal", citedRequest(t, db, s, key, sources)), &entry) != nil {
		t.Fatal("invalid entry result")
	}
	return entry
}

func entryCitations(t *testing.T, db *testutil.Database, s ledger.Scope, id string, history bool) []ledger.SourceCitation {
	t.Helper()
	var page struct {
		Sources []ledger.SourceCitation `json:"sources"`
	}
	if json.Unmarshal(db.Call(t, s, "get_entry_documents", ledger.EntryDocumentsQuery{EntryID: id, History: history, Limit: 100}), &page) != nil {
		t.Fatal("invalid citation page")
	}
	return page.Sources
}

func TestLegacyPostingInputRemainsByteIdenticalWithoutSources(t *testing.T) {
	q := ledger.PostJournal{RequestKey: "opening", EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example opening", Lines: []ledger.PostLine{
		{AccountID: "11111111-1111-4111-8111-111111111111", Side: "debit", AmountMinor: "1234"},
		{AccountID: "22222222-2222-4222-8222-222222222222", Side: "credit", AmountMinor: "1234"},
	}}
	const released = `{"request_key":"opening","economic_date":"2026-01-02","accounting_date":"2026-01-02","description":"Example opening","lines":[{"account_id":"11111111-1111-4111-8111-111111111111","side":"debit","amount_minor":"1234"},{"account_id":"22222222-2222-4222-8222-222222222222","side":"credit","amount_minor":"1234"}]}`
	actual, err := json.Marshal(q)
	if err != nil || string(actual) != released {
		t.Fatal("released posting bytes changed", err)
	}
	q.Sources = []ledger.SourceInput{}
	actual, err = json.Marshal(q)
	if err != nil || string(actual) != released {
		t.Fatal("empty sources changed released posting bytes", err)
	}
}

func TestMultipleSourcesAndCorrectionLeaveAccountingUnchanged(t *testing.T) {
	db, s, _ := documentFixture(t)
	a := readyOriginal(t, db, s, "statement")
	b := readyOriginal(t, db, s, "invoice")
	sources := []ledger.SourceInput{{DocumentID: a.ID, Location: ledger.Location{Page: locationNumber(1)}}, {DocumentID: b.ID, Location: ledger.Location{Row: locationNumber(3)}}}
	q := citedRequest(t, db, s, "opening", sources)
	original := db.Call(t, s, "post_journal", q)
	var entry ledger.Entry
	if json.Unmarshal(original, &entry) != nil {
		t.Fatal("invalid entry")
	}
	before := db.Call(t, s, "get_journal", ledger.EntryQuery{EntryID: entry.ID})
	trial := db.Call(t, s, "get_trial_balance", ledger.TrialQuery{})
	citations := entryCitations(t, db, s, entry.ID, false)
	if len(citations) != 2 || citations[0].DocumentID != a.ID || citations[1].DocumentID != b.ID {
		t.Fatal("multiple sources were not preserved")
	}
	if err := db.ClosePeriod(db.Context, s.BookID, "2026-01-01", "2026-01-31"); err != nil {
		t.Fatal(err)
	}
	fix := ledger.CorrectSourceCitation{RequestKey: "citation-fix", SourceID: citations[0].SourceID, ExpectedRevision: 1,
		DocumentID: b.ID, Location: ledger.Location{Row: locationNumber(7)}, Reason: "Correct the supporting location"}
	corrected := db.Call(t, s, "correct_source_citation", fix)
	if replay := db.Call(t, s, "correct_source_citation", fix); !bytes.Equal(replay, corrected) {
		t.Fatal("citation retry changed its outcome")
	}
	history := entryCitations(t, db, s, entry.ID, true)
	if len(history) != 3 || history[0].DocumentID != a.ID || history[2].Revision != 2 || history[2].DocumentID != b.ID ||
		history[2].ActorID != s.ActorID || history[2].Reason != fix.Reason || history[2].SourceID != history[0].SourceID {
		t.Fatal("citation correction erased or misattributed history")
	}
	if after := db.Call(t, s, "get_journal", ledger.EntryQuery{EntryID: entry.ID}); !bytes.Equal(before, after) {
		t.Fatal("citation correction changed accounting")
	}
	afterTrial := db.Call(t, s, "get_trial_balance", ledger.TrialQuery{})
	var beforeAmounts, afterAmounts map[string]json.RawMessage
	if json.Unmarshal(trial, &beforeAmounts) != nil || json.Unmarshal(afterTrial, &afterAmounts) != nil || len(beforeAmounts["state_revision"]) == 0 || bytes.Equal(beforeAmounts["state_revision"], afterAmounts["state_revision"]) {
		t.Fatal("changed accepted records did not change the observed state")
	}
	for _, key := range []string{"state_revision", "state_captured_at", "state_knowledge_cutoff"} {
		delete(beforeAmounts, key)
		delete(afterAmounts, key)
	}
	if !bytes.Equal(value(beforeAmounts), value(afterAmounts)) {
		t.Fatal("citation correction changed financial content")
	}
	if retry := db.Call(t, s, "post_journal", q); !bytes.Equal(retry, original) {
		t.Fatal("posting retry changed after a citation correction")
	}
	fix.RequestKey = "stale-fix"
	input, _ := json.Marshal(fix)
	if _, err := db.Store.Call(db.Context, s, "correct_source_citation", input); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("stale citation revision accepted", err)
	}
	_, other := db.Book(t, "Other example Book")
	foreign := readyOriginal(t, db, other, "foreign")
	fix.RequestKey, fix.ExpectedRevision, fix.DocumentID = "foreign-fix", 2, foreign.ID
	input, _ = json.Marshal(fix)
	if _, err := db.Store.Call(db.Context, s, "correct_source_citation", input); err == nil {
		t.Fatal("cross-Book citation accepted")
	}
	var used int
	if err := db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.documents WHERE used_at IS NOT NULL").Scan(&used); err != nil || used != 2 {
		t.Fatal("historical source use was not permanent", used, err)
	}
}

func TestUnverifiedSourcesAndInvalidLocationsCannotPost(t *testing.T) {
	db, s, _ := documentFixture(t)
	pending := prepareDocument(t, db, s, "pending", []byte("synthetic pending original"))
	ready := readyOriginal(t, db, s, "ready")
	for i, sources := range [][]ledger.SourceInput{
		{{DocumentID: pending.Document.ID}}, {{DocumentID: ledger.ID()}},
		{{DocumentID: ready.ID, Location: ledger.Location{Page: locationNumber(0)}}},
		{{DocumentID: ready.ID}, {DocumentID: ready.ID}},
	} {
		q := citedRequest(t, db, s, fmt.Sprintf("invalid-%d", i), sources)
		input, _ := json.Marshal(q)
		if _, err := db.Store.Call(db.Context, s, "post_journal", input); err == nil {
			t.Fatal("unsupported source committed a journal")
		}
	}
	var entries, used int
	if err := db.Admin.QueryRow(db.Context, "SELECT (SELECT count(*) FROM public.entries),(SELECT count(*) FROM public.documents WHERE used_at IS NOT NULL)").Scan(&entries, &used); err != nil || entries != 0 || used != 0 {
		t.Fatal("bad source left posting or use effects", entries, used, err)
	}
}

func TestSourceReuseIsVisibleAndDoesNotBlockLegitimateEntries(t *testing.T) {
	db, s, _ := documentFixture(t)
	doc := readyOriginal(t, db, s, "statement")
	location := ledger.Location{Row: locationNumber(4)}
	sources := []ledger.SourceInput{{DocumentID: doc.ID, Location: location}}
	postCitedJournal(t, db, s, "first", sources)
	second := postCitedJournal(t, db, s, "second", sources)
	query := ledger.SourceUsageQuery{DocumentID: doc.ID, Location: location, Limit: 1}
	var usage struct {
		Count   int64 `json:"active_entry_count"`
		Reused  bool  `json:"reused"`
		HasMore bool  `json:"has_more"`
		Next    int64 `json:"next_after"`
	}
	if json.Unmarshal(db.Call(t, s, "get_source_usage", query), &usage) != nil || usage.Count != 2 || !usage.Reused || !usage.HasMore {
		t.Fatal("reused source was not flagged with bounded paging")
	}
	db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse-second", EntryID: second.ID, AccountingDate: "2026-02-01", Reason: "Correct duplicate entry"})
	if json.Unmarshal(db.Call(t, s, "get_source_usage", query), &usage) != nil || usage.Count != 1 || usage.Reused || usage.HasMore {
		t.Fatal("reversed entry remained active in source usage")
	}
}

func TestPostedSourceIsImmutableAndCannotBeAddedLater(t *testing.T) {
	db, s, _ := documentFixture(t)
	doc := readyOriginal(t, db, s, "statement")
	entry := postCitedJournal(t, db, s, "opening", []ledger.SourceInput{{DocumentID: doc.ID}})
	for _, operation := range []string{"update", "delete", "append"} {
		t.Run(operation, func(t *testing.T) {
			tx, err := db.Runtime.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`, s.TenantID, s.BookID, s.ActorID, s.ConnectionID); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "update":
				_, err = tx.Exec(db.Context, "UPDATE public.source_citations SET location='{}'::jsonb WHERE entry_id=$1", entry.ID)
			case "delete":
				_, err = tx.Exec(db.Context, "DELETE FROM public.source_citations WHERE entry_id=$1", entry.ID)
			case "append":
				_, err = tx.Exec(db.Context, `INSERT INTO public.source_citations(tenant_id,book_id,id,source_id,revision,sequence,entry_id,position,document_id,actor_id,receipt_id,location)
                    VALUES($1,$2,$3,$3,1,1,$4,2,$5,$6,$7,'{}'::jsonb)`, s.TenantID, s.BookID, ledger.ID(), entry.ID, doc.ID, s.ActorID, entry.ReceiptID)
			}
			if err == nil {
				t.Fatal("posted provenance was mutable")
			}
		})
	}
}

func TestNestedTriggerCannotInventDocumentUseWithoutHistory(t *testing.T) {
	db, s, _ := documentFixture(t)
	doc := readyOriginal(t, db, s, "unused")
	tx, err := db.Runtime.BeginTx(db.Context, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(db.Context, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`, s.TenantID, s.BookID, s.ActorID, s.ConnectionID); err != nil {
		t.Fatal(err)
	}
	query := fmt.Sprintf(`CREATE TEMP TABLE invented_use(id uuid);
        CREATE FUNCTION pg_temp.invent_use() RETURNS trigger LANGUAGE plpgsql AS $body$
        BEGIN PERFORM public.document_use('%s','%s',NEW.id);RETURN NEW;END $body$;
        CREATE TRIGGER invent_use BEFORE INSERT ON invented_use FOR EACH ROW EXECUTE FUNCTION pg_temp.invent_use()`, s.TenantID, s.BookID)
	if _, err = tx.Exec(db.Context, query); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(db.Context, "INSERT INTO invented_use VALUES($1)", doc.ID); err != nil {
		t.Fatal("nested negative control did not reach the history check", err)
	}
	err = tx.Commit(db.Context)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
		t.Fatal("document use was forged without a citation", err)
	}
}
