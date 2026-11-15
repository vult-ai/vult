package docread

import (
	"bytes"
	"encoding/xml"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/statementfile"
)

// ErrNotExport means the file isn't a statement export this package reads.
var ErrNotExport = errors.New("docread: not a statement export")

// Export reads a bank's statement export: OFX/QFX, CAMT.053, MT940 or CSV.
// Amounts follow BankSigns.
func Export(data []byte, mediaType, name string) (Reading, error) {
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	lower := strings.ToLower(string(head))
	switch {
	case strings.Contains(lower, "<ofx") || strings.Contains(lower, "ofxheader"):
		return OFX(data)
	case strings.Contains(lower, "camt.053") || strings.Contains(lower, "bktocstmrstmt"):
		return CAMT053(data)
	case mt940Start.Match(head) && bytes.Contains(data, []byte(":60")):
		return MT940(data)
	case mediaType == "text/csv" || strings.HasSuffix(strings.ToLower(name), ".csv"):
		return CSV(data)
	}
	return Reading{}, ErrNotExport
}

// minorText writes minor units at scale 2 as a plain decimal.
func minorText(n int64) string {
	r := new(big.Rat).SetFrac64(n, 100)
	d, _ := Decimal(r.FloatString(2))
	return d
}

// CSV reads a CSV export through the statement file reader. A CSV names no
// account or holder.
func CSV(data []byte) (Reading, error) {
	st, err := statementfile.ReadCSV(data, "cash", 2)
	if err != nil {
		return Reading{}, err
	}
	r := Reading{Kind: "statement", Signs: BankSigns, PeriodStart: Field{Value: st.PeriodStart}, PeriodEnd: Field{Value: st.PeriodEnd}}
	sum := int64(0)
	for _, l := range st.Lines {
		r.Lines = append(r.Lines, Line{Date: l.Date, Description: l.Description, Amount: minorText(l.AmountMinor)})
		sum += l.AmountMinor
	}
	if st.Balances {
		r.Opening, r.Closing = Field{Value: minorText(st.Opening)}, Field{Value: minorText(st.Closing)}
	}
	return r, nil
}

// ofxTag reads OFX in both its SGML and XML forms: a tag's value runs to
// the next tag.
var ofxTag = regexp.MustCompile(`(?s)<([A-Za-z0-9.]+)>([^<]*)`)

func ofxDate(v string) string {
	v = strings.TrimSpace(v)
	if len(v) < 8 {
		return ""
	}
	s, ok := ymdText(v[0:4], v[4:6], v[6:8])
	if !ok {
		return ""
	}
	return s
}

func ymdText(y, m, d string) (string, bool) {
	var yi, mi, di int
	for _, c := range y + m + d {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	for _, c := range y {
		yi = yi*10 + int(c-'0')
	}
	for _, c := range m {
		mi = mi*10 + int(c-'0')
	}
	for _, c := range d {
		di = di*10 + int(c-'0')
	}
	return ymd(yi, mi, di)
}

// OFX reads an OFX or QFX statement. It gives a closing (ledger) balance
// only; the opening is worked out from it and the lines.
func OFX(data []byte) (Reading, error) {
	r := Reading{Kind: "statement", Signs: BankSigns}
	var line *Line
	var balance string
	inBalance := false
	for _, m := range ofxTag.FindAllSubmatch(data, -1) {
		tag, value := strings.ToUpper(string(m[1])), strings.TrimSpace(string(m[2]))
		switch tag {
		case "ORG":
			if r.Institution.Value == "" {
				r.Institution.Value = value
			}
		case "ACCTID":
			r.Account.Value = value
		case "DTSTART":
			r.PeriodStart.Value = ofxDate(value)
		case "DTEND":
			r.PeriodEnd.Value = ofxDate(value)
		case "STMTTRN":
			r.Lines = append(r.Lines, Line{})
			line = &r.Lines[len(r.Lines)-1]
		case "DTPOSTED":
			if line != nil {
				line.Date = ofxDate(value)
			}
		case "TRNAMT":
			if line != nil {
				line.Amount, _ = Decimal(value)
			}
		case "NAME", "MEMO":
			if line != nil && line.Description == "" {
				line.Description = value
			}
		case "LEDGERBAL":
			inBalance = true
		case "BALAMT":
			if inBalance && balance == "" {
				balance = value
			}
		case "AVAILBAL":
			inBalance = false
		}
	}
	if r.Account.Value == "" && len(r.Lines) == 0 {
		return Reading{}, ErrNotExport
	}
	if closing, ok := Decimal(balance); ok {
		r.Closing.Value = closing
		r.Opening.Value = subtract(closing, r.Lines)
		r.OpeningDerived = true
	}
	return r, nil
}

// subtract works out an opening balance: closing less the lines.
func subtract(closing string, lines []Line) string {
	total, ok := new(big.Rat).SetString(closing)
	if !ok {
		return ""
	}
	for _, l := range lines {
		n, ok := new(big.Rat).SetString(l.Amount)
		if !ok {
			return ""
		}
		total.Sub(total, n)
	}
	d, _ := Decimal(total.FloatString(6))
	return d
}

// CAMT.053, the ISO 20022 bank-to-customer statement, read by local
// element names so any version's namespace is accepted.
type camtDocument struct {
	Statements []camtStatement `xml:"BkToCstmrStmt>Stmt"`
}

type camtStatement struct {
	Account struct {
		IBAN  string `xml:"Id>IBAN"`
		Other string `xml:"Id>Othr>Id"`
		Owner string `xml:"Ownr>Nm"`
		Bank  string `xml:"Svcr>FinInstnId>Nm"`
	} `xml:"Acct"`
	From     string        `xml:"FrToDt>FrDtTm"`
	To       string        `xml:"FrToDt>ToDtTm"`
	Balances []camtBalance `xml:"Bal"`
	Entries  []struct {
		Amount string `xml:"Amt"`
		Side   string `xml:"CdtDbtInd"`
		// Sts holds a code in newer versions and plain text in older ones.
		Status struct {
			Code string `xml:"Cd"`
			Text string `xml:",chardata"`
		} `xml:"Sts"`
		Booked     string `xml:"BookgDt>Dt"`
		BookedTime string `xml:"BookgDt>DtTm"`
		Info       string `xml:"AddtlNtryInf"`
		Party      string `xml:"NtryDtls>TxDtls>RltdPties>Cdtr>Nm"`
	} `xml:"Ntry"`
}

type camtBalance struct {
	Type   string `xml:"Tp>CdOrPrtry>Cd"`
	Amount string `xml:"Amt"`
	Side   string `xml:"CdtDbtInd"`
	Date   string `xml:"Dt>Dt"`
}

func signed(amount, side string) string {
	d, ok := Decimal(amount)
	if !ok {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(side), "DBIT") && d != "0" {
		d = "-" + strings.TrimPrefix(d, "-")
	}
	return d
}

// CAMT053 reads a CAMT.053 statement: its first statement.
func CAMT053(data []byte) (Reading, error) {
	var doc camtDocument
	if err := xml.Unmarshal(data, &doc); err != nil || len(doc.Statements) == 0 {
		return Reading{}, ErrNotExport
	}
	s := doc.Statements[0]
	r := Reading{Kind: "statement", Signs: BankSigns, Holder: Field{Value: strings.TrimSpace(s.Account.Owner)},
		Institution: Field{Value: strings.TrimSpace(s.Account.Bank)}}
	r.Account.Value = strings.TrimSpace(s.Account.IBAN)
	if r.Account.Value == "" {
		r.Account.Value = strings.TrimSpace(s.Account.Other)
	}
	if len(s.From) >= 10 {
		r.PeriodStart.Value = s.From[:10]
	}
	if len(s.To) >= 10 {
		r.PeriodEnd.Value = s.To[:10]
	}
	for _, b := range s.Balances {
		switch b.Type {
		case "OPBD", "PRCD":
			if r.Opening.Value == "" {
				r.Opening.Value = signed(b.Amount, b.Side)
			}
		case "CLBD":
			r.Closing.Value = signed(b.Amount, b.Side)
		}
	}
	for _, e := range s.Entries {
		status := strings.TrimSpace(e.Status.Code)
		if status == "" {
			status = strings.TrimSpace(e.Status.Text)
		}
		if status != "" && status != "BOOK" {
			continue
		}
		date := e.Booked
		if date == "" && len(e.BookedTime) >= 10 {
			date = e.BookedTime[:10]
		}
		text := strings.TrimSpace(e.Info)
		if text == "" {
			text = strings.TrimSpace(e.Party)
		}
		r.Lines = append(r.Lines, Line{Date: date, Description: text, Amount: signed(e.Amount, e.Side)})
	}
	return r, nil
}

var (
	mt940Start   = regexp.MustCompile(`(?m)^:20:`)
	mt940Field   = regexp.MustCompile(`(?m)^:(\d{2}[A-Z]?):`)
	mt940Balance = regexp.MustCompile(`^([CD])(\d{6})([A-Z]{3})([\d,]+)`)
	mt940Line    = regexp.MustCompile(`^(\d{6})(\d{4})?(R?[CD])[A-Z]?([\d,]+)`)
)

func mt940Date(yymmdd string) string {
	s, _ := ymdText("20"+yymmdd[0:2], yymmdd[2:4], yymmdd[4:6])
	return s
}

func mt940Amount(side, amount string) string {
	d, ok := Decimal(strings.TrimSuffix(amount, ","))
	if !ok {
		d, ok = Decimal(amount)
	}
	if !ok {
		return ""
	}
	if (side == "D" || side == "RC") && d != "0" {
		d = "-" + d
	}
	return d
}

// MT940 reads a SWIFT MT940 statement: its first statement.
func MT940(data []byte) (Reading, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	r := Reading{Kind: "statement", Signs: BankSigns}
	locs := mt940Field.FindAllStringSubmatchIndex(text, -1)
	var last *Line
	for i, m := range locs {
		tag := text[m[2]:m[3]]
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		value := strings.TrimSpace(text[m[1]:end])
		switch tag {
		case "25":
			r.Account.Value = value
		case "60F", "60M":
			if r.Opening.Value == "" {
				if b := mt940Balance.FindStringSubmatch(value); b != nil {
					r.Opening.Value = mt940Amount(b[1], b[4])
					r.PeriodStart.Value = mt940Date(b[2])
					// An opening balance dated a month's last day is the
					// previous statement's close; this one starts the next day.
					if t, err := time.Parse(time.DateOnly, r.PeriodStart.Value); err == nil && t.AddDate(0, 0, 1).Day() == 1 {
						r.PeriodStart.Value = t.AddDate(0, 0, 1).Format(time.DateOnly)
					}
				}
			}
		case "62F", "62M":
			if b := mt940Balance.FindStringSubmatch(value); b != nil {
				r.Closing.Value = mt940Amount(b[1], b[4])
				r.PeriodEnd.Value = mt940Date(b[2])
			}
		case "61":
			if l := mt940Line.FindStringSubmatch(value); l != nil {
				r.Lines = append(r.Lines, Line{Date: mt940Date(l[1]), Amount: mt940Amount(l[3], l[4])})
				last = &r.Lines[len(r.Lines)-1]
			}
		case "86":
			if last != nil && last.Description == "" {
				last.Description = strings.Join(strings.Fields(value), " ")
			}
		}
		if tag == "62F" {
			break
		}
	}
	if r.Account.Value == "" || r.Closing.Value == "" {
		return Reading{}, ErrNotExport
	}
	return r, nil
}
