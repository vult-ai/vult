//go:build devserver && browserpreview

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// A seed file of whole synthetic Books: their accounts, typed the way an
// accounting export types them, every entry with its lines, and the
// documents some entries cite. Nothing in it is real.
type fullSeed struct {
	Through string         `json:"through"`
	Books   []fullSeedBook `json:"books"`
}

type fullSeedBook struct {
	Name     string `json:"name"`
	Profile  string `json:"profile"`
	Currency string `json:"currency"`
	Timezone string `json:"timezone"`
	Founder  string `json:"founder"`
	Logo     string `json:"logo"`
	Accounts []struct {
		Name   string `json:"name"`
		Type   string `json:"type"`
		Detail string `json:"detail"`
		Parent string `json:"parent"`
	} `json:"accounts"`
	Entries []struct {
		Key         string `json:"key"`
		Date        string `json:"date"`
		Description string `json:"description"`
		Document    string `json:"document"`
		Lines       []struct {
			Account string `json:"account"`
			Debit   int64  `json:"debit"`
			Credit  int64  `json:"credit"`
		} `json:"lines"`
	} `json:"entries"`
	Documents []struct {
		ID       string `json:"id"`
		Filename string `json:"filename"`
		Text     string `json:"text"`
	} `json:"documents"`
	// Banks are connected banks with activity waiting to be recorded.
	Banks []fullSeedBank `json:"banks"`
}

// A connected bank in a seed file: its accounts, each linked to the Book's
// account of the same full name, and their activity. Amounts are in cents,
// signed the way the account's balance moves.
type fullSeedBank struct {
	Institution string `json:"institution"`
	Accounts    []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Mask    string `json:"mask"`
		Kind    string `json:"kind"`
		Account string `json:"account"`
	} `json:"accounts"`
	Activity []struct {
		Account      string `json:"account"`
		ID           string `json:"id"`
		Date         string `json:"date"`
		Amount       int64  `json:"amount"`
		Description  string `json:"description"`
		Counterparty string `json:"counterparty"`
	} `json:"activity"`
}

// seedAccountType gives an exported account type its class and role, and
// the first code of its range. The type decides, never the account's name.
func seedAccountType(kind, detail string) (class, role, first string, err error) {
	switch kind {
	case "Bank":
		return "asset", "cash", "1000", nil
	case "Accounts Receivable":
		return "asset", "receivable", "1200", nil
	case "Other Current Assets":
		return "asset", "", "1300", nil
	case "Other Assets":
		return "asset", "investment", "1600", nil
	case "Fixed Assets":
		return "asset", "fixed_asset", "1700", nil
	case "Credit Card":
		return "liability", "card", "2000", nil
	case "Accounts Payable":
		return "liability", "payable", "2200", nil
	case "Other Current Liabilities":
		return "liability", "", "2300", nil
	case "Long Term Liabilities":
		return "liability", "loan", "2700", nil
	case "Equity":
		if detail == "Opening Balance Equity" {
			return "equity", "opening_balance", "3900", nil
		}
		return "equity", "", "3000", nil
	case "Income":
		return "income", "", "4000", nil
	case "Other Income":
		return "income", "", "4800", nil
	case "Cost of Goods Sold":
		return "expense", "cost_of_sales", "5000", nil
	case "Expenses":
		return "expense", "", "6000", nil
	case "Other Expense":
		return "expense", "", "8000", nil
	}
	return "", "", "", fmt.Errorf("unknown account type %q", kind)
}

// seedCall runs one ledger operation and says which record it was for when
// it fails.
func seedCall(t *testing.T, f *documentServerFixture, scope ledger.Scope, method string, payload any, what string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.db.Store.Call(f.db.Context, scope, method, b)
	if err != nil {
		t.Fatalf("%s for %s: %v", method, what, err)
	}
	return out
}

// seedPDF writes text as a one-page PDF, for synthetic receipts and invoices.
func seedPDF(text string) []byte {
	escape := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`)
	var content bytes.Buffer
	content.WriteString("BT /F1 11 Tf 56 760 Td 15 TL\n")
	for _, line := range strings.Split(text, "\n") {
		ascii := strings.Map(func(r rune) rune {
			if r < 32 || r > 126 {
				return '-'
			}
			return r
		}, line)
		fmt.Fprintf(&content, "(%s) Tj T*\n", escape.Replace(ascii))
	}
	content.WriteString("ET\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", content.Len(), content.String()),
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, o := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}

// seedFixture opens the development Vult as its owner, for a seed step.
func seedFixture(t *testing.T, e *devEnv) *documentServerFixture {
	t.Helper()
	ctx := context.Background()
	admin := e.pool(t, "", 0)
	var tenant string
	err := admin.QueryRow(ctx, `SELECT o.tenant_id::text FROM access.owners o JOIN access.identities i ON i.id=o.identity_id
 JOIN access.passkey_profiles p ON p.identity_id=i.id
 WHERE i.issuer=$1 AND i.subject=$2 AND p.origin=$3 AND p.state='ready' LIMIT 1`, access.BuiltinIssuer, devOwnerSubject, e.origin).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("the owner hasn't finished the first run yet: make the passkey first, then seed")
	}
	if err != nil {
		t.Fatal(err)
	}
	runtime := e.pool(t, devRuntimeRole, 8)
	f := &documentServerFixture{
		db:       &testutil.Database{Context: ctx, Admin: admin, Runtime: runtime, Owner: ledger.Admin{DB: admin}, Store: ledger.Store{Runtime: runtime}, Role: devRuntimeRole, Name: devDatabase},
		operator: access.Operator{DB: admin},
		tenant:   tenant,
		identity: access.Identity{Issuer: access.BuiltinIssuer, Subject: devOwnerSubject, AuthenticatedAt: time.Now().UTC()},
	}
	f.auth, f.owners = e.pool(t, devAuthRole, 0), e.pool(t, devOwnerRole, 0)
	if f.sessions, err = access.New(f.auth, f.owners, e.origin); err != nil {
		t.Fatal(err)
	}
	if f.session, err = f.sessions.IssueSession(ctx, f.identity); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(filepath.Join(e.dir, "documents"))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.OpenKeyring(filepath.Join(e.dir, "documents-key.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.vault = &documents.Vault{Storage: disk, Keys: keys}
	f.db.Store.Documents = f.vault
	return f
}

// TestDevSeedFull loads whole synthetic Books into the development Vult
// through the ledger's own operations, renames earlier sample Books of the
// same names, links the business and its founder, and records a few costs
// the founder paid for the business. VULT_DEV_SEED_FILE names the seed.
func TestDevSeedFull(t *testing.T) {
	ctx := context.Background()
	path := os.Getenv("VULT_DEV_SEED_FILE")
	if path == "" {
		t.Fatal("VULT_DEV_SEED_FILE names the seed file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var seed fullSeed
	if err := json.Unmarshal(raw, &seed); err != nil || len(seed.Books) == 0 {
		t.Fatalf("the seed file can't be read: %v", err)
	}
	e := openDevEnv(t, false)
	f := seedFixture(t, e)
	defer func() { _ = f.sessions.EndSession(ctx, f.session) }()

	var existing []ledger.Book
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		existing, err = o.Books(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, b := range existing {
		if b.Name == seed.Books[0].Name {
			t.Fatalf("%s is already here; the full seed is loaded once", b.Name)
		}
	}
	// Earlier sample Books with these names step aside, renamed, not removed.
	earlier := map[string]bool{"Eightball Inc.": true}
	for _, s := range seed.Books {
		earlier[s.Name] = true
	}
	for _, b := range existing {
		if !earlier[b.Name] || strings.HasSuffix(b.Name, "(earlier sample)") {
			continue
		}
		var revision int64
		if b.MetadataRevision != nil {
			revision = *b.MetadataRevision
		}
		if _, err := f.sessions.ReviseBookSettings(ctx, f.session, f.tenant, ledger.ReviseBookSettings{BookID: b.ID, RequestKey: "earlier-" + b.ID, Revision: revision,
			Name: b.Name + " (earlier sample)", Founder: b.Founder}, nil); err != nil {
			t.Fatalf("renaming %s: %v", b.Name, err)
		}
	}

	loaded := map[string]seededBook{}
	for _, s := range seed.Books {
		loaded[s.Profile] = seedFullBook(t, f, s)
	}
	business, personal := loaded["business"], loaded["personal"]
	if business.id != "" && personal.id != "" {
		seedFounderLink(t, f, business, personal, seed.Through)
		// What the owner should see: the same read Who owes whom uses.
		if err := f.sessions.WithOwner(ctx, f.session, f.tenant, false, func(o ledger.TenantAdmin) error {
			got, err := o.OwnerLinkedBalances(ctx)
			if err != nil {
				return err
			}
			out, _ := json.Marshal(got)
			fmt.Printf("linked balances %s\n", out)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("DEV SEEDED FULL")
}

type seededBook struct {
	id, name string
	scope    ledger.Scope
	accounts map[string]ledger.Account
	byType   map[string][]string
}

func seedFullBook(t *testing.T, f *documentServerFixture, s fullSeedBook) seededBook {
	t.Helper()
	ctx := f.db.Context
	var book ledger.Book
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		book, err = o.CreateBook(ctx, s.Name, s.Currency, 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	start := "2024-01-01"
	if len(s.Entries) > 0 {
		start = s.Entries[0].Date[:4] + "-01-01"
	}
	if _, err := f.sessions.ReviseBookSettings(ctx, f.session, f.tenant, ledger.ReviseBookSettings{BookID: book.ID, RequestKey: "full-setup-" + book.ID, Revision: 0,
		Name: s.Name, Founder: s.Founder, Profile: &s.Profile, AccountingTimezone: &s.Timezone, StartDate: &start}, nil); err != nil {
		t.Fatalf("setting up %s: %v", s.Name, err)
	}
	var c ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		c, err = o.CreateConnection(ctx, book.ID, "Example bookkeeping agent", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	out := seededBook{id: book.ID, name: s.Name, scope: scope, accounts: map[string]ledger.Account{}, byType: map[string][]string{}}

	if s.Logo != "" {
		data, err := base64.StdEncoding.DecodeString(s.Logo)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		change, err := ledger.NewBookLogoChange("image/jpeg", hex.EncodeToString(sum[:]), data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Store.SetBookLogo(ctx, scope, "logo-"+book.ID, change); err != nil {
			t.Fatalf("logo for %s: %v", s.Name, err)
		}
	}

	// A parent the export only implies becomes a heading of its children's type.
	named := map[string]bool{}
	for _, a := range s.Accounts {
		named[a.Name] = true
	}
	headings := map[string]bool{}
	for _, a := range s.Accounts {
		if a.Parent != "" && !named[a.Parent] {
			named[a.Parent], headings[a.Parent] = true, true
			s.Accounts = append(s.Accounts, struct {
				Name   string `json:"name"`
				Type   string `json:"type"`
				Detail string `json:"detail"`
				Parent string `json:"parent"`
			}{Name: a.Parent, Type: a.Type})
		}
	}

	// Accounts: class and role from the exported type; codes by range.
	next := map[string]int{}
	for _, a := range s.Accounts {
		class, role, first, err := seedAccountType(a.Type, a.Detail)
		if err != nil {
			t.Fatal(err)
		}
		if next[first] == 0 {
			next[first], _ = strconv.Atoi(first)
		}
		code := strconv.Itoa(next[first])
		next[first] += 10
		name := a.Name
		if i := strings.LastIndex(name, ":"); i >= 0 {
			name = name[i+1:]
		}
		var created ledger.Account
		if err := json.Unmarshal(seedCall(t, f, scope, "create_account", ledger.CreateAccount{RequestKey: "account-" + code, Class: class, Code: code, Name: name, Role: role}, s.Name+" "+a.Name), &created); err != nil {
			t.Fatal(err)
		}
		out.accounts[a.Name] = created
		out.byType[a.Type] = append(out.byType[a.Type], a.Name)
	}
	for name := range headings {
		h := out.accounts[name]
		heading := true
		var revised ledger.Account
		if err := json.Unmarshal(seedCall(t, f, scope, "revise_account", ledger.ReviseAccount{RequestKey: "heading-" + h.Code, AccountID: h.ID, ExpectedRevision: h.Revision,
			Code: h.Code, Name: h.Name, Heading: &heading}, name), &revised); err != nil {
			t.Fatal(err)
		}
		out.accounts[name] = revised
	}
	for _, a := range s.Accounts {
		if a.Parent == "" {
			continue
		}
		child, parent := out.accounts[a.Name], out.accounts[a.Parent]
		if parent.ID == "" {
			t.Fatalf("%s names a parent %s that isn't in the chart", a.Name, a.Parent)
		}
		var revised ledger.Account
		if err := json.Unmarshal(seedCall(t, f, scope, "revise_account", ledger.ReviseAccount{RequestKey: "parent-" + child.Code, AccountID: child.ID, ExpectedRevision: child.Revision,
			Code: child.Code, Name: child.Name, ParentAccountID: &parent.ID}, a.Name), &revised); err != nil {
			t.Fatal(err)
		}
		out.accounts[a.Name] = revised
	}

	// Documents, as one-page PDFs, kept for the entries that cite them.
	docs := map[string]string{}
	for _, d := range s.Documents {
		data := seedPDF(d.Text)
		sum := sha256.Sum256(data)
		var intake ledger.DocumentIntake
		if err := json.Unmarshal(seedCall(t, f, scope, "add_document", ledger.AddDocument{RequestKey: "document-" + d.ID, Name: d.Filename, MediaType: "application/pdf",
			Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, d.Filename), &intake); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Store.Upload(ctx, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
			t.Fatalf("uploading %s: %v", d.Filename, err)
		}
		docs[d.ID] = intake.Document.ID
	}

	for _, entry := range s.Entries {
		q := ledger.PostJournal{RequestKey: "entry-" + entry.Key, EconomicDate: entry.Date, AccountingDate: entry.Date, Description: entry.Description}
		for _, l := range entry.Lines {
			a := out.accounts[l.Account]
			if a.ID == "" {
				t.Fatalf("%s: unknown account %s", entry.Key, l.Account)
			}
			if l.Debit > 0 {
				q.Lines = append(q.Lines, ledger.PostLine{AccountID: a.ID, Side: "debit", AmountMinor: strconv.FormatInt(l.Debit, 10)})
			}
			if l.Credit > 0 {
				q.Lines = append(q.Lines, ledger.PostLine{AccountID: a.ID, Side: "credit", AmountMinor: strconv.FormatInt(l.Credit, 10)})
			}
		}
		if id := docs[entry.Document]; id != "" {
			q.Sources = []ledger.SourceInput{{DocumentID: id}}
		}
		seedCall(t, f, scope, "post_journal", q, s.Name+" "+entry.Key+" "+entry.Date)
	}
	for i, b := range s.Banks {
		var accounts []ledger.BankSyncAccount
		links := map[string]string{}
		for _, a := range b.Accounts {
			kind, subtype := "depository", "checking"
			if a.Kind == "card" {
				kind, subtype = "credit", "credit card"
			}
			accounts = append(accounts, ledger.BankSyncAccount{ProviderAccountID: a.ID, Name: a.Name, Mask: a.Mask, Type: kind, Subtype: subtype, Kind: a.Kind, Currency: s.Currency, Scale: 2})
			if out.accounts[a.Account].ID == "" {
				t.Fatalf("%s: bank account %s names an account %s that isn't in the chart", s.Name, a.Name, a.Account)
			}
			links[a.ID] = out.accounts[a.Account].ID
		}
		var added []ledger.BankSyncTransaction
		for _, x := range b.Activity {
			added = append(added, ledger.BankSyncTransaction{ProviderAccountID: x.Account, ProviderTransactionID: x.ID, Date: x.Date, AmountMinor: x.Amount,
				Currency: s.Currency, Scale: 2, Description: x.Description, Counterparty: x.Counterparty})
		}
		seedBank(t, f, out, b.Institution, fmt.Sprintf("item-seed-%s-%d", book.ID, i), accounts, links, added)
		fmt.Printf("connected %s to %s: %d accounts, %d items\n", b.Institution, s.Name, len(accounts), len(added))
	}
	fmt.Printf("loaded %s: %d accounts, %d entries, %d documents\n", s.Name, len(s.Accounts), len(s.Entries), len(s.Documents))
	return out
}

// seedBank connects a bank to a Book, adds its activity in pages and links
// each of its accounts to the Book's account it stands for.
func seedBank(t *testing.T, f *documentServerFixture, b seededBook, institution, item string, accounts []ledger.BankSyncAccount, links map[string]string, added []ledger.BankSyncTransaction) string {
	t.Helper()
	ctx := f.db.Context
	var login string
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		login, err = o.AddBankConnection(ctx, b.id, who, ledger.NewBankConnection{Provider: "mercury", Environment: "sandbox", ProviderItemID: item,
			InstitutionName: institution, TokenSealed: bytes.Repeat([]byte{3}, 48), KeyVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target := ledger.BankSyncTarget{TenantID: f.tenant, BookID: b.id, ConnectionID: login}
	const page = 200
	cursor := ""
	for first := true; first || len(added) > 0; first = false {
		n := min(page, len(added))
		next := fmt.Sprintf("seed-%d", len(added)-n)
		if _, err := f.db.Store.ApplyBankSync(ctx, target, ledger.BankSyncBatch{FromCursor: cursor, NextCursor: next, Complete: n == len(added), Accounts: accounts, Added: added[:n]}); err != nil {
			t.Fatal(err)
		}
		added, cursor = added[n:], next
	}
	var views []ledger.BankConnectionView
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		views, err = o.BankConnections(ctx, b.id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.ID != login {
			continue
		}
		for _, a := range v.Accounts {
			for _, sa := range accounts {
				if sa.Name == a.Name && links[sa.ProviderAccountID] != "" {
					if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
						return o.LinkBankAccount(ctx, b.id, who, a.ID, links[sa.ProviderAccountID])
					}); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	return login
}

// seedFounderLink links the founder's personal Book to the business, owed
// back, gives each Book a connected bank with recent activity, and records a
// few business costs the founder paid on her card, so Who owes whom and What
// this Book owes have real figures.
func seedFounderLink(t *testing.T, f *documentServerFixture, business, personal seededBook, through string) {
	t.Helper()
	ctx := f.db.Context
	create := func(b seededBook, code, class, role, name string) ledger.Account {
		var a ledger.Account
		if err := json.Unmarshal(seedCall(t, f, b.scope, "create_account", ledger.CreateAccount{RequestKey: "account-" + code, Class: class, Code: code, Name: name, Role: role}, name), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	owedToFounder := create(business, "2290", "liability", "payable", "Owed to "+personal.name)
	owedByBusiness := create(personal, "1290", "asset", "receivable", "Owed by "+business.name)
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		_, err := o.SetLinkTerms(ctx, who, ledger.LinkTermsChange{BookIDs: [2]string{business.id, personal.id}, PayerBookID: personal.id, Treatment: "owed",
			PayerAccountID: owedByBusiness.ID, BeneficiaryAccountID: owedToFounder.ID, Active: true})
		return err
	}); err != nil {
		t.Fatalf("linking the Books: %v", err)
	}

	day := func(back int) string {
		d, err := time.Parse("2006-01-02", through)
		if err != nil {
			t.Fatal(err)
		}
		return d.AddDate(0, 0, -back).Format("2006-01-02")
	}
	bank := func(b seededBook, institution, item string, accounts []ledger.BankSyncAccount, links map[string]string, added []ledger.BankSyncTransaction) map[string]string {
		return map[string]string{"login": seedBank(t, f, b, institution, item, accounts, links, added)}
	}
	tx := func(account, id, date string, amount int64, name, counterparty string) ledger.BankSyncTransaction {
		return ledger.BankSyncTransaction{ProviderAccountID: account, ProviderTransactionID: id, Date: date, AmountMinor: amount, Currency: "USD", Scale: 2, Description: name, Counterparty: counterparty}
	}

	// The business bank: a few items since the last recorded day, to record.
	checking, amex := business.accounts[business.byType["Bank"][0]], business.accounts[business.byType["Credit Card"][1]]
	bank(business, "Mercury", "item-synthetic-business",
		[]ledger.BankSyncAccount{
			{ProviderAccountID: "checking", Name: "Mercury Checking", Mask: "4417", Type: "depository", Subtype: "checking", Kind: "cash", Currency: "USD", Scale: 2},
			{ProviderAccountID: "amex", Name: "Amex Business", Mask: "1009", Type: "credit", Subtype: "credit card", Kind: "card", Currency: "USD", Scale: 2},
		},
		map[string]string{"checking": checking.ID, "amex": amex.ID},
		[]ledger.BankSyncTransaction{
			tx("checking", "b1", day(1), 1268000, "STRIPE TRANSFER", "Stripe"),
			tx("checking", "b2", day(1), -840000, "CAPITAL FACTORY COWORKING", "Capital Factory Coworking"),
			tx("amex", "b3", day(2), 38900, "FIGMA PROFESSIONAL", "Figma"),
			tx("amex", "b4", day(0), 6420, "UBER TRIP", "Uber"),
			// Paying the founder back, and her returning what was overpaid.
			tx("checking", "b5", day(3), -50000, "TRANSFER TO FOUNDER", personal.name),
			tx("checking", "b6", day(2), -8000, "TRANSFER TO FOUNDER COWORKING", personal.name),
			tx("checking", "b7", day(1), 5000, "TRANSFER FROM FOUNDER", personal.name),
		})

	// The founder's card: business costs she paid, and her own spending.
	card := personal.accounts[personal.byType["Credit Card"][1]]
	bank(personal, "Capital One", "item-synthetic-founder",
		[]ledger.BankSyncAccount{{ProviderAccountID: "venture", Name: "Venture X", Mask: "7702", Type: "credit", Subtype: "credit card", Kind: "card", Currency: "USD", Scale: 2}},
		map[string]string{"venture": card.ID},
		[]ledger.BankSyncTransaction{
			tx("venture", "f1", day(9), 64800, "DELTA AIR LINES AUS-SFO", "Delta"),
			tx("venture", "f2", day(8), 112950, "HOTEL ZETTA SAN FRANCISCO", "Hotel Zetta"),
			tx("venture", "f3", day(6), 18740, "STATE BIRD PROVISIONS", "State Bird Provisions"),
			tx("venture", "f4", day(4), 9612, "WHOLE FOODS MARKET", "Whole Foods"),
			tx("venture", "f5", day(2), 4500, "CITY BIKE SHARE", "City Bike Share"),
			// Charges for the cash claims she makes: one for the taxi to
			// confirm, two for the coworking pass to choose between.
			tx("venture", "f6", day(7), 2500, "CITY TAXI", "City Taxi"),
			tx("venture", "f7", day(10), 3000, "COWORKING DAY PASS", "Coworking"),
			tx("venture", "f8", day(12), 3000, "COWORKING DAY PASS REFUNDABLE", "Coworking"),
		})

	// Three of the founder's charges were the business's: one pair each.
	items := map[string]string{}
	rows, err := f.db.Admin.Query(ctx, `SELECT t.id::text, t.provider_transaction_id FROM public.bank_transactions t WHERE t.tenant_id=$1 AND t.book_id=$2`, f.tenant, personal.id)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, provider string
		if err := rows.Scan(&id, &provider); err != nil {
			t.Fatal(err)
		}
		items[provider] = id
	}
	rows.Close()
	for _, p := range []struct{ item, account, description string }{
		{"f1", "Travel:Air", "Flight to the customer summit, paid by Kit"},
		{"f2", "Travel:Hotel", "Hotel for the customer summit, paid by Kit"},
		{"f3", "Meals", "Dinner with a partner, paid by Kit"},
	} {
		beneficiary := business.accounts[p.account]
		if items[p.item] == "" || beneficiary.ID == "" {
			t.Fatalf("missing %s or %s for a pair", p.item, p.account)
		}
		if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
			_, err := o.RecordPair(ctx, who, ledger.OwnerPair{BankItemID: items[p.item], BeneficiaryBookID: business.id, BeneficiaryAccountID: beneficiary.ID, Description: p.description})
			return err
		}); err != nil {
			t.Fatalf("recording %s for the business: %v", p.item, err)
		}
	}
	seedExpenseReports(t, f, business, personal, card, owedToFounder, day)
}

// seedExpenseReports gives the founder link expense reports in each state
// to look at: a draft (the flight); one returned with a note; the hotel and
// dinner approved and part paid; one overpaid, whose leftover was an error
// now owed back with her return offered; and one amended to a second
// revision. The founder approves her own; the passkey answer is stood in
// for, as the tests do.
func seedExpenseReports(t *testing.T, f *documentServerFixture, business, personal seededBook, card, owedToFounder ledger.Account, day func(int) string) {
	t.Helper()
	ctx := f.db.Context
	var link, identity string
	if err := f.db.Admin.QueryRow(ctx, "SELECT link_id::text FROM public.book_link_terms WHERE tenant_id=$1 AND beneficiary_book_id=$2 AND payer_book_id=$3 AND treatment='owed' AND active LIMIT 1",
		f.tenant, business.id, personal.id).Scan(&link); err != nil {
		t.Fatalf("the founder link: %v", err)
	}
	if err := f.db.Admin.QueryRow(ctx, "SELECT identity_id::text FROM access.sessions WHERE token_hash=$1", access.Hash(f.session)).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	pair := func(description string) string {
		var key string
		if err := f.db.Admin.QueryRow(ctx, `SELECT p.pair_key FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id=p.tenant_id AND e.book_id=p.beneficiary_book_id AND e.id=p.beneficiary_entry_id
 WHERE p.tenant_id=$1 AND p.beneficiary_book_id=$2 AND e.description=$3`, f.tenant, business.id, description).Scan(&key); err != nil {
			t.Fatalf("the pair for %q: %v", description, err)
		}
		return key
	}
	act := func(what string, call func(ledger.TenantAdmin, string) error) {
		if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, call); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	var person string
	act("the person", func(o ledger.TenantAdmin, who string) error {
		p, err := o.SavePerson(ctx, business.id, who, ledger.PersonChange{Name: personal.name, Capacity: "shareholder_employee", PersonalBookID: personal.id})
		person = p.ID
		return err
	})
	travel := business.accounts["Travel:Air"]
	lodging := true
	report := func(claims ...ledger.ClaimInput) string {
		var ref string
		act("a report", func(o ledger.TenantAdmin, who string) error {
			var err error
			if ref, err = o.CreateReport(ctx, who, ledger.NewReport{LinkID: link, PersonID: person}); err != nil {
				return err
			}
			for _, q := range claims {
				if _, err := o.AddReportClaim(ctx, business.id, who, ref, q); err != nil {
					return err
				}
			}
			return nil
		})
		return ref
	}
	cash := func(back int, vendor string, amount, purpose string) ledger.ClaimInput {
		return ledger.ClaimInput{Kind: "cash", Date: day(back), Vendor: vendor, AmountMinor: amount, PaidFromAccountID: card.ID, ProposedAccountID: travel.ID, Purpose: purpose}
	}
	submit := func(ref string, approve bool) {
		var d ledger.ReportDetail
		act("reading the report", func(o ledger.TenantAdmin, _ string) error {
			var err error
			d, err = o.ExpenseReport(ctx, business.id, ref)
			return err
		})
		purpose := "submit"
		if approve {
			purpose = "submit_approve"
		}
		token := "seed-proof-" + ledger.ID()
		if _, err := f.db.Admin.Exec(ctx, `INSERT INTO access.book_deletion_proofs(token_hash,identity_id,epoch,session_hash,tenant_id,book_ref,purpose,bound_to,ceremony,taken_at,verified_at)
 SELECT $1,s.identity_id,i.epoch,s.token_hash,$2,$3,$4,$5,'{}',clock_timestamp(),clock_timestamp() FROM access.sessions s JOIN access.identities i ON i.id=s.identity_id WHERE s.token_hash=$6`,
			access.Hash(token), f.tenant, business.id, purpose, ledger.ProofBinding(d.ID, d.RevisionID, d.Revision, d.SnapshotSHA256), access.Hash(f.session)); err != nil {
			t.Fatal(err)
		}
		act("submitting "+ref, func(o ledger.TenantAdmin, who string) error {
			_, err := o.SubmitReport(ctx, business.id, who, identity, ref,
				ledger.SubmitReport{Revision: d.Revision, SnapshotSHA256: d.SnapshotSHA256, Attestation: true, Approve: approve, ProofToken: token})
			return err
		})
	}
	// Money in and out of the business's checking, recorded against what it
	// owes the founder.
	record := func(provider string) string {
		var item, entry string
		if err := f.db.Admin.QueryRow(ctx, "SELECT id::text FROM public.bank_transactions WHERE tenant_id=$1 AND book_id=$2 AND provider_transaction_id=$3", f.tenant, business.id, provider).Scan(&item); err != nil {
			t.Fatalf("bank item %s: %v", provider, err)
		}
		seedCall(t, f, business.scope, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "seed-" + provider, TransactionID: item,
			Description: "Paid back " + personal.name, OtherAccountID: owedToFounder.ID, Reason: "Expense reimbursement"}, provider)
		if err := f.db.Admin.QueryRow(ctx, "SELECT entry_id::text FROM public.bank_transaction_links WHERE tenant_id=$1 AND transaction_id=$2 AND revoked_at IS NULL", f.tenant, item).Scan(&entry); err != nil {
			t.Fatalf("the entry for %s: %v", provider, err)
		}
		return entry
	}

	draft := report(ledger.ClaimInput{Kind: "cost", PairKey: pair("Flight to the customer summit, paid by Kit"), Purpose: "Flight to the customer summit"})

	returned := report(cash(12, "Airport parking", "4500", "Parking for the summit trip"))
	submit(returned, false)
	act("returning a report", func(o ledger.TenantAdmin, who string) error {
		return o.ReturnReport(ctx, business.id, who, who, returned, "Add the parking receipt, please.")
	})

	partly := report(ledger.ClaimInput{Kind: "cost", PairKey: pair("Hotel for the customer summit, paid by Kit"), Purpose: "Customer summit stay", Lodging: &lodging},
		ledger.ClaimInput{Kind: "cost", PairKey: pair("Dinner with a partner, paid by Kit"), Purpose: "Partner dinner at the summit"})
	submit(partly, true)
	act("paying part of it", func(o ledger.TenantAdmin, who string) error {
		_, err := o.ApplyReportPayment(ctx, business.id, who, partly, record("b5"), 0, nil)
		return err
	})

	overpaid := report(cash(10, "Coworking day pass", "3000", "Working day at the summit"))
	submit(overpaid, true)
	var paid ledger.PaymentResult
	act("overpaying it", func(o ledger.TenantAdmin, who string) error {
		var err error
		paid, err = o.ApplyReportPayment(ctx, business.id, who, overpaid, record("b6"), 0, nil)
		return err
	})
	act("classifying the leftover", func(o ledger.TenantAdmin, who string) error {
		return o.ClassifyResidual(ctx, business.id, who, paid.ResidualID, "error", "", "Paid the whole transfer by mistake")
	})
	record("b7")

	amended := report(cash(8, "Taxi to the venue", "2500", "Taxi to the summit venue"))
	submit(amended, true)
	var claim string
	act("amending a report", func(o ledger.TenantAdmin, who string) error {
		if err := o.AmendReport(ctx, business.id, who, amended, "Part of the ride was personal"); err != nil {
			return err
		}
		d, err := o.ExpenseReport(ctx, business.id, amended)
		if err != nil {
			return err
		}
		claim = d.Lines[0].ClaimID
		return o.EditReportClaim(ctx, business.id, who, amended, claim, ledger.ClaimInput{PortionMinor: "2000"})
	})
	submit(amended, true)

	fmt.Printf("expense reports: %s draft, %s returned, %s part paid, %s overpaid, %s amended\n", draft, returned, partly, overpaid, amended)
}
