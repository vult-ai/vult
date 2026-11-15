package doccheck

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/docread"
	"github.com/vult-ai/vult/internal/pdftext/pdftest"
	"github.com/vult-ai/vult/ledger"
)

// book is a Book in memory: what the checker reads and what it records.
type book struct {
	facts    ledger.DocumentFacts
	written  []ledger.DocumentCheck
	readings []ledger.StatementReading
	receipts []ledger.RecordReceiptDetails
	saved    []ledger.DocumentReading
}

func (b *book) Facts(context.Context) (ledger.DocumentFacts, error) { return b.facts, nil }
func (b *book) Write(_ context.Context, c ledger.DocumentCheck) error {
	b.written = append(b.written, c)
	return nil
}
func (b *book) Numbers(_ context.Context, q ledger.StatementReading) ([]ledger.DocumentCheck, error) {
	b.readings = append(b.readings, q)
	var out []ledger.DocumentCheck
	for _, check := range []string{ledger.CheckStatementPeriod, ledger.CheckStatementBalances, ledger.CheckDuplicate} {
		c := ledger.DocumentCheck{DocumentID: q.DocumentID, Check: check, Result: ledger.ResultOK}
		b.written = append(b.written, c)
		out = append(out, c)
	}
	return out, nil
}
func (b *book) Receipt(_ context.Context, q ledger.RecordReceiptDetails) ([]ledger.DocumentCheck, error) {
	b.receipts = append(b.receipts, q)
	return nil, nil
}
func (b *book) Reading(_ context.Context, document string) (ledger.DocumentReading, bool, error) {
	for i := len(b.saved) - 1; i >= 0; i-- {
		if b.saved[i].DocumentID == document {
			return b.saved[i], true, nil
		}
	}
	return ledger.DocumentReading{}, false, nil
}
func (b *book) SaveReading(_ context.Context, r ledger.DocumentReading) error {
	b.saved = append(b.saved, r)
	return nil
}

// latest is each check's last result.
func (b *book) latest() map[string]ledger.DocumentCheck {
	out := map[string]ledger.DocumentCheck{}
	for _, c := range b.written {
		out[c.Check] = c
	}
	return out
}

func (b *book) reading() ledger.DocumentReading { return b.saved[len(b.saved)-1] }

// model answers a reading with a fixed reading and the narrow question
// with a fixed verdict, and remembers what it was shown.
type model struct {
	reading Reading
	same    bool
	reason  string
	reads   []handed
	judged  []string
}

// handed is one document as the assistant was given it.
type handed struct {
	Pages []string
	Files int
}

func (m *model) Read(_ context.Context, pages []string, _ string, data []byte) (Reading, string, error) {
	s := handed{Pages: pages}
	if data != nil {
		s.Files = 1
	}
	m.reads = append(m.reads, s)
	return m.reading, "", nil
}

func (m *model) Same(_ context.Context, kind, found, expected string) (bool, string, error) {
	m.judged = append(m.judged, "Kind: "+kind+"\nOn the statement: "+found+"\nIn the Book: "+expected)
	return m.same, m.reason, nil
}

func (m *model) Mark(context.Context, string, bool) {}

// ocr reads a scan as these pages.
type ocr []string

func (o ocr) Pages(context.Context, string, []byte) ([]string, error) { return o, nil }

func eightball() *book {
	return &book{facts: ledger.DocumentFacts{BookName: "Eightball", LegalName: "Łucja Wójcik", Currency: "USD", Scale: 2,
		Accounts:  []ledger.StatementAccountFact{{AccountID: ledger.ID(), Name: "Checking", Kind: "cash", Institution: "Example Bank", Mask: "0001"}},
		Addresses: []string{"Rynek 50, 31-001 Kraków"}}}
}

func statementText(holder, address, account string) []string {
	return []string{"Example Bank", "Account holder: " + holder, address, "Account number: " + account,
		"Statement period: 01.09.2026 - 30.09.2026", "Opening balance 1,000.00",
		"05.09.2026 Example supplies -50.01 949.99", "Closing balance 949.99"}
}

func pdf(lines []string) Document {
	return Document{ID: ledger.ID(), Name: "statement.pdf", MediaType: "application/pdf", Data: pdftest.PDF([][]string{lines}, pdftest.Options{})}
}

func TestNoAIStillReadsAndChecks(t *testing.T) {
	b := eightball()
	if err := Check(context.Background(), Tools{}, b, pdf(statementText("Łucja Wójcik", "Rynek 50, 31-001 Kraków", "****0001"))); err != nil {
		t.Fatal(err)
	}
	if r := b.reading(); r.ReadBy != ledger.ReadByPDF || len(r.Missing) != 0 {
		t.Fatal("read from the PDF, nothing missing", r.ReadingSummary)
	}
	for check, c := range b.latest() {
		if c.Result != ledger.ResultOK {
			t.Fatal("everything passes without AI", check, c)
		}
	}
	r := b.readings[0]
	if r.OpeningMinor != 100000 || r.ClosingMinor != 94999 || len(r.Lines) != 1 || r.Lines[0].AmountMinor != -5001 {
		t.Fatal("amounts read to the cent", r)
	}
	// Every check starts before it ends, and done is last.
	started := map[string]bool{}
	for _, c := range b.written {
		if c.Result == ledger.ResultRunning {
			started[c.Check] = true
			continue
		}
		if c.Check != ledger.CheckDone && !started[c.Check] {
			t.Fatal("a check ended before it started", c.Check)
		}
	}
	if last := b.written[len(b.written)-1]; last.Check != ledger.CheckDone {
		t.Fatal("done comes last", last)
	}
}

func TestStatementChecksCompareLikeAPerson(t *testing.T) {
	b := eightball()
	if err := Check(context.Background(), Tools{}, b, pdf(statementText("WÓJCIK ŁUCJA", "RYNEK 50/7, 31-001 KRAKÓW", "****0001"))); err != nil {
		t.Fatal(err)
	}
	got := b.latest()
	if c := got[ledger.CheckStatementAccount]; c.Result != ledger.ResultOK || c.Against != "Example Bank Checking ••0001" {
		t.Fatal("the account matches by its last four digits", c)
	}
	if c := got[ledger.CheckStatementHolder]; c.Result != ledger.ResultOK || c.Found != "WÓJCIK ŁUCJA" || c.Against != "Łucja Wójcik" {
		t.Fatal("a swapped name order in capitals passes, accents kept", c)
	}
	if c := got[ledger.CheckStatementAddress]; c.Result != ledger.ResultNote || c.Reason != "The statement shows RYNEK 50/7, 31-001 KRAKÓW; the Book expects no flat or unit number." {
		t.Fatal("a flat number on one side is a note", c)
	}

	// An unsure name asks the AI one narrow question; with no AI it goes to
	// a person.
	m := &model{same: true, reason: "The statement shows Lucia Woycik; the Book has Łucja Wójcik, the same name transliterated."}
	b = eightball()
	if err := Check(context.Background(), Tools{Assistant: m}, b, pdf(statementText("Lucia Woycik", "Rynek 50, 31-001 Kraków", "****0001"))); err != nil {
		t.Fatal(err)
	}
	if len(m.reads) != 0 || len(m.judged) != 1 {
		t.Fatal("the PDF was read without AI; only the name was asked", m.reads, m.judged)
	}
	if c := b.latest()[ledger.CheckStatementHolder]; c.Result != ledger.ResultNote || c.Reason != m.reason {
		t.Fatal("the AI's reason is shown", c)
	}
	b = eightball()
	if err := Check(context.Background(), Tools{}, b, pdf(statementText("Lucia Woycik", "Rynek 50, 31-001 Kraków", "****0001"))); err != nil {
		t.Fatal(err)
	}
	if c := b.latest()[ledger.CheckStatementHolder]; c.Result != ledger.ResultReview {
		t.Fatal("unsure, with no AI, goes to a person", c)
	}

	// Another person and another account fail, with fixes.
	b = eightball()
	if err := Check(context.Background(), Tools{}, b, pdf(statementText("Kit Blanche", "9 Rue Lepic, Paris", "****9999"))); err != nil {
		t.Fatal(err)
	}
	got = b.latest()
	if c := got[ledger.CheckStatementHolder]; c.Result != ledger.ResultFailed || c.Fix == "" {
		t.Fatal("a different name fails with a fix", c)
	}
	if c := got[ledger.CheckStatementAccount]; c.Result != ledger.ResultFailed || !strings.Contains(c.Reason, "ending 9999") {
		t.Fatal("no account in the Book ends in those digits", c)
	}
}

func TestAScanIsReadByLocalOCR(t *testing.T) {
	b := eightball()
	scan := Document{ID: ledger.ID(), MediaType: "image/jpeg", Data: []byte("\xff\xd8\xffexample")}
	if err := Check(context.Background(), Tools{OCR: ocr{strings.Join(statementText("Łucja Wójcik", "Rynek 50, 31-001 Kraków", "0001"), "\n")}}, b, scan); err != nil {
		t.Fatal(err)
	}
	if r := b.reading(); r.ReadBy != ledger.ReadByOCR || len(r.Missing) != 0 {
		t.Fatal("read from the scan", r.ReadingSummary)
	}
	if c := b.latest()[ledger.CheckDone]; c.Result != ledger.ResultOK {
		t.Fatal("the scan checks out", c)
	}
}

func TestAnExportIsReadAsItIs(t *testing.T) {
	b := eightball()
	b.facts.Accounts[0].Kind = "card"
	ofx := `OFXHEADER:100
<OFX><CREDITCARDMSGSRSV1><CCSTMTTRNRS><CCSTMTRS><CCACCTFROM><ACCTID>4111111111110001</CCACCTFROM>
<BANKTRANLIST><DTSTART>20260901<DTEND>20260930
<STMTTRN><DTPOSTED>20260905<TRNAMT>-50.01<NAME>Example supplies</STMTTRN>
</BANKTRANLIST><LEDGERBAL><BALAMT>-250.01<DTASOF>20260930</LEDGERBAL></CCSTMTRS></CCSTMTTRNRS></CREDITCARDMSGSRSV1></OFX>`
	if err := Check(context.Background(), Tools{}, b, Document{ID: ledger.ID(), Name: "card.qfx", MediaType: "application/vnd.intu.qfx", Data: []byte(ofx)}); err != nil {
		t.Fatal(err)
	}
	if r := b.reading(); r.ReadBy != ledger.ReadByExport {
		t.Fatal("read from the bank's export", r.ReadingSummary)
	}
	// A card's charges and balance owed are positive on its statement.
	q := b.readings[0]
	if q.ClosingMinor != 25001 || q.OpeningMinor != 20000 || q.Lines[0].AmountMinor != 5001 || !q.OpeningDerived {
		t.Fatal("a card export is signed like a card statement", q)
	}
	if c := b.latest()[ledger.CheckStatementHolder]; c.Result != ledger.ResultNote {
		t.Fatal("an export without a holder is a note, not a fault", c)
	}
}

func TestGapsAreProposedVerifiedOrConfirmed(t *testing.T) {
	// A statement that prints no period where Vult looks for it.
	lines := []string{"Example Bank", "Account holder: Łucja Wójcik", "Account number: ****0001", "Opening balance 1,000.00",
		"05.09.2026 Example supplies -50.01 949.99", "30.09.2026 Example fee -1.00 948.99", "Closing balance 948.99"}
	doc := pdf(lines)
	b := eightball()
	if err := Check(context.Background(), Tools{}, b, doc); err != nil {
		t.Fatal(err)
	}
	if r := b.reading(); strings.Join(r.Missing, ",") != "period_start,period_end" {
		t.Fatal("the period is missing", r.Missing)
	}
	if c := b.latest()[ledger.CheckDone]; c.Result != ledger.ResultReview || c.Action != ledger.ActionConfirmReading ||
		c.Reason != "Vult couldn't read the period's first day and the period's last day from this document." {
		t.Fatal("the owner is asked to confirm the missing fields", c)
	}

	// An agent's proposal with a date that isn't in the document is refused.
	bad := Reading{PeriodStart: Field{Value: "2026-08-01"}, PeriodEnd: Field{Value: "2026-09-30"}}
	var rejected *docread.Rejection
	if err := Propose(context.Background(), Tools{}, b, doc, bad, ledger.ReadByAgent, "Example agent"); !errors.As(err, &rejected) || !strings.Contains(rejected.Reason, "2026-08-01") {
		t.Fatal("a proposal that fails verification is refused with the reason", err)
	}
	// One whose dates are in it is taken, and the checks run.
	good := Reading{PeriodStart: Field{Value: "2026-09-05"}, PeriodEnd: Field{Value: "2026-09-30"}, Account: Field{Value: "9999"}}
	if err := Propose(context.Background(), Tools{}, b, doc, good, ledger.ReadByAgent, "Example agent"); err != nil {
		t.Fatal(err)
	}
	if r := b.reading(); r.ReadBy != ledger.ReadByAgent || r.Reader != "Example agent" || len(r.Missing) != 0 {
		t.Fatal("read by the agent, verified by Vult", r.ReadingSummary)
	}
	var kept Reading
	_ = json.Unmarshal(b.reading().Fields, &kept)
	if kept.Last4() != "0001" {
		t.Fatal("a proposal fills gaps only, never what Vult read", kept.Account)
	}
	if c := b.latest()[ledger.CheckDone]; c.Result != ledger.ResultOK {
		t.Fatal("the checks pass once the reading is complete", c)
	}

	// The owner confirms instead.
	b = eightball()
	_ = Check(context.Background(), Tools{}, b, doc)
	if err := Propose(context.Background(), Tools{}, b, doc, Reading{PeriodStart: Field{Value: "2026-09-01"}, PeriodEnd: Field{Value: "2026-09-30"}}, ledger.ReadByOwner, ""); err != nil {
		t.Fatal(err)
	}
	if r := b.reading(); r.ReadBy != ledger.ReadByOwner || len(r.Missing) != 0 {
		t.Fatal("confirmed by the owner", r.ReadingSummary)
	}

	// The Book's AI fills gaps too, verified the same way.
	m := &model{reading: Reading{Kind: "statement", PeriodStart: Field{Value: "2026-09-05", Page: 1}, PeriodEnd: Field{Value: "2026-09-30", Page: 1}}}
	b = eightball()
	if err := Check(context.Background(), Tools{Assistant: m}, b, doc); err != nil {
		t.Fatal(err)
	}
	if r := b.reading(); r.ReadBy != ledger.ReadByAI || len(r.Missing) != 0 || len(m.reads) != 1 || m.reads[0].Files != 0 {
		t.Fatal("Vult's AI read the gaps from the text", r.ReadingSummary)
	}
	m = &model{reading: Reading{Kind: "statement", PeriodStart: Field{Value: "2025-01-01"}, PeriodEnd: Field{Value: "2026-09-30"}}}
	b = eightball()
	_ = Check(context.Background(), Tools{Assistant: m}, b, doc)
	if r := b.reading(); r.ReadBy != ledger.ReadByPDF || len(r.Missing) != 2 {
		t.Fatal("an AI reading that doesn't check out isn't used", r.ReadingSummary)
	}
}

func TestMinorReadsAmountsToTheCent(t *testing.T) {
	for text, want := range map[string]int64{"1,234.56": 123456, "-50.00": -5000, "(50.00)": -5000, "50.00-": -5000, "0.01": 1, "12": 1200, "-.5": -50} {
		if n, ok := Minor(text, 2); !ok || n != want {
			t.Errorf("Minor(%q) = %d, %v; want %d", text, n, ok, want)
		}
	}
	for _, bad := range []string{"", "1.234", "abc", "1.2.3"} {
		if _, ok := Minor(bad, 2); ok {
			t.Errorf("Minor(%q) should refuse", bad)
		}
	}
}

func TestAReceiptFiledAsOneGetsNoStatementChecks(t *testing.T) {
	b := eightball()
	doc := pdf([]string{"Example Cloud Services", "Invoice", "Account number: 5555", "Billing period: 01.10.2026 - 31.10.2026",
		"Invoice date: 02.11.2026", "Compute 120.00", "Total 120.00"})
	doc.Group = "receipt"
	if err := Check(context.Background(), Tools{}, b, doc); err != nil {
		t.Fatal(err)
	}
	if len(b.readings) != 0 {
		t.Fatal("a receipt was checked as a statement", b.readings)
	}
	if len(b.receipts) != 1 {
		t.Fatal("a receipt's details are recorded", b.receipts)
	}
	if r := b.reading(); len(r.Missing) != 0 {
		t.Fatal("a receipt is never asked for a statement's fields", r.Missing)
	}
	if c := b.latest()[ledger.CheckStatementAccount]; c.Against != "a receipt, not a statement" {
		t.Fatal("the account check says it's a receipt", c)
	}
}
