// Package likeness compares names and addresses the way a person reading a
// statement would: case, accents, punctuation, common abbreviations, name
// order and initials don't matter, and a unit number or middle initial on
// only one side is a harmless extra. It never rejects or changes non-ASCII
// text; it only folds it to compare.
package likeness

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Verdict is how two values compare.
type Verdict int

const (
	// Same: the same value, written differently.
	Same Verdict = iota
	// Extra: the same, but one side has a harmless extra; Note says what.
	Extra
	// Unsure: they share something but plain rules can't tell.
	Unsure
	// Different: nothing in common.
	Different
)

// Result is a comparison with a plain note for Extra.
type Result struct {
	Verdict Verdict
	Note    string
}

// letters that don't decompose into a base letter and a mark.
var folds = map[rune]string{'ł': "l", 'Ł': "l", 'ø': "o", 'Ø': "o", 'ß': "ss", 'æ': "ae", 'Æ': "ae", 'œ': "oe", 'Œ': "oe",
	'đ': "d", 'Đ': "d", 'ð': "d", 'þ': "th", 'ı': "i"}

// Fold lowercases text and removes accents, for comparing only.
func Fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if f, ok := folds[r]; ok {
			b.WriteString(f)
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func words(s string) []string {
	return strings.FieldsFunc(Fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// Company forms and joining words that don't tell two names apart.
var nameNoise = map[string]bool{"inc": true, "incorporated": true, "llc": true, "l": true, "ltd": true, "limited": true, "corp": true,
	"corporation": true, "co": true, "company": true, "the": true, "and": true, "plc": true, "pllc": true, "lp": true, "llp": true,
	"gmbh": true, "ag": true, "sa": true, "sas": true, "sarl": true, "bv": true, "nv": true, "sp": true, "z": true, "o": true, "oo": true,
	"spzoo": true, "pty": true, "mr": true, "mrs": true, "ms": true, "dr": true}

func nameWords(s string) []string {
	var out []string
	for _, w := range words(s) {
		if !nameNoise[w] {
			out = append(out, w)
		}
	}
	return out
}

// Names compares two names of a person or company.
func Names(a, b string) Result {
	x, y := nameWords(a), nameWords(b)
	if len(x) == 0 || len(y) == 0 {
		return Result{Verdict: Unsure}
	}
	if len(x) > len(y) {
		x, y = y, x
	}
	// Match each word of the shorter name to one of the longer, exactly or
	// as an initial.
	used := make([]bool, len(y))
	initials := 0
	for _, w := range x {
		found := false
		for i, v := range y {
			if !used[i] && v == w {
				used[i], found = true, true
				break
			}
		}
		if !found {
			for i, v := range y {
				if !used[i] && (len(w) == 1 && strings.HasPrefix(v, w) || len(v) == 1 && strings.HasPrefix(w, v)) {
					used[i], found = true, true
					initials++
					break
				}
			}
		}
		if !found {
			shared := 0
			for _, w := range x {
				for _, v := range y {
					if w == v && len(w) > 1 {
						shared++
					}
				}
			}
			if shared > 0 || near(x, y) {
				return Result{Verdict: Unsure}
			}
			return Result{Verdict: Different}
		}
	}
	extra := len(y) - len(x)
	switch {
	case extra == 0 && initials == 0:
		return Result{Verdict: Same}
	case len(x) < 2 && extra > 0:
		// One word against several can't tell first from last names.
		return Result{Verdict: Unsure}
	case extra == 0:
		return Result{Verdict: Extra, Note: "One side gives an initial where the other has the name."}
	default:
		return Result{Verdict: Extra, Note: "One side has a middle name or initial the other leaves out."}
	}
}

// Address words with the same meaning, folded to one.
var addressWords = map[string]string{"street": "st", "str": "st", "st": "st", "ulica": "ul", "ul": "ul", "avenue": "ave", "av": "ave",
	"ave": "ave", "road": "rd", "rd": "rd", "drive": "dr", "dr": "dr", "boulevard": "blvd", "blvd": "blvd", "lane": "ln", "ln": "ln",
	"place": "pl", "plac": "pl", "pl": "pl", "court": "ct", "ct": "ct", "aleja": "al", "al": "al", "aleje": "al", "osiedle": "os",
	"os": "os", "north": "n", "n": "n", "south": "s", "s": "s", "east": "e", "e": "e", "west": "w", "w": "w", "highway": "hwy",
	"hwy": "hwy", "suite": "unit", "ste": "unit", "apt": "unit", "apartment": "unit", "unit": "unit", "flat": "unit", "lok": "unit",
	"lokal": "unit", "m": "unit", "room": "unit", "rm": "unit", "floor": "fl", "fl": "fl", "fwy": "fwy"}

// address is a postal address split into its building part and its unit.
type address struct {
	words []string
	unit  string
}

func parseAddress(s string) address {
	// 50/7 is building 50, flat 7; # starts a unit.
	folded := Fold(s)
	var b strings.Builder
	for i, r := range folded {
		switch {
		case r == '/' && i > 0 && unicode.IsDigit(rune(folded[i-1])):
			b.WriteString(" unit ")
		case r == '#':
			b.WriteString(" unit ")
		default:
			b.WriteRune(r)
		}
	}
	raw := strings.FieldsFunc(b.String(), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' })
	var out address
	for i := 0; i < len(raw); i++ {
		w := strings.Trim(raw[i], "-")
		if w == "" {
			continue
		}
		// Postal codes: 31-001 and 31001 are the same; so are SW1A 1AA and SW1A1AA.
		w = strings.ReplaceAll(w, "-", "")
		if v, ok := addressWords[w]; ok {
			w = v
		}
		if w == "unit" {
			if i+1 < len(raw) {
				out.unit = strings.ReplaceAll(raw[i+1], "-", "")
				i++
			}
			continue
		}
		out.words = append(out.words, w)
	}
	// Join a split postcode such as "sw1a 1aa" so both spellings compare
	// equal: adjacent short letter-and-digit words become one.
	var joined []string
	for i := 0; i < len(out.words); i++ {
		w := out.words[i]
		if i+1 < len(out.words) && mixed(w) && mixed(out.words[i+1]) && len(w) <= 4 && len(out.words[i+1]) == 3 {
			w += out.words[i+1]
			i++
		}
		joined = append(joined, w)
	}
	out.words = joined
	return out
}

func mixed(w string) bool {
	letter, digit := false, false
	for _, r := range w {
		letter = letter || unicode.IsLetter(r)
		digit = digit || unicode.IsDigit(r)
	}
	return letter && digit
}

func sameSet(x, y []string) bool {
	if len(x) != len(y) {
		return false
	}
	count := map[string]int{}
	for _, w := range x {
		count[w]++
	}
	for _, w := range y {
		count[w]--
		if count[w] < 0 {
			return false
		}
	}
	return true
}

// Addresses compares two postal addresses. found is the address being
// checked and expected the one it's compared with; the note names them as
// written.
func Addresses(found, expected string) Result {
	x, y := parseAddress(found), parseAddress(expected)
	if len(x.words) == 0 || len(y.words) == 0 {
		return Result{Verdict: Unsure}
	}
	if !sameSet(x.words, y.words) {
		shared := 0
		for _, w := range x.words {
			for _, v := range y.words {
				if w == v {
					shared++
					break
				}
			}
		}
		if shared == 0 {
			return Result{Verdict: Different}
		}
		return Result{Verdict: Unsure}
	}
	switch {
	case x.unit == y.unit:
		return Result{Verdict: Same}
	case y.unit == "":
		return Result{Verdict: Extra, Note: "The statement shows " + strings.TrimSpace(found) + "; the Book expects no flat or unit number."}
	case x.unit == "":
		return Result{Verdict: Extra, Note: "The statement shows no flat or unit number; the Book expects " + strings.TrimSpace(expected) + "."}
	default:
		return Result{Verdict: Different}
	}
}

// near says whether two names have words spelled almost alike, as with a
// transliteration or a typing slip: "Lucia" and "Łucja".
func near(x, y []string) bool {
	for _, w := range x {
		for _, v := range y {
			if len(w) >= 4 && len(v) >= 4 && distance(w, v) <= (min(len(w), len(v))+3)/5 {
				return true
			}
		}
	}
	return false
}

// distance is the edit distance between two words.
func distance(a, b string) int {
	x, y := []rune(a), []rune(b)
	prev := make([]int, len(y)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(x); i++ {
		cur := make([]int, len(y)+1)
		cur[0] = i
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(y)]
}
