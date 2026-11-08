package ledger

import (
	"math/big"
	"regexp"
	"sort"
	"strconv"
)

type CurrencyAmount struct {
	Currency       string `json:"currency"`
	Scale          *int   `json:"scale"`
	AmountMinor    string `json:"amount_minor"`
	UnitDefinition string `json:"unit_definition,omitempty"`
}

type NativeMovement struct {
	Currency        string `json:"currency"`
	Scale           *int   `json:"scale"`
	AmountMinor     string `json:"amount_minor"`
	AccountRevision int64  `json:"account_revision,omitempty"`
	UnitDefinition  string `json:"unit_definition,omitempty"`
}

type ContextMeasurement struct {
	Role   string         `json:"role"`
	Amount CurrencyAmount `json:"amount"`
	Source string         `json:"source,omitempty"`
}

type AppliedRate struct {
	Base            string `json:"base"`
	Quote           string `json:"quote"`
	Rate            string `json:"rate"`
	RequestedDate   string `json:"requested_date"`
	ObservationDate string `json:"observation_date"`
	Provenance      string `json:"provenance"`
}

type CarryingAllocation struct {
	Policy          string         `json:"policy"`
	AccountID       string         `json:"account_id"`
	AccountRevision int64          `json:"account_revision"`
	RoleRevision    int64          `json:"role_revision"`
	BeforeSequence  int64          `json:"before_sequence"`
	BeforeNative    CurrencyAmount `json:"before_native"`
	BeforeBook      CurrencyAmount `json:"before_book"`
	ConsumedNative  CurrencyAmount `json:"consumed_native"`
	ConsumedBook    CurrencyAmount `json:"consumed_book"`
}

type Valuation struct {
	Basis           string              `json:"basis"`
	Policy          string              `json:"policy"`
	Input           *CurrencyAmount     `json:"input,omitempty"`
	Rate            *AppliedRate        `json:"rate,omitempty"`
	Source          string              `json:"source,omitempty"`
	AllocationGroup *int                `json:"allocation_group,omitempty"`
	Carrying        *CarryingAllocation `json:"carrying,omitempty"`
}

type NativeTransfer struct {
	Lines []int `json:"lines"`
}

type AllocationPart struct {
	Line   int    `json:"line"`
	Weight string `json:"weight"`
}

type AmountAllocation struct {
	Total  CurrencyAmount   `json:"total"`
	Method string           `json:"method"`
	Parts  []AllocationPart `json:"parts"`
}

type MeasurementPolicy struct {
	Version         int                `json:"version"`
	NativeTransfers []NativeTransfer   `json:"native_transfers,omitempty"`
	Allocations     []AmountAllocation `json:"allocations,omitempty"`
}

type NativePosition struct {
	Status             string `json:"status"`
	Currency           string `json:"currency,omitempty"`
	Scale              *int   `json:"scale,omitempty"`
	DebitPositiveMinor string `json:"debit_positive_minor,omitempty"`
	ThroughSequence    int64  `json:"through_sequence,omitempty"`
}

var (
	minorPattern       = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)
	signedMinorPattern = regexp.MustCompile(`^(0|-?[1-9][0-9]{0,18})$`)
	ratePattern        = regexp.MustCompile(`^(0|[1-9][0-9]{0,63})(\.[0-9]{1,64})?$`)
	rolePattern        = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

func nonnegativeAmount(s string) (*big.Int, error) {
	if !minorPattern.MatchString(s) {
		return nil, ErrInvalid
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, ErrInvalid
	}
	return big.NewInt(n), nil
}

func signedAmount(s string) (*big.Int, error) {
	if !signedMinorPattern.MatchString(s) {
		return nil, ErrInvalid
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, ErrInvalid
	}
	return big.NewInt(n), nil
}

func money(currency string, scale int, minor string) CurrencyAmount {
	return CurrencyAmount{Currency: currency, Scale: &scale, AmountMinor: minor}
}

func validCurrencyAmount(value CurrencyAmount, signed bool) bool {
	if !currencyPattern.MatchString(value.Currency) || value.Scale == nil || *value.Scale < 0 || *value.Scale > 4 {
		return false
	}
	if signed {
		_, err := signedAmount(value.AmountMinor)
		return err == nil
	}
	_, err := nonnegativeAmount(value.AmountMinor)
	return err == nil
}

func validNativeUnit(currency string, scale *int) bool {
	if scale == nil {
		return false
	}
	expected, ok := CurrencyScale(currency)
	return ok && *scale == expected
}

func normalizeUnit(value *CurrencyAmount, book Book) bool {
	if value.Scale == nil || value.Currency != book.Currency || *value.Scale != book.Scale {
		return false
	}
	if value.UnitDefinition == "" {
		value.UnitDefinition = "book"
	}
	return value.UnitDefinition == "book"
}

func normalizeDenomination(value *CurrencyAmount) bool {
	if !validNativeUnit(value.Currency, value.Scale) {
		return false
	}
	if value.UnitDefinition == "" {
		value.UnitDefinition = currencyUnitVersion
	}
	return value.UnitDefinition == currencyUnitVersion
}

func sameUnit(a, b CurrencyAmount) bool {
	return a.Currency == b.Currency && a.Scale != nil && b.Scale != nil && *a.Scale == *b.Scale
}

func equalScale(a, b *int) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// roundHalfEven rounds a nonnegative exact fraction once to an integer.
func roundHalfEven(numerator, denominator *big.Int) *big.Int {
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(numerator, denominator, remainder)
	twice := new(big.Int).Lsh(remainder, 1)
	if c := twice.Cmp(denominator); c > 0 || c == 0 && whole.Bit(0) != 0 {
		whole.Add(whole, big.NewInt(1))
	}
	return whole
}

func convertedAmount(input CurrencyAmount, rate AppliedRate, book Book) (string, error) {
	if !currencyPattern.MatchString(book.Currency) || book.Scale < 0 || book.Scale > 4 ||
		!validCurrencyAmount(input, false) || !normalizeDenomination(&input) || rate.Base != input.Currency || rate.Quote != book.Currency ||
		!ratePattern.MatchString(rate.Rate) || !ValidDate(rate.RequestedDate) || !ValidDate(rate.ObservationDate) ||
		rate.ObservationDate > rate.RequestedDate || len(rate.Provenance) == 0 || len(rate.Provenance) > 512 {
		return "", ErrInvalid
	}
	ratio, ok := new(big.Rat).SetString(rate.Rate)
	if !ok || ratio.Sign() <= 0 {
		return "", ErrInvalid
	}
	amount, _ := nonnegativeAmount(input.AmountMinor)
	numerator := new(big.Int).Mul(amount, ratio.Num())
	numerator.Mul(numerator, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(book.Scale)), nil))
	denominator := new(big.Int).Mul(ratio.Denom(), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(*input.Scale)), nil))
	result := roundHalfEven(numerator, denominator)
	if !result.IsInt64() {
		return "", ErrInvalid
	}
	return result.String(), nil
}

func allocationShares(group AmountAllocation, book Book) (map[int]string, error) {
	if group.Method != "largest-remainder-v1" || !validCurrencyAmount(group.Total, false) || !normalizeUnit(&group.Total, book) ||
		group.Total.Currency != book.Currency || *group.Total.Scale != book.Scale || len(group.Parts) == 0 || len(group.Parts) > 256 {
		return nil, ErrInvalid
	}
	total, _ := nonnegativeAmount(group.Total.AmountMinor)
	weights := new(big.Int)
	seen := map[int]bool{}
	for _, part := range group.Parts {
		weight, err := positiveAmount(part.Weight)
		if err != nil || part.Line < 1 || part.Line > 256 || seen[part.Line] {
			return nil, ErrInvalid
		}
		seen[part.Line] = true
		weights.Add(weights, big.NewInt(weight))
	}
	type share struct {
		line              int
		amount, remainder *big.Int
	}
	shares := make([]share, 0, len(group.Parts))
	allocated := new(big.Int)
	for _, part := range group.Parts {
		weight, _ := positiveAmount(part.Weight)
		numerator := new(big.Int).Mul(total, big.NewInt(weight))
		whole, remainder := new(big.Int), new(big.Int)
		whole.QuoRem(numerator, weights, remainder)
		shares = append(shares, share{part.Line, whole, remainder})
		allocated.Add(allocated, whole)
	}
	sort.Slice(shares, func(i, j int) bool {
		if c := shares[i].remainder.Cmp(shares[j].remainder); c != 0 {
			return c > 0
		}
		return shares[i].line < shares[j].line
	})
	residual := new(big.Int).Sub(total, allocated).Int64()
	for i := int64(0); i < residual; i++ {
		shares[i].amount.Add(shares[i].amount, big.NewInt(1))
	}
	out := make(map[int]string, len(shares))
	for _, value := range shares {
		out[value.line] = value.amount.String()
	}
	return out, nil
}

func carryingShare(beforeNative, beforeBook, consumed string) (string, error) {
	native, err := signedAmount(beforeNative)
	if err != nil || native.Sign() == 0 {
		return "", ErrInvalid
	}
	book, err := signedAmount(beforeBook)
	if err != nil || book.Sign() != 0 && book.Sign() != native.Sign() {
		return "", ErrInvalid
	}
	quantity, err := nonnegativeAmount(consumed)
	if err != nil || quantity.Sign() == 0 {
		return "", ErrInvalid
	}
	native.Abs(native)
	book.Abs(book)
	if quantity.Cmp(native) > 0 {
		return "", ErrInvalid
	}
	if quantity.Cmp(native) == 0 {
		if !book.IsInt64() {
			return "", ErrInvalid
		}
		return book.String(), nil
	}
	result := roundHalfEven(new(big.Int).Mul(book, quantity), native)
	if !result.IsInt64() {
		return "", ErrInvalid
	}
	return result.String(), nil
}
