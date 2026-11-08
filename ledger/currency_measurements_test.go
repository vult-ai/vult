package ledger_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func currencyScale(n int) *int { return &n }
func measuredMoney(code string, scale int, amount string) ledger.CurrencyAmount {
	return ledger.CurrencyAmount{Currency: code, Scale: currencyScale(scale), AmountMinor: amount}
}
func measuredAccount(t *testing.T, db *testutil.Database, s ledger.Scope, code, currency string, scale int, origin bool) ledger.Account {
	t.Helper()
	q := ledger.CreateAccount{RequestKey: code, Class: "asset", Code: code, Name: "Example cash account", Currency: currency, Scale: currencyScale(scale), Role: "cash"}
	if origin {
		q.NativeOrigin = "empty_ledger"
	}
	var a ledger.Account
	if json.Unmarshal(db.Call(t, s, "create_account", q), &a) != nil {
		t.Fatal("invalid account result")
	}
	return a
}
func measuredPost(a ledger.Account, other, key, native, book string) ledger.PostJournal {
	q := post(a.ID, other, key)
	q.Lines[0].AmountMinor, q.Lines[1].AmountMinor = book, book
	q.Lines[0].Native = &ledger.NativeMovement{Currency: a.Currency, Scale: a.Scale, AmountMinor: native}
	return q
}
func quoteOn(q *ledger.PostJournal, rate string) {
	n := q.Lines[0].Native
	input := measuredMoney(n.Currency, *n.Scale, n.AmountMinor)
	q.Lines[0].Valuation = &ledger.Valuation{Basis: "applied_quote", Policy: "quoted-rate-half-even-v1", Input: &input,
		Rate: &ledger.AppliedRate{Base: n.Currency, Quote: "USD", Rate: rate, RequestedDate: "2026-01-02", ObservationDate: "2026-01-02", Provenance: "Hypothetical quote"}}
}
func measuredBalance(t *testing.T, db *testutil.Database, s ledger.Scope, account string) ledger.Balance {
	t.Helper()
	var v ledger.Balance
	if json.Unmarshal(db.Call(t, s, "get_balance", ledger.AccountQuery{AccountID: account}), &v) != nil {
		t.Fatal("invalid balance")
	}
	return v
}
func assertPosition(t *testing.T, v ledger.Balance, native, book string) {
	t.Helper()
	if v.Native == nil || v.Native.Status != "complete" || v.Native.DebitPositiveMinor != native || v.DebitPositiveMinor != book {
		t.Fatal("position differs from literal expectation", v)
	}
}
func rejectMeasured(t *testing.T, db *testutil.Database, s ledger.Scope, q ledger.PostJournal) {
	t.Helper()
	var before, after int
	if db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.requests").Scan(&before) != nil {
		t.Fatal("count failed")
	}
	if _, err := db.Store.Call(db.Context, s, "post_journal", value(q)); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("invalid measurement accepted", err)
	}
	if db.Admin.QueryRow(db.Context, "SELECT count(*) FROM public.requests").Scan(&after) != nil || before != after {
		t.Fatal("rejection recorded a partial outcome")
	}
}

func TestCurrencyZeroValuesKeepNativeEffectsAndContextSeparate(t *testing.T) {
	db, _, s, _, equity := setup(t)
	cash := measuredAccount(t, db, s, "1200", "JPY", 0, true)
	q := measuredPost(cash, equity.ID, "tiny-acquisition", "1", "0")
	quoteOn(&q, "0.001")
	first := db.Call(t, s, "post_journal", q)
	assertPosition(t, measuredBalance(t, db, s, cash.ID), "1", "0")
	if !bytes.Equal(first, db.Call(t, s, "post_journal", q)) {
		t.Fatal("retry changed a rounded-zero operation")
	}
	expense := db.Account(t, s, "6000", "expense")
	spend := measuredPost(cash, expense.ID, "tiny-expense", "1", "0")
	spend.Lines[0].Side, spend.Lines[1].Side = "credit", "debit"
	quoteOn(&spend, "0.001")
	spend.Lines[1].Contexts = []ledger.ContextMeasurement{{Role: "invoice", Amount: measuredMoney("JPY", 0, "1")}}
	var entry ledger.Entry
	if json.Unmarshal(db.Call(t, s, "post_journal", spend), &entry) != nil || entry.Lines[1].Native != nil || len(entry.Lines[1].Contexts) != 1 {
		t.Fatal("expense acquired an invented native movement")
	}
	assertPosition(t, measuredBalance(t, db, s, cash.ID), "0", "0")
	empty := post(expense.ID, equity.ID, "empty-context")
	empty.Lines[0].AmountMinor, empty.Lines[1].AmountMinor = "0", "0"
	empty.Lines[0].Contexts = spend.Lines[1].Contexts
	rejectMeasured(t, db, s, empty)
	zero := measuredPost(cash, equity.ID, "zero-native-only", "0", "0")
	rejectMeasured(t, db, s, zero)
}

func TestCurrencyFeeFreeTransferChecksNativeUnitsAtZeroBookValue(t *testing.T) {
	db, _, s, _, _ := setup(t)
	a := measuredAccount(t, db, s, "1100", "JPY", 0, true)
	b := measuredAccount(t, db, s, "1200", "JPY", 0, true)
	q := measuredPost(a, b.ID, "mismatched-transfer", "2", "0")
	q.Lines[1].Native = &ledger.NativeMovement{Currency: "JPY", Scale: currencyScale(0), AmountMinor: "1"}
	q.MeasurementPolicy = &ledger.MeasurementPolicy{Version: 1, NativeTransfers: []ledger.NativeTransfer{{Lines: []int{1, 2}}}}
	rejectMeasured(t, db, s, q)
	q.RequestKey = "matched-transfer"
	q.Lines[0].Native.AmountMinor = "1"
	db.Call(t, s, "post_journal", q)
	assertPosition(t, measuredBalance(t, db, s, a.ID), "1", "0")
	assertPosition(t, measuredBalance(t, db, s, b.ID), "-1", "0")
}

func TestCurrencyMeasurementsRejectMalformedUnitsAndOverflow(t *testing.T) {
	db, _, s, _, equity := setup(t)
	cash := measuredAccount(t, db, s, "1200", "JPY", 0, true)
	for _, modify := range []func(*ledger.PostJournal){
		func(q *ledger.PostJournal) { q.Lines[0].Native.Scale = nil },
		func(q *ledger.PostJournal) { q.Lines[0].Native.Scale = currencyScale(2) },
		func(q *ledger.PostJournal) { q.Lines[0].Native.Currency = "ABC" },
		func(q *ledger.PostJournal) { q.Lines[0].Native.AmountMinor = "01" },
		func(q *ledger.PostJournal) { q.Lines[0].Native.AmountMinor = "-1" },
		func(q *ledger.PostJournal) { q.Lines[0].Native.AmountMinor = "9223372036854775808" },
	} {
		q := measuredPost(cash, equity.ID, "malformed", "1", "0")
		modify(&q)
		rejectMeasured(t, db, s, q)
	}
	q := measuredPost(cash, equity.ID, "maximum", "9223372036854775807", "0")
	db.Call(t, s, "post_journal", q)
	q = measuredPost(cash, equity.ID, "overflow", "1", "0")
	rejectMeasured(t, db, s, q)
}

func TestCurrencyRetriesAndReversalsFreezeAcceptedInputs(t *testing.T) {
	db, book, s, _, equity := setup(t)
	cash := measuredAccount(t, db, s, "1200", "JPY", 0, true)
	q := measuredPost(cash, equity.ID, "frozen-quote", "1", "0")
	quoteOn(&q, "0.001")
	first := db.Call(t, s, "post_journal", q)
	var original ledger.Entry
	if json.Unmarshal(first, &original) != nil {
		t.Fatal("entry decode failed")
	}
	jpy := "EUR"
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "change-denomination", AccountID: cash.ID, ExpectedRevision: 1, Code: cash.Code, Name: cash.Name, Currency: &jpy, Scale: currencyScale(2)})
	if !bytes.Equal(first, db.Call(t, s, "post_journal", q)) {
		t.Fatal("later denomination changed retained outcome")
	}
	changed := q
	changed.Lines = append([]ledger.PostLine{}, q.Lines...)
	valuation := *q.Lines[0].Valuation
	rate := *valuation.Rate
	rate.Rate = "0.02"
	valuation.Rate = &rate
	changed.Lines[0].Valuation = &valuation
	if _, err := db.Store.Call(db.Context, s, "post_journal", value(changed)); !errors.Is(err, ledger.ErrConflict) {
		t.Fatal("changed quote reused accepted request key", err)
	}
	var reversal ledger.Entry
	if json.Unmarshal(db.Call(t, s, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse-frozen", EntryID: original.ID, AccountingDate: "2026-02-01", Reason: "Example correction"}), &reversal) != nil {
		t.Fatal("invalid reversal")
	}
	if !bytes.Equal(value(reversal.Lines[0].Native), value(original.Lines[0].Native)) || !bytes.Equal(value(reversal.Lines[0].Valuation), value(original.Lines[0].Valuation)) {
		t.Fatal("reversal recalculated accepted input")
	}
	if _, err := db.Owner.Verify(db.Context, book.ID); err != nil {
		t.Fatal("verification lost frozen measurement", err)
	}
}

func TestCurrencyAllocationsPreserveAuthoritativeTotal(t *testing.T) {
	db, _, s, debit, credit := setup(t)
	group := 1
	q := post(debit.ID, credit.ID, "one-cent")
	q.Lines = []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: "1"}, {AccountID: debit.ID, Side: "debit", AmountMinor: "0"}, {AccountID: debit.ID, Side: "debit", AmountMinor: "0"}, {AccountID: credit.ID, Side: "credit", AmountMinor: "1"}}
	for i := 0; i < 3; i++ {
		q.Lines[i].Valuation = &ledger.Valuation{Basis: "authoritative_allocation", Policy: "largest-remainder-v1", AllocationGroup: &group}
	}
	q.MeasurementPolicy = &ledger.MeasurementPolicy{Version: 1, Allocations: []ledger.AmountAllocation{{Total: measuredMoney("USD", 2, "1"), Method: "largest-remainder-v1", Parts: []ledger.AllocationPart{{Line: 3, Weight: "1"}, {Line: 1, Weight: "1"}, {Line: 2, Weight: "1"}}}}}
	db.Call(t, s, "post_journal", q)
	bad := q
	bad.RequestKey = "wrong-residual"
	bad.Lines = append([]ledger.PostLine{}, q.Lines...)
	bad.Lines[0].AmountMinor, bad.Lines[1].AmountMinor = "0", "1"
	rejectMeasured(t, db, s, bad)
}

func TestCurrencyIndependentEventsAreBatchSizeIndependent(t *testing.T) {
	for _, batch := range []int{1, 3, 20} {
		t.Run(fmt.Sprint(batch), func(t *testing.T) {
			db, _, s, _, equity := setup(t)
			cash := measuredAccount(t, db, s, "1200", "JPY", 0, true)
			for start := 0; start < 20; start += batch {
				for i := start; i < min(20, start+batch); i++ {
					q := measuredPost(cash, equity.ID, fmt.Sprintf("event-%d", i), "1", "0")
					quoteOn(&q, "0.004")
					db.Call(t, s, "post_journal", q)
				}
			}
			assertPosition(t, measuredBalance(t, db, s, cash.ID), "20", "0")
		})
	}
}

func TestCurrencyAssignmentDoesNotInventLegacyNativeHistory(t *testing.T) {
	db, _, s, cash, equity := setup(t)
	db.Call(t, s, "post_journal", post(cash.ID, equity.ID, "old-book-only"))
	code := "JPY"
	db.Call(t, s, "revise_account", ledger.ReviseAccount{RequestKey: "assign-currency", AccountID: cash.ID, ExpectedRevision: 1, Code: cash.Code, Name: cash.Name, Currency: &code, Scale: currencyScale(0)})
	balance := measuredBalance(t, db, s, cash.ID)
	if balance.Native == nil || balance.Native.Status != "incomplete" || balance.Native.DebitPositiveMinor != "" || balance.DebitPositiveMinor != "1250" {
		t.Fatal("assignment fabricated native history", balance)
	}
}

func TestCurrencyValuationOnlyAdjustmentHasKnownZeroNativeEffect(t *testing.T) {
	db, _, s, _, equity := setup(t)
	cash := measuredAccount(t, db, s, "1200", "AUD", 2, true)
	db.Call(t, s, "post_journal", measuredPost(cash, equity.ID, "acquisition", "100000", "65000"))
	q := measuredPost(cash, equity.ID, "valuation-adjustment", "0", "3000")
	db.Call(t, s, "post_journal", q)
	assertPosition(t, measuredBalance(t, db, s, cash.ID), "100000", "68000")
}

func carryingPost(t *testing.T, db *testutil.Database, s ledger.Scope, cash ledger.Account, other, key, quantity, book string) ledger.PostJournal {
	t.Helper()
	before := measuredBalance(t, db, s, cash.ID)
	q := measuredPost(cash, other, key, quantity, book)
	q.Lines[0].Side, q.Lines[1].Side = "credit", "debit"
	q.Lines[0].Valuation = &ledger.Valuation{Basis: "historical_carrying", Policy: "moving-average-cash-v1", Carrying: &ledger.CarryingAllocation{
		Policy: "moving-average-cash-v1", AccountID: cash.ID, AccountRevision: *cash.CurrencyRevision, RoleRevision: *cash.RoleRevision, BeforeSequence: before.Native.ThroughSequence,
		BeforeNative: measuredMoney(cash.Currency, *cash.Scale, before.Native.DebitPositiveMinor), BeforeBook: measuredMoney("USD", 2, before.DebitPositiveMinor),
		ConsumedNative: measuredMoney(cash.Currency, *cash.Scale, quantity), ConsumedBook: measuredMoney("USD", 2, book)}}
	return q
}
func TestCurrencyCarryingAllocationsSurviveBackdatedAcquisitions(t *testing.T) {
	db, book, s, _, equity := setup(t)
	cash := measuredAccount(t, db, s, "1200", "EUR", 2, true)
	for i, amount := range []string{"11000", "12000", "13000"} {
		q := measuredPost(cash, equity.ID, fmt.Sprintf("acquisition-%d", i), "10000", amount)
		db.Call(t, s, "post_journal", q)
	}
	q := carryingPost(t, db, s, cash, equity.ID, "disposal", "10000", "12000")
	first := db.Call(t, s, "post_journal", q)
	var accepted ledger.Entry
	json.Unmarshal(first, &accepted)
	later := measuredPost(cash, equity.ID, "backdated-acquisition", "10000", "10000")
	later.EconomicDate, later.AccountingDate = "2025-12-01", "2025-12-01"
	db.Call(t, s, "post_journal", later)
	assertPosition(t, measuredBalance(t, db, s, cash.ID), "30000", "34000")
	if !bytes.Equal(first, db.Call(t, s, "post_journal", q)) || !bytes.Equal(first, db.Call(t, s, "get_journal", ledger.EntryQuery{EntryID: accepted.ID})) {
		t.Fatal("accepted allocation changed")
	}
	if _, err := db.Owner.Verify(db.Context, book.ID); err != nil {
		t.Fatal("accepted allocation no longer verifies", err)
	}
	snapshot, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	target := testutil.New(t)
	if err = target.Owner.Restore(target.Context, snapshot); err != nil {
		t.Fatal("restore reinterpreted accepted allocation", err)
	}
}
func TestCurrencyCarryingResidualAndRepeatedConsumption(t *testing.T) {
	db, _, s, _, equity := setup(t)
	cash := measuredAccount(t, db, s, "1200", "JPY", 0, true)
	db.Call(t, s, "post_journal", measuredPost(cash, equity.ID, "position", "3", "1"))
	q := carryingPost(t, db, s, cash, equity.ID, "repeated", "1", "0")
	q.Lines = append(q.Lines, q.Lines[0], q.Lines[1], q.Lines[0], q.Lines[1])
	rejectMeasured(t, db, s, q)
	for i, amount := range []string{"0", "0", "1"} {
		q := carryingPost(t, db, s, cash, equity.ID, fmt.Sprintf("consume-%d", i), "1", amount)
		db.Call(t, s, "post_journal", q)
	}
	assertPosition(t, measuredBalance(t, db, s, cash.ID), "0", "0")
}
func TestCurrencyIncompleteNativePositionCannotAllocateCarryingValue(t *testing.T) {
	db, _, s, _, equity := setup(t)
	cash := measuredAccount(t, db, s, "1200", "AUD", 2, false)
	db.Call(t, s, "post_journal", measuredPost(cash, equity.ID, "partial-history", "100000", "65000"))
	balance := measuredBalance(t, db, s, cash.ID)
	if balance.Native == nil || balance.Native.Status != "incomplete" || balance.Native.DebitPositiveMinor != "" {
		t.Fatal("incomplete native position became asserted total", balance)
	}
	q := measuredPost(cash, equity.ID, "unsupported-carrying", "10000", "6500")
	q.Lines[0].Side, q.Lines[1].Side = "credit", "debit"
	q.Lines[0].Valuation = &ledger.Valuation{Basis: "historical_carrying", Policy: "moving-average-cash-v1", Carrying: &ledger.CarryingAllocation{
		Policy: "moving-average-cash-v1", AccountID: cash.ID, AccountRevision: 1, RoleRevision: 1, BeforeSequence: balance.Native.ThroughSequence,
		BeforeNative: measuredMoney("AUD", 2, "100000"), BeforeBook: measuredMoney("USD", 2, "65000"), ConsumedNative: measuredMoney("AUD", 2, "10000"), ConsumedBook: measuredMoney("USD", 2, "6500")}}
	rejectMeasured(t, db, s, q)
}
