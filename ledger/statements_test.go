package ledger_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// statementDocument stores a ready synthetic original to cite.
func statementDocument(t *testing.T, db *testutil.Database, s ledger.Scope, key string) string {
	t.Helper()
	if db.Store.Documents == nil {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		disk, err := documents.OpenDisk(dir)
		succeeded(t, err)
		t.Cleanup(func() { disk.Close() })
		identity, err := age.GenerateX25519Identity()
		succeeded(t, err)
		db.Store.Documents = &documents.Vault{Storage: disk, Keys: documentKeys{identity}}
	}
	data := []byte("%PDF-1.4\n% synthetic statement " + key + "\n")
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	if json.Unmarshal(db.Call(t, s, "add_document", ledger.AddDocument{RequestKey: "doc-" + key, Name: key + ".pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}), &intake) != nil {
		t.Fatal("invalid document intake")
	}
	_, err := db.Store.Upload(db.Context, s, intake.UploadID, bytes.NewReader(data))
	succeeded(t, err)
	return intake.Document.ID
}

func row(n int, date, amount, description string) ledger.StatementRowInput {
	return ledger.StatementRowInput{RowNumber: n, Date: date, AmountMinor: amount, Description: description, Raw: fmt.Sprintf("%s,%s,%s", date, description, amount)}
}

func importStatement(t *testing.T, db *testutil.Database, s ledger.Scope, q ledger.ImportStatement) ledger.Statement {
	t.Helper()
	var st ledger.Statement
	if json.Unmarshal(db.Call(t, s, "import_statement", q), &st) != nil || st.ID == "" {
		t.Fatal("invalid statement result")
	}
	return st
}

func reconciliation(t *testing.T, db *testutil.Database, s ledger.Scope, id string) ledger.StatementReconciliation {
	t.Helper()
	var r ledger.StatementReconciliation
	if json.Unmarshal(db.Call(t, s, "get_statement_reconciliation", ledger.StatementQuery{StatementID: id}), &r) != nil {
		t.Fatal("invalid reconciliation")
	}
	return r
}

func suggestions(t *testing.T, db *testutil.Database, s ledger.Scope, id string) []ledger.StatementSuggestion {
	t.Helper()
	var out struct {
		Suggestions []ledger.StatementSuggestion `json:"suggestions"`
	}
	if json.Unmarshal(db.Call(t, s, "suggest_statement_matches", ledger.StatementQuery{StatementID: id}), &out) != nil {
		t.Fatal("invalid suggestions")
	}
	return out.Suggestions
}

func entryLine(t *testing.T, db *testutil.Database, s ledger.Scope, entry ledger.Entry, account string) string {
	t.Helper()
	for _, l := range entry.Lines {
		if l.AccountID == account {
			return l.ID
		}
	}
	t.Fatal("entry has no line on the account")
	return ""
}

func acceptMatches(t *testing.T, db *testutil.Database, s ledger.Scope, key string, list []ledger.StatementSuggestion) {
	t.Helper()
	for i, sug := range list {
		if sug.Kind != "match" {
			continue
		}
		db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: fmt.Sprintf("%s-%d", key, i), Rows: []ledger.StatementMatchPart{{RowID: sug.RowID}},
			Lines: []ledger.StatementMatchPart{{LineID: sug.LineIDs[0]}}, Reason: "Statement row and Book entry are the same payment"})
	}
}

// The USD checking fixture: opening 1,000.00; +200.00 income, −60.00
// purchase, −100.00 own-account transfer, −5.00 bank fee; closing 1,035.00.
// The Book lacks the fee and reads 1,040.00.
func TestCheckingStatementReconcilesAfterTheMissingFee(t *testing.T) {
	db := testutil.New(t)
	book, s, byCode := chartBook(t, db, "business-v1")
	checking, savings, sales, software, fees, opening := byCode["1110"].ID, byCode["1120"].ID, byCode["4100"].ID, byCode["6200"].ID, byCode["6600"].ID, byCode["3900"].ID
	journal(t, db, s, "opening", "2026-01-31", checking, opening, "100000")
	income := journal(t, db, s, "income", "2026-02-03", checking, sales, "20000")
	purchase := journal(t, db, s, "purchase", "2026-02-09", software, checking, "6000")
	transfer := journal(t, db, s, "transfer", "2026-02-14", savings, checking, "10000")
	// Posted on the last day; the bank clears it next month.
	cheque := journal(t, db, s, "cheque", "2026-02-28", software, checking, "3000")
	doc := statementDocument(t, db, s, "checking-feb")
	q := ledger.ImportStatement{RequestKey: "feb", AccountID: checking, DocumentID: doc, PeriodStart: "2026-02-01", PeriodEnd: "2026-02-28", OpeningMinor: "100000", ClosingMinor: "103500",
		Rows: []ledger.StatementRowInput{row(1, "2026-02-03", "20000", "Client payment"), row(2, "2026-02-10", "-6000", "Example Design Tools"), row(3, "2026-02-14", "-10000", "Transfer to savings"), row(4, "2026-02-27", "-500", "Monthly account fee")}}
	st := importStatement(t, db, s, q)
	if st.Kind != "cash" || st.Currency != "USD" || st.RowCount != 4 || st.Status != "not_reviewed" {
		t.Fatal("unexpected statement", st)
	}
	// Importing the same statement again adds nothing.
	q.RequestKey = "feb-again"
	if again := importStatement(t, db, s, q); again.ID != st.ID || !again.AlreadyRecorded {
		t.Fatal("re-importing the same statement added a second one", again)
	}
	var listed struct {
		Statements []ledger.Statement `json:"statements"`
	}
	if json.Unmarshal(db.Call(t, s, "list_statements", ledger.StatementListQuery{}), &listed) != nil || len(listed.Statements) != 1 {
		t.Fatal("one statement expected")
	}
	// Nothing matched yet: every row is on the statement but not in the Book.
	r := reconciliation(t, db, s, st.ID)
	if r.Summary.NotInBookCount != 4 || r.Summary.DifferenceMinor != "3500" || r.Summary.Explained || r.Summary.StatementDifference != "0" {
		t.Fatal("unexpected first comparison", r.Summary)
	}
	sug := suggestions(t, db, s, st.ID)
	want := map[string]string{income.ID: "", purchase.ID: "", transfer.ID: ""}
	for _, x := range sug {
		if x.Kind == "match" {
			delete(want, x.EntryIDs[0])
		}
	}
	if len(sug) != 3 || len(want) != 0 {
		t.Fatal("the three Book entries are suggested; the fee is not", sug)
	}
	// Suggestions record nothing.
	if r := reconciliation(t, db, s, st.ID); r.Summary.MatchedMinor != "0" {
		t.Fatal("a suggestion recorded a match")
	}
	acceptMatches(t, db, s, "match", sug)
	r = reconciliation(t, db, s, st.ID)
	if r.Summary.MatchedMinor != "4000" || r.Summary.NotInBookMinor != "-500" || r.Summary.NotInBookCount != 1 || r.Summary.DifferenceMinor != "-500" ||
		r.Summary.OutstandingCount != 1 || r.Summary.OutstandingMinor != "-3000" || r.Outstanding[0].EntryID != cheque.ID || len(r.Matches) != 3 {
		t.Fatal("the fee is on the statement but not in the Book, and the cheque is outstanding", r.Summary)
	}
	// The database refuses a reconciled review that isn't explained.
	err := callError(db, s, "review_statement", ledger.ReviewStatement{RequestKey: "too-early", StatementID: st.ID, ExpectedRevision: 0, Outcome: "reconciled"})
	var refused *ledger.RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "not fully explained") {
		t.Fatal("an unexplained statement was reconciled", err)
	}
	db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "needs-work", StatementID: st.ID, ExpectedRevision: 0, Outcome: "needs_work", Reason: "A 5.00 fee is missing from the Book"})
	// An agent that only proposes prepares the fee entry and its adjustment.
	proposer := connectionScope(t, db, book.ID, "propose")
	feeEntry := ledger.PostJournal{RequestKey: "fee", EconomicDate: "2026-02-27", AccountingDate: "2026-02-27", Description: "Monthly account fee",
		Lines: []ledger.PostLine{{AccountID: fees, Side: "debit", AmountMinor: "500"}, {AccountID: checking, Side: "credit", AmountMinor: "500"}}}
	p := prepareOperation(t, db, proposer, "propose-fee", "post_journal", feeEntry)
	approveOperation(t, db, s, p, s)
	var fee ledger.Entry
	if json.Unmarshal(executeOperation(t, db, s, p), &fee) != nil || fee.ID == "" {
		t.Fatal("the approved fee entry was not posted")
	}
	feeRow := ""
	for _, x := range r.Rows {
		if x.RowNumber == 4 {
			feeRow = x.ID
		}
	}
	adjust := prepareOperation(t, db, proposer, "propose-adjust", "record_statement_adjustment", ledger.RecordStatementAdjustment{RequestKey: "adjust", RowID: feeRow, EntryID: fee.ID})
	if adjust.Preview.StatementChange == nil || adjust.Preview.StatementChange.Change != "adjust" {
		t.Fatal("an adjustment proposal previews its decision", adjust.Preview)
	}
	// A proposed explanation still counts as outstanding.
	r = reconciliation(t, db, s, st.ID)
	if r.Rows[3].State != "proposed" || r.Summary.ProposedCount != 1 || r.Summary.NotInBookCount != 1 || r.Summary.Explained {
		t.Fatal("a proposed adjustment explains nothing until approved", r.Rows[3], r.Summary)
	}
	approveOperation(t, db, s, adjust, s)
	executeOperation(t, db, s, adjust)
	r = reconciliation(t, db, s, st.ID)
	if !r.Summary.Explained || r.Summary.DifferenceMinor != "0" || r.Summary.AdjustedMinor != "-500" || r.Summary.OutstandingCount != 1 {
		t.Fatal("after the fee entry the statement is explained; the cheque stays outstanding", r.Summary)
	}
	var reviewed ledger.Statement
	if json.Unmarshal(db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "reconciled", StatementID: st.ID, ExpectedRevision: 1, Outcome: "reconciled"}), &reviewed) != nil ||
		reviewed.Status != "reconciled" || reviewed.Review.Revision != 2 || len(reviewed.Review.ReportSHA256) != 64 {
		t.Fatal("the explained statement reconciles at 1,035.00", reviewed)
	}
	if err := callError(db, s, "review_statement", ledger.ReviewStatement{RequestKey: "stale", StatementID: st.ID, ExpectedRevision: 1, Outcome: "needs_work", Reason: "x"}); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("a stale review was accepted", err)
	}
	// A matched entry is reversed only after its match is revoked.
	if err := callError(db, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: purchase.ID, AccountingDate: "2026-03-02", Reason: "Wrong amount"}); err == nil {
		t.Fatal("a matched entry was reversed")
	}
	db.Call(t, s, "revoke_statement_match", ledger.RevokeStatementMatch{RequestKey: "revoke", MatchID: r.Matches[1].ID, Reason: "Matched to the wrong entry"})
	if r := reconciliation(t, db, s, st.ID); r.Summary.Explained || len(r.Matches) != 2 || r.Summary.NotInBookCount != 1 || r.Statement.Status != "needs_work" || r.Statement.Review.Outcome != "reconciled" {
		t.Fatal("a revoked match releases its row", r.Summary)
	}
	db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: purchase.ID, AccountingDate: "2026-03-02", Reason: "Wrong amount"})
	// Lines of a reversed entry can't be matched.
	line := entryLine(t, db, s, purchase, checking)
	if err := callError(db, s, "match_statement", ledger.MatchStatement{RequestKey: "reversed", Rows: []ledger.StatementMatchPart{{RowID: r.Rows[1].ID}}, Lines: []ledger.StatementMatchPart{{LineID: line}}}); !errors.As(err, &refused) {
		t.Fatal("a reversed entry's line was matched", err)
	}
}

func TestStatementDuplicatesAreDecisionsNotGuesses(t *testing.T) {
	db := testutil.New(t)
	_, s, byCode := chartBook(t, db, "business-v1")
	checking, fees := byCode["1110"].ID, byCode["6600"].ID
	doc := statementDocument(t, db, s, "march")
	// A genuine second same-day, same-amount fee is kept.
	first := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "march", AccountID: checking, DocumentID: doc, PeriodStart: "2026-03-01", PeriodEnd: "2026-03-15", OpeningMinor: "0", ClosingMinor: "-1000",
		Rows: []ledger.StatementRowInput{row(1, "2026-03-05", "-500", "Wire fee"), row(2, "2026-03-05", "-500", "Wire fee")}})
	if first.RowCount != 2 || reconciliation(t, db, s, first.ID).Summary.NotInBookCount != 2 {
		t.Fatal("two identical rows on one statement are two rows")
	}
	// An overlapping statement repeats the second fee.
	doc2 := statementDocument(t, db, s, "march-b")
	second := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "march-b", AccountID: checking, DocumentID: doc2, PeriodStart: "2026-03-05", PeriodEnd: "2026-03-31", OpeningMinor: "-500", ClosingMinor: "-1000",
		Rows: []ledger.StatementRowInput{row(2, "2026-03-05", "-500", "Wire fee")}})
	sug := suggestions(t, db, s, second.ID)
	if len(sug) != 1 || sug[0].Kind != "duplicate" {
		t.Fatal("an overlapping row is suggested as a duplicate", sug)
	}
	r2 := reconciliation(t, db, s, second.ID)
	// Suggestions record nothing; the decision does.
	if r2.Summary.DuplicateCount != 0 {
		t.Fatal("a suggestion recorded a duplicate")
	}
	db.Call(t, s, "mark_statement_duplicate", ledger.MarkStatementDuplicate{RequestKey: "dup", RowID: sug[0].RowID, CanonicalRowID: sug[0].CanonicalID, Reason: "The statements overlap on March 5"})
	r2 = reconciliation(t, db, s, second.ID)
	if r2.Summary.DuplicateCount != 1 || r2.Rows[0].State != "duplicate" || r2.Summary.NotInBookCount != 1 || r2.Summary.StatementDifference != "0" {
		t.Fatal("a duplicate retains its observed movement and needs its canonical row explained", r2.Summary)
	}
	// A duplicate can't be matched.
	fee := journal(t, db, s, "fee", "2026-03-05", fees, checking, "500")
	var refused *ledger.RefusedError
	if err := callError(db, s, "match_statement", ledger.MatchStatement{RequestKey: "dup-match", Rows: []ledger.StatementMatchPart{{RowID: r2.Rows[0].ID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, fee, checking)}}}); !errors.As(err, &refused) {
		t.Fatal("a duplicate row was matched", err)
	}
	var canonical ledger.StatementMatchRead
	if json.Unmarshal(db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: "canonical-match", Rows: []ledger.StatementMatchPart{{RowID: sug[0].CanonicalID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, fee, checking)}}}), &canonical) != nil {
		t.Fatal("invalid canonical match")
	}
	r2 = reconciliation(t, db, s, second.ID)
	if !r2.Summary.Explained || r2.Summary.DuplicateMinor != "-500" || r2.Summary.DifferenceMinor != "0" {
		t.Fatal("an overlapping statement uses its canonical explanation without reallocating money", r2.Summary)
	}
	db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "duplicate-reviewed", StatementID: second.ID, Outcome: "reconciled"})
	db.Call(t, s, "revoke_statement_match", ledger.RevokeStatementMatch{RequestKey: "undo-canonical", MatchID: canonical.ID, Reason: "Wrong fee"})
	if r := reconciliation(t, db, s, second.ID); r.Statement.Status != "needs_work" || r.Summary.Explained || r.Statement.Review.Outcome != "reconciled" {
		t.Fatal("a revoked canonical explanation also invalidates the duplicate statement's current status", r)
	}
}

func TestStatementAdjustmentsSharePostedLineAllocation(t *testing.T) {
	for _, order := range []string{"two adjustments", "adjust then match", "match then adjust"} {
		t.Run(order, func(t *testing.T) {
			db := testutil.New(t)
			_, s, accounts := chartBook(t, db, "business-v1")
			cash, fees := accounts["1110"].ID, accounts["6600"].ID
			entry := journal(t, db, s, "fee", "2025-01-10", fees, cash, "500")
			st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "statement", AccountID: cash, DocumentID: statementDocument(t, db, s, "two-fees"), PeriodStart: "2025-01-01", PeriodEnd: "2025-01-31", OpeningMinor: "100000", ClosingMinor: "99000",
				Rows: []ledger.StatementRowInput{row(1, "2025-01-10", "-500", "First fee"), row(2, "2025-01-11", "-500", "Second fee")}})
			r := reconciliation(t, db, s, st.ID)
			adjust := func(key, rowID string) error {
				return callError(db, s, "record_statement_adjustment", ledger.RecordStatementAdjustment{RequestKey: key, RowID: rowID, EntryID: entry.ID})
			}
			match := func(key, rowID string) error {
				return callError(db, s, "match_statement", ledger.MatchStatement{RequestKey: key, Rows: []ledger.StatementMatchPart{{RowID: rowID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, entry, cash), AmountMinor: "-500"}}})
			}
			var err error
			switch order {
			case "two adjustments":
				succeeded(t, adjust("first", r.Rows[0].ID))
				err = adjust("second", r.Rows[1].ID)
			case "adjust then match":
				succeeded(t, adjust("first", r.Rows[0].ID))
				err = match("second", r.Rows[1].ID)
			case "match then adjust":
				succeeded(t, match("first", r.Rows[0].ID))
				err = adjust("second", r.Rows[1].ID)
			}
			var refused *ledger.RefusedError
			if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "allocated beyond") {
				t.Fatal("the same posted movement must not explain a second row", err)
			}
			if r := reconciliation(t, db, s, st.ID); r.Summary.Explained || r.Summary.NotInBookCount != 1 || r.Summary.DifferenceMinor != "-500" {
				t.Fatal("the second fee is still missing", r.Summary)
			}
		})
	}
}

func TestStatementDuplicatesCannotMultiplyAnObservation(t *testing.T) {
	for _, sameStatement := range []bool{true, false} {
		name := "one statement"
		if !sameStatement {
			name = "overlapping statements"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.New(t)
			_, s, accounts := chartBook(t, db, "business-v1")
			cash, fees := accounts["1110"].ID, accounts["6600"].ID
			entry := journal(t, db, s, "fee", "2025-01-10", fees, cash, "500")
			st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "two-fees", AccountID: cash, DocumentID: statementDocument(t, db, s, "two-distinct-fees"), PeriodStart: "2025-01-01", PeriodEnd: "2025-01-31", OpeningMinor: "0", ClosingMinor: "-1000", Rows: []ledger.StatementRowInput{row(1, "2025-01-10", "-500", "First fee"), row(2, "2025-01-11", "-500", "Second fee")}})
			r := reconciliation(t, db, s, st.ID)
			canonical := r.Rows[0].ID
			if !sameStatement {
				other := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "one-fee", AccountID: cash, DocumentID: statementDocument(t, db, s, "one-fee-observation"), PeriodStart: "2025-01-01", PeriodEnd: "2025-01-10", OpeningMinor: "0", ClosingMinor: "-500", Rows: []ledger.StatementRowInput{row(1, "2025-01-10", "-500", "First fee")}})
				canonical = reconciliation(t, db, s, other.ID).Rows[0].ID
				db.Call(t, s, "mark_statement_duplicate", ledger.MarkStatementDuplicate{RequestKey: "first-duplicate", RowID: r.Rows[0].ID, CanonicalRowID: canonical, Reason: "Same first fee"})
			}
			db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: "canonical", Rows: []ledger.StatementMatchPart{{RowID: canonical}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, entry, cash)}}})
			var refused *ledger.RefusedError
			if err := callError(db, s, "mark_statement_duplicate", ledger.MarkStatementDuplicate{RequestKey: "second-duplicate", RowID: r.Rows[1].ID, CanonicalRowID: canonical, Reason: "Trying to reuse the first fee"}); !errors.As(err, &refused) {
				t.Fatal("two rows of one statement cannot consume one canonical observation", err)
			}
			if r := reconciliation(t, db, s, st.ID); r.Summary.Explained || r.Summary.NotInBookCount != 1 {
				t.Fatal("the genuine second fee remains unexplained", r.Summary)
			}
		})
	}
}

func TestStatementComparisonKeepsCurrencyScale(t *testing.T) {
	db := testutil.New(t)
	book, err := db.Owner.CreateBook(db.Context, "Example three-decimal Book", "USD", 3)
	succeeded(t, err)
	connection, err := db.Owner.CreateConnection(db.Context, book.ID, "Example connection", "write")
	succeeded(t, err)
	s := ledger.Scope{TenantID: connection.TenantID, BookID: connection.BookID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "write"}
	cash := roleAccount(t, db, s, "1000", "asset", "cash")
	fees := db.Account(t, s, "5000", "expense")
	old := journal(t, db, s, "old-fee", "2025-01-10", fees.ID, cash.ID, "1000")
	tiny := journal(t, db, s, "tiny-fee", "2025-01-11", fees.ID, cash.ID, "1")
	newFee := journal(t, db, s, "another-fee", "2025-01-12", fees.ID, cash.ID, "1000")
	legacy := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "legacy", AccountID: cash.ID, DocumentID: statementDocument(t, db, s, "three-decimal-statement"), PeriodStart: "2025-01-01", PeriodEnd: "2025-01-31", OpeningMinor: "0", ClosingMinor: "-1001", Rows: []ledger.StatementRowInput{row(1, "2025-01-10", "-1000", "Fee"), row(2, "2025-01-11", "-1", "Small fee")}})
	usd, scale := "USD", 2
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "two-decimal-unit", AccountID: cash.ID, ExpectedRevision: cash.Revision, Code: cash.Code, Name: cash.Name, Currency: &usd, Scale: &scale})
	oldRows := reconciliation(t, db, s, legacy.ID)
	for i, entry := range []ledger.Entry{old, tiny} {
		db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: fmt.Sprintf("legacy-match-%d", i), Rows: []ledger.StatementMatchPart{{RowID: oldRows.Rows[i].ID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, entry, cash.ID)}}})
	}
	db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "legacy-review", StatementID: legacy.ID, Outcome: "reconciled"})
	if r := reconciliation(t, db, s, legacy.ID); r.Statement.Scale != 3 || !r.Summary.Explained {
		t.Fatal("a historical statement keeps its original scale after account declaration", r)
	}
	wrong := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "wrong-raw-amount", AccountID: cash.ID, DocumentID: statementDocument(t, db, s, "two-decimal-wrong-amount"), PeriodStart: "2025-02-01", PeriodEnd: "2025-02-28", OpeningMinor: "0", ClosingMinor: "-1000", Rows: []ledger.StatementRowInput{row(1, "2025-02-01", "-1000", "Different value")}})
	wrongRow := reconciliation(t, db, s, wrong.ID).Rows[0].ID
	var refused *ledger.RefusedError
	if err := callError(db, s, "match_statement", ledger.MatchStatement{RequestKey: "raw-number-is-not-value", Rows: []ledger.StatementMatchPart{{RowID: wrongRow}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, newFee, cash.ID), AmountMinor: "-1000"}}}); !errors.As(err, &refused) {
		t.Fatal("1000 three-decimal units cannot match 1000 two-decimal units", err)
	}
	if err := callError(db, s, "match_statement", ledger.MatchStatement{RequestKey: "mixed-scales", Rows: []ledger.StatementMatchPart{{RowID: oldRows.Rows[0].ID, AmountMinor: "-1000"}, {RowID: wrongRow, AmountMinor: "-1000"}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, newFee, cash.ID), AmountMinor: "-2000"}}}); !errors.As(err, &refused) || !strings.Contains(refused.Reason, "one unit") {
		t.Fatal("one match cannot combine different scales", err)
	}
	if err := callError(db, s, "mark_statement_duplicate", ledger.MarkStatementDuplicate{RequestKey: "mixed-scale-duplicate", RowID: wrongRow, CanonicalRowID: oldRows.Rows[0].ID, Reason: "Same raw number"}); !errors.As(err, &refused) {
		t.Fatal("equal raw numbers in different scales are not duplicate amounts", err)
	}
	correct := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "exact-rescale", AccountID: cash.ID, DocumentID: statementDocument(t, db, s, "two-decimal-correct-amount"), PeriodStart: "2025-02-01", PeriodEnd: "2025-02-28", OpeningMinor: "0", ClosingMinor: "-100", Rows: []ledger.StatementRowInput{row(1, "2025-02-01", "-100", "One dollar fee")}})
	r := reconciliation(t, db, s, correct.ID)
	if r.Summary.UnresolvedCount != 1 {
		t.Fatal("a fractional target minor unit is unresolved instead of rounded", r.Summary)
	}
	if err := callError(db, s, "match_statement", ledger.MatchStatement{RequestKey: "reuse-old-in-new-scale", Rows: []ledger.StatementMatchPart{{RowID: r.Rows[0].ID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, old, cash.ID), AmountMinor: "-100"}}}); !errors.As(err, &refused) {
		t.Fatal("rescaling does not release an earlier allocation", err)
	}
	db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: "correct-value", Rows: []ledger.StatementMatchPart{{RowID: r.Rows[0].ID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, newFee, cash.ID)}}})
	db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "correct-review", StatementID: correct.ID, Outcome: "reconciled"})
	if r := reconciliation(t, db, s, correct.ID); !r.Summary.Explained || r.Summary.MatchedMinor != "-100" {
		t.Fatal("exact rescaling preserves the monetary value", r.Summary)
	}
}

func TestStatementOutstandingAndCompletenessCoverEachAccountLeg(t *testing.T) {
	db := testutil.New(t)
	_, s, accounts := chartBook(t, db, "business-v1")
	a, b := accounts["1110"].ID, accounts["1120"].ID
	transfer := journal(t, db, s, "transfer", "2025-03-10", a, b, "500")
	statement := func(key, account, amount string) ledger.StatementReconciliation {
		st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: key, AccountID: account, DocumentID: statementDocument(t, db, s, key), PeriodStart: "2025-03-01", PeriodEnd: "2025-03-31", OpeningMinor: "0", ClosingMinor: amount,
			Rows: []ledger.StatementRowInput{row(1, "2025-03-10", amount, "Transfer")}})
		return reconciliation(t, db, s, st.ID)
	}
	ra, rb := statement("deposit", a, "500"), statement("withdrawal", b, "-500")
	db.Call(t, s, "record_statement_adjustment", ledger.RecordStatementAdjustment{RequestKey: "adjust-a", RowID: ra.Rows[0].ID, EntryID: transfer.ID})
	db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "review-a", StatementID: ra.Statement.ID, Outcome: "reconciled"})
	if r := reconciliation(t, db, s, rb.Statement.ID); r.Summary.OutstandingCount != 1 || r.Outstanding[0].EntryID != transfer.ID || r.Summary.OutstandingMinor != "-500" {
		t.Fatal("an adjustment on one account leaves the other leg outstanding", r.Summary)
	}
	db.Call(t, s, "record_statement_adjustment", ledger.RecordStatementAdjustment{RequestKey: "adjust-b", RowID: rb.Rows[0].ID, EntryID: transfer.ID})
	db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "review-b", StatementID: rb.Statement.ID, Outcome: "reconciled"})
	if r := reconciliation(t, db, s, rb.Statement.ID); !r.Summary.Explained || r.Statement.Status != "reconciled" {
		t.Fatal("each account's adjustment explains its own leg", r.Summary)
	}
}

func TestStatementOutstandingRespectsStatementCutoff(t *testing.T) {
	db := testutil.New(t)
	_, s, accounts := chartBook(t, db, "business-v1")
	cash, fees := accounts["1110"].ID, accounts["6600"].ID
	entry := journal(t, db, s, "payment", "2025-01-30", fees, cash, "500")
	jan := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "january", AccountID: cash, DocumentID: statementDocument(t, db, s, "january"), PeriodStart: "2025-01-01", PeriodEnd: "2025-01-31", OpeningMinor: "100000", ClosingMinor: "100000"})
	feb := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "february", AccountID: cash, DocumentID: statementDocument(t, db, s, "february"), PeriodStart: "2025-02-01", PeriodEnd: "2025-02-28", OpeningMinor: "100000", ClosingMinor: "99500", Rows: []ledger.StatementRowInput{row(1, "2025-02-02", "-500", "Payment cleared")}})
	r := reconciliation(t, db, s, feb.ID)
	db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: "cleared", Rows: []ledger.StatementMatchPart{{RowID: r.Rows[0].ID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, entry, cash)}}})
	if r := reconciliation(t, db, s, jan.ID); r.Summary.OutstandingCount != 1 || r.Summary.OutstandingMinor != "-500" {
		t.Fatal("a later clearing does not remove a payment outstanding at the earlier cutoff", r.Summary)
	}
	if r := reconciliation(t, db, s, feb.ID); r.Summary.OutstandingCount != 0 {
		t.Fatal("the payment is cleared at the later cutoff", r.Summary)
	}
}

func TestCombinedStatementMatchPreservesCutoffAllocation(t *testing.T) {
	for _, multipleLines := range []bool{false, true} {
		name := "one posted line"
		if multipleLines {
			name = "several posted lines"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.New(t)
			_, s, accounts := chartBook(t, db, "business-v1")
			cash, fees := accounts["1110"].ID, accounts["6600"].ID
			amount := "1000"
			if multipleLines {
				amount = "500"
			}
			entry := journal(t, db, s, "payment", "2025-01-30", fees, cash, amount)
			parts := []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, entry, cash)}}
			if multipleLines {
				other := journal(t, db, s, "other-payment", "2025-01-30", fees, cash, amount)
				parts = append(parts, ledger.StatementMatchPart{LineID: entryLine(t, db, s, other, cash)})
			}
			jan := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "jan", AccountID: cash, DocumentID: statementDocument(t, db, s, "jan-cutoff"), PeriodStart: "2025-01-01", PeriodEnd: "2025-01-31", OpeningMinor: "0", ClosingMinor: "-500", Rows: []ledger.StatementRowInput{row(1, "2025-01-31", "-500", "First clearing")}})
			feb := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "feb", AccountID: cash, DocumentID: statementDocument(t, db, s, "feb-cutoff"), PeriodStart: "2025-02-01", PeriodEnd: "2025-02-28", OpeningMinor: "-500", ClosingMinor: "-1000", Rows: []ledger.StatementRowInput{row(1, "2025-02-02", "-500", "Second clearing")}})
			r1, r2 := reconciliation(t, db, s, jan.ID), reconciliation(t, db, s, feb.ID)
			db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: "combined-clearing", Rows: []ledger.StatementMatchPart{{RowID: r1.Rows[0].ID}, {RowID: r2.Rows[0].ID}}, Lines: parts})
			r := reconciliation(t, db, s, jan.ID)
			if multipleLines {
				if r.Summary.UnresolvedCount != 2 || len(r.Outstanding) != 2 || !r.Outstanding[0].Unresolved || r.Outstanding[0].RemainingMinor != "" || r.Outstanding[1].RemainingMinor != "" {
					t.Fatal("an ambiguous per-line allocation remains unresolved at the earlier cutoff", r)
				}
			} else if r.Summary.OutstandingMinor != "-500" || r.Summary.OutstandingCount != 1 || r.Summary.UnresolvedCount != 0 {
				t.Fatal("one line has an exact half-cleared amount at the earlier cutoff", r.Summary)
			}
			if r := reconciliation(t, db, s, feb.ID); r.Summary.OutstandingCount != 0 || r.Summary.UnresolvedCount != 0 {
				t.Fatal("all lines are fully covered after both clearings", r.Summary)
			}
		})
	}
}

func TestStatementKeepsHistoricalBookCurrencyAfterDeclaration(t *testing.T) {
	db := testutil.New(t)
	_, s, accounts := chartBook(t, db, "business-v1")
	cash, fees := accounts["1110"], accounts["6600"].ID
	entry := journal(t, db, s, "fee", "2025-01-10", fees, cash.ID, "500")
	usd, scale := "USD", 2
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "declare-unit", AccountID: cash.ID, ExpectedRevision: cash.Revision, Code: cash.Code, Name: cash.Name, Currency: &usd, Scale: &scale})
	st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "statement", AccountID: cash.ID, DocumentID: statementDocument(t, db, s, "declared-unit"), PeriodStart: "2025-01-01", PeriodEnd: "2025-01-31", OpeningMinor: "0", ClosingMinor: "-500", Rows: []ledger.StatementRowInput{row(1, "2025-01-10", "-500", "Fee")}})
	r := reconciliation(t, db, s, st.ID)
	db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: "match", Rows: []ledger.StatementMatchPart{{RowID: r.Rows[0].ID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, entry, cash.ID)}}})
	if r := reconciliation(t, db, s, st.ID); !r.Summary.Explained || r.Summary.UnresolvedCount != 0 {
		t.Fatal("a later declaration preserves the known historical Book-currency amount", r.Summary)
	}
}

func TestReconciliationTotalsDoNotWrap(t *testing.T) {
	db := testutil.New(t)
	_, s, accounts := chartBook(t, db, "business-v1")
	cash, fees := accounts["1110"].ID, accounts["6600"].ID
	entry := journal(t, db, s, "one-minor-unit", "2025-01-10", fees, cash, "1")
	st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "statement", AccountID: cash, DocumentID: statementDocument(t, db, s, "large-balances"), PeriodStart: "2025-01-01", PeriodEnd: "2025-01-31", OpeningMinor: "-9223372036854775808", ClosingMinor: "9223372036854775807", Rows: []ledger.StatementRowInput{row(1, "2025-01-10", "-1", "Fee")}})
	r := reconciliation(t, db, s, st.ID)
	db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: "match", Rows: []ledger.StatementMatchPart{{RowID: r.Rows[0].ID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, entry, cash)}}})
	if r := reconciliation(t, db, s, st.ID); r.Summary.Explained || r.Summary.DifferenceMinor != "18446744073709551616" || r.Summary.StatementDifference != "18446744073709551616" {
		t.Fatal("statement arithmetic stays exact beyond an individual bigint", r.Summary)
	}
	var refused *ledger.RefusedError
	if err := callError(db, s, "review_statement", ledger.ReviewStatement{RequestKey: "review", StatementID: st.ID, Outcome: "reconciled"}); !errors.As(err, &refused) {
		t.Fatal("the same inexact statement must be refused by the database", err)
	}
}

func TestAdjustedStatementLosesCurrentStatusAfterReversal(t *testing.T) {
	db := testutil.New(t)
	_, s, accounts := chartBook(t, db, "business-v1")
	cash, fees := accounts["1110"].ID, accounts["6600"].ID
	entry := journal(t, db, s, "fee", "2025-01-10", fees, cash, "500")
	st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "statement", AccountID: cash, DocumentID: statementDocument(t, db, s, "adjusted-fee"), PeriodStart: "2025-01-01", PeriodEnd: "2025-01-31", OpeningMinor: "0", ClosingMinor: "-500", Rows: []ledger.StatementRowInput{row(1, "2025-01-10", "-500", "Fee")}})
	r := reconciliation(t, db, s, st.ID)
	db.Call(t, s, "record_statement_adjustment", ledger.RecordStatementAdjustment{RequestKey: "adjust", RowID: r.Rows[0].ID, EntryID: entry.ID})
	db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "review", StatementID: st.ID, Outcome: "reconciled"})
	before := reconciliation(t, db, s, st.ID)
	db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: entry.ID, AccountingDate: "2025-01-11", Reason: "Wrong fee entry"})
	after := reconciliation(t, db, s, st.ID)
	if after.Statement.Status != "needs_work" || after.Summary.Explained || after.Summary.NotInBookCount != 1 || after.Statement.Review.Outcome != "reconciled" || after.Statement.Review.ReportSHA256 != before.Statement.Review.ReportSHA256 {
		t.Fatal("the current statement loses coverage while its signed history stays intact", after)
	}
	corrected := journal(t, db, s, "correct-fee", "2025-01-10", fees, cash, "500")
	db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: "correct-match", Rows: []ledger.StatementMatchPart{{RowID: r.Rows[0].ID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, corrected, cash)}}})
	if r := reconciliation(t, db, s, st.ID); !r.Summary.Explained {
		t.Fatal("a replacement match can explain a row after its adjustment was reversed", r.Summary)
	}
	db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "review-corrected", StatementID: st.ID, ExpectedRevision: 1, Outcome: "reconciled"})
	if r := reconciliation(t, db, s, st.ID); r.Statement.Status != "reconciled" || r.Statement.Review.Revision != 2 {
		t.Fatal("the repaired statement can be signed off again", r.Statement)
	}
}

// A card statement separates purchase, refund and payment; the payment never
// counts as spending.
func TestCardStatementSeparatesPurchaseRefundAndPayment(t *testing.T) {
	db := testutil.New(t)
	book, s, byCode := chartBook(t, db, "business-v1")
	checking, card, travel := byCode["1110"].ID, byCode["2100"].ID, byCode["6400"].ID
	purchase := journal(t, db, s, "buy", "2026-04-03", travel, card, "5000")
	refund := journal(t, db, s, "refund", "2026-04-08", card, travel, "2000")
	payment := journal(t, db, s, "pay", "2026-04-20", card, checking, "10000")
	doc := statementDocument(t, db, s, "card-apr")
	refundRow := row(2, "2026-04-08", "-2000", "Example Rail refund")
	refundRow.Lifecycle = "refund"
	pending := row(4, "2026-04-29", "1200", "Example Cafe (pending)")
	pending.Lifecycle = "pending"
	st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "card", AccountID: card, DocumentID: doc, PeriodStart: "2026-04-01", PeriodEnd: "2026-04-30", OpeningMinor: "10000", ClosingMinor: "3000",
		Rows: []ledger.StatementRowInput{row(1, "2026-04-03", "5000", "Example Rail"), refundRow, row(3, "2026-04-20", "-10000", "Payment, thank you"), pending}})
	if st.Kind != "card" {
		t.Fatal("a card statement is signed as a card", st.Kind)
	}
	sug := suggestions(t, db, s, st.ID)
	if len(sug) != 3 {
		t.Fatal("purchase, refund and payment are each suggested; the pending row is not", sug)
	}
	acceptMatches(t, db, s, "card", sug)
	r := reconciliation(t, db, s, st.ID)
	if !r.Summary.Explained || r.Summary.PendingCount != 1 || r.Summary.MatchedMinor != "-7000" {
		t.Fatal("the card statement is explained; the pending row never counts", r.Summary)
	}
	for _, x := range r.Rows {
		if x.RowNumber == 4 && x.State != "pending" {
			t.Fatal("a pending row stays pending", x.State)
		}
	}
	db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "ok", StatementID: st.ID, Outcome: "reconciled"})
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	succeeded(t, err)
	pl, err := owner.FinancialReport(db.Context, ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.ProfitLoss, From: "2026-04-01", To: "2026-04-30"}, false)
	if err != nil || pl.Reconciliation["expenses"] != "3000" {
		t.Fatal("spending is the purchase less the refund, never the payment", err, pl.Reconciliation)
	}
	_ = purchase
	_ = refund
	_ = payment
}

// EUR and a zero-decimal currency reconcile in native amounts.
func TestForeignStatementsReconcileInNativeAmounts(t *testing.T) {
	db := testutil.New(t)
	_, s, byCode := chartBook(t, db, "business-v1")
	travel := byCode["6400"].ID
	two, zero := 2, 0
	account := func(key, code, currency string, scale *int) ledger.Account {
		var a ledger.Account
		if json.Unmarshal(db.Call(t, s, "create_account", ledger.CreateAccount{RequestKey: key, Class: "asset", Code: code, Name: "Example " + currency + " account", Currency: currency, Scale: scale, Role: "cash"}), &a) != nil {
			t.Fatal("invalid account")
		}
		return a
	}
	eur, jpy := account("eur", "1130", "EUR", &two), account("jpy", "1140", "JPY", &zero)
	spend := func(key, date string, a ledger.Account, scale *int, native, rate, usd string) ledger.Entry {
		in := ledger.CurrencyAmount{Currency: a.Currency, Scale: scale, AmountMinor: native}
		applied := ledger.AppliedRate{Base: a.Currency, Quote: "USD", Rate: rate, RequestedDate: date, ObservationDate: date, Provenance: "Synthetic reference rate"}
		var e ledger.Entry
		if json.Unmarshal(db.Call(t, s, "post_journal", ledger.PostJournal{RequestKey: key, EconomicDate: date, AccountingDate: date, Description: "Example " + key,
			Lines: []ledger.PostLine{{AccountID: travel, Side: "debit", AmountMinor: usd},
				{AccountID: a.ID, Side: "credit", AmountMinor: usd, Native: &ledger.NativeMovement{Currency: a.Currency, Scale: scale, AmountMinor: native},
					Valuation: &ledger.Valuation{Basis: "applied_quote", Policy: "quoted-rate-half-even-v1", Input: &in, Rate: &applied}}}}), &e) != nil {
			t.Fatal("invalid entry")
		}
		return e
	}
	spend("hotel", "2026-05-04", eur, &two, "10000", "1.1", "11000")
	spend("rail", "2026-05-06", jpy, &zero, "5000", "0.0067", "3350")
	for _, c := range []struct {
		key      string
		account  ledger.Account
		currency string
		amount   string
	}{{"eur", eur, "EUR", "-10000"}, {"jpy", jpy, "JPY", "-5000"}} {
		doc := statementDocument(t, db, s, "statement-"+c.key)
		st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "st-" + c.key, AccountID: c.account.ID, DocumentID: doc, PeriodStart: "2026-05-01", PeriodEnd: "2026-05-31",
			OpeningMinor: "0", ClosingMinor: c.amount, Rows: []ledger.StatementRowInput{row(1, "2026-05-05", c.amount, "Example merchant")}})
		if st.Currency != c.currency {
			t.Fatal("a statement is in its account's currency", st.Currency)
		}
		sug := suggestions(t, db, s, st.ID)
		if len(sug) != 1 || sug[0].AmountMinor != c.amount {
			t.Fatal("the native amount matches", sug)
		}
		acceptMatches(t, db, s, c.key, sug)
		if r := reconciliation(t, db, s, st.ID); !r.Summary.Explained {
			t.Fatal("a foreign statement reconciles in native amounts", r.Summary)
		}
		db.Call(t, s, "review_statement", ledger.ReviewStatement{RequestKey: "ok-" + c.key, StatementID: st.ID, Outcome: "reconciled"})
	}
}

func TestStatementDecisionsFollowConnectionLevels(t *testing.T) {
	db := testutil.New(t)
	book, writer, byCode := chartBook(t, db, "business-v1")
	checking, sales := byCode["1110"].ID, byCode["4100"].ID
	doc := statementDocument(t, db, writer, "levels")
	q := ledger.ImportStatement{RequestKey: "levels", AccountID: checking, DocumentID: doc, PeriodStart: "2026-06-01", PeriodEnd: "2026-06-30", OpeningMinor: "0", ClosingMinor: "100",
		Rows: []ledger.StatementRowInput{row(1, "2026-06-02", "100", "Deposit")}}
	proposer, reader := connectionScope(t, db, book.ID, "propose"), connectionScope(t, db, book.ID, "read")
	for _, scope := range []ledger.Scope{proposer, reader} {
		if err := callError(db, scope, "import_statement", q); !errors.Is(err, ledger.ErrDenied) {
			t.Fatal("only write agents import directly", scope.Access, err)
		}
	}
	p := prepareOperation(t, db, proposer, "propose-import", "import_statement", q)
	if p.Preview.StatementChange == nil || p.Preview.StatementChange.Change != "import" || p.Preview.StatementChange.Rows != 1 {
		t.Fatal("an import proposal previews the statement", p.Preview)
	}
	var none struct {
		Statements []ledger.Statement `json:"statements"`
	}
	if json.Unmarshal(db.Call(t, reader, "list_statements", ledger.StatementListQuery{}), &none) != nil || len(none.Statements) != 0 {
		t.Fatal("preparing an import recorded it")
	}
	approveOperation(t, db, writer, p, writer)
	executeOperation(t, db, writer, p)
	if json.Unmarshal(db.Call(t, reader, "list_statements", ledger.StatementListQuery{}), &none) != nil || len(none.Statements) != 1 {
		t.Fatal("the approved import was recorded and read agents read it")
	}
	// A statement must belong to a cash or card account.
	q.RequestKey, q.AccountID = "sales", sales
	if err := callError(db, writer, "import_statement", q); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("a statement was recorded on an income account", err)
	}
	// The statement must cite its document.
	q.RequestKey, q.AccountID, q.DocumentID = "no-doc", checking, ledger.ID()
	if err := callError(db, writer, "import_statement", q); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("a statement without its document was recorded", err)
	}
}
