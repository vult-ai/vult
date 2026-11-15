package ledger_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func documentChecks(t *testing.T, db *testutil.Database, s ledger.Scope, q ledger.DocumentCheckQuery) ledger.DocumentCheckPage {
	t.Helper()
	var page ledger.DocumentCheckPage
	if json.Unmarshal(db.Call(t, s, "list_document_checks", q), &page) != nil {
		t.Fatal("invalid document checks")
	}
	return page
}

func checksOf(t *testing.T, db *testutil.Database, s ledger.Scope, document string) map[string]ledger.DocumentCheck {
	t.Helper()
	out := map[string]ledger.DocumentCheck{}
	for _, c := range documentChecks(t, db, s, ledger.DocumentCheckQuery{DocumentID: document}).Checks {
		if c.DocumentID != document {
			t.Fatal("checks for another document", c)
		}
		out[c.Check] = c
	}
	return out
}

func statementFile(t *testing.T, f *bankFixture, key, start, end, opening, closing string, rows ...ledger.StatementRowInput) string {
	t.Helper()
	doc := statementDocument(t, f.db, f.s, key)
	_, err := f.owner.ImportStatementFile(f.db.Context, f.book.ID, f.actor, ledger.StatementFile{AccountID: f.checking.ID, DocumentID: doc,
		PeriodStart: start, PeriodEnd: end, Rows: rows, Balances: true, OpeningMinor: opening, ClosingMinor: closing})
	succeeded(t, err)
	return doc
}

// A statement file Vult reads in plain code is checked as it's recorded.
func TestStatementFilesAreCheckedByVult(t *testing.T) {
	f := newBankFixture(t)
	db, s := f.db, f.s
	september := statementFile(t, f, "september", "2026-09-01", "2026-09-30", "100000", "95000", row(1, "2026-09-05", "-5000", "Example supplies"))
	got := checksOf(t, db, s, september)
	if c := got[ledger.CheckStatementAccount]; c.Result != ledger.ResultReview || c.Reason == "" || c.Fix == "" {
		t.Fatal("a CSV has no account number, so a person confirms the account", c)
	}
	for _, name := range []string{ledger.CheckStatementPeriod, ledger.CheckStatementBalances, ledger.CheckDuplicate} {
		if c := got[name]; c.Result != ledger.ResultOK || !c.OK || c.Reason != "" {
			t.Fatal("a first statement that adds up passes", name, c)
		}
	}
	if c := got[ledger.CheckDone]; c.Result != ledger.ResultReview || c.Reason != "1 check needs a look." {
		t.Fatal("done sums up", c)
	}
	// October opens at the wrong balance; December leaves November out.
	october := statementFile(t, f, "october", "2026-10-01", "2026-10-31", "94000", "94000")
	if c := checksOf(t, db, s, october)[ledger.CheckStatementBalances]; c.Result != ledger.ResultFailed || c.Reason != "It opens at $940.00, but the statement before closed at $950.00." || c.Fix == "" {
		t.Fatal("an opening that isn't the previous closing fails", c)
	}
	december := statementFile(t, f, "december", "2026-12-01", "2026-12-31", "94000", "94000")
	if c := checksOf(t, db, s, december)[ledger.CheckStatementPeriod]; c.Result != ledger.ResultFailed || c.Reason != "Nothing covers 1 Nov 2026 – 30 Nov 2026 between this statement and the one before." {
		t.Fatal("a gap fails", c)
	}
	again := statementFile(t, f, "september-again", "2026-09-01", "2026-09-30", "100000", "100000")
	if c := checksOf(t, db, s, again)[ledger.CheckDuplicate]; c.Result != ledger.ResultFailed || !strings.Contains(c.Reason, "already has this account's 1 Sep 2026 – 30 Sep 2026 statement") {
		t.Fatal("the same account and period from another file is a duplicate", c)
	}
	// The owner reads them too, and only what's new.
	before := documentChecks(t, db, s, ledger.DocumentCheckQuery{}).Next
	page, err := f.owner.ReadDocumentChecks(db.Context, f.book.ID, ledger.DocumentCheckQuery{After: before - 1})
	succeeded(t, err)
	if len(page.Checks) != 5 || page.Next != before || page.Checks[len(page.Checks)-1].DocumentID != again {
		t.Fatal("the owner reads the latest checks of documents with news", page)
	}
	for i := 1; i < len(page.Checks); i++ {
		if page.Checks[i].Seq <= page.Checks[i-1].Seq {
			t.Fatal("checks come in order", page.Checks)
		}
	}
}

// Outside agents read checks but never write one, pass one or change one,
// whatever they record.
func TestAgentsNeverWriteChecks(t *testing.T) {
	f := newBankFixture(t)
	db, s := f.db, f.s
	// An agent's import records no checks.
	imported := statementDocument(t, db, s, "agent-import")
	importStatement(t, db, s, ledger.ImportStatement{RequestKey: "agent-import", AccountID: f.checking.ID, DocumentID: imported,
		PeriodStart: "2026-09-01", PeriodEnd: "2026-09-30", OpeningMinor: "0", ClosingMinor: "-5000",
		Rows: []ledger.StatementRowInput{row(1, "2026-09-05", "-5000", "Example supplies")}})
	if got := checksOf(t, db, s, imported); len(got) != 0 {
		t.Fatal("an agent's import records no checks", got)
	}
	// Vult reads a receipt it can't match yet; an agent attaching it to an
	// entry doesn't pass the check.
	receipt := statementDocument(t, db, s, "receipt")
	_, err := f.owner.RecordReceiptAsVult(db.Context, f.book.ID, f.actor, ledger.RecordReceiptDetails{DocumentID: receipt, AmountMinor: "4400", Date: "2026-11-02", Vendor: "Kawiarnia Łąka"})
	succeeded(t, err)
	entry := journal(t, db, s, "lunch", "2026-12-20", f.office.ID, f.checking.ID, "4400")
	db.Call(t, s, "attach_evidence", ledger.AttachEvidence{RequestKey: "attach", EntryID: entry.ID, DocumentID: receipt, Role: "receipt", Reason: "Example receipt"})
	if c := checksOf(t, db, s, receipt)[ledger.CheckReceiptMatched]; c.Result != ledger.ResultFailed {
		t.Fatal("an agent's attachment doesn't mark a receipt matched", c)
	}
	// No agent method writes a check.
	for _, method := range []string{"record_receipt_details", "report_unusable_document", "write_document_check", "record_document_check"} {
		if _, err := db.Store.Call(db.Context, s, method, json.RawMessage(`{"document_id":"`+receipt+`","reason":"Example","result":"ok"}`)); err == nil {
			t.Fatal("an agent wrote a check through", method)
		}
	}
	// Agents read them.
	if page := documentChecks(t, db, s, ledger.DocumentCheckQuery{DocumentID: receipt}); len(page.Checks) == 0 {
		t.Fatal("agents read checks")
	}
	// Recorded checks never change.
	for _, change := range []string{"UPDATE public.document_checks SET result='ok'", "DELETE FROM public.document_checks"} {
		if _, err := db.Admin.Exec(db.Context, change+" WHERE document_id=$1", receipt); err == nil {
			t.Fatal("a recorded check changed", change)
		}
	}
}

// Vult's own reading of a receipt finds its details, duplicates and its
// transaction.
func TestReceiptChecksFindDetailsDuplicatesAndTheirTransaction(t *testing.T) {
	f := newBankFixture(t)
	db, s := f.db, f.s
	journal(t, db, s, "supplies", "2026-10-14", f.office.ID, f.checking.ID, "1250")
	record := func(q ledger.RecordReceiptDetails) map[string]ledger.DocumentCheck {
		t.Helper()
		_, err := f.owner.RecordReceiptAsVult(db.Context, f.book.ID, f.actor, q)
		succeeded(t, err)
		return checksOf(t, db, s, q.DocumentID)
	}
	first := statementDocument(t, db, s, "receipt-1")
	got := record(ledger.RecordReceiptDetails{DocumentID: first, AmountMinor: "1250", Date: "2026-10-12", Vendor: "Blue Bottle"})
	if c := got[ledger.CheckReceiptDetails]; c.Result != ledger.ResultOK || c.Found != "$12.50 · 12 Oct 2026 · Blue Bottle" {
		t.Fatal("the details read are shown", c)
	}
	if c := got[ledger.CheckReceiptMatched]; c.Result != ledger.ResultOK || c.Against != "14 Oct 2026 · Example supplies" {
		t.Fatal("a transaction of the same amount within five days matches", c)
	}
	if c := got[ledger.CheckDone]; c.Result != ledger.ResultOK {
		t.Fatal("a matched receipt is done", c)
	}
	second := statementDocument(t, db, s, "receipt-2")
	if c := record(ledger.RecordReceiptDetails{DocumentID: second, AmountMinor: "1250", Date: "2026-10-12", Vendor: "Blue Bottle"})[ledger.CheckDuplicate]; c.Result != ledger.ResultFailed || !strings.Contains(c.Reason, "same amount, date and vendor") {
		t.Fatal("the same receipt twice is a duplicate", c)
	}
	unread := statementDocument(t, db, s, "receipt-3")
	got = record(ledger.RecordReceiptDetails{DocumentID: unread, AmountMinor: "4400", Date: "2026-11-02"})
	if c := got[ledger.CheckReceiptDetails]; c.Result != ledger.ResultFailed || c.Reason != "Its vendor couldn't be read." {
		t.Fatal("missing details fail", c)
	}
	if c := got[ledger.CheckDuplicate]; c.Result != ledger.ResultReview {
		t.Fatal("without its details, a person checks for a duplicate", c)
	}
	if c := got[ledger.CheckReceiptMatched]; c.Result != ledger.ResultFailed || !strings.HasPrefix(c.Reason, "No transaction of $44.00") {
		t.Fatal("no transaction yet fails", c)
	}
}

// Vult's checker runs the number checks on what it read, before anyone
// imports the statement, and records each check as Vult.
func TestCheckerNumbersOnAStatementAsRead(t *testing.T) {
	f := newBankFixture(t)
	db, ctx := f.db, f.db.Context
	september := statementDocument(t, db, f.s, "september")
	importStatement(t, db, f.s, ledger.ImportStatement{RequestKey: "september", AccountID: f.checking.ID, DocumentID: september,
		PeriodStart: "2026-09-01", PeriodEnd: "2026-09-30", OpeningMinor: "100000", ClosingMinor: "95000",
		Rows: []ledger.StatementRowInput{row(1, "2026-09-05", "-5000", "Example supplies")}})
	// The bank feed already brought one October line in.
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "feed-1", "2026-10-03", -2500, "Example rent")}, nil)

	facts, err := f.owner.DocumentFacts(ctx, f.book.ID)
	succeeded(t, err)
	var checking *ledger.StatementAccountFact
	for i := range facts.Accounts {
		if facts.Accounts[i].AccountID == f.checking.ID {
			checking = &facts.Accounts[i]
		}
	}
	if checking == nil || checking.Mask != "0001" || checking.Institution != "Example Bank" || checking.Kind != "cash" || facts.Currency != f.book.Currency {
		t.Fatal("facts name each statement account with its bank", facts)
	}

	// October, off by one cent, with one line the feed already has.
	october := statementDocument(t, db, f.s, "october")
	checks, err := f.owner.CheckStatementNumbers(ctx, f.book.ID, f.actor, ledger.StatementReading{DocumentID: october, AccountID: f.checking.ID,
		PeriodStart: "2026-10-01", PeriodEnd: "2026-10-31", OpeningMinor: 95000, ClosingMinor: 92501,
		Lines: []ledger.StatementLine{{Date: "2026-10-03", AmountMinor: -2500}}})
	succeeded(t, err)
	byCheck := map[string]ledger.DocumentCheck{}
	for _, c := range checks {
		byCheck[c.Check] = c
	}
	if c := byCheck[ledger.CheckStatementBalances]; c.Result != ledger.ResultFailed || c.Reason != "The lines add up to $925.00, but the statement closes at $925.01." || c.Fix == "" {
		t.Fatal("a balance off by one cent fails, to the cent", c)
	}
	if c := byCheck[ledger.CheckStatementPeriod]; c.Result != ledger.ResultOK || c.Against != "after 1 Sep 2026 – 30 Sep 2026" {
		t.Fatal("October follows September", c)
	}
	if c := byCheck[ledger.CheckDuplicate]; c.Result != ledger.ResultNote || !strings.HasPrefix(c.Reason, "1 of its 1 lines are already in the Book from the bank feed") {
		t.Fatal("lines the feed already has are a note", c)
	}
	if got := checksOf(t, db, f.s, october); got[ledger.CheckStatementBalances].Result != ledger.ResultFailed {
		t.Fatal("the checks are recorded", got)
	}

	// An overlapping period.
	overlap := statementDocument(t, db, f.s, "overlap")
	checks, err = f.owner.CheckStatementNumbers(ctx, f.book.ID, f.actor, ledger.StatementReading{DocumentID: overlap, AccountID: f.checking.ID,
		PeriodStart: "2026-09-15", PeriodEnd: "2026-10-14", OpeningMinor: 0, ClosingMinor: 0})
	succeeded(t, err)
	for _, c := range checks {
		if c.Check == ledger.CheckStatementPeriod && (c.Result != ledger.ResultFailed || !strings.HasPrefix(c.Reason, "It overlaps the 1 Sep 2026 – 30 Sep 2026 statement")) {
			t.Fatal("an overlapping period fails", c)
		}
	}

	// A check shows running, then its result; accepted addresses become facts.
	succeeded(t, f.owner.WriteDocumentCheck(ctx, f.book.ID, f.actor, ledger.DocumentCheck{DocumentID: october, Check: ledger.CheckStatementAddress, Result: ledger.ResultRunning}))
	if c := checksOf(t, db, f.s, october)[ledger.CheckStatementAddress]; c.Result != ledger.ResultRunning || c.OK {
		t.Fatal("a running check reads as running", c)
	}
	succeeded(t, f.owner.WriteDocumentCheck(ctx, f.book.ID, f.actor, ledger.DocumentCheck{DocumentID: october, Check: ledger.CheckStatementAddress,
		Result: ledger.ResultNote, Found: "RYNEK 50/7, 31-001 KRAKÓW", Reason: "The statement shows a flat number."}))
	if c := checksOf(t, db, f.s, october)[ledger.CheckStatementAddress]; c.Result != ledger.ResultNote || !c.OK || c.Found != "RYNEK 50/7, 31-001 KRAKÓW" {
		t.Fatal("the result replaces running, Unicode unchanged", c)
	}
	facts, err = f.owner.DocumentFacts(ctx, f.book.ID)
	succeeded(t, err)
	if len(facts.Addresses) != 1 || facts.Addresses[0] != "RYNEK 50/7, 31-001 KRAKÓW" {
		t.Fatal("an accepted address is compared with next time", facts.Addresses)
	}
	if err := f.owner.WriteDocumentCheck(ctx, f.book.ID, f.actor, ledger.DocumentCheck{DocumentID: october, Check: ledger.CheckStatementAddress, Result: ledger.ResultFailed}); err != nil {
		t.Fatal("a failure without a reason gets a plain one", err)
	}
	if err := f.owner.WriteDocumentCheck(ctx, f.book.ID, f.actor, ledger.DocumentCheck{DocumentID: october, Check: "made_up", Result: ledger.ResultOK}); err == nil {
		t.Fatal("an unknown check is refused")
	}
}

// Vult keeps how it read each document; the latest reading is current, and
// the owner's page says how each was read and what's missing.
func TestDocumentCheckReadingsAreBatchedAndKeepTheirWatermark(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	a := statementDocument(t, f.db, f.s, "first-reading")
	b := statementDocument(t, f.db, f.s, "second-reading")
	c := statementDocument(t, f.db, f.s, "without-reading")
	for _, id := range []string{a, b} {
		succeeded(t, f.owner.SaveDocumentReading(ctx, f.book.ID, f.actor, ledger.DocumentReading{
			ReadingSummary: ledger.ReadingSummary{DocumentID: id, ReadBy: ledger.ReadByPDF, Missing: []string{"period_start"}}, Fields: json.RawMessage(`{}`)}))
	}
	succeeded(t, f.owner.SaveDocumentReading(ctx, f.book.ID, f.actor, ledger.DocumentReading{
		ReadingSummary: ledger.ReadingSummary{DocumentID: b, ReadBy: ledger.ReadByOwner}, Fields: json.RawMessage(`{}`)}))
	for _, id := range []string{b, a, c} {
		succeeded(t, f.owner.WriteDocumentCheck(ctx, f.book.ID, f.actor, ledger.DocumentCheck{DocumentID: id, Check: ledger.CheckDone, Result: ledger.ResultOK}))
	}
	pool, counts := traceQueries(t, f.db.Admin)
	owner, err := (ledger.Admin{DB: pool}).ForTenant(ctx, f.book.TenantID)
	succeeded(t, err)
	page, err := owner.ReadDocumentChecks(ctx, f.book.ID, ledger.DocumentCheckQuery{})
	succeeded(t, err)
	if counts.readingReads.Load() != 1 || len(page.Readings) != 2 || page.Readings[0].DocumentID != b || page.Readings[0].ReadBy != ledger.ReadByOwner || len(page.Readings[0].Missing) != 0 || page.Readings[1].DocumentID != a || len(page.Readings[1].Missing) != 1 {
		t.Fatal("latest readings share one query and follow check order", counts.readingReads.Load(), page.Readings)
	}
	counts.readingReads.Store(0)
	fresh, err := owner.ReadDocumentChecks(ctx, f.book.ID, ledger.DocumentCheckQuery{After: page.Checks[0].Seq})
	succeeded(t, err)
	if counts.readingReads.Load() != 1 || len(fresh.Readings) != 1 || fresh.Readings[0].DocumentID != a || fresh.Next != page.Next {
		t.Fatal("only changed documents contribute readings", fresh)
	}
	one, err := owner.ReadDocumentChecks(ctx, f.book.ID, ledger.DocumentCheckQuery{DocumentID: a, After: page.Checks[0].Seq})
	succeeded(t, err)
	if len(one.Checks) != 1 || one.Checks[0].DocumentID != a || len(one.Readings) != 1 || one.Readings[0].DocumentID != a {
		t.Fatal("a document filter keeps its own changed checks", one)
	}
	unchanged, err := owner.ReadDocumentChecks(ctx, f.book.ID, ledger.DocumentCheckQuery{DocumentID: b, After: page.Checks[0].Seq})
	succeeded(t, err)
	if len(unchanged.Checks) != 0 || len(unchanged.Readings) != 0 || unchanged.Next != page.Checks[0].Seq {
		t.Fatal("other documents cannot advance one document's watermark", unchanged)
	}
	counts.readingReads.Store(0)
	empty, err := owner.ReadDocumentChecks(ctx, f.book.ID, ledger.DocumentCheckQuery{After: page.Next})
	succeeded(t, err)
	if len(empty.Checks) != 0 || len(empty.Readings) != 0 || empty.Next != page.Next || counts.readingReads.Load() != 0 {
		t.Fatal("an unchanged page needs no reading query", empty)
	}
}

func TestReadingsAreKept(t *testing.T) {
	f := newBankFixture(t)
	db, ctx := f.db, f.db.Context
	doc := statementDocument(t, db, f.s, "reading")
	fields := json.RawMessage(`{"kind":"statement","holder":{"value":"Łucja Wójcik","page":1}}`)
	succeeded(t, f.owner.SaveDocumentReading(ctx, f.book.ID, f.actor, ledger.DocumentReading{ReadingSummary: ledger.ReadingSummary{DocumentID: doc,
		ReadBy: ledger.ReadByPDF, Missing: []string{"period_start", "period_end"}}, Fields: fields}))
	succeeded(t, f.owner.WriteDocumentCheck(ctx, f.book.ID, f.actor, ledger.DocumentCheck{DocumentID: doc, Check: ledger.CheckDone, Result: ledger.ResultReview,
		Reason: "Vult couldn't read the period.", Action: ledger.ActionConfirmReading}))
	page, err := f.owner.ReadDocumentChecks(ctx, f.book.ID, ledger.DocumentCheckQuery{DocumentID: doc})
	succeeded(t, err)
	if len(page.Readings) != 1 || page.Readings[0].ReadBy != ledger.ReadByPDF || strings.Join(page.Readings[0].Missing, ",") != "period_start,period_end" {
		t.Fatal("the page says how it was read and what's missing", page.Readings)
	}
	// An agent's verified proposal, kept in the agent's request, is named
	// for the agent.
	agent := ledger.AgentRequest{Store: db.Store, Scope: f.s}
	succeeded(t, agent.SaveReading(ctx, ledger.DocumentReading{ReadingSummary: ledger.ReadingSummary{DocumentID: doc, ReadBy: ledger.ReadByAgent}, Fields: fields}))
	latest, ok, err := f.owner.LatestDocumentReading(ctx, f.book.ID, doc)
	succeeded(t, err)
	if !ok || latest.ReadBy != ledger.ReadByAgent || latest.Reader == "" || len(latest.Missing) != 0 || !strings.Contains(string(latest.Fields), "Łucja Wójcik") {
		t.Fatal("the latest reading is current, named for its agent", latest.ReadingSummary)
	}
	reader := f.s
	reader.Access = "read"
	if err := (ledger.AgentRequest{Store: db.Store, Scope: reader}).SaveReading(ctx, latest); err == nil {
		t.Fatal("a read-only agent's request keeps no reading")
	}
	if err := f.owner.SaveDocumentReading(ctx, f.book.ID, f.actor, ledger.DocumentReading{ReadingSummary: ledger.ReadingSummary{DocumentID: doc, ReadBy: "guess"}, Fields: fields}); err == nil {
		t.Fatal("an unknown reader is refused")
	}
	if _, err := db.Admin.Exec(ctx, "UPDATE public.document_readings SET read_by='owner' WHERE document_id=$1", doc); err == nil {
		t.Fatal("a kept reading never changes")
	}
}
