// Package statementfile reads bank and card statements exported as CSV into
// statement lines, in plain code, so they reach the bank activity inbox
// without an agent. Files that need reading, such as PDFs and scans, are
// left to an agent.
package statementfile

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Line is one transaction from the file. AmountMinor is signed the way the
// account's balance moves: a deposit is positive on a bank account, a
// charge is positive on a card.
type Line struct {
	Row         int
	Date        string
	AmountMinor int64
	Description string
	ExternalID  string
	Raw         string
}

// Statement is what the file says. With Balances, Opening and Closing come
// from the file's running balance; without, the file gives none.
type Statement struct {
	Lines       []Line
	PeriodStart string
	PeriodEnd   string
	Balances    bool
	Opening     int64
	Closing     int64
}

// Problem is a reason the file can't be read, in plain words.
type Problem struct{ Reason string }

func (p *Problem) Error() string { return p.Reason }

const maxLines = 5000

var (
	dateNames    = []string{"date", "transaction date", "posted date", "posting date", "post date", "booking date", "trans date", "value date"}
	amountNames  = []string{"amount", "transaction amount", "amount (usd)", "amount usd", "net amount"}
	outNames     = []string{"debit", "debits", "withdrawal", "withdrawals", "money out", "paid out", "debit amount", "outflow"}
	inNames      = []string{"credit", "credits", "deposit", "deposits", "money in", "paid in", "credit amount", "inflow"}
	textNames    = []string{"description", "payee", "name", "merchant", "details", "narrative", "transaction description", "memo", "reference description"}
	balanceNames = []string{"balance", "running balance", "available balance", "ledger balance"}
	idNames      = []string{"transaction id", "id", "fitid", "reference", "ref", "reference number"}
)

// ReadCSV reads a statement for an account of kind cash or card, in an
// account with this many decimal places. Amounts in the file are read as
// the bank writes them: money out negative or in a debit column.
func ReadCSV(data []byte, kind string, scale int) (Statement, error) {
	var out Statement
	if kind != "cash" && kind != "card" {
		return out, errors.New("statementfile: kind is cash or card")
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return out, &Problem{"This file isn't text Vult can read. Export the statement as CSV (UTF-8)."}
	}
	r := csv.NewReader(bytes.NewReader(data))
	// Some banks separate columns with semicolons.
	if first, _, _ := bytes.Cut(data, []byte("\n")); bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")) {
		r.Comma = ';'
	}
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var records [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, &Problem{"This CSV file is malformed: " + err.Error() + "."}
		}
		if len(strings.Join(rec, "")) > 0 {
			records = append(records, rec)
		}
	}
	header, at := findHeader(records)
	if header == nil {
		return out, &Problem{"Vult couldn't find the columns in this file. It needs a header row with a date, a description, and either an amount or debit and credit columns."}
	}
	col := func(names []string) int {
		for _, n := range names {
			for i, h := range header {
				if h == n {
					return i
				}
			}
		}
		return -1
	}
	date, amount, out1, in1, text, balance, id := col(dateNames), col(amountNames), col(outNames), col(inNames), col(textNames), col(balanceNames), col(idNames)
	body := records[at+1:]
	if len(body) == 0 {
		return out, &Problem{"This file has no transactions."}
	}
	if len(body) > maxLines {
		return out, &Problem{fmt.Sprintf("This file has %d lines; Vult reads up to %d at a time. Export a shorter period.", len(body), maxLines)}
	}
	layout, err := dateLayout(body, date)
	if err != nil {
		return out, err
	}
	type parsed struct {
		Line
		balance *int64
	}
	var lines []parsed
	type statedBalance struct {
		date    string
		balance int64
	}
	var stated []statedBalance
	for i, rec := range body {
		row := at + i + 2
		get := func(c int) string {
			if c < 0 || c >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[c])
		}
		when, err := time.Parse(layout, normalizeDate(get(date)))
		if err != nil {
			return out, &Problem{fmt.Sprintf("Line %d: %q isn't a date Vult can read.", row, get(date))}
		}
		// A row with no amount but a balance states the balance, such as an
		// opening balance line; it isn't a transaction.
		if get(amount) == "" && get(out1) == "" && get(in1) == "" {
			if b, err := minor(get(balance), scale); balance >= 0 && get(balance) != "" && err == nil {
				stated = append(stated, statedBalance{date: when.Format(time.DateOnly), balance: b})
			}
			continue
		}
		var value int64
		if amount >= 0 {
			v, err := minor(get(amount), scale)
			if err != nil {
				return out, &Problem{fmt.Sprintf("Line %d: %s", row, err)}
			}
			value = v
		} else {
			o, err1 := minorOrZero(get(out1), scale)
			n, err2 := minorOrZero(get(in1), scale)
			if err1 != nil || err2 != nil {
				return out, &Problem{fmt.Sprintf("Line %d: the debit or credit isn't an amount Vult can read.", row)}
			}
			value = abs(n) - abs(o)
		}
		if value == 0 {
			continue
		}
		// The file speaks for the account holder's money; a card's balance
		// owed moves the other way.
		if kind == "card" {
			value = -value
		}
		raw := strings.Join(rec, ",")
		if len(raw) > 4000 {
			raw = raw[:4000]
		}
		p := parsed{Line: Line{Row: row, Date: when.Format(time.DateOnly), AmountMinor: value, Description: clip(get(text), 1000), ExternalID: clip(get(id), 256), Raw: raw}}
		if balance >= 0 && get(balance) != "" {
			if b, err := minor(get(balance), scale); err == nil {
				p.balance = &b
			}
		}
		lines = append(lines, p)
	}
	if len(lines) == 0 {
		return out, &Problem{"This file has no transactions with an amount."}
	}
	// Files list newest or oldest first; statements read oldest first, and
	// lines on the same day keep the file's order.
	if lines[0].Date > lines[len(lines)-1].Date {
		for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
			lines[i], lines[j] = lines[j], lines[i]
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Date < lines[j].Date })
	out.PeriodStart, out.PeriodEnd = lines[0].Date, lines[len(lines)-1].Date
	for _, l := range lines {
		out.Lines = append(out.Lines, l.Line)
	}
	// A balance stated before the first transaction is the opening balance;
	// with no running balance on the lines, the closing follows from them.
	var opening *int64
	for _, b := range stated {
		if b.date <= out.PeriodStart && kind == "cash" {
			v := b.balance
			opening = &v
		}
	}
	// A running balance counts only on a bank account, and only when every
	// line agrees with it.
	if opening != nil && (balance < 0 || lines[0].balance == nil) {
		sum := int64(0)
		for _, l := range lines {
			sum += l.AmountMinor
		}
		out.Balances, out.Opening, out.Closing = true, *opening, *opening+sum
		return out, nil
	}
	if kind == "cash" && balance >= 0 {
		ok := true
		for i, l := range lines {
			if l.balance == nil || i > 0 && *lines[i-1].balance+l.AmountMinor != *l.balance {
				ok = false
				break
			}
		}
		if ok && (opening == nil || *opening == *lines[0].balance-lines[0].AmountMinor) {
			out.Balances = true
			out.Opening = *lines[0].balance - lines[0].AmountMinor
			out.Closing = *lines[len(lines)-1].balance
		}
	}
	return out, nil
}

func findHeader(records [][]string) ([]string, int) {
	for i, rec := range records {
		if i > 10 {
			break
		}
		h := make([]string, len(rec))
		hasDate, hasAmount := false, false
		for j, v := range rec {
			h[j] = strings.ToLower(strings.Join(strings.Fields(strings.Trim(v, " \"'")), " "))
			hasDate = hasDate || contains(dateNames, h[j])
			hasAmount = hasAmount || contains(amountNames, h[j]) || contains(outNames, h[j]) || contains(inNames, h[j])
		}
		if hasDate && hasAmount {
			return h, i
		}
	}
	return nil, 0
}

var slashDate = regexp.MustCompile(`^(\d{1,2})[/.-](\d{1,2})[/.-](\d{2}|\d{4})$`)

// dateLayout picks one layout for the whole file. Day and month order is
// day first when any first number is over 12, month first otherwise.
func dateLayout(body [][]string, c int) (string, error) {
	dayFirst, slashes := false, false
	var sample string
	for _, rec := range body {
		if c >= len(rec) {
			continue
		}
		v := normalizeDate(strings.TrimSpace(rec[c]))
		if sample == "" {
			sample = v
		}
		if m := slashDate.FindStringSubmatch(v); m != nil {
			slashes = true
			if n, _ := strconv.Atoi(m[1]); n > 12 {
				dayFirst = true
			}
		}
	}
	if slashes {
		m := slashDate.FindStringSubmatch(sample)
		if m == nil {
			return "", &Problem{"This file mixes date formats. Export it again with one date format."}
		}
		year := "2006"
		if len(m[3]) == 2 {
			year = "06"
		}
		if dayFirst {
			return "2/1/" + year, nil
		}
		return "1/2/" + year, nil
	}
	for _, layout := range []string{time.DateOnly, "2006/01/02", "Jan 2 2006", "2 Jan 2006", "January 2 2006", "2 January 2006", "20060102"} {
		if _, err := time.Parse(layout, sample); err == nil {
			return layout, nil
		}
	}
	return "", &Problem{fmt.Sprintf("%q isn't a date format Vult can read. Use dates like 2026-01-31.", sample)}
}

// normalizeDate turns separators into one form, so one layout reads them.
func normalizeDate(v string) string {
	v = strings.ReplaceAll(v, ",", "")
	if m := slashDate.FindStringSubmatch(v); m != nil {
		return strings.TrimLeft(m[1], "0") + "/" + strings.TrimLeft(m[2], "0") + "/" + m[3]
	}
	return strings.Join(strings.Fields(v), " ")
}

// minor reads an amount as the file writes it: currency signs, thousands
// separators, parentheses or a trailing DR for money out, CR for money in.
func minor(v string, scale int) (int64, error) {
	s := strings.TrimSpace(v)
	negative := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		negative, s = true, s[1:len(s)-1]
	}
	upper := strings.ToUpper(s)
	switch {
	case strings.HasSuffix(upper, "DR"):
		negative, s = !negative, strings.TrimSpace(s[:len(s)-2])
	case strings.HasSuffix(upper, "CR"):
		s = strings.TrimSpace(s[:len(s)-2])
	}
	s = strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r == '.' || r == ',' || r == '-' || r == '+' {
			return r
		}
		return -1
	}, s)
	if strings.HasPrefix(s, "-") {
		negative, s = !negative, s[1:]
	} else if strings.HasSuffix(s, "-") {
		negative, s = !negative, s[:len(s)-1]
	}
	s = strings.TrimPrefix(s, "+")
	// "1.234,56" writes the decimal with a comma.
	if i, j := strings.LastIndex(s, ","), strings.LastIndex(s, "."); i > j && len(s)-i-1 != 3 {
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" && frac == "" || strings.ContainsAny(whole+frac, ".,") {
		return 0, fmt.Errorf("%q isn't an amount Vult can read", v)
	}
	if len(frac) > scale {
		if strings.Trim(frac[scale:], "0") != "" {
			return 0, fmt.Errorf("%q has more decimal places than this account uses", v)
		}
		frac = frac[:scale]
	}
	n, ok := new(big.Int).SetString(whole+frac+strings.Repeat("0", scale-len(frac)), 10)
	if !ok || !n.IsInt64() {
		return 0, fmt.Errorf("%q isn't an amount Vult can read", v)
	}
	if negative {
		return -n.Int64(), nil
	}
	return n.Int64(), nil
}

func minorOrZero(v string, scale int) (int64, error) {
	if strings.TrimSpace(v) == "" {
		return 0, nil
	}
	return minor(v, scale)
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
