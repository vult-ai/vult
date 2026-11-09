package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type ActivityFilters struct {
	Search  string `json:"search"`
	Kind    string `json:"kind"`
	From    string `json:"from"`
	To      string `json:"to"`
	Account string `json:"account"`
}

func (q ActivityFilters) validate() error {
	if len(q.Search) > 512 || !utf8.ValidString(q.Search) || strings.ContainsRune(q.Search, 0) || q.From != "" && !ValidDate(q.From) || q.To != "" && !ValidDate(q.To) || q.From != "" && q.To != "" && q.From > q.To || q.Account != "" && !ValidID(q.Account) {
		return ErrInvalid
	}
	switch q.Kind {
	case "", "journal", "reversal", "income", "expense":
		return nil
	default:
		return ErrInvalid
	}
}

type ActivityBucket struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Income   string `json:"income_minor"`
	Expenses string `json:"expenses_minor"`
	Count    int    `json:"count"`
}

type ActivitySummary struct {
	RecordedState
	Version            int              `json:"version"`
	BookID             string           `json:"book_id"`
	Currency           string           `json:"currency"`
	Scale              int              `json:"scale"`
	Filters            ActivityFilters  `json:"filters"`
	Count              int              `json:"count"`
	FirstDate          string           `json:"first_date"`
	LastDate           string           `json:"last_date"`
	Income             string           `json:"income_minor"`
	Expenses           string           `json:"expenses_minor"`
	Net                string           `json:"net_minor"`
	Unit               string           `json:"unit"`
	Series             []ActivityBucket `json:"series"`
	Categories         []ActivityBucket `json:"categories"`
	Counterparties     []ActivityBucket `json:"counterparties"`
	CounterpartyStatus string           `json:"counterparty_status"`
	Revision           string           `json:"revision"`
	Basis              string           `json:"basis"`
	SourceCoverage     string           `json:"source_coverage"`
}

type ActivityPageQuery struct {
	ActivityFilters
	Limit    int
	Offset   int
	Revision string
}

type ActivityEntry struct {
	ID                string    `json:"id"`
	BookID            string    `json:"book_id"`
	Sequence          int64     `json:"sequence"`
	Kind              string    `json:"kind"`
	Date              string    `json:"date"`
	EconomicDate      string    `json:"economic_date"`
	AccountingDate    string    `json:"accounting_date"`
	RecordedAt        time.Time `json:"recorded_at"`
	Description       string    `json:"description"`
	Currency          string    `json:"currency"`
	Scale             int       `json:"scale"`
	AmountMinor       string    `json:"amount_minor"`
	AmountRole        string    `json:"amount_role"`
	HasNative         bool      `json:"has_native_measurements"`
	MeasurementStatus string    `json:"measurement_status"`
	ReversalOf        string    `json:"reversal_of,omitempty"`
	ReversedBy        string    `json:"reversed_by,omitempty"`
	// OriginalAmount is the amount in another currency the entry records:
	// the charged amount a statement shows, or a foreign account's movement.
	OriginalAmount *CurrencyAmount `json:"original_amount,omitempty"`
}

type ActivityAccount struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	GLCode   string `json:"gl_code,omitempty"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Role     string `json:"role,omitempty"`
	Revision int64  `json:"revision"`
}

type ActivityPage struct {
	RecordedState
	Version        int               `json:"version"`
	ReadView       string            `json:"read_view"`
	Book           Book              `json:"book"`
	Currencies     []Currency        `json:"currencies"`
	Accounts       []ActivityAccount `json:"accounts"`
	Transactions   []ActivityEntry   `json:"transactions"`
	Summary        ActivityTotals    `json:"summary"`
	Coverage       ActivityCoverage  `json:"coverage"`
	Revision       string            `json:"revision"`
	SourceCoverage string            `json:"source_coverage"`
	// Counts are the entries each Activity tab would show under the same
	// dates, search and account, keyed "" (all), "income" and "expense".
	Counts map[string]int `json:"transaction_counts"`
}

type ActivityTotals struct {
	Income   string `json:"income_minor"`
	Expenses string `json:"expenses_minor"`
	Net      string `json:"net_minor"`
}

type ActivityCoverage struct {
	Count      int    `json:"count"`
	Offset     int    `json:"offset"`
	Limit      int    `json:"limit"`
	HasMore    bool   `json:"has_more"`
	NextOffset int    `json:"next_offset"`
	Complete   bool   `json:"complete"`
	Basis      string `json:"basis"`
}

const activitySelectionSQL = `WITH matched AS MATERIALIZED (
 SELECT e.id,e.sequence,e.accounting_date,r.id reversed_by FROM public.entries e
 LEFT JOIN public.entries r ON r.tenant_id=e.tenant_id AND r.book_id=e.book_id AND r.reversal_of=e.id AND r.recorded_at<=$8::timestamptz
 WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.recorded_at<=$8::timestamptz
 AND ($5='' OR e.accounting_date>=nullif($5,'')::date)
 AND ($6='' OR e.accounting_date<=nullif($6,'')::date)
 AND ($4='' OR ($4='journal' AND e.reversal_of IS NULL) OR ($4='reversal' AND e.reversal_of IS NOT NULL)
   OR ($4 IN('income','expense') AND EXISTS(SELECT 1 FROM public.lines l JOIN public.accounts a
     ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id
     WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=e.id AND a.class=$4)))
 AND ($7='' OR EXISTS(SELECT 1 FROM public.lines l WHERE l.tenant_id=$1 AND l.book_id=$2
   AND l.entry_id=e.id AND l.account_id=nullif($7,'')::uuid))
 AND ($3='' OR e.id IN (` + activitySearchMatches + `))
 ORDER BY e.sequence LIMIT 100001
 ), selected_lines AS MATERIALIZED (
 SELECT e.id entry_id,e.accounting_date,l.id,l.account_id,l.side,l.amount_minor,a.class,a.name
 FROM matched e JOIN public.lines l ON l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=e.id
 LEFT JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id
 LIMIT 100001
 ) `

// activitySearchMatches stands in for the Book search's matching entries
// (SearchFilter); activitySelect replaces it. The list, its summary and the
// finder therefore share one population predicate.
const activitySearchMatches = "SELECT NULL::uuid WHERE false"

type activitySelection struct {
	state                 readState
	count, lines, missing int
	first, last           string
	income, expenses      string
	ids                   []string
	cutoff                time.Time
	revision              string
	unit                  string
	population            string
	series, categories    []ActivityBucket
}

func activitySelect(ctx context.Context, tx pgx.Tx, scope Scope, book Book, q ActivityFilters, limit, offset int, summary bool) (activitySelection, error) {
	var out activitySelection
	state, err := captureReadState(ctx, tx, scope, book, recordedReadScope{})
	if err != nil {
		return out, err
	}
	out.state, out.cutoff = state, state.cutoff
	if q.Account != "" {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 AND id=$3)", scope.TenantID, scope.BookID, q.Account).Scan(&exists); err != nil {
			return out, dbError(err)
		}
		if !exists {
			return out, ErrNotFound
		}
	}
	// Book search: the same rules as the finder. A query of only punctuation
	// filters nothing.
	search, selection := "", activitySelectionSQL
	searchArgs := []any{}
	if strings.TrimSpace(q.Search) != "" {
		filter, extra, ok, err := SearchFilter(ctx, tx, scope, q.Search, BookToday(book, out.cutoff), 11)
		if errors.Is(err, ErrQueryTooLong) {
			return out, ErrInvalid
		}
		if err != nil {
			return out, err
		}
		if ok {
			search, searchArgs = q.Search, extra
			selection = strings.Replace(selection, activitySearchMatches, filter, 1)
		}
	}
	args := append([]any{scope.TenantID, scope.BookID, search, q.Kind, q.From, q.To, q.Account, out.cutoff, limit, offset, summary}, searchArgs...)
	var series, categories []byte
	err = tx.QueryRow(ctx, selection+`, stats AS (
 SELECT count(*) count,min(accounting_date) first_date,max(accounting_date) last_date,
 encode(sha256(convert_to(coalesce(string_agg(id::text||':'||coalesce(reversed_by::text,''),',' ORDER BY sequence),''),'UTF8')),'hex') population FROM matched
 ), amounts AS (
 SELECT count(*) line_count,count(*) FILTER(WHERE class IS NULL) missing,
 coalesce(sum(CASE WHEN class='income' THEN CASE side WHEN 'credit' THEN amount_minor::numeric ELSE -amount_minor::numeric END ELSE 0 END),0) income,
 coalesce(sum(CASE WHEN class='expense' THEN CASE side WHEN 'debit' THEN amount_minor::numeric ELSE -amount_minor::numeric END ELSE 0 END),0) expenses
 FROM selected_lines
 ), date_window AS (
 SELECT coalesce(nullif($5,'')::date,first_date) first_date,coalesce(nullif($6,'')::date,last_date) last_date FROM stats
 ), grain AS (
 SELECT CASE WHEN (extract(year FROM last_date)-extract(year FROM first_date))*12
   +extract(month FROM last_date)-extract(month FROM first_date)>120 THEN 'year' ELSE 'month' END unit FROM date_window
 ), series AS (
 SELECT to_char(accounting_date,CASE WHEN unit='year' THEN 'YYYY' ELSE 'YYYY-MM' END) key,
 sum(CASE WHEN class='income' THEN CASE side WHEN 'credit' THEN amount_minor::numeric ELSE -amount_minor::numeric END ELSE 0 END)::text income_minor,
 sum(CASE WHEN class='expense' THEN CASE side WHEN 'debit' THEN amount_minor::numeric ELSE -amount_minor::numeric END ELSE 0 END)::text expenses_minor,
 count(DISTINCT entry_id) count FROM selected_lines CROSS JOIN grain WHERE $11 GROUP BY key
 ), categories AS (
 SELECT account_id::text key,coalesce(nullif(name,''),account_id::text) label,
 sum(CASE WHEN class='income' THEN CASE side WHEN 'credit' THEN amount_minor::numeric ELSE -amount_minor::numeric END ELSE 0 END)::text income_minor,
 sum(CASE WHEN class='expense' THEN CASE side WHEN 'debit' THEN amount_minor::numeric ELSE -amount_minor::numeric END ELSE 0 END)::text expenses_minor,
 count(DISTINCT entry_id) count FROM selected_lines WHERE $11 AND class IN('income','expense') GROUP BY account_id,name
 ), ranked AS (
 SELECT * FROM categories ORDER BY greatest(abs(income_minor::numeric),abs(expenses_minor::numeric)) DESC,key LIMIT 5
 ) SELECT stats.count,coalesce(stats.first_date::text,''),coalesce(stats.last_date::text,''),amounts.line_count,amounts.missing,
 amounts.income::text,amounts.expenses::text,stats.population,
 ARRAY(SELECT id::text FROM matched ORDER BY accounting_date DESC,sequence DESC LIMIT $9 OFFSET $10),grain.unit,
 coalesce((SELECT jsonb_agg(jsonb_build_object('key',key,'label',key,'income_minor',income_minor,'expenses_minor',expenses_minor,'count',count) ORDER BY key) FROM series),'[]'::jsonb),
 coalesce((SELECT jsonb_agg(to_jsonb(ranked) ORDER BY greatest(abs(income_minor::numeric),abs(expenses_minor::numeric)) DESC,key) FROM ranked),'[]'::jsonb)
 FROM stats CROSS JOIN amounts CROSS JOIN grain`, args...).Scan(&out.count, &out.first, &out.last, &out.lines, &out.missing, &out.income, &out.expenses, &out.population, &out.ids, &out.unit, &series, &categories)
	if err != nil {
		return out, dbError(err)
	}
	if out.count > 100000 || out.lines > 100000 {
		return out, ErrLimit
	}
	if out.missing != 0 {
		return out, ErrDenied
	}
	if json.Unmarshal(series, &out.series) != nil || json.Unmarshal(categories, &out.categories) != nil {
		return out, ErrDenied
	}
	digest := sha256.Sum256(raw(struct {
		Tenant, Book, Population string
		Epoch                    int64
		Filters                  ActivityFilters
	}{scope.TenantID, scope.BookID, out.population, state.epoch, q}))
	out.revision = hex.EncodeToString(digest[:])
	return out, nil
}

func (a TenantAdmin) Activity(ctx context.Context, book string, q ActivityPageQuery) (ActivityPage, error) {
	if err := q.ActivityFilters.validate(); err != nil || q.Limit < 1 || q.Limit > 100 || q.Offset < 0 || q.Offset > 100000 || !validViewRevision(q.Revision) {
		return ActivityPage{}, ErrInvalid
	}
	var out ActivityPage
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, header Book) error {
		selected, err := activitySelect(ctx, tx, scope, header, q.ActivityFilters, q.Limit, q.Offset, false)
		if err != nil {
			return err
		}
		if q.Revision != "" && q.Revision != selected.revision {
			return ErrConflict
		}
		net, err := subtractMinor(selected.income, selected.expenses)
		if err != nil {
			return err
		}
		out = ActivityPage{RecordedState: selected.state.RecordedState, Version: 1, ReadView: "transactions", Book: header, Currencies: Currencies(), Accounts: []ActivityAccount{}, Transactions: []ActivityEntry{}, Summary: ActivityTotals{Income: selected.income, Expenses: selected.expenses, Net: net}, Revision: selected.revision, SourceCoverage: "unknown",
			Coverage: ActivityCoverage{Count: selected.count, Offset: q.Offset, Limit: q.Limit, HasMore: q.Offset+len(selected.ids) < selected.count, NextOffset: q.Offset + len(selected.ids), Complete: q.Offset == 0 && len(selected.ids) == selected.count, Basis: "filtered_recorded_journal_entries"}}
		out.Counts = map[string]int{}
		for _, kind := range []string{"", "income", "expense"} {
			if kind == q.Kind {
				out.Counts[kind] = selected.count
				continue
			}
			other := q.ActivityFilters
			other.Kind = kind
			tab, err := activitySelect(ctx, tx, scope, header, other, 1, 0, false)
			if err != nil {
				return err
			}
			out.Counts[kind] = tab.count
		}
		accounts, err := readScopedCatalog(ctx, tx, scope)
		if err != nil {
			return err
		}
		for _, account := range accounts {
			out.Accounts = append(out.Accounts, activityAccount(account))
		}
		if len(selected.ids) == 0 {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT e.id::text,e.sequence,e.economic_date::text,e.accounting_date::text,e.recorded_at,e.description,
 coalesce(e.reversal_of::text,''),coalesce(r.id::text,''),m.amount,m.native,
 coalesce((SELECT c->'amount' FROM public.lines l, jsonb_array_elements(coalesce(l.contexts,'[]'::jsonb)) c
   WHERE l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id AND c->>'role'='transaction_amount' AND c->'amount'->>'currency'<>$5
   ORDER BY l.id LIMIT 1),
  (SELECT jsonb_build_object('currency',l.native_currency,'scale',l.native_scale,'amount_minor',l.native_amount_minor::text) FROM public.lines l
   WHERE l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id AND l.native_currency IS NOT NULL AND l.native_currency<>$5
   ORDER BY l.id LIMIT 1))
 FROM public.entries e LEFT JOIN public.entries r ON r.tenant_id=e.tenant_id AND r.book_id=e.book_id AND r.reversal_of=e.id AND r.recorded_at<=$4::timestamptz
 CROSS JOIN LATERAL(SELECT coalesce(sum(l.amount_minor::numeric) FILTER(WHERE l.side='debit'),0)::text amount,
   coalesce(bool_or(l.native_amount_minor IS NOT NULL),false) native FROM public.lines l
   WHERE l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id) m
 WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.id=ANY($3::uuid[]) ORDER BY e.accounting_date DESC,e.sequence DESC`, scope.TenantID, scope.BookID, selected.ids, selected.cutoff, header.Currency)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			entry := ActivityEntry{BookID: book, Currency: header.Currency, Scale: header.Scale, Kind: "journal", AmountRole: "book_debit_total", MeasurementStatus: "entry_lines"}
			var original []byte
			if err := rows.Scan(&entry.ID, &entry.Sequence, &entry.EconomicDate, &entry.AccountingDate, &entry.RecordedAt, &entry.Description, &entry.ReversalOf, &entry.ReversedBy, &entry.AmountMinor, &entry.HasNative, &original); err != nil {
				return dbError(err)
			}
			if len(original) > 0 {
				var amount CurrencyAmount
				if json.Unmarshal(original, &amount) == nil && amount.Currency != "" && amount.Scale != nil {
					entry.OriginalAmount = &CurrencyAmount{Currency: amount.Currency, Scale: amount.Scale, AmountMinor: amount.AmountMinor}
				}
			}
			entry.Date = entry.AccountingDate
			if entry.ReversalOf != "" {
				entry.Kind = "reversal"
			}
			out.Transactions = append(out.Transactions, entry)
		}
		if err := rows.Err(); err != nil {
			return dbError(err)
		}
		if len(out.Transactions) != len(selected.ids) {
			return ErrDenied
		}
		return nil
	})
	if err != nil {
		return ActivityPage{}, err
	}
	return out, nil
}

func subtractMinor(a, b string) (string, error) {
	x, ok := new(big.Int).SetString(a, 10)
	y, other := new(big.Int).SetString(b, 10)
	if !ok || !other {
		return "", ErrDenied
	}
	return x.Sub(x, y).String(), nil
}

func (a TenantAdmin) ActivitySummary(ctx context.Context, book string, q ActivityFilters, revision string) (ActivitySummary, error) {
	if err := q.validate(); err != nil || !validViewRevision(revision) {
		return ActivitySummary{}, ErrInvalid
	}
	var out ActivitySummary
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, header Book) error {
		selected, err := activitySelect(ctx, tx, scope, header, q, 0, 0, true)
		if err != nil {
			return err
		}
		if revision != "" && revision != selected.revision {
			return ErrConflict
		}
		net, err := subtractMinor(selected.income, selected.expenses)
		if err != nil {
			return err
		}
		out = ActivitySummary{RecordedState: selected.state.RecordedState, Version: 1, BookID: book, Currency: header.Currency, Scale: header.Scale, Filters: q, Count: selected.count, FirstDate: selected.first, LastDate: selected.last, Income: selected.income, Expenses: selected.expenses, Net: net, Unit: selected.unit, Series: []ActivityBucket{}, Categories: selected.categories, Counterparties: []ActivityBucket{}, CounterpartyStatus: "not_recorded", Revision: selected.revision, Basis: "filtered_recorded_accounting_dates", SourceCoverage: "unknown"}
		if selected.count == 0 {
			return nil
		}
		from, to := q.From, q.To
		if from == "" {
			from = selected.first
		}
		if to == "" {
			to = selected.last
		}
		first, _ := time.Parse(time.DateOnly, from)
		last, _ := time.Parse(time.DateOnly, to)
		buckets := map[string]ActivityBucket{}
		for _, bucket := range selected.series {
			buckets[bucket.Key] = bucket
		}
		for cursor := first; !cursor.After(last); {
			if len(out.Series) == 1000 {
				return ErrLimit
			}
			key := cursor.Format("2006-01")
			if out.Unit == "year" {
				key = cursor.Format("2006")
			}
			bucket, exists := buckets[key]
			if !exists {
				bucket = ActivityBucket{Key: key, Label: key, Income: "0", Expenses: "0"}
			}
			out.Series = append(out.Series, bucket)
			if out.Unit == "year" {
				cursor = time.Date(cursor.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
			} else {
				cursor = time.Date(cursor.Year(), cursor.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			}
		}
		return nil
	})
	if err != nil {
		return ActivitySummary{}, err
	}
	return out, nil
}

func validViewRevision(value string) bool {
	if value == "" {
		return true
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}
