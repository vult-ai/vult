package bankconnect

import (
	"strings"
	"testing"
	"time"
)

func TestMinorUnitsAreExact(t *testing.T) {
	for _, c := range []struct {
		in    string
		scale int
		out   int64
		ok    bool
	}{{"45.5", 2, 4550, true}, {"-1200", 2, -120000, true}, {"0.01", 2, 1, true}, {"12.340", 2, 1234, true},
		{"1500", 0, 1500, true}, {"12.345", 2, 0, false}, {"1e3", 2, 0, false}, {"", 2, 0, false}, {"1.2.3", 2, 0, false}} {
		got, err := minorUnits(c.in, c.scale)
		if (err == nil) != c.ok || c.ok && got != c.out {
			t.Fatalf("minorUnits(%q, %d) = %d, %v", c.in, c.scale, got, err)
		}
	}
}

func TestRoutingNumbersAreChecked(t *testing.T) {
	for n, ok := range map[string]bool{"011000015": true, "021000021": true, "011000016": false, "01100001": false, "01100001a": false, "": false} {
		if ValidRoutingNumber(n) != ok {
			t.Errorf("%q: %v", n, !ok)
		}
	}
}

// Answers are kept for a day per provider environment, so the picker's
// grid and repeated searches don't call the provider again.
func TestInstitutionCacheKeepsADay(t *testing.T) {
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	c := institutionCache{now: func() time.Time { return now }}
	c.put("example/sandbox/search/chase", []Institution{{ID: "ins_chase"}})
	if list, ok := c.get("example/sandbox/search/chase"); !ok || list[0].ID != "ins_chase" {
		t.Fatal("not kept")
	}
	if _, ok := c.get("example/production/search/chase"); ok {
		t.Fatal("shared across environments")
	}
	now = now.Add(25 * time.Hour)
	if _, ok := c.get("example/sandbox/search/chase"); ok {
		t.Fatal("kept past a day")
	}
	if q, ok := normalQuery("  Bank   of AMERICA "); !ok || q != "bank of america" {
		t.Fatal(q)
	}
	if _, ok := normalQuery(strings.Repeat("a", 81)); ok {
		t.Fatal("a long query was accepted")
	}
}

func TestCountriesComeFromCurrencies(t *testing.T) {
	if got := strings.Join(CountriesFor([]string{"USD", "GBP", "USD", "JPY"}), ","); got != "GB,US" {
		t.Fatal(got)
	}
	if got := CountriesFor([]string{"JPY"}); got != nil {
		t.Fatal(got)
	}
}
