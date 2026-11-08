package ledger

import (
	"context"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/reporting"
)

type StateSummary struct {
	Income   string  `json:"income_minor"`
	Expenses string  `json:"expenses_minor"`
	Net      string  `json:"net_minor"`
	Cash     *string `json:"cash_minor,omitempty"`
}

type RecordedPosition struct {
	Assets      string `json:"assets_minor"`
	Liabilities string `json:"liabilities_minor"`
	Equity      string `json:"equity_accounts_minor"`
	Accumulated string `json:"accumulated_result_minor"`
	NetAssets   string `json:"net_assets_minor"`
	Basis       string `json:"basis"`
	Coverage    string `json:"source_coverage"`
}

type BrowserState struct {
	RecordedState
	SchemaVersion     int                          `json:"schema_version"`
	Version           int                          `json:"version"`
	ReadView          string                       `json:"read_view"`
	Bootstrap         bool                         `json:"bootstrap"`
	Book              Book                         `json:"book"`
	Currencies        []Currency                   `json:"currencies"`
	Accounts          []ActivityAccount            `json:"accounts"`
	AccountBalances   []AccountPositionRead        `json:"account_balances"`
	AccountActivity   *AccountActivityRead         `json:"account_activity,omitempty"`
	CashAccounts      []CashAccountRead            `json:"cash_accounts"`
	Transactions      []ActivityEntry              `json:"transactions"`
	Summary           *StateSummary                `json:"summary,omitempty"`
	Dashboard         *DashboardRead               `json:"dashboard,omitempty"`
	DashboardSnapshot *reporting.DashboardSnapshot `json:"dashboard_snapshot,omitempty"`
	Position          *RecordedPosition            `json:"position,omitempty"`
	AsOf              string                       `json:"as_of,omitempty"`
	Period            *reporting.Period            `json:"period,omitempty"`
	CashStatus        string                       `json:"cash_status"`
	HierarchyBasis    string                       `json:"hierarchy_basis"`
	SourceCoverage    string                       `json:"source_coverage"`
	Revision          string                       `json:"revision"`
	Capabilities      map[string]bool              `json:"capabilities"`
}

func (a TenantAdmin) BrowserState(ctx context.Context, book, view, from, to string) (BrowserState, error) {
	return a.browserState(ctx, book, view, from, to, "")
}

func (a TenantAdmin) DashboardState(ctx context.Context, book, from, to, unit string) (BrowserState, error) {
	switch unit {
	case "day", "week", "month", "quarter", "year":
	default:
		return BrowserState{}, ErrInvalid
	}
	return a.browserState(ctx, book, "dashboard", from, to, unit)
}

func (a TenantAdmin) browserState(ctx context.Context, book, view, from, to, unit string) (BrowserState, error) {
	metadata := view == "settings" || view == "documents" || view == "statements"
	if from != "" && !ValidDate(from) || to != "" && !ValidDate(to) || from != "" && to != "" && from > to || !metadata && (view != "dashboard" && view != "accounts" || from == "" || to == "") {
		return BrowserState{}, ErrInvalid
	}
	var out BrowserState
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, header Book) error {
		out = BrowserState{SchemaVersion: 1, Version: 1, ReadView: view, Bootstrap: metadata, Book: header, Currencies: Currencies(), Accounts: []ActivityAccount{}, Transactions: []ActivityEntry{}, HierarchyBasis: "flat_recorded_accounts", SourceCoverage: "unknown", CashStatus: "not_requested", Capabilities: map[string]bool{"dashboard": true, "financial_reports": true, "account_balances": true, "activity": true, "stored_account_parents": false}}
		state, err := captureReadState(ctx, tx, scope, header, recordedReadScope{AllAccepted: metadata})
		if err != nil {
			return err
		}
		out.RecordedState = state.RecordedState
		if unit != "" {
			req := FinancialReportRequest{BookID: book, Report: reporting.BalanceSheet, AsOf: to}
			in, err := reportInputAt(ctx, tx, scope, header, req, state, "", false)
			if err != nil {
				return err
			}
			dashboard, err := reporting.BuildDashboardSnapshot(in, from, to, unit)
			if err != nil {
				return reportError(err)
			}
			for _, account := range in.Accounts {
				class := account.Kind
				switch class {
				case "other_income":
					class = "income"
				case "other_expense", "cost_of_goods_sold":
					class = "expense"
				}
				out.Accounts = append(out.Accounts, ActivityAccount{ID: account.ID, Code: account.ID, GLCode: account.GLCode, Name: account.Name, Kind: class, Role: account.Role, Revision: account.Revision})
			}
			out.DashboardSnapshot = &dashboard
			out.AsOf, out.Period, out.CashStatus = to, &dashboard.Period, dashboard.CashStatus
			out.Revision = state.StateRevision
			return nil
		}
		if metadata {
			accounts, err := readScopedCatalog(ctx, tx, scope)
			if err != nil {
				return err
			}
			for _, account := range accounts {
				out.Accounts = append(out.Accounts, activityAccount(account))
			}
			out.Revision = archiveHash(raw(struct {
				Book     Book
				Accounts []ActivityAccount
			}{header, out.Accounts}))
			return nil
		}
		positions, err := readAccountPositions(ctx, tx, scope, positionReadScope{From: from, Through: to, Cutoff: state.StateKnowledgeCutoff, Limit: reportAccountLimit})
		if err != nil {
			return err
		}
		out.AccountBalances = positions
		out.AsOf, out.Period = to, &reporting.Period{From: from, To: to}
		out.CashAccounts = cashAccountViews(positions)
		out.CashStatus = "unavailable"
		position, summary, err := summarizePositions(positions)
		if err != nil {
			return err
		}
		out.Position, out.Summary = &position, &summary
		if summary.Cash != nil {
			out.CashStatus = "partial"
		}
		for _, account := range positions {
			out.Accounts = append(out.Accounts, ActivityAccount{ID: account.ID, Code: account.ID, GLCode: account.GLCode, Name: account.Name, Kind: account.Kind, Role: account.Role, Revision: account.Revision})
		}
		out.Revision = archiveHash(raw(struct {
			Book      Book
			Epoch     int64
			From, To  string
			Positions []AccountPositionRead
		}{header, state.epoch, from, to, positions}))
		if view == "accounts" {
			lines, err := readAccountHistoryLines(ctx, tx, scope, to, state.StateKnowledgeCutoff, nil, false)
			if err != nil {
				return err
			}
			activity, err := buildAccountActivity(accountHistory{
				State: state.RecordedState, Book: header, AsOf: to, Accounts: positions, Lines: lines,
			})
			if err != nil {
				return err
			}
			out.AccountActivity = &activity
		}
		if view == "dashboard" {
			req := FinancialReportRequest{BookID: book, Report: reporting.BalanceSheet, AsOf: to}
			in, err := reportInputAt(ctx, tx, scope, header, req, state, state.StateKnowledgeCutoff, false)
			if err != nil {
				return err
			}
			in, err = scopeReportInput(in, req)
			if err != nil {
				return err
			}
			dashboard, err := reporting.BuildDashboard(in, from, to)
			if err != nil {
				return reportError(err)
			}
			if dashboard.Profit != summary.Net {
				return ErrDenied
			}
			out.Dashboard = &DashboardRead{RecordedState: state.RecordedState, Dashboard: dashboard}
		}
		return nil
	})
	if err != nil {
		return BrowserState{}, err
	}
	return out, nil
}

func activityAccount(a Account) ActivityAccount {
	return ActivityAccount{ID: a.ID, Code: a.ID, GLCode: a.Code, Name: a.Name, Kind: a.Class, Role: a.Role, Revision: a.Revision}
}

func summarizePositions(accounts []AccountPositionRead) (RecordedPosition, StateSummary, error) {
	assets, liabilities, equity, accumulated, income, expenses, balance, cash := new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int)
	hasCash := false
	for _, a := range accounts {
		amount, valid := new(big.Int).SetString(a.BalanceMinor, 10)
		period, periodValid := new(big.Int).SetString(a.PeriodMinor, 10)
		if !valid || !periodValid {
			return RecordedPosition{}, StateSummary{}, ErrDenied
		}
		balance.Add(balance, amount)
		switch a.Kind {
		case "asset":
			assets.Add(assets, amount)
			if a.Role == "cash" {
				hasCash = true
				cash.Add(cash, amount)
			}
		case "liability":
			liabilities.Sub(liabilities, amount)
		case "equity":
			equity.Sub(equity, amount)
		case "income":
			income.Sub(income, period)
			accumulated.Sub(accumulated, amount)
		case "expense":
			expenses.Add(expenses, period)
			accumulated.Sub(accumulated, amount)
		default:
			return RecordedPosition{}, StateSummary{}, ErrDenied
		}
	}
	if balance.Sign() != 0 {
		return RecordedPosition{}, StateSummary{}, ErrDenied
	}
	position := RecordedPosition{Assets: assets.String(), Liabilities: liabilities.String(), Equity: equity.String(), Accumulated: accumulated.String(), NetAssets: new(big.Int).Sub(assets, liabilities).String(), Basis: "recorded_book_carrying", Coverage: "unknown"}
	summary := StateSummary{Income: income.String(), Expenses: expenses.String(), Net: new(big.Int).Sub(income, expenses).String()}
	if hasCash {
		value := cash.String()
		summary.Cash = &value
	}
	return position, summary, nil
}
