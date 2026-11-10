package ledger_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestStatementAllocationSkipsWholeMatchWithoutCutoff(t *testing.T) {
	db := testutil.New(t)
	_, scope, accounts := chartBook(t, db, "business-v1")
	cash, fees := accounts["1110"].ID, accounts["6600"].ID
	const count = 128
	entries := make([]ledger.Entry, 0, count)
	observations := make([]ledger.StatementRowInput, 0, count)
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("fee-%d", i)
		entries = append(entries, journal(t, db, scope, key, "2025-03-10", fees, cash, "100"))
		observations = append(observations, row(i+1, "2025-03-10", "-100", "Example "+key))
	}
	statement := importStatement(t, db, scope, ledger.ImportStatement{RequestKey: "combined", AccountID: cash,
		DocumentID: statementDocument(t, db, scope, "combined-fees"), PeriodStart: "2025-03-01", PeriodEnd: "2025-03-31",
		OpeningMinor: "0", ClosingMinor: "-12800", Rows: observations})
	match := ledger.MatchStatement{RequestKey: "all-fees"}
	for i, observation := range reconciliation(t, db, scope, statement.ID).Rows {
		match.Rows = append(match.Rows, ledger.StatementMatchPart{RowID: observation.ID})
		match.Lines = append(match.Lines, ledger.StatementMatchPart{LineID: entryLine(t, db, scope, entries[i], cash)})
	}
	db.Call(t, scope, "match_statement", match)
	line := match.Lines[0].LineID
	lineIDs := make([]string, 0, count)
	for _, part := range match.Lines {
		lineIDs = append(lineIDs, part.LineID)
	}
	var body string
	var parameters []string
	succeeded(t, db.Admin.QueryRow(db.Context, `SELECT prosrc,proargnames[1:pronargs] FROM pg_proc
 WHERE oid='public.statement_line_allocations(uuid,uuid,uuid[],text,integer,date,uuid[])'::regprocedure`).Scan(&body, &parameters))
	// Explain the deployed function's query, rather than a second implementation.
	// Its arguments are unqualified identifiers; table columns use aliases.
	types := []string{"uuid", "uuid", "uuid[]", "text", "integer", "date", "uuid[]"}
	if len(parameters) != len(types) {
		t.Fatal("unexpected allocation function arguments", parameters)
	}
	positions := make(map[string]string, len(parameters))
	for i, parameter := range parameters {
		positions[parameter] = "$" + strconv.Itoa(i+1) + "::" + types[i]
	}
	identifiers := regexp.MustCompile(`\b(` + strings.Join(parameters, "|") + `)\b`)
	query := identifiers.ReplaceAllStringFunc(functionQuery(body), func(name string) string { return positions[name] })
	for _, tc := range []struct {
		name   string
		cutoff any
	}{
		{"current capacity", nil},
		{"statement cutoff", "2025-03-31"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			succeeded(t, write(db, db.Runtime, scope, func(tx pgx.Tx) error {
				args := []any{scope.TenantID, scope.BookID, lineIDs, "USD", 2, tc.cutoff, nil}
				var amount string
				if err := tx.QueryRow(db.Context, "SELECT public.statement_line_allocated($1,$2,$3,'USD',2,$4,NULL)::text", scope.TenantID, scope.BookID, line, tc.cutoff).Scan(&amount); err != nil {
					return err
				}
				if amount != "-100" {
					t.Fatal("a combined match consumes only this line's amount", amount)
				}
				var lines, covered int
				if err := tx.QueryRow(db.Context, "SELECT count(*),count(*) FILTER (WHERE allocated_minor=-100) FROM public.statement_line_allocations($1,$2,$3,$4,$5,$6,$7)", args...).Scan(&lines, &covered); err != nil {
					return err
				}
				if lines != count || covered != count {
					t.Fatal("bulk allocation must keep every selected line's amount", lines, covered)
				}
				var data []byte
				if err := tx.QueryRow(db.Context, "EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...).Scan(&data); err != nil {
					return err
				}
				var plan any
				if err := json.Unmarshal(data, &plan); err != nil {
					return err
				}
				var scans []map[string]any
				visits := statementObservationVisits(plan, &scans)
				t.Logf("%d-row combined match: %.0f statement observations visited", count, visits)
				limit := float64(count*3 + 2)
				if tc.cutoff == nil {
					limit = 2
				}
				if visits > limit {
					details, _ := json.Marshal(scans)
					if len(details) > 16000 {
						details = details[:16000]
					}
					t.Logf("statement observation scans: %s", details)
					var nodes []map[string]any
					allocationPlanNodes(plan, &nodes)
					tree, _ := json.Marshal(nodes)
					if len(tree) > 32000 {
						tree = tree[:32000]
					}
					t.Logf("allocation execution nodes: %s", tree)
					t.Error("allocation work must be shared across all selected lines", visits, limit)
				}
				return nil
			}))
		})
	}
	succeeded(t, write(db, db.Runtime, scope, func(tx pgx.Tx) error {
		var amount string
		if err := tx.QueryRow(db.Context, "SELECT public.statement_line_allocated($1,$2,$3,'USD',2,NULL,ARRAY[$4::uuid])::text", scope.TenantID, scope.BookID, line, statement.ID).Scan(&amount); err != nil {
			return err
		}
		if amount != "-100" {
			t.Fatal("reviewed coverage retains the combined allocation", amount)
		}
		return nil
	}))
}

func allocationPlanNodes(value any, nodes *[]map[string]any) {
	switch node := value.(type) {
	case map[string]any:
		if _, ok := node["Node Type"]; ok {
			detail := map[string]any{}
			for _, key := range []string{"Node Type", "Subplan Name", "CTE Name", "Relation Name", "Alias", "Index Cond", "Join Filter", "Hash Cond", "Actual Rows", "Actual Loops", "Rows Removed by Join Filter"} {
				if entry, ok := node[key]; ok {
					detail[key] = entry
				}
			}
			*nodes = append(*nodes, detail)
		}
		for _, child := range node {
			allocationPlanNodes(child, nodes)
		}
	case []any:
		for _, child := range node {
			allocationPlanNodes(child, nodes)
		}
	}
}

func statementObservationVisits(value any, scans *[]map[string]any) float64 {
	var visits float64
	switch node := value.(type) {
	case map[string]any:
		if node["Relation Name"] == "statement_rows" {
			rows, _ := node["Actual Rows"].(float64)
			filtered, _ := node["Rows Removed by Filter"].(float64)
			rechecked, _ := node["Rows Removed by Index Recheck"].(float64)
			loops, _ := node["Actual Loops"].(float64)
			visits += (rows + filtered + rechecked) * loops
			detail := map[string]any{}
			for _, key := range []string{"Node Type", "Index Name", "Index Cond", "Filter", "Actual Rows", "Actual Loops", "Rows Removed by Filter", "Rows Removed by Index Recheck"} {
				if value, ok := node[key]; ok {
					detail[key] = value
				}
			}
			*scans = append(*scans, detail)
		}
		for _, child := range node {
			visits += statementObservationVisits(child, scans)
		}
	case []any:
		for _, child := range node {
			visits += statementObservationVisits(child, scans)
		}
	}
	return visits
}
