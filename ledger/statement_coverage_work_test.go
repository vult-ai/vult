package ledger_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestStatementCoverageSharesMatchEligibility(t *testing.T) {
	db := testutil.New(t)
	_, scope, accounts := chartBook(t, db, "business-v1")
	cash, fees := accounts["1110"].ID, accounts["6600"].ID
	const count = 24
	post := ledger.PostJournal{RequestKey: "fees", EconomicDate: "2025-03-10", AccountingDate: "2025-03-10", Description: "Example fees",
		Lines: []ledger.PostLine{{AccountID: fees, Side: "debit", AmountMinor: "2400"}}}
	observations := make([]ledger.StatementRowInput, 0, count)
	for i := 0; i < count; i++ {
		post.Lines = append(post.Lines, ledger.PostLine{AccountID: cash, Side: "credit", AmountMinor: "100"})
		observations = append(observations, row(i+1, "2025-03-10", "-100", "Example fee"))
	}
	var entry ledger.Entry
	if json.Unmarshal(db.Call(t, scope, "post_journal", post), &entry) != nil {
		t.Fatal("invalid journal entry")
	}
	statement := importStatement(t, db, scope, ledger.ImportStatement{RequestKey: "statement", AccountID: cash,
		DocumentID: statementDocument(t, db, scope, "fees"), PeriodStart: "2025-03-01", PeriodEnd: "2025-03-31",
		OpeningMinor: "0", ClosingMinor: "-2400", Rows: observations})
	match := ledger.MatchStatement{RequestKey: "combined"}
	for _, observation := range reconciliation(t, db, scope, statement.ID).Rows {
		match.Rows = append(match.Rows, ledger.StatementMatchPart{RowID: observation.ID})
	}
	for _, line := range entry.Lines {
		if line.AccountID == cash {
			match.Lines = append(match.Lines, ledger.StatementMatchPart{LineID: line.ID})
		}
	}
	var matched ledger.StatementMatchRead
	if json.Unmarshal(db.Call(t, scope, "match_statement", match), &matched) != nil {
		t.Fatal("invalid match")
	}
	db.Call(t, scope, "review_statement", ledger.ReviewStatement{RequestKey: "review", StatementID: statement.ID, Outcome: "reconciled"})
	var reviewed time.Time
	succeeded(t, db.Admin.QueryRow(db.Context, "SELECT recorded_at FROM public.statement_reviews WHERE tenant_id=$1 AND book_id=$2 AND statement_id=$3", scope.TenantID, scope.BookID, statement.ID).Scan(&reviewed))
	var body string
	succeeded(t, db.Admin.QueryRow(db.Context, "SELECT prosrc FROM pg_proc WHERE oid='public.statement_row_coverages(uuid,uuid,uuid,timestamptz)'::regprocedure").Scan(&body))
	parameters := map[string]string{"t": "$1::uuid", "b": "$2::uuid", "sid": "$3::uuid", "at_time": "$4::timestamptz"}
	query := regexp.MustCompile(`\b(t|b|sid|at_time)\b`).ReplaceAllStringFunc(functionQuery(body), func(name string) string { return parameters[name] })
	succeeded(t, write(db, db.Runtime, scope, func(tx pgx.Tx) error {
		var data []byte
		if err := tx.QueryRow(db.Context, "EXPLAIN (ANALYZE, FORMAT JSON) "+query, scope.TenantID, scope.BookID, statement.ID, reviewed).Scan(&data); err != nil {
			return err
		}
		var plan any
		if err := json.Unmarshal(data, &plan); err != nil {
			return err
		}
		shared := map[string]int{}
		var visit func(any)
		visit = func(value any) {
			switch node := value.(type) {
			case map[string]any:
				if name, _ := node["Subplan Name"].(string); name == "CTE eligible_entries" || name == "CTE eligible_matches" {
					shared[name]++
					if node["Actual Loops"] != float64(1) || node["Actual Rows"] != float64(1) {
						t.Error("the shared entry and match eligibility must each be computed once", name, node["Actual Loops"], node["Actual Rows"])
					}
				}
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
		if shared["CTE eligible_entries"] != 1 || shared["CTE eligible_matches"] != 1 {
			t.Fatal("the coverage query must share eligibility across every observed row", shared)
		}
		return nil
	}))
	coverage := func(at any, want string) {
		t.Helper()
		succeeded(t, write(db, db.Runtime, scope, func(tx pgx.Tx) error {
			var rows int
			var amounts string
			if err := tx.QueryRow(db.Context, "SELECT count(*),string_agg(covered_minor::text,',' ORDER BY row_id) FROM public.statement_row_coverages($1,$2,$3,$4)", scope.TenantID, scope.BookID, statement.ID, at).Scan(&rows, &amounts); err != nil {
				return err
			}
			if rows != count || amounts != strings.TrimSuffix(strings.Repeat(want+",", count), ",") {
				t.Fatal("every observation keeps its exact coverage at the requested time", rows, amounts)
			}
			return nil
		}))
	}
	coverage(reviewed, "-100")
	db.Call(t, scope, "revoke_statement_match", ledger.RevokeStatementMatch{RequestKey: "revoke", MatchID: matched.ID, Reason: "Example correction"})
	db.Call(t, scope, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: entry.ID, AccountingDate: "2025-03-11", Reason: "Example correction"})
	coverage("infinity", "0")
	coverage(reviewed, "-100")
}

// functionQuery returns the query a set-returning function runs, so a test
// can plan it with the function's arguments bound as parameters.
func functionQuery(body string) string {
	if i := strings.Index(body, "RETURN QUERY"); i >= 0 {
		body = body[i+len("RETURN QUERY"):]
		body = body[:strings.LastIndex(body, "END")]
		body = strings.TrimSuffix(strings.TrimSpace(body), ";")
	}
	return body
}
