package ledger

import (
	"reflect"
	"testing"
)

func hypotheticalRate(rate string) AppliedRate {
	return AppliedRate{Base: "JPY", Quote: "USD", Rate: rate, RequestedDate: "2026-01-02", ObservationDate: "2026-01-02", Provenance: "Hypothetical conversion input"}
}

func TestConversionKeepsZeroAndExactHalfEvenBoundaries(t *testing.T) {
	book := Book{Currency: "USD", Scale: 2}
	for _, example := range []struct{ native, rate, want string }{
		{"1", "0.001", "0"}, {"1", "0.005", "0"}, {"3", "0.005", "2"}, {"5", "0.005", "2"},
		{"1", "0.015", "2"}, {"1", "0.00500000000000000000000000000001", "1"}, {"0", "0.001", "0"},
	} {
		t.Run(example.native+"-"+example.rate, func(t *testing.T) {
			got, err := convertedAmount(money("JPY", 0, example.native), hypotheticalRate(example.rate), book)
			if err != nil || got != example.want {
				t.Fatal("conversion differs from exact expected cents", got, err)
			}
		})
	}
	input := money("KWD", 3, "1000")
	rate := hypotheticalRate("3.25123456789")
	rate.Base = "KWD"
	if got, err := convertedAmount(input, rate, book); err != nil || got != "325" {
		t.Fatal("source precision was replaced by Book precision", got, err)
	}
	for _, example := range []CurrencyAmount{
		{Currency: "JPY", AmountMinor: "1"}, money("JPY", 2, "1"), money("ABC", 2, "1"),
		money("JPY", 0, "01"), money("JPY", 0, "1e3"), money("JPY", 0, "-1"), money("JPY", 0, "9223372036854775808"),
	} {
		if _, err := convertedAmount(example, hypotheticalRate("0.001"), book); err == nil {
			t.Fatal("invalid measurement converted", example)
		}
	}
	for _, value := range []string{"0", "-1", "NaN", "1e-3", "1/3", "0.005x"} {
		if _, err := convertedAmount(money("JPY", 0, "1"), hypotheticalRate(value), book); err == nil {
			t.Fatal("invalid exact rate converted", value)
		}
	}
	if _, err := convertedAmount(money("JPY", 0, "9223372036854775807"), hypotheticalRate("100"), book); err == nil {
		t.Fatal("converted value overflowed")
	}
}

func TestContextAndQuoteUnitsNeverInheritBookPrecision(t *testing.T) {
	legacyBook := Book{Currency: "JPY", Scale: 2}
	context := money("JPY", 2, "1")
	if normalizeDenomination(&context) {
		t.Fatal("legacy Book precision became a source denomination")
	}
	rate := hypotheticalRate("1")
	rate.Quote = "JPY"
	if _, err := convertedAmount(money("JPY", 2, "1"), rate, legacyBook); err == nil {
		t.Fatal("quote base inherited legacy Book precision")
	}
	if got, err := convertedAmount(money("JPY", 0, "1"), rate, legacyBook); err != nil || got != "100" {
		t.Fatal("native unit was not converted into the distinct Book unit", got, err)
	}
}

func TestAllocationPreservesOneAuthorityAndStableResiduals(t *testing.T) {
	book := Book{Currency: "USD", Scale: 2}
	for _, example := range []struct {
		total string
		want  map[int]string
	}{
		{"0", map[int]string{1: "0", 2: "0", 3: "0"}},
		{"1", map[int]string{1: "1", 2: "0", 3: "0"}},
		{"2", map[int]string{1: "1", 2: "1", 3: "0"}},
		{"3", map[int]string{1: "1", 2: "1", 3: "1"}},
		{"9223372036854775807", map[int]string{1: "3074457345618258603", 2: "3074457345618258602", 3: "3074457345618258602"}},
	} {
		group := AmountAllocation{Total: money("USD", 2, example.total), Method: "largest-remainder-v1", Parts: []AllocationPart{{Line: 3, Weight: "1"}, {Line: 1, Weight: "1"}, {Line: 2, Weight: "1"}}}
		got, err := allocationShares(group, book)
		if err != nil || !reflect.DeepEqual(got, example.want) {
			t.Fatal("allocation lost or invented a minor unit", got, err)
		}
	}
	large := AmountAllocation{Total: money("USD", 2, "1"), Method: "largest-remainder-v1", Parts: []AllocationPart{{Line: 1, Weight: "9223372036854775807"}, {Line: 2, Weight: "9223372036854775807"}, {Line: 3, Weight: "1"}}}
	if got, err := allocationShares(large, book); err != nil || !reflect.DeepEqual(got, map[int]string{1: "1", 2: "0", 3: "0"}) {
		t.Fatal("weight sum overflowed", got, err)
	}
	large.Parts[1].Line = 1
	if _, err := allocationShares(large, book); err == nil {
		t.Fatal("duplicate allocation line accepted")
	}
}

func TestCarryingAllocationKeepsLastRemainderAndSignedLimits(t *testing.T) {
	for _, example := range []struct{ native, book, consumed, want string }{
		{"30000", "36000", "10000", "12000"}, {"3", "1", "1", "0"}, {"2", "1", "1", "0"}, {"1", "1", "1", "1"},
		{"3", "2", "1", "1"}, {"-3", "-2", "1", "1"}, {"100", "0", "100", "0"},
		{"-9223372036854775808", "-9223372036854775808", "9223372036854775807", "9223372036854775807"},
	} {
		if got, err := carryingShare(example.native, example.book, example.consumed); err != nil || got != example.want {
			t.Fatal("carrying amount differs", got, err)
		}
	}
	for _, example := range [][3]string{{"0", "0", "1"}, {"3", "2", "4"}, {"3", "2", "0"}, {"3", "-2", "1"}, {"-3", "2", "1"}} {
		if _, err := carryingShare(example[0], example[1], example[2]); err == nil {
			t.Fatal("unsupported carrying position accepted", example)
		}
	}
}

func TestIndependentEventsDoNotBecomeOneRoundedTotal(t *testing.T) {
	book := Book{Currency: "USD", Scale: 2}
	for _, batch := range []int{1, 3, 20} {
		actual := []string{}
		for start := 0; start < 20; start += batch {
			for i := start; i < min(20, start+batch); i++ {
				value, err := convertedAmount(money("JPY", 0, "1"), hypotheticalRate("0.004"), book)
				if err != nil {
					t.Fatal(err)
				}
				actual = append(actual, value)
			}
		}
		if len(actual) != 20 {
			t.Fatal("independent event missing")
		}
		for _, value := range actual {
			if value != "0" {
				t.Fatal("transport batch changed an accepted cent")
			}
		}
	}
	if value, err := convertedAmount(money("JPY", 0, "20"), hypotheticalRate("0.004"), book); err != nil || value != "8" {
		t.Fatal("separate position valuation is not eight cents", value, err)
	}
}
