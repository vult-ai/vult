package statementfile

import (
	"errors"
	"strings"
	"testing"
)

func TestReadsCommonBankExports(t *testing.T) {
	// Newest first, signed amounts, a running balance.
	s, err := ReadCSV([]byte("\xef\xbb\xbfDate,Description,Amount,Balance\n"+
		"01/05/2026,EXAMPLE CLIENT CO ACH,\"$1,200.00\",\"1,450.00\"\n"+
		"01/03/2026,EXAMPLE CAFE 0042,-18.50,250.00\n"+
		"01/02/2026,Opening deposit,268.50,268.50\n"), "cash", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Lines) != 3 || s.PeriodStart != "2026-01-02" || s.PeriodEnd != "2026-01-05" || s.Lines[1].AmountMinor != -1850 || s.Lines[2].AmountMinor != 120000 {
		t.Fatalf("statement = %+v", s)
	}
	if !s.Balances || s.Opening != 0 || s.Closing != 145000 {
		t.Fatalf("balances = %v %d %d", s.Balances, s.Opening, s.Closing)
	}
	// Debit and credit columns, day-first dates, no balance.
	s, err = ReadCSV([]byte("Datum;Description;Amount\nDate;Text;Amount\n31.01.2026;Example Shop;-12,50\n"), "cash", 2)
	if err != nil || len(s.Lines) != 1 || s.Lines[0].Date != "2026-01-31" || s.Lines[0].AmountMinor != -1250 {
		t.Fatalf("semicolons = %+v, %v", s, err)
	}
	s, err = ReadCSV([]byte("Posted Date,Payee,Debit,Credit\n13/01/2026,Example Rent,950.00,\n14/01/2026,Example Refund,,(12.00)\n"), "cash", 2)
	if err != nil || len(s.Lines) != 2 || s.Lines[0].Date != "2026-01-13" || s.Lines[0].AmountMinor != -95000 || s.Lines[1].AmountMinor != 1200 || s.Balances {
		t.Fatalf("statement = %+v, %v", s, err)
	}
	// On a card, a purchase raises the balance owed.
	s, err = ReadCSV([]byte("Date,Description,Amount\n2026-02-01,EXAMPLE HOSTING,-129.99\n2026-02-03,PAYMENT THANK YOU,600.00\n"), "card", 2)
	if err != nil || s.Lines[0].AmountMinor != 12999 || s.Lines[1].AmountMinor != -60000 {
		t.Fatalf("card = %+v, %v", s, err)
	}
	// A balance that doesn't agree with the lines isn't used.
	s, err = ReadCSV([]byte("Date,Description,Amount,Balance\n2026-02-01,A,-1.00,10.00\n2026-02-02,B,-1.00,5.00\n"), "cash", 2)
	if err != nil || s.Balances {
		t.Fatalf("inconsistent balance used: %+v, %v", s, err)
	}
}

func TestExplainsFilesItCantRead(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"Name,Value\nA,1\n", "header row with a date"},
		{"Date,Description,Amount\nsoon,A,1.00\n", "isn't a date format"},
		{"Date,Description,Amount\n2026-01-01,A,1.005\n", "more decimal places"},
		{"Date,Description,Amount\n2026-01-01,A,lots\n", "isn't an amount"},
		{"Date,Description,Amount\n", "no transactions"},
	} {
		_, err := ReadCSV([]byte(tc.file), "cash", 2)
		var p *Problem
		if !errors.As(err, &p) || !strings.Contains(p.Reason, tc.want) {
			t.Fatalf("%q: %v", tc.file, err)
		}
	}
}

func TestAmountsAsBanksWriteThem(t *testing.T) {
	for in, want := range map[string]int64{"1,234.56": 123456, "1.234,56": 123456, "(5.00)": -500, "5.00-": -500, "12.00 DR": -1200, "12.00 CR": 1200, "€7": 700, "-0.5": -50} {
		if got, err := minor(in, 2); err != nil || got != want {
			t.Fatalf("%q = %d, %v", in, got, err)
		}
	}
}

// A bank's opening-balance row, with a balance and no amount, is the
// opening balance, not a failure of the file.
func TestReadsAnOpeningBalanceRow(t *testing.T) {
	s, err := ReadCSV([]byte("Date,Description,Amount,Balance\n2026-10-01,Opening balance,,4200.00\n2026-10-02,Example Cafe,-18.50,4181.50\n2026-10-05,Example Client,1200.00,5381.50\n"), "cash", 2)
	if err != nil || len(s.Lines) != 2 || !s.Balances || s.Opening != 420000 || s.Closing != 538150 {
		t.Fatalf("statement = %+v, %v", s, err)
	}
	// Without a running balance on the lines, the closing follows from them.
	s, err = ReadCSV([]byte("Date,Description,Amount,Balance\n2026-10-01,Opening balance,,4200.00\n2026-10-02,Example Cafe,-18.50,\n"), "cash", 2)
	if err != nil || len(s.Lines) != 1 || !s.Balances || s.Opening != 420000 || s.Closing != 418150 {
		t.Fatalf("statement = %+v, %v", s, err)
	}
}
