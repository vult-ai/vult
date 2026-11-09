// Package fxrates looks up historical reference exchange rates for agents.
//
// It asks Frankfurter's v2 blended reference rates for one currency pair on
// one date. Only the pair and the date leave the installation. The answer is
// the latest genuine observation on or before the requested date, at most
// seven days earlier, so a weekend or holiday uses the last real publication
// and says so. A rate is a reference, not what a bank charged: an actual
// billed or received amount always takes precedence.
package fxrates

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vult-ai/vult/ledger"
)

// Provenance names the source in each rate, so a posting records where its
// rate came from.
const Provenance = "Frankfurter v2 blended reference rate (api.frankfurter.dev)"

// MaxAge is how far before the requested date an observation may be.
const MaxAge = 7 * 24 * time.Hour

// ErrUnavailable means no qualified reference rate exists for the pair and
// date. The agent should use the actual amount from the source, or ask the
// owner for a manual rate.
var ErrUnavailable = errors.New("reference rate unavailable for this pair and date; use the actual amount from the statement or receipt, or a manual rate the owner confirms")

// ErrInvalid means the request itself is malformed.
var ErrInvalid = errors.New("invalid request: base and quote are three-letter ISO 4217 codes and date is YYYY-MM-DD")

// ErrUnsupportedCurrency means a code Vult cannot record, such as a metal or
// fund code, even if a provider quotes it.
var ErrUnsupportedCurrency = errors.New("unsupported currency: only active ISO 4217 currencies Vult can record have rates")

// ErrFutureDate means the date has not happened yet, so no rate exists.
var ErrFutureDate = errors.New("no rate yet: the date is in the future")

// Rate matches the valuation rate that post_journal accepts.
type Rate struct {
	Base            string `json:"base"`
	Quote           string `json:"quote"`
	Rate            string `json:"rate"`
	RequestedDate   string `json:"requested_date"`
	ObservationDate string `json:"observation_date"`
	Provenance      string `json:"provenance"`
}

// Client fetches rates. The zero value uses the public endpoint.
type Client struct {
	HTTP    *http.Client
	BaseURL string
	Now     func() time.Time
}

var (
	codePattern     = regexp.MustCompile(`^[A-Z]{3}$`)
	providerDecimal = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]{1,2}))?$`)
	ratePattern     = regexp.MustCompile(`^(0|[1-9][0-9]{0,63})(\.[0-9]{1,64})?$`)
)

type providerRate struct {
	Date      string      `json:"date"`
	Base      string      `json:"base"`
	Quote     string      `json:"quote"`
	Rate      json.Number `json:"rate"`
	Providers []struct {
		Key      string      `json:"key"`
		Date     string      `json:"date"`
		Rate     json.Number `json:"rate"`
		Excluded bool        `json:"excluded"`
	} `json:"providers"`
}

func validDate(s string) bool {
	t, err := time.Parse("2006-01-02", s)
	return err == nil && t.Format("2006-01-02") == s
}

// decimal turns a provider number, possibly in exponent notation, into exact
// positive decimal text without float conversion.
func decimal(raw string) (string, error) {
	m := providerDecimal.FindStringSubmatch(raw)
	if len(raw) > 64 || m == nil {
		return "", ErrUnavailable
	}
	exponent := 0
	if m[3] != "" {
		exponent, _ = strconv.Atoi(m[3])
	}
	if exponent < -18 || exponent > 18 {
		return "", ErrUnavailable
	}
	digits := m[1] + m[2]
	point := len(m[1]) + exponent
	switch {
	case point <= 0:
		digits = "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		digits += strings.Repeat("0", point-len(digits))
	default:
		digits = digits[:point] + "." + digits[point:]
	}
	if strings.Contains(digits, ".") {
		digits = strings.TrimRight(digits, "0")
		digits = strings.TrimSuffix(digits, ".")
	}
	whole := digits
	if i := strings.IndexByte(digits, '.'); i >= 0 {
		whole = digits[:i]
	}
	trimmed := strings.TrimLeft(whole, "0")
	if trimmed == "" {
		trimmed = "0"
	}
	digits = trimmed + strings.TrimPrefix(digits, whole)
	if !ratePattern.MatchString(digits) || strings.Trim(digits, "0.") == "" {
		return "", ErrUnavailable
	}
	return digits, nil
}

func eligible(requested, observed string) bool {
	if !validDate(requested) || !validDate(observed) || observed > requested {
		return false
	}
	r, _ := time.Parse("2006-01-02", requested)
	o, _ := time.Parse("2006-01-02", observed)
	return r.Sub(o) <= MaxAge
}

// Lookup returns the qualified reference rate for one unit of base in quote.
func (c Client) Lookup(ctx context.Context, base, quote, date string) (Rate, error) {
	if !codePattern.MatchString(base) || !codePattern.MatchString(quote) || !validDate(date) {
		return Rate{}, ErrInvalid
	}
	if _, ok := ledger.CurrencyScale(base); !ok {
		return Rate{}, ErrUnsupportedCurrency
	}
	if _, ok := ledger.CurrencyScale(quote); !ok {
		return Rate{}, ErrUnsupportedCurrency
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	if date > now().UTC().Add(24*time.Hour).Format("2006-01-02") {
		return Rate{}, ErrFutureDate
	}
	if base == quote {
		return Rate{Base: base, Quote: quote, Rate: "1", RequestedDate: date, ObservationDate: date, Provenance: "Same currency; no conversion"}, nil
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	}
	endpoint := c.BaseURL
	if endpoint == "" {
		endpoint = "https://api.frankfurter.dev/v2/rates"
	}
	fetch := func(day string) (providerRate, error) {
		var value providerRate
		query := url.Values{"base": {base}, "quotes": {quote}, "date": {day}, "expand": {"providers"}}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
		if err != nil {
			return value, ErrUnavailable
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "Vult (bookkeeping)")
		response, err := client.Do(req)
		if err != nil {
			return value, ErrUnavailable
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return value, ErrUnavailable
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
		if err != nil || len(raw) > 65536 {
			return value, ErrUnavailable
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		var rows []providerRate
		if decoder.Decode(&rows) != nil || len(rows) != 1 {
			return value, ErrUnavailable
		}
		value = rows[0]
		if value.Base != base || value.Quote != quote || !eligible(date, value.Date) {
			return value, ErrUnavailable
		}
		if _, err = decimal(value.Rate.String()); err != nil {
			return value, ErrUnavailable
		}
		return value, nil
	}
	// A blended row's date may be carried forward from earlier publications.
	// The latest contributing observation, excluding outliers, is the real date.
	observation := func(v providerRate) string {
		latest := ""
		for _, p := range v.Providers {
			if p.Excluded {
				continue
			}
			if p.Key == "" || !validDate(p.Date) || p.Date > date || p.Date > v.Date {
				return ""
			}
			if _, err := decimal(p.Rate.String()); err != nil {
				return ""
			}
			if p.Date > latest {
				latest = p.Date
			}
		}
		return latest
	}
	value, err := fetch(date)
	if err != nil {
		return Rate{}, err
	}
	observed := observation(value)
	if observed == "" || !eligible(date, observed) {
		return Rate{}, ErrUnavailable
	}
	if value.Date != observed {
		value, err = fetch(observed)
		if err != nil || value.Date != observed || observation(value) != observed {
			return Rate{}, ErrUnavailable
		}
	}
	rate, err := decimal(value.Rate.String())
	if err != nil {
		return Rate{}, err
	}
	return Rate{Base: base, Quote: quote, Rate: rate, RequestedDate: date, ObservationDate: observed, Provenance: Provenance}, nil
}
