package ledger_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func listedAccounts(t *testing.T, db *testutil.Database, s ledger.Scope) map[string]ledger.Account {
	t.Helper()
	var out struct {
		Accounts []ledger.Account `json:"accounts"`
	}
	if json.Unmarshal(db.Call(t, s, "list_accounts", map[string]string{}), &out) != nil {
		t.Fatal("invalid account list")
	}
	accounts := map[string]ledger.Account{}
	for _, a := range out.Accounts {
		accounts[a.ID] = a
	}
	return accounts
}

func callError(db *testutil.Database, s ledger.Scope, method string, payload any) error {
	_, err := db.Store.Call(db.Context, s, method, value(payload))
	return err
}

func TestAccountParentsAreRecordedRevisions(t *testing.T) {
	db := testutil.New(t)
	_, s := db.Book(t, "Example Book")
	assets := db.Account(t, s, "1000", "asset")
	bank := db.Account(t, s, "1100", "asset")
	equity := db.Account(t, s, "3000", "equity")
	parent := func(key string, a ledger.Account, revision int64, id *string) error {
		return callError(db, s, "revise_account", ledger.ReviseAccount{RequestKey: key, AccountID: a.ID, ExpectedRevision: revision, Code: a.Code, Name: a.Name, ParentAccountID: id})
	}
	if err := parent("cross-class", bank, 1, &equity.ID); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("a parent of another class was accepted", err)
	}
	if err := parent("set", bank, 1, &assets.ID); err != nil {
		t.Fatal(err)
	}
	if got := listedAccounts(t, db, s)[bank.ID]; got.ParentAccountID != assets.ID || got.Revision != 2 || got.Status != "" {
		t.Fatal("parent was not recorded", got)
	}
	// Omitting the parent keeps it; an empty string clears it.
	if err := callError(db, s, "revise_account", ledger.ReviseAccount{RequestKey: "rename", AccountID: bank.ID, ExpectedRevision: 2, Code: "1100", Name: "Example checking"}); err != nil {
		t.Fatal(err)
	}
	if got := listedAccounts(t, db, s)[bank.ID]; got.ParentAccountID != assets.ID || got.Name != "Example checking" {
		t.Fatal("a rename dropped the parent", got)
	}
	clear := ""
	if err := parent("clear", ledger.Account{ID: bank.ID, Code: "1100", Name: "Example checking"}, 3, &clear); err != nil {
		t.Fatal(err)
	}
	if got := listedAccounts(t, db, s)[bank.ID]; got.ParentAccountID != "" {
		t.Fatal("parent was not cleared", got)
	}
	if err := parent("cycle", assets, 1, &assets.ID); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("an account became its own parent", err)
	}
}

func TestRetireAccountAsksWhereTheMoneyWentAndRecordsBothTogether(t *testing.T) {
	db, s, _ := documentFixture(t)
	savings := roleAccount(t, db, s, "1010", "asset", "cash")
	checking := roleAccount(t, db, s, "1000", "asset", "cash")
	equity := db.Account(t, s, "3000", "equity")
	journal(t, db, s, "fund", "2026-01-02", savings.ID, equity.ID, "4000")

	err := callError(db, s, "retire_account", ledger.RetireAccount{RequestKey: "retire", AccountID: savings.ID, ExpectedRevision: 1})
	var remaining *ledger.RemainingBalanceError
	if !errors.As(err, &remaining) || !errors.Is(err, ledger.ErrInvalid) || len(remaining.Remaining) != 1 ||
		remaining.Remaining[0] != (ledger.RemainingBalance{Currency: "USD", Scale: 2, AmountMinor: "4000"}) ||
		!strings.HasPrefix(err.Error(), "This account still holds 40.00 USD. Say where the money went") {
		t.Fatal("a holding account must be refused with its remaining balance and a prompt", err)
	}
	if got := listedAccounts(t, db, s)[savings.ID]; got.Status != "" || got.Revision != 1 {
		t.Fatal("a refused retirement changed the account")
	}
	if err := callError(db, s, "retire_account", ledger.RetireAccount{RequestKey: "self", AccountID: savings.ID, ExpectedRevision: 1, DestinationAccountID: savings.ID, TransferDate: "2026-01-31"}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("an account can't receive its own balance", err)
	}
	document := readyOriginal(t, db, s, "closing-statement")
	q := ledger.RetireAccount{RequestKey: "retire", AccountID: savings.ID, ExpectedRevision: 1, DestinationAccountID: checking.ID, TransferDate: "2026-01-31", Description: "Closing balance wired to checking",
		Sources: []ledger.SourceInput{{DocumentID: document.ID, Location: ledger.Location{Row: locationNumber(2)}}}}
	first := db.Call(t, s, "retire_account", q)
	var retired ledger.RetiredAccount
	if json.Unmarshal(first, &retired) != nil || retired.Account.Status != "retired" || retired.Account.Revision != 2 || retired.Transfer == nil ||
		retired.Transfer.Description != "Closing balance wired to checking" || retired.Transfer.AccountingDate != "2026-01-31" || len(retired.Transfer.Lines) != 2 {
		t.Fatal("retirement and transfer were not recorded together", string(first))
	}
	if string(db.Call(t, s, "retire_account", q)) != string(first) {
		t.Fatal("a retry must return the original outcome")
	}
	citations := entryCitations(t, db, s, retired.Transfer.ID, false)
	if len(citations) != 1 || citations[0].DocumentID != document.ID || citations[0].EntryID != retired.Transfer.ID || citations[0].Location.Row == nil || *citations[0].Location.Row != 2 {
		t.Fatal("the transfer must preserve its original document and location", citations)
	}
	if b := measuredBalance(t, db, s, savings.ID); b.DebitPositiveMinor != "0" {
		t.Fatal("the retired account still holds money", b)
	}
	if b := measuredBalance(t, db, s, checking.ID); b.DebitPositiveMinor != "4000" {
		t.Fatal("the destination did not receive the balance", b)
	}
	var outcome ledger.Outcome
	if json.Unmarshal(db.Call(t, s, "get_request_outcome", ledger.OutcomeQuery{RequestKey: "retire/transfer"}), &outcome) != nil || outcome.Operation != "post_journal" {
		t.Fatal("the transfer must keep its own journal receipt", outcome)
	}
	if err := tryJournal(db, s, "late", "2026-02-01", savings.ID, equity.ID, "100"); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("a retired account took a new line", err)
	}
	if err := callError(db, s, "retire_account", ledger.RetireAccount{RequestKey: "again", AccountID: savings.ID, ExpectedRevision: 2}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("an account was retired twice", err)
	}
	if got := listedAccounts(t, db, s)[savings.ID]; got.Status != "retired" {
		t.Fatal("list_accounts must show the retired status", got)
	}
	db.Call(t, s, "reactivate_account", ledger.ReactivateAccount{RequestKey: "reactivate", AccountID: savings.ID, ExpectedRevision: 2})
	if got := listedAccounts(t, db, s)[savings.ID]; got.Status != "" || got.Revision != 3 {
		t.Fatal("reactivation is a new revision", got)
	}
	journal(t, db, s, "after", "2026-02-01", savings.ID, equity.ID, "100")
	// An empty account retires without a destination; naming one is refused.
	empty := db.Account(t, s, "1020", "asset")
	if err := callError(db, s, "retire_account", ledger.RetireAccount{RequestKey: "empty-dest", AccountID: empty.ID, ExpectedRevision: 1, DestinationAccountID: checking.ID, TransferDate: "2026-01-31"}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("an empty account has nothing to transfer", err)
	}
	db.Call(t, s, "retire_account", ledger.RetireAccount{RequestKey: "empty", AccountID: empty.ID, ExpectedRevision: 1})
}

func TestRetireAccountMovesTheNativeBalance(t *testing.T) {
	db := testutil.New(t)
	_, s := db.Book(t, "Example Book")
	euros := measuredAccount(t, db, s, "1200", "EUR", 2, true)
	other := measuredAccount(t, db, s, "1210", "EUR", 2, true)
	equity := db.Account(t, s, "3000", "equity")
	db.Call(t, s, "post_journal", measuredPost(euros, equity.ID, "fund", "1000", "1100"))
	err := callError(db, s, "retire_account", ledger.RetireAccount{RequestKey: "retire", AccountID: euros.ID, ExpectedRevision: 1})
	var remaining *ledger.RemainingBalanceError
	if !errors.As(err, &remaining) || len(remaining.Remaining) != 2 || remaining.Remaining[0].Currency != "EUR" || remaining.Remaining[0].AmountMinor != "1000" ||
		remaining.Remaining[1].Currency != "USD" || remaining.Remaining[1].AmountMinor != "1100" {
		t.Fatal("the refusal must state every currency the account holds", err)
	}
	db.Call(t, s, "retire_account", ledger.RetireAccount{RequestKey: "retire", AccountID: euros.ID, ExpectedRevision: 1, DestinationAccountID: other.ID, TransferDate: "2026-01-31"})
	if b := measuredBalance(t, db, s, euros.ID); b.DebitPositiveMinor != "0" || b.Native == nil || b.Native.DebitPositiveMinor != "0" {
		t.Fatal("the native balance was not moved", b)
	}
	if b := measuredBalance(t, db, s, other.ID); b.DebitPositiveMinor != "1100" || b.Native == nil || b.Native.DebitPositiveMinor != "1000" {
		t.Fatal("the destination did not receive both amounts", b)
	}
}

func TestStarterChartsApplyOnlyToABookWithoutAccounts(t *testing.T) {
	db := testutil.New(t)
	_, s := db.Book(t, "Example Book")
	var charts struct {
		Charts []ledger.StarterChartTemplate `json:"charts"`
	}
	if json.Unmarshal(db.Call(t, s, "list_starter_charts", map[string]string{}), &charts) != nil || len(charts.Charts) != 2 ||
		charts.Charts[0].ID != "business-v1" || charts.Charts[1].ID != "personal-v1" {
		t.Fatal("starter charts are listed by versioned ID")
	}
	if err := callError(db, s, "apply_starter_chart", ledger.ApplyStarterChart{RequestKey: "unknown", Template: "business-v0"}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("an unknown chart was applied", err)
	}
	var applied ledger.AppliedStarterChart
	if json.Unmarshal(db.Call(t, s, "apply_starter_chart", ledger.ApplyStarterChart{RequestKey: "chart", Template: "business-v1"}), &applied) != nil ||
		applied.Template != "business-v1" || len(applied.Accounts) != len(charts.Charts[0].Accounts) {
		t.Fatal("the chart's accounts were not created")
	}
	byCode := map[string]ledger.Account{}
	for _, a := range listedAccounts(t, db, s) {
		byCode[a.Code] = a
	}
	for _, item := range charts.Charts[0].Accounts {
		a, ok := byCode[item.Code]
		if !ok || a.Name != item.Name || a.Class != item.Class || a.Role != item.Role || (item.Parent == "") != (a.ParentAccountID == "") ||
			item.Parent != "" && byCode[item.Parent].ID != a.ParentAccountID {
			t.Fatal("chart account differs from the template", item, a)
		}
	}
	var request []byte
	if err := db.Admin.QueryRow(db.Context, "SELECT request_bytes FROM public.requests WHERE request_key='chart'").Scan(&request); err != nil || !strings.Contains(string(request), `"template":"business-v1"`) {
		t.Fatal("the request must record the template ID and version", err)
	}
	if err := callError(db, s, "apply_starter_chart", ledger.ApplyStarterChart{RequestKey: "second", Template: "personal-v1"}); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("a chart was applied to a Book with accounts", err)
	}
	// The database refuses it too, whatever the caller checks first.
	_, other := db.Book(t, "Example other Book")
	db.Account(t, other, "1000", "asset")
	refused(t, write(db, db.Runtime, other, func(tx pgx.Tx) error {
		_, err := receipt(db, tx, other, "apply_starter_chart", "chart-bypass")
		return err
	}), "starter chart applies only to a Book with no accounts")
}

func TestReportsRollChildrenIntoParents(t *testing.T) {
	db := testutil.New(t)
	book, s := db.Book(t, "Example Book")
	db.Call(t, s, "apply_starter_chart", ledger.ApplyStarterChart{RequestKey: "chart", Template: "business-v1"})
	byCode := map[string]ledger.Account{}
	for _, a := range listedAccounts(t, db, s) {
		byCode[a.Code] = a
	}
	journal(t, db, s, "fund", "2026-01-02", byCode["1110"].ID, byCode["3100"].ID, "50000")
	journal(t, db, s, "save", "2026-01-03", byCode["1120"].ID, byCode["1110"].ID, "20000")
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := owner.FinancialReport(db.Context, ledger.FinancialReportRequest{BookID: book.ID, Report: reporting.BalanceSheet, AsOf: "2026-12-31"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Scope.HierarchyBasis != "recorded_account_parents" {
		t.Fatal("a Book with parents reports on its recorded parents", report.Scope.HierarchyBasis)
	}
	var find func([]reporting.Row, string) *reporting.Row
	find = func(rows []reporting.Row, id string) *reporting.Row {
		for i := range rows {
			if rows[i].AccountID == id {
				return &rows[i]
			}
			if r := find(rows[i].Children, id); r != nil {
				return r
			}
		}
		return nil
	}
	banks := find(report.Rows, byCode["1100"].ID)
	if banks == nil || banks.Values["current"] != "50000" || len(banks.Children) != 2 {
		t.Fatal("the parent must roll up its children", banks)
	}
	if assets := find(report.Rows, byCode["1000"].ID); assets == nil || assets.Values["current"] != "50000" {
		t.Fatal("rollups continue to the top of the chart", assets)
	}
}

func TestAccountChangesCanBeProposedAndApproved(t *testing.T) {
	db := testutil.New(t)
	book, writer := db.Book(t, "Example Book")
	proposer := connectionScope(t, db, book.ID, "propose")
	savings := roleAccount(t, db, writer, "1010", "asset", "cash")
	checking := roleAccount(t, db, writer, "1000", "asset", "cash")
	equity := db.Account(t, writer, "3000", "equity")
	journal(t, db, writer, "fund", "2026-01-02", savings.ID, equity.ID, "4000")
	if err := callError(db, proposer, "retire_account", ledger.RetireAccount{RequestKey: "direct", AccountID: savings.ID, ExpectedRevision: 1}); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("a proposing agent retired an account directly", err)
	}
	p := prepareOperation(t, db, proposer, "propose-retire", "retire_account",
		ledger.RetireAccount{RequestKey: "retire", AccountID: savings.ID, ExpectedRevision: 1, DestinationAccountID: checking.ID, TransferDate: "2026-01-31"})
	if len(p.Preview.AccountChanges) != 1 || p.Preview.AccountChanges[0].Change != "retire" || p.Preview.EntriesAffected != 1 || len(p.Preview.Lines) != 2 ||
		p.Preview.Description != "Retire account Example 1010" || !strings.Contains(p.Approval.Message, "records this exact change") {
		t.Fatal("the proposal must preview the retirement and its transfer", p.Preview)
	}
	if got := listedAccounts(t, db, writer)[savings.ID]; got.Status != "" {
		t.Fatal("preparing a proposal changed the account")
	}
	chart := prepareOperation(t, db, proposer, "propose-parent", "revise_account",
		ledger.ReviseAccount{RequestKey: "parent", AccountID: checking.ID, ExpectedRevision: 1, Code: "1000", Name: checking.Name, ParentAccountID: &savings.ID})
	if len(chart.Preview.AccountChanges) != 1 || chart.Preview.AccountChanges[0].ParentAccountID != savings.ID {
		t.Fatal("a parent proposal must preview its parent", chart.Preview)
	}
	approveOperation(t, db, writer, p, writer)
	executeOperation(t, db, writer, p)
	if got := listedAccounts(t, db, writer)[savings.ID]; got.Status != "retired" {
		t.Fatal("the approved retirement was not recorded")
	}
	if b := measuredBalance(t, db, writer, checking.ID); b.DebitPositiveMinor != "4000" {
		t.Fatal("the approved transfer was not recorded", b)
	}
	heading := true
	if h := prepareOperation(t, db, proposer, "propose-heading", "revise_account",
		ledger.ReviseAccount{RequestKey: "heading", AccountID: equity.ID, ExpectedRevision: 1, Code: "3000", Name: equity.Name, Heading: &heading}); len(h.Preview.AccountChanges) != 1 || !h.Preview.AccountChanges[0].Heading {
		t.Fatal("a heading proposal must preview the change", h.Preview)
	}
	// The DB refuses a retired parent when the approved work runs.
	approveOperation(t, db, writer, chart, writer)
	if _, err := db.Store.Call(db.Context, writer, "execute_proposal", value(ledger.ExecuteProposal{ProposalID: chart.ID, SHA256: chart.SHA256})); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("an approved change still meets every account rule", err)
	}
}
