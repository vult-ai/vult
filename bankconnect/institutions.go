package bankconnect

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vult-ai/vult/ledger"
)

// Institution is a bank the owner can pick before the provider's screen
// opens. Logo is a base64 PNG, and PrimaryColor a hex color, when the
// provider has them.
type Institution struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Provider       string   `json:"provider"`
	Logo           string   `json:"logo,omitempty"`
	PrimaryColor   string   `json:"primary_color,omitempty"`
	URL            string   `json:"url,omitempty"`
	RoutingNumbers []string `json:"routing_numbers,omitempty"`
}

// LinkOptions narrow the provider's screen to what the owner picked.
type LinkOptions struct {
	// RoutingNumber puts the institution with this routing number first.
	RoutingNumber string
	// Countries are the countries whose banks the screen offers, when they
	// differ from the connector's own.
	Countries []string
}

// InstitutionDirectory is a connector whose banks can be searched before
// its screen opens, and whose screen can start at the bank picked.
type InstitutionDirectory interface {
	// SearchInstitutions searches banks in countries, or the connector's own
	// countries when none are given.
	SearchInstitutions(ctx context.Context, c Credentials, query string, countries []string) ([]Institution, error)
	Institution(ctx context.Context, c Credentials, id string) (Institution, error)
	StartLinkWith(ctx context.Context, c Credentials, user, webhook string, o LinkOptions) (LinkStart, error)
}

// BankCountries is a connector that says which countries' banks it serves,
// for the picker's country switch.
type BankCountries interface {
	Countries() []string
}

// ValidRoutingNumber reports whether s is a nine-digit ABA routing number
// with a valid check digit.
func ValidRoutingNumber(s string) bool {
	if len(s) != 9 {
		return false
	}
	sum := 0
	for i, weight := range []int{3, 7, 1, 3, 7, 1, 3, 7, 1} {
		d := int(s[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		sum += d * weight
	}
	return sum%10 == 0
}

// PopularInstitutions are the banks shown before the owner types, looked up
// by name in the provider's directory.
var PopularInstitutions = []string{"Chase", "Bank of America", "Wells Fargo", "Citibank", "Capital One", "American Express",
	"U.S. Bank", "PNC", "Charles Schwab", "Ally Bank", "Discover", "Navy Federal Credit Union"}

// institutionCache keeps what the provider said about banks for a day:
// searches by environment and query, and banks by environment and id.
type institutionCache struct {
	mu      sync.Mutex
	entries map[string]cachedInstitutions
	now     func() time.Time
}

type cachedInstitutions struct {
	at   time.Time
	list []Institution
}

const (
	institutionCacheFor  = 24 * time.Hour
	institutionCacheSize = 2048
)

func (c *institutionCache) get(key string) ([]Institution, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.clock().Sub(e.at) > institutionCacheFor {
		return nil, false
	}
	return e.list, true
}

func (c *institutionCache) put(key string, list []Institution) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= institutionCacheSize {
		c.entries = map[string]cachedInstitutions{}
	}
	c.entries[key] = cachedInstitutions{at: c.clock(), list: list}
}

func (c *institutionCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// normalQuery is a search as the cache keys it: trimmed, lower case, and
// at most 80 characters.
func normalQuery(q string) (string, bool) {
	q = strings.ToLower(strings.Join(strings.Fields(q), " "))
	if !utf8.ValidString(q) || len(q) > 80 {
		return "", false
	}
	return q, true
}

// directory is the provider's bank directory, with this Vult's keys for it.
func (s *Service) directory(ctx context.Context, provider string) (InstitutionDirectory, Credentials, string, error) {
	c, err := s.connector(provider)
	if err != nil {
		return nil, Credentials{}, "", err
	}
	d, ok := c.(InstitutionDirectory)
	if !ok {
		return nil, Credentials{}, "", ErrUnsupported
	}
	creds, err := s.providerKeys(ctx, provider)
	if err != nil {
		return nil, Credentials{}, "", err
	}
	return d, creds, provider + "/" + creds.Environment, nil
}

// Institutions finds banks by name in a provider's directory; an empty
// query lists the popular ones. Answers are kept for a day, for every Book
// on the same provider environment.
func (s *Service) Institutions(ctx context.Context, provider, query string, countries []string) ([]Institution, error) {
	q, ok := normalQuery(query)
	if !ok {
		return nil, ledger.ErrInvalid
	}
	d, creds, env, err := s.directory(ctx, provider)
	if err != nil {
		return nil, err
	}
	// Each set of countries has its own answers.
	key := searchKey(env, countries, q)
	if list, ok := s.institutions.get(key); ok {
		return list, nil
	}
	var list []Institution
	if q != "" {
		if list, err = d.SearchInstitutions(ctx, creds, q, countries); err != nil {
			return nil, err
		}
	} else {
		seen := map[string]bool{}
		for _, name := range PopularInstitutions {
			found, err := d.SearchInstitutions(ctx, creds, name, countries)
			if err != nil {
				return nil, err
			}
			// The first answer for each name is the bank meant.
			if len(found) > 0 && !seen[found[0].ID] {
				seen[found[0].ID] = true
				list = append(list, found[0])
			}
		}
	}
	s.institutions.put(key, list)
	for _, i := range list {
		s.institutions.put(env+"/id/"+i.ID, []Institution{i})
	}
	return list, nil
}

// InstitutionByID reads one bank from a provider's directory.
func (s *Service) InstitutionByID(ctx context.Context, provider, id string) (Institution, error) {
	if id == "" || len(id) > 64 || strings.ContainsAny(id, "/ \t\n") {
		return Institution{}, ledger.ErrInvalid
	}
	d, creds, env, err := s.directory(ctx, provider)
	if err != nil {
		return Institution{}, err
	}
	if list, ok := s.institutions.get(env + "/id/" + id); ok && len(list) == 1 {
		return list[0], nil
	}
	i, err := d.Institution(ctx, creds, id)
	if err != nil {
		return Institution{}, err
	}
	s.institutions.put(env+"/id/"+id, []Institution{i})
	return i, nil
}

// StartLinkWith opens the provider's screen for a new login, starting at
// the bank the owner picked.
func (s *Service) StartLinkWith(ctx context.Context, o ledger.TenantAdmin, tenant, book, actor, provider string, opts LinkOptions) (LinkStart, error) {
	if opts.RoutingNumber != "" && !ValidRoutingNumber(opts.RoutingNumber) {
		return LinkStart{}, ledger.ErrInvalid
	}
	c, err := s.connector(provider)
	if err != nil {
		return LinkStart{}, err
	}
	creds, err := s.providerKeys(ctx, provider)
	if err != nil {
		return LinkStart{}, err
	}
	if d, ok := c.(InstitutionDirectory); ok {
		return d.StartLinkWith(ctx, creds, actor, s.WebhookURL, opts)
	}
	return c.StartLink(ctx, creds, actor, s.WebhookURL)
}

// currencyCountries are the countries whose banks hold a currency, for a
// Book that records no country of its own.
var currencyCountries = map[string][]string{"USD": {"US"}, "CAD": {"CA"}, "GBP": {"GB"}, "DKK": {"DK"}, "NOK": {"NO"}, "SEK": {"SE"}, "PLN": {"PL"},
	"EUR": {"IE", "FR", "ES", "NL", "DE", "IT", "PT", "BE", "AT", "FI", "EE", "LT", "LV"}}

// CountriesFor returns the bank countries for a set of currencies, sorted
// and without repeats; nil when none is served.
func CountriesFor(currencies []string) []string {
	seen := map[string]bool{}
	for _, c := range currencies {
		for _, country := range currencyCountries[c] {
			seen[country] = true
		}
	}
	return sortedCountries(seen)
}

func sortedCountries(seen map[string]bool) []string {
	var out []string
	for c := range seen {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

func searchKey(env string, countries []string, query string) string {
	return env + "/" + strings.Join(countries, ",") + "/search/" + query
}

// Countries lists, sorted, the countries whose banks a provider's connector
// serves; nil when the connector doesn't say.
func (s *Service) Countries(provider string) []string {
	c, ok := s.Connectors[provider].(BankCountries)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for _, country := range c.Countries() {
		seen[country] = true
	}
	return sortedCountries(seen)
}
