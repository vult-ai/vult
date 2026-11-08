package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Book search finds entries, accounts and sources the way a person describes
// them: words in any order, an amount, a date. The interface's finder, the
// list filters and the search_book agent tool all use it, so people and agents
// find the same records.
//
// A query is a list of terms and every term must match. A term may match in
// several ways (as words, a date or an amount) and counts its best way. A
// record ranks by its weakest term:
//
//	1  the name of an account the entry posts to, its date, or its amount
//	2  other text (description and memos), a phrase, or one line's amount
//	3  a word with a small typo
//
// Newest accounting date first within a rank. Text is folded and split by the
// database's public.search_tokens, for stored words and query words alike.

const (
	SearchMaxCharacters     = 200
	SearchMaxTerms          = 8
	SearchMaxTermCharacters = 64
	searchMaxLimit          = 20
	searchDefaultLimit      = 5
)

// ErrQueryTooLong rejects queries exceeding the character, term or word bound.
var ErrQueryTooLong = errors.New("search query is too long")

var searchGroups = []string{"transactions", "accounts", "counterparties", "sources"}

type SearchQuery struct {
	Query string   `json:"query"`
	Types []string `json:"types,omitempty"`
	Limit int      `json:"limit,omitempty"`
	// AccountIDsOnly is the browser's complete account search selection.
	AccountIDsOnly bool `json:"-"`
	// Today anchors dates without a year ("Oct 24", "October"). The caller sets
	// it to the Book's local date; empty means today in UTC.
	Today string `json:"-"`
}

type SearchGroup[T any] struct {
	Items []T `json:"items"`
	// Total is the number of matches; every group's total is exact.
	Total int `json:"total"`
	// Status is "not_recorded" for a group the ledger has no facts for.
	Status string `json:"status,omitempty"`
}

// SearchEntry describes a journal entry the way Activity does: its amount is
// the Book-currency debit total (amount_role "book_debit_total"), unsigned. No
// flow direction or counterparty is inferred.
type SearchEntry struct {
	ID          string   `json:"id"`
	Date        string   `json:"date"`
	Description string   `json:"description"`
	Kind        string   `json:"kind"` // "journal" or "reversal"
	AmountMinor string   `json:"amount_minor"`
	AmountRole  string   `json:"amount_role"`
	ReversalOf  string   `json:"reversal_of,omitempty"`
	Accounts    []string `json:"accounts"`
}
type SearchAccount struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Name  string `json:"name"`
	Class string `json:"class"`
}
type SearchCounterparty struct {
	Name    string `json:"name"`
	Entries int    `json:"entries"`
}
type SearchSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Date string `json:"date"`
}
type SearchResult struct {
	SchemaVersion int    `json:"schema_version"`
	BookID        string `json:"book_id"`
	Currency      string `json:"currency"`
	Scale         int    `json:"scale"`
	RecordedState
	Query          string                          `json:"query"`
	Transactions   SearchGroup[SearchEntry]        `json:"transactions"`
	Accounts       SearchGroup[SearchAccount]      `json:"accounts"`
	AccountIDs     []string                        `json:"account_ids,omitempty"`
	Counterparties SearchGroup[SearchCounterparty] `json:"counterparties"`
	Sources        SearchGroup[SearchSource]       `json:"sources"`
}

// SearchBook answers a Book search for the Book's owner. The results and the
// recorded state they describe come from the same scoped read, and only
// entries recorded by that state's cutoff are found.
func (a TenantAdmin) SearchBook(ctx context.Context, book string, q SearchQuery) (SearchResult, error) {
	var out SearchResult
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, header Book) (err error) {
		out, err = searchRecorded(ctx, tx, scope, header, q)
		return err
	})
	if err != nil {
		return SearchResult{}, err
	}
	return out, nil
}

// searchRecorded captures the Book's recorded state and searches within it.
func searchRecorded(ctx context.Context, tx pgx.Tx, scope Scope, header Book, q SearchQuery) (SearchResult, error) {
	state, err := captureReadState(ctx, tx, scope, header, recordedReadScope{})
	if err != nil {
		return SearchResult{}, err
	}
	if q.Today == "" {
		q.Today = BookToday(header, state.cutoff)
	}
	out, err := searchBook(ctx, tx, scope, q, state.cutoff)
	out.RecordedState = state.RecordedState
	out.SchemaVersion, out.BookID, out.Currency, out.Scale = 1, header.ID, header.Currency, header.Scale
	return out, err
}

// BookToday is the Book's local date at an instant: its accounting timezone,
// or UTC when none is set.
func BookToday(header Book, at time.Time) string {
	if location, err := time.LoadLocation(header.AccountingTimezone); header.AccountingTimezone != "" && err == nil {
		return at.In(location).Format("2006-01-02")
	}
	return at.UTC().Format("2006-01-02")
}

// searchBook runs a search inside a transaction whose row policies already
// scope it to the Book: a connection's or the owner's. Entries recorded after
// cutoff are not found.
func searchBook(ctx context.Context, tx pgx.Tx, s Scope, q SearchQuery, cutoff time.Time) (SearchResult, error) {
	out := SearchResult{Query: strings.TrimSpace(q.Query)}
	out.Transactions.Items = []SearchEntry{}
	out.Accounts.Items = []SearchAccount{}
	// The ledger records no counterparties; none are inferred from text.
	out.Counterparties = SearchGroup[SearchCounterparty]{Items: []SearchCounterparty{}, Status: "not_recorded"}
	out.Sources.Items = []SearchSource{}
	limit := q.Limit
	if limit == 0 {
		limit = searchDefaultLimit
	}
	if limit < 1 || limit > searchMaxLimit {
		return out, ErrInvalid
	}
	want := map[string]bool{}
	for _, t := range q.Types {
		if !contains(searchGroups, t) {
			return out, ErrInvalid
		}
		want[t] = true
	}
	all := len(want) == 0
	if q.AccountIDsOnly && (len(want) != 1 || !want["accounts"]) {
		return out, ErrInvalid
	}
	plan, e := compileSearch(ctx, tx, s, q.Query, q.Today, all || want["accounts"] || want["transactions"])
	if e != nil || plan.empty() {
		return out, e
	}
	if all || want["transactions"] {
		ids, total, err := plan.entries(ctx, tx, limit, cutoff)
		if err != nil {
			return out, err
		}
		out.Transactions.Total = total
		if out.Transactions.Items, err = searchEntryItems(ctx, tx, s, ids); err != nil {
			return out, err
		}
	}
	if all || want["accounts"] {
		out.Accounts, out.AccountIDs = plan.matchingAccounts(limit, q.AccountIDsOnly)
		if q.AccountIDsOnly && out.Accounts.Total > reportAccountLimit {
			return out, ErrLimit
		}
	}
	if all || want["sources"] {
		out.Sources, e = plan.sourceSearchGroup(ctx, tx, limit)
		if e != nil {
			return out, e
		}
	}
	return out, nil
}

// SearchFilter compiles a query into a subquery of matching entry IDs, for a
// list read (Activity's page and summary) to use as `e.id IN (subquery)` beside
// its own filters, cutoff, order and paging, so the list and the finder share
// one population predicate. Placeholders start at $(argOffset+1); append the
// returned arguments after the caller's own. ok is false for an empty or
// punctuation-only query, which filters nothing.
func SearchFilter(ctx context.Context, tx pgx.Tx, s Scope, query, today string, argOffset int) (subquery string, args []any, ok bool, err error) {
	plan, e := compileSearch(ctx, tx, s, query, today, true)
	if e != nil || plan.empty() {
		return "", nil, false, e
	}
	matched, list, e := plan.matchedSQL(argOffset)
	if e != nil {
		return "", nil, false, e
	}
	return "SELECT entry_id FROM (" + matched + ") search_matches", list, true, nil
}

// ---- Parsing --------------------------------------------------------------

type rawTerm struct {
	text   string
	phrase bool
}

var termPattern = regexp.MustCompile(`"([^"]*)"?|(\S+)`)

// searchTerms splits a query into words and quoted phrases, as typed. An
// unclosed quote runs to the end of the query.
func searchTerms(query string) ([]rawTerm, error) {
	if !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return nil, ErrInvalid
	}
	query = strings.TrimSpace(query)
	if utf8.RuneCountInString(query) > SearchMaxCharacters {
		return nil, ErrQueryTooLong
	}
	out := []rawTerm{}
	for _, m := range termPattern.FindAllStringSubmatchIndex(query, -1) {
		if m[2] >= 0 {
			if text := strings.TrimSpace(query[m[2]:m[3]]); text != "" {
				out = append(out, rawTerm{text, true})
			}
		} else {
			out = append(out, rawTerm{query[m[4]:m[5]], false})
		}
	}
	if len(out) > SearchMaxTerms {
		return nil, ErrQueryTooLong
	}
	return out, nil
}

var amountPatternLoose = regexp.MustCompile(`^[-−+]?[$€£¥]?(\d{1,3}(?:,\d{3})+|\d+)(?:\.(\d+))?$`)

// searchAmount reads "$1,502.55", "1502.55" or "-8" as minor units at the
// Book's scale. More decimals than the scale is not an amount. Entry totals
// are arbitrary integers; a line comparison additionally requires int64 range.
func searchAmount(word string, scale int) (string, bool) {
	m := amountPatternLoose.FindStringSubmatch(word)
	if m == nil || len(m[2]) > scale {
		return "", false
	}
	whole, ok := new(big.Int).SetString(strings.ReplaceAll(m[1], ",", ""), 10)
	if !ok {
		return "", false
	}
	frac := m[2] + strings.Repeat("0", scale-len(m[2]))
	minor := new(big.Int).Mul(whole, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil))
	if frac != "" {
		f, _ := new(big.Int).SetString(frac, 10)
		minor.Add(minor, f)
	}
	if minor.Sign() == 0 {
		return "", false
	}
	return minor.String(), true
}

var months = []string{"january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"}

func monthOf(w string) int {
	w = strings.ToLower(w)
	if len(w) < 3 || strings.Trim(w, "abcdefghijklmnopqrstuvwxyz") != "" {
		return -1
	}
	for i, m := range months {
		if strings.HasPrefix(m, w) {
			return i
		}
	}
	return -1
}

type dateRange struct{ from, to string }

var (
	isoDate = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	year    = regexp.MustCompile(`^(19|20)\d{2}$`)
	fourDig = regexp.MustCompile(`^\d{4}$`)
	dayNum  = regexp.MustCompile(`^\d{1,2}$`)
)

func validDay(y, m, d int) bool {
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && int(t.Month()) == m && t.Day() == d
}
func day(y, m, d int) string { return fmt.Sprintf("%04d-%02d-%02d", y, m, d) }
func lastDay(y, m int) int   { return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day() }

// searchDate reads a date at raw[i]: "2026-10-24", "Oct 24 2019", "Oct 24",
// "Oct 2026", "October" or "2026". Without a year, a month or day means the
// most recent one up to today. It returns the range and how many terms it used.
func searchDate(raw []rawTerm, i int, today time.Time) (dateRange, int, bool) {
	word := func(k int) string {
		if k < len(raw) && !raw[k].phrase {
			return raw[k].text
		}
		return ""
	}
	w := word(i)
	if isoDate.MatchString(w) && ValidDate(w) {
		return dateRange{w, w}, 1, true
	}
	if year.MatchString(w) {
		return dateRange{w + "-01-01", w + "-12-31"}, 1, true
	}
	m := monthOf(w)
	if m < 0 {
		return dateRange{}, 0, false
	}
	month := m + 1
	next, after := word(i+1), word(i+2)
	ty, tm, td := today.Date()
	if fourDig.MatchString(next) {
		y, _ := strconv.Atoi(next)
		return dateRange{day(y, month, 1), day(y, month, lastDay(y, month))}, 2, true
	}
	if dayNum.MatchString(next) {
		d, _ := strconv.Atoi(next)
		if fourDig.MatchString(after) {
			y, _ := strconv.Atoi(after)
			if !validDay(y, month, d) {
				return dateRange{}, 0, false
			}
			return dateRange{day(y, month, d), day(y, month, d)}, 3, true
		}
		todayISO := day(ty, int(tm), td)
		for y := ty; y > ty-8; y-- {
			if validDay(y, month, d) && day(y, month, d) <= todayISO {
				return dateRange{day(y, month, d), day(y, month, d)}, 2, true
			}
		}
		return dateRange{}, 0, false
	}
	y := ty
	if month > int(tm) {
		y--
	}
	return dateRange{day(y, month, 1), day(y, month, lastDay(y, month))}, 1, true
}

// ---- Compilation ------------------------------------------------------------

// A word must match the start of a stored word; numbers and single
// characters must match a whole word.
type searchWord struct {
	word     string
	whole    bool
	accounts []string // accounts whose code or name has the word: rank 1
	typoAccs []string // accounts with a name within a small typo: rank 3
	fallback bool     // nothing matched the word, so typos count
}

type searchTerm struct {
	dates  *dateRange
	amount string
	words  []*searchWord // all must match (one typed term can hold several words)
	phrase string        // folded phrase; words then narrow the candidates
}

type searchAccountRow struct {
	SearchAccount
	tokens     []string
	nameTokens []string
	folded     string
}

type searchPlan struct {
	s        Scope
	terms    []searchTerm
	accounts []searchAccountRow
}

func (p searchPlan) empty() bool { return len(p.terms) == 0 }

func compileSearch(ctx context.Context, tx pgx.Tx, s Scope, query, todayISO string, includeAccounts bool) (searchPlan, error) {
	plan := searchPlan{s: s}
	raw, e := searchTerms(query)
	if e != nil || len(raw) == 0 {
		return plan, e
	}
	today := time.Now().UTC()
	if todayISO != "" {
		if t, err := time.Parse("2006-01-02", todayISO); err == nil {
			today = t
		} else {
			return plan, ErrInvalid
		}
	}
	var scale int
	if e = tx.QueryRow(ctx, "SELECT scale FROM public.books WHERE tenant_id=$1 AND id=$2", s.TenantID, s.BookID).Scan(&scale); e != nil {
		return plan, dbError(e)
	}
	if includeAccounts {
		if plan.accounts, e = searchAccounts(ctx, tx, s); e != nil {
			return plan, e
		}
	}
	// Fold every term's text in one round trip.
	texts := make([]string, len(raw))
	for i, t := range raw {
		texts[i] = t.text
	}
	var folded []string
	var tokens [][]string
	rows, e := tx.Query(ctx, "SELECT btrim(public.search_text(t)), public.search_tokens(t) FROM unnest($1::text[]) WITH ORDINALITY AS q(t,n) ORDER BY n", texts)
	if e != nil {
		return plan, dbError(e)
	}
	for rows.Next() {
		var f string
		var w []string
		if e = rows.Scan(&f, &w); e != nil {
			rows.Close()
			return plan, dbError(e)
		}
		folded, tokens = append(folded, f), append(tokens, w)
	}
	rows.Close()
	if e = rows.Err(); e != nil {
		return plan, dbError(e)
	}
	for _, text := range folded {
		for _, word := range strings.Fields(text) {
			if utf8.RuneCountInString(word) > SearchMaxTermCharacters {
				return plan, ErrQueryTooLong
			}
		}
	}
	for i := 0; i < len(raw); i++ {
		t := raw[i]
		if t.phrase {
			if folded[i] == "" {
				continue
			}
			term := searchTerm{phrase: folded[i]}
			for _, w := range strings.Fields(folded[i]) {
				term.words = append(term.words, &searchWord{word: w, whole: wholeWord(w)})
			}
			plan.terms = append(plan.terms, term)
			continue
		}
		var term searchTerm
		if d, span, ok := searchDate(raw, i, today); ok {
			term.dates = &d
			if span > 1 {
				plan.terms = append(plan.terms, term)
				i += span - 1
				continue
			}
		}
		if amount, ok := searchAmount(t.text, scale); ok {
			term.amount = amount
		}
		for _, w := range tokens[i] {
			term.words = append(term.words, &searchWord{word: w, whole: wholeWord(w)})
		}
		if term.dates != nil || term.amount != "" || len(term.words) > 0 {
			plan.terms = append(plan.terms, term)
		}
	}
	for _, term := range plan.terms {
		// A typo fallback is for words only: a date or an amount already matched as itself.
		exact := term.phrase != "" || term.dates != nil || term.amount != ""
		for _, w := range term.words {
			if e = plan.resolve(ctx, tx, w, exact); e != nil {
				return plan, e
			}
		}
	}
	return plan, nil
}

func wholeWord(w string) bool {
	if utf8.RuneCountInString(w) == 1 {
		return true
	}
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (w *searchWord) matches(token string) bool {
	if w.whole {
		return token == w.word
	}
	return strings.HasPrefix(token, w.word)
}

func searchAccounts(ctx context.Context, tx pgx.Tx, s Scope) ([]searchAccountRow, error) {
	rows, e := tx.Query(ctx, `SELECT id::text,class,code,name,public.search_tokens(code||' '||name),public.search_text(code||' '||name),public.search_tokens(name) FROM public.accounts
 WHERE tenant_id=$1 AND book_id=$2 ORDER BY lower(name),code,id LIMIT 100001`, s.TenantID, s.BookID)
	if e != nil {
		return nil, dbError(e)
	}
	defer rows.Close()
	out := []searchAccountRow{}
	for rows.Next() {
		var a searchAccountRow
		if e = rows.Scan(&a.ID, &a.Class, &a.Code, &a.Name, &a.tokens, &a.folded, &a.nameTokens); e != nil {
			return nil, dbError(e)
		}
		out = append(out, a)
	}
	if len(out) > 100000 {
		return nil, ErrLimit
	}
	return out, dbError(rows.Err())
}

// resolve finds the accounts a word names and, when nothing at all matches it,
// account names within a small typo. Description and memo words never gain a typo match.
func (p searchPlan) resolve(ctx context.Context, tx pgx.Tx, w *searchWord, exact bool) error {
	for _, a := range p.accounts {
		for _, t := range a.tokens {
			if w.matches(t) {
				w.accounts = append(w.accounts, a.ID)
				break
			}
		}
	}
	if exact || w.whole || utf8.RuneCountInString(w.word) < 4 || len(w.accounts) > 0 {
		return nil
	}
	// A fallback is allowed only if the word has no ordinary match in the Book.
	exists := `SELECT EXISTS(SELECT 1 FROM public.search_words WHERE tenant_id=$1 AND book_id=$2 AND word LIKE $3)
		OR EXISTS(SELECT 1 FROM public.documents d
		CROSS JOIN LATERAL unnest(public.search_tokens(d.original_name)) AS t(word)
		WHERE d.tenant_id=$1 AND d.book_id=$2 AND ` + liveReadyDocument + ` AND t.word LIKE $3)`
	if p.accounts == nil {
		exists += ` OR EXISTS(SELECT 1 FROM public.accounts a
			CROSS JOIN LATERAL unnest(public.search_tokens(a.code||' '||a.name)) AS t(word)
			WHERE a.tenant_id=$1 AND a.book_id=$2 AND t.word LIKE $3)`
	}
	var stored bool
	if e := tx.QueryRow(ctx, exists, p.s.TenantID, p.s.BookID, w.word+"%").Scan(&stored); e != nil {
		return dbError(e)
	}
	if stored {
		return nil
	}
	w.fallback = true
	for _, a := range p.accounts {
		for _, t := range a.nameTokens {
			if typo(w.word, t) {
				w.typoAccs = append(w.typoAccs, a.ID)
				break
			}
		}
	}
	return nil
}

func trigrams(s string) map[string]bool {
	r := []rune("  " + s + " ")
	out := map[string]bool{}
	for i := 0; i+3 <= len(r); i++ {
		out[string(r[i:i+3])] = true
	}
	return out
}

// typo: a word of 4+ characters within 2 characters of the other's length and
// trigram similarity of at least 0.4.
func typo(word, candidate string) bool {
	a, b := utf8.RuneCountInString(word), utf8.RuneCountInString(candidate)
	if a < 4 || a-b > 2 || b-a > 2 {
		return false
	}
	x, y := trigrams(word), trigrams(candidate)
	shared := 0
	for t := range x {
		if y[t] {
			shared++
		}
	}
	return float64(shared)/float64(len(x)+len(y)-shared) >= 0.4
}

// ---- Entries ------------------------------------------------------------------

type sqlArgs []any

func (a *sqlArgs) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// entries returns the top matching entry IDs in rank order and the total.
func (p searchPlan) entries(ctx context.Context, tx pgx.Tx, limit int, cutoff time.Time) ([]string, int, error) {
	matched, args, e := p.matchedSQL(0)
	if e != nil {
		return nil, 0, e
	}
	list := sqlArgs(args)
	query := fmt.Sprintf(`SELECT e.id::text, count(*) OVER () FROM (%s) m JOIN public.entries e
 ON e.tenant_id=%s AND e.book_id=%s AND e.id=m.entry_id AND e.recorded_at<=%s::timestamptz
 ORDER BY m.r, e.accounting_date DESC, e.sequence DESC LIMIT %s`,
		matched, list.add(p.s.TenantID), list.add(p.s.BookID), list.add(cutoff), list.add(limit))
	rows, e := tx.Query(ctx, query, list...)
	if e != nil {
		return nil, 0, dbError(e)
	}
	defer rows.Close()
	ids, total := []string{}, 0
	for rows.Next() {
		var id string
		if e = rows.Scan(&id, &total); e != nil {
			return nil, 0, dbError(e)
		}
		ids = append(ids, id)
	}
	return ids, total, dbError(rows.Err())
}

// matchedSQL selects (entry_id, r): every entry matching all terms, with its
// rank. Placeholders start at $(offset+1).
func (p searchPlan) matchedSQL(offset int) (string, []any, error) {
	args := sqlArgs(make([]any, offset))
	tenant, book := args.add(p.s.TenantID), args.add(p.s.BookID)
	scope := "tenant_id=" + tenant + "::uuid AND book_id=" + book + "::uuid"
	ctes := []string{}
	for i, term := range p.terms {
		ways := []string{}
		if term.dates != nil {
			ways = append(ways, fmt.Sprintf("SELECT id AS entry_id,1 AS r FROM public.entries WHERE %s AND accounting_date BETWEEN %s::date AND %s::date",
				scope, args.add(term.dates.from), args.add(term.dates.to)))
		}
		if term.amount != "" {
			x := args.add(term.amount)
			ways = append(ways, fmt.Sprintf("SELECT entry_id,1 AS r FROM public.search_totals WHERE %s AND debit_minor=%s::numeric", scope, x))
			minor, valid := new(big.Int).SetString(term.amount, 10)
			if valid && minor.IsInt64() {
				ways = append(ways, fmt.Sprintf("SELECT entry_id,2 AS r FROM public.lines WHERE %s AND amount_minor=%s::bigint", scope, x))
			}
		}
		if len(term.words) > 0 {
			ways = append(ways, p.wordsSQL(term, &args, scope, tenant, book))
		}
		if len(ways) == 0 {
			return "", nil, ErrInvalid
		}
		ctes = append(ctes, fmt.Sprintf("t%d AS (SELECT entry_id,min(r) AS r FROM (%s) w GROUP BY entry_id)", i, strings.Join(ways, " UNION ALL ")))
	}
	joins, ranks := "t0", []string{"t0.r"}
	for i := 1; i < len(p.terms); i++ {
		joins += fmt.Sprintf(" JOIN t%d USING (entry_id)", i)
		ranks = append(ranks, fmt.Sprintf("t%d.r", i))
	}
	return fmt.Sprintf("WITH %s SELECT entry_id, greatest(%s) AS r FROM %s", strings.Join(ctes, ", "), strings.Join(ranks, ","), joins), args[offset:], nil
}

// wordsSQL: every word of the term must match the same entry; the term counts
// its weakest word. A phrase must also appear, in order, in the entry's text.
func (p searchPlan) wordsSQL(term searchTerm, args *sqlArgs, scope, tenant, book string) string {
	parts := []string{}
	for _, w := range term.words {
		cands := []string{}
		if w.whole {
			cands = append(cands, fmt.Sprintf("SELECT entry_id,2 AS r FROM public.search_words WHERE %s AND word=%s", scope, args.add(w.word)))
		} else {
			cands = append(cands, fmt.Sprintf("SELECT entry_id,2 AS r FROM public.search_words WHERE %s AND word LIKE %s", scope, args.add(w.word+"%")))
		}
		if len(w.accounts) > 0 {
			cands = append(cands, fmt.Sprintf("SELECT entry_id,1 AS r FROM public.lines WHERE %s AND account_id=ANY(%s::uuid[])", scope, args.add(w.accounts)))
		}
		if len(w.typoAccs) > 0 {
			cands = append(cands, fmt.Sprintf("SELECT entry_id,3 AS r FROM public.lines WHERE %s AND account_id=ANY(%s::uuid[])", scope, args.add(w.typoAccs)))
		}
		parts = append(parts, fmt.Sprintf("(SELECT entry_id,min(r) AS r FROM (%s) c GROUP BY entry_id)", strings.Join(cands, " UNION ALL ")))
	}
	join, ranks := parts[0]+" w0", []string{"w0.r"}
	for i := 1; i < len(parts); i++ {
		join += fmt.Sprintf(" JOIN %s w%d USING (entry_id)", parts[i], i)
		ranks = append(ranks, fmt.Sprintf("w%d.r", i))
	}
	if term.phrase == "" {
		return fmt.Sprintf("SELECT entry_id, greatest(%s) AS r FROM %s", strings.Join(ranks, ","), join)
	}
	// OFFSET 0 keeps the candidates (entries holding every word) a separate step,
	// so the phrase check folds only their text, never the whole Book's.
	return fmt.Sprintf(`SELECT c.entry_id, CASE WHEN a.matched THEN 1 ELSE 2 END AS r
 FROM (SELECT w0.entry_id FROM %s OFFSET 0) c
 JOIN public.entries e ON e.tenant_id=%[3]s::uuid AND e.book_id=%[4]s::uuid AND e.id=c.entry_id
 CROSS JOIN LATERAL (SELECT EXISTS(
   SELECT 1 FROM public.lines l JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id
   WHERE l.tenant_id=%[3]s::uuid AND l.book_id=%[4]s::uuid AND l.entry_id=e.id
    AND public.search_text(a.code||' '||a.name) LIKE %[2]s) AS matched OFFSET 0) a
 WHERE a.matched OR public.search_text(e.description||' '||coalesce((SELECT string_agg(l.memo,' ' ORDER BY l.position) FROM public.lines l
 WHERE l.tenant_id=%[3]s::uuid AND l.book_id=%[4]s::uuid AND l.entry_id=e.id),'')) LIKE %[2]s`, join, args.add("% "+term.phrase+"%"), tenant, book)
}

func searchEntryItems(ctx context.Context, tx pgx.Tx, s Scope, ids []string) ([]SearchEntry, error) {
	out := []SearchEntry{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, e := tx.Query(ctx, `SELECT e.id::text,e.accounting_date::text,e.description,coalesce(e.reversal_of::text,''),
 coalesce(t.debit_minor,0)::text,
 ARRAY(SELECT CASE WHEN a.name<>'' THEN a.name ELSE a.code END FROM public.lines l JOIN public.accounts a ON a.tenant_id=l.tenant_id
  AND a.book_id=l.book_id AND a.id=l.account_id WHERE l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id
  GROUP BY a.id,a.name,a.code ORDER BY min(l.position) LIMIT 2)
FROM public.entries e LEFT JOIN public.search_totals t ON t.tenant_id=e.tenant_id AND t.book_id=e.book_id AND t.entry_id=e.id
WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.id=ANY($3::uuid[])`, s.TenantID, s.BookID, ids)
	if e != nil {
		return nil, dbError(e)
	}
	defer rows.Close()
	found := map[string]SearchEntry{}
	for rows.Next() {
		v := SearchEntry{Kind: "journal", AmountRole: "book_debit_total"}
		if e = rows.Scan(&v.ID, &v.Date, &v.Description, &v.ReversalOf, &v.AmountMinor, &v.Accounts); e != nil {
			return nil, dbError(e)
		}
		if v.ReversalOf != "" {
			v.Kind = "reversal"
		}
		found[v.ID] = v
	}
	if e = rows.Err(); e != nil {
		return nil, dbError(e)
	}
	for _, id := range ids {
		v, ok := found[id]
		if !ok {
			return nil, ErrDenied
		}
		out = append(out, v)
	}
	return out, nil
}

// ---- Accounts and sources ---------------------------------------------------------

// nameRank ranks a name's words against the query: 1 for words that match,
// 3 when one needed a typo, -1 when a term doesn't match. Terms that are only
// a date or an amount don't apply to names.
func (p searchPlan) nameRank(tokens []string, folded, id string) int {
	worst, applied := 0, false
	for _, term := range p.terms {
		if len(term.words) == 0 {
			continue
		}
		applied = true
		if term.phrase != "" {
			if !strings.Contains(folded, " "+term.phrase) {
				return -1
			}
			worst = max(worst, 1)
			continue
		}
		termRank := 0
		for _, w := range term.words {
			r := -1
			for _, t := range tokens {
				if w.matches(t) {
					r = 1
					break
				}
			}
			if r < 0 && w.fallback && contains(w.typoAccs, id) {
				r = 3
			}
			if r < 0 {
				return -1
			}
			termRank = max(termRank, r)
		}
		worst = max(worst, termRank)
	}
	if !applied {
		return -1
	}
	return worst
}

func (p searchPlan) matchingAccounts(limit int, idsOnly bool) (SearchGroup[SearchAccount], []string) {
	type ranked struct {
		a SearchAccount
		r int
	}
	out := SearchGroup[SearchAccount]{Items: []SearchAccount{}}
	var matched []ranked
	var ids []string
	for _, a := range p.accounts {
		if r := p.nameRank(a.tokens, a.folded, a.ID); r > 0 {
			out.Total++
			if idsOnly {
				ids = append(ids, a.ID)
			} else {
				matched = append(matched, ranked{a.SearchAccount, r})
			}
		}
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].r < matched[j].r })
	for _, match := range matched[:min(len(matched), limit)] {
		out.Items = append(out.Items, match.a)
	}
	return out, ids
}

// sourceMatchesSQL is shared by the finder and the paged Sources list. The
// database returns the exact count and bounded page, even in a large Book.
func (p searchPlan) sourceMatchesSQL() (string, sqlArgs) {
	args := sqlArgs{p.s.TenantID, p.s.BookID}
	var conditions []string
	for _, term := range p.terms {
		if term.phrase != "" {
			conditions = append(conditions, "strpos(folded,"+args.add(" "+term.phrase)+")>0")
			continue
		}
		for _, word := range term.words {
			conditions = append(conditions, sourceWordSQL(word, &args))
		}
	}
	if len(conditions) == 0 {
		return "", nil
	}
	return `WITH available AS MATERIALIZED (
		SELECT sequence,id,original_name,media_type,created_at,public.search_text(original_name) AS folded
		FROM public.documents WHERE tenant_id=$1 AND book_id=$2 AND ` + liveReadyDocument + `
	), matches AS MATERIALIZED (
		SELECT sequence,id,original_name,media_type,created_at FROM available
		WHERE ` + strings.Join(conditions, " AND ") + `
	) `, args
}

func sourceWordSQL(word *searchWord, args *sqlArgs) string {
	const tokens = `EXISTS(SELECT 1 FROM unnest(string_to_array(btrim(folded),' ')) AS t(token)
		WHERE length(token) BETWEEN 1 AND 64 AND `
	if word.whole {
		return tokens + "token=" + args.add(word.word) + ")"
	}
	if !word.fallback {
		return tokens + "token LIKE " + args.add(word.word+"%") + ")"
	}
	// The word was absent in the ordinary search. Apply the same padded
	// trigram Jaccard rule as typo(), with exact integer arithmetic.
	var grams []string
	for gram := range trigrams(word.word) {
		grams = append(grams, gram)
	}
	length := args.add(utf8.RuneCountInString(word.word))
	probe := args.add(grams) + "::text[]"
	return tokens + "length(token) BETWEEN " + length + "::int-2 AND " + length + `::int+2
		AND (SELECT 7*count(DISTINCT gram) FILTER(WHERE gram=ANY(` + probe + `)) >=
			2*(count(DISTINCT gram)+cardinality(` + probe + `))
			FROM (SELECT substr('  '||token||' ',n,3) AS gram
				FROM generate_series(1,length(token)+1) n) AS trigrams))`
}

func (p searchPlan) sourceSearchGroup(ctx context.Context, tx pgx.Tx, limit int) (SearchGroup[SearchSource], error) {
	out := SearchGroup[SearchSource]{Items: []SearchSource{}}
	query, args := p.sourceMatchesSQL()
	if query == "" {
		return out, nil
	}
	// Fallback words have no exact matches in the Book, so every source that
	// satisfies the same terms has the same rank.
	query += `SELECT (SELECT count(*) FROM matches),
		coalesce(json_agg(json_build_object('id',id,'name',original_name,'kind',media_type,'date',created_at::date)
			ORDER BY created_at DESC,id),'[]'::json)
		FROM (SELECT * FROM matches ORDER BY created_at DESC,id LIMIT ` + args.add(limit) + `) page`
	var page []byte
	if err := tx.QueryRow(ctx, query, args...).Scan(&out.Total, &page); err != nil {
		return out, dbError(err)
	}
	if err := json.Unmarshal(page, &out.Items); err != nil {
		return out, ErrDenied
	}
	for i := range out.Items {
		out.Items[i].Kind = sourceKind(out.Items[i].Kind)
	}
	return out, nil
}

func sourceKind(media string) string {
	switch {
	case media == "application/pdf":
		return "PDF"
	case media == "text/csv":
		return "CSV"
	case strings.HasPrefix(media, "image/"):
		return "Image"
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
