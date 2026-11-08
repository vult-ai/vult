package ledger

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/reporting"
)

type CashAccountRead struct {
	ID              string  `json:"id"`
	Code            string  `json:"code"`
	GLCode          string  `json:"gl_code,omitempty"`
	Name            string  `json:"name"`
	Currency        string  `json:"currency,omitempty"`
	Scale           *int    `json:"scale,omitempty"`
	NativeStatus    string  `json:"native_status"`
	NativeMinor     *string `json:"native_minor,omitempty"`
	CarryingMinor   string  `json:"carrying_minor"`
	ThroughSequence int64   `json:"through_sequence"`
}

type CashSummaryRead struct {
	RecordedState
	SchemaVersion int                 `json:"schema_version"`
	BookID        string              `json:"book_id"`
	Currency      string              `json:"currency"`
	Scale         int                 `json:"scale"`
	Period        reporting.Period    `json:"period"`
	Through       string              `json:"through"`
	Revision      string              `json:"revision"`
	Basis         string              `json:"basis"`
	CashStatus    string              `json:"cash_status"`
	Cash          *string             `json:"cash_minor,omitempty"`
	Accounts      []CashAccountRead   `json:"cash_accounts"`
	MoneyIn       *string             `json:"money_in_minor,omitempty"`
	MoneyOut      *string             `json:"money_out_minor,omitempty"`
	MovementCount *int                `json:"movement_count,omitempty"`
	PositionError string              `json:"position_error,omitempty"`
	MovementError string              `json:"movement_error,omitempty"`
	Findings      []reporting.Finding `json:"findings"`
	Limitations   []string            `json:"limitations"`
}

type DashboardRead struct {
	RecordedState
	reporting.Dashboard
}

type DashboardProjectionRead struct {
	RecordedState
	reporting.DashboardProjection
}

func (a TenantAdmin) withDashboardRead(ctx context.Context, book, from, to string, read func(pgx.Tx, Scope, Book, reporting.Input, RecordedState) error) error {
	if !ValidDate(from) || !ValidDate(to) || from > to {
		return ErrInvalid
	}
	return a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, header Book) error {
		req := FinancialReportRequest{BookID: book, Report: reporting.BalanceSheet, AsOf: to}
		in, state, err := reportInput(ctx, tx, scope, header, req, "", "", false)
		if err != nil {
			return err
		}
		in, err = scopeReportInput(in, req)
		if err != nil {
			return err
		}
		return read(tx, scope, header, in, state.RecordedState)
	})
}

func (a TenantAdmin) Dashboard(ctx context.Context, book, from, to string) (DashboardRead, error) {
	var out DashboardRead
	err := a.withDashboardRead(ctx, book, from, to, func(_ pgx.Tx, _ Scope, _ Book, in reporting.Input, state RecordedState) error {
		var err error
		out.RecordedState = state
		out.Dashboard, err = reporting.BuildDashboard(in, from, to)
		return reportError(err)
	})
	if err != nil {
		return DashboardRead{}, err
	}
	return out, nil
}

func (a TenantAdmin) DashboardProjection(ctx context.Context, book, from, to, unit string) (DashboardProjectionRead, error) {
	var out DashboardProjectionRead
	err := a.withDashboardRead(ctx, book, from, to, func(_ pgx.Tx, _ Scope, _ Book, in reporting.Input, state RecordedState) error {
		var err error
		out.RecordedState = state
		out.DashboardProjection, err = reporting.BuildDashboardProjection(in, from, to, unit)
		return reportError(err)
	})
	if err != nil {
		return DashboardProjectionRead{}, err
	}
	return out, nil
}

func (a TenantAdmin) CashSummary(ctx context.Context, book, from, to string) (CashSummaryRead, error) {
	var out CashSummaryRead
	err := a.withDashboardRead(ctx, book, from, to, func(tx pgx.Tx, scope Scope, _ Book, in reporting.Input, state RecordedState) error {
		base, err := reporting.BuildDashboard(in, from, to)
		if err != nil {
			return reportError(err)
		}
		out = CashSummaryRead{RecordedState: state, SchemaVersion: 1, BookID: book, Currency: in.Book.Currency, Scale: in.Book.Scale, Period: base.Period, Through: to, Revision: base.Version, Basis: "recorded_cash_account_carrying", CashStatus: base.CashStatus, Cash: base.Cash, Accounts: []CashAccountRead{}, MoneyIn: base.MoneyIn, MoneyOut: base.MoneyOut, MovementCount: base.MovementCount, Findings: base.Findings, Limitations: base.Limitations}
		if base.Cash == nil {
			out.PositionError = "Cash account scope is not established."
		}
		if base.CashFlow == nil {
			out.MovementError = "Cash movement treatment is not established."
		}
		out.Accounts, err = readCashAccounts(ctx, tx, scope, to, in.Provenance.KnowledgeCutoff)
		return err
	})
	if err != nil {
		return CashSummaryRead{}, err
	}
	return out, nil
}

func readCashAccounts(ctx context.Context, tx pgx.Tx, scope Scope, through, cutoff string) ([]CashAccountRead, error) {
	positions, err := readAccountPositions(ctx, tx, scope, positionReadScope{Through: through, Cutoff: cutoff, CashOnly: true, Limit: 1000})
	if err != nil {
		return nil, err
	}
	return cashAccountViews(positions), nil
}
