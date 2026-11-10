package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Payee is the permanent identity of a person or organization the Book deals
// with, and its current display revision. Names change; identity doesn't.
// Two payees with the same name stay separate.
type Payee struct {
	ID          string    `json:"id"`
	Revision    int64     `json:"revision"`
	DisplayName string    `json:"display_name"`
	Kind        string    `json:"kind"`
	BrandID     string    `json:"brand_id,omitempty"`
	Status      string    `json:"status"`
	RecordedAt  time.Time `json:"recorded_at"`
	// Entries counts entries whose current payee decision names this payee.
	Entries int64 `json:"entries"`
}

type CreatePayee struct {
	RequestKey  string `json:"request_key"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"`
	BrandID     string `json:"brand_id,omitempty"`
}

// RevisePayee records a new name, kind, brand or status. It never changes
// which entries the payee is linked to.
type RevisePayee struct {
	RequestKey       string `json:"request_key"`
	PayeeID          string `json:"payee_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	DisplayName      string `json:"display_name"`
	Kind             string `json:"kind"`
	BrandID          string `json:"brand_id,omitempty"`
	Status           string `json:"status"`
	Reason           string `json:"reason"`
}

// SetEntryPayee records who an entry was with: a payee, or explicitly
// unknown. It is a separate decision from the entry, so correcting it never
// changes the journal. ExpectedRevision is 0 for an entry with no decision yet.
type SetEntryPayee struct {
	RequestKey       string `json:"request_key"`
	EntryID          string `json:"entry_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	PayeeID          string `json:"payee_id,omitempty"`
	Unknown          bool   `json:"unknown,omitempty"`
	Reason           string `json:"reason,omitempty"`
}

// EntryPayee is one recorded decision about an entry's payee.
type EntryPayee struct {
	EntryID     string    `json:"entry_id"`
	Revision    int64     `json:"revision"`
	Counterpart string    `json:"counterpart"`
	PayeeID     string    `json:"payee_id,omitempty"`
	PayeeName   string    `json:"payee_name,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	ActorID     string    `json:"actor_id"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// EntryPayeeRead is an entry's current payee and the decisions behind it.
// Status not_recorded means no decision was ever made, which differs from an
// explicit unknown.
type EntryPayeeRead struct {
	EntryID string       `json:"entry_id"`
	Status  string       `json:"status"`
	Current *EntryPayee  `json:"current,omitempty"`
	History []EntryPayee `json:"history"`
}

var brandPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

func validPayeeName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed != name || len([]rune(name)) > 200 {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validPayee(name, kind, brand string) bool {
	return validPayeeName(name) && (kind == "person" || kind == "organization") && (brand == "" || brandPattern.MatchString(brand))
}

const payeeColumns = `p.id::text,r.revision,r.display_name,r.kind,coalesce(r.brand_id,''),r.status,r.recorded_at`

const payeeFrom = ` FROM public.payees p JOIN LATERAL (SELECT * FROM public.payee_revisions r
 WHERE r.tenant_id=p.tenant_id AND r.book_id=p.book_id AND r.payee_id=p.id ORDER BY r.revision DESC LIMIT 1) r ON true`

func readPayee(ctx context.Context, tx pgx.Tx, s Scope, id string) (Payee, error) {
	var p Payee
	err := tx.QueryRow(ctx, "SELECT "+payeeColumns+payeeFrom+" WHERE p.tenant_id=$1 AND p.book_id=$2 AND p.id=$3", s.TenantID, s.BookID, id).
		Scan(&p.ID, &p.Revision, &p.DisplayName, &p.Kind, &p.BrandID, &p.Status, &p.RecordedAt)
	return p, err
}

func recordPayeeRevision(ctx context.Context, tx pgx.Tx, s Scope, p *Payee, reason, receipt string) error {
	return tx.QueryRow(ctx, `INSERT INTO public.payee_revisions(tenant_id,book_id,payee_id,revision,display_name,kind,brand_id,status,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,$5,$6,nullif($7,''),$8,$9,$10,$11) RETURNING recorded_at`, s.TenantID, s.BookID, p.ID, p.Revision, p.DisplayName, p.Kind, p.BrandID, p.Status, s.ActorID, reason, receipt).Scan(&p.RecordedAt)
}

func createPayee(ctx context.Context, tx pgx.Tx, s Scope, q CreatePayee, receipt string) (json.RawMessage, error) {
	p := Payee{ID: ID(), Revision: 1, DisplayName: q.DisplayName, Kind: q.Kind, BrandID: q.BrandID, Status: "active"}
	if _, err := tx.Exec(ctx, "INSERT INTO public.payees(tenant_id,book_id,id,actor_id,receipt_id) VALUES($1,$2,$3,$4,$5)", s.TenantID, s.BookID, p.ID, s.ActorID, receipt); err != nil {
		return nil, err
	}
	if err := recordPayeeRevision(ctx, tx, s, &p, "", receipt); err != nil {
		return nil, err
	}
	return raw(p), nil
}

func revisePayee(ctx context.Context, tx pgx.Tx, s Scope, q RevisePayee, receipt string) (json.RawMessage, error) {
	p, err := readPayee(ctx, tx, s, q.PayeeID)
	if err != nil {
		return nil, dbError(err)
	}
	if p.Revision != q.ExpectedRevision {
		return nil, ErrConflict
	}
	p.Revision++
	p.DisplayName, p.Kind, p.BrandID, p.Status = q.DisplayName, q.Kind, q.BrandID, q.Status
	if err = recordPayeeRevision(ctx, tx, s, &p, q.Reason, receipt); err != nil {
		return nil, err
	}
	return raw(p), nil
}

func setEntryPayee(ctx context.Context, tx pgx.Tx, s Scope, q SetEntryPayee, receipt string) (json.RawMessage, error) {
	var current int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(revision),0) FROM public.entry_payees WHERE tenant_id=$1 AND book_id=$2 AND entry_id=$3", s.TenantID, s.BookID, q.EntryID).Scan(&current); err != nil {
		return nil, dbError(err)
	}
	if current != q.ExpectedRevision {
		return nil, ErrConflict
	}
	link := EntryPayee{EntryID: q.EntryID, Revision: current + 1, Counterpart: "unknown", Reason: q.Reason, ActorID: s.ActorID}
	if q.PayeeID != "" {
		p, err := readPayee(ctx, tx, s, q.PayeeID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalid
		}
		if err != nil {
			return nil, dbError(err)
		}
		link.Counterpart, link.PayeeID, link.PayeeName = "payee", p.ID, p.DisplayName
	}
	err := tx.QueryRow(ctx, `INSERT INTO public.entry_payees(tenant_id,book_id,entry_id,revision,counterpart,payee_id,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,$5,nullif($6,'')::uuid,$7,$8,$9) RETURNING recorded_at`, s.TenantID, s.BookID, link.EntryID, link.Revision, link.Counterpart, link.PayeeID, s.ActorID, link.Reason, receipt).Scan(&link.RecordedAt)
	if err != nil {
		return nil, err
	}
	return raw(link), nil
}

func validPayeeChange(method string, input []byte) (any, string, error) {
	switch method {
	case "create_payee":
		var q CreatePayee
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !validPayee(q.DisplayName, q.Kind, q.BrandID) {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "revise_payee":
		var q RevisePayee
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.PayeeID) || q.ExpectedRevision < 1 ||
			!validPayee(q.DisplayName, q.Kind, q.BrandID) || (q.Status != "active" && q.Status != "retired") ||
			len(strings.TrimSpace(q.Reason)) < 1 || len(q.Reason) > 4000 {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "set_entry_payee":
		var q SetEntryPayee
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.EntryID) || q.ExpectedRevision < 0 ||
			(q.PayeeID == "") == !q.Unknown || q.PayeeID != "" && !ValidID(q.PayeeID) || len(q.Reason) > 4000 ||
			q.ExpectedRevision > 0 && len(strings.TrimSpace(q.Reason)) < 1 {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	}
	return nil, "", ErrInvalid
}

func payeeWrite(ctx context.Context, tx pgx.Tx, s Scope, method string, input []byte, admit func(context.Context, pgx.Tx, Scope, string, json.RawMessage) error) (json.RawMessage, error) {
	q, key, err := validPayeeChange(method, input)
	if err != nil {
		return nil, err
	}
	return write(ctx, tx, s, method, key, raw(q), admit, func(receipt string) (json.RawMessage, error) {
		switch q := q.(type) {
		case CreatePayee:
			return createPayee(ctx, tx, s, q, receipt)
		case RevisePayee:
			return revisePayee(ctx, tx, s, q, receipt)
		case SetEntryPayee:
			return setEntryPayee(ctx, tx, s, q, receipt)
		}
		return nil, ErrInvalid
	})
}

// payeeLimit bounds one payee list read.
const payeeLimit = 5000

func readPayees(ctx context.Context, tx pgx.Tx, s Scope, status string) ([]Payee, error) {
	rows, err := tx.Query(ctx, "SELECT "+payeeColumns+`,(SELECT count(*) FROM public.entry_payees l WHERE l.tenant_id=p.tenant_id AND l.book_id=p.book_id
 AND l.payee_id=p.id AND l.revision=(SELECT max(revision) FROM public.entry_payees x WHERE x.tenant_id=l.tenant_id AND x.book_id=l.book_id AND x.entry_id=l.entry_id))`+payeeFrom+`
 WHERE p.tenant_id=$1 AND p.book_id=$2 AND ($3='' OR r.status=$3) ORDER BY lower(r.display_name),p.id LIMIT $4`, s.TenantID, s.BookID, status, payeeLimit+1)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []Payee{}
	for rows.Next() {
		var p Payee
		if err := rows.Scan(&p.ID, &p.Revision, &p.DisplayName, &p.Kind, &p.BrandID, &p.Status, &p.RecordedAt, &p.Entries); err != nil {
			return nil, dbError(err)
		}
		out = append(out, p)
		if len(out) > payeeLimit {
			return nil, ErrLimit
		}
	}
	return out, dbError(rows.Err())
}

// entryPayeeHistory selects, for an entry row e, its payee decisions with
// each payee's current name, in revision order, as one JSON array.
const entryPayeeHistory = `coalesce((SELECT jsonb_agg(jsonb_build_object('entry_id',l.entry_id,'revision',l.revision,'counterpart',l.counterpart,
 'payee_id',l.payee_id,'payee_name',(SELECT r.display_name FROM public.payee_revisions r WHERE r.tenant_id=l.tenant_id AND r.book_id=l.book_id
  AND r.payee_id=l.payee_id ORDER BY r.revision DESC LIMIT 1),'reason',nullif(l.reason,''),'actor_id',l.actor_id,'recorded_at',l.recorded_at) ORDER BY l.revision)
 FROM public.entry_payees l WHERE l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id),'[]'::jsonb)`

func entryPayeeFromHistory(entry string, data []byte) (EntryPayeeRead, error) {
	out := EntryPayeeRead{EntryID: entry, Status: "not_recorded", History: []EntryPayee{}}
	if json.Unmarshal(data, &out.History) != nil {
		return out, ErrInvalid
	}
	if n := len(out.History); n > 0 {
		current := out.History[n-1]
		out.Current, out.Status = &current, current.Counterpart
	}
	return out, nil
}

func readEntryPayee(ctx context.Context, tx pgx.Tx, s Scope, entry string) (EntryPayeeRead, error) {
	var data []byte
	err := tx.QueryRow(ctx, "SELECT "+entryPayeeHistory+" FROM public.entries e WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.id=$3", s.TenantID, s.BookID, entry).Scan(&data)
	if err != nil {
		return EntryPayeeRead{}, dbError(err)
	}
	return entryPayeeFromHistory(entry, data)
}

// RecurringCharge is derived, never stored: the same payee charging at a
// regular interval for a similar amount. It changes as soon as the entries or
// payee decisions it rests on change.
type RecurringCharge struct {
	PayeeID            string   `json:"payee_id"`
	PayeeName          string   `json:"payee_name"`
	Cadence            string   `json:"cadence"`
	Occurrences        int      `json:"occurrences"`
	TypicalAmountMinor string   `json:"typical_amount_minor"`
	LastDate           string   `json:"last_date"`
	NextExpected       string   `json:"next_expected"`
	EntryIDs           []string `json:"entry_ids"`
}

type RecurringQuery struct {
	Through string `json:"through,omitempty"`
}

type recurringOccurrence struct {
	entry  string
	date   time.Time
	amount *big.Int
}

var cadences = []struct {
	name       string
	low, high  float64
	minimum    int
	step       func(time.Time) time.Time
	spreadDays float64
}{
	{"weekly", 5, 9, 4, func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }, 2},
	{"monthly", 26, 35, 3, func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }, 5},
	{"quarterly", 84, 98, 3, func(t time.Time) time.Time { return t.AddDate(0, 3, 0) }, 8},
	{"yearly", 350, 380, 2, func(t time.Time) time.Time { return t.AddDate(1, 0, 0) }, 12},
}

// A yearly run needs its previous occurrence (up to 380 days), the next
// interval (up to 380 days), and its currentness allowance (24 + 175 days).
const recurringHistoryDays = 960

// readRecurringCharges finds payees charging the Book at a regular interval
// for a similar amount through the given date. Its bounded history retains
// the occurrences needed by every supported cadence while it is current.
// An entry's amount is what it adds to expense accounts; reversed entries
// and reversals don't count.
func readRecurringCharges(ctx context.Context, tx pgx.Tx, s Scope, through string) ([]RecurringCharge, error) {
	end, err := time.Parse(time.DateOnly, through)
	if err != nil {
		return nil, ErrInvalid
	}
	rows, err := tx.Query(ctx, `WITH current AS (
 SELECT DISTINCT ON (l.entry_id) l.entry_id,l.counterpart,l.payee_id FROM public.entry_payees l
 WHERE l.tenant_id=$1 AND l.book_id=$2 ORDER BY l.entry_id,l.revision DESC
) SELECT c.payee_id::text,r.display_name,e.id::text,e.accounting_date,
 sum(CASE li.side WHEN 'debit' THEN li.amount_minor::numeric ELSE -li.amount_minor::numeric END)::text
 FROM current c JOIN public.entries e ON e.tenant_id=$1 AND e.book_id=$2 AND e.id=c.entry_id
 JOIN public.lines li ON li.tenant_id=e.tenant_id AND li.book_id=e.book_id AND li.entry_id=e.id
 JOIN public.accounts a ON a.tenant_id=li.tenant_id AND a.book_id=li.book_id AND a.id=li.account_id AND a.class='expense'
 JOIN LATERAL (SELECT display_name FROM public.payee_revisions r WHERE r.tenant_id=$1 AND r.book_id=$2 AND r.payee_id=c.payee_id
  ORDER BY r.revision DESC LIMIT 1) r ON true
 WHERE c.counterpart='payee' AND e.reversal_of IS NULL
 AND NOT EXISTS(SELECT 1 FROM public.entries x WHERE x.tenant_id=e.tenant_id AND x.book_id=e.book_id AND x.reversal_of=e.id)
 AND e.accounting_date BETWEEN $3::date-$4::integer AND $3::date
 GROUP BY c.payee_id,r.display_name,e.id,e.accounting_date
 ORDER BY c.payee_id,e.accounting_date,e.id LIMIT 100001`, s.TenantID, s.BookID, through, recurringHistoryDays)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	names := map[string]string{}
	byPayee := map[string][]recurringOccurrence{}
	count := 0
	for rows.Next() {
		var payee, name, entry, amount string
		var date time.Time
		if err := rows.Scan(&payee, &name, &entry, &date, &amount); err != nil {
			return nil, dbError(err)
		}
		if count++; count > 100000 {
			return nil, ErrLimit
		}
		value, _ := new(big.Int).SetString(amount, 10)
		if value.Sign() <= 0 {
			continue
		}
		names[payee] = name
		byPayee[payee] = append(byPayee[payee], recurringOccurrence{entry, date, value})
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	out := []RecurringCharge{}
	for payee, items := range byPayee {
		if charge, ok := recurringCharge(items, end); ok {
			charge.PayeeID, charge.PayeeName = payee, names[payee]
			out = append(out, charge)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PayeeName != out[j].PayeeName {
			return out[i].PayeeName < out[j].PayeeName
		}
		return out[i].PayeeID < out[j].PayeeID
	})
	return out, nil
}

// recurringCharge accepts the most recent run of occurrences whose intervals
// fit one cadence and whose amounts are within a fifth of their median. The
// run must still be current: its next charge is not long overdue.
func recurringCharge(items []recurringOccurrence, end time.Time) (RecurringCharge, bool) {
	for _, cadence := range cadences {
		run := []recurringOccurrence{items[len(items)-1]}
		for i := len(items) - 2; i >= 0; i-- {
			gap := run[0].date.Sub(items[i].date).Hours() / 24
			if gap < cadence.low || gap > cadence.high {
				break
			}
			run = append([]recurringOccurrence{items[i]}, run...)
		}
		if len(run) < cadence.minimum {
			continue
		}
		amounts := make([]*big.Int, len(run))
		for i, item := range run {
			amounts[i] = item.amount
		}
		sort.Slice(amounts, func(i, j int) bool { return amounts[i].Cmp(amounts[j]) < 0 })
		median := amounts[len(amounts)/2]
		similar := true
		for _, a := range amounts {
			difference := new(big.Int).Abs(new(big.Int).Sub(a, median))
			if new(big.Int).Mul(difference, big.NewInt(5)).Cmp(median) > 0 {
				similar = false
				break
			}
		}
		last := run[len(run)-1]
		next := cadence.step(last.date)
		if !similar || end.Sub(next).Hours()/24 > cadence.spreadDays*2+cadence.low/2 {
			continue
		}
		ids := make([]string, len(run))
		for i, item := range run {
			ids[i] = item.entry
		}
		return RecurringCharge{Cadence: cadence.name, Occurrences: len(run), TypicalAmountMinor: median.String(),
			LastDate: last.date.Format(time.DateOnly), NextExpected: next.Format(time.DateOnly), EntryIDs: ids}, true
	}
	return RecurringCharge{}, false
}

// Payees is the owner's payee list.
func (a TenantAdmin) Payees(ctx context.Context, book, status string) ([]Payee, error) {
	if status != "" && status != "active" && status != "retired" {
		return nil, ErrInvalid
	}
	var out []Payee
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		var err error
		out, err = readPayees(ctx, tx, scope, status)
		return err
	})
	return out, err
}
