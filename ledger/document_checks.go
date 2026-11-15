package ledger

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Document checks are what Vult compared a statement or receipt with, one
// row per check, as it happens. Only Vult writes them: its document checker
// and its own reading of statement files. Outside agents read them and can
// never write or pass one.

const (
	CheckStatementAccount  = "statement_account"
	CheckStatementHolder   = "statement_holder"
	CheckStatementAddress  = "statement_address"
	CheckStatementPeriod   = "statement_period"
	CheckStatementBalances = "statement_balances"
	CheckDuplicate         = "duplicate"
	CheckReceiptDetails    = "receipt_details"
	CheckReceiptMatched    = "receipt_matched"
	CheckDone              = "done"
)

// How a check ended, or that it has started.
const (
	ResultRunning = "running"
	ResultOK      = "ok"
	ResultNote    = "note"   // passed, with a note: a harmless difference
	ResultReview  = "review" // a person needs to look
	ResultFailed  = "failed"
)

// DocumentCheck is one check on one document. Against is the value it was
// compared with, ready to show; Found is what the document says. Reason says
// what was found against what was expected whenever the result is note,
// review or failed; Fix says what to do, when there's a fix. OK is true for
// ok and note.
type DocumentCheck struct {
	Seq        int64  `json:"seq"`
	DocumentID string `json:"document_id"`
	Check      string `json:"check"`
	Result     string `json:"result"`
	OK         bool   `json:"ok"`
	Against    string `json:"against,omitempty"`
	Found      string `json:"found,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Fix        string `json:"fix,omitempty"`
	// Action is a button the owner's screen offers with the fix.
	Action string    `json:"action,omitempty"`
	At     time.Time `json:"at"`
}

var checkNames = map[string]bool{CheckStatementAccount: true, CheckStatementHolder: true, CheckStatementAddress: true, CheckStatementPeriod: true, CheckStatementBalances: true,
	CheckDuplicate: true, CheckReceiptDetails: true, CheckReceiptMatched: true, CheckDone: true}
var checkResults = map[string]bool{ResultRunning: true, ResultOK: true, ResultNote: true, ResultReview: true, ResultFailed: true}

type DocumentCheckQuery struct {
	DocumentID string `json:"document_id,omitempty"`
	After      int64  `json:"after,omitempty"`
}

// DocumentCheckPage holds the latest check of each kind per document, after
// a sequence number, in order. Next is where to read from next time.
type DocumentCheckPage struct {
	Checks []DocumentCheck `json:"checks"`
	// Readings says how each document with checks was read, and what's
	// still missing.
	Readings []ReadingSummary `json:"readings"`
	Next     int64            `json:"next"`
}

// RecordReceiptDetails is what an agent read from a receipt or invoice.
type RecordReceiptDetails struct {
	DocumentID  string `json:"document_id"`
	AmountMinor string `json:"amount_minor,omitempty"`
	Date        string `json:"date,omitempty"`
	Vendor      string `json:"vendor,omitempty"`
}

// ActionConfirmReading asks the owner to confirm the fields Vult couldn't
// read, beside the document.
const ActionConfirmReading = "confirm_reading"

// Agents only read checks.
func documentCheckMethod(method string) bool { return method == "list_document_checks" }

func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func noteCheck(ctx context.Context, tx pgx.Tx, s Scope, c DocumentCheck) error {
	if c.Result == "" {
		c.Result = ResultOK
	}
	if !checkNames[c.Check] || !checkResults[c.Result] || c.Action != "" && c.Action != ActionConfirmReading {
		return ErrInvalid
	}
	if c.Result != ResultOK && c.Result != ResultRunning && strings.TrimSpace(c.Reason) == "" {
		c.Reason = "Vult couldn't check this."
	}
	_, err := tx.Exec(ctx, `INSERT INTO public.document_checks(tenant_id,book_id,document_id,check_name,result,against,found,reason,fix,action,actor_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, s.TenantID, s.BookID, c.DocumentID, c.Check, c.Result,
		cut(c.Against, 200), cut(c.Found, 200), cut(c.Reason, 300), cut(c.Fix, 300), c.Action, s.ActorID)
	return dbError(err)
}

func documentCheckCall(ctx context.Context, tx pgx.Tx, s Scope, method string, input json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "list_document_checks":
		var q DocumentCheckQuery
		if len(input) > 0 && strictjson.Decode(input, &q) != nil || q.After < 0 || q.DocumentID != "" && !ValidID(q.DocumentID) {
			return nil, ErrInvalid
		}
		page, err := readDocumentChecks(ctx, tx, s, q)
		if err != nil {
			return nil, err
		}
		return raw(page), nil
	}
	return nil, ErrInvalid
}

// ReadDocumentChecks is the owner's read of a Book's checks.
func (a TenantAdmin) ReadDocumentChecks(ctx context.Context, book string, q DocumentCheckQuery) (DocumentCheckPage, error) {
	var out DocumentCheckPage
	if q.After < 0 || q.DocumentID != "" && !ValidID(q.DocumentID) {
		return out, ErrInvalid
	}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readDocumentChecks(ctx, tx, s, q)
		return err
	})
	return out, err
}

func readDocumentChecks(ctx context.Context, tx pgx.Tx, s Scope, q DocumentCheckQuery) (DocumentCheckPage, error) {
	out := DocumentCheckPage{Checks: []DocumentCheck{}, Next: q.After}
	var document *string
	if q.DocumentID != "" {
		document = &q.DocumentID
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (c.document_id,c.check_name) c.sequence,c.document_id::text,c.check_name,c.result,c.against,c.found,c.reason,c.fix,c.action,c.recorded_at
 FROM public.document_checks c WHERE c.tenant_id=$1 AND c.book_id=$2 AND ($3::uuid IS NULL OR c.document_id=$3::uuid)
 AND c.document_id IN (SELECT n.document_id FROM public.document_checks n WHERE n.tenant_id=$1 AND n.book_id=$2 AND n.sequence>$4 AND ($3::uuid IS NULL OR n.document_id=$3::uuid))
 ORDER BY c.document_id,c.check_name,c.sequence DESC LIMIT 2001`, s.TenantID, s.BookID, document, q.After)
	if err != nil {
		return out, dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c DocumentCheck
		if err := rows.Scan(&c.Seq, &c.DocumentID, &c.Check, &c.Result, &c.Against, &c.Found, &c.Reason, &c.Fix, &c.Action, &c.At); err != nil {
			return out, dbError(err)
		}
		c.At = c.At.UTC()
		c.OK = c.Result == ResultOK || c.Result == ResultNote
		out.Checks = append(out.Checks, c)
		if c.Seq > out.Next {
			out.Next = c.Seq
		}
		if len(out.Checks) > 2000 {
			return out, ErrLimit
		}
	}
	if err := rows.Err(); err != nil {
		return out, dbError(err)
	}
	// In the order they happened.
	sortChecks(out.Checks)
	out.Readings = []ReadingSummary{}
	seen := map[string]bool{}
	documents := []string{}
	for _, c := range out.Checks {
		if seen[c.DocumentID] {
			continue
		}
		seen[c.DocumentID] = true
		documents = append(documents, c.DocumentID)
	}
	if len(documents) == 0 {
		return out, nil
	}
	readings, err := tx.Query(ctx, `SELECT DISTINCT ON (document_id) document_id::text,read_by,reader,missing,recorded_at
 FROM public.document_readings WHERE tenant_id=$1 AND book_id=$2 AND document_id=ANY($3::uuid[])
 ORDER BY document_id,sequence DESC`, s.TenantID, s.BookID, documents)
	if err != nil {
		return out, dbError(err)
	}
	defer readings.Close()
	latest := map[string]ReadingSummary{}
	for readings.Next() {
		var r ReadingSummary
		if err := readings.Scan(&r.DocumentID, &r.ReadBy, &r.Reader, &r.Missing, &r.At); err != nil {
			return out, dbError(err)
		}
		r.At = r.At.UTC()
		latest[r.DocumentID] = r
	}
	if err := readings.Err(); err != nil {
		return out, dbError(err)
	}
	for _, id := range documents {
		if r, ok := latest[id]; ok {
			out.Readings = append(out.Readings, r)
		}
	}
	return out, nil
}

func sortChecks(c []DocumentCheck) {
	sort.Slice(c, func(i, j int) bool { return c[i].Seq < c[j].Seq })
}

func documentName(ctx context.Context, tx pgx.Tx, s Scope, id string) string {
	var name string
	_ = tx.QueryRow(ctx, "SELECT original_name FROM public.documents WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, id).Scan(&name)
	if name == "" {
		return "another document"
	}
	return name
}

// amountText writes a minor-unit amount for a person: $4,210.00, or the
// currency code before other currencies.
func amountText(minor int64, scale int, currency string) string {
	plain := formatMinor(strconv.FormatInt(minor, 10), scale)
	sign := ""
	if strings.HasPrefix(plain, "-") {
		sign, plain = "-", plain[1:]
	}
	whole, frac, _ := strings.Cut(plain, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	text := b.String()
	if frac != "" {
		text += "." + frac
	}
	if currency == "USD" {
		return sign + "$" + text
	}
	return sign + currency + " " + text
}

func dayText(date string) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return d.Format("2 Jan 2006")
}

func periodText(start, end string) string { return dayText(start) + " – " + dayText(end) }

type earlierStatement struct {
	id, document, start, end string
	opening, closing         int64
}

// recordStatementChecks compares a newly imported statement with the account and
// the account's other statements, and records each check.
func recordStatementChecks(ctx context.Context, tx pgx.Tx, s Scope, q ImportStatement, st Statement) error {
	doc := q.DocumentID
	var all []DocumentCheck
	add := func(c DocumentCheck) error {
		if c.Result == "" {
			c.Result = ResultOK
		}
		c.OK = c.Result == ResultOK || c.Result == ResultNote
		c.DocumentID = doc
		all = append(all, c)
		return noteCheck(ctx, tx, s, c)
	}
	account := st.AccountName
	if account == "" {
		account = "This account"
	}

	// A statement file has no account number; the owner chose the account.
	if err := add(DocumentCheck{Check: CheckStatementAccount, Against: account, Result: ResultReview,
		Reason: "A CSV statement shows no account number, so Vult took the account you chose.",
		Fix:    "Check the file is for " + account + "."}); err != nil {
		return err
	}
	opening, _ := minorInt(q.OpeningMinor)
	closing, _ := minorInt(q.ClosingMinor)
	var lines []StatementLine
	for _, r := range q.Rows {
		if r.Lifecycle == "pending" || r.Lifecycle == "cancelled" {
			continue
		}
		n, _ := minorInt(r.AmountMinor)
		lines = append(lines, StatementLine{Date: r.Date, AmountMinor: n})
	}
	var fromFile string
	_ = tx.QueryRow(ctx, "SELECT coalesce(current_setting('vult.statement_file',true),'')").Scan(&fromFile)
	numbers, err := numberChecks(ctx, tx, s, StatementReading{DocumentID: doc, AccountID: q.AccountID, PeriodStart: q.PeriodStart, PeriodEnd: q.PeriodEnd,
		OpeningMinor: opening, ClosingMinor: closing, Lines: lines}, st.ID, account, st.Scale, st.Currency, fileDerived(fromFile == "on" && q.EvidenceState == "incomplete"), false)
	if err != nil {
		return err
	}
	for _, c := range numbers {
		if err := add(c); err != nil {
			return err
		}
	}
	return done(ctx, tx, s, doc, all)
}

// StatementReading is a statement as read: its account in the Book, period,
// balances and settled lines, signed the way the balance moves.
type StatementReading struct {
	DocumentID   string
	AccountID    string
	PeriodStart  string
	PeriodEnd    string
	OpeningMinor int64
	ClosingMinor int64
	Lines        []StatementLine
	// OpeningDerived says the document gave no opening balance, and it was
	// worked out from the closing balance and the lines.
	OpeningDerived bool
}

type StatementLine struct {
	Date        string
	AmountMinor int64
}

// numberChecks compares a statement's period, balances and lines with the
// account's other statements and its bank feed, in plain code. exclude is
// the statement itself once imported; derived says Vult worked out its
// balances.
func numberChecks(ctx context.Context, tx pgx.Tx, s Scope, q StatementReading, exclude, account string, scale int, currency string, derived string, imported bool) ([]DocumentCheck, error) {
	var out []DocumentCheck
	add := func(c DocumentCheck) error {
		if c.Result == "" {
			c.Result = ResultOK
		}
		c.OK = c.Result == ResultOK || c.Result == ResultNote
		c.DocumentID = q.DocumentID
		out = append(out, c)
		return nil
	}
	// Earlier and later statements for the same account.
	rows, err := tx.Query(ctx, `SELECT s.id::text,s.document_id::text,s.period_start::text,s.period_end::text,s.opening_balance_minor,s.closing_balance_minor
 FROM public.statements s WHERE s.tenant_id=$1 AND s.book_id=$2 AND s.account_id=$3 AND s.id::text<>$4 ORDER BY s.period_start,s.period_end`,
		s.TenantID, s.BookID, q.AccountID, exclude)
	if err != nil {
		return nil, dbError(err)
	}
	var others []earlierStatement
	for rows.Next() {
		var e earlierStatement
		if err := rows.Scan(&e.id, &e.document, &e.start, &e.end, &e.opening, &e.closing); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		others = append(others, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	day := func(d string, n int) string {
		t, _ := time.Parse(time.DateOnly, d)
		return t.AddDate(0, 0, n).Format(time.DateOnly)
	}
	var before, after, same, overlap *earlierStatement
	for i := range others {
		e := &others[i]
		switch {
		case e.start == q.PeriodStart && e.end == q.PeriodEnd && e.document != q.DocumentID:
			same = e
		case e.start <= q.PeriodEnd && e.end >= q.PeriodStart && e.document != q.DocumentID:
			if overlap == nil {
				overlap = e
			}
		case e.end < q.PeriodStart:
			before = e
		case e.start > q.PeriodEnd && after == nil:
			after = e
		}
	}
	c := DocumentCheck{Check: CheckStatementPeriod, Found: periodText(q.PeriodStart, q.PeriodEnd)}
	switch {
	case overlap != nil:
		c.Against = periodText(overlap.start, overlap.end)
		c.Result, c.Reason = ResultFailed, "It overlaps the "+periodText(overlap.start, overlap.end)+" statement ("+documentName(ctx, tx, s, overlap.document)+")."
		c.Fix = "Lines in both statements are matched once; check it isn't a corrected copy of the other."
	case before != nil && day(before.end, 1) < q.PeriodStart:
		c.Against = "after " + periodText(before.start, before.end)
		c.Result, c.Reason = ResultFailed, "Nothing covers "+periodText(day(before.end, 1), day(q.PeriodStart, -1))+" between this statement and the one before."
		c.Fix = "Add the statement for the missing dates."
	case after != nil && day(q.PeriodEnd, 1) < after.start:
		c.Against = "before " + periodText(after.start, after.end)
		c.Result, c.Reason = ResultFailed, "Nothing covers "+periodText(day(q.PeriodEnd, 1), day(after.start, -1))+" between this statement and the next."
		c.Fix = "Add the statement for the missing dates."
	case before != nil:
		c.Against = "after " + periodText(before.start, before.end)
	case after != nil:
		c.Against = "before " + periodText(after.start, after.end)
	default:
		c.Against = "the first statement for " + account
	}
	_ = add(c)

	// Its balances.
	opening, closing := q.OpeningMinor, q.ClosingMinor
	sum := int64(0)
	for _, l := range q.Lines {
		sum += l.AmountMinor
	}
	money := func(n int64) string { return amountText(n, scale, currency) }
	c = DocumentCheck{Check: CheckStatementBalances, Found: "opening " + money(opening) + " · closing " + money(closing)}
	switch {
	case derived != "":
		c.Result, c.Reason = ResultNote, derived
	case opening+sum != closing:
		c.Against = "opening + lines = " + money(opening+sum)
		c.Result, c.Reason = ResultFailed, "The lines add up to "+money(opening+sum)+", but the statement closes at "+money(closing)+"."
		c.Fix = "Check every line was read, including the last page; a line may be missing or misread."
	case before != nil && day(before.end, 1) == q.PeriodStart && before.closing != opening:
		c.Against = "closing " + dayText(before.end) + " " + money(before.closing)
		c.Result, c.Reason = ResultFailed, "It opens at "+money(opening)+", but the statement before closed at "+money(before.closing)+"."
		c.Fix = "Check both statements are for the same account, and that no statement is missing between them."
	case before != nil && day(before.end, 1) == q.PeriodStart:
		c.Against = "closing " + dayText(before.end) + " " + money(before.closing)
	default:
		c.Against = "opening + lines = closing"
	}
	_ = add(c)

	// Whether it's already in the Book.
	c = DocumentCheck{Check: CheckDuplicate, Against: strconv.Itoa(len(others)) + " other statements for " + account}
	if len(others) == 1 {
		c.Against = "1 other statement for " + account
	}
	if same != nil {
		name := documentName(ctx, tx, s, same.document)
		c.Against = name
		c.Result, c.Reason = ResultFailed, "The Book already has this account's "+periodText(same.start, same.end)+" statement, from "+name+"."
		c.Fix = "Remove this file if it's a copy; Vult won't record its lines twice."
	} else if !imported && len(q.Lines) > 0 {
		// Lines the bank feed already brought in for the account.
		var fed int
		dates, amounts := make([]string, 0, len(q.Lines)), make([]int64, 0, len(q.Lines))
		for _, l := range q.Lines {
			dates, amounts = append(dates, l.Date), append(amounts, l.AmountMinor)
		}
		_ = tx.QueryRow(ctx, `SELECT count(*) FROM unnest($4::date[],$5::bigint[]) AS l(day,amount) WHERE EXISTS (
 SELECT 1 FROM public.bank_transactions t JOIN public.bank_accounts b ON b.tenant_id=t.tenant_id AND b.book_id=t.book_id AND b.id=t.bank_account_id
 WHERE t.tenant_id=$1 AND t.book_id=$2 AND b.account_id=$3 AND t.source='bank' AND t.removed_at IS NULL AND NOT t.pending
 AND t.amount_minor=l.amount AND t.occurred_on BETWEEN l.day-3 AND l.day+3)`, s.TenantID, s.BookID, q.AccountID, dates, amounts).Scan(&fed)
		if fed > 0 {
			c.Result = ResultNote
			c.Against = "the bank feed for " + account
			c.Reason = strconv.Itoa(fed) + " of its " + strconv.Itoa(len(q.Lines)) + " lines are already in the Book from the bank feed; Vult matches them instead of adding them twice."
		}
	}
	_ = add(c)
	return out, nil
}

func done(ctx context.Context, tx pgx.Tx, s Scope, doc string, all []DocumentCheck) error {
	return noteCheck(ctx, tx, s, Done(doc, all))
}

// Done sums up a document's checks: failed when any failed, review when any
// needs a person, else ok.
func Done(doc string, all []DocumentCheck) DocumentCheck {
	failed, review := 0, 0
	for _, c := range all {
		switch c.Result {
		case ResultFailed:
			failed++
		case ResultReview:
			review++
		}
	}
	c := DocumentCheck{DocumentID: doc, Check: CheckDone, Result: ResultOK}
	count := func(n int, what string) string {
		if n == 1 {
			return "1 check " + what
		}
		return strconv.Itoa(n) + " checks " + what
	}
	switch {
	case failed > 0:
		c.Result, c.Reason = ResultFailed, count(failed, "failed")+"."
		if review > 0 {
			c.Reason = count(failed, "failed") + " and " + count(review, "need") + " a look."
		}
	case review > 0:
		c.Result, c.Reason = ResultReview, count(review, "needs")+" a look."
		if review > 1 {
			c.Reason = count(review, "need") + " a look."
		}
	}
	return c
}

// receiptChecks records what was read from a receipt, whether it's already
// in the Book, and the transaction it belongs to.
func receiptChecks(ctx context.Context, tx pgx.Tx, s Scope, q RecordReceiptDetails) ([]DocumentCheck, error) {
	var state string
	if err := tx.QueryRow(ctx, "SELECT state FROM public.documents WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, q.DocumentID).Scan(&state); err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrNotFound
		}
		return nil, dbError(err)
	}
	book, err := readBook(ctx, tx, s)
	if err != nil {
		return nil, dbError(err)
	}
	var all []DocumentCheck
	add := func(c DocumentCheck) error {
		if c.Result == "" {
			c.Result = ResultOK
		}
		c.OK = c.Result == ResultOK || c.Result == ResultNote
		c.DocumentID = q.DocumentID
		all = append(all, c)
		return noteCheck(ctx, tx, s, c)
	}
	amount, _ := minorInt(q.AmountMinor)
	vendor := strings.TrimSpace(q.Vendor)
	var parts, missing []string
	if q.AmountMinor != "" {
		parts = append(parts, amountText(amount, book.Scale, book.Currency))
	} else {
		missing = append(missing, "amount")
	}
	if q.Date != "" {
		parts = append(parts, dayText(q.Date))
	} else {
		missing = append(missing, "date")
	}
	if vendor != "" {
		parts = append(parts, vendor)
	} else {
		missing = append(missing, "vendor")
	}
	c := DocumentCheck{Check: CheckReceiptDetails, Found: strings.Join(parts, " · "), Against: "amount, date and vendor"}
	if len(missing) > 0 {
		c.Result, c.Reason = ResultFailed, "Its "+strings.Join(missing, " and ")+" couldn't be read."
	}
	if err := add(c); err != nil {
		return nil, err
	}

	// Another receipt with the same amount, date and vendor.
	c = DocumentCheck{Check: CheckDuplicate, Against: "other receipts in this Book"}
	if len(missing) == 0 {
		var other string
		_ = tx.QueryRow(ctx, `SELECT c.document_id::text FROM public.document_checks c WHERE c.tenant_id=$1 AND c.book_id=$2 AND c.check_name='receipt_details'
 AND c.document_id<>$3 AND c.found=$4 ORDER BY c.sequence LIMIT 1`, s.TenantID, s.BookID, q.DocumentID, cut(strings.Join(parts, " · "), 200)).Scan(&other)
		if other != "" {
			name := documentName(ctx, tx, s, other)
			c.Against = name
			c.Result, c.Reason = ResultFailed, "It has the same amount, date and vendor as "+name+"."
		}
	} else {
		c.Result, c.Reason = ResultReview, "Without its amount, date and vendor, Vult can't tell whether it's already here."
	}
	if err := add(c); err != nil {
		return nil, err
	}

	// The transaction it belongs to: an entry citing it, or a transaction of
	// the same amount within five days.
	c = DocumentCheck{Check: CheckReceiptMatched}
	var label string
	_ = tx.QueryRow(ctx, `SELECT left(to_char(e.accounting_date,'FMDD Mon YYYY')||' · '||e.description,200) FROM public.entries e
 WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.id IN (
   SELECT x.entry_id FROM public.source_citations x WHERE x.tenant_id=$1 AND x.book_id=$2 AND x.document_id=$3
   UNION SELECT x.entry_id FROM public.evidence_links x WHERE x.tenant_id=$1 AND x.book_id=$2 AND x.document_id=$3 AND x.action='attach')
 ORDER BY e.sequence DESC LIMIT 1`, s.TenantID, s.BookID, q.DocumentID).Scan(&label)
	if label == "" && q.AmountMinor != "" && q.Date != "" {
		abs := amount
		if abs < 0 {
			abs = -abs
		}
		_ = tx.QueryRow(ctx, `SELECT label FROM (
   SELECT e.accounting_date AS day,left(to_char(e.accounting_date,'FMDD Mon YYYY')||' · '||e.description,200) AS label
   FROM public.entries e JOIN public.lines l ON l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id
   JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id AND a.role IN ('cash','card')
   WHERE e.tenant_id=$1 AND e.book_id=$2 AND l.amount_minor=$3 AND e.accounting_date BETWEEN $4::date-5 AND $4::date+5
     AND e.reversal_of IS NULL AND NOT EXISTS (SELECT 1 FROM public.entries r WHERE r.tenant_id=e.tenant_id AND r.book_id=e.book_id AND r.reversal_of=e.id)
   UNION ALL
   SELECT t.occurred_on,left(to_char(t.occurred_on,'FMDD Mon YYYY')||' · '||coalesce(nullif(t.counterparty,''),nullif(t.description,''),'Bank item')||' (not recorded yet)',200)
   FROM public.bank_transactions t WHERE t.tenant_id=$1 AND t.book_id=$2 AND abs(t.amount_minor)=$3 AND t.removed_at IS NULL AND NOT t.pending
     AND t.occurred_on BETWEEN $4::date-5 AND $4::date+5
 ) m ORDER BY abs(m.day-$4::date) LIMIT 1`, s.TenantID, s.BookID, abs, q.Date).Scan(&label)
	}
	switch {
	case label != "":
		c.Result, c.Against = ResultOK, label
	case q.AmountMinor == "" || q.Date == "":
		c.Result, c.Reason = ResultReview, "Without its amount and date, Vult can't look for its transaction."
	default:
		c.Against = "transactions within 5 days of " + dayText(q.Date)
		c.Result = ResultFailed
		c.Reason = "No transaction of " + amountText(amount, book.Scale, book.Currency) + " is in the Book within 5 days of " + dayText(q.Date) + " yet."
	}
	if err := add(c); err != nil {
		return nil, err
	}
	if err := done(ctx, tx, s, q.DocumentID, all); err != nil {
		return nil, err
	}
	page, err := readDocumentChecks(ctx, tx, s, DocumentCheckQuery{DocumentID: q.DocumentID})
	return page.Checks, err
}

// How a document was read.
const (
	ReadByExport = "export"
	ReadByPDF    = "pdf"
	ReadByOCR    = "ocr"
	ReadByAI     = "ai"
	ReadByAgent  = "agent"
	ReadByOwner  = "owner"
)

var readBy = map[string]bool{ReadByExport: true, ReadByPDF: true, ReadByOCR: true, ReadByAI: true, ReadByAgent: true, ReadByOwner: true}

// ReadingSummary is how a document was read: ReadBy, Reader (the agent's
// name, for an agent's reading) and the needed fields still missing.
type ReadingSummary struct {
	DocumentID string    `json:"document_id"`
	ReadBy     string    `json:"read_by"`
	Reader     string    `json:"reader,omitempty"`
	Missing    []string  `json:"missing"`
	At         time.Time `json:"at"`
}

// DocumentReading is a reading with its fields, as the checker keeps them.
type DocumentReading struct {
	ReadingSummary
	Fields json.RawMessage `json:"fields"`
}

func noteReading(ctx context.Context, tx pgx.Tx, s Scope, r DocumentReading) error {
	if !ValidID(r.DocumentID) || !readBy[r.ReadBy] || len(r.Reader) > 120 || len(r.Missing) > 16 || len(r.Fields) == 0 || len(r.Fields) > 1<<20 {
		return ErrInvalid
	}
	if r.Missing == nil {
		r.Missing = []string{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO public.document_readings(tenant_id,book_id,document_id,read_by,reader,fields,missing,actor_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, s.TenantID, s.BookID, r.DocumentID, r.ReadBy, r.Reader, string(r.Fields), r.Missing, s.ActorID)
	return dbError(err)
}

func latestReading(ctx context.Context, tx pgx.Tx, s Scope, document string) (DocumentReading, bool, error) {
	var r DocumentReading
	var fields []byte
	err := tx.QueryRow(ctx, `SELECT document_id::text,read_by,reader,missing,fields,recorded_at FROM public.document_readings
 WHERE tenant_id=$1 AND book_id=$2 AND document_id=$3 ORDER BY sequence DESC LIMIT 1`, s.TenantID, s.BookID, document).
		Scan(&r.DocumentID, &r.ReadBy, &r.Reader, &r.Missing, &fields, &r.At)
	if err == pgx.ErrNoRows {
		return r, false, nil
	}
	if err != nil {
		return r, false, dbError(err)
	}
	r.Fields, r.At = fields, r.At.UTC()
	return r, true, nil
}

func fileDerived(derived bool) string {
	if derived {
		return "The file gives no balances, so Vult worked them out from the Book."
	}
	return ""
}
