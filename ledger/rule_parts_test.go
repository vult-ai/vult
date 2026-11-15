package ledger_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// A rule splits a loan payment into interest, worked out from the loan's
// balance in the Book, and principal, the rest; and a deposit into sales
// and the tax inside it. The Book's balance needn't match the lender's: the
// entry stays balanced and its reason shows the arithmetic, and the owner
// corrects any difference with an ordinary entry.
func TestRulesSplitLoanPaymentsAndTaxIncluded(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	_, _, byCode := chartBookAccounts(t, f)
	loans, interest, sales, tax, savings := byCode["2300"], byCode["7000"], byCode["4100"], byCode["2400"], byCode["1120"]
	// The loan as the Book knows it: a few dollars off the lender's figure.
	f.db.Call(t, f.s, "post_journal", ledger.PostJournal{RequestKey: "loan", EconomicDate: "2026-01-01", AccountingDate: "2026-01-01", Description: "Loan received",
		Lines: []ledger.PostLine{{AccountID: f.checking.ID, Side: "debit", AmountMinor: "12248000"}, {AccountID: loans.ID, Side: "credit", AmountMinor: "12248000"}}})
	payment := func(id, date string, amount int64, party string) ledger.BankSyncTransaction {
		return ledger.BankSyncTransaction{ProviderAccountID: "acc-checking", ProviderTransactionID: id, Date: date, AmountMinor: amount,
			Currency: f.book.Currency, Scale: f.book.Scale, Description: strings.ToUpper(party) + " " + id, Counterparty: party}
	}
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{
		payment("p1", "2026-02-01", -150000, "Example Lender"), payment("p2", "2026-03-01", -150000, "Example Lender"),
		payment("p3", "2026-04-01", -150000, "Example Lender"), payment("p4", "2026-05-01", -150000, "Example Lender"),
		payment("d1", "2026-02-02", 108250, "Example Shop Sales"), payment("d2", "2026-02-09", 54125, "Example Shop Sales"),
	}, nil)
	page := f.inbox(t, f.s, "all")
	find := func(d string) string { return byDescription(page, d).ID }
	// The owner's first decisions, then rules from them.
	f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "p1", TransactionID: find("EXAMPLE LENDER p1"), Description: "Loan payment", OtherAccountID: loans.ID})
	f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "d1", TransactionID: find("EXAMPLE SHOP SALES d1"), Description: "Shop sales", OtherAccountID: sales.ID})
	loanRule, err := f.owner.CreateRecordingRule(ctx, f.book.ID, f.actor, ledger.RuleFromActivity{TransactionID: find("EXAMPLE LENDER p1"), Description: "Loan payment"})
	succeeded(t, err)
	salesRule, err := f.owner.CreateRecordingRule(ctx, f.book.ID, f.actor, ledger.RuleFromActivity{TransactionID: find("EXAMPLE SHOP SALES d1"), MinMinor: "100", MaxMinor: "1000000"})
	succeeded(t, err)
	// Unnamed, a rule takes the counterparty as the bank wrote it; a
	// recorded item names the account its entry went to.
	if salesRule.Description != "Example Shop Sales" {
		t.Fatalf("rule named %q", salesRule.Description)
	}
	if link := byDescription(f.inbox(t, f.s, "all"), "EXAMPLE LENDER p1").Link; link == nil || link.OtherAccountName != loans.Name {
		t.Fatalf("link = %+v", link)
	}

	// Parts are classified by account type; interest needs a loan.
	_, err = f.owner.SetRuleParts(ctx, f.book.ID, f.actor, loanRule.ID, []ledger.RulePart{{AccountID: savings.ID, Kind: "fixed", AmountMinor: "100"}})
	bankRefused(t, err, "never goes to cash or a card")
	_, err = f.owner.SetRuleParts(ctx, f.book.ID, f.actor, salesRule.ID, []ledger.RulePart{{AccountID: interest.ID, Kind: "interest", Numerator: 6, Denominator: 100, Basis: "monthly"}})
	bankRefused(t, err, "money in to income, debts or equity")
	loanRule, err = f.owner.SetRuleParts(ctx, f.book.ID, f.actor, loanRule.ID, []ledger.RulePart{{AccountID: interest.ID, Kind: "interest", Numerator: 6, Denominator: 100, Basis: "monthly"}})
	succeeded(t, err)
	_, err = f.owner.SetRuleParts(ctx, f.book.ID, f.actor, salesRule.ID, []ledger.RulePart{{AccountID: tax.ID, Kind: "ratio", Numerator: 825, Denominator: 10825}})
	succeeded(t, err)

	// A granted agent records by the split rules.
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	succeeded(t, f.owner.GrantRuleRecording(ctx, f.book.ID, f.actor, agent.ConnectionID, true))
	var rules struct {
		Rules []ledger.RecordingRule `json:"rules"`
	}
	json.Unmarshal(f.db.Call(t, agent, "list_recording_rules", map[string]string{}), &rules)
	if len(rules.Rules) != 2 || len(rules.Rules[0].Parts)+len(rules.Rules[1].Parts) != 2 {
		t.Fatalf("rules = %+v", rules.Rules)
	}
	record := func(key, item, rule string) ledger.BankLinkResult {
		t.Helper()
		out, err := f.db.Store.Call(ctx, agent, "record_by_rule", value(ledger.RecordByRule{RequestKey: key, TransactionID: find(item), RuleID: rule}))
		succeeded(t, err)
		var v ledger.BankLinkResult
		json.Unmarshal(out, &v)
		return v
	}
	amounts := func(r ledger.BankLinkResult) map[string]string {
		t.Helper()
		out := map[string]string{}
		var debit, credit int64
		for _, l := range r.Entry.Lines {
			out[l.AccountID] = l.Side + " " + l.AmountMinor
			n, _ := json.Number(l.AmountMinor).Int64()
			if l.Side == "debit" {
				debit += n
			} else {
				credit += n
			}
		}
		if debit != credit {
			t.Fatal("unbalanced", r.Entry.Lines)
		}
		return out
	}
	// Monthly: 6% a year on the Book's 120,980.00 is 604.90.
	got := record("p2", "EXAMPLE LENDER p2", loanRule.ID)
	if a := amounts(got); a[interest.ID] != "debit 60490" || a[loans.ID] != "debit 89510" || a[f.checking.ID] != "credit 150000" {
		t.Fatalf("monthly split = %v", a)
	}
	if reason := got.Links[0].Reason; !strings.Contains(reason, "6% a year on 120980.00 for a month") || !strings.Contains(reason, "895.10 (the rest)") {
		t.Fatalf("reason = %q", reason)
	}
	// Daily: 31 days since the last entry, over 365.
	_, err = f.owner.SetRuleParts(ctx, f.book.ID, f.actor, loanRule.ID, []ledger.RulePart{{AccountID: interest.ID, Kind: "interest", Numerator: 6, Denominator: 100, Basis: "actual_365"}})
	succeeded(t, err)
	got = record("p3", "EXAMPLE LENDER p3", loanRule.ID)
	if a := amounts(got); a[interest.ID] != "debit 61194" || a[loans.ID] != "debit 88806" {
		t.Fatalf("daily split = %v", a)
	}
	if !strings.Contains(got.Links[0].Reason, "for 31 days of 365") {
		t.Fatalf("reason = %q", got.Links[0].Reason)
	}
	// Tax included: 825/10825 of 541.25 is 41.25.
	if a := amounts(record("d2", "EXAMPLE SHOP SALES d2", salesRule.ID)); a[tax.ID] != "credit 4125" || a[sales.ID] != "credit 50000" || a[f.checking.ID] != "debit 54125" {
		t.Fatalf("tax split = %v", a)
	}
	// Parts that take the whole item are refused when saved.
	_, err = f.owner.SetRuleParts(ctx, f.book.ID, f.actor, loanRule.ID, []ledger.RulePart{{AccountID: interest.ID, Kind: "fixed", AmountMinor: "150000"}})
	bankRefused(t, err, "leaves nothing for the rest")
	// An interest part can still come to the whole payment on a large
	// balance; recording refuses that item and leaves it for the owner.
	_, err = f.owner.SetRuleParts(ctx, f.book.ID, f.actor, loanRule.ID, []ledger.RulePart{{AccountID: interest.ID, Kind: "interest", Numerator: 99, Denominator: 100, Basis: "monthly"}})
	succeeded(t, err)
	_, err = f.db.Store.Call(ctx, agent, "record_by_rule", value(ledger.RecordByRule{RequestKey: "p4", TransactionID: find("EXAMPLE LENDER p4"), RuleID: loanRule.ID}))
	bankRefused(t, err, "or more; record it as a proposal")
	// Only the owner changes parts.
	tx, err := f.db.Runtime.Begin(ctx)
	succeeded(t, err)
	defer tx.Rollback(ctx)
	tx.Exec(ctx, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`,
		agent.TenantID, agent.BookID, agent.ActorID, agent.ConnectionID)
	if _, err := tx.Exec(ctx, "UPDATE public.recording_rule_parts SET replaced_at=clock_timestamp() WHERE rule_id=$1", loanRule.ID); err == nil {
		var current int
		f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.recording_rule_parts WHERE rule_id=$1 AND replaced_at IS NULL", loanRule.ID).Scan(&current)
		if current != 1 {
			t.Fatal("an agent changed a rule's parts")
		}
	}
}

func chartBookAccounts(t *testing.T, f *bankFixture) (ledger.Book, ledger.Scope, map[string]ledger.Account) {
	t.Helper()
	byCode := map[string]ledger.Account{}
	for _, a := range listedAccounts(t, f.db, f.s) {
		byCode[a.Code] = a
	}
	return f.book, f.s, byCode
}
