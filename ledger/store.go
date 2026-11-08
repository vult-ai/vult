package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/strictjson"
)

type Store struct {
	Runtime   *pgxpool.Pool
	Documents *documents.Vault
	// Authorize adds a trusted deployment restriction inside the same transaction.
	// It cannot bypass the ledger's database scope, live leases or invariants.
	Authorize func(context.Context, pgx.Tx, Scope, string, json.RawMessage) error
	// AdmitWrite runs only for a new request, after retained-outcome lookup, in
	// the same transaction as its financial effects. Retries never consume twice.
	AdmitWrite func(context.Context, pgx.Tx, Scope, string, json.RawMessage) error
}

func setScope(ctx context.Context, tx pgx.Tx, s Scope) error {
	_, e := tx.Exec(ctx, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),
 set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`, s.TenantID, s.BookID, s.ActorID, s.ConnectionID)
	return e
}
func dbError(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, ErrDenied) {
		return ErrDenied
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) {
		if p.Code == "VP001" {
			return ErrProposalStale
		}
		if p.Code == "42501" {
			return ErrDenied
		}
		if p.Code == "23505" {
			return ErrConflict
		}
		if strings.HasPrefix(p.Code, "22") || strings.HasPrefix(p.Code, "23") || p.Code == "25001" {
			return ErrInvalid
		}
	}
	return errors.New("database operation failed")
}
func Writing(method string) bool {
	switch method {
	case "create_account", "revise_account", "post_journal", "reverse_journal", "add_document", "complete_document_upload", "correct_source_citation", "attach_evidence", "detach_evidence":
		return true
	case "prepare_operation", "execute_proposal", setBookLogoOperation:
		return true
	}
	return false
}
func Supported(method string) bool {
	if documentMethod(method) || citationMethod(method) || evidenceMethod(method) {
		return true
	}
	if method == "prepare_operation" || method == "get_operation_proposal" || method == "execute_proposal" || method == setBookLogoOperation {
		return true
	}
	switch method {
	case "get_book", "list_accounts", "create_account", "revise_account", "post_journal", "get_journal", "list_journals", "reverse_journal", "get_balance", "get_trial_balance", "get_request_outcome", "search_book":
		return true
	}
	return false
}

// Call holds the live connection and Book through transaction completion.
func (s Store) Call(ctx context.Context, scope Scope, method string, input json.RawMessage) (json.RawMessage, error) {
	if !Supported(method) {
		return nil, ErrDenied
	}
	if method == "prepare_operation" {
		return s.prepareOperation(ctx, scope, input)
	}
	if method == setBookLogoOperation {
		change, key, err := decodeBookLogoTool(input)
		if err != nil {
			return nil, err
		}
		return s.SetBookLogo(ctx, scope, key, change)
	}
	return s.transact(ctx, scope, method, input, func(tx pgx.Tx) (json.RawMessage, error) {
		if method == "get_operation_proposal" || method == "execute_proposal" {
			return s.proposalCall(ctx, tx, scope, method, input)
		}
		if documentMethod(method) {
			return s.documentCall(ctx, tx, scope, method, input)
		}
		if citationMethod(method) {
			return s.citationCall(ctx, tx, scope, method, input)
		}
		if evidenceMethod(method) {
			return s.evidenceCall(ctx, tx, scope, method, input)
		}
		return dispatch(ctx, tx, scope, method, input, s.AdmitWrite)
	})
}

func (s Store) transact(ctx context.Context, scope Scope, method string, input json.RawMessage, apply func(pgx.Tx) (json.RawMessage, error)) (json.RawMessage, error) {
	if s.Runtime == nil || !ValidID(scope.TenantID) || !ValidID(scope.BookID) || !ValidID(scope.ActorID) || !ValidID(scope.ConnectionID) {
		return nil, ErrDenied
	}
	if Writing(method) && scope.Access != "write" {
		return nil, ErrDenied
	}
	tx, e := s.Runtime.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return nil, dbError(e)
	}
	defer tx.Rollback(context.Background())
	if e = setScope(ctx, tx, scope); e != nil {
		return nil, dbError(e)
	}
	if s.Authorize != nil {
		if e = s.Authorize(ctx, tx, scope, method, input); e != nil {
			return nil, e
		}
	}
	var access string
	e = tx.QueryRow(ctx, "SELECT access FROM public.connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND actor_id=$4 AND revoked_at IS NULL FOR SHARE", scope.TenantID, scope.BookID, scope.ConnectionID, scope.ActorID).Scan(&access)
	if e != nil || access != scope.Access {
		return nil, ErrDenied
	}
	if Writing(method) {
		if method == "execute_proposal" {
			if e = holdProposalOwner(ctx, tx, scope, input); e != nil {
				return nil, e
			}
		}
		if _, e = tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, scope.BookID); e != nil {
			return nil, dbError(e)
		}
	} else {
		var locked string
		if e = tx.QueryRow(ctx, "SELECT id::text FROM public.books WHERE tenant_id=$1 AND id=$2 FOR SHARE", scope.TenantID, scope.BookID).Scan(&locked); e != nil {
			return nil, ErrDenied
		}
	}
	result, e := apply(tx)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		var rejected *pgconn.PgError
		if errors.As(e, &rejected) && (strings.HasPrefix(rejected.Code, "22") || strings.HasPrefix(rejected.Code, "23") || rejected.Code == "25001" || rejected.Code == "42501" || rejected.Code == "VP001") {
			return nil, dbError(e)
		}
		return nil, ErrUncertain
	}
	return result, nil
}

func dispatch(ctx context.Context, tx pgx.Tx, s Scope, method string, input []byte, admit func(context.Context, pgx.Tx, Scope, string, json.RawMessage) error) (json.RawMessage, error) {
	decode := func(v any) error {
		if strictjson.Decode(input, v) != nil {
			return ErrInvalid
		}
		return nil
	}
	switch method {
	case "get_book":
		if e := decode(&struct{}{}); e != nil {
			return nil, e
		}
		b, e := readBook(ctx, tx, s)
		return raw(b), dbError(e)
	case "list_accounts":
		if e := decode(&struct{}{}); e != nil {
			return nil, e
		}
		a, e := readAccounts(ctx, tx, s)
		return raw(struct {
			Accounts []Account `json:"accounts"`
		}{a}), dbError(e)
	case "get_journal":
		var q EntryQuery
		if decode(&q) != nil || !ValidID(q.EntryID) {
			return nil, ErrInvalid
		}
		v, e := readEntry(ctx, tx, s, q.EntryID)
		return raw(v), dbError(e)
	case "list_journals":
		var q ListQuery
		if decode(&q) != nil || q.After < 0 || q.Limit < 0 || q.Limit > 100 {
			return nil, ErrInvalid
		}
		if q.Limit == 0 {
			q.Limit = 50
		}
		entries, more, e := readEntryPage(ctx, tx, s, q.After, q.Limit)
		if e != nil {
			return nil, dbError(e)
		}
		next := q.After
		if len(entries) > 0 {
			next = entries[len(entries)-1].Sequence
		}
		state, e := immutableReadState(ctx, tx, s)
		if e != nil {
			return nil, e
		}
		return raw(struct {
			RecordedState
			Entries   []Entry `json:"entries"`
			NextAfter int64   `json:"next_after"`
			HasMore   bool    `json:"has_more"`
		}{state, entries, next, more}), nil
	case "get_balance":
		var q AccountQuery
		if decode(&q) != nil || !ValidID(q.AccountID) || (q.Through != "" && !ValidDate(q.Through)) {
			return nil, ErrInvalid
		}
		balance, e := readBalance(ctx, tx, s, q)
		return raw(balance), dbError(e)
	case "search_book":
		var q SearchQuery
		if decode(&q) != nil {
			return nil, ErrInvalid
		}
		header, e := readBook(ctx, tx, s)
		if e != nil {
			return nil, dbError(e)
		}
		v, e := searchRecorded(ctx, tx, s, header, q)
		if e != nil {
			return nil, e
		}
		return raw(v), nil
	case "get_trial_balance":
		var q TrialQuery
		if decode(&q) != nil || (q.Through != "" && !ValidDate(q.Through)) {
			return nil, ErrInvalid
		}
		v, e := readTrial(ctx, tx, s, q.Through)
		return raw(v), dbError(e)
	case "get_request_outcome":
		var q OutcomeQuery
		if decode(&q) != nil || !validKey(q.RequestKey) {
			return nil, ErrInvalid
		}
		var operation string
		var result []byte
		e := tx.QueryRow(ctx, "SELECT operation,result_bytes FROM public.requests WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4", s.TenantID, s.BookID, s.ActorID, q.RequestKey).Scan(&operation, &result)
		if errors.Is(e, pgx.ErrNoRows) {
			return raw(Outcome{Status: "unresolved"}), nil
		}
		if e != nil {
			return nil, dbError(e)
		}
		return raw(Outcome{Status: "recorded", Operation: operation, Result: result}), nil
	case "create_account":
		var q CreateAccount
		if decode(&q) != nil || !validAccount(q.Class, q.Code, q.Name) || q.Code == "" && !validAccountCodeInput(input, false) {
			return nil, ErrInvalid
		}
		return write(ctx, tx, s, method, q.RequestKey, raw(q), admit, func(receipt string) (json.RawMessage, error) {
			return createAccount(ctx, tx, s, q, receipt)
		})
	case "revise_account":
		var q ReviseAccount
		if decode(&q) != nil || !ValidID(q.AccountID) || q.ExpectedRevision < 1 || !validCode(q.Code) || len(q.Name) < 1 || len(q.Name) > 120 || q.Code == "" && !validAccountCodeInput(input, true) {
			return nil, ErrInvalid
		}
		return write(ctx, tx, s, method, q.RequestKey, raw(q), admit, func(receipt string) (json.RawMessage, error) {
			return reviseAccount(ctx, tx, s, q, receipt)
		})
	case "post_journal":
		var q PostJournal
		if decode(&q) != nil || validatePost(q) != nil {
			return nil, ErrInvalid
		}
		return write(ctx, tx, s, method, q.RequestKey, raw(q), admit, func(receipt string) (json.RawMessage, error) {
			if err := prepareMeasurements(ctx, tx, s, &q); err != nil {
				return nil, err
			}
			v := Entry{ID: ID(), ReceiptID: receipt, ActorID: s.ActorID, EconomicDate: q.EconomicDate, AccountingDate: q.AccountingDate, Description: q.Description, Lines: []Line{}, MeasurementPolicy: q.MeasurementPolicy}
			for _, l := range q.Lines {
				v.Lines = append(v.Lines, Line{ID: ID(), AccountID: l.AccountID, Side: l.Side, AmountMinor: l.AmountMinor, Memo: l.Memo, Native: l.Native, Contexts: l.Contexts, Valuation: l.Valuation})
			}
			if e := insertEntry(ctx, tx, s, &v); e != nil {
				return nil, e
			}
			for i, source := range q.Sources {
				if e := insertSource(ctx, tx, s, v.ID, receipt, i+1, source); e != nil {
					return nil, e
				}
			}
			return raw(v), nil
		})
	case "reverse_journal":
		var q ReverseJournal
		if decode(&q) != nil || !ValidID(q.EntryID) || !ValidDate(q.AccountingDate) || len(q.Reason) < 1 || len(q.Reason) > 4000 {
			return nil, ErrInvalid
		}
		return write(ctx, tx, s, method, q.RequestKey, raw(q), admit, func(receipt string) (json.RawMessage, error) {
			old, e := readEntry(ctx, tx, s, q.EntryID)
			if e != nil {
				return nil, e
			}
			v := Entry{ID: ID(), ReceiptID: receipt, ActorID: s.ActorID, EconomicDate: old.EconomicDate, AccountingDate: q.AccountingDate, Description: q.Reason, ReversalOf: old.ID, Lines: []Line{}, MeasurementPolicy: old.MeasurementPolicy}
			for _, l := range old.Lines {
				side := "debit"
				if l.Side == "debit" {
					side = "credit"
				}
				v.Lines = append(v.Lines, Line{ID: ID(), AccountID: l.AccountID, Side: side, AmountMinor: l.AmountMinor, Memo: l.Memo, ReversesLineID: l.ID, Native: l.Native, Contexts: l.Contexts, Valuation: l.Valuation})
			}
			if e := insertEntry(ctx, tx, s, &v); e != nil {
				return nil, e
			}
			return raw(v), nil
		})
	}
	return nil, ErrInvalid
}

func validCode(code string) bool { return code == "" || codePattern.MatchString(code) }

func validAccountCodeInput(input []byte, required bool) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return false
	}
	code, present := fields["code"]
	// A revision must explicitly retain or clear the code. Omission must not
	// silently clear an established label; null is not an accepted string.
	return !present && !required || present && !bytes.Equal(bytes.TrimSpace(code), []byte("null"))
}

func validAccount(class, code, name string) bool {
	if !validCode(code) || len(name) < 1 || len(name) > 120 {
		return false
	}
	switch class {
	case "asset", "liability", "equity", "income", "expense":
		return true
	}
	return false
}
func validatePost(q PostJournal) error {
	if validateSources(q.Sources) != nil {
		return ErrInvalid
	}
	if !validKey(q.RequestKey) || !ValidDate(q.EconomicDate) || !ValidDate(q.AccountingDate) || len(q.Description) < 1 || len(q.Description) > 4000 || len(q.Lines) < 2 || len(q.Lines) > 256 {
		return ErrInvalid
	}
	total := new(big.Int)
	for _, l := range q.Lines {
		n, e := nonnegativeAmount(l.AmountMinor)
		if e != nil || !ValidID(l.AccountID) || len(l.Memo) > 1000 {
			return ErrInvalid
		}
		v := new(big.Int).Set(n)
		if l.Side == "credit" {
			v.Neg(v)
		} else if l.Side != "debit" {
			return ErrInvalid
		}
		total.Add(total, v)
	}
	if total.Sign() != 0 {
		return ErrInvalid
	}
	return nil
}
func write(ctx context.Context, tx pgx.Tx, s Scope, method, key string, request []byte, admit func(context.Context, pgx.Tx, Scope, string, json.RawMessage) error, apply func(string) (json.RawMessage, error)) (json.RawMessage, error) {
	if !validKey(key) {
		return nil, ErrInvalid
	}
	// transact already holds the Book write lock before receipt lookup/admission.
	var oldMethod string
	var oldRequest, oldResult []byte
	e := tx.QueryRow(ctx, "SELECT operation,request_bytes,result_bytes FROM public.requests WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4", s.TenantID, s.BookID, s.ActorID, key).Scan(&oldMethod, &oldRequest, &oldResult)
	if e == nil {
		if oldMethod != method || !bytes.Equal(oldRequest, request) {
			return nil, ErrConflict
		}
		return oldResult, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, dbError(e)
	}
	if admit != nil {
		if e = admit(ctx, tx, s, method, json.RawMessage(request)); e != nil {
			return nil, e
		}
	}
	id := ID()
	result, e := apply(id)
	if e != nil {
		for _, safe := range []error{ErrConflict, ErrInvalid, ErrDenied, ErrNotFound, ErrUploadExpired, documents.ErrIntegrity, documents.ErrUnavailable} {
			if errors.Is(e, safe) {
				return nil, safe
			}
		}
		return nil, dbError(e)
	}
	_, e = tx.Exec(ctx, "INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", s.TenantID, s.BookID, id, s.ActorID, key, method, request, []byte(result))
	if e != nil {
		return nil, dbError(e)
	}
	return result, nil
}

const bookColumns = `b.tenant_id::text,b.id::text,coalesce(m.name,b.name),b.currency,b.scale,b.created_at,m.revision,
 coalesce(m.founder,''),coalesce(m.profile,''),coalesce(m.start_date::text,''),m.fiscal_year_start_month,m.fiscal_year_start_day,coalesce(m.accounting_timezone,''),m.logo`
const bookFrom = ` FROM public.books b LEFT JOIN LATERAL(
 SELECT name,founder,profile,start_date,fiscal_year_start_month,fiscal_year_start_day,accounting_timezone,revision,logo
 FROM public.book_revisions r WHERE r.tenant_id=b.tenant_id AND r.book_id=b.id ORDER BY revision DESC LIMIT 1) m ON true`

func scanBook(row pgx.Row) (Book, error) {
	var b Book
	err := row.Scan(&b.TenantID, &b.ID, &b.Name, &b.Currency, &b.Scale, &b.CreatedAt, &b.MetadataRevision, &b.Founder, &b.Profile, &b.StartDate,
		&b.FiscalYearStartMonth, &b.FiscalYearStartDay, &b.AccountingTimezone, &b.Logo)
	return b, err
}
func readBook(ctx context.Context, tx pgx.Tx, s Scope) (Book, error) {
	return scanBook(tx.QueryRow(ctx, "SELECT "+bookColumns+bookFrom+" WHERE b.tenant_id=$1 AND b.id=$2", s.TenantID, s.BookID))
}

func readAccounts(ctx context.Context, tx pgx.Tx, s Scope) ([]Account, error) {
	rows, e := tx.Query(ctx, "SELECT "+accountColumns+" FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 ORDER BY code,id", s.TenantID, s.BookID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const entryColumns = `id::text,sequence,receipt_id::text,actor_id::text,economic_date::text,accounting_date::text,
 description,coalesce(reversal_of::text,''),recorded_at,measurement_policy`

func scanEntry(row pgx.Row, extra ...any) (Entry, error) {
	var v Entry
	v.Lines = []Line{}
	fields := []any{&v.ID, &v.Sequence, &v.ReceiptID, &v.ActorID, &v.EconomicDate, &v.AccountingDate, &v.Description, &v.ReversalOf, &v.RecordedAt, &v.MeasurementPolicy}
	err := row.Scan(append(fields, extra...)...)
	return v, err
}

func readEntry(ctx context.Context, tx pgx.Tx, s Scope, id string) (Entry, error) {
	row := tx.QueryRow(ctx, "SELECT "+entryColumns+" FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, id)
	return readEntryRow(ctx, tx, s, row)
}

func readEntryRow(ctx context.Context, tx pgx.Tx, s Scope, row pgx.Row, extra ...any) (Entry, error) {
	v, err := scanEntry(row, extra...)
	if err != nil {
		return v, err
	}
	entries := []Entry{v}
	err = readEntryLines(ctx, tx, s, entries)
	return entries[0], err
}

func readEntryPage(ctx context.Context, tx pgx.Tx, s Scope, after int64, limit int) ([]Entry, bool, error) {
	rows, err := tx.Query(ctx, "SELECT "+entryColumns+" FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND sequence>$3 ORDER BY sequence LIMIT $4", s.TenantID, s.BookID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	entries := []Entry{}
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			rows.Close()
			return nil, false, err
		}
		entries = append(entries, entry)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	more := len(entries) > limit
	if more {
		entries = entries[:limit]
	}
	err = readEntryLines(ctx, tx, s, entries)
	return entries, more, err
}

func readEntryLines(ctx context.Context, tx pgx.Tx, s Scope, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	ids := make([]string, len(entries))
	positions := make(map[string]int, len(entries))
	for i := range entries {
		ids[i], positions[entries[i].ID] = entries[i].ID, i
	}
	rows, err := tx.Query(ctx, `SELECT l.entry_id::text,l.id::text,l.account_id::text,l.side,l.amount_minor::text,l.memo,coalesce(l.reverses_line_id::text,''),
 l.native_currency,l.native_scale,l.native_amount_minor::text,l.native_account_revision,r.currency_definition,l.contexts,l.valuation
 FROM public.lines l LEFT JOIN public.account_revisions r ON r.tenant_id=l.tenant_id AND r.book_id=l.book_id
 AND r.account_id=l.account_id AND r.revision=l.native_account_revision
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=ANY($3::uuid[]) ORDER BY l.entry_id,l.position`, s.TenantID, s.BookID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var entry string
		var line Line
		var currency, amount, definition *string
		var scale *int
		var revision *int64
		if err := rows.Scan(&entry, &line.ID, &line.AccountID, &line.Side, &line.AmountMinor, &line.Memo, &line.ReversesLineID,
			&currency, &scale, &amount, &revision, &definition, &line.Contexts, &line.Valuation); err != nil {
			return err
		}
		if currency != nil {
			// A time-sensitive row policy can hide the joined revision after
			// admitting its line. An incomplete visible unit is not a native zero.
			if scale == nil || amount == nil || revision == nil || definition == nil {
				return ErrDenied
			}
			line.Native = &NativeMovement{Currency: *currency, Scale: scale, AmountMinor: *amount, AccountRevision: *revision, UnitDefinition: *definition}
		} else if scale != nil || amount != nil || revision != nil || definition != nil {
			return ErrDenied
		}
		i, ok := positions[entry]
		if !ok {
			return ErrInvalid
		}
		entries[i].Lines = append(entries[i].Lines, line)
	}
	return rows.Err()
}
func insertEntry(ctx context.Context, tx pgx.Tx, s Scope, v *Entry) error {
	e := tx.QueryRow(ctx, `INSERT INTO public.entries(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description,reversal_of,measurement_policy)
 VALUES($1,$2,$3,1,$4,$5,$6,$7,$8,nullif($9,'')::uuid,$10) RETURNING sequence,recorded_at`,
		s.TenantID, s.BookID, v.ID, v.ReceiptID, v.ActorID, v.EconomicDate, v.AccountingDate, v.Description, v.ReversalOf, v.MeasurementPolicy).Scan(&v.Sequence, &v.RecordedAt)
	if e != nil {
		return e
	}
	for i, l := range v.Lines {
		var currency, amount *string
		var scale *int
		var revision *int64
		if l.Native != nil {
			currency, amount, scale, revision = &l.Native.Currency, &l.Native.AmountMinor, l.Native.Scale, &l.Native.AccountRevision
		}
		_, e = tx.Exec(ctx, `INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor,memo,reverses_line_id,
 native_currency,native_scale,native_amount_minor,native_account_revision,contexts,valuation)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::bigint,$9,nullif($10,'')::uuid,$11,$12,$13::bigint,$14,$15,$16)`,
			s.TenantID, s.BookID, l.ID, v.ID, i+1, l.AccountID, l.Side, l.AmountMinor, l.Memo, l.ReversesLineID,
			currency, scale, amount, revision, emptyContexts(l.Contexts), l.Valuation)
		if e != nil {
			return e
		}
	}
	return nil
}

const balanceQuery = `SELECT a.id::text,a.code,a.name,p.book_minor::text,
 p.status,coalesce(p.currency,''),p.native_scale,coalesce(p.native_minor::text,''),p.last_sequence
 FROM public.accounts a CROSS JOIN LATERAL public.ledger_native_position(a.tenant_id,a.book_id,a.id,$3,0) p
 WHERE a.tenant_id=$1 AND a.book_id=$2`

func scanBalance(row pgx.Row) (Balance, error) {
	var v Balance
	n := &NativePosition{}
	err := row.Scan(&v.AccountID, &v.Code, &v.Name, &v.DebitPositiveMinor, &n.Status, &n.Currency, &n.Scale, &n.DebitPositiveMinor, &n.ThroughSequence)
	if n.Status != "not_recorded" {
		v.Native = n
	}
	return v, err
}

func readBalance(ctx context.Context, tx pgx.Tx, s Scope, q AccountQuery) (Balance, error) {
	out, err := scanBalance(tx.QueryRow(ctx, balanceQuery+" AND a.id=$4", s.TenantID, s.BookID, q.Through, q.AccountID))
	if err != nil {
		return Balance{}, err
	}
	state, err := immutableReadState(ctx, tx, s)
	if err != nil {
		return Balance{}, err
	}
	out.RecordedState = state
	return out, nil
}

func readTrial(ctx context.Context, tx pgx.Tx, s Scope, through string) (Trial, error) {
	b, err := readBook(ctx, tx, s)
	if err != nil {
		return Trial{}, err
	}
	state, err := captureReadState(ctx, tx, s, b, recordedReadScope{AllAccepted: true})
	if err != nil {
		return Trial{}, err
	}
	out := Trial{RecordedState: state.RecordedState, Currency: b.Currency, Scale: b.Scale, Through: through, Balances: []Balance{}}
	rows, err := tx.Query(ctx, balanceQuery+" ORDER BY a.code,a.id", s.TenantID, s.BookID, through)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanBalance(rows)
		if err != nil {
			return out, err
		}
		out.Balances = append(out.Balances, v)
	}
	return out, rows.Err()
}
