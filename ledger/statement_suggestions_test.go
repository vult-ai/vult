package ledger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type duplicateSuggestionQuery struct{}
type statementSuggestionTrace struct {
	reports       atomic.Int64
	duplicateRows atomic.Int64
	currentChecks atomic.Int64
	query         chan pgx.TraceQueryStartData
}

func (trace *statementSuggestionTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if strings.Contains(q.SQL, "r.row_number,r.location,r.occurred_on") {
		trace.reports.Add(1)
	}
	if strings.Contains(q.SQL, "public.statement_current_reconciled") {
		trace.currentChecks.Add(1)
	}
	duplicates := strings.Contains(q.SQL, "c.raw_sha256=ANY(")
	if duplicates {
		select {
		case trace.query <- pgx.TraceQueryStartData{SQL: q.SQL, Args: append([]any(nil), q.Args...)}:
		default:
		}
	}
	return context.WithValue(ctx, duplicateSuggestionQuery{}, duplicates)
}

func TestStatementDetailReusesCoverageForCurrentReviewStatus(t *testing.T) {
	db := testutil.New(t)
	book, scope, accounts := chartBook(t, db, "business-v1")
	cash, fees := accounts["1110"].ID, accounts["6600"].ID
	entry := journal(t, db, scope, "fee", "2026-03-10", fees, cash, "500")
	statement := importStatement(t, db, scope, ledger.ImportStatement{RequestKey: "statement", AccountID: cash,
		DocumentID: statementDocument(t, db, scope, "fee"), PeriodStart: "2026-03-01", PeriodEnd: "2026-03-31",
		OpeningMinor: "0", ClosingMinor: "-500", Rows: []ledger.StatementRowInput{row(1, "2026-03-10", "-500", "Example fee")}})
	observed := reconciliation(t, db, scope, statement.ID).Rows[0]
	var match ledger.StatementMatchRead
	succeeded(t, json.Unmarshal(db.Call(t, scope, "match_statement", ledger.MatchStatement{RequestKey: "match",
		Rows: []ledger.StatementMatchPart{{RowID: observed.ID}}, Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, scope, entry, cash)}}}), &match))
	db.Call(t, scope, "review_statement", ledger.ReviewStatement{RequestKey: "review", StatementID: statement.ID, Outcome: "reconciled"})
	trace := &statementSuggestionTrace{query: make(chan pgx.TraceQueryStartData, 1)}
	config := db.Admin.Config()
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(db.Context, config)
	succeeded(t, err)
	defer pool.Close()
	owner, err := (ledger.Admin{DB: pool}).ForTenant(db.Context, book.TenantID)
	succeeded(t, err)
	var signedHash string
	for _, revoked := range []bool{false, true} {
		if revoked {
			db.Call(t, scope, "revoke_statement_match", ledger.RevokeStatementMatch{RequestKey: "revoke", MatchID: match.ID, Reason: "Example correction"})
		}
		report, _, err := owner.StatementReconciliation(db.Context, book.ID, statement.ID)
		succeeded(t, err)
		var explained bool
		succeeded(t, write(db, db.Runtime, scope, func(tx pgx.Tx) error {
			return tx.QueryRow(db.Context, "SELECT public.statement_explained($1,$2,$3,'infinity'::timestamptz)", scope.TenantID, scope.BookID, statement.ID).Scan(&explained)
		}))
		wantStatus := "reconciled"
		if revoked {
			wantStatus = "needs_work"
		}
		if report.Summary.Explained != explained || explained == revoked || report.Statement.Status != wantStatus || report.Statement.Review == nil || report.Statement.Review.Outcome != "reconciled" {
			t.Fatal("current status must follow the same coverage as the report and database while preserving the signed review", revoked, report)
		}
		if !revoked {
			signedHash = report.Statement.Review.ReportSHA256
		} else if report.Statement.Review.ReportSHA256 != signedHash {
			t.Fatal("a lost match must not rewrite the review that signed it", report.Statement.Review)
		}
	}
	if trace.reports.Load() != 2 || trace.currentChecks.Load() != 0 {
		t.Fatal("each detail read must calculate coverage once and reuse it for current status", trace.reports.Load(), trace.currentChecks.Load())
	}
}

func (trace *statementSuggestionTrace) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryEndData) {
	if selected, _ := ctx.Value(duplicateSuggestionQuery{}).(bool); selected {
		trace.duplicateRows.Add(q.CommandTag.RowsAffected())
	}
}

func TestOwnerReconciliationReusesItsReportAndBoundsDuplicateChoices(t *testing.T) {
	db := testutil.New(t)
	book, s, accounts := chartBook(t, db, "business-v1")
	const count, copies = 32, 5
	observed := []ledger.StatementRowInput{}
	for i := 0; i < count; i++ {
		observed = append(observed, row(i+1, "2026-03-05", "-500", fmt.Sprintf("Example fee %d", i)))
	}
	var earliest []ledger.StatementRow
	var latest ledger.Statement
	for i := 0; i < copies; i++ {
		key := fmt.Sprintf("overlapping-%d", i)
		latest = importStatement(t, db, s, ledger.ImportStatement{RequestKey: key, AccountID: accounts["1110"].ID,
			DocumentID: statementDocument(t, db, s, key), PeriodStart: "2026-03-01", PeriodEnd: "2026-03-31", OpeningMinor: "0", ClosingMinor: fmt.Sprint(-500 * count), Rows: observed})
		if i == 0 {
			earliest = reconciliation(t, db, s, latest.ID).Rows
		}
	}
	succeeded(t, func() error {
		_, err := db.Admin.Exec(db.Context, "ANALYZE public.statements,public.statement_rows,public.statement_duplicates")
		return err
	}())
	trace := &statementSuggestionTrace{query: make(chan pgx.TraceQueryStartData, 1)}
	config := db.Admin.Config()
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(db.Context, config)
	succeeded(t, err)
	defer pool.Close()
	owner, err := (ledger.Admin{DB: pool}).ForTenant(db.Context, book.TenantID)
	succeeded(t, err)
	report, choices, err := owner.StatementReconciliation(db.Context, book.ID, latest.ID)
	succeeded(t, err)
	if trace.reports.Load() != 1 {
		t.Fatal("the report returned to the owner must also supply its match suggestions", trace.reports.Load())
	}
	// Each earlier observation is read at most once, never once per row it
	// might explain.
	if trace.duplicateRows.Load() > (copies-1)*count || len(choices) != count {
		t.Fatal("historical copies must not multiply duplicate choices transferred from the database", trace.duplicateRows.Load(), choices)
	}
	for i, choice := range choices {
		if choice.Kind != "duplicate" || choice.RowID != report.Rows[i].ID || choice.CanonicalID != earliest[i].ID {
			t.Fatal("the earliest eligible observation must be chosen deterministically", i, choice)
		}
	}
	if standalone := suggestions(t, db, s, latest.ID); !reflect.DeepEqual(standalone, choices) {
		t.Fatal("standalone suggestions must use the same report and canonical ordering", standalone, choices)
	}
	var query pgx.TraceQueryStartData
	select {
	case query = <-trace.query:
	default:
		t.Fatal("the duplicate choices query was not captured")
	}
	succeeded(t, write(db, db.Admin, s, func(tx pgx.Tx) error {
		var data []byte
		if err := tx.QueryRow(db.Context, "EXPLAIN (ANALYZE, FORMAT JSON) "+query.SQL, query.Args...).Scan(&data); err != nil {
			return err
		}
		var plan any
		if err := json.Unmarshal(data, &plan); err != nil {
			return err
		}
		work := float64(0)
		var visit func(any)
		visit = func(value any) {
			switch node := value.(type) {
			case map[string]any:
				number := func(key string) float64 { n, _ := node[key].(float64); return n }
				if node["Relation Name"] == "statement_rows" {
					work += number("Actual Loops") * (number("Actual Rows") + number("Rows Removed by Filter") + number("Rows Removed by Index Recheck"))
				}
				work += number("Actual Loops") * number("Rows Removed by Join Filter")
				for _, child := range node {
					visit(child)
				}
			case []any:
				for _, child := range node {
					visit(child)
				}
			}
		}
		visit(plan)
		if work > 8*count*copies {
			t.Fatal("finding repeated rows must not rescan every historical observation for every target row", work)
		}
		return nil
	}))
}

func TestStatementSuggestionsKeepNearestDateAndLineOrder(t *testing.T) {
	db := testutil.New(t)
	_, s, accounts := chartBook(t, db, "business-v1")
	cash, fees := accounts["1110"].ID, accounts["6600"].ID
	entries := []ledger.Entry{}
	for i, date := range []string{"2026-03-08", "2026-03-10", "2026-03-10", "2026-03-12", "2026-03-18", "2026-03-19"} {
		entries = append(entries, journal(t, db, s, fmt.Sprintf("fee-%d", i), date, fees, cash, "500"))
	}
	observed := []ledger.StatementRowInput{row(1, "2026-03-10", "-500", "Example first fee")}
	for i := 2; i <= 6; i++ {
		observed = append(observed, row(i, "2026-03-11", "-500", "Example later fee"))
	}
	st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "fees", AccountID: cash, DocumentID: statementDocument(t, db, s, "fees"),
		PeriodStart: "2026-03-01", PeriodEnd: "2026-03-31", OpeningMinor: "0", ClosingMinor: "-3000", Rows: observed})
	choices := suggestions(t, db, s, st.ID)
	expected := []int{1, 2, 3, 0, 4}
	reasons := []string{"Same amount and date", "Same amount, a day apart", "Same amount, a day apart", "Same amount, 3 days apart", "Same amount, 7 days apart"}
	if len(choices) != len(expected) {
		t.Fatal("each line is suggested once and dates beyond seven days are excluded", choices)
	}
	for i, index := range expected {
		if choices[i].Kind != "match" || len(choices[i].EntryIDs) != 1 || choices[i].EntryIDs[0] != entries[index].ID || choices[i].Reason != reasons[i] {
			t.Fatal("nearest dates win, with original line order breaking ties", i, choices[i], entries[index].ID)
		}
	}
}

func TestStatementDuplicateSuggestionsPreferAnExactExternalID(t *testing.T) {
	db := testutil.New(t)
	_, s, accounts := chartBook(t, db, "business-v1")
	importRow := func(key string, observed ledger.StatementRowInput) ledger.StatementReconciliation {
		st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: key, AccountID: accounts["1110"].ID,
			DocumentID: statementDocument(t, db, s, key), PeriodStart: "2026-03-01", PeriodEnd: "2026-03-31", OpeningMinor: "0", ClosingMinor: "-500", Rows: []ledger.StatementRowInput{observed}})
		return reconciliation(t, db, s, st.ID)
	}
	observed := row(1, "2026-03-05", "-500", "Example bank fee")
	importRow("without-external-id", observed)
	observed.ExternalID = "example-fee-001"
	exact := importRow("with-external-id", observed)
	latest := importRow("current-observation", observed)
	choices := suggestions(t, db, s, latest.Statement.ID)
	if len(choices) != 1 || choices[0].CanonicalID != exact.Rows[0].ID || choices[0].Reason != "Same external ID on an earlier statement" {
		t.Fatal("the exact external ID must take precedence over an older raw row without an ID", choices)
	}
}

func TestStatementDuplicateSuggestionsExcludeUnavailableObservations(t *testing.T) {
	db := testutil.New(t)
	_, s, accounts := chartBook(t, db, "business-v1")
	importRows := func(key string, rows ...ledger.StatementRowInput) ledger.StatementReconciliation {
		closing := 0
		for _, r := range rows {
			if r.Lifecycle != "pending" && r.Lifecycle != "cancelled" {
				closing -= 500
			}
		}
		st := importStatement(t, db, s, ledger.ImportStatement{RequestKey: key, AccountID: accounts["1110"].ID, DocumentID: statementDocument(t, db, s, key),
			PeriodStart: "2026-03-01", PeriodEnd: "2026-03-31", OpeningMinor: "0", ClosingMinor: fmt.Sprint(closing), Rows: rows})
		return reconciliation(t, db, s, st.ID)
	}
	for _, lifecycle := range []string{"pending", "cancelled"} {
		for _, source := range []bool{true, false} {
			key := fmt.Sprintf("%s-source-%t", lifecycle, source)
			canonical, target := row(1, "2026-03-05", "-500", key), row(1, "2026-03-05", "-500", key)
			if source {
				canonical.Lifecycle = lifecycle
			} else {
				target.Lifecycle = lifecycle
			}
			importRows(key+"-old", canonical)
			st := importRows(key+"-new", target)
			if choices := suggestions(t, db, s, st.Statement.ID); len(choices) != 0 {
				t.Fatal("pending and cancelled observations cannot explain another statement", key, choices)
			}
		}
	}
	canonical := importRows("canonical", row(1, "2026-03-10", "-500", "Example repeated fee"))
	target := importRows("two-fees", row(1, "2026-03-10", "-500", "Example repeated fee"), row(2, "2026-03-10", "-500", "Example repeated fee"))
	if choices := suggestions(t, db, s, target.Statement.ID); len(choices) != 1 || choices[0].RowID != target.Rows[0].ID || choices[0].CanonicalID != canonical.Rows[0].ID {
		t.Fatal("one suggestion batch cannot offer the same canonical money twice", choices)
	}
	db.Call(t, s, "mark_statement_duplicate", ledger.MarkStatementDuplicate{RequestKey: "one-observation", RowID: target.Rows[0].ID, CanonicalRowID: canonical.Rows[0].ID, Reason: "An overlapping observation"})
	if choices := suggestions(t, db, s, target.Statement.ID); len(choices) != 0 {
		t.Fatal("a canonical observation used on this statement cannot explain a second fee", choices)
	}
	old := importRows("old-observation", row(1, "2026-03-15", "-500", "Example canonical fee"))
	middle := importRows("middle-observation", row(1, "2026-03-15", "-500", "Example canonical fee"))
	latest := importRows("latest-observation", row(1, "2026-03-15", "-500", "Example canonical fee"))
	db.Call(t, s, "mark_statement_duplicate", ledger.MarkStatementDuplicate{RequestKey: "canonical-reference", RowID: latest.Rows[0].ID, CanonicalRowID: middle.Rows[0].ID, Reason: "An overlapping observation"})
	if choices := suggestions(t, db, s, middle.Statement.ID); len(choices) != 0 {
		t.Fatal("an observation already used as canonical cannot become a duplicate", old.Statement.ID, choices)
	}
	for _, operation := range []string{"partial-match", "full-match", "adjustment"} {
		observed := row(1, "2026-03-20", "-500", operation)
		importRows(operation+"-canonical", observed)
		current := importRows(operation+"-current", observed)
		amount := "500"
		if operation == "partial-match" {
			amount = "100"
		}
		entry := journal(t, db, s, operation+"-entry", "2026-03-20", accounts["6600"].ID, accounts["1110"].ID, amount)
		if operation == "adjustment" {
			db.Call(t, s, "record_statement_adjustment", ledger.RecordStatementAdjustment{RequestKey: operation, RowID: current.Rows[0].ID, EntryID: entry.ID})
		} else {
			db.Call(t, s, "match_statement", ledger.MatchStatement{RequestKey: operation,
				Rows:  []ledger.StatementMatchPart{{RowID: current.Rows[0].ID, AmountMinor: "-" + amount}},
				Lines: []ledger.StatementMatchPart{{LineID: entryLine(t, db, s, entry, accounts["1110"].ID)}}})
		}
		if choices := suggestions(t, db, s, current.Statement.ID); len(choices) != 0 {
			t.Fatal("a row already explained in the Book cannot become a duplicate", operation, choices)
		}
	}
}

func TestStatementDuplicateSuggestionsKeepCurrencyAndPrecision(t *testing.T) {
	for _, currency := range []string{"USD", "EUR"} {
		t.Run(currency, func(t *testing.T) {
			db := testutil.New(t)
			book, err := db.Owner.CreateBook(db.Context, "Example three-decimal Book", "USD", 3)
			succeeded(t, err)
			connection, err := db.Owner.CreateConnection(db.Context, book.ID, "Example connection", "write")
			succeeded(t, err)
			s := ledger.Scope{TenantID: connection.TenantID, BookID: connection.BookID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "write"}
			cash := roleAccount(t, db, s, "1000", "asset", "cash")
			observed := row(1, "2026-03-05", "-500", "Example fee")
			importStatement(t, db, s, ledger.ImportStatement{RequestKey: "old-unit", AccountID: cash.ID, DocumentID: statementDocument(t, db, s, "old-unit"),
				PeriodStart: "2026-03-01", PeriodEnd: "2026-03-31", OpeningMinor: "0", ClosingMinor: "-500", Rows: []ledger.StatementRowInput{observed}})
			scale := 2
			db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "new-unit", AccountID: cash.ID, ExpectedRevision: cash.Revision, Code: cash.Code, Name: cash.Name, Currency: &currency, Scale: &scale})
			current := importStatement(t, db, s, ledger.ImportStatement{RequestKey: "current-unit", AccountID: cash.ID, DocumentID: statementDocument(t, db, s, "current-unit"),
				PeriodStart: "2026-03-01", PeriodEnd: "2026-03-31", OpeningMinor: "0", ClosingMinor: "-500", Rows: []ledger.StatementRowInput{observed}})
			if choices := suggestions(t, db, s, current.ID); len(choices) != 0 {
				t.Fatal("equal raw numbers in different units must not be offered as duplicates", currency, choices)
			}
		})
	}
}
