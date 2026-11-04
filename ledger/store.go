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
	"github.com/vult-ai/vult/internal/strictjson"
)

type Store struct{ Runtime *pgxpool.Pool }

func setScope(ctx context.Context, tx pgx.Tx, s Scope) error {
	_, e := tx.Exec(ctx, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),
 set_config('vult.actor_id',$3,true),set_config('vult.connection_id',$4,true)`, s.TenantID, s.BookID, s.ActorID, s.ConnectionID)
	return e
}
func dbError(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) {
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
	case "create_account", "revise_account", "post_journal", "reverse_journal":
		return true
	}
	return false
}
func Supported(method string) bool {
	switch method {
	case "get_book", "list_accounts", "create_account", "revise_account", "post_journal", "get_journal", "list_journals", "reverse_journal", "get_balance", "get_trial_balance", "get_request_outcome":
		return true
	}
	return false
}

// Call holds the live connection and Book through transaction completion.
func (s Store) Call(ctx context.Context, scope Scope, method string, input json.RawMessage) (json.RawMessage, error) {
	if s.Runtime == nil || !Supported(method) || !ValidID(scope.TenantID) || !ValidID(scope.BookID) || !ValidID(scope.ActorID) || !ValidID(scope.ConnectionID) {
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
	var access string
	e = tx.QueryRow(ctx, "SELECT access FROM public.connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND actor_id=$4 AND revoked_at IS NULL FOR SHARE", scope.TenantID, scope.BookID, scope.ConnectionID, scope.ActorID).Scan(&access)
	if e != nil || access != scope.Access {
		return nil, ErrDenied
	}
	if Writing(method) {
		if _, e = tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, scope.BookID); e != nil {
			return nil, dbError(e)
		}
	} else {
		var locked string
		if e = tx.QueryRow(ctx, "SELECT id::text FROM public.books WHERE tenant_id=$1 AND id=$2 FOR SHARE", scope.TenantID, scope.BookID).Scan(&locked); e != nil {
			return nil, ErrDenied
		}
	}
	result, e := dispatch(ctx, tx, scope, method, input)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		var rejected *pgconn.PgError
		if errors.As(e, &rejected) && (strings.HasPrefix(rejected.Code, "22") || strings.HasPrefix(rejected.Code, "23") || rejected.Code == "25001") {
			return nil, dbError(e)
		}
		return nil, ErrUncertain
	}
	return result, nil
}

func dispatch(ctx context.Context, tx pgx.Tx, s Scope, method string, input []byte) (json.RawMessage, error) {
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
		rows, e := tx.Query(ctx, "SELECT id::text FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND sequence>$3 ORDER BY sequence LIMIT $4", s.TenantID, s.BookID, q.After, q.Limit+1)
		if e != nil {
			return nil, dbError(e)
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return nil, dbError(e)
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, dbError(e)
		}
		more := len(ids) > q.Limit
		if more {
			ids = ids[:q.Limit]
		}
		entries := []Entry{}
		next := q.After
		for _, id := range ids {
			v, e := readEntry(ctx, tx, s, id)
			if e != nil {
				return nil, dbError(e)
			}
			entries = append(entries, v)
			next = v.Sequence
		}
		return raw(struct {
			Entries   []Entry `json:"entries"`
			NextAfter int64   `json:"next_after"`
			HasMore   bool    `json:"has_more"`
		}{entries, next, more}), nil
	case "get_balance":
		var q AccountQuery
		if decode(&q) != nil || !ValidID(q.AccountID) || (q.Through != "" && !ValidDate(q.Through)) {
			return nil, ErrInvalid
		}
		t, e := readTrial(ctx, tx, s, q.Through)
		if e != nil {
			return nil, dbError(e)
		}
		for _, b := range t.Balances {
			if b.AccountID == q.AccountID {
				return raw(b), nil
			}
		}
		return nil, ErrNotFound
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
		if decode(&q) != nil || !validAccount(q.Class, q.Code, q.Name) {
			return nil, ErrInvalid
		}
		return write(ctx, tx, s, method, q.RequestKey, raw(q), func(receipt string) (json.RawMessage, error) {
			id := ID()
			if _, e := tx.Exec(ctx, "INSERT INTO public.accounts(tenant_id,book_id,id,class) VALUES($1,$2,$3,$4)", s.TenantID, s.BookID, id, q.Class); e != nil {
				return nil, e
			}
			if _, e := tx.Exec(ctx, "INSERT INTO public.account_revisions(tenant_id,book_id,account_id,revision,code,name,actor_id) VALUES($1,$2,$3,1,$4,$5,$6)", s.TenantID, s.BookID, id, q.Code, q.Name, s.ActorID); e != nil {
				return nil, e
			}
			return raw(Account{id, q.Class, q.Code, q.Name, 1}), nil
		})
	case "revise_account":
		var q ReviseAccount
		if decode(&q) != nil || !ValidID(q.AccountID) || q.ExpectedRevision < 1 || !codePattern.MatchString(q.Code) || len(q.Name) < 1 || len(q.Name) > 120 {
			return nil, ErrInvalid
		}
		return write(ctx, tx, s, method, q.RequestKey, raw(q), func(receipt string) (json.RawMessage, error) {
			var class string
			var revision int64
			if e := tx.QueryRow(ctx, "SELECT class,revision FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, q.AccountID).Scan(&class, &revision); e != nil {
				return nil, e
			}
			if revision != q.ExpectedRevision {
				return nil, ErrConflict
			}
			if _, e := tx.Exec(ctx, "INSERT INTO public.account_revisions(tenant_id,book_id,account_id,revision,code,name,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7)", s.TenantID, s.BookID, q.AccountID, revision+1, q.Code, q.Name, s.ActorID); e != nil {
				return nil, e
			}
			return raw(Account{q.AccountID, class, q.Code, q.Name, revision + 1}), nil
		})
	case "post_journal":
		var q PostJournal
		if decode(&q) != nil || validatePost(q) != nil {
			return nil, ErrInvalid
		}
		return write(ctx, tx, s, method, q.RequestKey, raw(q), func(receipt string) (json.RawMessage, error) {
			v := Entry{ID: ID(), ReceiptID: receipt, ActorID: s.ActorID, EconomicDate: q.EconomicDate, AccountingDate: q.AccountingDate, Description: q.Description, Lines: []Line{}}
			for _, l := range q.Lines {
				v.Lines = append(v.Lines, Line{ID: ID(), AccountID: l.AccountID, Side: l.Side, AmountMinor: l.AmountMinor, Memo: l.Memo})
			}
			if e := insertEntry(ctx, tx, s, &v); e != nil {
				return nil, e
			}
			return raw(v), nil
		})
	case "reverse_journal":
		var q ReverseJournal
		if decode(&q) != nil || !ValidID(q.EntryID) || !ValidDate(q.AccountingDate) || len(q.Reason) < 1 || len(q.Reason) > 4000 {
			return nil, ErrInvalid
		}
		return write(ctx, tx, s, method, q.RequestKey, raw(q), func(receipt string) (json.RawMessage, error) {
			old, e := readEntry(ctx, tx, s, q.EntryID)
			if e != nil {
				return nil, e
			}
			v := Entry{ID: ID(), ReceiptID: receipt, ActorID: s.ActorID, EconomicDate: old.EconomicDate, AccountingDate: q.AccountingDate, Description: q.Reason, ReversalOf: old.ID, Lines: []Line{}}
			for _, l := range old.Lines {
				side := "debit"
				if l.Side == "debit" {
					side = "credit"
				}
				v.Lines = append(v.Lines, Line{ID: ID(), AccountID: l.AccountID, Side: side, AmountMinor: l.AmountMinor, Memo: l.Memo, ReversesLineID: l.ID})
			}
			if e := insertEntry(ctx, tx, s, &v); e != nil {
				return nil, e
			}
			return raw(v), nil
		})
	}
	return nil, ErrInvalid
}

func validAccount(class, code, name string) bool {
	if !codePattern.MatchString(code) || len(name) < 1 || len(name) > 120 {
		return false
	}
	switch class {
	case "asset", "liability", "equity", "income", "expense":
		return true
	}
	return false
}
func validatePost(q PostJournal) error {
	if !validKey(q.RequestKey) || !ValidDate(q.EconomicDate) || !ValidDate(q.AccountingDate) || len(q.Description) < 1 || len(q.Description) > 4000 || len(q.Lines) < 2 || len(q.Lines) > 256 {
		return ErrInvalid
	}
	total := new(big.Int)
	for _, l := range q.Lines {
		n, e := positiveAmount(l.AmountMinor)
		if e != nil || !ValidID(l.AccountID) || len(l.Memo) > 1000 {
			return ErrInvalid
		}
		v := big.NewInt(n)
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
func write(ctx context.Context, tx pgx.Tx, s Scope, method, key string, request []byte, apply func(string) (json.RawMessage, error)) (json.RawMessage, error) {
	if !validKey(key) {
		return nil, ErrInvalid
	}
	if _, e := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", s.TenantID, s.BookID); e != nil {
		return nil, dbError(e)
	}
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
	id := ID()
	result, e := apply(id)
	if e != nil {
		if errors.Is(e, ErrConflict) {
			return nil, e
		}
		return nil, dbError(e)
	}
	_, e = tx.Exec(ctx, "INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", s.TenantID, s.BookID, id, s.ActorID, key, method, request, []byte(result))
	if e != nil {
		return nil, dbError(e)
	}
	return result, nil
}
func readBook(ctx context.Context, tx pgx.Tx, s Scope) (Book, error) {
	var b Book
	e := tx.QueryRow(ctx, "SELECT tenant_id::text,id::text,name,currency,scale,created_at FROM public.books WHERE tenant_id=$1 AND id=$2", s.TenantID, s.BookID).Scan(&b.TenantID, &b.ID, &b.Name, &b.Currency, &b.Scale, &b.CreatedAt)
	return b, e
}
func readAccounts(ctx context.Context, tx pgx.Tx, s Scope) ([]Account, error) {
	rows, e := tx.Query(ctx, "SELECT id::text,class,code,name,revision FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 ORDER BY code,id", s.TenantID, s.BookID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		if e = rows.Scan(&a.ID, &a.Class, &a.Code, &a.Name, &a.Revision); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func readEntry(ctx context.Context, tx pgx.Tx, s Scope, id string) (Entry, error) {
	var v Entry
	v.Lines = []Line{}
	e := tx.QueryRow(ctx, `SELECT id::text,sequence,receipt_id::text,actor_id::text,economic_date::text,accounting_date::text,
 description,coalesce(reversal_of::text,''),recorded_at FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND id=$3`, s.TenantID, s.BookID, id).Scan(&v.ID, &v.Sequence, &v.ReceiptID, &v.ActorID, &v.EconomicDate, &v.AccountingDate, &v.Description, &v.ReversalOf, &v.RecordedAt)
	if e != nil {
		return v, e
	}
	rows, e := tx.Query(ctx, "SELECT id::text,account_id::text,side,amount_minor::text,memo,coalesce(reverses_line_id::text,'') FROM public.lines WHERE tenant_id=$1 AND book_id=$2 AND entry_id=$3 ORDER BY position", s.TenantID, s.BookID, id)
	if e != nil {
		return v, e
	}
	defer rows.Close()
	for rows.Next() {
		var l Line
		if e = rows.Scan(&l.ID, &l.AccountID, &l.Side, &l.AmountMinor, &l.Memo, &l.ReversesLineID); e != nil {
			return v, e
		}
		v.Lines = append(v.Lines, l)
	}
	return v, rows.Err()
}
func insertEntry(ctx context.Context, tx pgx.Tx, s Scope, v *Entry) error {
	e := tx.QueryRow(ctx, `INSERT INTO public.entries(tenant_id,book_id,id,sequence,receipt_id,actor_id,economic_date,accounting_date,description,reversal_of)
 VALUES($1,$2,$3,1,$4,$5,$6,$7,$8,nullif($9,'')::uuid) RETURNING sequence,recorded_at`,
		s.TenantID, s.BookID, v.ID, v.ReceiptID, v.ActorID, v.EconomicDate, v.AccountingDate, v.Description, v.ReversalOf).Scan(&v.Sequence, &v.RecordedAt)
	if e != nil {
		return e
	}
	for i, l := range v.Lines {
		_, e = tx.Exec(ctx, `INSERT INTO public.lines(tenant_id,book_id,id,entry_id,position,account_id,side,amount_minor,memo,reverses_line_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::bigint,$9,nullif($10,'')::uuid)`, s.TenantID, s.BookID, l.ID, v.ID, i+1, l.AccountID, l.Side, l.AmountMinor, l.Memo, l.ReversesLineID)
		if e != nil {
			return e
		}
	}
	return nil
}
func readTrial(ctx context.Context, tx pgx.Tx, s Scope, through string) (Trial, error) {
	b, e := readBook(ctx, tx, s)
	if e != nil {
		return Trial{}, e
	}
	out := Trial{Currency: b.Currency, Scale: b.Scale, Through: through, Balances: []Balance{}}
	rows, e := tx.Query(ctx, `SELECT a.id::text,a.code,a.name,coalesce(sum(CASE WHEN e.id IS NULL THEN 0
 WHEN l.side='debit' THEN l.amount_minor::numeric ELSE -l.amount_minor::numeric END),0)::text
 FROM public.accounts a LEFT JOIN public.lines l ON l.tenant_id=a.tenant_id AND l.book_id=a.book_id AND l.account_id=a.id
 LEFT JOIN public.entries e ON e.tenant_id=l.tenant_id AND e.book_id=l.book_id AND e.id=l.entry_id
 AND($3='' OR e.accounting_date<=nullif($3,'')::date)
 WHERE a.tenant_id=$1 AND a.book_id=$2 GROUP BY a.id,a.code,a.name ORDER BY a.code,a.id`, s.TenantID, s.BookID, through)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var v Balance
		if e = rows.Scan(&v.AccountID, &v.Code, &v.Name, &v.DebitPositiveMinor); e != nil {
			return out, e
		}
		out.Balances = append(out.Balances, v)
	}
	return out, rows.Err()
}
