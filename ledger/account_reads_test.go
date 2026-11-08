package ledger_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestOwnerAccountReadsPreservePaginationSplitsAndRecordedBasis(t *testing.T) {
	db, book, scope, cash, equity := setup(t)
	other := db.Account(t, scope, "1100", "asset")
	q := post(cash.ID, equity.ID, "prior")
	q.AccountingDate, q.EconomicDate = "2025-12-30", "2025-12-29"
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "1000", "1000"
	db.Call(t, scope, "post_journal", q)
	q = post(cash.ID, equity.ID, "split")
	q.AccountingDate, q.EconomicDate = "2026-01-02", "2025-12-31"
	q.Lines = []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "60"}, {AccountID: cash.ID, Side: "debit", AmountMinor: "40"}, {AccountID: other.ID, Side: "debit", AmountMinor: "50"}, {AccountID: equity.ID, Side: "credit", AmountMinor: "150"}}
	db.Call(t, scope, "post_journal", q)
	for i := 0; i < 101; i++ {
		q = post(cash.ID, equity.ID, fmt.Sprintf("row-%d", i))
		q.AccountingDate, q.EconomicDate = "2026-01-03", "2026-01-01"
		q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "1", "1"
		db.Call(t, scope, "post_journal", q)
	}
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	query := ledger.AccountDetailQuery{AccountIDs: []string{cash.ID}, From: "2026-01-01", To: "2026-01-31", Limit: 100}
	first, err := owner.AccountDetail(db.Context, book.ID, query)
	if err != nil || first.Ledger == nil || first.Ledger.Opening != "1000" || first.Ledger.OpeningBasis != "recorded_prior_postings" || first.Ledger.Closing != "1201" || first.Ledger.Debits != "201" || first.Count != 103 || len(first.Ledger.Lines) != 100 || first.Ledger.Lines[99].Balance != "1198" || first.Total != "1201" || first.Earliest != "2025-12-30" || first.SourceCoverage != "unknown" {
		t.Fatal("ledger page lost a split, prior position or exact balance", err)
	}
	if first.Ledger.Lines[0].Date != "2026-01-02" || first.Ledger.Lines[0].EconomicDate != "2025-12-31" || first.Ledger.Lines[0].Position >= first.Ledger.Lines[1].Position {
		t.Fatal("ledger dates or intra-entry order changed")
	}
	query.Offset, query.Revision = 100, first.Revision
	last, err := owner.AccountDetail(db.Context, book.ID, query)
	if err != nil || last.Activity != nil || last.Count != 103 || len(last.Ledger.Lines) != 3 || last.Ledger.Lines[0].Balance != "1199" || last.Ledger.Lines[2].Balance != "1201" {
		t.Fatal("continuation reset its running balance or fetched unrequested activity", err)
	}
	ids := []string{cash.ID, other.ID}
	sort.Strings(ids)
	group, err := owner.AccountDetail(db.Context, book.ID, ledger.AccountDetailQuery{AccountIDs: ids, From: "2026-01-01", To: "2026-01-31", Mode: "group", Limit: 100})
	if err != nil || group.Ledger != nil || group.Count != 103 || group.Total != "1251" || len(group.Entries) != 100 || group.Activity == nil {
		t.Fatal("group counted lines instead of entries", err)
	}
	activity, err := owner.AccountActivity(db.Context, book.ID, "2026-01-31", ids)
	if err != nil || activity.Accounts[cash.ID].Total != 103 || activity.Accounts[other.ID].Total != 1 || activity.ActivityBasis != "recorded_entries" || activity.HierarchyBasis != "flat_recorded_accounts" || activity.SourceCoverage != "unknown" {
		t.Fatal("activity inferred source coverage or duplicated a split", err)
	}
	found := false
	for _, set := range activity.Groups {
		if len(set.Codes) == 2 {
			found = true
			if set.Count != 103 {
				t.Fatal("selected group double-counted the split entry")
			}
		}
	}
	if !found {
		t.Fatal("explicit selected account group missing")
	}
	db.Call(t, scope, "post_journal", post(cash.ID, equity.ID, "append"))
	if value, err := owner.AccountDetail(db.Context, book.ID, query); !errors.Is(err, ledger.ErrConflict) || value.BookID != "" {
		t.Fatal("stale continuation silently changed the population", err)
	}
}

func TestOwnerAccountReadsKeepNativeUnknownsAndExactAmounts(t *testing.T) {
	db, book, scope, cash, equity := setup(t)
	income := db.Account(t, scope, "4000", "income")
	q := post(cash.ID, income.ID, "exact")
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = "9007199254740993", "9007199254740993"
	db.Call(t, scope, "post_journal", q)
	foreign := measuredAccount(t, db, scope, "1200", "JPY", 0, true)
	q = measuredPost(foreign, equity.ID, "rounded", "1", "0")
	quoteOn(&q, "0.001")
	db.Call(t, scope, "post_journal", q)
	legacy := measuredAccount(t, db, scope, "1300", "EUR", 2, false)
	db.Call(t, scope, "post_journal", measuredPost(legacy, equity.ID, "incomplete", "100", "125"))
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := owner.AccountActivity(db.Context, book.ID, "2026-01-31", nil)
	if err != nil || out.NativeStatus[cash.ID] != "not_recorded" || out.NativeStatus[legacy.ID] != "incomplete" || out.NativeStatus[foreign.ID] != "complete" || out.NativeBalances[foreign.ID] != "1" || out.NativeCurrencies[foreign.ID] != "JPY" || out.NativeScales[foreign.ID] != 0 || out.Accounts[foreign.ID].Total != 1 {
		t.Fatal("native unknowns or zero-Book activity were lost", err)
	}
	if _, ok := out.NativeBalances[legacy.ID]; ok {
		t.Fatal("incomplete native history became an asserted amount")
	}
	if _, ok := out.NativeBalances[cash.ID]; ok {
		t.Fatal("unrecorded native history became zero")
	}
	qdetail := ledger.AccountDetailQuery{AccountIDs: []string{income.ID}, From: "2026-01-01", To: "2026-01-31", Limit: 100}
	detail, err := owner.AccountDetail(db.Context, book.ID, qdetail)
	if err != nil || detail.Basis != "period" || detail.Total != "9007199254740993" || detail.Ledger.PositiveSide != "credit" || detail.Ledger.Credits != detail.Total || detail.Ledger.PeriodChange != detail.Total {
		t.Fatal("stored income class or exact minor units changed", err)
	}
	qdetail.AccountIDs = []string{foreign.ID}
	detail, err = owner.AccountDetail(db.Context, book.ID, qdetail)
	if err != nil || detail.Count != 1 || detail.Total != "0" || detail.Activity.NativeBalances[foreign.ID] != "1" || len(detail.Ledger.Lines) != 1 {
		t.Fatal("zero-Book native movement vanished from detail", err)
	}
	before, err := owner.AccountActivity(db.Context, book.ID, "2026-01-31", []string{foreign.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Admin.Exec(db.Context, `CREATE FUNCTION public.future_recording() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN NEW.recorded_at=clock_timestamp()+interval '1 day'; RETURN NEW; END $$;
 CREATE TRIGGER future_recording BEFORE INSERT ON public.entries FOR EACH ROW EXECUTE FUNCTION public.future_recording()`); err != nil {
		t.Fatal(err)
	}
	q = measuredPost(foreign, equity.ID, "future", "2", "0")
	quoteOn(&q, "0.001")
	db.Call(t, scope, "post_journal", q)
	after, err := owner.AccountActivity(db.Context, book.ID, "2026-01-31", []string{foreign.ID})
	if err != nil || after.Digest != before.Digest || after.Accounts[foreign.ID].Total != 1 || after.NativeBalances[foreign.ID] != "1" {
		t.Fatal("knowledge cutoff differed across activity and native balances", err)
	}
	if _, ok := new(big.Int).SetString(detail.Total, 10); !ok {
		t.Fatal("detail amount is not an exact integer")
	}
}

func TestOwnerAccountReadsRejectMalformedForeignAndMixedScopes(t *testing.T) {
	db, book, _, cash, equity := setup(t)
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := owner.AccountActivity(db.Context, book.ID, "2026-01-31", nil)
	if err != nil || empty.Accounts[cash.ID].Total != 0 || len(empty.EmptyWeekly) != 52 || len(empty.NativeBalances) != 0 || empty.SourceCoverage != "unknown" {
		t.Fatal("valid empty recorded activity failed", err)
	}
	query := ledger.AccountDetailQuery{AccountIDs: []string{cash.ID}, From: "2026-01-01", To: "2026-01-31", Limit: 100}
	read, err := owner.AccountDetail(db.Context, book.ID, query)
	if err != nil || read.Ledger == nil || read.Count != 0 || read.Total != "0" || read.Earliest != "" {
		t.Fatal("valid empty ledger failed", err)
	}
	_, other, otherScope := tenantOwner(t, db)
	foreign := db.Account(t, otherScope, "1000", "asset")
	if got, err := owner.AccountActivity(db.Context, other.ID, "2026-01-31", nil); !errors.Is(err, ledger.ErrNotFound) || got.BookID != "" {
		t.Fatal("activity crossed Book ownership", err)
	}
	if got, err := owner.AccountActivity(db.Context, book.ID, "2026-01-31", []string{foreign.ID}); !errors.Is(err, ledger.ErrNotFound) || got.BookID != "" {
		t.Fatal("foreign account accepted as a selector", err)
	}
	for _, ids := range [][]string{{}, {cash.ID, cash.ID}, {"1000"}} {
		if _, err := owner.AccountActivity(db.Context, book.ID, "2026-01-31", ids); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatal("invalid account identity scope accepted", err)
		}
	}
	for _, day := range []string{"2026-02-31", "0001-01-01", ""} {
		if _, err := owner.AccountActivity(db.Context, book.ID, day, nil); !errors.Is(err, ledger.ErrInvalid) {
			t.Fatal("invalid activity window accepted", err)
		}
	}
	for _, change := range []func(*ledger.AccountDetailQuery){
		func(q *ledger.AccountDetailQuery) { q.AccountIDs = []string{cash.ID, equity.ID}; q.Mode = "group" },
		func(q *ledger.AccountDetailQuery) { q.From = "2026-02-31" }, func(q *ledger.AccountDetailQuery) { q.Offset = 1 },
		func(q *ledger.AccountDetailQuery) { q.Mode = "guessed" }, func(q *ledger.AccountDetailQuery) { q.Revision = strings.Repeat("z", 64) },
		func(q *ledger.AccountDetailQuery) { q.Limit = 0 }, func(q *ledger.AccountDetailQuery) { q.Offset = -1 },
	} {
		q := query
		change(&q)
		if got, err := owner.AccountDetail(db.Context, book.ID, q); !errors.Is(err, ledger.ErrInvalid) || got.BookID != "" {
			t.Fatal("malformed detail returned accounting records", err)
		}
	}
	encoded, err := json.Marshal(empty)
	if err != nil || strings.Contains(string(encoded), book.TenantID) {
		t.Fatal("account read disclosed tenant identity", err)
	}
}
