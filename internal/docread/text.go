package docread

import (
	"math/big"
	"regexp"
	"strings"

	"github.com/vult-ai/vult/internal/likeness"
)

// Labels a statement prints before each field, in English, Polish, German,
// French and Spanish, folded (lowercase, no accents). Longer labels are
// tried first, so "closing balance" wins over "balance".
var labels = map[string][]string{
	"opening_balance": {"previous statement balance", "last statement balance", "balance from last statement", "opening balance", "previous balance", "beginning balance", "starting balance", "balance brought forward",
		"balance forward", "saldo poczatkowe", "saldo otwarcia", "saldo poprzednie", "anfangssaldo", "alter saldo", "saldo alt",
		"solde initial", "solde precedent", "ancien solde", "solde de depart", "saldo inicial", "saldo anterior"},
	"closing_balance": {"closing balance", "ending balance", "new balance", "balance carried forward", "statement balance",
		"saldo koncowe", "saldo zamkniecia", "saldo na koniec", "endsaldo", "neuer saldo", "saldo neu", "solde final",
		"nouveau solde", "solde de cloture", "saldo final", "saldo actual"},
	"holder": {"account holder", "account name", "customer name", "wlasciciel rachunku", "posiadacz rachunku", "wlasciciel",
		"kontoinhaber", "inhaber", "titulaire du compte", "titulaire", "titular de la cuenta", "titular"},
	"last4": {"account number", "account no", "card number", "card ending in", "account ending in", "ending in", "iban",
		"numer rachunku", "nr rachunku", "numer karty", "kontonummer", "kartennummer", "numero de compte", "n de compte",
		"numero de carte", "numero de cuenta", "numero de tarjeta"},
	"period": {"statement period", "for the period", "period", "okres rozliczeniowy", "za okres", "okres", "abrechnungszeitraum",
		"zeitraum", "periode du", "periode", "periodo"},
	"address": {"address", "adres", "anschrift", "adresse", "direccion"},
}

// languages that write numeric dates month first.
var monthFirstLabels = []string{"opening balance", "closing balance", "beginning balance", "ending balance", "new balance", "statement period"}

var amountToken = regexp.MustCompile(`[-−(]?\s?[\p{Sc}]?\s?(?:\d{1,3}(?:[ \x{00a0}\x{202f}.,']\d{3})+|\d+)[.,]\d{2}\)?(?:\s?(?:DR|CR|Dr|Cr|Wn|Ma)\b|-)?`)

type textLine struct {
	text, folded string
	page         int
}

// Text reads a statement from its text, page by page: the text layer of a
// PDF, or what local OCR read from a scan.
func Text(pages []string) Reading {
	r := Reading{Kind: "statement", Signs: StatementSigns}
	var lines []textLine
	for i, p := range pages {
		for _, l := range strings.Split(p, "\n") {
			if t := strings.TrimSpace(l); t != "" {
				lines = append(lines, textLine{t, likeness.Fold(t), i + 1})
			}
		}
	}
	monthFirst := englishUS(lines)
	used := map[int]bool{}
	shared := map[int]bool{}
	// from says where on a line a later field may still be found: the
	// rest of a line that holds two balances, "Opening balance: $0.00 ·
	// Closing balance: $7,825.01".
	from := map[int]int{}

	// value finds the first line with one of a field's labels and returns
	// what follows the label, or the next line when nothing does.
	value := func(field string) (string, int, int) {
		for i, l := range lines {
			// A line another field's label already took isn't read again:
			// "previous statement balance" is the opening balance, not a
			// "statement balance".
			if used[i] {
				continue
			}
			for _, label := range labels[field] {
				at := strings.Index(l.folded[from[i]:], label)
				if at >= 0 {
					at += from[i]
				}
				if at < 0 || at > 0 && isLetter(l.folded[at-1]) || at+len(label) < len(l.folded) && isLetter(l.folded[at+len(label)]) {
					continue
				}
				rest := strings.TrimLeft(cutFolded(l.text, l.folded, at+len(label)), " :.-–\t")
				// An opening balance stops where a closing one starts on
				// the same line, which stays open for it.
				if field == "opening_balance" {
					if next := closingAfter(l.folded, at+len(label)); next > 0 {
						rest = strings.TrimSpace(strings.TrimSuffix(rest, cutFolded(l.text, l.folded, next)))
						from[i] = next
						shared[i] = true
					}
				}
				if strings.TrimSpace(rest) == "" && i+1 < len(lines) {
					return lines[i+1].text, lines[i+1].page, i
				}
				return rest, l.page, i
			}
		}
		return "", 0, -1
	}

	for _, field := range []string{"opening_balance", "closing_balance"} {
		v, page, at := value(field)
		if at < 0 {
			continue
		}
		if !shared[at] || field == "closing_balance" {
			used[at] = true
		}
		tokens := amountToken.FindAllString(v, -1)
		if len(tokens) == 0 {
			continue
		}
		if d, ok := Decimal(tokens[len(tokens)-1]); ok {
			f := Field{Value: d, Page: page}
			if field == "opening_balance" {
				r.Opening = f
			} else {
				r.Closing = f
			}
		}
	}
	if v, page, at := value("holder"); at >= 0 {
		r.Holder = Field{Value: strings.TrimSpace(v), Page: page}
		used[at] = true
		// The lines under a holder's name with a number in them are the
		// address.
		var parts []string
		for k := at + 1; k < len(lines) && k <= at+3 && len(parts) < 2; k++ {
			if lines[k].text == r.Holder.Value || !hasDigit(lines[k].text) || amountToken.MatchString(lines[k].text) || len(Dates(lines[k].text, monthFirst)) > 0 {
				continue
			}
			if labelled(lines[k].folded) {
				break
			}
			parts = append(parts, lines[k].text)
		}
		if len(parts) > 0 {
			r.Address = Field{Value: strings.Join(parts, ", "), Page: page}
		}
	}
	if v, page, at := value("address"); at >= 0 {
		r.Address = Field{Value: strings.TrimSpace(v), Page: page}
	}
	if v, page, at := value("last4"); at >= 0 {
		for _, token := range strings.FieldsFunc(v, func(c rune) bool { return c == ' ' || c == ',' || c == ';' }) {
			if len(digits(token)) >= 4 {
				r.Account = Field{Value: token, Page: page}
			}
		}
		// An IBAN or account number split into groups of digits.
		if r.Account.Value == "" || len(digits(r.Account.Value)) < 4 {
			if d := digits(v); len(d) >= 4 {
				r.Account = Field{Value: strings.TrimSpace(v), Page: page}
			}
		}
	}
	if v, page, at := value("period"); at >= 0 {
		ds := Dates(v, monthFirst)
		if len(ds) < 2 && at+1 < len(lines) {
			ds = append(ds, Dates(lines[at+1].text, monthFirst)...)
		}
		if len(ds) >= 2 {
			r.PeriodStart, r.PeriodEnd = Field{Value: ds[0].Date, Page: page}, Field{Value: ds[1].Date, Page: page}
		}
	}

	// The transaction table: lines that start with a date and end with
	// amounts. With a running balance beside each amount, the balance's
	// change gives the sign.
	previous, havePrevious := new(big.Rat), false
	if o, ok := new(big.Rat).SetString(r.Opening.Value); ok && r.Opening.Value != "" {
		previous, havePrevious = o, true
	}
	for i, l := range lines {
		if used[i] || labelled(l.folded) {
			continue
		}
		ds := Dates(l.text, monthFirst)
		if len(ds) == 0 || ds[0].Start > 2 {
			continue
		}
		tokens := amountToken.FindAllStringIndex(l.text, -1)
		if len(tokens) == 0 {
			continue
		}
		var amounts []string
		for _, t := range tokens {
			if t[0] < ds[0].End {
				continue
			}
			if d, ok := Decimal(l.text[t[0]:t[1]]); ok {
				amounts = append(amounts, d)
			}
		}
		if len(amounts) == 0 {
			continue
		}
		start := tokens[0][0]
		for _, t := range tokens {
			if t[0] >= ds[0].End {
				start = t[0]
				break
			}
		}
		description := strings.TrimSpace(l.text[ds[0].End:start])
		// A second date (the value date) is part of the description.
		for _, d := range Dates(description, monthFirst) {
			if d.Start == 0 {
				description = strings.TrimSpace(description[d.End:])
			}
		}
		amount := amounts[0]
		if len(amounts) >= 2 {
			balance, _ := new(big.Rat).SetString(amounts[len(amounts)-1])
			amount = amounts[len(amounts)-2]
			if havePrevious && balance != nil {
				change := new(big.Rat).Sub(balance, previous)
				a, _ := new(big.Rat).SetString(strings.TrimPrefix(amount, "-"))
				if a != nil && new(big.Rat).Abs(change).Cmp(a) == 0 {
					d, _ := Decimal(change.FloatString(6))
					amount = d
				}
			}
			if balance != nil {
				previous, havePrevious = balance, true
			}
		}
		r.Lines = append(r.Lines, Line{Date: ds[0].Date, Description: description, Amount: amount, Page: l.page})
	}
	return r
}

func cutFolded(text, folded string, at int) string {
	// Folding can change byte lengths; map by runes counted in the folded
	// prefix where lengths agree, else fall back to the folded text.
	if len(text) == len(folded) {
		return text[at:]
	}
	prefix := []rune(folded[:at])
	runes := []rune(text)
	if len(runes) >= len(prefix) && likeness.Fold(string(runes[:len(prefix)])) == string(prefix) {
		return string(runes[len(prefix):])
	}
	return folded[at:]
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' }

func hasDigit(s string) bool { return strings.ContainsAny(s, "0123456789") }

func labelled(folded string) bool {
	for field, list := range labels {
		if field == "period" || field == "address" {
			continue
		}
		for _, l := range list {
			if strings.HasPrefix(folded, l) {
				return true
			}
		}
	}
	return false
}

// englishUS says whether numeric dates are month first: English labels,
// and no date whose first number is above 12.
func englishUS(lines []textLine) bool {
	english := false
	for _, l := range lines {
		for _, label := range monthFirstLabels {
			if strings.Contains(l.folded, label) {
				english = true
			}
		}
	}
	if !english {
		return false
	}
	for _, l := range lines {
		for _, m := range numericDate.FindAllStringSubmatch(l.text, -1) {
			if len(m[1]) == 2 && m[1] > "12" {
				return false
			}
		}
	}
	return true
}

// closingAfter is where a closing balance's label starts on a folded line
// after position start, or 0.
func closingAfter(folded string, start int) int {
	best := 0
	for _, label := range labels["closing_balance"] {
		if at := strings.Index(folded[start:], label); at >= 0 {
			at += start
			if (at == 0 || !isLetter(folded[at-1])) && (best == 0 || at < best) {
				best = at
			}
		}
	}
	return best
}
