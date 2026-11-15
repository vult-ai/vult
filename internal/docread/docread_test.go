package docread

import (
	"errors"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/pdftext"
	"github.com/vult-ai/vult/internal/pdftext/pdftest"
)

func TestDecimalReadsEveryNumberFormat(t *testing.T) {
	for text, want := range map[string]string{
		"1 234,56": "1234.56", "1.234,56": "1234.56", "1,234.56": "1234.56", "1'234.56": "1234.56", "-50.00": "-50", "50,00-": "-50",
		"(50.00)": "-50", "50.00 DR": "-50", "50.00CR": "50", "€ 12,50": "12.5", "12,50 zł": "12.5", "1 234 567,89 PLN": "1234567.89",
		"$4,210.00": "4210", "0,01": "0.01", "1,234": "1234", "−7.25": "-7.25", "100": "100", "1.234.567,00": "1234567",
	} {
		if got, ok := Decimal(text); !ok || got != want {
			t.Errorf("Decimal(%q) = %q, %v; want %q", text, got, ok, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3", "12-34"} {
		if got, ok := Decimal(bad); ok {
			t.Errorf("Decimal(%q) = %q, should refuse", bad, got)
		}
	}
}

func TestDatesReadEveryFormat(t *testing.T) {
	for text, want := range map[string]string{
		"2026-09-05": "2026-09-05", "05.09.2026": "2026-09-05", "5 września 2026": "2026-09-05", "5 Sep 2026": "2026-09-05",
		"September 5, 2026": "2026-09-05", "5. September 2026": "2026-09-05", "5 septembre 2026": "2026-09-05", "5 de septiembre de 2026": "",
		"25/09/2026": "2026-09-25",
	} {
		ds := Dates(text, false)
		got := ""
		if len(ds) > 0 {
			got = ds[0].Date
		}
		if got != want {
			t.Errorf("Dates(%q) = %q; want %q", text, got, want)
		}
	}
	if ds := Dates("09/05/2026", true); len(ds) != 1 || ds[0].Date != "2026-09-05" {
		t.Error("month first in US English", ds)
	}
}

func textOf(t *testing.T, pages [][]string) []string {
	t.Helper()
	got, err := pdftext.Pages(pdftest.PDF(pages, pdftest.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAnEnglishPDFIsReadWithoutAI(t *testing.T) {
	pages := textOf(t, [][]string{{
		"Example Bank",
		"Account holder: Eightball, Inc.",
		"100 Main Street Unit 4",
		"Springfield, IL 62701",
		"Account number: ****0001",
		"Statement period: 09/01/2026 - 09/30/2026",
		"Opening balance $1,000.00",
		"Date Description Amount Balance",
		"09/05/2026 Example supplies -50.01 949.99",
		"09/18/2026 Example client payment 1,200.00 2,149.99",
	}, {
		"09/29/2026 Example software -200.00 1,949.99",
		"Closing balance $1,949.99",
	}})
	r := Text(pages)
	if r.Holder.Value != "Eightball, Inc." || r.Address.Value != "100 Main Street Unit 4, Springfield, IL 62701" {
		t.Fatalf("holder and address: %+v %+v", r.Holder, r.Address)
	}
	if r.Last4() != "0001" || r.PeriodStart.Value != "2026-09-01" || r.PeriodEnd.Value != "2026-09-30" {
		t.Fatalf("account and period: %+v %+v %+v", r.Account, r.PeriodStart, r.PeriodEnd)
	}
	if r.Opening.Value != "1000" || r.Closing.Value != "1949.99" || r.Closing.Page != 2 {
		t.Fatalf("balances: %+v %+v", r.Opening, r.Closing)
	}
	if len(r.Lines) != 3 || r.Lines[0].Amount != "-50.01" || r.Lines[1].Amount != "1200" || r.Lines[2].Page != 2 || r.Lines[0].Description != "Example supplies" {
		t.Fatalf("lines: %+v", r.Lines)
	}
	if missing := r.Missing(); len(missing) != 0 {
		t.Fatal("nothing missing", missing)
	}
	if err := Verify(r, pages); err != nil {
		t.Fatal("what was read adds up", err)
	}
}

func TestAPolishPDFIsReadWithoutAI(t *testing.T) {
	pages := textOf(t, [][]string{{
		"Przykładowy Bank S.A.",
		"Właściciel rachunku: Łucja Wójcik",
		"Rynek 50/7",
		"31-001 Kraków",
		"Numer rachunku: PL61 1090 1014 0000 0712 1981 2874",
		"Okres: 01.09.2026 - 30.09.2026",
		"Saldo początkowe: 1 000,00 PLN",
		"Data Opis Kwota Saldo",
		"05.09.2026 Zakupy w sklepie -50,01 949,99",
		"18.09.2026 Wpływ od klienta 1 200,00 2 149,99",
		"Saldo końcowe: 2 149,99 PLN",
	}})
	r := Text(pages)
	if r.Holder.Value != "Łucja Wójcik" || r.Address.Value != "Rynek 50/7, 31-001 Kraków" {
		t.Fatalf("holder and address keep their accents: %+v %+v", r.Holder, r.Address)
	}
	if r.Last4() != "2874" || r.PeriodStart.Value != "2026-09-01" || r.PeriodEnd.Value != "2026-09-30" {
		t.Fatalf("account and period: %+v %+v %+v", r.Account, r.PeriodStart, r.PeriodEnd)
	}
	if r.Opening.Value != "1000" || r.Closing.Value != "2149.99" || len(r.Lines) != 2 || r.Lines[0].Amount != "-50.01" || r.Lines[1].Amount != "1200" {
		t.Fatalf("balances and lines: %+v %+v %+v", r.Opening, r.Closing, r.Lines)
	}
	if err := Verify(r, pages); err != nil {
		t.Fatal(err)
	}
}

func TestExportsAreReadWithoutAI(t *testing.T) {
	csv := "Date,Description,Amount,Balance\n2026-09-05,Example supplies,-50.01,949.99\n2026-09-18,Example client,1200.00,2149.99\n"
	ofx := `OFXHEADER:100
DATA:OFXSGML
<OFX><SIGNONMSGSRSV1><SONRS><FI><ORG>Example Bank</ORG></FI></SONRS></SIGNONMSGSRSV1>
<BANKMSGSRSV1><STMTTRNRS><STMTRS><CURDEF>USD<BANKACCTFROM><BANKID>123<ACCTID>9876540001<ACCTTYPE>CHECKING</BANKACCTFROM>
<BANKTRANLIST><DTSTART>20260901<DTEND>20260930
<STMTTRN><TRNTYPE>DEBIT<DTPOSTED>20260905120000<TRNAMT>-50.01<FITID>1<NAME>Example supplies</STMTTRN>
<STMTTRN><TRNTYPE>CREDIT<DTPOSTED>20260918<TRNAMT>1200.00<FITID>2<NAME>Example client</STMTTRN>
</BANKTRANLIST><LEDGERBAL><BALAMT>2149.99<DTASOF>20260930</LEDGERBAL><AVAILBAL><BALAMT>2000<DTASOF>20260930</AVAILBAL>
</STMTRS></STMTTRNRS></BANKMSGSRSV1></OFX>`
	camt := `<?xml version="1.0" encoding="UTF-8"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"><BkToCstmrStmt><GrpHdr><MsgId>1</MsgId></GrpHdr><Stmt><Id>1</Id>
<FrToDt><FrDtTm>2026-09-01T00:00:00</FrDtTm><ToDtTm>2026-09-30T23:59:59</ToDtTm></FrToDt>
<Acct><Id><IBAN>PL61109010140000071219812874</IBAN></Id><Ownr><Nm>Łucja Wójcik</Nm></Ownr><Svcr><FinInstnId><Nm>Przykładowy Bank</Nm></FinInstnId></Svcr></Acct>
<Bal><Tp><CdOrPrtry><Cd>OPBD</Cd></CdOrPrtry></Tp><Amt Ccy="PLN">1000.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-09-01</Dt></Dt></Bal>
<Bal><Tp><CdOrPrtry><Cd>CLBD</Cd></CdOrPrtry></Tp><Amt Ccy="PLN">2149.99</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-09-30</Dt></Dt></Bal>
<Ntry><Amt Ccy="PLN">50.01</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts><BookgDt><Dt>2026-09-05</Dt></BookgDt><AddtlNtryInf>Zakupy</AddtlNtryInf></Ntry>
<Ntry><Amt Ccy="PLN">1200.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts><BookgDt><Dt>2026-09-18</Dt></BookgDt><AddtlNtryInf>Wpływ</AddtlNtryInf></Ntry>
<Ntry><Amt Ccy="PLN">9.99</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts><Cd>PDNG</Cd></Sts><BookgDt><Dt>2026-09-30</Dt></BookgDt></Ntry>
</Stmt></BkToCstmrStmt></Document>`
	mt940 := ":20:STMT1\r\n:25:PL61109010140000071219812874\r\n:28C:1/1\r\n:60F:C260831PLN1000,00\r\n:61:2609050905D50,01NTRFNONREF\r\n:86:Zakupy w sklepie\r\n:61:2609180918C1200,00NTRFNONREF\r\n:86:Wplyw od klienta\r\n:62F:C260930PLN2149,99\r\n-"
	for name, c := range map[string]struct {
		data, media, file string
		last4, opening    string
		holder            string
		derived           bool
	}{
		"csv":   {csv, "text/csv", "statement.csv", "", "1000", "", false},
		"ofx":   {ofx, "application/x-ofx", "statement.ofx", "0001", "1000", "", true},
		"camt":  {camt, "application/xml", "statement.xml", "2874", "1000", "Łucja Wójcik", false},
		"mt940": {mt940, "text/plain", "statement.sta", "2874", "1000", "", false},
	} {
		r, err := Export([]byte(c.data), c.media, c.file)
		if err != nil {
			t.Fatal(name, err)
		}
		if r.Signs != BankSigns || r.Last4() != c.last4 || r.Opening.Value != c.opening || r.Closing.Value != "2149.99" || r.Holder.Value != c.holder || r.OpeningDerived != c.derived {
			t.Fatalf("%s: %+v", name, r)
		}
		if r.PeriodStart.Value != "2026-09-01" && name != "csv" || r.PeriodEnd.Value != "2026-09-30" && name != "csv" {
			t.Fatalf("%s period: %+v %+v", name, r.PeriodStart, r.PeriodEnd)
		}
		if len(r.Lines) != 2 || r.Lines[0].Amount != "-50.01" || r.Lines[1].Amount != "1200" || r.Lines[0].Date != "2026-09-05" {
			t.Fatalf("%s lines: %+v", name, r.Lines)
		}
		if err := Verify(r, nil); err != nil {
			t.Fatal(name, err)
		}
	}
	if _, err := Export([]byte("%PDF-1.7"), "application/pdf", "x.pdf"); !errors.Is(err, ErrNotExport) {
		t.Fatal("a PDF isn't an export", err)
	}
}

func TestProposalsAreVerifiedAgainstTheDocument(t *testing.T) {
	pages := textOf(t, [][]string{{"Account number ****0001", "Opening balance 100.00", "05/09/2026 Example -50.00", "Closing balance 50.00"}})
	base := Text(pages)
	// Say the period wasn't printed where Vult looks; a proposal fills it.
	good := Reading{PeriodStart: Field{Value: "2026-09-05"}, PeriodEnd: Field{Value: "2026-09-05"}}
	if err := Verify(Fill(base, good), pages); err != nil {
		t.Fatal("dates in the document pass", err)
	}
	wrongDate := Reading{PeriodStart: Field{Value: "2026-08-01"}, PeriodEnd: Field{Value: "2026-09-05"}}
	if err := Verify(Fill(base, wrongDate), pages); err == nil || !strings.Contains(err.Error(), "2026-08-01") {
		t.Fatal("a date not in the document is refused", err)
	}
	wrongAccount := Fill(Reading{Kind: "statement"}, Reading{Account: Field{Value: "9999"}, Opening: Field{Value: "100"}, Closing: Field{Value: "50"},
		Lines: []Line{{Date: "2026-09-05", Amount: "-50"}}})
	if err := Verify(wrongAccount, pages); err == nil || !strings.Contains(err.Error(), "9999") {
		t.Fatal("account digits not in the document are refused", err)
	}
	wrongSum := Fill(Reading{Kind: "statement"}, Reading{Account: Field{Value: "0001"}, Opening: Field{Value: "100"}, Closing: Field{Value: "50.01"},
		Lines: []Line{{Date: "2026-09-05", Amount: "-50"}}})
	if err := Verify(wrongSum, pages); err == nil || !strings.Contains(err.Error(), "not the closing 50.01") {
		t.Fatal("lines that don't add up are refused", err)
	}
	// Fill never replaces what Vult read itself.
	if got := Fill(base, Reading{Account: Field{Value: "9999"}}); got.Last4() != "0001" {
		t.Fatal("a proposal fills gaps only", got.Account)
	}
}

func TestAProposalWrittenAsPrintedIsTidiedBeforeItIsChecked(t *testing.T) {
	r := Tidy(Reading{
		Opening: Field{Value: "2 149,99"}, Closing: Field{Value: "$410.00"}, Total: Field{Value: "-12.5"},
		PeriodStart: Field{Value: "30.09.2026"}, PeriodEnd: Field{Value: "05/09/2026"}, Date: Field{Value: "2026-09-01"},
		Lines: []Line{{Date: "15 Sept 2026", Amount: "1.000,00"}, {Date: "2026-09-15", Amount: "about forty"}},
	})
	got := []string{r.Opening.Value, r.Closing.Value, r.Total.Value, r.PeriodStart.Value, r.PeriodEnd.Value, r.Date.Value,
		r.Lines[0].Date, r.Lines[0].Amount, r.Lines[1].Amount}
	want := []string{"2149.99", "410", "-12.5", "2026-09-30", "05/09/2026", "2026-09-01", "2026-09-15", "1000", "about forty"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("value %d: %q, want %q", i, got[i], want[i])
		}
	}
	// A date that reads two ways stays as written, for verification to refuse.
	if err := Verify(Fill(Reading{Kind: "statement"}, Reading{PeriodEnd: Field{Value: "05/09/2026"}}), []string{"Period 05/09/2026"}); err == nil {
		t.Error("an ambiguous date is not taken")
	}
}

// A card statement's "previous statement balance" is its opening balance,
// though it contains the closing label "statement balance".
func TestACardStatementsPreviousBalanceOpensIt(t *testing.T) {
	r := Text([]string{"Example Card Services\nCard ending 0002\nBilling cycle 2026-09-03 through 2026-10-02\nPrevious statement balance $410.00\n" +
		"2026-09-10 FIGMA 45.00\n2026-09-21 PAYMENT THANK YOU -410.00\nNew balance due $45.00"})
	if r.Opening.Value != "410" || r.Closing.Value != "45" {
		t.Fatalf("opening %q, closing %q", r.Opening.Value, r.Closing.Value)
	}
}
