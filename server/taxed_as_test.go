package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// How a Book is taxed is a Book setting: a business Book is a sole owner
// until the owner says otherwise, and a personal Book is always personal.
// An S corporation paying its owner more than it owes makes a shareholder
// distribution, and each cost it repays without a receipt is asked for.
func TestABookIsTaxedAsTheOwnerSays(t *testing.T) {
	lt := newLinkedTenant(t)
	read := func(name string) ledger.Book {
		t.Helper()
		var b ledger.Book
		if err := lt.f.sessions.WithOwner(lt.f.db.Context, lt.f.session, lt.f.tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			b, err = o.Book(lt.f.db.Context, lt.books[name].ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if s, k := read("studio"), read("kit"); s.TaxedAs != ledger.TaxedSoleOwner || s.TaxedAsSet || k.TaxedAs != ledger.TaxedPersonal || k.TaxedAsSet {
		t.Fatal("defaults", s.TaxedAs, s.TaxedAsSet, k.TaxedAs, k.TaxedAsSet)
	}
	// A Book nobody has classified exports as it did before Books were
	// taxed, so its journal checksum doesn't change with the release.
	if err := lt.f.sessions.WithOwner(lt.f.db.Context, lt.f.session, lt.f.tenant, false, func(o ledger.TenantAdmin) error {
		export, err := o.Export(lt.f.db.Context, lt.books["studio"].ID)
		if err != nil {
			return err
		}
		if data, _ := json.Marshal(export); strings.Contains(string(data), "taxed_as") {
			t.Fatal("an unclassified Book's export names how it is taxed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	save := func(name, key, taxed string) *http.Response {
		t.Helper()
		b := read(name)
		body, _ := json.Marshal(ledger.ReviseBookSettings{BookID: b.ID, RequestKey: key, Revision: *b.MetadataRevision, Name: b.Name, Founder: b.Founder, TaxedAs: &taxed})
		w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/settings", string(body))
		return w.Result()
	}
	if r := save("kit", "kit-s-corp", ledger.TaxedSCorporation); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatal("a personal Book taxed as a corporation", r.StatusCode)
	}
	if r := save("studio", "studio-s-corp", ledger.TaxedSCorporation); r.StatusCode != http.StatusOK {
		t.Fatal("taxed as an S corporation", r.StatusCode)
	}
	if s := read("studio"); s.TaxedAs != ledger.TaxedSCorporation || !s.TaxedAsSet {
		t.Fatal("after saving", s.TaxedAs, s.TaxedAsSet)
	}
	// The browser's Book list says it too.
	w := bankCall(t, lt.s, lt.f.session, http.MethodGet, booksPath, "")
	var listed struct{ Books []webBook }
	if json.Unmarshal(w.Body.Bytes(), &listed) != nil {
		t.Fatal(w.Body.String())
	}
	found := 0
	for _, b := range listed.Books {
		if b.ID == lt.books["studio"].ID && b.TaxedAs == ledger.TaxedSCorporation && b.TaxedAsSet ||
			b.ID == lt.books["kit"].ID && b.TaxedAs == ledger.TaxedPersonal && !b.TaxedAsSet {
			found++
		}
	}
	if found != 2 {
		t.Fatal("the Book list doesn't say how Books are taxed", w.Body.String())
	}
	// A new logo keeps it.
	logo, _ := exampleJPEG(t, 64, false)
	putOwnerLogo(lt.s, lt.f.session, lt.books["studio"].ID, logo)
	if s := read("studio"); s.TaxedAs != ledger.TaxedSCorporation || !s.TaxedAsSet {
		t.Fatal("after a new logo", s.TaxedAs, s.TaxedAsSet)
	}

	card := lt.account("kit", "2100", "liability", "card", "Rewards card")
	kitChecking := lt.account("kit", "1000", "asset", "cash", "Kit checking")
	kitFromStudio := lt.account("kit", "1310", "asset", "receivable", "Owed by the studio")
	kitInStudio := lt.account("kit", "1500", "asset", "investment", "Investment in the studio")
	studioChecking := lt.account("studio", "1000", "asset", "cash", "Studio checking")
	tools := lt.account("studio", "6200", "expense", "", "Design tools")
	studioToKit := lt.account("studio", "2300", "liability", "payable", "Owed to Kit Blanche")
	distributions := lt.account("studio", "3200", "equity", "", "Shareholder distributions")
	lt.bank("kit", "card", card, ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-02", AmountMinor: 5000, Description: "EXAMPLE DESK", Counterparty: "Example Desk"})
	lt.bank("kit", "cash", kitChecking, ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-12", AmountMinor: 8000, Description: "FROM THE STUDIO", Counterparty: "Kit Blanche Studio"})
	lt.bank("studio", "cash", studioChecking, ledger.BankSyncTransaction{ProviderTransactionID: "s1", Date: "2026-11-11", AmountMinor: -8000, Description: "TO KIT", Counterparty: "Kit Blanche"})
	if w := lt.link("kit", "studio", "owed", kitFromStudio, studioToKit, 0); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := lt.link("studio", "kit", "owner_equity", distributions, kitInStudio, 1); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	body, _ := json.Marshal(ledger.OwnerPair{BankItemID: lt.item("kit", "EXAMPLE DESK").ID, BeneficiaryBookID: lt.books["studio"].ID, BeneficiaryAccountID: tools.ID, Description: "Desk"})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, pairsPath, string(body)); w.Code != http.StatusOK {
		t.Fatal("desk", w.Code, w.Body.String())
	}
	body, _ = json.Marshal(ledger.OwnerSettlement{DebtorItemID: lt.item("studio", "TO KIT").ID, CreditorItemID: lt.item("kit", "FROM THE STUDIO").ID})
	var preview ledger.SettlementPreview
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, settlementPreview, string(body)); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &preview) != nil ||
		!preview.NamesExpected || preview.ExcessRecordedAs != "shareholder_distribution" {
		t.Fatal("preview", w.Code, w.Body.String())
	}
	var paid ledger.OwnerSettlementResult
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, settlementsPath, string(body)); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &paid) != nil ||
		paid.ExcessMinor != "3000" || paid.ExcessRecordedAs != "shareholder_distribution" {
		t.Fatal("settle", w.Code, w.Body.String())
	}
	var reminders int
	lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT count(*) FROM public.agent_loops WHERE tenant_id=$1 AND book_id=$2 AND status='open' AND text LIKE 'Add the receipt for%'",
		lt.f.tenant, lt.books["studio"].ID).Scan(&reminders)
	if reminders != 1 {
		t.Fatal("receipt reminders", reminders)
	}
	// The owner sees it in Needs you, and can mark it done.
	needsYou := func() []ledger.AgentLoop {
		t.Helper()
		w := bankCall(t, lt.s, lt.f.session, http.MethodGet, remindersPath+"?book_id="+lt.books["studio"].ID, "")
		var out struct{ Reminders []ledger.AgentLoop }
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("reminders", w.Code, w.Body.String())
		}
		return out.Reminders
	}
	open := needsYou()
	if len(open) != 1 || !strings.HasPrefix(open[0].Text, "Add the receipt for") {
		t.Fatal("reminders", open)
	}
	done, _ := json.Marshal(map[string]string{"book_id": lt.books["studio"].ID, "id": open[0].ID})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, remindersDonePath, string(done)); w.Code != http.StatusOK {
		t.Fatal("done", w.Code, w.Body.String())
	}
	if left := needsYou(); len(left) != 0 {
		t.Fatal("after done", left)
	}
	if !strings.Contains(paid.ExcessRecordedAs, "distribution") {
		t.Fatal(paid.ExcessRecordedAs)
	}
}

// Terms that don't fit how a business is taxed, and a partnership repaying
// a partner, are things for the owner to look at; neither refuses anything.
func TestHowABookIsTaxedAsksTheOwnerToLook(t *testing.T) {
	for taxed, want := range map[string]string{
		ledger.TaxedCCorporation: "to owed back",
		ledger.TaxedPartnership:  "Partnership repayments need a look",
	} {
		t.Run(taxed, func(t *testing.T) {
			lt := newLinkedTenant(t)
			b, err := func() (ledger.Book, error) {
				var out ledger.Book
				err := lt.f.sessions.WithOwner(lt.f.db.Context, lt.f.session, lt.f.tenant, false, func(o ledger.TenantAdmin) error {
					var err error
					out, err = o.Book(lt.f.db.Context, lt.books["studio"].ID)
					return err
				})
				return out, err
			}()
			if err != nil {
				t.Fatal(err)
			}
			card := lt.account("kit", "2100", "liability", "card", "Rewards card")
			kitChecking := lt.account("kit", "1000", "asset", "cash", "Kit checking")
			kitFromStudio := lt.account("kit", "1310", "asset", "receivable", "Owed by the studio")
			kitInStudio := lt.account("kit", "1500", "asset", "investment", "Investment in the studio")
			studioChecking := lt.account("studio", "1000", "asset", "cash", "Studio checking")
			tools := lt.account("studio", "6200", "expense", "", "Design tools")
			studioToKit := lt.account("studio", "2300", "liability", "payable", "Owed to Kit Blanche")
			draws := lt.account("studio", "3200", "equity", "", "Owner draws")
			lt.bank("kit", "card", card, ledger.BankSyncTransaction{ProviderTransactionID: "k1", Date: "2026-11-02", AmountMinor: 5000, Description: "EXAMPLE DESK", Counterparty: "Example Desk"})
			lt.bank("kit", "cash", kitChecking, ledger.BankSyncTransaction{ProviderTransactionID: "k2", Date: "2026-11-12", AmountMinor: 8000, Description: "FROM THE STUDIO", Counterparty: "Kit Blanche Studio"})
			lt.bank("studio", "cash", studioChecking, ledger.BankSyncTransaction{ProviderTransactionID: "s1", Date: "2026-11-11", AmountMinor: -8000, Description: "TO KIT", Counterparty: "Kit Blanche"},
				ledger.BankSyncTransaction{ProviderTransactionID: "s2", Date: "2026-11-14", AmountMinor: -4500, Description: "EXAMPLE GYM", Counterparty: "Example Gym"})
			if w := lt.link("kit", "studio", "owed", kitFromStudio, studioToKit, 0); w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			if w := lt.link("studio", "kit", "owner_equity", draws, kitInStudio, 1); w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			// Saying how it is taxed after the link is set: a C corporation's
			// owner's-equity terms no longer fit, and the owner is asked to
			// switch them. Nothing recorded changes.
			body, _ := json.Marshal(ledger.ReviseBookSettings{BookID: b.ID, RequestKey: "taxed", Revision: *b.MetadataRevision, Name: b.Name, Founder: b.Founder, TaxedAs: &taxed})
			if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/settings", string(body)); w.Code != http.StatusOK {
				t.Fatal("taxed as", w.Code, w.Body.String())
			}
			body, _ = json.Marshal(ledger.OwnerPair{BankItemID: lt.item("kit", "EXAMPLE DESK").ID, BeneficiaryBookID: lt.books["studio"].ID, BeneficiaryAccountID: tools.ID, Description: "Desk"})
			if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, pairsPath, string(body)); w.Code != http.StatusOK {
				t.Fatal("desk", w.Code, w.Body.String())
			}
			body, _ = json.Marshal(ledger.OwnerSettlement{DebtorItemID: lt.item("studio", "TO KIT").ID, CreditorItemID: lt.item("kit", "FROM THE STUDIO").ID})
			if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, settlementsPath, string(body)); w.Code != http.StatusOK {
				t.Fatal("settle", w.Code, w.Body.String())
			}
			// The business paying for its owner's own cost records too, by
			// its terms, whatever it is taxed as.
			personal := lt.account("kit", "6900", "expense", "", "Personal care")
			body, _ = json.Marshal(ledger.OwnerPair{BankItemID: lt.item("studio", "EXAMPLE GYM").ID, BeneficiaryBookID: lt.books["kit"].ID, BeneficiaryAccountID: personal.ID, Description: "Gym"})
			if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, pairsPath, string(body)); w.Code != http.StatusOK {
				t.Fatal("the owner's own cost", w.Code, w.Body.String())
			}
			var looks, receipts int
			lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT count(*) FILTER (WHERE text LIKE '%'||$3||'%'),count(*) FILTER (WHERE text LIKE 'Add the receipt for%') FROM public.agent_loops WHERE tenant_id=$1 AND book_id=$2 AND status='open'",
				lt.f.tenant, lt.books["studio"].ID, want).Scan(&looks, &receipts)
			if looks != 1 || receipts != 1 {
				t.Fatal("asked to look", looks, "receipts", receipts)
			}
		})
	}
}

// How a business pays its owner follows how it is taxed: a C corporation's
// is only ever owed back. The links read says what each direction allows,
// and setting anything else is refused in plain words.
func TestLinkTermsFollowHowTheBusinessIsTaxed(t *testing.T) {
	for _, taxed := range []string{ledger.TaxedSoleOwner, ledger.TaxedPartnership, ledger.TaxedSCorporation, ledger.TaxedCCorporation} {
		t.Run(taxed, func(t *testing.T) {
			lt := newLinkedTenant(t)
			var b ledger.Book
			if err := lt.f.sessions.WithOwner(lt.f.db.Context, lt.f.session, lt.f.tenant, false, func(o ledger.TenantAdmin) error {
				var err error
				b, err = o.Book(lt.f.db.Context, lt.books["studio"].ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			value := taxed
			body, _ := json.Marshal(ledger.ReviseBookSettings{BookID: b.ID, RequestKey: "taxed", Revision: *b.MetadataRevision, Name: b.Name, Founder: b.Founder, TaxedAs: &value})
			if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/settings", string(body)); w.Code != http.StatusOK {
				t.Fatal("taxed as", w.Code, w.Body.String())
			}
			kitFromStudio := lt.account("kit", "1310", "asset", "receivable", "Owed by the studio")
			kitInStudio := lt.account("kit", "1500", "asset", "investment", "Investment in the studio")
			studioToKit := lt.account("studio", "2300", "liability", "payable", "Owed to Kit Blanche")
			studioFromKit := lt.account("studio", "1320", "asset", "receivable", "Owed by Kit Blanche")
			kitToStudio := lt.account("kit", "2320", "liability", "payable", "Owed to the studio")
			draws := lt.account("studio", "3200", "equity", "", "Paid to the owner")
			if w := lt.link("kit", "studio", "owed", kitFromStudio, studioToKit, 0); w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			w := lt.link("studio", "kit", "owner_equity", draws, kitInStudio, 1)
			if taxed == ledger.TaxedCCorporation {
				if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "is owed back, never owner's equity") {
					t.Fatal("a C corporation paying its owner as owner's equity", w.Code, w.Body.String())
				}
				if w := lt.link("studio", "kit", "owed", studioFromKit, kitToStudio, 1); w.Code != http.StatusOK {
					t.Fatal("owed back", w.Code, w.Body.String())
				}
			} else if w.Code != http.StatusOK {
				t.Fatal("owner's equity", w.Code, w.Body.String())
			}
			r := bankCall(t, lt.s, lt.f.session, http.MethodGet, bookLinksPath, "")
			var read struct{ Links []ledger.OwnerLink }
			if json.Unmarshal(r.Body.Bytes(), &read) != nil || len(read.Links) != 1 {
				t.Fatal(r.Body.String())
			}
			want := map[string]string{lt.books["studio"].ID: "owed,owner_equity", lt.books["kit"].ID: "contribution,owed"}
			if taxed == ledger.TaxedCCorporation {
				want[lt.books["studio"].ID] = "owed"
			}
			for _, a := range read.Links[0].Allowed {
				if strings.Join(a.Treatments, ",") != want[a.PayerBookID] {
					t.Fatal("allowed", a.PayerBookID, a.Treatments, want)
				}
			}
			if len(read.Links[0].Allowed) != 2 {
				t.Fatal("allowed", read.Links[0].Allowed)
			}
		})
	}
}

// Saying how a business is taxed renames the equity account Vult made for
// what it pays its owner, as an ordinary account revision, unless the owner
// named it themselves. A C corporation's name is left for the owner to
// switch.
func TestTaxedAsNamesTheOwnersDrawAccount(t *testing.T) {
	for taxed, want := range map[string]string{
		ledger.TaxedSCorporation: "Shareholder distributions to Kit Blanche",
		ledger.TaxedPartnership:  "Partner draws to Kit Blanche",
		ledger.TaxedCCorporation: "Draws to Kit Blanche",
	} {
		t.Run(taxed, func(t *testing.T) {
			lt := newLinkedTenant(t)
			kitFromStudio := lt.account("kit", "1310", "asset", "receivable", "Owed by the studio")
			kitInStudio := lt.account("kit", "1500", "asset", "investment", "Investment in the studio")
			studioToKit := lt.account("studio", "2300", "liability", "payable", "Owed to Kit Blanche")
			draws := lt.account("studio", "3200", "equity", "", "Draws to Kit Blanche")
			if w := lt.link("kit", "studio", "owed", kitFromStudio, studioToKit, 0); w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			if w := lt.link("studio", "kit", "owner_equity", draws, kitInStudio, 1); w.Code != http.StatusOK {
				t.Fatal(w.Code, w.Body.String())
			}
			var b ledger.Book
			if err := lt.f.sessions.WithOwner(lt.f.db.Context, lt.f.session, lt.f.tenant, false, func(o ledger.TenantAdmin) error {
				var err error
				b, err = o.Book(lt.f.db.Context, lt.books["studio"].ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			value := taxed
			body, _ := json.Marshal(ledger.ReviseBookSettings{BookID: b.ID, RequestKey: "taxed", Revision: *b.MetadataRevision, Name: b.Name, Founder: b.Founder, TaxedAs: &value})
			if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/settings", string(body)); w.Code != http.StatusOK {
				t.Fatal("taxed as", w.Code, w.Body.String())
			}
			var name string
			var revisions int
			lt.f.db.Admin.QueryRow(lt.f.db.Context, `SELECT (SELECT name FROM public.account_revisions WHERE account_id=$1 ORDER BY revision DESC LIMIT 1),
 (SELECT count(*) FROM public.account_revisions WHERE account_id=$1)`, draws.ID).Scan(&name, &revisions)
			if name != want || (taxed == ledger.TaxedCCorporation) != (revisions == 1) {
				t.Fatal("the draw account", name, revisions)
			}
		})
	}
	// A name the owner chose stays.
	lt := newLinkedTenant(t)
	kitFromStudio := lt.account("kit", "1310", "asset", "receivable", "Owed by the studio")
	kitInStudio := lt.account("kit", "1500", "asset", "investment", "Investment in the studio")
	studioToKit := lt.account("studio", "2300", "liability", "payable", "Owed to Kit Blanche")
	draws := lt.account("studio", "3200", "equity", "", "Paid to Kit")
	lt.link("kit", "studio", "owed", kitFromStudio, studioToKit, 0)
	lt.link("studio", "kit", "owner_equity", draws, kitInStudio, 1)
	var b ledger.Book
	lt.f.sessions.WithOwner(lt.f.db.Context, lt.f.session, lt.f.tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		b, err = o.Book(lt.f.db.Context, lt.books["studio"].ID)
		return err
	})
	value := ledger.TaxedSCorporation
	body, _ := json.Marshal(ledger.ReviseBookSettings{BookID: b.ID, RequestKey: "taxed", Revision: *b.MetadataRevision, Name: b.Name, Founder: b.Founder, TaxedAs: &value})
	if w := bankCall(t, lt.s, lt.f.session, http.MethodPost, "/api/v1/settings", string(body)); w.Code != http.StatusOK {
		t.Fatal("taxed as", w.Code, w.Body.String())
	}
	var name string
	lt.f.db.Admin.QueryRow(lt.f.db.Context, "SELECT name FROM public.account_revisions WHERE account_id=$1 ORDER BY revision DESC LIMIT 1", draws.ID).Scan(&name)
	if name != "Paid to Kit" {
		t.Fatal("an owner's name was changed", name)
	}
}
