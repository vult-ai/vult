package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// A corporation owed both ways repays its owner in part: unless the owner
// chooses, each payment goes to the oldest open costs first, the last one
// it reaches in part, and the next payment goes on from what's still open.
// The owner may choose other costs and amounts, adding up to the payment.
func TestARepaymentMayRepayPartOfACost(t *testing.T) {
	lt := newLinkedTenant(t)
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitChecking := lt.account("kit", "1000", "asset", "cash", "Kit checking")
	kitFromEightball := lt.account("kit", "1300", "asset", "receivable", "Owed by Eightball Inc.")
	kitOwes := lt.account("kit", "2300", "liability", "payable", "Owed to Eightball Inc.")
	checking := lt.account("eightball", "1100", "asset", "cash", "Checking")
	software := lt.account("eightball", "6100", "expense", "", "Software")
	eightballToKit := lt.account("eightball", "2300", "liability", "payable", "Owed to Kit Blanche")
	eightballFromKit := lt.account("eightball", "1300", "asset", "receivable", "Owed by Kit Blanche")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "c1", Date: "2026-10-01", AmountMinor: 100000, Description: "EXAMPLE LAPTOP", Counterparty: "Example Computers"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c2", Date: "2026-10-02", AmountMinor: 64900, Description: "EXAMPLE MONITOR", Counterparty: "Example Computers"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c3", Date: "2026-10-03", AmountMinor: 23500, Description: "EXAMPLE DESK", Counterparty: "Example Furniture"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c4", Date: "2026-10-04", AmountMinor: 9999, Description: "EXAMPLE SOFTWARE", Counterparty: "Example Software"})
	lt.bank("kit", "cash", kitChecking,
		ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-02", AmountMinor: 50000, Description: "FROM EIGHTBALL ONE", Counterparty: "Eightball Inc."},
		ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-09", AmountMinor: 60000, Description: "FROM EIGHTBALL TWO", Counterparty: "Eightball Inc."},
		ledger.BankSyncTransaction{ProviderTransactionID: "k3", Date: "2026-11-16", AmountMinor: 40000, Description: "FROM EIGHTBALL THREE", Counterparty: "Eightball Inc."})
	lt.bank("eightball", "cash", checking,
		ledger.BankSyncTransaction{ProviderTransactionID: "e1", Date: "2026-11-02", AmountMinor: -50000, Description: "TO KIT ONE", Counterparty: "Kit Blanche"},
		ledger.BankSyncTransaction{ProviderTransactionID: "e2", Date: "2026-11-09", AmountMinor: -60000, Description: "TO KIT TWO", Counterparty: "Kit Blanche"},
		ledger.BankSyncTransaction{ProviderTransactionID: "e3", Date: "2026-11-16", AmountMinor: -40000, Description: "TO KIT THREE", Counterparty: "Kit Blanche"})
	if w := lt.link("kit", "eightball", "owed", kitFromEightball, eightballToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := lt.link("eightball", "kit", "owed", eightballFromKit, kitOwes, 1); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	call := func(path string, v any) (int, []byte) {
		t.Helper()
		body, _ := json.Marshal(v)
		w := bankCall(t, lt.s, lt.f.session, http.MethodPost, path, string(body))
		return w.Code, w.Body.Bytes()
	}
	keys := map[string]string{}
	for _, d := range []string{"EXAMPLE LAPTOP", "EXAMPLE MONITOR", "EXAMPLE DESK", "EXAMPLE SOFTWARE"} {
		code, body := call(pairsPath, ledger.OwnerPair{BankItemID: lt.item("kit", d).ID, BeneficiaryBookID: lt.books["eightball"].ID, BeneficiaryAccountID: software.ID, Description: d})
		var pair ledger.OwnerPairResult
		if code != http.StatusOK || json.Unmarshal(body, &pair) != nil {
			t.Fatal("pair", d, code, string(body))
		}
		keys[d] = pair.PairKey
	}
	payment := func(n string) ledger.OwnerSettlement {
		return ledger.OwnerSettlement{DebtorItemID: lt.item("eightball", "TO KIT "+n).ID, CreditorItemID: lt.item("kit", "FROM EIGHTBALL "+n).ID}
	}
	open := func() map[string]string {
		t.Helper()
		w := bankCall(t, lt.s, lt.f.session, http.MethodGet, linkedBalancesPath, "")
		var all struct {
			Links []struct {
				OpenPairs []struct {
					PairKey   string `json:"pair_key"`
					OpenMinor string `json:"open_minor"`
					Repaid    bool   `json:"repaid"`
					Partly    bool   `json:"partly_repaid"`
					Amount    string `json:"amount_minor"`
				} `json:"open_pairs"`
			} `json:"links"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &all) != nil || len(all.Links) != 1 {
			t.Fatal("who owes whom", w.Code, w.Body.String())
		}
		out := map[string]string{}
		for _, p := range all.Links[0].OpenPairs {
			if p.Repaid != (p.OpenMinor == "0") || p.Partly != (p.OpenMinor != "0" && p.OpenMinor != p.Amount) {
				t.Fatal("repaid and open disagree", p)
			}
			out[p.PairKey] = p.OpenMinor
		}
		return out
	}
	expectOpen := func(when string, laptop, monitor, desk, software string) {
		t.Helper()
		got := open()
		want := map[string]string{keys["EXAMPLE LAPTOP"]: laptop, keys["EXAMPLE MONITOR"]: monitor, keys["EXAMPLE DESK"]: desk, keys["EXAMPLE SOFTWARE"]: software}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("%s: open %v, want %v", when, got, want)
			}
		}
	}
	expectOpen("before", "100000", "64900", "23500", "9999")

	// $500 against $1,000, $649, $235 and $99.99: the preview proposes $500
	// of the oldest, the laptop, which stays open for the other $500.
	code, body := call(settlementPreview, payment("ONE"))
	var preview ledger.SettlementPreview
	if code != http.StatusOK || json.Unmarshal(body, &preview) != nil || preview.RepayingMinor != "50000" || preview.ExcessMinor != "0" || len(preview.Costs) != 1 ||
		preview.Costs[0] != (ledger.SettlementCost{PairKey: keys["EXAMPLE LAPTOP"], AmountMinor: "50000", OpenMinor: "100000", LeftAfterMinor: "50000"}) {
		t.Fatal("preview", code, string(body))
	}
	expectOpen("after a preview", "100000", "64900", "23500", "9999")
	if code, body := call(settlementsPath, payment("ONE")); code != http.StatusOK {
		t.Fatal("partial repayment", code, string(body))
	}
	expectOpen("after $500", "50000", "64900", "23500", "9999")

	// The owner chooses instead: $400 of the monitor and $200 of the laptop.
	// Choices that don't add up to the payment, or ask more than is left of
	// a cost, are refused in plain words.
	two := payment("TWO")
	two.Repays = []ledger.Repay{{PairKey: keys["EXAMPLE MONITOR"], AmountMinor: "40000"}, {PairKey: keys["EXAMPLE LAPTOP"], AmountMinor: "10000"}}
	if code, body := call(settlementPreview, two); code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "add up to it exactly") || strings.Contains(string(body), "repays:") {
		t.Fatal("choices short of the payment", code, string(body))
	}
	two.Repays = []ledger.Repay{{PairKey: keys["EXAMPLE DESK"], AmountMinor: "60000"}}
	if code, body := call(settlementPreview, two); code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "at most what is left") {
		t.Fatal("more than is left of a cost", code, string(body))
	}
	two.Repays = []ledger.Repay{{PairKey: keys["EXAMPLE MONITOR"], AmountMinor: "40000"}, {PairKey: keys["EXAMPLE LAPTOP"], AmountMinor: "20000"}}
	if code, body := call(settlementsPath, two); code != http.StatusOK {
		t.Fatal("chosen repayment", code, string(body))
	}
	expectOpen("after the owner's choice", "30000", "24900", "23500", "9999")

	// The next payment, unnamed, goes on from what's open: the rest of the
	// laptop, then part of the monitor.
	if code, body := call(settlementsPath, payment("THREE")); code != http.StatusOK {
		t.Fatal("third repayment", code, string(body))
	}
	expectOpen("after the third", "0", "14900", "23500", "9999")
	var stored int
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT count(*) FROM public.allocations WHERE source='settlement' AND removed_at IS NULL").Scan(&stored); err != nil || stored != 5 {
		t.Fatal("repaid parts kept", stored, err)
	}
}
