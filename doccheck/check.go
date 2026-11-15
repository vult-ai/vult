package doccheck

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/vult-ai/vult/internal/docread"
	"github.com/vult-ai/vult/internal/likeness"
	"github.com/vult-ai/vult/internal/pdftext"
	"github.com/vult-ai/vult/ledger"
)

// Reading is what was read from a document.
type Reading = docread.Reading

// Field is one value read.
type Field = docread.Field

// Line is one transaction line read.
type Line = docread.Line

var fieldLabels = map[string]string{"last4": "the account number", "period_start": "the period's first day", "period_end": "the period's last day",
	"opening_balance": "the opening balance", "closing_balance": "the closing balance"}

func fieldNames(missing []string) string {
	var names []string
	for _, m := range missing {
		names = append(names, fieldLabels[m])
	}
	switch len(names) {
	case 0:
		return "everything"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// Book is how the checker reads a Book and records its readings and checks.
type Book interface {
	Facts(ctx context.Context) (ledger.DocumentFacts, error)
	Write(ctx context.Context, c ledger.DocumentCheck) error
	Numbers(ctx context.Context, q ledger.StatementReading) ([]ledger.DocumentCheck, error)
	Receipt(ctx context.Context, q ledger.RecordReceiptDetails) ([]ledger.DocumentCheck, error)
	Reading(ctx context.Context, document string) (ledger.DocumentReading, bool, error)
	SaveReading(ctx context.Context, r ledger.DocumentReading) error
}

// Document is a file the owner added.
type Document struct {
	ID        string
	Name      string
	MediaType string
	// Group is where the owner filed it; a receipt filed as one is read as one.
	Group string
	Data  []byte
}

// Tools are what the checker may use to read: an assistant, when the
// program has one, and this machine's text recognition for scans. Either
// may be nil.
type Tools struct {
	Assistant Assistant
	OCR       docread.OCR
}

const confirmFix = "Confirm them beside the document, or ask your agent to read it."

// Check reads a document through the reader chain and records its checks.
// It never waits silently: what can't be read goes to the owner to
// confirm.
func Check(ctx context.Context, tools Tools, book Book, doc Document) error {
	c := &checker{ctx: ctx, assistant: tools.Assistant, book: book, doc: doc.ID}
	if err := c.start(ledger.CheckStatementAccount); err != nil {
		return err
	}
	reading, by, _ := Read(ctx, tools, doc)
	if doc.Group == "receipt" {
		// The owner filed it as a receipt: never ask a receipt for a
		// statement's account, period and balances.
		reading.Kind = "receipt"
	}
	if err := c.save(reading, by, ""); err != nil {
		return err
	}
	return c.run(reading)
}

// Read runs the reader chain: a bank's export, the PDF's text, local OCR,
// then the Book's AI for what's still missing, verified against the
// document. It returns the reading, how it was read and the document's
// text, if any.
func Read(ctx context.Context, tools Tools, doc Document) (docread.Reading, string, []string) {
	reading, by, pages := readPlain(ctx, tools, doc)
	if len(reading.Missing()) > 0 && tools.Assistant != nil {
		if proposed, call, err := assistantRead(ctx, tools.Assistant, pages, doc.MediaType, doc.Data); err == nil {
			if proposed.Kind == "receipt" && by == "" {
				tools.Assistant.Mark(ctx, call, true)
				return proposed, ledger.ReadByAI, pages
			}
			merged := docread.Fill(reading, proposed)
			if docread.Verify(merged, pages) == nil && len(merged.Missing()) < len(reading.Missing()) {
				reading = merged
				by = ledger.ReadByAI
				tools.Assistant.Mark(ctx, call, true)
			} else {
				tools.Assistant.Mark(ctx, call, false)
			}
		}
	}
	if by == "" {
		by = ledger.ReadByOCR
		if doc.MediaType == "application/pdf" {
			by = ledger.ReadByPDF
		}
	}
	return reading, by, pages
}

// readPlain reads without AI.
func readPlain(ctx context.Context, tools Tools, doc Document) (docread.Reading, string, []string) {
	if r, err := docread.Export(doc.Data, doc.MediaType, doc.Name); err == nil {
		return r, ledger.ReadByExport, nil
	}
	var pages []string
	if doc.MediaType == "application/pdf" {
		pages, _ = pdftext.Pages(doc.Data)
		if Enough(pages) {
			return docread.Text(pages), ledger.ReadByPDF, pages
		}
	}
	if tools.OCR != nil {
		if scanned, err := tools.OCR.Pages(ctx, doc.MediaType, doc.Data); err == nil && Enough(scanned) {
			return docread.Text(scanned), ledger.ReadByOCR, scanned
		}
	}
	return docread.Reading{Kind: "statement"}, "", pages
}

// Propose fills what Vult couldn't read with a reading proposed by an agent
// (by is ReadByAgent, with the agent's name) or confirmed by the owner (by
// is ReadByOwner), then runs the checks. An agent's proposal is verified
// against the document first; one that fails is refused with the reason.
func Propose(ctx context.Context, tools Tools, book Book, doc Document, proposal docread.Reading, by, name string) error {
	base, _, pages := readPlain(ctx, tools, doc)
	if saved, ok, err := book.Reading(ctx, doc.ID); err != nil {
		return err
	} else if ok {
		var kept docread.Reading
		if json.Unmarshal(saved.Fields, &kept) == nil {
			base = docread.Fill(kept, base)
		}
	}
	missing := base.Missing()
	if len(missing) == 0 && by == ledger.ReadByAgent {
		return &docread.Rejection{Reason: "Vult already read everything it needs from this document."}
	}
	merged := docread.Fill(base, proposal)
	if by == ledger.ReadByAgent {
		if len(merged.Missing()) == len(missing) {
			return &docread.Rejection{Reason: "The proposal doesn't give any of the missing fields: " + strings.Join(missing, ", ") + "."}
		}
		if err := docread.Verify(merged, pages); err != nil {
			return err
		}
	}
	c := &checker{ctx: ctx, assistant: tools.Assistant, book: book, doc: doc.ID}
	if err := c.start(ledger.CheckStatementAccount); err != nil {
		return err
	}
	if err := c.save(merged, by, name); err != nil {
		return err
	}
	return c.run(merged)
}

func (c *checker) save(r docread.Reading, by, name string) error {
	fields, err := json.Marshal(r)
	if err != nil {
		return err
	}
	c.missing = r.Missing()
	return c.book.SaveReading(c.ctx, ledger.DocumentReading{ReadingSummary: ledger.ReadingSummary{DocumentID: c.doc, ReadBy: by, Reader: name,
		Missing: c.missing}, Fields: fields})
}

// run records the checks on a reading.
func (c *checker) run(reading docread.Reading) error {
	switch reading.Kind {
	case "receipt":
		c.end(ledger.DocumentCheck{Check: ledger.CheckStatementAccount, Result: ledger.ResultOK, Against: "a receipt, not a statement"})
		return c.receipt(reading)
	case "statement", "":
	default:
		c.end(ledger.DocumentCheck{Check: ledger.CheckDone, Result: ledger.ResultFailed,
			Reason: "This doesn't look like a bank or card statement, or a receipt.",
			Fix:    "If it's a company or tax record, file it there; otherwise add the statement itself."})
		return c.err
	}
	facts, err := c.book.Facts(c.ctx)
	if err != nil {
		return err
	}
	account := c.account(reading, facts)
	c.holder(reading, facts)
	c.address(reading, facts)
	c.numbers(reading, facts, account)
	return c.finish()
}

type checker struct {
	ctx       context.Context
	assistant Assistant
	book      Book
	doc       string
	all       []ledger.DocumentCheck
	missing   []string
	err       error
}

func (c *checker) start(check string) error {
	if c.err == nil {
		c.err = c.book.Write(c.ctx, ledger.DocumentCheck{DocumentID: c.doc, Check: check, Result: ledger.ResultRunning})
	}
	return c.err
}

func (c *checker) end(d ledger.DocumentCheck) {
	d.DocumentID = c.doc
	c.all = append(c.all, d)
	if c.err == nil {
		c.err = c.book.Write(c.ctx, d)
	}
}

func (c *checker) finish() error {
	if c.err != nil {
		return c.err
	}
	done := ledger.Done(c.doc, c.all)
	// What couldn't be read goes to the owner, beside the document.
	if len(c.missing) > 0 && done.Result != ledger.ResultFailed {
		done.Result = ledger.ResultReview
		done.Reason = "Vult couldn't read " + fieldNames(c.missing) + " from this document."
		done.Fix, done.Action = confirmFix, ledger.ActionConfirmReading
	}
	c.end(done)
	return c.err
}

func shown(f Field) string { return strings.TrimSpace(f.Value) }

// account finds the Book account the statement is for.
func (c *checker) account(r Reading, f ledger.DocumentFacts) *ledger.StatementAccountFact {
	last4 := r.Last4()
	d := ledger.DocumentCheck{Check: ledger.CheckStatementAccount}
	if last4 != "" {
		d.Found = "••" + last4
	}
	if inst := shown(r.Institution); inst != "" {
		d.Found = strings.TrimPrefix(inst+" "+d.Found, " ")
	}
	var match, only *ledger.StatementAccountFact
	masked := 0
	for i := range f.Accounts {
		a := &f.Accounts[i]
		if a.Mask != "" {
			masked++
		}
		if last4 != "" && a.Mask == last4 && match == nil {
			match = a
		}
	}
	if len(f.Accounts) == 1 {
		only = &f.Accounts[0]
	}
	label := func(a *ledger.StatementAccountFact) string {
		s := a.Name
		if a.Institution != "" && !strings.Contains(likeness.Fold(s), likeness.Fold(a.Institution)) {
			s = a.Institution + " " + s
		}
		if a.Mask != "" {
			s += " ••" + a.Mask
		}
		return strings.TrimSpace(s)
	}
	switch {
	case match != nil:
		d.Against, d.Result = label(match), ledger.ResultOK
		if inst := shown(r.Institution); inst != "" && match.Institution != "" && likeness.Names(inst, match.Institution).Verdict == likeness.Different {
			d.Result = ledger.ResultNote
			d.Reason = "The statement is from " + inst + "; the account is connected through " + match.Institution + "."
		}
	case last4 == "":
		d.Result = ledger.ResultReview
		d.Reason = "The account number wasn't on the statement, so Vult can't tell which account it's for."
		d.Fix = "Choose the account when you import it."
		if only != nil {
			match = only
			d.Against = label(only)
			d.Result = ledger.ResultNote
			d.Reason = "The statement shows no account number; Vult took it for " + label(only) + ", the Book's only bank or card account."
			d.Fix = ""
		}
	case masked > 0:
		d.Result = ledger.ResultFailed
		d.Reason = "The statement is for an account ending " + last4 + "; no account in the Book ends in " + last4 + "."
		d.Fix = "Check it's for this Book, or connect the bank that holds the account."
	case only != nil:
		match = only
		d.Against = label(only)
		d.Result = ledger.ResultNote
		d.Reason = "No account number is on file, so Vult took it for " + label(only) + ", the Book's only bank or card account."
		d.Fix = "Connect its bank to compare account numbers."
	default:
		d.Result = ledger.ResultReview
		d.Reason = "No account in the Book has an account number on file to compare with ••" + last4 + "."
		d.Fix = "Connect the bank, or choose the account when you import it."
	}
	c.end(d)
	return match
}

// compare runs plain rules, then asks the assistant only when they can't tell.
func (c *checker) compare(check, kind, found string, expected []string, against func(likeness.Result, string) ledger.DocumentCheck) {
	if c.start(check) != nil {
		return
	}
	best, bestOn := likeness.Result{Verdict: likeness.Different}, ""
	for _, e := range expected {
		if strings.TrimSpace(e) == "" {
			continue
		}
		var r likeness.Result
		if kind == "name" {
			r = likeness.Names(found, e)
		} else {
			r = likeness.Addresses(found, e)
		}
		if bestOn == "" || r.Verdict < best.Verdict {
			best, bestOn = r, e
		}
	}
	d := against(best, bestOn)
	if best.Verdict == likeness.Unsure && bestOn != "" {
		same, reason, err := c.judge(kind, found, bestOn)
		switch {
		case err != nil || reason == "":
			d.Result, d.Reason = ledger.ResultReview, "The statement shows "+found+"; the Book has "+bestOn+". Vult can't tell whether they're the same."
		case same:
			d.Result, d.Reason = ledger.ResultNote, reason
		default:
			d.Result, d.Reason = ledger.ResultFailed, reason
		}
	}
	c.end(d)
}

func (c *checker) holder(r Reading, f ledger.DocumentFacts) {
	found := shown(r.Holder)
	expected := append([]string{f.LegalName, f.BookName}, f.Holders...)
	if found == "" {
		// Exports seldom name the holder; the account number is what ties
		// a statement to the Book.
		c.start(ledger.CheckStatementHolder)
		c.end(ledger.DocumentCheck{Check: ledger.CheckStatementHolder, Result: ledger.ResultNote,
			Reason: "The statement doesn't name the account holder, so Vult went by the account number."})
		return
	}
	c.compare(ledger.CheckStatementHolder, "name", found, expected, func(v likeness.Result, on string) ledger.DocumentCheck {
		d := ledger.DocumentCheck{Check: ledger.CheckStatementHolder, Found: found, Against: on}
		switch v.Verdict {
		case likeness.Same:
			d.Result = ledger.ResultOK
		case likeness.Extra:
			d.Result, d.Reason = ledger.ResultNote, "The statement shows "+found+"; the Book has "+on+". "+v.Note
		case likeness.Different:
			d.Result = ledger.ResultFailed
			d.Reason = "The statement is in the name of " + found + "; the Book is for " + on + "."
			d.Fix = "Check it belongs to this Book. If it's the owner's own card used for the business, its charges are recorded as paid by the owner."
		}
		return d
	})
}

func (c *checker) address(r Reading, f ledger.DocumentFacts) {
	found := shown(r.Address)
	switch {
	case found == "":
		c.start(ledger.CheckStatementAddress)
		c.end(ledger.DocumentCheck{Check: ledger.CheckStatementAddress, Result: ledger.ResultNote, Reason: "The statement shows no address."})
		return
	case len(f.Addresses) == 0:
		c.start(ledger.CheckStatementAddress)
		c.end(ledger.DocumentCheck{Check: ledger.CheckStatementAddress, Result: ledger.ResultOK, Found: found,
			Against: "the first address on file; later statements are compared with it"})
		return
	}
	c.compare(ledger.CheckStatementAddress, "address", found, f.Addresses, func(v likeness.Result, on string) ledger.DocumentCheck {
		d := ledger.DocumentCheck{Check: ledger.CheckStatementAddress, Found: found, Against: on}
		switch v.Verdict {
		case likeness.Same:
			d.Result = ledger.ResultOK
		case likeness.Extra:
			d.Result, d.Reason = ledger.ResultNote, v.Note
		case likeness.Different:
			d.Result = ledger.ResultFailed
			d.Reason = "The statement shows " + found + "; the Book's earlier statements show " + on + "."
			d.Fix = "If the address changed, nothing needs doing; otherwise check the statement is for this Book."
		}
		return d
	})
}

// numbers runs the plain-code checks on what was read.
func (c *checker) numbers(r Reading, f ledger.DocumentFacts, account *ledger.StatementAccountFact) {
	review := func(reason, fix string) {
		for _, check := range []string{ledger.CheckStatementPeriod, ledger.CheckStatementBalances, ledger.CheckDuplicate} {
			c.start(check)
			c.end(ledger.DocumentCheck{Check: check, Result: ledger.ResultReview, Reason: reason, Fix: fix})
		}
	}
	if account == nil {
		review("Vult can't tell which account this statement is for, so it can't compare it with the account's other statements.", "Choose the account when you import it.")
		return
	}
	opening, ok1 := Minor(shown(r.Opening), f.Scale)
	closing, ok2 := Minor(shown(r.Closing), f.Scale)
	start, end := shown(r.PeriodStart), shown(r.PeriodEnd)
	if !ok1 || !ok2 || !ledger.ValidDate(start) || !ledger.ValidDate(end) || start > end {
		review("Vult couldn't read the statement's period and balances.", confirmFix)
		return
	}
	// A bank's export signs money out negative; on a card that's a charge,
	// which a card's statement signs positive, like the balance owed.
	flip := int64(1)
	if r.Signs == docread.BankSigns && account.Kind == "card" {
		flip = -1
	}
	q := ledger.StatementReading{DocumentID: c.doc, AccountID: account.AccountID, PeriodStart: start, PeriodEnd: end,
		OpeningMinor: flip * opening, ClosingMinor: flip * closing, OpeningDerived: r.OpeningDerived}
	for _, l := range r.Lines {
		n, ok := Minor(l.Amount, f.Scale)
		if !ok || !ledger.ValidDate(l.Date) {
			review("Vult couldn't read every line of the statement.", confirmFix)
			return
		}
		q.Lines = append(q.Lines, ledger.StatementLine{Date: l.Date, AmountMinor: flip * n})
	}
	for _, check := range []string{ledger.CheckStatementPeriod, ledger.CheckStatementBalances, ledger.CheckDuplicate} {
		if c.start(check) != nil {
			return
		}
	}
	checks, err := c.book.Numbers(c.ctx, q)
	if err != nil {
		review("Vult couldn't compare the statement with the Book just now.", "Add the file again in a moment.")
		return
	}
	// The ledger recorded them; keep them for the summary.
	c.all = append(c.all, checks...)
}

func (c *checker) receipt(r Reading) error {
	if c.err != nil {
		return c.err
	}
	q := ledger.RecordReceiptDetails{DocumentID: c.doc, Date: shown(r.Date), Vendor: shown(r.Vendor)}
	if !ledger.ValidDate(q.Date) {
		q.Date = ""
	}
	if n, ok := Minor(shown(r.Total), 2); ok && shown(r.Total) != "" {
		if n < 0 {
			n = -n
		}
		q.AmountMinor = strconv.FormatInt(n, 10)
	}
	c.start(ledger.CheckReceiptDetails)
	_, err := c.book.Receipt(c.ctx, q)
	return err
}

// Minor turns a decimal as read into minor units at this scale: 1,234.56,
// -50.00, (50.00) and 50.00- are all read. More decimals than the scale are
// refused rather than rounded.
func Minor(text string, scale int) (int64, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return 0, false
	}
	negative := false
	if strings.HasPrefix(t, "(") && strings.HasSuffix(t, ")") {
		negative, t = true, t[1:len(t)-1]
	}
	if strings.HasSuffix(t, "-") {
		negative, t = true, strings.TrimSuffix(t, "-")
	}
	if strings.HasPrefix(t, "-") {
		negative, t = !negative, t[1:]
	}
	t = strings.TrimPrefix(t, "+")
	t = strings.ReplaceAll(strings.ReplaceAll(t, ",", ""), " ", "")
	whole, frac, _ := strings.Cut(t, ".")
	if whole == "" {
		whole = "0"
	}
	if len(frac) > scale {
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

// judge asks the assistant one narrow question: are these the same?
func (c *checker) judge(kind, found, expected string) (bool, string, error) {
	if c.assistant == nil {
		return false, "", ErrNotReadable
	}
	same, reason, err := c.assistant.Same(c.ctx, kind, found, expected)
	if err != nil {
		return false, "", err
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 280 {
		reason = reason[:280]
	}
	return same, reason, nil
}
