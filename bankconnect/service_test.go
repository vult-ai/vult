package bankconnect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vult-ai/vult/internal/secretbox"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// screenProvider is the key the fake screen connector is registered under.
// The ledger records logins only for the providers it knows, so the fake
// stands in under one of them.
const screenProvider = "mercury"

// fakeScreen is a connector whose screen links one synthetic login and whose
// syncs return queued changes.
type fakeScreen struct {
	mu      sync.Mutex
	calls   []string
	creds   []Credentials
	cursors []string
	changes []Changes
	fail    map[string]error // by call
	signIn  string           // the access token a sign-in was opened for
}

func newFakeScreen() *fakeScreen { return &fakeScreen{fail: map[string]error{}} }

func (f *fakeScreen) call(name string, c Credentials) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.creds = append(f.creds, c)
	return f.fail[name]
}

func (f *fakeScreen) Provider() string { return screenProvider }

func (f *fakeScreen) StartLink(_ context.Context, c Credentials, _, _ string) (LinkStart, error) {
	if err := f.call("start", c); err != nil {
		return LinkStart{}, err
	}
	return LinkStart{Provider: screenProvider, Token: "link-example"}, nil
}

func (f *fakeScreen) StartSignIn(_ context.Context, c Credentials, _, _, accessToken string) (LinkStart, error) {
	if err := f.call("sign-in", c); err != nil {
		return LinkStart{}, err
	}
	f.signIn = accessToken
	return LinkStart{Provider: screenProvider, Token: "link-again-example"}, nil
}

func (f *fakeScreen) FinishLink(_ context.Context, c Credentials, publicToken string) (Linked, error) {
	if err := f.call("finish", c); err != nil {
		return Linked{}, err
	}
	return Linked{AccessToken: "access-example", ItemID: "item-" + publicToken, InstitutionID: "ins_example", InstitutionName: "Example Bank"}, nil
}

func (f *fakeScreen) Changes(_ context.Context, c Credentials, accessToken, cursor string) (Changes, error) {
	if err := f.call("changes", c); err != nil {
		return Changes{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if accessToken != "access-example" {
		return Changes{}, ErrCredentials
	}
	f.cursors = append(f.cursors, cursor)
	next := Changes{NextCursor: cursor + "+"}
	if len(f.changes) > 0 {
		next, f.changes = f.changes[0], f.changes[1:]
	}
	next.Accounts = append(next.Accounts, Account{ProviderAccountID: "acc-checking", Name: "Example Checking", Mask: "0001", Subtype: "checking", Kind: "cash", Currency: "USD", Scale: 2})
	return next, nil
}

func (f *fakeScreen) Remove(_ context.Context, c Credentials, _ string) error {
	return f.call("remove", c)
}

func (f *fakeScreen) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

var exampleKeys = Credentials{Environment: "sandbox", ClientID: "client-example", Secret: "secret-example"}

type serviceFixture struct {
	db      *testutil.Database
	fake    *fakeScreen
	service *Service
	owner   ledger.TenantAdmin
	book    ledger.Book
	actor   string
	changed []string
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	db := testutil.New(t)
	book, _ := db.Book(t, "Example Book")
	owner, err := db.Owner.ForTenant(db.Context, book.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureBookOwner(db.Context, book.ID); err != nil {
		t.Fatal(err)
	}
	var actor string
	if err := db.Admin.QueryRow(db.Context, "SELECT identity_id::text FROM access.owners WHERE tenant_id=$1", book.TenantID).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	f := &serviceFixture{db: db, fake: newFakeScreen(), owner: owner, book: book, actor: actor}
	f.service = &Service{Store: db.Store, Keys: &secretbox.Cached{Source: secretbox.FileSource{Path: filepath.Join(t.TempDir(), "keys.json")}},
		Connectors: map[string]Connector{screenProvider: f.fake}, Changed: func(_, book string) { f.changed = append(f.changed, book) },
		ProviderKeys: func(context.Context, string) (Credentials, error) { return exampleKeys, nil }}
	return f
}

func (f *serviceFixture) link(t *testing.T, publicToken string) (string, ledger.BankSyncTarget) {
	t.Helper()
	id, err := f.service.FinishLink(f.db.Context, f.owner, f.book.TenantID, f.book.ID, f.actor, screenProvider, publicToken)
	if err != nil {
		t.Fatal(err)
	}
	return id, ledger.BankSyncTarget{TenantID: f.book.TenantID, BookID: f.book.ID, ConnectionID: id}
}

func TestServiceLinksSyncsAndNeverStoresTokensInTheClear(t *testing.T) {
	f := newServiceFixture(t)
	ctx := f.db.Context
	// Without keys for the provider, nothing opens.
	keys := f.service.ProviderKeys
	f.service.ProviderKeys = func(context.Context, string) (Credentials, error) { return Credentials{}, ErrNotConfigured }
	if _, err := f.service.StartLink(ctx, f.owner, f.book.TenantID, f.book.ID, f.actor, screenProvider); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("link without keys = %v", err)
	}
	if _, err := f.service.StartLink(ctx, f.owner, f.book.TenantID, f.book.ID, f.actor, "unknown"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("link with an unknown provider = %v", err)
	}
	f.service.ProviderKeys = keys
	start, err := f.service.StartLink(ctx, f.owner, f.book.TenantID, f.book.ID, f.actor, screenProvider)
	if err != nil || start.Token == "" {
		t.Fatalf("start = %+v, %v", start, err)
	}
	f.fake.changes = []Changes{{NextCursor: "c1", Added: []Transaction{{ProviderAccountID: "acc-checking", ProviderTransactionID: "t1", Date: "2026-03-02",
		AmountMinor: -4550, Currency: "USD", Scale: 2, Description: "Example Office Supply"}}}}
	id, target := f.link(t, "public-example")
	// Every call carried this Vult's keys.
	for i, c := range f.fake.creds {
		if c != exampleKeys {
			t.Fatalf("call %s had keys %+v", f.fake.calls[i], c)
		}
	}
	var token []byte
	var environment string
	if err := f.db.Admin.QueryRow(ctx, "SELECT token_sealed,environment FROM public.bank_connections WHERE id=$1", id).Scan(&token, &environment); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(token, []byte("access-example")) || environment != "sandbox" {
		t.Fatal("the token is stored in the clear, or the environment is lost", environment)
	}
	got, err := f.service.Sync(ctx, target)
	if err != nil || got.Added != 1 {
		t.Fatalf("sync = %+v, %v", got, err)
	}
	if len(f.changed) == 0 {
		t.Fatal("a sync with new activity didn't announce it")
	}
	page, _, err := f.owner.BankActivity(ctx, f.book.ID, ledger.BankActivityQuery{Status: "all"})
	if err != nil || len(page.Items) != 1 || page.Items[0].AmountMinor != "-4550" || page.Items[0].Status != "needs_account" {
		t.Fatalf("inbox = %+v, %v", page, err)
	}
	// The sync continued from the cursor it stored.
	if _, err := f.service.Sync(ctx, target); err != nil {
		t.Fatal(err)
	}
	if last := f.fake.cursors[len(f.fake.cursors)-1]; last != "c1" {
		t.Fatalf("second sync started from %q", last)
	}
	// A bank that needs a new sign-in stops syncing and says so.
	f.fake.fail["changes"] = ErrReconnect
	if _, err := f.service.Sync(ctx, target); err == nil {
		t.Fatal("an expired bank sign-in synced")
	}
	views, err := f.owner.BankConnections(ctx, f.book.ID)
	if err != nil || views[0].Status != "needs_attention" || views[0].StatusReason == "" {
		t.Fatalf("status = %+v, %v", views, err)
	}
	// Disconnecting ends the provider's access too.
	delete(f.fake.fail, "changes")
	if err := f.service.Disconnect(ctx, f.owner, f.book.TenantID, f.book.ID, f.actor, id, ""); err != nil {
		t.Fatal(err)
	}
	// The sync timer ends the provider's access, then forgets the token.
	f.service.release(ctx)
	var kept []byte
	if err := f.db.Admin.QueryRow(ctx, "SELECT token_sealed FROM public.bank_connections WHERE id=$1", id).Scan(&kept); err != nil || kept != nil {
		t.Fatal("the token outlived the provider's access", err)
	}
	if f.fake.last() != "remove" {
		t.Fatalf("last provider call = %s", f.fake.last())
	}
}

// A Vult whose keys went away can't sync a login that needs them, and says
// so on the login.
func TestServiceSyncWithoutKeysSaysSo(t *testing.T) {
	f := newServiceFixture(t)
	_, target := f.link(t, "public-example")
	f.service.ProviderKeys = func(context.Context, string) (Credentials, error) { return Credentials{}, ErrNotConfigured }
	if _, err := f.service.Sync(f.db.Context, target); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("synced without keys", err)
	}
	views, err := f.owner.BankConnections(f.db.Context, f.book.ID)
	if err != nil || views[0].Status != "needs_attention" || !strings.Contains(views[0].StatusReason, "no bank connection service") {
		t.Fatalf("status = %+v, %v", views, err)
	}
}

func TestServiceTokensAreBoundToTheirLogin(t *testing.T) {
	f := newServiceFixture(t)
	ctx := f.db.Context
	first, _ := f.link(t, "public-1")
	// A second login whose stored token is a copy of the first one's.
	var token []byte
	if err := f.db.Admin.QueryRow(ctx, "SELECT token_sealed FROM public.bank_connections WHERE id=$1", first).Scan(&token); err != nil {
		t.Fatal(err)
	}
	second, err := f.owner.AddBankConnection(ctx, f.book.ID, f.actor, ledger.NewBankConnection{Provider: screenProvider, Environment: "sandbox", ProviderItemID: "item-copy", TokenSealed: token, KeyVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Sync(ctx, ledger.BankSyncTarget{TenantID: f.book.TenantID, BookID: f.book.ID, ConnectionID: second}); err == nil {
		t.Fatal("a token copied to another login opened")
	}
}

func TestSigningInAgainReopensTheSameLogin(t *testing.T) {
	f := newServiceFixture(t)
	ctx := f.db.Context
	id, target := f.link(t, "public-example")
	if err := f.db.Store.RecordBankSyncProblem(ctx, target, "The bank needs you to sign in again. Reconnect it to keep syncing.", true); err != nil {
		t.Fatal(err)
	}
	start, err := f.service.StartSignIn(ctx, f.owner, f.book.TenantID, f.book.ID, f.actor, id)
	if err != nil || start.Token == "" {
		t.Fatalf("start = %+v, %v", start, err)
	}
	// The provider's screen opens for the existing login, not a new one.
	if f.fake.signIn != "access-example" {
		t.Fatalf("sign-in opened for %q", f.fake.signIn)
	}
	if err := f.service.FinishSignIn(ctx, f.owner, f.book.TenantID, f.book.ID, id); err != nil {
		t.Fatal(err)
	}
	// Access this Vult's key can't open, as after Books are copied from
	// another Vult, can't sign in again: the owner is told to connect anew.
	other := &Service{Store: f.service.Store, Connectors: f.service.Connectors, Keys: f.service.Keys}
	if _, err := f.db.Admin.Exec(ctx, "UPDATE public.bank_connections SET token_sealed=set_byte(token_sealed, octet_length(token_sealed)-1, get_byte(token_sealed, octet_length(token_sealed)-1) # 1) WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	var refused *ledger.RefusedError
	if _, err := other.StartSignIn(ctx, f.owner, f.book.TenantID, f.book.ID, f.actor, id); !errors.As(err, &refused) || !strings.Contains(refused.Reason, "Disconnect it, then connect it again") {
		t.Fatalf("signing in again with access that can't be opened: %v", err)
	}
	if _, err := f.db.Admin.Exec(ctx, "UPDATE public.bank_connections SET token_sealed=set_byte(token_sealed, octet_length(token_sealed)-1, get_byte(token_sealed, octet_length(token_sealed)-1) # 1) WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Sync(ctx, target); err != nil {
		t.Fatal(err)
	}
	logins, err := f.owner.BankConnections(ctx, f.book.ID)
	if err != nil || len(logins) != 1 || logins[0].Status != "active" {
		t.Fatalf("after signing in again = %+v, %v", logins, err)
	}
}

// A provider's own words for a refusal reach the owner only when the error
// offers them.
func TestReasonIsTheProvidersOwnWords(t *testing.T) {
	if Reason(ErrProvider) != "" || Reason(nil) != "" {
		t.Fatal("a reason from a plain error")
	}
	if got := Reason(fmt.Errorf("wrapped: %w", reasonedError{})); got != "The bank is closed for maintenance." {
		t.Fatal(got)
	}
}

type reasonedError struct{}

func (reasonedError) Error() string       { return "refused" }
func (reasonedError) OwnerReason() string { return "The bank is closed for maintenance." }

// A bank read with the owner's own token connects and syncs without any
// provider keys; the first sync's balance checks are kept for the summary,
// and an account that doesn't add up tells the owner why on the login.
func TestServiceConnectsMercuryWithATokenAndKeepsItsChecks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hidden string
	}{{"adds up", ""}, {"a missing transaction", "tx-deposit"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServiceFixture(t)
			ctx := f.db.Context
			fake := newFakeMercury()
			if tc.hidden != "" {
				fake.hidden[tc.hidden] = true
			}
			mercury, _ := mercuryAt(t, fake, "2026-11-10")
			f.service.Connectors["mercury"] = mercury
			if _, err := f.service.ConnectToken(ctx, f.owner, f.book.TenantID, f.book.ID, f.actor, "mercury", "secret-token:mercury_wrong"); !errors.Is(err, ErrCredentials) {
				t.Fatal("a refused token connected", err)
			}
			id, err := f.service.ConnectToken(ctx, f.owner, f.book.TenantID, f.book.ID, f.actor, "mercury", mercuryToken)
			if err != nil {
				t.Fatal(err)
			}
			var sealed []byte
			if err := f.db.Admin.QueryRow(ctx, "SELECT token_sealed FROM public.bank_connections WHERE id=$1", id).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte(mercuryToken)) {
				t.Fatal("the token was stored in the clear", err)
			}
			if _, err := f.service.Sync(ctx, ledger.BankSyncTarget{TenantID: f.book.TenantID, BookID: f.book.ID, ConnectionID: id}); err != nil {
				t.Fatal("sync without provider keys", err)
			}
			views, err := f.owner.BankConnections(ctx, f.book.ID)
			if err != nil || len(views) != 1 {
				t.Fatal(views, err)
			}
			var checks []struct {
				Name         string `json:"name"`
				Transactions int    `json:"transactions"`
				Passed       bool   `json:"passed"`
				Problem      string `json:"problem"`
			}
			if json.Unmarshal(views[0].FirstSyncChecks, &checks) != nil || len(checks) != 3 {
				t.Fatalf("first sync checks: %s", views[0].FirstSyncChecks)
			}
			failed := 0
			for _, c := range checks {
				if !c.Passed {
					failed++
				}
			}
			if tc.hidden == "" {
				if failed != 0 || views[0].Provider != "mercury" || views[0].StatusReason != "" || len(views[0].Accounts) != 3 {
					t.Fatalf("a good first sync: %+v %+v", views[0], checks)
				}
				return
			}
			if failed != 1 || !strings.Contains(views[0].StatusReason, "don't add up") {
				t.Fatalf("an account that doesn't add up: %q %+v", views[0].StatusReason, checks)
			}
		})
	}
}
