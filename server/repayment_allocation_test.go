package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// What a payment repaid is stored when it's recorded and never moves: a
// settlement that named no costs, an opening balance or older charge added
// later, and a plain payment recorded straight on what is owed. An older
// cost added after them opens on its own.
func TestRepaymentsKeepWhatTheyRepaid(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitChecking := lt.account("kit", "1000", "asset", "cash", "Kit checking")
	kitFromStudio := lt.account("kit", "1310", "asset", "receivable", "Owed by the studio")
	studioChecking := lt.account("studio", "1000", "asset", "cash", "Studio checking")
	tools := lt.account("studio", "6200", "expense", "", "Design tools")
	studioToKit := lt.account("studio", "2300", "liability", "payable", "Owed to Kit Blanche")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-01", AmountMinor: 5000, Description: "EXAMPLE DESK", Counterparty: "Example Desk"},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-03", AmountMinor: 4000, Description: "EXAMPLE LAMP", Counterparty: "Example Lamp"})
	lt.bank("kit", "cash", kitChecking,
		ledger.BankSyncTransaction{ProviderTransactionID: "k3", Date: "2026-11-10", AmountMinor: 6000, Description: "FROM THE STUDIO 1", Counterparty: "Kit Blanche Studio"})
	lt.bank("studio", "cash", studioChecking,
		ledger.BankSyncTransaction{ProviderTransactionID: "s1", Date: "2026-11-10", AmountMinor: -6000, Description: "TO KIT 1", Counterparty: "Kit Blanche"},
		ledger.BankSyncTransaction{ProviderTransactionID: "s2", Date: "2026-11-12", AmountMinor: -1000, Description: "TO KIT PLAIN", Counterparty: "Kit Blanche"})
	if w := lt.link("kit", "studio", "owed", kitFromStudio, studioToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	pair := func(d string) {
		t.Helper()
		body, _ := json.Marshal(ledger.OwnerPair{BankItemID: lt.item("kit", d).ID, BeneficiaryBookID: lt.books["studio"].ID, BeneficiaryAccountID: tools.ID, Description: d})
		if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, pairsPath, string(body)); w.Code != http.StatusOK {
			t.Fatal("pair", d, w.Code, w.Body.String())
		}
	}
	pair("EXAMPLE DESK")
	pair("EXAMPLE LAMP")
	body, _ := json.Marshal(ledger.OwnerSettlement{DebtorItemID: lt.item("studio", "TO KIT 1").ID, CreditorItemID: lt.item("kit", "FROM THE STUDIO 1").ID})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, settlementsPath, string(body)); w.Code != http.StatusOK {
		t.Fatal("settle", w.Code, w.Body.String())
	}
	ctx := lt.f.db.Context
	state := func() (string, string) {
		t.Helper()
		var repays, open string
		if err := lt.f.db.Admin.QueryRow(ctx, `SELECT coalesce(string_agg(e.description||'='||r.amount_minor::text, ', ' ORDER BY e.accounting_date),'')
 FROM public.allocations r JOIN public.entries e ON e.tenant_id=r.tenant_id AND e.book_id=r.obligation_book_id AND e.id=r.obligation_entry_id WHERE r.tenant_id=$1 AND r.removed_at IS NULL`, lt.f.tenant).Scan(&repays); err != nil {
			t.Fatal(err)
		}
		if err := lt.f.db.Admin.QueryRow(ctx, `SELECT string_agg(o.description||' open='||o.open||' repaid='||o.repaid, ', ' ORDER BY o.accounting_date)
 FROM public.open_costs($1,$2,$3,$4,NULL) o`, lt.f.tenant, lt.books["kit"].ID, lt.books["studio"].ID, studioToKit.ID).Scan(&open); err != nil {
			t.Fatal(err)
		}
		return repays, open
	}
	repaysBefore, openBefore := state()
	if repaysBefore != "EXAMPLE DESK=5000, EXAMPLE LAMP=1000" || openBefore != "EXAMPLE DESK open=0 repaid=5000, EXAMPLE LAMP open=3000 repaid=1000" {
		t.Fatal("the settlement", repaysBefore, openBefore)
	}
	// An older card charge, from an older statement, dated before the
	// repayment and before every other cost.
	oldCard := lt.account("kit", "2110", "liability", "card", "Older card")
	lt.bank("kit", "card", oldCard,
		ledger.BankSyncTransaction{ProviderTransactionID: "k0", Date: "2026-10-20", AmountMinor: 3000, Description: "EXAMPLE CHAIR", Counterparty: "Example Chair"})
	pair("EXAMPLE CHAIR")
	repaysAfter, openAfter := state()
	if repaysAfter != repaysBefore {
		t.Error("the earlier repayment's allocation changed")
	}
	if openAfter != "EXAMPLE CHAIR open=3000 repaid=0, "+openBefore {
		t.Error("open amounts shifted")
	}
	// An opening balance owed from before the bank history, dated earlier.
	lt.account("kit", "3900", "equity", "opening_balance", "Opening balances")
	lt.account("studio", "3900", "equity", "opening_balance", "Opening balances")
	ob, _ := json.Marshal(ledger.OwnerOpeningBalance{DebtorBookID: lt.books["studio"].ID, CreditorBookID: lt.books["kit"].ID, AsOf: "2026-09-30", AmountMinor: "2000", Description: "Owed before"})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, openingBalancePath, string(ob)); w.Code != http.StatusOK {
		t.Fatal("opening", w.Code, w.Body.String())
	}
	repaysOpening, openOpening := state()
	if repaysOpening != repaysBefore || openOpening != "Owed before open=2000 repaid=0, "+openAfter {
		t.Error("an opening balance moved what was repaid", repaysOpening, openOpening)
	}
	// A payment to Kit recorded as a plain entry on what the studio owes,
	// not as a settlement: nothing stores which costs it repays.
	plain, err := lt.prepare("studio", "st-plain", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "st-plain", TransactionID: lt.item("studio", "TO KIT PLAIN").ID,
		Description: "Paid Kit back", OtherAccountID: studioToKit.ID, Reason: "Repaying Kit"})
	if err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(lt.books["studio"].ID, plain, "approve-plain", 0)); w.Code != http.StatusOK {
		t.Fatal("approve", w.Code, w.Body.String())
	}
	repaysPlain, openPlain := state()
	if !strings.Contains(repaysPlain, "Owed before=1000") || !strings.HasPrefix(openPlain, "Owed before open=1000 repaid=1000, ") {
		t.Fatal("the plain payment repays the oldest open cost when it's recorded", repaysPlain, openPlain)
	}
	lt.bank("kit", "card", lt.account("kit", "2120", "liability", "card", "Oldest card"),
		ledger.BankSyncTransaction{ProviderTransactionID: "kx", Date: "2026-08-15", AmountMinor: 1500, Description: "EXAMPLE SHELF", Counterparty: "Example Shelf"})
	pair("EXAMPLE SHELF")
	repaysLast, openLast := state()
	if repaysLast != repaysPlain {
		t.Error("an older cost moved what earlier payments repaid", repaysPlain, repaysLast)
	}
	if openLast != "EXAMPLE SHELF open=1500 repaid=0, "+openPlain {
		t.Error("open amounts shifted", openLast)
	}
}
