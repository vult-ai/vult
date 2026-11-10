//go:build browserpreview

package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

// TestBrowserPreview serves the real server and interface on 127.0.0.1:8080
// (or VULT_PREVIEW_ADDR)
// with synthetic Books, for reviewing the interface in a browser. It acts as
// the TLS proxy a deployment has in front of the server.
//
// With VULT_PREVIEW_STATE set to a folder, the preview keeps its database,
// owner and sources across restarts, seeding the synthetic Books only once.
func TestBrowserPreview(t *testing.T) {
	var f *documentServerFixture
	fresh := true
	if dir := os.Getenv("VULT_PREVIEW_STATE"); dir != "" {
		f, fresh = keptPreview(t, dir)
	} else {
		f = documentServer(t)
	}
	origin := configuration().Origin
	host := strings.TrimPrefix(origin, "https://")

	if fresh {
		seedPreview(t, f)
	}

	web := os.Getenv("VULT_PREVIEW_WEB")
	if web == "" {
		t.Fatal("VULT_PREVIEW_WEB must name the built interface directory")
	}
	// "Continue" signs in as the synthetic owner, as a configured
	// identity provider would.
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) {
		id := f.identity
		id.AuthenticatedAt = time.Now().UTC()
		return id, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(configuration(), Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: login}, Documents: f.vault, Web: os.DirFS(web)})
	if err != nil {
		t.Fatal(err)
	}
	artwork, err := sharedAssetOrigin(configuration().SharedAssetOrigin)
	if err != nil {
		t.Fatal(err)
	}
	// VULT_PREVIEW_ADDR serves a second preview beside a kept one.
	addr := "127.0.0.1:8080"
	if value := os.Getenv("VULT_PREVIEW_ADDR"); value != "" {
		addr = value
	}
	local := "http://" + addr
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__preview/sign-in" {
			// A fresh session each time, so signing out elsewhere never strands a reviewer.
			f.identity.AuthenticatedAt = time.Now().UTC()
			session, err := f.sessions.IssueSession(r.Context(), f.identity)
			if err != nil {
				http.Error(w, "sign-in unavailable", http.StatusServiceUnavailable)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: access.SessionCookie, Value: session, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		for key := range r.Header {
			if lower := strings.ToLower(key); lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") {
				r.Header.Del(key)
			}
		}
		r.Host = host
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", host)
		r.Header.Set("X-Forwarded-For", "127.0.0.1")
		if r.Header.Get("Origin") == local {
			r.Header.Set("Origin", origin)
		}
		if ref := r.Header.Get("Referer"); strings.HasPrefix(ref, local) {
			r.Header.Set("Referer", origin+strings.TrimPrefix(ref, local))
		}
		// A page load gets the index.html now on disk, so a new web build shows
		// on reload without restarting the preview. Assets are always read live.
		if r.Method == http.MethodGet && interfacePath(r.URL.Path) && !strings.Contains(path.Base(r.URL.Path), ".") {
			page := httptest.NewRecorder()
			s.Handler.ServeHTTP(page, r)
			fresh, err := os.ReadFile(filepath.Join(web, "index.html"))
			for key, values := range page.Header() {
				w.Header()[key] = values
			}
			if page.Code == http.StatusOK && strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") && err == nil && r.URL.Path != "/auth/approve" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(bytes.ReplaceAll(fresh, sharedAssetPlaceholder, []byte(artwork)))
				return
			}
			w.WriteHeader(page.Code)
			_, _ = w.Write(page.Body.Bytes())
			return
		}
		s.Handler.ServeHTTP(w, r)
	})
	listener := &http.Server{Addr: addr, Handler: proxy, ReadHeaderTimeout: 10 * time.Second}
	t.Log("preview ready at " + local + "/__preview/sign-in")
	fmt.Println("PREVIEW READY " + local + "/__preview/sign-in")
	if err := listener.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}

// seedPreview adds the synthetic Books, agents and history the preview shows.
func seedPreview(t *testing.T, f *documentServerFixture) {
	ctx := f.db.Context
	// The empty Book keeps the fixture's name and has no timezone.
	if _, err := f.sessions.ReviseBookSettings(ctx, f.session, f.tenant, ledger.ReviseBookSettings{BookID: f.book.ID, RequestKey: "empty-name", Revision: 0, Name: "New Book (synthetic)", Founder: ""}, nil); err != nil {
		t.Fatal(err)
	}

	var studio ledger.Book
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		studio, err = o.CreateBook(ctx, "Northstar Studio (synthetic)", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	business, zone := "business", "Europe/Warsaw"
	if _, err := f.sessions.ReviseBookSettings(ctx, f.session, f.tenant, ledger.ReviseBookSettings{BookID: studio.ID, RequestKey: "studio-setup", Revision: 0, Name: "Northstar Studio (synthetic)", Founder: "Alex Morgan", Profile: &business, AccountingTimezone: &zone}, nil); err != nil {
		t.Fatal(err)
	}
	var c ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		c, err = o.CreateConnection(ctx, studio.ID, "Example agent", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.db.Store.Documents = f.vault
	scope := ledger.Scope{TenantID: f.tenant, BookID: studio.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	cash := f.db.Account(t, scope, "1000", "asset")
	equity := f.db.Account(t, scope, "3000", "equity")
	sales := f.db.Account(t, scope, "4000", "income")
	hosting := f.db.Account(t, scope, "6100", "expense")
	software := f.db.Account(t, scope, "6200", "expense")
	travel := f.db.Account(t, scope, "6300", "expense")
	contractors := f.db.Account(t, scope, "6400", "expense")
	role := func(a ledger.Account, key, name, value string) ledger.Account {
		var out ledger.Account
		if err := json.Unmarshal(f.db.Call(t, scope, "revise_account", ledger.ReviseAccount{RequestKey: key, AccountID: a.ID, ExpectedRevision: a.Revision, Code: a.Code, Name: name, Role: &value}), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Roles drive the cash-flow statement: cash, a loan and equipment.
	cash = role(cash, "cash-role", "Example Bank checking", "cash")
	loan := role(f.db.Account(t, scope, "2300", "liability"), "loan-role", "Example Bank loan", "loan")
	equipment := role(f.db.Account(t, scope, "1500", "asset"), "equipment-role", "Equipment", "fixed_asset")

	data := []byte("%PDF-1.4\n% Synthetic statement for interface review\n")
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	if err := json.Unmarshal(f.db.Call(t, scope, "add_document", ledger.AddDocument{RequestKey: "statement", Name: "synthetic-card-statement.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}), &intake); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Store.Upload(ctx, scope, intake.UploadID, strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}

	post := func(key, date, description string, debit, credit ledger.Account, minor string, cite bool) {
		q := ledger.PostJournal{RequestKey: key, EconomicDate: date, AccountingDate: date, Description: description,
			Lines: []ledger.PostLine{{AccountID: debit.ID, Side: "debit", AmountMinor: minor}, {AccountID: credit.ID, Side: "credit", AmountMinor: minor}}}
		if cite {
			q.Sources = []ledger.SourceInput{{DocumentID: intake.Document.ID}}
		}
		f.db.Call(t, scope, "post_journal", q)
	}
	post("funding", "2026-01-02", "Founder funding", cash, equity, "1500000", false)
	months := []string{"01", "02", "03", "04", "05", "06", "07", "08", "09", "10"}
	for i, m := range months {
		post("sale-"+m, "2026-"+m+"-12", fmt.Sprintf("Client project %d (synthetic)", i+1), cash, sales, fmt.Sprint(420000+i*35000), false)
		post("hosting-"+m, "2026-"+m+"-03", "Example Hosting Co, monthly plan", hosting, cash, "4900", m >= "07")
		post("software-"+m, "2026-"+m+"-15", "Example Design Tools subscription", software, cash, fmt.Sprint(2900+i*100), m >= "08")
	}
	post("travel", "2026-09-21", "Example Rail, client visit", travel, cash, "18640", true)
	post("loan", "2026-03-02", "Example Bank loan drawdown", cash, loan, "2000000", false)
	post("equipment", "2026-04-10", "Studio workstations (synthetic)", equipment, cash, "650000", false)
	for _, m := range months {
		post("contractors-"+m, "2026-"+m+"-25", "Example Contractors, monthly retainer", contractors, cash, "750000", false)
	}

	seedPayees(t, f, scope)
	seedStatements(t, f, scope, cash.ID, intake.Document.ID)
	seedCardCurrencies(t, f)

	// More agents: one that only reads and has recorded nothing, and one the
	// owner disconnected.
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		if _, err := o.CreateConnection(ctx, studio.ID, "Example reviewing agent", "propose"); err != nil {
			return err
		}
		gone, err := o.CreateConnection(ctx, studio.ID, "Example Assistant", "write")
		if err != nil {
			return err
		}
		return o.Revoke(ctx, gone.ID)
	}); err != nil {
		t.Fatal(err)
	}

}

// seedCardCurrencies adds a Personal dollar Book shaped like real card
// statements: foreign charges with the original amount and the billed dollars,
// a merchant with no original currency, refunds at a different rate, interest,
// cash back, payments from a bank not yet in the Book, and a zloty account
// valued at a quoted rate. Every name and amount is synthetic.
func seedCardCurrencies(t *testing.T, f *documentServerFixture) {
	ctx := f.db.Context
	var book ledger.Book
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		book, err = o.CreateBook(ctx, "Synthetic Personal Cards", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	personal, zone := "personal", "Europe/Warsaw"
	if _, err := f.sessions.ReviseBookSettings(ctx, f.session, f.tenant, ledger.ReviseBookSettings{BookID: book.ID, RequestKey: "cards-setup", Revision: 0, Name: "Synthetic Personal Cards", Founder: "", Profile: &personal, AccountingTimezone: &zone}, nil); err != nil {
		t.Fatal(err)
	}
	var c ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		c, err = o.CreateConnection(ctx, book.ID, "Example agent", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	two := 2
	account := func(key, class, code, name, currency, role string, empty bool) ledger.Account {
		q := ledger.CreateAccount{RequestKey: key, Class: class, Code: code, Name: name, Role: role}
		if currency != "" {
			q.Currency, q.Scale = currency, &two
		}
		if empty {
			q.NativeOrigin = "empty_ledger"
		}
		var a ledger.Account
		if err := json.Unmarshal(f.db.Call(t, scope, "create_account", q), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	cardA := account("card-a", "liability", "2100", "Example Rewards card ••1111", "USD", "", false)
	cardB := account("card-b", "liability", "2110", "Example Travel card ••2222", "USD", "", false)
	transit := account("transit", "asset", "1900", "Transfers in transit (bank not yet in Book)", "", "", false)
	zloty := account("zloty", "asset", "1020", "Example złoty account", "PLN", "cash", true)
	opening := account("opening", "equity", "3000", "Opening balances", "", "", false)
	groceries := account("groceries", "expense", "6100", "Groceries", "", "", false)
	subscriptions := account("subscriptions", "expense", "6200", "Software & subscriptions", "", "", false)
	transport := account("transport", "expense", "6300", "Local transport", "", "", false)
	interest := account("interest", "expense", "6900", "Card interest", "", "", false)
	cashback := account("cashback", "income", "4100", "Card cash back", "", "", false)

	money := func(currency, minor string) ledger.CurrencyAmount {
		return ledger.CurrencyAmount{Currency: currency, Scale: &two, AmountMinor: minor}
	}
	actual := func(minor string) *ledger.Valuation {
		in := money("USD", minor)
		return &ledger.Valuation{Basis: "actual_source", Policy: "actual-source-book-value-v1", Input: &in, Source: "Card statement billed amount"}
	}
	post := func(key, date, description string, debit, credit ledger.Account, usd string, original *ledger.CurrencyAmount) {
		debitLine := ledger.PostLine{AccountID: debit.ID, Side: "debit", AmountMinor: usd}
		creditLine := ledger.PostLine{AccountID: credit.ID, Side: "credit", AmountMinor: usd}
		if original != nil {
			// The statement shows the merchant's amount and the billed dollars.
			for _, line := range []*ledger.PostLine{&debitLine, &creditLine} {
				line.Valuation = actual(usd)
			}
			context := []ledger.ContextMeasurement{{Role: "transaction_amount", Amount: *original, Source: "Card statement: charged amount"}}
			if debit.Class == "expense" {
				debitLine.Contexts = context
			} else {
				creditLine.Contexts = context
			}
		}
		f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: key, EconomicDate: date, AccountingDate: date, Description: description, Lines: []ledger.PostLine{debitLine, creditLine}})
	}
	pln := func(minor string) *ledger.CurrencyAmount { v := money("PLN", minor); return &v }
	eur := func(minor string) *ledger.CurrencyAmount { v := money("EUR", minor); return &v }

	// Opening balances owed on each card, from the first statements.
	post("open-a", "2025-12-01", "Opening balance owed on Example Rewards card ••1111 per first statement", opening, cardA, "184250", nil)
	post("open-b", "2026-08-31", "Opening balance owed on Example Travel card ••2222 per first statement", opening, cardB, "92011", nil)
	// The same grocer in zloty, each time at a slightly different implied rate.
	for i, g := range []struct{ date, pln, usd string }{
		{"2025-12-04", "21430", "5382"}, {"2025-12-11", "18790", "4731"}, {"2025-12-19", "26515", "6702"},
		{"2026-01-08", "19960", "5044"}, {"2026-01-22", "23100", "5869"},
	} {
		post(fmt.Sprintf("grocer-%d", i), g.date, fmt.Sprintf("EXAMPLE GROCER, Warszawa POL (Example Rewards ••1111); charged %s PLN, billed $%s", decimalText(g.pln), decimalText(g.usd)), groceries, cardA, g.usd, pln(g.pln))
	}
	// Subscriptions billed from abroad in zloty, and one in euros on the other card.
	post("video", "2025-12-15", "EXAMPLE VIDEO, London GBR (Example Rewards ••1111); charged 49.99 PLN, billed $12.61", subscriptions, cardA, "1261", pln("4999"))
	post("mail", "2026-09-16", "EXAMPLE MAIL AG, Geneva CHE (Example Travel ••2222); charged 39.90 EUR, billed $46.11", subscriptions, cardB, "4611", eur("3990"))
	// A foreign merchant that charged in dollars: nothing to convert.
	post("tools", "2026-09-28", "EXAMPLE TOOLS INC, New York NY (Example Travel ••2222)", subscriptions, cardB, "2000", nil)
	// Only a posting date on the statement.
	post("ride", "2026-09-24", "EXAMPLE RIDES, Warsaw POL (Example Travel ••2222); posted 09/24, no transaction date shown; charged 23.50 PLN, billed $6.03", transport, cardB, "603", pln("2350"))
	// A refund of a zloty charge, credited at a different implied rate.
	post("grocer-refund", "2025-12-21", "EXAMPLE GROCER refund, Warszawa POL (Example Rewards ••1111); credited 26.40 PLN, $6.49", cardA, groceries, "649", pln("2640"))
	// Interest, cash back, and payments from a bank that is not in the Book yet.
	post("interest-a", "2026-01-11", "Interest charged on purchases, billing period 12/12/25–01/11/26 (Example Rewards ••1111)", interest, cardA, "3127", nil)
	post("cashback-1", "2025-12-16", "CASH BACK REWARD on Example Rewards ••1111", cardA, cashback, "412", nil)
	post("cashback-2", "2025-12-29", "CASH BACK REWARD on Example Rewards ••1111", cardA, cashback, "268", nil)
	post("payment-a", "2025-12-23", "Mobile payment to Example Rewards ••1111 (paying bank not yet in Book)", cardA, transit, "50000", nil)
	post("payment-b", "2026-09-09", "Online payment to Example Travel ••2222; paying account not shown", cardB, transit, "30000", nil)

	// A zloty account funded twice from dollars, each valued at a quoted rate.
	for i, fund := range []struct{ date, pln, rate, usd string }{
		{"2026-10-15", "100000", "0.25635", "25635"},
		{"2026-11-06", "40000", "0.2571", "10284"},
	} {
		in := money("PLN", fund.pln)
		rate := ledger.AppliedRate{Base: "PLN", Quote: "USD", Rate: fund.rate, RequestedDate: fund.date, ObservationDate: fund.date, Provenance: "Synthetic reference rate for interface review"}
		f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: fmt.Sprintf("zloty-%d", i), EconomicDate: fund.date, AccountingDate: fund.date,
			Description: fmt.Sprintf("Transfer to Example złoty account: %s PLN at %s", decimalText(fund.pln), fund.rate),
			Lines: []ledger.PostLine{
				{AccountID: zloty.ID, Side: "debit", AmountMinor: fund.usd, Native: &ledger.NativeMovement{Currency: "PLN", Scale: &two, AmountMinor: fund.pln},
					Valuation: &ledger.Valuation{Basis: "applied_quote", Policy: "quoted-rate-half-even-v1", Input: &in, Rate: &rate}},
				{AccountID: transit.ID, Side: "credit", AmountMinor: fund.usd},
			}})
	}
}

// decimalText shows minor units with two decimals, for synthetic descriptions.
func decimalText(minor string) string {
	for len(minor) < 3 {
		minor = "0" + minor
	}
	return minor[:len(minor)-2] + "." + minor[len(minor)-2:]
}

// seedStatements imports two checking statements: September, matched and
// reconciled, and October, with one row still to match and a bank fee the
// Book doesn't have yet.
func seedStatements(t *testing.T, f *documentServerFixture, scope ledger.Scope, cash, document string) {
	balance := func(through string) int64 {
		var b struct {
			Balance string `json:"debit_positive_minor"`
		}
		if err := json.Unmarshal(f.db.Call(t, scope, "get_balance", map[string]string{"account_id": cash, "through": through}), &b); err != nil {
			t.Fatal(err)
		}
		n, _ := strconv.ParseInt(b.Balance, 10, 64)
		return n
	}
	statement := func(key, start, end string, rows [][3]string) ledger.Statement {
		day, _ := time.Parse("2006-01-02", start)
		opening := balance(day.AddDate(0, 0, -1).Format("2006-01-02"))
		in := ledger.ImportStatement{RequestKey: key, AccountID: cash, DocumentID: document, PeriodStart: start, PeriodEnd: end, OpeningMinor: strconv.FormatInt(opening, 10)}
		closing := opening
		for i, r := range rows {
			n, _ := strconv.ParseInt(r[1], 10, 64)
			closing += n
			in.Rows = append(in.Rows, ledger.StatementRowInput{RowNumber: i + 1, Date: r[0], AmountMinor: r[1], Description: r[2], Raw: r[0] + "," + r[2] + "," + r[1]})
		}
		in.ClosingMinor = strconv.FormatInt(closing, 10)
		var st ledger.Statement
		if err := json.Unmarshal(f.db.Call(t, scope, "import_statement", in), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	match := func(st ledger.Statement, skip string) {
		var out struct {
			Suggestions []ledger.StatementSuggestion `json:"suggestions"`
		}
		if err := json.Unmarshal(f.db.Call(t, scope, "suggest_statement_matches", ledger.StatementQuery{StatementID: st.ID}), &out); err != nil {
			t.Fatal(err)
		}
		for i, s := range out.Suggestions {
			if s.Kind != "match" || s.AmountMinor == skip {
				continue
			}
			f.db.Call(t, scope, "match_statement", ledger.MatchStatement{RequestKey: fmt.Sprintf("%s-match-%d", st.ID, i), Rows: []ledger.StatementMatchPart{{RowID: s.RowID}},
				Lines: []ledger.StatementMatchPart{{LineID: s.LineIDs[0]}}, Reason: "Same payment on the statement and in the Book"})
		}
	}
	september := statement("statement-sep", "2026-09-01", "2026-09-30", [][3]string{
		{"2026-09-03", "-4900", "EXAMPLE HOSTING CO"}, {"2026-09-12", "700000", "CLIENT PROJECT 9"}, {"2026-09-15", "-3700", "EXAMPLE DESIGN TOOLS"},
		{"2026-09-22", "-18640", "EXAMPLE RAIL"}, {"2026-09-25", "-750000", "EXAMPLE CONTRACTORS"}})
	match(september, "")
	f.db.Call(t, scope, "review_statement", ledger.ReviewStatement{RequestKey: "statement-sep-review", StatementID: september.ID, Outcome: "reconciled"})
	october := statement("statement-oct", "2026-10-01", "2026-10-31", [][3]string{
		{"2026-10-03", "-4900", "EXAMPLE HOSTING CO"}, {"2026-10-12", "735000", "CLIENT PROJECT 10"}, {"2026-10-15", "-3800", "EXAMPLE DESIGN TOOLS"},
		{"2026-10-26", "-750000", "EXAMPLE CONTRACTORS"}, {"2026-10-31", "-1200", "MONTHLY ACCOUNT FEE"}})
	match(october, "-750000")
}

// seedPayees names who the studio's regular charges were with, so recurring
// charges and spending by payee have something to show.
func seedPayees(t *testing.T, f *documentServerFixture, scope ledger.Scope) {
	var page struct {
		Journals []ledger.Entry `json:"entries"`
	}
	if err := json.Unmarshal(f.db.Call(t, scope, "list_journals", map[string]int{"limit": 100}), &page); err != nil {
		t.Fatal(err)
	}
	payees := map[string]string{}
	for _, name := range []string{"Example Hosting Co", "Example Design Tools", "Example Contractors"} {
		var p ledger.Payee
		if err := json.Unmarshal(f.db.Call(t, scope, "create_payee", ledger.CreatePayee{RequestKey: "payee-" + name, DisplayName: name, Kind: "organization"}), &p); err != nil {
			t.Fatal(err)
		}
		payees[name] = p.ID
	}
	for _, e := range page.Journals {
		for name, id := range payees {
			if strings.HasPrefix(e.Description, name) {
				f.db.Call(t, scope, "set_entry_payee", ledger.SetEntryPayee{RequestKey: "link-" + e.ID, EntryID: e.ID, PayeeID: id})
			}
		}
	}
}
