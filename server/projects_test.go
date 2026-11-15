package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// A personal Book labels its spending by project: a charge is recorded and
// labelled in one step, an entry is split across two projects, a rule
// labels a counterparty's charges in its date range, and the report says
// what each project cost and what is in no project. Labels never change an
// amount, and a backup keeps them.
func TestProjectsLabelSpendingWithoutChangingIt(t *testing.T) {
	lt := newLinkedTenant(t)
	kit := lt.books["kit"].ID
	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	software := lt.account("kit", "6100", "expense", "", "Software")
	ads := lt.account("kit", "6200", "expense", "", "Advertising")
	lt.bank("kit", "card", card,
		ledger.BankSyncTransaction{ProviderTransactionID: "c1", Date: "2025-03-02", AmountMinor: 2000, Description: "EXAMPLE CLOUD MAR", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c2", Date: "2025-04-02", AmountMinor: 9000, Description: "EXAMPLE ADS", Counterparty: "Example Ads"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c3", Date: "2026-02-02", AmountMinor: 2500, Description: "EXAMPLE CLOUD FEB", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c4", Date: "2026-06-02", AmountMinor: 2600, Description: "EXAMPLE CLOUD JUN", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c5", Date: "2026-06-05", AmountMinor: 4000, Description: "EXAMPLE SHOP", Counterparty: "Example Shop"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c6", Date: "2026-11-01", AmountMinor: 2700, Description: "EXAMPLE CLOUD NOV", Counterparty: "Example Cloud"},
		ledger.BankSyncTransaction{ProviderTransactionID: "c7", Date: "2026-11-02", AmountMinor: 9100, Description: "EXAMPLE ADS NOV", Counterparty: "Example Ads"})
	post := func(path string, v any) (int, []byte) {
		t.Helper()
		body, _ := json.Marshal(v)
		w := bankCall(t, lt.s, lt.f.session, http.MethodPost, path, string(body))
		return w.Code, w.Body.Bytes()
	}
	// The Book says projects are available.
	w := bankCall(t, lt.s, lt.f.session, http.MethodGet, booksPath, "")
	if !strings.Contains(w.Body.String(), `"projects":true`) {
		t.Fatal("the Book read doesn't offer projects", w.Body.String())
	}
	project := func(key, name string) ledger.Project {
		t.Helper()
		code, body := post(projectsPath, map[string]any{"book_id": kit, "request_key": key, "name": name})
		var p ledger.Project
		if code != http.StatusOK || json.Unmarshal(body, &p) != nil || p.Name != name {
			t.Fatal("project", code, string(body))
		}
		return p
	}
	app, shop := project("app", "Side app"), project("shop", "Example shop idea")
	if again := project("app", "Side app"); again.ID != app.ID {
		t.Fatal("the same request made a second project")
	}
	if code, body := post(projectsPath, map[string]any{"book_id": kit, "request_key": "dup", "name": "side app"}); code != http.StatusUnprocessableEntity || !strings.Contains(string(body), "already has a project") {
		t.Fatal("a second project of the same name", code, string(body))
	}

	before := lt.balance("kit", "6100")
	// A charge recorded and labelled in one step.
	code, body := post(projectsLabelPath, map[string]any{"book_id": kit, "request_key": "c1", "bank_item_id": lt.item("kit", "EXAMPLE ADS").ID, "account_id": ads.ID, "project_id": app.ID})
	var labelled struct {
		EntryID string `json:"entry_id"`
	}
	if code != http.StatusOK || json.Unmarshal(body, &labelled) != nil || labelled.EntryID == "" {
		t.Fatal("label a charge", code, string(body))
	}
	// A rule labels Example Cloud as Side app from 2026 on; an earlier charge
	// stays unlabelled.
	if code, body := post(projectsRulesPath, map[string]any{"book_id": kit, "project_id": app.ID, "counterparty": "Example Cloud", "starts_on": "2026-01-01"}); code != http.StatusOK {
		t.Fatal("rule", code, string(body))
	}
	// Recording the cloud charges through the ordinary path applies the rule.
	for _, d := range []string{"EXAMPLE CLOUD MAR", "EXAMPLE CLOUD FEB", "EXAMPLE CLOUD JUN"} {
		lt.f.db.Call(t, lt.scope(lt.writers["kit"]), "record_bank_activity", ledger.RecordBankActivity{RequestKey: "rec-" + d, TransactionID: lt.item("kit", d).ID, Description: "Example Cloud", OtherAccountID: software.ID})
	}
	// The shop charge is split: 3,000 to the shop idea, 1,000 to the app.
	shopItem := lt.item("kit", "EXAMPLE SHOP")
	lt.f.db.Call(t, lt.scope(lt.writers["kit"]), "record_bank_activity", ledger.RecordBankActivity{RequestKey: "rec-shop", TransactionID: shopItem.ID, Description: "Example Shop", OtherAccountID: software.ID})
	var shopEntry string
	if err := lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT entry_id::text FROM public.bank_transaction_links WHERE transaction_id=$1", shopItem.ID).Scan(&shopEntry); err != nil {
		t.Fatal(err)
	}
	if code, body := post(projectsLabelPath, map[string]any{"book_id": kit, "entry_id": shopEntry, "splits": []map[string]string{{"project_id": shop.ID, "amount_minor": "3000"}, {"project_id": app.ID, "amount_minor": "3001"}}}); code != http.StatusUnprocessableEntity {
		t.Fatal("parts over the line", code, string(body))
	}
	if code, body := post(projectsLabelPath, map[string]any{"book_id": kit, "entry_id": shopEntry, "splits": []map[string]string{{"project_id": shop.ID, "amount_minor": "3000"}, {"project_id": app.ID, "amount_minor": "1000"}}}); code != http.StatusOK {
		t.Fatal("split", code, string(body))
	}
	// The entry's read says its projects.
	if r := bankCall(t, lt.s, lt.f.session, http.MethodGet, "/api/v1/transactions/"+shopEntry+"?book_id="+kit, ""); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"project_labels"`) {
		t.Fatal("the entry read has no project labels", r.Body.String())
	}
	if lt.balance("kit", "6100") != before+2000+2500+2600+4000 {
		t.Fatal("labels changed an amount", lt.balance("kit", "6100"))
	}

	// The report: Side app spent 9,000 (ads) in 2025, and 2,500 + 2,600 +
	// 1,000 on software in 2026; the shop idea 3,000 in 2026. In no project:
	// 2,000 in 2025 (the cloud charge before the rule's range).
	w = bankCall(t, lt.s, lt.f.session, http.MethodGet, projectsReportPath+"?book_id="+kit, "")
	var report ledger.ProjectReport
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &report) != nil {
		t.Fatal("report", w.Code, w.Body.String())
	}
	totals := map[string]string{}
	for _, p := range report.Projects {
		for _, y := range p.ByYear {
			totals[p.Name+"/"+itoa(y.Year)] = y.TotalMinor
		}
	}
	want := map[string]string{"Side app/2025": "9000", "Side app/2026": "6100", "Example shop idea/2026": "3000"}
	for k, v := range want {
		if totals[k] != v {
			t.Fatal("report totals", totals, w.Body.String())
		}
	}
	if len(report.Unlabelled) != 1 || report.Unlabelled[0].Year != 2025 || report.Unlabelled[0].AmountMinor != "2000" {
		t.Fatal("unlabelled", report.Unlabelled)
	}
	if csv := bankCall(t, lt.s, lt.f.session, http.MethodGet, projectsReportCSV+"?book_id="+kit, ""); csv.Code != http.StatusOK || !strings.Contains(csv.Body.String(), "Side app,2025,Advertising,9000") {
		t.Fatal("csv", csv.Code, csv.Body.String())
	}

	// Bank items say their project, or the one they likely belong to.
	if p := lt.item("kit", "EXAMPLE ADS").Project; p == nil || p.ID != app.ID || p.Name != "Side app" {
		t.Fatal("a labelled item's project", p)
	}
	if p := lt.item("kit", "EXAMPLE CLOUD NOV").SuggestedProject; p == nil || p.ID != app.ID || p.Why != "rule" {
		t.Fatal("a rule's suggestion", p)
	}
	if p := lt.item("kit", "EXAMPLE ADS NOV").SuggestedProject; p == nil || p.ID != app.ID || p.Why != "history" || p.AccountID != ads.ID {
		t.Fatal("the counterparty's history", p)
	}
	// The Book's own record says where the counterparty's items went before.
	if before := lt.item("kit", "EXAMPLE ADS NOV").RecordedBefore; len(before) != 1 || before[0].AccountID != ads.ID || before[0].Times < 1 || before[0].LargestMinor == "" || before[0].LargestMinor == "0" {
		t.Fatal("an unrecorded item says where its kind was recorded before", before)
	}
	if lt.item("kit", "EXAMPLE SHOP").Project != nil {
		t.Fatal("a split item has no single project")
	}

	// An agent reads and labels through its tools.
	scope := lt.scope(lt.writers["kit"])
	var listed struct{ Projects []ledger.Project }
	if json.Unmarshal(lt.f.db.Call(t, scope, "list_projects", struct{}{}), &listed) != nil || len(listed.Projects) != 2 {
		t.Fatal("list_projects")
	}
	lt.f.db.Call(t, scope, "label_project", map[string]any{"request_key": "agent-unlabel", "entry_id": shopEntry, "project_id": nil})
	var n int
	lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT count(*) FROM public.project_labels WHERE entry_id=$1", shopEntry).Scan(&n)
	if n != 0 {
		t.Fatal("unlabel", n)
	}

	// Turning Side app into a company formed on 2026-03-01 proposes the
	// costs before it: the ads and the February cloud charge.
	code, body = post(projectsPath+"/"+app.ID+"/promote/preview", map[string]any{"book_id": kit, "formation_date": "2026-03-01"})
	var preview struct{ Costs []ledger.PromotionCost }
	if code != http.StatusOK || json.Unmarshal(body, &preview) != nil || len(preview.Costs) != 3 {
		t.Fatal("promotion preview", code, string(body))
	}
	got := []string{}
	for _, c := range preview.Costs {
		got = append(got, c.Date+"/"+c.AmountMinor+"/"+map[bool]string{true: "before", false: "after"}[c.BeforeFormation])
	}
	if strings.Join(got, ",") != "2025-04-02/9000/before,2026-02-02/2500/before,2026-06-02/2600/after" {
		t.Fatal("promotion costs", got)
	}

	// Side app becomes a C corporation formed on 2026-03-01, carrying the
	// two costs before it. Nothing is doubled: Kit's spending moves to what
	// the company owes, and the company records it as start-up costs.
	reportTotals := func() (map[string]string, string) {
		t.Helper()
		w := bankCall(t, lt.s, lt.f.session, http.MethodGet, projectsReportPath+"?book_id="+kit, "")
		var r ledger.ProjectReport
		if json.Unmarshal(w.Body.Bytes(), &r) != nil {
			t.Fatal(w.Body.String())
		}
		out, carried := map[string]string{}, ""
		for _, p := range r.Projects {
			for _, y := range p.ByYear {
				out[p.Name+"/"+itoa(y.Year)] = y.TotalMinor
			}
			if p.ID == app.ID {
				carried = p.CarriedMinor
			}
		}
		for _, u := range r.Unlabelled {
			out["none/"+itoa(u.Year)] = u.AmountMinor
		}
		return out, carried
	}
	totalsBefore, _ := reportTotals()
	adsBefore, softwareBefore := lt.balance("kit", "6200"), lt.balance("kit", "6100")
	promote := map[string]any{"book_id": kit, "request_key": "app-co", "name": "Example App Inc.", "taxed_as": ledger.TaxedCCorporation, "formation_date": "2026-03-01",
		"entry_ids": []string{preview.Costs[0].EntryID, preview.Costs[1].EntryID}}
	code, body = post(projectsPath+"/"+app.ID+"/promote", promote)
	var promoted ledger.PromotionResult
	if code != http.StatusOK || json.Unmarshal(body, &promoted) != nil || promoted.BookID == "" || len(promoted.Pairs) != 2 {
		t.Fatal("promote", code, string(body))
	}
	if code, body := post(projectsPath+"/"+app.ID+"/promote", promote); code != http.StatusOK || !strings.Contains(string(body), promoted.BookID) {
		t.Fatal("asked again", code, string(body))
	}
	companyTotals := func() (startup, owed int64) {
		lt.f.db.Admin.QueryRow(lt.f.db.Context, `SELECT coalesce(sum(CASE WHEN l.side='debit' THEN l.amount_minor ELSE -l.amount_minor END) FILTER (WHERE a.class='expense'),0),
 coalesce(sum(CASE WHEN l.side='credit' THEN l.amount_minor ELSE -l.amount_minor END) FILTER (WHERE a.class='liability'),0)
 FROM public.lines l JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id WHERE l.book_id=$1`, promoted.BookID).Scan(&startup, &owed)
		return
	}
	startup, owed := companyTotals()
	if lt.balance("kit", "6200") != adsBefore-9000 || lt.balance("kit", "6100") != softwareBefore-2500 || startup != 11500 || owed != 11500 {
		t.Fatal("carried", lt.balance("kit", "6200"), lt.balance("kit", "6100"), startup, owed)
	}
	totalsAfter, carried := reportTotals()
	if strings.Join(sortedKV(totalsAfter), ",") != strings.Join(sortedKV(totalsBefore), ",") || carried != "11500" {
		t.Fatal("the report changed", totalsBefore, totalsAfter, carried)
	}
	// Who owes whom: the company owes Kit both costs, oldest first.
	w = bankCall(t, lt.s, lt.f.session, http.MethodGet, linkedBalancesPath, "")
	if !strings.Contains(w.Body.String(), `"amount_minor":"11500"`) {
		t.Fatal("debts", w.Body.String())
	}
	// Carried costs aren't offered again.
	code, body = post(projectsPath+"/"+app.ID+"/promote/preview", map[string]any{"book_id": kit, "formation_date": "2026-03-01"})
	if code != http.StatusOK || strings.Count(string(body), "entry_id") != 1 {
		t.Fatal("preview after", code, string(body))
	}

	// Undo, as a group: both Books are as before.
	if code, body := post(projectsPath+"/promote/undo", map[string]any{"book_id": kit, "request_key": "app-co"}); code != http.StatusOK {
		t.Fatal("undo", code, string(body))
	}
	startup, owed = companyTotals()
	if lt.balance("kit", "6200") != adsBefore || lt.balance("kit", "6100") != softwareBefore || startup != 0 || owed != 0 {
		t.Fatal("after undo", lt.balance("kit", "6200"), lt.balance("kit", "6100"), startup, owed)
	}
	totalsUndone, carried := reportTotals()
	if strings.Join(sortedKV(totalsUndone), ",") != strings.Join(sortedKV(totalsBefore), ",") || carried != "0" {
		t.Fatal("the report after undo", totalsBefore, totalsUndone, carried)
	}
}

func sortedKV(m map[string]string) []string {
	out := []string{}
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
