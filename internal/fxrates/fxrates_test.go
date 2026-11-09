package fxrates

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// provider serves fixed rows keyed by requested date.
func provider(t *testing.T, rows map[string]string) (Client, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		if q.Get("expand") != "providers" || len(q) != 4 {
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		body, ok := rows[q.Get("date")]
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	now := func() time.Time { return time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC) }
	return Client{HTTP: server.Client(), BaseURL: server.URL, Now: now}, &calls
}

func row(date, base, quote, rate string, providers ...string) string {
	list := ""
	for i, p := range providers {
		if i > 0 {
			list += ","
		}
		list += p
	}
	return fmt.Sprintf(`[{"date":%q,"base":%q,"quote":%q,"rate":%s,"providers":[%s]}]`, date, base, quote, rate, list)
}

func TestLookupUsesTheActualObservationDate(t *testing.T) {
	c, calls := provider(t, map[string]string{
		// Saturday's blended row carries Friday's observations forward.
		"2026-11-07": row("2026-11-07", "EUR", "USD", "1.121", `{"key":"A","date":"2026-11-06","rate":1.1219}`, `{"key":"B","date":"2026-11-05","rate":1.1233}`),
		"2026-11-06": row("2026-11-06", "EUR", "USD", "1.1205", `{"key":"A","date":"2026-11-06","rate":1.1219}`, `{"key":"B","date":"2026-11-05","rate":1.1233}`),
	})
	got, err := c.Lookup(context.Background(), "EUR", "USD", "2026-11-07")
	if err != nil {
		t.Fatal(err)
	}
	want := Rate{Base: "EUR", Quote: "USD", Rate: "1.1205", RequestedDate: "2026-11-07", ObservationDate: "2026-11-06", Provenance: Provenance}
	if got != want || *calls != 2 {
		t.Fatalf("got %+v after %d calls, want %+v after 2", got, *calls, want)
	}
}

func TestLookupRefusesStaleWrongAndUncorroboratedRates(t *testing.T) {
	cases := map[string]struct {
		date string
		rows map[string]string
	}{
		"older than seven days": {"2026-11-07", map[string]string{
			"2026-11-07": row("2026-11-07", "EUR", "USD", "1.12", `{"key":"A","date":"2026-10-30","rate":1.12}`),
		}},
		"wrong pair": {"2026-11-06", map[string]string{
			"2026-11-06": row("2026-11-06", "USD", "EUR", "0.89", `{"key":"A","date":"2026-11-06","rate":0.89}`),
		}},
		"observation after the requested date": {"2026-11-06", map[string]string{
			"2026-11-06": row("2026-11-06", "EUR", "USD", "1.12", `{"key":"A","date":"2026-11-07","rate":1.12}`),
		}},
		"no contributing observation": {"2026-11-06", map[string]string{
			"2026-11-06": row("2026-11-06", "EUR", "USD", "1.12"),
		}},
		"zero rate": {"2026-11-06", map[string]string{
			"2026-11-06": row("2026-11-06", "EUR", "USD", "0", `{"key":"A","date":"2026-11-06","rate":1.12}`),
		}},
		"provider error": {"2026-11-06", map[string]string{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := provider(t, tc.rows)
			if _, err := c.Lookup(context.Background(), "EUR", "USD", tc.date); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("got %v, want unavailable", err)
			}
		})
	}
}

func TestLookupValidatesInputWithoutCallingTheProvider(t *testing.T) {
	c, calls := provider(t, nil)
	for _, in := range [][3]string{{"eur", "USD", "2026-11-06"}, {"EUR", "US", "2026-11-06"}, {"EUR", "USD", "2026-11-31"}} {
		if _, err := c.Lookup(context.Background(), in[0], in[1], in[2]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%v: got %v, want invalid", in, err)
		}
	}
	if _, err := c.Lookup(context.Background(), "EUR", "USD", "2027-01-01"); !errors.Is(err, ErrFutureDate) {
		t.Fatalf("future date: got %v", err)
	}
	// Metals, funds and testing codes are quoted by some providers but are not
	// currencies a Book can record.
	for _, code := range []string{"XAU", "XAG", "XTS", "ZZZ"} {
		if _, err := c.Lookup(context.Background(), code, "USD", "2026-11-06"); !errors.Is(err, ErrUnsupportedCurrency) {
			t.Fatalf("%s: got %v, want unsupported", code, err)
		}
	}
	same, err := c.Lookup(context.Background(), "PLN", "PLN", "2026-11-06")
	if err != nil || same.Rate != "1" || *calls != 0 {
		t.Fatalf("same currency: %+v %v after %d calls", same, err, *calls)
	}
}

func TestDecimalIsExact(t *testing.T) {
	for raw, want := range map[string]string{"1.10": "1.1", "1.5e-3": "0.0015", "4.2E2": "420", "0.000123": "0.000123", "007.50": "7.5", "25": "25"} {
		got, err := decimal(raw)
		if err != nil || got != want {
			t.Fatalf("%s: got %q %v, want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"-1", "1e40", "abc", "0.0", ""} {
		if _, err := decimal(raw); err == nil {
			t.Fatalf("%s: accepted", raw)
		}
	}
}
