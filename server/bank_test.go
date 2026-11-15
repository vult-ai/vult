package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/bankconnect"
	"github.com/vult-ai/vult/bankconnect/banktest"
	"github.com/vult-ai/vult/internal/secretbox"
	"github.com/vult-ai/vult/ledger"
)

// screenKey is the key the synthetic screen provider is registered under in
// tests. The ledger records logins only for the providers it knows, so the
// synthetic one stands in under one of them.
const screenKey = "mercury"

// exampleScreen is how the tests' server offers the synthetic provider.
func exampleScreen() *BankScreen {
	return &BankScreen{Provider: screenKey, Name: "Example Connect", Script: "/bank-screen.js",
		ScriptSources: []string{"https://cdn.example.com"}, FrameSources: []string{"https://cdn.example.com"}, ConnectSources: []string{"https://api.example.com"}}
}

// screenOf is the synthetic provider a bankServer's banks use.
func screenOf(t *testing.T, banks *bankconnect.Service) *banktest.Screen {
	t.Helper()
	fake, ok := banks.Connectors[screenKey].(*banktest.Screen)
	if !ok {
		t.Fatal("no synthetic screen provider")
	}
	return fake
}

func bankServer(t *testing.T) (*documentServerFixture, *http.Server, *bankconnect.Service) {
	t.Helper()
	return bankServerWith(t, nil)
}

// bankServerWith is bankServer with options a test sets.
func bankServerWith(t *testing.T, set func(*Options)) (*documentServerFixture, *http.Server, *bankconnect.Service) {
	t.Helper()
	f := documentServer(t)
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	banks := &bankconnect.Service{Store: ledger.Store{Runtime: f.db.Runtime}, Connectors: map[string]bankconnect.Connector{screenKey: &banktest.Screen{Key: screenKey}},
		Keys: &secretbox.Cached{Source: secretbox.FileSource{Path: filepath.Join(t.TempDir(), "bank-keys.json")}}}
	options := Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: login},
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}}, Banks: banks, BankScreen: exampleScreen(), Documents: f.vault}
	if set != nil {
		set(&options)
	}
	s, err := New(configuration(), options)
	if err != nil {
		t.Fatal(err)
	}
	return f, s, banks
}

func bankCall(t *testing.T, s *http.Server, session, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if method == http.MethodPost {
		r = settingsRequest(session, body)
		r.URL.Path = path
	} else {
		r = browserRequest(method, path, session)
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	return w
}

func TestOwnerConnectsABankThroughTheProvidersScreen(t *testing.T) {
	var arrived []ledger.AgentTarget
	f, s, banks := bankServerWith(t, func(o *Options) {
		o.ActivityArrived = func(_ context.Context, t ledger.AgentTarget, _ OwnerAct) { arrived = append(arrived, t) }
	})
	ctx := f.db.Context
	book := f.book.ID
	w := bankCall(t, s, f.session, http.MethodGet, bankPath+"?book_id="+book, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"connections":[]`) || strings.Contains(w.Body.String(), `"keys"`) || strings.Contains(w.Body.String(), `"settings"`) {
		t.Fatal("overview", w.Code, w.Body.String())
	}
	// The provider's screen is the one connector, opened by its own script.
	if w := bankCall(t, s, f.session, http.MethodGet, bankConnectorsPath+"?book_id="+book, ""); w.Code != http.StatusOK ||
		strings.TrimSpace(w.Body.String()) != `{"connectors":[{"id":"`+screenKey+`","name":"Example Connect","kinds":["bank","card"],"route":"partner","start":"screen","ready":true,"script":"/bank-screen.js"}]}` {
		t.Fatal("connectors", w.Code, w.Body.String())
	}
	// No route takes provider keys from the owner.
	if w := bankCall(t, s, f.session, http.MethodPost, "/api/v1/bank/settings", `{"book_id":"`+book+`","environment":"sandbox","client_id":"x","secret":"y","expected_revision":0}`); w.Code != http.StatusNotFound {
		t.Fatal("owner keys were taken", w.Code, w.Body.String())
	}
	// The owner opens the provider's screen, links a bank, and Vult syncs it.
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "link-example") {
		t.Fatal("link start", w.Code, w.Body.String())
	}
	w = bankCall(t, s, f.session, http.MethodPost, bankLinkFinishPath, `{"book_id":"`+book+`","public_token":"public-example"}`)
	var linked struct {
		ConnectionID string `json:"connection_id"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &linked) != nil || !ledger.ValidID(linked.ConnectionID) {
		t.Fatal("link finish", w.Code, w.Body.String())
	}
	var leaked bool
	if err := f.db.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.bank_connections WHERE position('access-example'::bytea IN token_sealed)>0)`).Scan(&leaked); err != nil || leaked {
		t.Fatal("the access token was stored in the clear", err)
	}
	if _, err := banks.Sync(ctx, ledger.BankSyncTarget{TenantID: f.tenant, BookID: book, ConnectionID: linked.ConnectionID}); err != nil {
		t.Fatal(err)
	}
	var overview bankOverview
	w = bankCall(t, s, f.session, http.MethodGet, bankPath+"?book_id="+book, "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &overview) != nil || len(overview.Connections) != 1 || len(overview.Connections[0].Accounts) != 1 {
		t.Fatal("overview after link", w.Code, w.Body.String())
	}
	// Link the bank account to a cash Book account, then record the activity
	// with an entry an agent already posted.
	var c ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		c, err = o.CreateConnection(ctx, book, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: book, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	checking := f.db.Account(t, scope, "1000", "asset")
	supplies := f.db.Account(t, scope, "6100", "expense")
	bankAccount := overview.Connections[0].Accounts[0].ID
	if w := bankCall(t, s, f.session, http.MethodPost, bankAccountLinkPath, `{"book_id":"`+book+`","bank_account_id":"`+bankAccount+`","account_id":"`+supplies.ID+`"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatal("a cash account linked to an expense", w.Code, w.Body.String())
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankAccountLinkPath, `{"book_id":"`+book+`","bank_account_id":"`+bankAccount+`","account_id":"`+checking.ID+`"}`); w.Code != http.StatusOK {
		t.Fatal("account link", w.Code, w.Body.String())
	}
	// The refused link woke nobody; the linked account's activity is the
	// agent's to work now.
	if len(arrived) != 1 || arrived[0].BookID != book {
		t.Fatal("the agent is told when a bank account's activity can be recorded", arrived)
	}
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "supplies", EconomicDate: "2026-03-03", AccountingDate: "2026-03-03", Description: "Office supplies",
		Lines: []ledger.PostLine{{AccountID: supplies.ID, Side: "debit", AmountMinor: "4550"}, {AccountID: checking.ID, Side: "credit", AmountMinor: "4550"}}})
	var activity struct {
		Page        ledger.BankActivityPage `json:"page"`
		Suggestions []ledger.BankSuggestion `json:"suggestions"`
	}
	w = bankCall(t, s, f.session, http.MethodGet, bankActivityPath+"?book_id="+book, "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &activity) != nil || len(activity.Page.Items) != 1 || len(activity.Suggestions) != 1 {
		t.Fatal("activity", w.Code, w.Body.String())
	}
	accept := `{"book_id":"` + book + `","operation":"link_bank_activity","arguments":{"request_key":"accept-1","transaction_id":"` + activity.Page.Items[0].ID + `","entry_id":"` + activity.Suggestions[0].EntryID + `"}}`
	if w := accountChange(t, s, f.session, accept); w.Code != http.StatusOK {
		t.Fatal("owner link", w.Code, w.Body.String())
	}
	w = bankCall(t, s, f.session, http.MethodGet, bankActivityPath+"?book_id="+book, "")
	if json.Unmarshal(w.Body.Bytes(), &activity) != nil || activity.Page.Summary.Unrecorded != 0 {
		t.Fatal("still unrecorded", w.Body.String())
	}
	// Disconnecting keeps the history.
	if w := bankCall(t, s, f.session, http.MethodPost, bankDisconnectPath, `{"book_id":"`+book+`","connection_id":"`+linked.ConnectionID+`"}`); w.Code != http.StatusOK {
		t.Fatal("disconnect", w.Code, w.Body.String())
	}
	w = bankCall(t, s, f.session, http.MethodGet, bankActivityPath+"?book_id="+book+"&status=recorded", "")
	if json.Unmarshal(w.Body.Bytes(), &activity) != nil || len(activity.Page.Items) != 1 {
		t.Fatal("history lost on disconnect", w.Body.String())
	}
}

func TestBankRoutesNeedAnOwnedBook(t *testing.T) {
	f, s, _ := bankServer(t)
	other := access.Identity{Issuer: f.identity.Issuer, Subject: "other", AuthenticatedAt: time.Now().UTC()}
	if _, err := f.operator.BindOwner(f.db.Context, "", other); err != nil {
		t.Fatal(err)
	}
	session, err := f.sessions.IssueSession(f.db.Context, other)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{bankPath + "?book_id=" + f.book.ID, bankActivityPath + "?book_id=" + f.book.ID} {
		if w := bankCall(t, s, session, http.MethodGet, path, ""); w.Code == http.StatusOK {
			t.Fatal("another owner read this Book's bank", path)
		}
	}
	if w := bankCall(t, s, session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+f.book.ID+`"}`); w.Code == http.StatusOK {
		t.Fatal("another owner opened a screen for this Book")
	}
	if w := bankCall(t, s, "", http.MethodGet, bankPath+"?book_id="+f.book.ID, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("signed out", w.Code)
	}
}

func TestLiveUpdatesAnnounceWhichBookChanged(t *testing.T) {
	f, _, _ := bankServer(t)
	events := newBookEvents(f.db.Runtime)
	ch, done, ok := events.subscribe(f.tenant, f.book.ID)
	if !ok {
		t.Fatal("no stream")
	}
	defer done()
	var c ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		c, err = o.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	a, b := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
	// The listener may still be starting; post until a notice arrives.
	deadline := time.After(10 * time.Second)
	for i := 0; ; i++ {
		f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "live-" + string(rune('a'+i)), EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example",
			Lines: []ledger.PostLine{{AccountID: a.ID, Side: "debit", AmountMinor: "1"}, {AccountID: b.ID, Side: "credit", AmountMinor: "1"}}})
		select {
		case area := <-ch:
			if area != "entries" {
				t.Fatalf("area = %q", area)
			}
			return
		case <-time.After(500 * time.Millisecond):
		case <-deadline:
			t.Fatal("no live update")
		}
	}
}

// A second Book connects through the same provider screen, and a new
// login's accounts are linked in one step, creating the Book accounts the
// owner accepts.
func TestLinkingEveryAccountOfANewLoginAtOnce(t *testing.T) {
	f, s, banks := bankServer(t)
	ctx := f.db.Context
	var second ledger.Book
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		second, err = o.CreateBook(ctx, "Example Second Book", f.book.Currency, f.book.Scale)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+second.ID+`"}`); w.Code != http.StatusOK {
		t.Fatal("link start in the second Book", w.Code, w.Body.String())
	}
	w := bankCall(t, s, f.session, http.MethodPost, bankLinkFinishPath, `{"book_id":"`+second.ID+`","public_token":"public-example"}`)
	var linked struct {
		ConnectionID string `json:"connection_id"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &linked) != nil {
		t.Fatal("link finish", w.Code, w.Body.String())
	}
	if _, err := banks.Sync(ctx, ledger.BankSyncTarget{TenantID: f.tenant, BookID: second.ID, ConnectionID: linked.ConnectionID}); err != nil {
		t.Fatal("sync", err)
	}
	var overview bankOverview

	// The new login's checking account has nothing to link to: Vult
	// proposes a cash account, by the bank's type.
	w = bankCall(t, s, f.session, http.MethodGet, bankPath+"?book_id="+second.ID, "")
	if json.Unmarshal(w.Body.Bytes(), &overview) != nil || len(overview.Connections) != 1 || len(overview.Connections[0].Accounts) != 1 {
		t.Fatal("overview", w.Body.String())
	}
	account := overview.Connections[0].Accounts[0]
	if account.SuggestedCreate == nil || account.SuggestedCreate.Class != "asset" || account.SuggestedCreate.Role != "cash" || !strings.Contains(account.SuggestedCreate.Name, "0001") {
		t.Fatalf("proposed account: %+v", account.SuggestedCreate)
	}
	linkAll := func(links any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"book_id": second.ID, "links": links})
		return bankCall(t, s, f.session, http.MethodPost, bankAccountsLinkAll, string(body))
	}
	// All or nothing: a choice that doesn't fit leaves no account behind.
	wrong := ledger.SuggestedAccount{Name: "Example card", Class: "liability", Role: "card"}
	if w := linkAll([]ledger.BankAccountLinkChoice{{BankAccountID: account.ID, Create: &wrong}}); w.Code != http.StatusUnprocessableEntity {
		t.Fatal("a card account for a checking account", w.Code, w.Body.String())
	}
	var accounts int
	f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.accounts WHERE tenant_id=$1 AND book_id=$2", f.tenant, second.ID).Scan(&accounts)
	if accounts != 0 {
		t.Fatal("a refused link left an account", accounts)
	}
	w = linkAll([]ledger.BankAccountLinkChoice{{BankAccountID: account.ID, Create: account.SuggestedCreate}})
	var done struct {
		Links       []ledger.BankAccountLinked `json:"links"`
		SyncStarted bool                       `json:"sync_started"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &done) != nil || len(done.Links) != 1 || !done.Links[0].Created || !ledger.ValidID(done.Links[0].AccountID) || !done.SyncStarted {
		t.Fatal("link all", w.Code, w.Body.String())
	}
	w = bankCall(t, s, f.session, http.MethodGet, bankPath+"?book_id="+second.ID, "")
	if json.Unmarshal(w.Body.Bytes(), &overview) != nil || overview.Connections[0].Accounts[0].AccountID != done.Links[0].AccountID {
		t.Fatal("linked", w.Body.String())
	}
}

// A keyring this Mac couldn't keep is a plain refusal: nothing was saved, and
// the owner is told why in words, not that Vult didn't answer.
func TestAKeyringThatCantBeKeptIsSaidPlainly(t *testing.T) {
	w := httptest.NewRecorder()
	if !bankAnswer(w, fmt.Errorf("%w: the enclave key couldn't be created: Permission denied", secretbox.ErrKeyStore)) {
		t.Fatal("not answered")
	}
	var body struct{ Error, Message string }
	if w.Code != http.StatusServiceUnavailable || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error != "key_store" ||
		!strings.HasPrefix(body.Message, "Not saved.") || strings.Contains(body.Message, "Permission denied") {
		t.Fatal(w.Code, w.Body.String())
	}
}

// A Vult built without a provider screen offers no screen connector and
// refuses every screen route in words: its Books bring activity in by upload
// and through agents.
func TestWithoutABankScreenNoScreenIsOffered(t *testing.T) {
	f, s, _ := bankServerWith(t, func(o *Options) { o.BankScreen = nil })
	book := f.book.ID
	if w := bankCall(t, s, f.session, http.MethodGet, bankConnectorsPath+"?book_id="+book, ""); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"connectors":[]}` {
		t.Fatal("connectors", w.Code, w.Body.String())
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, bankLinkStartPath, `{"book_id":"` + book + `"}`},
		{http.MethodPost, bankLinkFinishPath, `{"book_id":"` + book + `","public_token":"public-example"}`},
		{http.MethodGet, bankPopularPath + "?book_id=" + book, ""},
		{http.MethodGet, bankSearchPath + "?book_id=" + book + "&q=example", ""},
	} {
		w := bankCall(t, s, f.session, c.method, c.path, c.body)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"error":"refused"`) || !strings.Contains(w.Body.String(), "No bank connection service is available on this Vult") {
			t.Fatal(c.path, w.Code, w.Body.String())
		}
	}
	// The rest of the bank routes still answer.
	if w := bankCall(t, s, f.session, http.MethodGet, bankPath+"?book_id="+book, ""); w.Code != http.StatusOK {
		t.Fatal("overview", w.Code, w.Body.String())
	}
}

// A provider screen is accepted only when its provider is a connector this
// server has, its script is a path on this origin, and every source is a
// plain https origin.
func TestABankScreenIsChecked(t *testing.T) {
	f := documentServer(t)
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	start := func(screen *BankScreen) error {
		banks := &bankconnect.Service{Store: ledger.Store{Runtime: f.db.Runtime}, Connectors: map[string]bankconnect.Connector{screenKey: &banktest.Screen{Key: screenKey}},
			Keys: &secretbox.Cached{Source: secretbox.FileSource{Path: filepath.Join(t.TempDir(), "bank-keys.json")}}}
		_, err := New(configuration(), Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: login},
			Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, Banks: banks, BankScreen: screen, Documents: f.vault})
		return err
	}
	if err := start(exampleScreen()); err != nil {
		t.Fatal("a good screen", err)
	}
	if err := start(&BankScreen{Provider: screenKey, Script: "/bank-screen.js"}); err != nil {
		t.Fatal("a screen with no other origins", err)
	}
	if err := start(&BankScreen{Provider: screenKey, Script: "/bank-screen.js", ScriptSources: []string{"https://cdn.example.com:8443"}}); err != nil {
		t.Fatal("an origin with a port", err)
	}
	for name, change := range map[string]func(*BankScreen){
		"no provider":            func(b *BankScreen) { b.Provider = "" },
		"an unknown provider":    func(b *BankScreen) { b.Provider = "other" },
		"no script":              func(b *BankScreen) { b.Script = "" },
		"a relative script":      func(b *BankScreen) { b.Script = "bank-screen.js" },
		"another origin's path":  func(b *BankScreen) { b.Script = "//cdn.example.com/bank-screen.js" },
		"a script URL":           func(b *BankScreen) { b.Script = "https://cdn.example.com/bank-screen.js" },
		"a script with a query":  func(b *BankScreen) { b.Script = "/bank-screen.js?v=1" },
		"a script with a quote":  func(b *BankScreen) { b.Script = "/bank-screen.js'" },
		"http":                   func(b *BankScreen) { b.ScriptSources = []string{"http://cdn.example.com"} },
		"a path":                 func(b *BankScreen) { b.FrameSources = []string{"https://cdn.example.com/link"} },
		"a trailing slash":       func(b *BankScreen) { b.FrameSources = []string{"https://cdn.example.com/"} },
		"a query":                func(b *BankScreen) { b.ConnectSources = []string{"https://api.example.com?x=1"} },
		"credentials":            func(b *BankScreen) { b.ConnectSources = []string{"https://user@api.example.com"} },
		"a wildcard":             func(b *BankScreen) { b.ScriptSources = []string{"https://*.example.com"} },
		"another directive":      func(b *BankScreen) { b.ScriptSources = []string{"https://cdn.example.com; script-src *"} },
		"two origins in one":     func(b *BankScreen) { b.ConnectSources = []string{"https://a.example.com https://b.example.com"} },
		"a keyword":              func(b *BankScreen) { b.ScriptSources = []string{"'unsafe-inline'"} },
		"an empty source":        func(b *BankScreen) { b.FrameSources = []string{""} },
		"a scheme without hosts": func(b *BankScreen) { b.ScriptSources = []string{"https:"} },
	} {
		screen := exampleScreen()
		change(screen)
		if err := start(screen); !errors.Is(err, ErrConfiguration) {
			t.Error("accepted:", name, err)
		}
	}
}
