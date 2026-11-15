// Package docread reads bank and card statements without AI: structured
// exports (CSV, OFX/QFX, CAMT.053, MT940) field by field, and a statement's
// text, from a PDF's text layer or local OCR, through a multilingual label
// table and transaction-table heuristics. It also checks an AI's or agent's
// proposed reading against the document before Vult uses it.
package docread

import (
	"strings"
)

// Field is one value read from a document and the page it's on; page 0
// when the document has no pages, such as an export.
type Field struct {
	Value string `json:"value"`
	Page  int    `json:"page"`
}

// Line is one transaction line. Amount is a plain decimal with a dot.
type Line struct {
	Date        string `json:"date"`
	Description string `json:"description"`
	Amount      string `json:"amount"`
	Page        int    `json:"page"`
}

// Sign conventions a reading's amounts follow.
const (
	// StatementSigns: signed the way the statement's balance moves, as a
	// statement prints it: on a card, charges and the balance owed are
	// positive.
	StatementSigns = "statement"
	// BankSigns: as a bank's export writes them, money out negative, and a
	// card's balance owed negative.
	BankSigns = "bank"
)

// Reading is what was read from a statement or receipt.
type Reading struct {
	Kind        string `json:"kind"` // statement, receipt or other
	Holder      Field  `json:"holder"`
	Address     Field  `json:"address"`
	Institution Field  `json:"institution"`
	// Account is the account or card number as printed, whole or only its
	// last digits.
	Account     Field  `json:"last4"`
	PeriodStart Field  `json:"period_start"`
	PeriodEnd   Field  `json:"period_end"`
	Opening     Field  `json:"opening_balance"`
	Closing     Field  `json:"closing_balance"`
	Lines       []Line `json:"lines"`
	Total       Field  `json:"total"`
	Date        Field  `json:"date"`
	Vendor      Field  `json:"vendor"`
	// Signs says how amounts are signed; empty means StatementSigns.
	Signs string `json:"signs,omitempty"`
	// OpeningDerived is set when the document gave no opening balance and
	// it was worked out from the closing balance and the lines.
	OpeningDerived bool `json:"opening_derived,omitempty"`
}

// Last4 is the last four digits of the account number read.
func (r Reading) Last4() string {
	d := digits(r.Account.Value)
	if len(d) > 4 {
		d = d[len(d)-4:]
	}
	return d
}

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// Needed are the fields a statement's checks need, by name.
var Needed = []string{"last4", "period_start", "period_end", "opening_balance", "closing_balance"}

func (r Reading) field(name string) *Field {
	switch name {
	case "holder":
		return &r.Holder
	case "address":
		return &r.Address
	case "institution":
		return &r.Institution
	case "last4":
		return &r.Account
	case "period_start":
		return &r.PeriodStart
	case "period_end":
		return &r.PeriodEnd
	case "opening_balance":
		return &r.Opening
	case "closing_balance":
		return &r.Closing
	}
	return nil
}

// Missing names the needed fields this reading doesn't have.
func (r Reading) Missing() []string {
	var out []string
	if r.Kind != "" && r.Kind != "statement" {
		return nil
	}
	for _, name := range Needed {
		f := r.field(name)
		if strings.TrimSpace(f.Value) == "" {
			out = append(out, name)
			continue
		}
		switch name {
		case "last4":
			if len(r.Last4()) < 4 {
				out = append(out, name)
			}
		case "period_start", "period_end":
			if !validDate(f.Value) {
				out = append(out, name)
			}
		default:
			if _, ok := Decimal(f.Value); !ok {
				out = append(out, name)
			}
		}
	}
	return out
}

// Fill takes from more only what base is missing, and the lines when base
// has none. Holder, address and institution fill when empty too.
func Fill(base, more Reading) Reading {
	more = Tidy(more)
	if base.Kind == "" {
		base.Kind = more.Kind
	}
	take := func(dst *Field, src Field) {
		if strings.TrimSpace(dst.Value) == "" && strings.TrimSpace(src.Value) != "" {
			*dst = src
		}
	}
	missing := map[string]bool{}
	for _, m := range base.Missing() {
		missing[m] = true
	}
	for _, name := range Needed {
		if missing[name] {
			*base.fieldPtr(name) = *more.field(name)
		}
	}
	take(&base.Holder, more.Holder)
	take(&base.Address, more.Address)
	take(&base.Institution, more.Institution)
	take(&base.Total, more.Total)
	take(&base.Date, more.Date)
	take(&base.Vendor, more.Vendor)
	if len(base.Lines) == 0 && len(more.Lines) > 0 {
		base.Lines = more.Lines
		if more.Signs != "" {
			base.Signs = more.Signs
		}
	}
	return base
}

func (r *Reading) fieldPtr(name string) *Field {
	switch name {
	case "last4":
		return &r.Account
	case "period_start":
		return &r.PeriodStart
	case "period_end":
		return &r.PeriodEnd
	case "opening_balance":
		return &r.Opening
	case "closing_balance":
		return &r.Closing
	}
	return &Field{}
}

// Tidy writes a proposed reading's amounts as plain decimals and its dates
// as YYYY-MM-DD, when the proposal wrote them the way the document prints
// them: "1.000,00", "$410.00", "30.09.2026". A value that can't be read
// one way only is left as it was, for verification to refuse.
func Tidy(r Reading) Reading {
	amount := func(v string) string {
		if plainDecimal(v) {
			return v
		}
		if d, ok := Decimal(v); ok {
			return d
		}
		return v
	}
	date := func(v string) string {
		v = strings.TrimSpace(v)
		if v == "" || validDate(v) {
			return v
		}
		dayFirst, monthFirst := Dates(v, false), Dates(v, true)
		if len(dayFirst) == 1 && len(monthFirst) == 1 && dayFirst[0].Date == monthFirst[0].Date {
			return dayFirst[0].Date
		}
		return v
	}
	r.Opening.Value = amount(r.Opening.Value)
	r.Closing.Value = amount(r.Closing.Value)
	r.Total.Value = amount(r.Total.Value)
	r.PeriodStart.Value = date(r.PeriodStart.Value)
	r.PeriodEnd.Value = date(r.PeriodEnd.Value)
	r.Date.Value = date(r.Date.Value)
	if len(r.Lines) > 0 {
		lines := make([]Line, len(r.Lines))
		for i, l := range r.Lines {
			l.Amount, l.Date = amount(l.Amount), date(l.Date)
			lines[i] = l
		}
		r.Lines = lines
	}
	return r
}

// plainDecimal is an amount already written as -1234.56.
func plainDecimal(v string) bool {
	if v == "" {
		return true
	}
	v = strings.TrimPrefix(v, "-")
	dot := false
	for i, c := range v {
		switch {
		case c >= '0' && c <= '9':
		case c == '.' && !dot && i > 0 && i < len(v)-1:
			dot = true
		default:
			return false
		}
	}
	return v != ""
}
