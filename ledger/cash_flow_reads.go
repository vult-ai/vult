package ledger

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/reporting"
)

// CashFlowQuery reads the direct-method cash-flow statement for a period.
type CashFlowQuery struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// CashFlowRead is the statement with the period's notes. It classifies cash
// lines from account roles only.
type CashFlowRead struct {
	Statement   reporting.Result `json:"statement"`
	PeriodNotes []PeriodNote     `json:"period_notes"`
}

func readCashFlow(ctx context.Context, tx pgx.Tx, s Scope, q CashFlowQuery) (CashFlowRead, error) {
	header, err := readBook(ctx, tx, s)
	if err != nil {
		return CashFlowRead{}, dbError(err)
	}
	if q.To == "" {
		q.To = BookToday(header, time.Now())
	}
	if q.From == "" || !ValidDate(q.From) || !ValidDate(q.To) || q.From > q.To {
		return CashFlowRead{}, ErrInvalid
	}
	req := FinancialReportRequest{BookID: s.BookID, Report: reporting.CashFlows, From: q.From, To: q.To}
	in, err := connectionReportInput(ctx, tx, s, header, q.To)
	if err != nil {
		return CashFlowRead{}, err
	}
	in, err = scopeReportInput(in, req)
	if err != nil {
		return CashFlowRead{}, err
	}
	var out CashFlowRead
	if out.Statement, err = reporting.Build(in, req); err != nil {
		return CashFlowRead{}, reportError(err)
	}
	out.PeriodNotes, err = periodNotes(ctx, tx, s, q.From, q.To)
	return out, err
}

// connectionReportInput reads a report population through a connection's
// row policies: every recorded line through a date, with the Book's accounts.
// The owner's reads use the leased report path instead.
func connectionReportInput(ctx context.Context, tx pgx.Tx, s Scope, header Book, through string) (reporting.Input, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	in := reporting.Input{Book: reporting.Book{ID: header.ID, Name: header.Name, AccountingTimezone: header.AccountingTimezone, Currency: header.Currency, Scale: header.Scale, StartDate: header.StartDate, RecognitionBasis: "unknown", ReportingFramework: "unknown"}, Accounts: []reporting.Account{}, Postings: []reporting.Posting{}}
	if header.FiscalYearStartMonth != nil && header.FiscalYearStartDay != nil {
		in.Book.FiscalStartMonth, in.Book.FiscalStartDay = *header.FiscalYearStartMonth, *header.FiscalYearStartDay
	}
	in.Provenance = reporting.Provenance{KnowledgeCutoff: now, GeneratedAt: now, MappingVersion: "recorded-postings-v1", HierarchyBasis: "flat_recorded_accounts", Coverage: "unknown", OpeningBalances: "unknown"}
	accounts, err := readScopedCatalog(ctx, tx, s)
	if err != nil {
		return in, err
	}
	addReportAccounts(&in, accounts)
	rows, err := tx.Query(ctx, `SELECT l.id::text,e.id::text,l.account_id::text,e.accounting_date::text,l.side,l.amount_minor::text,l.native_amount_minor::text
 FROM public.entries e JOIN public.lines l ON l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id
 WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.accounting_date<=$3::date
 ORDER BY e.accounting_date,e.sequence,l.position LIMIT $4`, s.TenantID, s.BookID, through, reportPostingLimit+1)
	if err != nil {
		return in, dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		p := reporting.Posting{BookID: header.ID, Currency: header.Currency, Scale: header.Scale, Treatment: "unknown"}
		if err := rows.Scan(&p.ID, &p.EntryID, &p.AccountID, &p.Date, &p.Side, &p.Amount, &p.NativeAmount); err != nil {
			return in, dbError(err)
		}
		in.Postings = append(in.Postings, p)
	}
	if err := rows.Err(); err != nil {
		return in, dbError(err)
	}
	if len(in.Postings) > reportPostingLimit {
		return in, ErrLimit
	}
	return in, nil
}
