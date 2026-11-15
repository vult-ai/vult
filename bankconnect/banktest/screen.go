package banktest

import (
	"context"
	"strings"
	"sync"

	"github.com/vult-ai/vult/bankconnect"
)

// Screen is a synthetic bank connection provider with its own connection
// screen and bank directory, for tests of the paths that open a screen,
// finish a link, sync, sign in again and search for banks. It records what
// it was asked.
type Screen struct {
	// Key is what Provider reports.
	Key string
	// Fail makes a call fail: "start", "sign-in", "finish", "changes",
	// "remove", "search" or "institution".
	Fail map[string]error

	mu        sync.Mutex
	calls     []string
	searches  int
	countries []string
	link      bankconnect.LinkOptions
	signIn    string
}

var (
	_ bankconnect.Connector            = (*Screen)(nil)
	_ bankconnect.SignInAgain          = (*Screen)(nil)
	_ bankconnect.InstitutionDirectory = (*Screen)(nil)
	_ bankconnect.BankCountries        = (*Screen)(nil)
)

// ScreenCountries are the countries a Screen says it serves.
var ScreenCountries = []string{"GB", "IE", "PL", "US"}

func (s *Screen) call(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, name)
	return s.Fail[name]
}

func (s *Screen) Provider() string { return s.Key }

func (s *Screen) StartLink(ctx context.Context, c bankconnect.Credentials, user, webhook string) (bankconnect.LinkStart, error) {
	return s.StartLinkWith(ctx, c, user, webhook, bankconnect.LinkOptions{})
}

func (s *Screen) StartLinkWith(_ context.Context, _ bankconnect.Credentials, _, _ string, o bankconnect.LinkOptions) (bankconnect.LinkStart, error) {
	if err := s.call("start"); err != nil {
		return bankconnect.LinkStart{}, err
	}
	s.mu.Lock()
	s.link = o
	s.mu.Unlock()
	return bankconnect.LinkStart{Provider: s.Key, Token: "link-example", Expires: "2026-11-11T20:00:00Z"}, nil
}

func (s *Screen) StartSignIn(_ context.Context, _ bankconnect.Credentials, _, _, accessToken string) (bankconnect.LinkStart, error) {
	if err := s.call("sign-in"); err != nil {
		return bankconnect.LinkStart{}, err
	}
	s.mu.Lock()
	s.signIn = accessToken
	s.mu.Unlock()
	return bankconnect.LinkStart{Provider: s.Key, Token: "link-again-example"}, nil
}

func (s *Screen) FinishLink(_ context.Context, _ bankconnect.Credentials, publicToken string) (bankconnect.Linked, error) {
	if err := s.call("finish"); err != nil {
		return bankconnect.Linked{}, err
	}
	return bankconnect.Linked{AccessToken: "access-example", ItemID: "item-" + publicToken, InstitutionID: "ins_example", InstitutionName: "Example Bank"}, nil
}

// Changes reports one checking account and, on the first sync, one
// purchase of 45.50.
func (s *Screen) Changes(_ context.Context, _ bankconnect.Credentials, accessToken, cursor string) (bankconnect.Changes, error) {
	if err := s.call("changes"); err != nil {
		return bankconnect.Changes{}, err
	}
	if accessToken != "access-example" {
		return bankconnect.Changes{}, bankconnect.ErrCredentials
	}
	out := bankconnect.Changes{NextCursor: "c1", Accounts: []bankconnect.Account{{ProviderAccountID: "acc-checking", Name: "Example Checking", Mask: "0001",
		Type: "depository", Subtype: "checking", Kind: "cash", Currency: "USD", Scale: 2}}}
	if cursor == "" {
		out.Added = []bankconnect.Transaction{{ProviderAccountID: "acc-checking", ProviderTransactionID: "t1", Date: "2026-03-02", AmountMinor: -4550,
			Currency: "USD", Scale: 2, Description: "Example Office Supply"}}
	}
	return out, nil
}

func (s *Screen) Remove(context.Context, bankconnect.Credentials, string) error {
	return s.call("remove")
}

// SearchInstitutions finds one bank named as asked.
func (s *Screen) SearchInstitutions(_ context.Context, _ bankconnect.Credentials, query string, countries []string) ([]bankconnect.Institution, error) {
	if err := s.call("search"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.searches++
	s.countries = countries
	s.mu.Unlock()
	id := "ins_" + strings.ReplaceAll(strings.ToLower(query), " ", "_")
	return []bankconnect.Institution{{ID: id, Name: query, Provider: s.Key, PrimaryColor: "#123456", Logo: "iVBORw0KGgo="}}, nil
}

func (s *Screen) Institution(_ context.Context, _ bankconnect.Credentials, id string) (bankconnect.Institution, error) {
	if err := s.call("institution"); err != nil {
		return bankconnect.Institution{}, err
	}
	return bankconnect.Institution{ID: id, Name: "Example Bank", Provider: s.Key, URL: "https://bank.example.com", RoutingNumbers: []string{"011000015"}}, nil
}

func (s *Screen) Countries() []string { return ScreenCountries }

// Calls are the calls made so far, in order.
func (s *Screen) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// Searches is how many directory searches were made.
func (s *Screen) Searches() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.searches
}

// SearchedCountries are the countries the last search named.
func (s *Screen) SearchedCountries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.countries
}

// LastLink is what the last new link's screen was asked to start at.
func (s *Screen) LastLink() bankconnect.LinkOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.link
}

// SignedInFor is the access token the last sign-in again was opened for.
func (s *Screen) SignedInFor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.signIn
}
