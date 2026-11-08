package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/reporting"
)

type FinancialReportRequest = reporting.Request

type FinancialReport struct {
	RecordedState
	reporting.Result
	ExportCSV       string `json:"export_csv,omitempty"`
	ExportAvailable bool   `json:"export_available"`
}

type FinancialContributionRead struct {
	RecordedState
	reporting.ContributionResult
}

type FinancialContributionRequest struct {
	Request         FinancialReportRequest
	Digest          string
	RowID           string
	ColumnID        string
	KnowledgeCutoff string
	GeneratedAt     string
}

type FinancialReportMonths struct {
	RecordedState
	SchemaVersion int               `json:"schema_version"`
	Base          FinancialReport   `json:"base"`
	Months        []FinancialReport `json:"months"`
}

const reportAccountLimit = 10000
const reportPostingLimit = 100000

func reportError(err error) error {
	var problem *reporting.Error
	if !errors.As(err, &problem) {
		return dbError(err)
	}
	switch problem.Code {
	case "stale_report_population", "contribution_sum_mismatch":
		return ErrConflict
	case "report_export_too_large", "calendar_bucket_limit":
		return ErrLimit
	default:
		return ErrInvalid
	}
}

// FinancialReport captures one authorized snapshot. Recorded Book amounts are
// never replaced by native balances, inferred exchange rates or new valuations.
func (a TenantAdmin) FinancialReport(ctx context.Context, req FinancialReportRequest, includeCSV bool) (FinancialReport, error) {
	var out FinancialReport
	err := a.withBookRead(ctx, req.BookID, func(tx pgx.Tx, scope Scope, book Book) error {
		in, state, err := reportInput(ctx, tx, scope, book, req, "", "", false)
		if err != nil {
			return err
		}
		out, err = buildFinancialReport(in, req, includeCSV, state.RecordedState)
		return err
	})
	if err != nil {
		return FinancialReport{}, err
	}
	return out, nil
}

func buildFinancialReport(in reporting.Input, req FinancialReportRequest, includeCSV bool, state RecordedState) (FinancialReport, error) {
	in, err := scopeReportInput(in, req)
	if err != nil {
		return FinancialReport{}, err
	}
	result, err := reporting.Build(in, req)
	if err != nil {
		return FinancialReport{}, reportError(err)
	}
	out := FinancialReport{RecordedState: state, Result: result, ExportAvailable: true}
	if includeCSV {
		data, err := reporting.CSV(result)
		if err != nil {
			return FinancialReport{}, reportError(err)
		}
		var metadata bytes.Buffer
		writer := csv.NewWriter(&metadata)
		if err := writer.WriteAll([][]string{{"State revision", state.StateRevision}, {"State captured at", state.StateCapturedAt}, {"State knowledge cutoff", state.StateKnowledgeCutoff}}); err != nil {
			return FinancialReport{}, err
		}
		if metadata.Len()+len(data) > reporting.MaxCSVBytes {
			return FinancialReport{}, ErrLimit
		}
		out.ExportCSV = metadata.String() + string(data)
	}
	return out, nil
}

// FinancialContributions reconstructs the requested snapshot and refuses a
// changed population. A digest identifies a result; it grants no authority.
func (a TenantAdmin) FinancialContributions(ctx context.Context, req FinancialContributionRequest) (FinancialContributionRead, error) {
	if len(req.Digest) != 64 || req.RowID == "" || len(req.RowID) > 256 || req.ColumnID == "" || len(req.ColumnID) > 32 || req.KnowledgeCutoff == "" || req.GeneratedAt == "" {
		return FinancialContributionRead{}, ErrInvalid
	}
	if _, err := hex.DecodeString(req.Digest); err != nil {
		return FinancialContributionRead{}, ErrInvalid
	}
	var out FinancialContributionRead
	err := a.withBookRead(ctx, req.Request.BookID, func(tx pgx.Tx, scope Scope, book Book) error {
		in, state, err := reportInput(ctx, tx, scope, book, req.Request, req.KnowledgeCutoff, req.GeneratedAt, false)
		if err != nil {
			return err
		}
		in, err = scopeReportInput(in, req.Request)
		if err != nil {
			return err
		}
		result, err := reporting.Build(in, req.Request)
		if err != nil {
			return reportError(err)
		}
		if result.Digest != req.Digest {
			return ErrConflict
		}
		out.RecordedState = state.RecordedState
		out.ContributionResult, err = reporting.Contributions(in, result, req.RowID, req.ColumnID)
		return reportError(err)
	})
	if err != nil {
		return FinancialContributionRead{}, err
	}
	return out, nil
}

// FinancialReportMonthSet reads the catalog and journal once. The calculation
// engine selects each window from that same population, including comparisons.
func (a TenantAdmin) FinancialReportMonthSet(ctx context.Context, req FinancialReportRequest, from, to string) (FinancialReportMonths, error) {
	if req.Report != reporting.BalanceSheet && req.Report != reporting.ProfitLoss {
		return FinancialReportMonths{}, ErrInvalid
	}
	periods, err := reporting.MonthPeriods(from, to)
	if err != nil {
		return FinancialReportMonths{}, reportError(err)
	}
	var out FinancialReportMonths
	err = a.withBookRead(ctx, req.BookID, func(tx pgx.Tx, scope Scope, book Book) error {
		in, state, err := reportInput(ctx, tx, scope, book, req, "", "", true)
		if err != nil {
			return err
		}
		baseInput, err := scopeReportInput(in, req)
		if err != nil {
			return err
		}
		base, err := reporting.Build(baseInput, req)
		if err != nil {
			return reportError(err)
		}
		requests, err := reporting.MonthRequests(base, periods)
		if err != nil {
			return reportError(err)
		}
		// MonthRequests gives every month the same latest-through population.
		// A base Profit & Loss can also include a disjoint comparison window.
		monthInput := baseInput
		if req.Report == reporting.ProfitLoss && base.Scope.Comparison != nil &&
			(base.Scope.Comparison.From < base.Scope.Period.From || base.Scope.Comparison.To > base.Scope.Period.To) {
			monthInput, err = scopeReportInput(in, requests[0])
			if err != nil {
				return err
			}
		}
		months, err := reporting.BuildMonthComparisons(ctx, monthInput, requests)
		if err != nil {
			return reportError(err)
		}
		wrap := func(result reporting.Result) FinancialReport {
			return FinancialReport{RecordedState: state.RecordedState, Result: result, ExportAvailable: true}
		}
		out = FinancialReportMonths{RecordedState: state.RecordedState, SchemaVersion: 1, Base: wrap(base), Months: make([]FinancialReport, 0, len(months))}
		for _, month := range months {
			out.Months = append(out.Months, wrap(month))
		}
		return nil
	})
	if err != nil {
		return FinancialReportMonths{}, err
	}
	return out, nil
}

func reportInput(ctx context.Context, tx pgx.Tx, scope Scope, book Book, req FinancialReportRequest, cutoffText, generatedText string, months bool) (reporting.Input, readState, error) {
	state, err := captureReadState(ctx, tx, scope, book, recordedReadScope{Cutoff: cutoffText})
	if err != nil {
		return reporting.Input{}, state, err
	}
	in, err := reportInputAt(ctx, tx, scope, book, req, state, generatedText, months)
	return in, state, err
}

func reportInputAt(ctx context.Context, tx pgx.Tx, scope Scope, book Book, req FinancialReportRequest, state readState, generatedText string, months bool) (reporting.Input, error) {
	in := reporting.Input{Book: reporting.Book{ID: book.ID, Name: book.Name, AccountingTimezone: book.AccountingTimezone, Currency: book.Currency, Scale: book.Scale, StartDate: book.StartDate, RecognitionBasis: "unknown", ReportingFramework: "unknown"}, Accounts: []reporting.Account{}, Postings: []reporting.Posting{}}
	if book.FiscalYearStartMonth != nil && book.FiscalYearStartDay != nil {
		in.Book.FiscalStartMonth, in.Book.FiscalStartDay = *book.FiscalYearStartMonth, *book.FiscalYearStartDay
	}
	cutoff := state.cutoff
	if generatedText == "" {
		generatedText = state.StateKnowledgeCutoff
	} else if value, err := time.Parse(time.RFC3339Nano, generatedText); err != nil || value.After(state.captured) {
		return in, ErrInvalid
	}
	in.Provenance = reporting.Provenance{KnowledgeCutoff: cutoff.UTC().Format(time.RFC3339Nano), GeneratedAt: generatedText, MappingVersion: "recorded-postings-v1", HierarchyBasis: "flat_recorded_accounts", Coverage: "unknown", OpeningBalances: "unknown", Limitations: []string{
		"Source coverage and opening balances have not been established. A Book start date does not establish a zero opening position.",
		"No account hierarchy is inferred from names, codes or display labels.",
		"Reporting treatment is unspecified. Recorded effects remain included; an economic-purpose annotation cannot exclude them.",
	}}
	accounts, err := readScopedCatalog(ctx, tx, scope)
	if err != nil {
		return in, err
	}
	for _, account := range accounts {
		// A stored subtype refines its matching class only. A contradictory
		// role never overrides the account's authoritative accounting class.
		kind := account.Class
		if account.Class == "income" && account.Role == "other_income" || account.Class == "expense" && (account.Role == "other_expense" || account.Role == "cost_of_goods_sold") {
			kind = account.Role
		}
		in.Accounts = append(in.Accounts, reporting.Account{ID: account.ID, Code: account.Code, GLCode: account.Code, Name: account.Name, Kind: kind, Role: account.Role, Revision: account.Revision})
	}
	period, comparison, err := reporting.PeriodFor(in, req)
	if err != nil {
		return in, reportError(err)
	}
	through := period.To
	if comparison != nil && comparison.To > through {
		through = comparison.To
	}
	var from, to, compareFrom, compareTo *string
	if req.Report == reporting.ProfitLoss || req.Report == reporting.Journal {
		// Activity reports need their displayed windows, not every earlier
		// position. Filter entries before joining their complete posting sets.
		from, to = &period.From, &period.To
		if months {
			// Monthly comparisons anchor their current column at the latest
			// requested end. Capture that whole window, including any gap.
			to = &through
		}
		if comparison != nil {
			compareFrom, compareTo = &comparison.From, &comparison.To
		}
	}
	// The SQL read returns one extra row on overflow. For an accepted read,
	// retain the original posting order used by its population revision.
	rows, err := tx.Query(ctx, `SELECT p.line_id::text,p.entry_id::text,p.account_id::text,p.accounting_date::text,
 p.side,p.amount_minor::text,p.native_amount_minor::text
 FROM access.report_postings($1,$2,$3::date,$4,$5::date,$6::date,$7::date,$8::date) p
 ORDER BY p.accounting_date,p.entry_sequence,p.line_position`,
		scope.TenantID, scope.BookID, through, cutoff, from, to, compareFrom, compareTo)
	if err != nil {
		return in, dbError(err)
	}
	for rows.Next() {
		p := reporting.Posting{BookID: book.ID, Currency: book.Currency, Scale: book.Scale, Treatment: "unknown"}
		if err := rows.Scan(&p.ID, &p.EntryID, &p.AccountID, &p.Date, &p.Side, &p.Amount, &p.NativeAmount); err != nil {
			rows.Close()
			return in, dbError(err)
		}
		in.Postings = append(in.Postings, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return in, dbError(err)
	}
	if len(in.Postings) > reportPostingLimit {
		return in, ErrLimit
	}
	return in, nil
}

// scopeReportInput makes a result's population independent of other results
// captured in the same transaction. Its standalone inspection can reproduce it.
func scopeReportInput(in reporting.Input, req FinancialReportRequest) (reporting.Input, error) {
	period, comparison, err := reporting.PeriodFor(in, req)
	if err != nil {
		return in, reportError(err)
	}
	through := period.To
	if comparison != nil && comparison.To > through {
		through = comparison.To
	}
	selected := make([]reporting.Posting, 0, len(in.Postings))
	for _, p := range in.Postings {
		included := p.Date <= through
		if req.Report == reporting.ProfitLoss || req.Report == reporting.Journal {
			included = p.Date >= period.From && p.Date <= period.To || comparison != nil && p.Date >= comparison.From && p.Date <= comparison.To
		}
		if included {
			selected = append(selected, p)
		}
	}
	in.Postings = selected
	data, err := json.Marshal(struct {
		Book     reporting.Book
		Accounts []reporting.Account
		Postings []reporting.Posting
	}{in.Book, in.Accounts, in.Postings})
	if err != nil {
		return in, err
	}
	digest := sha256.Sum256(data)
	in.Provenance.LedgerRevision = hex.EncodeToString(digest[:])
	return in, nil
}
