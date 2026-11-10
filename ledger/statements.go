package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Statements are evidence the Book is compared against. They never post. A
// row's amount is signed the way the statement's balance moves: a deposit is
// positive on a cash statement, a charge is positive on a card statement.

type StatementRowInput struct {
	RowNumber         int       `json:"row_number"`
	Location          *Location `json:"location,omitempty"`
	Date              string    `json:"date"`
	AmountMinor       string    `json:"amount_minor"`
	Description       string    `json:"description,omitempty"`
	ExternalID        string    `json:"external_id,omitempty"`
	Lifecycle         string    `json:"lifecycle,omitempty"`
	RelatedExternalID string    `json:"related_external_id,omitempty"`
	// Raw is the row exactly as the statement shows it, such as its CSV line.
	Raw string `json:"raw"`
}

type ImportStatement struct {
	RequestKey    string              `json:"request_key"`
	AccountID     string              `json:"account_id"`
	DocumentID    string              `json:"document_id"`
	PeriodStart   string              `json:"period_start"`
	PeriodEnd     string              `json:"period_end"`
	OpeningMinor  string              `json:"opening_balance_minor"`
	ClosingMinor  string              `json:"closing_balance_minor"`
	EvidenceState string              `json:"evidence_state,omitempty"`
	Note          string              `json:"note,omitempty"`
	Rows          []StatementRowInput `json:"rows"`
}

type MarkStatementDuplicate struct {
	RequestKey     string `json:"request_key"`
	RowID          string `json:"row_id"`
	CanonicalRowID string `json:"canonical_row_id"`
	Reason         string `json:"reason"`
}

// StatementMatchPart is a statement row or a posted line in a match. Amount
// defaults to what remains of it; a split or combined match gives amounts.
type StatementMatchPart struct {
	RowID       string `json:"row_id,omitempty"`
	LineID      string `json:"line_id,omitempty"`
	AmountMinor string `json:"amount_minor,omitempty"`
}

type MatchStatement struct {
	RequestKey string               `json:"request_key"`
	Rows       []StatementMatchPart `json:"rows"`
	Lines      []StatementMatchPart `json:"lines"`
	Reason     string               `json:"reason,omitempty"`
}

type RevokeStatementMatch struct {
	RequestKey string `json:"request_key"`
	MatchID    string `json:"match_id"`
	Reason     string `json:"reason"`
}

type RecordStatementAdjustment struct {
	RequestKey string `json:"request_key"`
	RowID      string `json:"row_id"`
	EntryID    string `json:"entry_id"`
}

type ReviewStatement struct {
	RequestKey       string `json:"request_key"`
	StatementID      string `json:"statement_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Outcome          string `json:"outcome"`
	Reason           string `json:"reason,omitempty"`
}

type StatementQuery struct {
	StatementID string `json:"statement_id"`
}

type StatementListQuery struct {
	AccountID string `json:"account_id,omitempty"`
}

type StatementReview struct {
	Revision     int64     `json:"revision"`
	Outcome      string    `json:"outcome"`
	Reason       string    `json:"reason,omitempty"`
	ReportSHA256 string    `json:"report_sha256"`
	ActorID      string    `json:"actor_id"`
	RecordedAt   time.Time `json:"recorded_at"`
}

type Statement struct {
	ID            string           `json:"id"`
	AccountID     string           `json:"account_id"`
	AccountName   string           `json:"account_name,omitempty"`
	Kind          string           `json:"kind"`
	Currency      string           `json:"currency"`
	Scale         int              `json:"scale"`
	DocumentID    string           `json:"document_id"`
	PeriodStart   string           `json:"period_start"`
	PeriodEnd     string           `json:"period_end"`
	OpeningMinor  string           `json:"opening_balance_minor"`
	ClosingMinor  string           `json:"closing_balance_minor"`
	EvidenceState string           `json:"evidence_state"`
	Note          string           `json:"note,omitempty"`
	ActorID       string           `json:"actor_id"`
	RecordedAt    time.Time        `json:"recorded_at"`
	RowCount      int              `json:"row_count"`
	Review        *StatementReview `json:"review,omitempty"`
	// Status is reconciled only while the latest reconciled review still has
	// complete coverage. Review keeps the immutable historical sign-off.
	Status string `json:"status"`
	// AlreadyRecorded is set when an import found this same statement.
	AlreadyRecorded bool `json:"already_recorded,omitempty"`
}

type StatementRow struct {
	ID                string   `json:"id"`
	RowNumber         int      `json:"row_number"`
	Location          Location `json:"location"`
	Date              string   `json:"date"`
	AmountMinor       string   `json:"amount_minor"`
	Description       string   `json:"description"`
	ExternalID        string   `json:"external_id,omitempty"`
	Lifecycle         string   `json:"lifecycle"`
	RelatedExternalID string   `json:"related_external_id,omitempty"`
	RawSHA256         string   `json:"raw_sha256"`
	// State: matched, adjusted, duplicate, pending, cancelled, proposed (a
	// pending proposal would explain it) or not_in_book. Partly matched rows
	// are not_in_book for the part that remains.
	State             string   `json:"state"`
	MatchedMinor      string   `json:"matched_minor"`
	RemainingMinor    string   `json:"remaining_minor"`
	MatchIDs          []string `json:"match_ids"`
	AdjustmentEntryID string   `json:"adjustment_entry_id,omitempty"`
	DuplicateOf       string   `json:"duplicate_of,omitempty"`
}

// OutstandingLine is posted on the account and not yet on a statement.
// Unresolved lines have no comparable amount or no exact allocation at this
// cutoff within a combined match. They stay visible without an invented remainder.
type OutstandingLine struct {
	LineID         string `json:"line_id"`
	EntryID        string `json:"entry_id"`
	Date           string `json:"date"`
	Description    string `json:"description"`
	AmountMinor    string `json:"amount_minor,omitempty"`
	RemainingMinor string `json:"remaining_minor,omitempty"`
	Unresolved     bool   `json:"unresolved,omitempty"`
	Proposed       bool   `json:"proposed,omitempty"`
}

type StatementMatchRead struct {
	ID         string               `json:"id"`
	Rows       []StatementMatchPart `json:"rows"`
	Lines      []StatementMatchPart `json:"lines"`
	Reason     string               `json:"reason,omitempty"`
	ActorID    string               `json:"actor_id"`
	RecordedAt time.Time            `json:"recorded_at"`
}

// ReconciliationSummary is the statement compared with the Book. It never says
// balanced without a statement behind it: every figure comes from one.
type ReconciliationSummary struct {
	OpeningMinor        string `json:"opening_balance_minor"`
	ClosingMinor        string `json:"closing_balance_minor"`
	MatchedMinor        string `json:"matched_minor"`
	AdjustedMinor       string `json:"adjusted_minor"`
	DuplicateMinor      string `json:"duplicate_minor"`
	NotInBookMinor      string `json:"on_statement_not_in_book_minor"`
	NotInBookCount      int    `json:"on_statement_not_in_book_count"`
	OutstandingMinor    string `json:"outstanding_in_book_minor"`
	OutstandingCount    int    `json:"outstanding_in_book_count"`
	UnresolvedCount     int    `json:"unresolved_lines"`
	DuplicateCount      int    `json:"duplicates"`
	PendingCount        int    `json:"pending_rows"`
	ProposedCount       int    `json:"proposed_rows"`
	StatementDifference string `json:"statement_arithmetic_difference_minor"`
	// Difference is closing − opening − matched − adjusted − duplicate: what the Book
	// does not yet explain. Zero, with every settled row explained, means it
	// can be recorded as reconciled.
	DifferenceMinor string `json:"remaining_difference_minor"`
	Explained       bool   `json:"fully_explained"`
}

type StatementReconciliation struct {
	Statement   Statement             `json:"statement"`
	Summary     ReconciliationSummary `json:"summary"`
	Rows        []StatementRow        `json:"rows"`
	Outstanding []OutstandingLine     `json:"outstanding"`
	Matches     []StatementMatchRead  `json:"matches"`
}

type StatementSuggestion struct {
	Kind        string   `json:"kind"` // match or duplicate
	RowID       string   `json:"row_id"`
	LineIDs     []string `json:"line_ids,omitempty"`
	EntryIDs    []string `json:"entry_ids,omitempty"`
	CanonicalID string   `json:"canonical_row_id,omitempty"`
	AmountMinor string   `json:"amount_minor"`
	Reason      string   `json:"reason"`
}

const statementRowLimit = 5000
const statementLineLimit = 20000

var statementLifecycles = map[string]bool{"settled": true, "pending": true, "refund": true, "chargeback": true, "chargeback_reversal": true, "cancelled": true}

func minorInt(text string) (int64, bool) {
	if text == "" || text == "-0" || strings.HasPrefix(text, "+") || len(text) > 1 && (text[0] == '0' || strings.HasPrefix(text, "-0")) {
		return 0, false
	}
	n, err := strconv.ParseInt(text, 10, 64)
	return n, err == nil
}

func validStatementText(s string, max int) bool {
	return len(s) <= max && strings.ToValidUTF8(s, "") == s
}

func validStatementChange(method string, input []byte) (any, string, error) {
	switch method {
	case "import_statement":
		var q ImportStatement
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.AccountID) || !ValidID(q.DocumentID) ||
			!ValidDate(q.PeriodStart) || !ValidDate(q.PeriodEnd) || q.PeriodStart > q.PeriodEnd || !validStatementText(q.Note, 4000) ||
			len(q.Rows) > statementRowLimit {
			return nil, "", ErrInvalid
		}
		if q.EvidenceState == "" {
			q.EvidenceState = "complete"
		}
		if q.EvidenceState != "complete" && q.EvidenceState != "incomplete" && q.EvidenceState != "disputed" {
			return nil, "", ErrInvalid
		}
		if _, ok := minorInt(q.OpeningMinor); !ok {
			return nil, "", ErrInvalid
		}
		if _, ok := minorInt(q.ClosingMinor); !ok {
			return nil, "", ErrInvalid
		}
		seen := map[int]bool{}
		for i := range q.Rows {
			r := &q.Rows[i]
			if r.Lifecycle == "" {
				r.Lifecycle = "settled"
			}
			if r.Location == nil {
				n := r.RowNumber
				r.Location = &Location{Row: &n}
			}
			if _, ok := minorInt(r.AmountMinor); !ok || r.RowNumber < 1 || r.RowNumber > 1000000 || seen[r.RowNumber] || !ValidDate(r.Date) ||
				!statementLifecycles[r.Lifecycle] || !r.Location.valid() || !validStatementText(r.Description, 4000) ||
				len(r.ExternalID) > 256 || len(r.RelatedExternalID) > 256 || r.Raw == "" || len(r.Raw) > 65536 {
				return nil, "", ErrInvalid
			}
			seen[r.RowNumber] = true
		}
		return q, q.RequestKey, nil
	case "mark_statement_duplicate":
		var q MarkStatementDuplicate
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.RowID) || !ValidID(q.CanonicalRowID) || q.RowID == q.CanonicalRowID ||
			strings.TrimSpace(q.Reason) == "" || len(q.Reason) > 4000 {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "match_statement":
		var q MatchStatement
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || len(q.Rows) < 1 || len(q.Lines) < 1 || len(q.Rows)+len(q.Lines) > 512 || len(q.Reason) > 4000 {
			return nil, "", ErrInvalid
		}
		seen := map[string]bool{}
		for _, p := range q.Rows {
			if !ValidID(p.RowID) || p.LineID != "" || seen[p.RowID] {
				return nil, "", ErrInvalid
			}
			if p.AmountMinor != "" {
				if n, ok := minorInt(p.AmountMinor); !ok || n == 0 {
					return nil, "", ErrInvalid
				}
			}
			seen[p.RowID] = true
		}
		for _, p := range q.Lines {
			if !ValidID(p.LineID) || p.RowID != "" || seen[p.LineID] {
				return nil, "", ErrInvalid
			}
			if p.AmountMinor != "" {
				if n, ok := minorInt(p.AmountMinor); !ok || n == 0 {
					return nil, "", ErrInvalid
				}
			}
			seen[p.LineID] = true
		}
		return q, q.RequestKey, nil
	case "revoke_statement_match":
		var q RevokeStatementMatch
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.MatchID) || strings.TrimSpace(q.Reason) == "" || len(q.Reason) > 4000 {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "record_statement_adjustment":
		var q RecordStatementAdjustment
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.RowID) || !ValidID(q.EntryID) {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "review_statement":
		var q ReviewStatement
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.StatementID) || q.ExpectedRevision < 0 ||
			q.Outcome != "reconciled" && q.Outcome != "needs_work" || len(q.Reason) > 4000 || q.Outcome == "needs_work" && strings.TrimSpace(q.Reason) == "" {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	}
	return nil, "", ErrInvalid
}

func statementMethod(method string) bool {
	switch method {
	case "import_statement", "mark_statement_duplicate", "match_statement", "revoke_statement_match", "record_statement_adjustment", "review_statement":
		return true
	}
	return false
}

func statementWrite(ctx context.Context, tx pgx.Tx, s Scope, method string, input []byte, admit func(context.Context, pgx.Tx, Scope, string, json.RawMessage) error) (json.RawMessage, error) {
	q, key, err := validStatementChange(method, input)
	if err != nil {
		return nil, err
	}
	return write(ctx, tx, s, method, key, raw(q), admit, func(receipt string) (json.RawMessage, error) {
		switch q := q.(type) {
		case ImportStatement:
			return importStatement(ctx, tx, s, q, receipt)
		case MarkStatementDuplicate:
			_, err := tx.Exec(ctx, `INSERT INTO public.statement_duplicates(tenant_id,book_id,row_id,canonical_row_id,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, s.TenantID, s.BookID, q.RowID, q.CanonicalRowID, s.ActorID, q.Reason, receipt)
			if err == nil {
				err = checked(ctx, tx)
			}
			if err != nil {
				return nil, statementError(err)
			}
			return raw(struct {
				RowID          string `json:"row_id"`
				CanonicalRowID string `json:"canonical_row_id"`
				Reason         string `json:"reason"`
			}{q.RowID, q.CanonicalRowID, q.Reason}), nil
		case MatchStatement:
			return matchStatement(ctx, tx, s, q, receipt)
		case RevokeStatementMatch:
			if _, err := tx.Exec(ctx, `INSERT INTO public.statement_match_revocations(tenant_id,book_id,match_id,actor_id,reason,receipt_id) VALUES($1,$2,$3,$4,$5,$6)`,
				s.TenantID, s.BookID, q.MatchID, s.ActorID, q.Reason, receipt); err != nil {
				return nil, statementError(err)
			}
			return raw(struct {
				MatchID string `json:"match_id"`
				Revoked bool   `json:"revoked"`
				Reason  string `json:"reason"`
			}{q.MatchID, true, q.Reason}), nil
		case RecordStatementAdjustment:
			if _, err := tx.Exec(ctx, `INSERT INTO public.statement_adjustments(tenant_id,book_id,row_id,entry_id,actor_id,receipt_id) VALUES($1,$2,$3,$4,$5,$6)`,
				s.TenantID, s.BookID, q.RowID, q.EntryID, s.ActorID, receipt); err != nil {
				return nil, statementError(err)
			}
			if err := checked(ctx, tx); err != nil {
				return nil, err
			}
			return raw(struct {
				RowID   string `json:"row_id"`
				EntryID string `json:"entry_id"`
			}{q.RowID, q.EntryID}), nil
		case ReviewStatement:
			return reviewStatement(ctx, tx, s, q, receipt)
		}
		return nil, ErrInvalid
	})
}

// RefusedError is a statement or period decision the database refused, with
// its reason: rules the agent or owner can act on, such as an unexplained
// statement recorded as reconciled. It is an invalid request.
type RefusedError struct {
	Reason string `json:"reason"`
}

func (e *RefusedError) Error() string { return e.Reason }
func (e *RefusedError) Unwrap() error { return ErrInvalid }

// statementChecks runs a decision's deferred checks now, inside the request,
// so a refusal keeps its reason. Receipt checks stay at commit.
const statementChecks = "SET CONSTRAINTS statement_duplicate_final,statement_match_final,statement_adjustment_final,statement_review_final IMMEDIATE"

func statementError(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) {
		switch p.Code {
		case "23514":
			return &RefusedError{Reason: refusalReason(p.Message)}
		case "23503", "23505":
			return ErrInvalid
		}
	}
	return err
}

// refusalReason turns a database rule into a sentence.
func refusalReason(message string) string {
	if message == "" {
		return "Vult refused this change."
	}
	return strings.ToUpper(message[:1]) + message[1:] + "."
}

// checked records a statement decision's rows, then runs its checks.
func checked(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, statementChecks); err != nil {
		return statementError(err)
	}
	return nil
}

func statementRowsDigest(rows []StatementRowInput) string {
	h := sha256.New()
	for _, r := range rows {
		_, _ = h.Write([]byte(strconv.Itoa(r.RowNumber) + "\x00" + r.Raw + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func importStatement(ctx context.Context, tx pgx.Tx, s Scope, q ImportStatement, receipt string) (json.RawMessage, error) {
	var kind, currency string
	var scale int
	err := tx.QueryRow(ctx, `SELECT coalesce(a.role,''),coalesce(a.currency,b.currency),coalesce(a.native_scale,b.scale)
 FROM public.accounts a JOIN public.books b ON b.tenant_id=a.tenant_id AND b.id=a.book_id
 WHERE a.tenant_id=$1 AND a.book_id=$2 AND a.id=$3`, s.TenantID, s.BookID, q.AccountID).Scan(&kind, &currency, &scale)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalid
	}
	if err != nil {
		return nil, dbError(err)
	}
	if kind != "cash" && kind != "card" {
		return nil, ErrInvalid
	}
	// The same statement imported again adds nothing: same account, document,
	// period, balances and rows.
	digest := statementRowsDigest(q.Rows)
	candidates, err := tx.Query(ctx, `SELECT s.id::text,(SELECT encode(sha256(string_agg(convert_to(r.row_number::text,'UTF8')||'\x00'::bytea||r.raw_bytes||'\x00'::bytea,''::bytea ORDER BY r.row_number)),'hex')
   FROM public.statement_rows r WHERE r.tenant_id=s.tenant_id AND r.book_id=s.book_id AND r.statement_id=s.id)
 FROM public.statements s WHERE s.tenant_id=$1 AND s.book_id=$2 AND s.account_id=$3 AND s.document_id=$4 AND s.period_start=$5 AND s.period_end=$6
 AND s.opening_balance_minor=$7 AND s.closing_balance_minor=$8`, s.TenantID, s.BookID, q.AccountID, q.DocumentID, q.PeriodStart, q.PeriodEnd, q.OpeningMinor, q.ClosingMinor)
	if err != nil {
		return nil, dbError(err)
	}
	var existing string
	for candidates.Next() {
		var id string
		var rowsDigest *string
		if err := candidates.Scan(&id, &rowsDigest); err != nil {
			candidates.Close()
			return nil, dbError(err)
		}
		empty := hex.EncodeToString(sha256.New().Sum(nil))
		if rowsDigest == nil && digest == empty || rowsDigest != nil && *rowsDigest == digest {
			existing = id
		}
	}
	candidates.Close()
	if err := candidates.Err(); err != nil {
		return nil, dbError(err)
	}
	if existing != "" {
		st, err := readStatement(ctx, tx, s, existing)
		if err != nil {
			return nil, err
		}
		st.AlreadyRecorded = true
		return raw(st), nil
	}
	id := ID()
	if _, err := tx.Exec(ctx, `INSERT INTO public.statements(tenant_id,book_id,id,account_id,kind,currency,scale,document_id,period_start,period_end,
 opening_balance_minor,closing_balance_minor,evidence_state,note,actor_id,receipt_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		s.TenantID, s.BookID, id, q.AccountID, kind, currency, scale, q.DocumentID, q.PeriodStart, q.PeriodEnd, q.OpeningMinor, q.ClosingMinor, q.EvidenceState, q.Note, s.ActorID, receipt); err != nil {
		return nil, statementError(err)
	}
	batch := &pgx.Batch{}
	for _, r := range q.Rows {
		location, _ := json.Marshal(r.Location)
		batch.Queue(`INSERT INTO public.statement_rows(tenant_id,book_id,id,statement_id,row_number,location,occurred_on,amount_minor,description,external_id,lifecycle,related_external_id,raw_bytes)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,nullif($10,''),$11,nullif($12,''),$13)`, s.TenantID, s.BookID, ID(), id, r.RowNumber, string(location), r.Date, r.AmountMinor, r.Description, r.ExternalID, r.Lifecycle, r.RelatedExternalID, []byte(r.Raw))
	}
	if batch.Len() > 0 {
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return nil, statementError(err)
		}
	}
	st, err := readStatement(ctx, tx, s, id)
	if err != nil {
		return nil, err
	}
	return raw(st), nil
}

func matchStatement(ctx context.Context, tx pgx.Tx, s Scope, q MatchStatement, receipt string) (json.RawMessage, error) {
	var account, currency string
	var scale int
	if err := tx.QueryRow(ctx, `SELECT s.account_id::text,s.currency,s.scale FROM public.statement_rows r JOIN public.statements s
 ON s.tenant_id=r.tenant_id AND s.book_id=r.book_id AND s.id=r.statement_id WHERE r.tenant_id=$1 AND r.book_id=$2 AND r.id=$3`,
		s.TenantID, s.BookID, q.Rows[0].RowID).Scan(&account, &currency, &scale); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalid
		}
		return nil, dbError(err)
	}
	match := StatementMatchRead{ID: ID(), Rows: []StatementMatchPart{}, Lines: []StatementMatchPart{}, Reason: q.Reason, ActorID: s.ActorID}
	// An omitted amount is what remains of that row or line.
	for _, p := range q.Rows {
		if p.AmountMinor == "" {
			var remaining *int64
			if err := tx.QueryRow(ctx, `SELECT r.amount_minor-coalesce((SELECT sum(p.amount_minor) FROM public.statement_match_parts p WHERE p.tenant_id=r.tenant_id AND p.book_id=r.book_id
   AND p.row_id=r.id AND NOT EXISTS(SELECT 1 FROM public.statement_match_revocations v WHERE v.tenant_id=p.tenant_id AND v.book_id=p.book_id AND v.match_id=p.match_id)),0)
 FROM public.statement_rows r WHERE r.tenant_id=$1 AND r.book_id=$2 AND r.id=$3`, s.TenantID, s.BookID, p.RowID).Scan(&remaining); err != nil || remaining == nil || *remaining == 0 {
				return nil, ErrInvalid
			}
			p.AmountMinor = strconv.FormatInt(*remaining, 10)
		}
		match.Rows = append(match.Rows, p)
	}
	for _, p := range q.Lines {
		if p.AmountMinor == "" {
			var remaining *string
			if err := tx.QueryRow(ctx, `SELECT (c.amount-public.statement_line_allocated($1,$2,$3,$4,$5,NULL,NULL))::text
 FROM (SELECT public.statement_line_amount($1,$2,$3,$4,$5) amount) c`, s.TenantID, s.BookID, p.LineID, currency, scale).Scan(&remaining); err != nil || remaining == nil || *remaining == "0" {
				return nil, ErrInvalid
			}
			value, ok := new(big.Rat).SetString(*remaining)
			if !ok || !value.IsInt() || !value.Num().IsInt64() {
				return nil, ErrInvalid
			}
			p.AmountMinor = value.Num().String()
		}
		match.Lines = append(match.Lines, p)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.statement_matches(tenant_id,book_id,id,account_id,actor_id,reason,receipt_id) VALUES($1,$2,$3,$4,$5,$6,$7)`,
		s.TenantID, s.BookID, match.ID, account, s.ActorID, q.Reason, receipt); err != nil {
		return nil, statementError(err)
	}
	part := 0
	for _, list := range [][]StatementMatchPart{match.Rows, match.Lines} {
		for _, p := range list {
			part++
			if _, err := tx.Exec(ctx, `INSERT INTO public.statement_match_parts(tenant_id,book_id,match_id,part,row_id,line_id,amount_minor) VALUES($1,$2,$3,$4,nullif($5,'')::uuid,nullif($6,'')::uuid,$7)`,
				s.TenantID, s.BookID, match.ID, part, p.RowID, p.LineID, p.AmountMinor); err != nil {
				return nil, statementError(err)
			}
		}
	}
	if err := checked(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, "SELECT recorded_at FROM public.statement_matches WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, match.ID).Scan(&match.RecordedAt); err != nil {
		return nil, dbError(err)
	}
	return raw(match), nil
}

func reviewStatement(ctx context.Context, tx pgx.Tx, s Scope, q ReviewStatement, receipt string) (json.RawMessage, error) {
	report, err := readStatementReconciliation(ctx, tx, s, q.StatementID)
	if err != nil {
		return nil, err
	}
	current := int64(0)
	if report.Statement.Review != nil {
		current = report.Statement.Review.Revision
	}
	if current != q.ExpectedRevision {
		return nil, ErrConflict
	}
	// The review signs this exact report.
	report.Statement.Review, report.Statement.Status = nil, ""
	bytes := raw(report)
	if _, err := tx.Exec(ctx, `INSERT INTO public.statement_reviews(tenant_id,book_id,statement_id,revision,outcome,report_bytes,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, s.TenantID, s.BookID, q.StatementID, current+1, q.Outcome, []byte(bytes), s.ActorID, q.Reason, receipt); err != nil {
		return nil, statementError(err)
	}
	if err := checked(ctx, tx); err != nil {
		return nil, err
	}
	st, err := readStatement(ctx, tx, s, q.StatementID)
	if err != nil {
		return nil, err
	}
	return raw(st), nil
}

const statementMetadataColumns = `s.id::text,s.account_id::text,coalesce((SELECT r.name FROM public.account_revisions r WHERE r.tenant_id=s.tenant_id AND r.book_id=s.book_id AND r.account_id=s.account_id ORDER BY r.revision DESC LIMIT 1),''),
 s.kind,s.currency,s.scale,s.document_id::text,s.period_start::text,s.period_end::text,s.opening_balance_minor::text,s.closing_balance_minor::text,
 s.evidence_state,s.note,s.actor_id::text,s.recorded_at,
 (SELECT count(*) FROM public.statement_rows r WHERE r.tenant_id=s.tenant_id AND r.book_id=s.book_id AND r.statement_id=s.id),
	 v.revision,v.outcome,v.reason,v.report_sha256,v.actor_id::text,v.recorded_at`
const statementColumns = statementMetadataColumns + `,
	 public.statement_current_reconciled(s.tenant_id,s.book_id,s.id)`
const statementFrom = ` FROM public.statements s LEFT JOIN LATERAL(SELECT * FROM public.statement_reviews v WHERE v.tenant_id=s.tenant_id AND v.book_id=s.book_id
 AND v.statement_id=s.id ORDER BY v.revision DESC LIMIT 1) v ON true`

func scanStatement(row pgx.Row) (Statement, error) {
	var st Statement
	var revision *int64
	var outcome, reason, sha, actor *string
	var at *time.Time
	var current bool
	if err := row.Scan(&st.ID, &st.AccountID, &st.AccountName, &st.Kind, &st.Currency, &st.Scale, &st.DocumentID, &st.PeriodStart, &st.PeriodEnd, &st.OpeningMinor, &st.ClosingMinor,
		&st.EvidenceState, &st.Note, &st.ActorID, &st.RecordedAt, &st.RowCount, &revision, &outcome, &reason, &sha, &actor, &at, &current); err != nil {
		return st, err
	}
	st.Status = "not_reviewed"
	if revision != nil {
		st.Review = &StatementReview{Revision: *revision, Outcome: *outcome, Reason: *reason, ReportSHA256: *sha, ActorID: *actor, RecordedAt: *at}
		st.Status = *outcome
		if st.Status == "reconciled" && !current {
			st.Status = "needs_work"
		}
	}
	return st, nil
}

func readStatement(ctx context.Context, tx pgx.Tx, s Scope, id string) (Statement, error) {
	st, err := scanStatement(tx.QueryRow(ctx, "SELECT "+statementColumns+statementFrom+" WHERE s.tenant_id=$1 AND s.book_id=$2 AND s.id=$3", s.TenantID, s.BookID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return st, ErrNotFound
	}
	return st, dbError(err)
}

func readStatements(ctx context.Context, tx pgx.Tx, s Scope, account string) ([]Statement, error) {
	rows, err := tx.Query(ctx, "SELECT "+statementColumns+statementFrom+` WHERE s.tenant_id=$1 AND s.book_id=$2 AND ($3='' OR s.account_id::text=$3)
 ORDER BY s.period_end DESC,s.recorded_at DESC LIMIT 1001`, s.TenantID, s.BookID, account)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []Statement{}
	for rows.Next() {
		st, err := scanStatement(rows)
		if err != nil {
			return nil, dbError(err)
		}
		out = append(out, st)
		if len(out) > 1000 {
			return nil, ErrLimit
		}
	}
	return out, dbError(rows.Err())
}

// proposedStatementRows are rows a waiting proposal would match, adjust or
// mark duplicate. Until approved they stay outstanding.
func proposedStatementRows(ctx context.Context, tx pgx.Tx, s Scope) (map[string]bool, map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT convert_from(p.payload_bytes,'UTF8')::jsonb->'arguments'
 FROM public.operation_proposals p WHERE p.tenant_id=$1 AND p.book_id=$2
 AND p.operation IN ('match_statement','record_statement_adjustment','mark_statement_duplicate')
 AND (p.expires_at IS NULL OR p.expires_at>transaction_timestamp()) AND `+proposalWaiting, s.TenantID, s.BookID)
	if err != nil {
		return nil, nil, dbError(err)
	}
	defer rows.Close()
	statementRows, lines := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var args []byte
		if err := rows.Scan(&args); err != nil {
			return nil, nil, dbError(err)
		}
		var a struct {
			RowID string               `json:"row_id"`
			Rows  []StatementMatchPart `json:"rows"`
			Lines []StatementMatchPart `json:"lines"`
		}
		_ = json.Unmarshal(args, &a)
		if a.RowID != "" {
			statementRows[a.RowID] = true
		}
		for _, p := range a.Rows {
			statementRows[p.RowID] = true
		}
		for _, p := range a.Lines {
			lines[p.LineID] = true
		}
	}
	return statementRows, lines, dbError(rows.Err())
}

func readStatementReconciliation(ctx context.Context, tx pgx.Tx, s Scope, id string) (StatementReconciliation, error) {
	// This report computes current coverage below; its header needs only the
	// historical review, rather than calculating that same coverage twice.
	st, err := scanStatement(tx.QueryRow(ctx, "SELECT "+statementMetadataColumns+",true"+statementFrom+" WHERE s.tenant_id=$1 AND s.book_id=$2 AND s.id=$3", s.TenantID, s.BookID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return StatementReconciliation{}, ErrNotFound
	}
	if err != nil {
		return StatementReconciliation{}, dbError(err)
	}
	out := StatementReconciliation{Statement: st, Rows: []StatementRow{}, Outstanding: []OutstandingLine{}, Matches: []StatementMatchRead{}}
	proposedRows, proposedLines, err := proposedStatementRows(ctx, tx, s)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `WITH coverage AS MATERIALIZED (
 SELECT * FROM public.statement_row_coverages($1,$2,$3,'infinity'::timestamptz)
) SELECT r.id::text,r.row_number,r.location,r.occurred_on::text,r.amount_minor::text,r.description,coalesce(r.external_id,''),r.lifecycle,coalesce(r.related_external_id,''),r.raw_sha256,
	 coverage.covered_minor::text,
 coalesce((SELECT array_agg(p.match_id::text ORDER BY p.match_id) FROM public.statement_match_parts p WHERE p.tenant_id=r.tenant_id AND p.book_id=r.book_id AND p.row_id=r.id
   AND NOT EXISTS(SELECT 1 FROM public.statement_match_revocations v WHERE v.tenant_id=p.tenant_id AND v.book_id=p.book_id AND v.match_id=p.match_id)),'{}'),
	 coalesce((SELECT a.entry_id::text FROM public.statement_adjustments a WHERE a.tenant_id=r.tenant_id AND a.book_id=r.book_id AND a.row_id=r.id
	   AND NOT public.statement_entry_reversed(a.tenant_id,a.book_id,a.entry_id)),''),
 coalesce((SELECT d.canonical_row_id::text FROM public.statement_duplicates d WHERE d.tenant_id=r.tenant_id AND d.book_id=r.book_id AND d.row_id=r.id),'')
 FROM public.statement_rows r JOIN coverage ON coverage.row_id=r.id
 WHERE r.tenant_id=$1 AND r.book_id=$2 AND r.statement_id=$3 ORDER BY r.row_number`, s.TenantID, s.BookID, id)
	if err != nil {
		return out, dbError(err)
	}
	opening, _ := new(big.Int).SetString(st.OpeningMinor, 10)
	closing, _ := new(big.Int).SetString(st.ClosingMinor, 10)
	matched, adjusted, duplicated, notInBook, settled := new(big.Int), new(big.Int), new(big.Int), new(big.Int), new(big.Int)
	sum := &out.Summary
	for rows.Next() {
		var r StatementRow
		var amountText, allocatedText string
		var location []byte
		if err := rows.Scan(&r.ID, &r.RowNumber, &location, &r.Date, &amountText, &r.Description, &r.ExternalID, &r.Lifecycle, &r.RelatedExternalID, &r.RawSHA256,
			&allocatedText, &r.MatchIDs, &r.AdjustmentEntryID, &r.DuplicateOf); err != nil {
			rows.Close()
			return out, dbError(err)
		}
		_ = json.Unmarshal(location, &r.Location)
		amount, _ := new(big.Int).SetString(amountText, 10)
		allocated, _ := new(big.Int).SetString(allocatedText, 10)
		r.AmountMinor, r.MatchedMinor = amountText, allocatedText
		remaining := new(big.Int).Sub(amount, allocated)
		switch {
		case r.Lifecycle == "pending" || r.Lifecycle == "cancelled":
			r.State = r.Lifecycle
			remaining.SetInt64(0)
			sum.PendingCount++
		case r.DuplicateOf != "":
			r.State = "duplicate"
			duplicated.Add(duplicated, allocated)
			settled.Add(settled, amount)
			sum.DuplicateCount++
		case r.AdjustmentEntryID != "":
			r.State = "adjusted"
			adjusted.Add(adjusted, allocated)
			settled.Add(settled, amount)
		default:
			settled.Add(settled, amount)
			matched.Add(matched, allocated)
			switch {
			case remaining.Sign() == 0:
				r.State = "matched"
			case proposedRows[r.ID]:
				r.State = "proposed"
				sum.ProposedCount++
			default:
				r.State = "not_in_book"
			}
		}
		if remaining.Sign() != 0 {
			notInBook.Add(notInBook, remaining)
			sum.NotInBookCount++
		}
		r.RemainingMinor = remaining.String()
		out.Rows = append(out.Rows, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, dbError(err)
	}
	// Outstanding in the Book: lines on the account dated in this statement's
	// period, or after the account's first statement began, that no active
	// match fully covers. Lines of reversed entries and their reversals
	// cancel out and are never statement money.
	lines, err := tx.Query(ctx, `WITH first AS (SELECT min(period_start) d FROM public.statements WHERE tenant_id=$1 AND book_id=$2 AND account_id=$3),
 posted AS MATERIALIZED (
 SELECT l.id line_id,e.id entry_id,e.accounting_date,e.description,e.sequence,l.position
 FROM public.lines l JOIN public.entries e ON e.tenant_id=l.tenant_id AND e.book_id=l.book_id AND e.id=l.entry_id
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.account_id=$3 AND e.accounting_date<=$6::date AND e.accounting_date>=(SELECT d FROM first)
 AND NOT public.statement_entry_reversed($1,$2,e.id)
 ORDER BY e.accounting_date,e.sequence,l.position LIMIT $7
), allocations AS MATERIALIZED (
 SELECT * FROM public.statement_line_allocations($1,$2,(SELECT array_agg(p.line_id) FROM posted p),$4,$5,$6::date,NULL)
)
 SELECT p.line_id::text,p.entry_id::text,p.accounting_date::text,p.description,public.statement_line_amount($1,$2,p.line_id,$4,$5)::text,a.allocated_minor::text
 FROM posted p LEFT JOIN allocations a ON a.line_id=p.line_id ORDER BY p.accounting_date,p.sequence,p.position`, s.TenantID, s.BookID, st.AccountID, st.Currency, st.Scale, st.PeriodEnd, statementLineLimit+1)
	if err != nil {
		return out, dbError(err)
	}
	outstanding := new(big.Int)
	count := 0
	for lines.Next() {
		var o OutstandingLine
		var amount *string
		var allocatedText *string
		if err := lines.Scan(&o.LineID, &o.EntryID, &o.Date, &o.Description, &amount, &allocatedText); err != nil {
			lines.Close()
			return out, dbError(err)
		}
		count++
		if count > statementLineLimit {
			lines.Close()
			return out, ErrLimit
		}
		if amount == nil || allocatedText == nil {
			o.Unresolved, o.Proposed = true, proposedLines[o.LineID]
			if amount != nil {
				o.AmountMinor = *amount
			}
			sum.UnresolvedCount++
			out.Outstanding = append(out.Outstanding, o)
			continue
		}
		value, _ := new(big.Int).SetString(*amount, 10)
		allocated, exact := new(big.Int).SetString(*allocatedText, 10)
		if !exact {
			o.Unresolved, o.AmountMinor = true, *amount
			sum.UnresolvedCount++
			out.Outstanding = append(out.Outstanding, o)
			continue
		}
		remaining := new(big.Int).Sub(value, allocated)
		if value.Sign() == 0 || remaining.Sign() == 0 {
			continue
		}
		o.AmountMinor, o.RemainingMinor, o.Proposed = *amount, remaining.String(), proposedLines[o.LineID]
		outstanding.Add(outstanding, remaining)
		out.Outstanding = append(out.Outstanding, o)
	}
	lines.Close()
	if err := lines.Err(); err != nil {
		return out, dbError(err)
	}
	sum.OutstandingCount = len(out.Outstanding)
	matches, err := tx.Query(ctx, `SELECT m.id::text,m.reason,m.actor_id::text,m.recorded_at,
 coalesce(jsonb_agg(jsonb_build_object('row_id',p.row_id,'line_id',p.line_id,'amount_minor',p.amount_minor::text) ORDER BY p.part),'[]')
 FROM public.statement_matches m JOIN public.statement_match_parts p ON p.tenant_id=m.tenant_id AND p.book_id=m.book_id AND p.match_id=m.id
 WHERE m.tenant_id=$1 AND m.book_id=$2 AND NOT EXISTS(SELECT 1 FROM public.statement_match_revocations v WHERE v.tenant_id=m.tenant_id AND v.book_id=m.book_id AND v.match_id=m.id)
 AND EXISTS(SELECT 1 FROM public.statement_match_parts x JOIN public.statement_rows r ON r.tenant_id=x.tenant_id AND r.book_id=x.book_id AND r.id=x.row_id
   WHERE x.tenant_id=m.tenant_id AND x.book_id=m.book_id AND x.match_id=m.id AND r.statement_id=$3)
 GROUP BY m.id ORDER BY m.recorded_at,m.id`, s.TenantID, s.BookID, id)
	if err != nil {
		return out, dbError(err)
	}
	for matches.Next() {
		m := StatementMatchRead{Rows: []StatementMatchPart{}, Lines: []StatementMatchPart{}}
		var parts []byte
		if err := matches.Scan(&m.ID, &m.Reason, &m.ActorID, &m.RecordedAt, &parts); err != nil {
			matches.Close()
			return out, dbError(err)
		}
		var list []struct {
			RowID, LineID *string
			Amount        string `json:"amount_minor"`
		}
		_ = json.Unmarshal(parts, &list)
		for _, p := range list {
			if p.RowID != nil {
				m.Rows = append(m.Rows, StatementMatchPart{RowID: *p.RowID, AmountMinor: p.Amount})
			} else if p.LineID != nil {
				m.Lines = append(m.Lines, StatementMatchPart{LineID: *p.LineID, AmountMinor: p.Amount})
			}
		}
		out.Matches = append(out.Matches, m)
	}
	matches.Close()
	if err := matches.Err(); err != nil {
		return out, dbError(err)
	}
	sum.OpeningMinor, sum.ClosingMinor = st.OpeningMinor, st.ClosingMinor
	sum.MatchedMinor, sum.AdjustedMinor, sum.DuplicateMinor = matched.String(), adjusted.String(), duplicated.String()
	sum.NotInBookMinor, sum.OutstandingMinor = notInBook.String(), outstanding.String()
	sum.StatementDifference = new(big.Int).Sub(new(big.Int).Sub(closing, opening), settled).String()
	difference := new(big.Int).Sub(new(big.Int).Sub(closing, opening), matched)
	difference.Sub(difference, adjusted).Sub(difference, duplicated)
	sum.DifferenceMinor = difference.String()
	sum.Explained = difference.Sign() == 0 && sum.NotInBookCount == 0 && st.EvidenceState == "complete"
	if out.Statement.Status == "reconciled" && !sum.Explained {
		out.Statement.Status = "needs_work"
	}
	return out, nil
}

// suggestStatementMatches proposes, never records: a settled row that is not
// in the Book yet and one unmatched line on the account with the same
// remaining amount within a week; and a row that repeats a row of another
// statement for the same account, amount and date or external ID.
func suggestStatementMatches(ctx context.Context, tx pgx.Tx, s Scope, id string) ([]StatementSuggestion, error) {
	report, err := readStatementReconciliation(ctx, tx, s, id)
	if err != nil {
		return nil, err
	}
	return suggestStatementMatchesFromReport(ctx, tx, s, report)
}

func suggestStatementMatchesFromReport(ctx context.Context, tx pgx.Tx, s Scope, report StatementReconciliation) ([]StatementSuggestion, error) {
	out := []StatementSuggestion{}
	type candidateKey struct {
		amount string
		day    int64
	}
	// Each date bucket preserves the report's order, which breaks equal-date
	// distance ties. A selected line is removed once; each row checks at most
	// fifteen buckets instead of scanning every outstanding line.
	candidates := map[candidateKey][]int{}
	for i, o := range report.Outstanding {
		if o.Unresolved {
			continue
		}
		date, _ := time.Parse(time.DateOnly, o.Date)
		key := candidateKey{o.RemainingMinor, date.Unix() / 86400}
		candidates[key] = append(candidates[key], i)
	}
	duplicateRows := []string{}
	for _, r := range report.Rows {
		if r.State != "not_in_book" {
			continue
		}
		if r.MatchedMinor == "0" {
			duplicateRows = append(duplicateRows, r.ID)
		}
		date, _ := time.Parse(time.DateOnly, r.Date)
		day := date.Unix() / 86400
		for days := int64(0); days <= 7; days++ {
			key := candidateKey{r.RemainingMinor, day - days}
			best := -1
			if bucket := candidates[key]; len(bucket) > 0 {
				best = bucket[0]
			}
			if days > 0 {
				after := candidateKey{r.RemainingMinor, day + days}
				if bucket := candidates[after]; len(bucket) > 0 && (best < 0 || bucket[0] < best) {
					key, best = after, bucket[0]
				}
			}
			if best < 0 {
				continue
			}
			candidates[key] = candidates[key][1:]
			line := report.Outstanding[best]
			reason := "Same amount and date"
			switch days {
			case 0:
			case 1:
				reason = "Same amount, a day apart"
			default:
				reason = "Same amount, " + strconv.FormatInt(days, 10) + " days apart"
			}
			out = append(out, StatementSuggestion{Kind: "match", RowID: r.ID, LineIDs: []string{line.LineID}, EntryIDs: []string{line.EntryID}, AmountMinor: r.RemainingMinor, Reason: reason})
			break
		}
	}
	if len(duplicateRows) == 0 {
		return out, nil
	}
	// Earlier observations are read in one scan of this account's statements
	// near this one and compared here, so the cost stays linear whatever the
	// planner knows about freshly imported rows.
	st := report.Statement
	period := func(date string, days int) string {
		day, _ := time.Parse(time.DateOnly, date)
		return day.AddDate(0, 0, days).Format(time.DateOnly)
	}
	waiting := map[string]bool{}
	for _, id := range duplicateRows {
		waiting[id] = true
	}
	canonicalHere := map[string]bool{}
	held, err := tx.Query(ctx, `SELECT canonical_row_id::text FROM public.statement_duplicates
 WHERE tenant_id=$1 AND book_id=$2 AND canonical_row_id=ANY($3::uuid[])`, s.TenantID, s.BookID, duplicateRows)
	if err != nil {
		return nil, dbError(err)
	}
	for held.Next() {
		var id string
		if err := held.Scan(&id); err != nil {
			held.Close()
			return nil, dbError(err)
		}
		canonicalHere[id] = true
	}
	held.Close()
	if err := held.Err(); err != nil {
		return nil, dbError(err)
	}
	type earlier struct {
		id, amount, externalID, date, raw, statement string
		row                                          int
		recorded                                     time.Time
	}
	raws, externalIDs := []string{}, []string{}
	for _, r := range report.Rows {
		if waiting[r.ID] {
			raws = append(raws, r.RawSHA256)
			if r.ExternalID != "" {
				externalIDs = append(externalIDs, r.ExternalID)
			}
		}
	}
	byAmount := map[string][]earlier{}
	rows, err := tx.Query(ctx, `SELECT c.id::text,c.amount_minor::text,coalesce(c.external_id,''),c.occurred_on::text,c.raw_sha256,c.row_number,cs.id::text,cs.recorded_at
 FROM public.statements cs JOIN public.statement_rows c ON c.tenant_id=cs.tenant_id AND c.book_id=cs.book_id AND c.statement_id=cs.id
 WHERE cs.tenant_id=$1 AND cs.book_id=$2 AND cs.account_id=$3 AND cs.currency=$4 AND cs.scale=$5 AND cs.id<>$6 AND cs.recorded_at<$7
 AND cs.period_start<=$9::date AND cs.period_end>=$8::date AND c.lifecycle NOT IN ('pending','cancelled')
 AND (c.raw_sha256=ANY($10::text[]) OR c.external_id=ANY($11::text[]))
 AND NOT EXISTS(SELECT 1 FROM public.statement_duplicates d WHERE d.tenant_id=c.tenant_id AND d.book_id=c.book_id AND d.row_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM public.statement_duplicates d JOIN public.statement_rows used
   ON used.tenant_id=d.tenant_id AND used.book_id=d.book_id AND used.id=d.row_id
   WHERE d.tenant_id=c.tenant_id AND d.book_id=c.book_id AND d.canonical_row_id=c.id AND used.statement_id=$6)`,
		s.TenantID, s.BookID, st.AccountID, st.Currency, st.Scale, st.ID, st.RecordedAt, period(st.PeriodStart, -31), period(st.PeriodEnd, 31), raws, externalIDs)
	if err != nil {
		return nil, dbError(err)
	}
	for rows.Next() {
		var e earlier
		if err := rows.Scan(&e.id, &e.amount, &e.externalID, &e.date, &e.raw, &e.row, &e.statement, &e.recorded); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		byAmount[e.amount] = append(byAmount[e.amount], e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	for _, list := range byAmount {
		sort.Slice(list, func(i, j int) bool {
			a, b := list[i], list[j]
			if !a.recorded.Equal(b.recorded) {
				return a.recorded.Before(b.recorded)
			}
			if a.statement != b.statement {
				return a.statement < b.statement
			}
			if a.row != b.row {
				return a.row < b.row
			}
			return a.id < b.id
		})
	}
	usedCanonical := map[string]bool{}
	// Rows arrive in statement order; each earlier observation explains at
	// most one of them. The bank's own ID outranks an identical raw row.
	for _, r := range report.Rows {
		if !waiting[r.ID] || canonicalHere[r.ID] {
			continue
		}
		var exact, same *earlier
		for i := range byAmount[r.AmountMinor] {
			e := &byAmount[r.AmountMinor][i]
			if usedCanonical[e.id] {
				continue
			}
			if r.ExternalID != "" && e.externalID == r.ExternalID {
				exact = e
				break
			}
			if same == nil && e.date == r.Date && e.raw == r.RawSHA256 {
				same = e
			}
		}
		choice, reason := exact, "Same external ID on an earlier statement"
		if choice == nil {
			choice, reason = same, "The same row on an earlier statement"
		}
		if choice == nil {
			continue
		}
		usedCanonical[choice.id] = true
		out = append(out, StatementSuggestion{Kind: "duplicate", RowID: r.ID, CanonicalID: choice.id, AmountMinor: r.AmountMinor, Reason: reason})
	}
	return out, nil
}
