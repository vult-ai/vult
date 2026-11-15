package docread

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Decimal normalises an amount as printed into a plain decimal with a dot
// and no trailing zeros: "1 234,56", "1.234,56", "1,234.56", "1'234.56",
// "-50.00", "50,00-", "(50.00)", "50.00 DR" and "€ 12,50" are all read.
// It refuses text that isn't one amount.
func Decimal(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return "", false
	}
	negative := false
	if m := sideMark.FindStringSubmatch(t); m != nil {
		t = strings.TrimSpace(t[:len(t)-len(m[0])+len(m[1])])
		if side := strings.ToLower(m[2]); side == "dr" || side == "db" || side == "debit" || side == "wn" {
			negative = true
		}
	}
	if strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")") {
		negative, t = !negative, t[1:len(t)-1]
	}
	// Currency signs and codes around the number.
	t = strings.TrimFunc(t, func(r rune) bool {
		return unicode.Is(unicode.Sc, r) || unicode.IsSpace(r) || unicode.IsLetter(r)
	})
	if strings.HasSuffix(t, "-") {
		negative, t = !negative, strings.TrimSpace(strings.TrimSuffix(t, "-"))
	}
	if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "−") {
		negative = !negative
		t = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "-"), "−"))
	}
	t = strings.TrimPrefix(t, "+")
	t = strings.TrimFunc(t, func(r rune) bool { return unicode.Is(unicode.Sc, r) || unicode.IsSpace(r) })
	// Group separators: spaces, thin spaces, apostrophes.
	t = strings.Map(func(r rune) rune {
		if r == ' ' || r == ' ' || r == ' ' || r == '\'' || r == '’' {
			return -1
		}
		return r
	}, t)
	if t == "" || strings.Trim(t, "0123456789.,") != "" {
		return "", false
	}
	dot, comma := strings.LastIndex(t, "."), strings.LastIndex(t, ",")
	var whole, frac string
	switch {
	case dot >= 0 && comma >= 0:
		// The later one is the decimal mark.
		if dot > comma {
			whole, frac = strings.ReplaceAll(t[:dot], ",", ""), t[dot+1:]
		} else {
			whole, frac = strings.ReplaceAll(t[:comma], ".", ""), t[comma+1:]
		}
	case dot >= 0 || comma >= 0:
		mark := "."
		if comma >= 0 {
			mark = ","
		}
		parts := strings.Split(t, mark)
		last := parts[len(parts)-1]
		// One mark with three digits after it, and more than one group or
		// a leading group of 1-3 digits, is a thousands separator: 1,234.
		if len(parts) > 2 || len(last) == 3 && len(parts[0]) >= 1 && len(parts[0]) <= 3 && mark == "," {
			if len(parts) > 2 && len(last) != 3 {
				return "", false
			}
			whole = strings.Join(parts, "")
		} else if len(parts) == 2 {
			whole, frac = parts[0], last
		}
	default:
		whole = t
	}
	if strings.Trim(whole+frac, "0123456789") != "" || whole == "" && frac == "" {
		return "", false
	}
	if whole == "" {
		whole = "0"
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	frac = strings.TrimRight(frac, "0")
	out := whole
	if frac != "" {
		out += "." + frac
	}
	if negative && out != "0" {
		out = "-" + out
	}
	return out, true
}

// Minor turns a plain decimal into minor units at this scale, refusing
// more decimals than the scale rather than rounding.
func Minor(decimal string, scale int) (int64, bool) {
	t := strings.TrimSpace(decimal)
	negative := strings.HasPrefix(t, "-")
	t = strings.TrimPrefix(t, "-")
	whole, frac, _ := strings.Cut(t, ".")
	if whole == "" || len(frac) > scale || strings.Trim(whole+frac, "0123456789") != "" {
		return 0, false
	}
	frac += strings.Repeat("0", scale-len(frac))
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return 0, false
	}
	if negative {
		n = -n
	}
	return n, true
}

func validDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}

// sideMark is a debit or credit mark after an amount: 50.00 DR, 50,00 Wn.
var sideMark = regexp.MustCompile(`(?i)([\d)])\s*(dr|db|debit|cr|credit|wn|ma)\.?$`)

// Month names in the label languages, folded to lowercase without accents
// where the statement may drop them.
var months = map[string]int{}

func init() {
	for i, names := range [][]string{
		{"jan", "january", "januar", "janvier", "enero", "ene", "stycznia", "styczeń", "styczen", "sty", "janv", "jän", "jänner"},
		{"feb", "february", "februar", "février", "fevrier", "févr", "fevr", "febrero", "lutego", "luty", "lut"},
		{"mar", "march", "märz", "marz", "mars", "marzo", "marca", "marzec"},
		{"apr", "april", "avril", "avr", "abril", "abr", "kwietnia", "kwiecień", "kwiecien", "kwi"},
		{"may", "mai", "mayo", "maja", "maj"},
		{"jun", "june", "juni", "juin", "junio", "czerwca", "czerwiec", "cze"},
		{"jul", "july", "juli", "juillet", "juil", "julio", "lipca", "lipiec", "lip"},
		{"aug", "august", "août", "aout", "agosto", "ago", "sierpnia", "sierpień", "sierpien", "sie"},
		{"sep", "sept", "september", "septembre", "septiembre", "set", "września", "wrzesnia", "wrzesień", "wrzesien", "wrz"},
		{"oct", "october", "oktober", "okt", "octobre", "octubre", "paź", "października", "pazdziernika", "październik", "pazdziernik", "paz"},
		{"nov", "november", "novembre", "noviembre", "listopada", "listopad", "lis"},
		{"dec", "december", "dezember", "dez", "décembre", "decembre", "déc", "diciembre", "dic", "grudnia", "grudzień", "grudzien", "gru"},
	} {
		for _, n := range names {
			months[n] = i + 1
		}
	}
}

var (
	isoDate     = regexp.MustCompile(`\b(\d{4})[-./](\d{1,2})[-./](\d{1,2})\b`)
	numericDate = regexp.MustCompile(`\b(\d{1,2})[-./](\d{1,2})[-./](\d{2,4})\b`)
	namedDate   = regexp.MustCompile(`(?i)\b(\d{1,2})\.?\s+([\p{L}]{3,12})\.?,?\s+(\d{4})\b`)
	namedFirst  = regexp.MustCompile(`(?i)\b([\p{L}]{3,12})\.?\s+(\d{1,2}),?\s+(\d{4})\b`)
)

func ymd(y, m, d int) (string, bool) {
	if y < 100 {
		y += 2000
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if m < 1 || m > 12 || d < 1 || t.Day() != d || y < 1900 || y > 2200 {
		return "", false
	}
	return t.Format(time.DateOnly), true
}

// DateAt is a date found in text, with where it was.
type DateAt struct {
	Date       string
	Start, End int
}

// Dates finds the dates in text, normalised to YYYY-MM-DD. Numeric dates
// are read day first unless monthFirst, as English (US) statements write
// them; a date whose day is above 12 settles the order.
func Dates(text string, monthFirst bool) []DateAt {
	var out []DateAt
	taken := make([]bool, len(text)+1)
	add := func(date string, start, end int) {
		for i := start; i < end; i++ {
			if taken[i] {
				return
			}
		}
		for i := start; i < end; i++ {
			taken[i] = true
		}
		out = append(out, DateAt{date, start, end})
	}
	for _, m := range isoDate.FindAllStringSubmatchIndex(text, -1) {
		y, _ := strconv.Atoi(text[m[2]:m[3]])
		mo, _ := strconv.Atoi(text[m[4]:m[5]])
		d, _ := strconv.Atoi(text[m[6]:m[7]])
		if s, ok := ymd(y, mo, d); ok {
			add(s, m[0], m[1])
		}
	}
	for _, m := range namedDate.FindAllStringSubmatchIndex(text, -1) {
		d, _ := strconv.Atoi(text[m[2]:m[3]])
		mo := months[strings.ToLower(text[m[4]:m[5]])]
		y, _ := strconv.Atoi(text[m[6]:m[7]])
		if s, ok := ymd(y, mo, d); ok && mo > 0 {
			add(s, m[0], m[1])
		}
	}
	for _, m := range namedFirst.FindAllStringSubmatchIndex(text, -1) {
		mo := months[strings.ToLower(text[m[2]:m[3]])]
		d, _ := strconv.Atoi(text[m[4]:m[5]])
		y, _ := strconv.Atoi(text[m[6]:m[7]])
		if s, ok := ymd(y, mo, d); ok && mo > 0 {
			add(s, m[0], m[1])
		}
	}
	for _, m := range numericDate.FindAllStringSubmatchIndex(text, -1) {
		a, _ := strconv.Atoi(text[m[2]:m[3]])
		b, _ := strconv.Atoi(text[m[4]:m[5]])
		y, _ := strconv.Atoi(text[m[6]:m[7]])
		if len(text[m[6]:m[7]]) == 3 {
			continue
		}
		d, mo := a, b
		if monthFirst && b <= 31 && a <= 12 || a > 31 {
			d, mo = b, a
		}
		if d <= 12 && mo > 12 {
			d, mo = mo, d
		}
		if s, ok := ymd(y, mo, d); ok {
			add(s, m[0], m[1])
		}
	}
	return out
}
