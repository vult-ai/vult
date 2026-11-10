package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Closing a period is a decision, not a permanent lock: who closed or reopened
// it, why, and what was still open is kept as an append-only history. Posting
// into a period whose latest decision is a close is refused.

type ClosePeriod struct {
	RequestKey string `json:"request_key"`
	From       string `json:"from"`
	Through    string `json:"through"`
	// Reason is required when the review still lists open items.
	Reason string `json:"reason,omitempty"`
}

type ReopenPeriod struct {
	RequestKey       string `json:"request_key"`
	PeriodID         string `json:"period_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason"`
}

type PeriodReviewQuery struct {
	From    string `json:"from"`
	Through string `json:"through"`
}

type StatementReviewRef struct {
	StatementID string `json:"statement_id"`
	Revision    int64  `json:"revision"`
}

type PeriodEvent struct {
	Revision         int64                `json:"revision"`
	Event            string               `json:"event"` // closed or reopened
	ActorID          string               `json:"actor_id,omitempty"`
	Reason           string               `json:"reason,omitempty"`
	OpenItems        map[string]int64     `json:"open_items,omitempty"`
	StatementReviews []StatementReviewRef `json:"statement_reviews"`
	ReportSHA256     string               `json:"report_sha256,omitempty"`
	// BeforeFoundations marks a close kept from before closes were decisions:
	// it names no actor or reason.
	BeforeFoundations bool      `json:"closed_before_decisions,omitempty"`
	RecordedAt        time.Time `json:"recorded_at"`
}

// AccountingPeriod is a closed or reopened date range, both ends included.
type AccountingPeriod struct {
	ID       string        `json:"id"`
	From     string        `json:"from"`
	Through  string        `json:"through"`
	Status   string        `json:"status"` // closed or reopened
	Revision int64         `json:"revision"`
	ClosedAt *time.Time    `json:"closed_at,omitempty"`
	Events   []PeriodEvent `json:"events"`
}

type PeriodEntry struct {
	EntryID        string `json:"entry_id"`
	AccountingDate string `json:"accounting_date"`
	Description    string `json:"description"`
}

// PeriodReview is what Vult shows before a close: what isn't reconciled,
// evidenced or attributed in the range. Open items don't block a close; they
// need the owner and a reason.
type PeriodReview struct {
	From                   string             `json:"from"`
	Through                string             `json:"through"`
	OpenItems              map[string]int64   `json:"open_items"`
	Clean                  bool               `json:"clean"`
	UnreconciledStatements []Statement        `json:"unreconciled_statements"`
	EntriesWithoutEvidence []PeriodEntry      `json:"entries_without_evidence"`
	EntriesWithoutPayee    []PeriodEntry      `json:"entries_without_payee"`
	Periods                []AccountingPeriod `json:"periods"`
}

// PeriodNote is shown on every report that includes a period whose close or
// reopening carries a reason the reader needs.
type PeriodNote struct {
	PeriodID string `json:"period_id"`
	From     string `json:"from"`
	Through  string `json:"through"`
	Kind     string `json:"kind"` // reopened or closed_with_open_items
	Reason   string `json:"reason"`
	At       string `json:"at"`
}

const periodListLimit = 1000
const periodReviewListLimit = 200
const periodStatementReviewLimit = 256

func validPeriodChange(method string, input []byte) (any, string, error) {
	switch method {
	case "close_period":
		var q ClosePeriod
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidDate(q.From) || !ValidDate(q.Through) || q.From > q.Through || len(q.Reason) > 4000 {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "reopen_period":
		var q ReopenPeriod
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.PeriodID) || q.ExpectedRevision < 1 || strings.TrimSpace(q.Reason) == "" || len(q.Reason) > 4000 {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	}
	return nil, "", ErrInvalid
}

func periodWrite(ctx context.Context, tx pgx.Tx, s Scope, method string, input []byte, admit func(context.Context, pgx.Tx, Scope, string, json.RawMessage) error) (json.RawMessage, error) {
	q, key, err := validPeriodChange(method, input)
	if err != nil {
		return nil, err
	}
	return write(ctx, tx, s, method, key, raw(q), admit, func(receipt string) (json.RawMessage, error) {
		switch q := q.(type) {
		case ClosePeriod:
			return closePeriod(ctx, tx, s, q, receipt)
		case ReopenPeriod:
			return reopenPeriod(ctx, tx, s, q, receipt)
		}
		return nil, ErrInvalid
	})
}

// periodError keeps the reason of a refused close or reopen.
func periodError(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && (p.Code == "23514" || p.Code == "42501" && strings.HasPrefix(p.Message, "only the owner")) {
		return &RefusedError{Reason: refusalReason(p.Message)}
	}
	return err
}

func closePeriod(ctx context.Context, tx pgx.Tx, s Scope, q ClosePeriod, receipt string) (json.RawMessage, error) {
	review, err := readPeriodReview(ctx, tx, s, q.From, q.Through)
	if err != nil {
		return nil, err
	}
	// The close cites the latest review of each statement in the period.
	refs := []StatementReviewRef{}
	rows, err := tx.Query(ctx, `SELECT s.id::text,max(v.revision) FROM public.statements s JOIN public.statement_reviews v
 ON v.tenant_id=s.tenant_id AND v.book_id=s.book_id AND v.statement_id=s.id
 WHERE s.tenant_id=$1 AND s.book_id=$2 AND s.period_start<=$4::date AND s.period_end>=$3::date GROUP BY s.id ORDER BY s.id LIMIT $5`, s.TenantID, s.BookID, q.From, q.Through, periodStatementReviewLimit+1)
	if err != nil {
		return nil, dbError(err)
	}
	for rows.Next() {
		var ref StatementReviewRef
		if err := rows.Scan(&ref.StatementID, &ref.Revision); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		refs = append(refs, ref)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	if len(refs) > periodStatementReviewLimit {
		return nil, ErrLimit
	}
	var period string
	var revision int64
	var event string
	err = tx.QueryRow(ctx, `SELECT p.id::text,e.revision,e.event FROM public.accounting_periods p JOIN LATERAL(SELECT revision,event FROM public.accounting_period_events e
 WHERE e.tenant_id=p.tenant_id AND e.book_id=p.book_id AND e.period_id=p.id ORDER BY revision DESC LIMIT 1) e ON true
 WHERE p.tenant_id=$1 AND p.book_id=$2 AND p.start_date=$3 AND p.end_date=$4`, s.TenantID, s.BookID, q.From, q.Through).Scan(&period, &revision, &event)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		period, revision = ID(), 0
		if _, err := tx.Exec(ctx, "INSERT INTO public.accounting_periods(tenant_id,book_id,id,start_date,end_date) VALUES($1,$2,$3,$4,$5)", s.TenantID, s.BookID, period, q.From, q.Through); err != nil {
			return nil, periodError(err)
		}
	case err != nil:
		return nil, dbError(err)
	case event == "closed":
		return nil, &RefusedError{Reason: "This period is already closed."}
	}
	report := raw(review)
	if _, err := tx.Exec(ctx, `INSERT INTO public.accounting_period_events(tenant_id,book_id,period_id,revision,event,origin,actor_id,reason,statement_reviews,report_bytes,receipt_id)
 VALUES($1,$2,$3,$4,'closed','decision',$5,$6,$7,$8,$9)`, s.TenantID, s.BookID, period, revision+1, s.ActorID, q.Reason, raw(refs), []byte(report), receipt); err != nil {
		return nil, periodError(err)
	}
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS accounting_period_final IMMEDIATE"); err != nil {
		return nil, periodError(err)
	}
	p, err := readPeriod(ctx, tx, s, period)
	if err != nil {
		return nil, err
	}
	return raw(p), nil
}

func reopenPeriod(ctx context.Context, tx pgx.Tx, s Scope, q ReopenPeriod, receipt string) (json.RawMessage, error) {
	p, err := readPeriod(ctx, tx, s, q.PeriodID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrInvalid
		}
		return nil, err
	}
	if p.Revision != q.ExpectedRevision {
		return nil, ErrConflict
	}
	if p.Status != "closed" {
		return nil, &RefusedError{Reason: "This period is already open."}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.accounting_period_events(tenant_id,book_id,period_id,revision,event,origin,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,'reopened','decision',$5,$6,$7)`, s.TenantID, s.BookID, q.PeriodID, p.Revision+1, s.ActorID, q.Reason, receipt); err != nil {
		return nil, periodError(err)
	}
	p, err = readPeriod(ctx, tx, s, q.PeriodID)
	if err != nil {
		return nil, err
	}
	return raw(p), nil
}

const periodEvents = `coalesce((SELECT jsonb_agg(jsonb_build_object('revision',e.revision,'event',e.event,'origin',e.origin,'actor_id',e.actor_id,'reason',nullif(e.reason,''),
 'open_items',e.open_items,'statement_reviews',e.statement_reviews,'report_sha256',e.report_sha256,'recorded_at',e.recorded_at) ORDER BY e.revision)
 FROM public.accounting_period_events e WHERE e.tenant_id=p.tenant_id AND e.book_id=p.book_id AND e.period_id=p.id),'[]'::jsonb)`

func scanPeriod(row pgx.Row) (AccountingPeriod, error) {
	var p AccountingPeriod
	var events []byte
	if err := row.Scan(&p.ID, &p.From, &p.Through, &events); err != nil {
		return p, err
	}
	var list []struct {
		PeriodEvent
		Origin    string           `json:"origin"`
		ActorID   *string          `json:"actor_id"`
		OpenItems map[string]int64 `json:"open_items"`
	}
	if json.Unmarshal(events, &list) != nil {
		return p, ErrInvalid
	}
	p.Events = []PeriodEvent{}
	for _, e := range list {
		ev := e.PeriodEvent
		if e.ActorID != nil {
			ev.ActorID = *e.ActorID
		}
		ev.BeforeFoundations = e.Origin == "closed_period"
		if len(e.OpenItems) > 0 {
			ev.OpenItems = e.OpenItems
		}
		if ev.StatementReviews == nil {
			ev.StatementReviews = []StatementReviewRef{}
		}
		p.Events = append(p.Events, ev)
		p.Revision, p.Status = ev.Revision, ev.Event
		if ev.Event == "closed" {
			at := ev.RecordedAt
			p.ClosedAt = &at
		}
	}
	return p, nil
}

func readPeriod(ctx context.Context, tx pgx.Tx, s Scope, id string) (AccountingPeriod, error) {
	p, err := scanPeriod(tx.QueryRow(ctx, "SELECT p.id::text,p.start_date::text,p.end_date::text,"+periodEvents+" FROM public.accounting_periods p WHERE p.tenant_id=$1 AND p.book_id=$2 AND p.id=$3", s.TenantID, s.BookID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, dbError(err)
}

// readPeriods lists periods, oldest first; with a range, those overlapping it.
func readPeriods(ctx context.Context, tx pgx.Tx, s Scope, from, through string) ([]AccountingPeriod, error) {
	rows, err := tx.Query(ctx, "SELECT p.id::text,p.start_date::text,p.end_date::text,"+periodEvents+` FROM public.accounting_periods p
 WHERE p.tenant_id=$1 AND p.book_id=$2 AND ($3='' OR p.end_date>=$3::date) AND ($4='' OR p.start_date<=$4::date)
 ORDER BY p.start_date,p.id LIMIT $5`, s.TenantID, s.BookID, from, through, periodListLimit+1)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []AccountingPeriod{}
	for rows.Next() {
		p, err := scanPeriod(rows)
		if err != nil {
			return nil, dbError(err)
		}
		out = append(out, p)
		if len(out) > periodListLimit {
			return nil, ErrLimit
		}
	}
	return out, dbError(rows.Err())
}

func periodEntries(ctx context.Context, tx pgx.Tx, s Scope, query string, from, through string) ([]PeriodEntry, error) {
	rows, err := tx.Query(ctx, query, s.TenantID, s.BookID, from, through, periodReviewListLimit)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []PeriodEntry{}
	for rows.Next() {
		var e PeriodEntry
		if err := rows.Scan(&e.EntryID, &e.AccountingDate, &e.Description); err != nil {
			return nil, dbError(err)
		}
		out = append(out, e)
	}
	return out, dbError(rows.Err())
}

func readPeriodReview(ctx context.Context, tx pgx.Tx, s Scope, from, through string) (PeriodReview, error) {
	out := PeriodReview{From: from, Through: through, UnreconciledStatements: []Statement{}}
	var items []byte
	if err := tx.QueryRow(ctx, "SELECT public.period_open_items($1,$2,$3::date,$4::date)", s.TenantID, s.BookID, from, through).Scan(&items); err != nil {
		return out, dbError(err)
	}
	if json.Unmarshal(items, &out.OpenItems) != nil {
		return out, ErrInvalid
	}
	out.Clean = true
	for _, n := range out.OpenItems {
		if n > 0 {
			out.Clean = false
		}
	}
	statements, err := tx.Query(ctx, "SELECT "+statementColumns+statementFrom+` WHERE s.tenant_id=$1 AND s.book_id=$2 AND s.period_start<=$4::date AND s.period_end>=$3::date
	 AND NOT public.statement_current_reconciled(s.tenant_id,s.book_id,s.id) ORDER BY s.period_end,s.id LIMIT 256`, s.TenantID, s.BookID, from, through)
	if err != nil {
		return out, dbError(err)
	}
	for statements.Next() {
		st, err := scanStatement(statements)
		if err != nil {
			statements.Close()
			return out, dbError(err)
		}
		out.UnreconciledStatements = append(out.UnreconciledStatements, st)
	}
	statements.Close()
	if err := statements.Err(); err != nil {
		return out, dbError(err)
	}
	if out.EntriesWithoutEvidence, err = periodEntries(ctx, tx, s, `SELECT e.id::text,e.accounting_date::text,e.description FROM public.entries e
 WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.accounting_date BETWEEN $3::date AND $4::date
 AND public.period_review_entry(e.tenant_id,e.book_id,e.id)
 AND NOT EXISTS(SELECT 1 FROM public.source_citations c WHERE c.tenant_id=e.tenant_id AND c.book_id=e.book_id AND c.entry_id=e.id)
 AND NOT EXISTS(SELECT 1 FROM public.evidence_links l WHERE l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id AND l.action='attach'
   AND NOT EXISTS(SELECT 1 FROM public.evidence_links later WHERE later.tenant_id=l.tenant_id AND later.book_id=l.book_id AND later.link_id=l.link_id AND later.revision>l.revision))
 ORDER BY e.accounting_date,e.sequence LIMIT $5`, from, through); err != nil {
		return out, err
	}
	if out.EntriesWithoutPayee, err = periodEntries(ctx, tx, s, `SELECT e.id::text,e.accounting_date::text,e.description FROM public.entries e
 WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.accounting_date BETWEEN $3::date AND $4::date
 AND public.period_review_entry(e.tenant_id,e.book_id,e.id)
 AND NOT EXISTS(SELECT 1 FROM public.entry_payees p WHERE p.tenant_id=e.tenant_id AND p.book_id=e.book_id AND p.entry_id=e.id)
 ORDER BY e.accounting_date,e.sequence LIMIT $5`, from, through); err != nil {
		return out, err
	}
	out.Periods, err = readPeriods(ctx, tx, s, from, through)
	return out, err
}

// periodNotes are the reasons a report covering from..through must show: a
// reopening, or a close made with open review items.
func periodNotes(ctx context.Context, tx pgx.Tx, s Scope, from, through string) ([]PeriodNote, error) {
	periods, err := readPeriods(ctx, tx, s, from, through)
	if err != nil {
		return nil, err
	}
	out := []PeriodNote{}
	for _, p := range periods {
		for _, e := range p.Events {
			open := int64(0)
			for _, n := range e.OpenItems {
				open += n
			}
			switch {
			case e.Event == "reopened":
				out = append(out, PeriodNote{PeriodID: p.ID, From: p.From, Through: p.Through, Kind: "reopened", Reason: e.Reason, At: e.RecordedAt.UTC().Format(time.RFC3339)})
			case e.Event == "closed" && open > 0:
				out = append(out, PeriodNote{PeriodID: p.ID, From: p.From, Through: p.Through, Kind: "closed_with_open_items", Reason: e.Reason, At: e.RecordedAt.UTC().Format(time.RFC3339)})
			}
		}
	}
	return out, nil
}

// Periods lists the Book's accounting periods for the owner.
func (a TenantAdmin) Periods(ctx context.Context, book string) ([]AccountingPeriod, error) {
	var out []AccountingPeriod
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		var err error
		out, err = readPeriods(ctx, tx, scope, "", "")
		return err
	})
	return out, err
}

// PeriodReview is the review the owner sees before closing from..through.
func (a TenantAdmin) PeriodReview(ctx context.Context, book, from, through string) (PeriodReview, error) {
	if !ValidDate(from) || !ValidDate(through) || from > through {
		return PeriodReview{}, ErrInvalid
	}
	var out PeriodReview
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		var err error
		out, err = readPeriodReview(ctx, tx, scope, from, through)
		return err
	})
	return out, err
}
