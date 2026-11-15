package docread

import (
	"math/big"
	"strings"
)

// Rejection is why a proposed reading wasn't accepted, in plain words.
type Rejection struct{ Reason string }

func (r *Rejection) Error() string { return r.Reason }

// Verify checks a reading proposed by an AI or an agent, already filled
// into what Vult read itself, against the document's text: the account
// digits and the period's dates must appear in it, and the lines must add
// up from the opening balance to the closing one. With no text (a scan
// nothing could read), only the sum is checked.
func Verify(r Reading, pages []string) error {
	text := strings.Join(pages, "\n")
	if strings.TrimSpace(text) != "" {
		if last4 := r.Last4(); last4 != "" {
			found := false
			for _, line := range strings.Split(text, "\n") {
				if strings.Contains(digits(line), last4) {
					found = true
					break
				}
			}
			if !found {
				return &Rejection{"The account number ending " + last4 + " doesn't appear in the document."}
			}
		}
		seen := map[string]bool{}
		for _, order := range []bool{false, true} {
			for _, d := range Dates(text, order) {
				seen[d.Date] = true
			}
		}
		for _, f := range []Field{r.PeriodStart, r.PeriodEnd} {
			if f.Value != "" && !seen[f.Value] {
				return &Rejection{"The date " + f.Value + " doesn't appear in the document."}
			}
		}
	}
	if r.Opening.Value != "" && r.Closing.Value != "" {
		total, ok1 := new(big.Rat).SetString(r.Opening.Value)
		closing, ok2 := new(big.Rat).SetString(r.Closing.Value)
		if !ok1 || !ok2 {
			return &Rejection{"The balances aren't amounts."}
		}
		for _, l := range r.Lines {
			n, ok := new(big.Rat).SetString(l.Amount)
			if !ok || !validDate(l.Date) {
				return &Rejection{"A line has no valid date or amount."}
			}
			total.Add(total, n)
		}
		if total.Cmp(closing) != 0 {
			return &Rejection{"The lines add up to " + total.FloatString(2) + " from the opening balance, not the closing " + closing.FloatString(2) + "."}
		}
	}
	return nil
}
