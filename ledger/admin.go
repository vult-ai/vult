package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Admin is used only after native owner authorization.
type Admin struct{ DB *pgxpool.Pool }

func (a Admin) EnsureTenant(ctx context.Context) (string, error) {
	tx, e := a.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return "", e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(769305014)"); e != nil {
		return "", e
	}
	var n int
	var id string
	if e = tx.QueryRow(ctx, "SELECT count(*),coalesce(min(id::text),'') FROM public.tenants").Scan(&n, &id); e != nil {
		return "", e
	}
	if n > 1 {
		return "", errors.New("installation has multiple tenants")
	}
	if n == 0 {
		id = ID()
		if _, e = tx.Exec(ctx, "INSERT INTO public.tenants(id) VALUES($1)", id); e != nil {
			return "", e
		}
	}
	return id, tx.Commit(ctx)
}
func (a Admin) CreateBook(ctx context.Context, name, currency string, scale int) (Book, error) {
	if len(strings.TrimSpace(name)) < 1 || len(name) > 120 || !currencyPattern.MatchString(currency) || scale < 0 || scale > 4 {
		return Book{}, ErrInvalid
	}
	tenant, e := a.EnsureTenant(ctx)
	if e != nil {
		return Book{}, e
	}
	b := Book{TenantID: tenant, ID: ID(), Name: name, Currency: currency, Scale: scale}
	e = a.DB.QueryRow(ctx, "INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,$3,$4,$5) RETURNING created_at", tenant, b.ID, name, currency, scale).Scan(&b.CreatedAt)
	return b, dbError(e)
}
func (a Admin) Books(ctx context.Context) ([]Book, error) {
	rows, e := a.DB.Query(ctx, "SELECT tenant_id::text,id::text,name,currency,scale,created_at FROM public.books ORDER BY created_at,id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Book{}
	for rows.Next() {
		var b Book
		if e = rows.Scan(&b.TenantID, &b.ID, &b.Name, &b.Currency, &b.Scale, &b.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (a Admin) CreateConnection(ctx context.Context, book, name, access string) (Connection, error) {
	if !ValidID(book) || len(strings.TrimSpace(name)) < 1 || len(name) > 80 || (access != "read" && access != "write") {
		return Connection{}, ErrInvalid
	}
	var c Connection
	c.ID = ID()
	c.ActorID = ID()
	c.BookID = book
	c.Name = name
	c.Access = access
	e := a.DB.QueryRow(ctx, `INSERT INTO public.connections(tenant_id,book_id,id,actor_id,name,access)
 SELECT tenant_id,id,$2,$3,$4,$5 FROM public.books WHERE id=$1
 RETURNING tenant_id::text,created_at`, book, c.ID, c.ActorID, name, access).Scan(&c.TenantID, &c.CreatedAt)
	return c, dbError(e)
}
func (a Admin) Connections(ctx context.Context) ([]Connection, error) {
	rows, e := a.DB.Query(ctx, "SELECT id::text,tenant_id::text,book_id::text,actor_id::text,name,access,created_at,revoked_at FROM public.connections ORDER BY created_at,id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		var c Connection
		if e = rows.Scan(&c.ID, &c.TenantID, &c.BookID, &c.ActorID, &c.Name, &c.Access, &c.CreatedAt, &c.RevokedAt); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (a Admin) Connection(ctx context.Context, id string) (Connection, error) {
	var c Connection
	if !ValidID(id) {
		return c, ErrInvalid
	}
	e := a.DB.QueryRow(ctx, "SELECT id::text,tenant_id::text,book_id::text,actor_id::text,name,access,created_at,revoked_at FROM public.connections WHERE id=$1 AND revoked_at IS NULL", id).Scan(&c.ID, &c.TenantID, &c.BookID, &c.ActorID, &c.Name, &c.Access, &c.CreatedAt, &c.RevokedAt)
	return c, dbError(e)
}
func (a Admin) Revoke(ctx context.Context, id string) error {
	if !ValidID(id) {
		return ErrInvalid
	}
	tag, e := a.DB.Exec(ctx, "UPDATE public.connections SET revoked_at=clock_timestamp() WHERE id=$1 AND revoked_at IS NULL", id)
	if e != nil {
		return dbError(e)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.connections WHERE id=$1)", id).Scan(&exists) != nil || !exists {
			return ErrNotFound
		}
	}
	return nil
}
func (a Admin) ClosePeriod(ctx context.Context, book, from, through string) error {
	if !ValidID(book) || !ValidDate(from) || !ValidDate(through) || from > through {
		return ErrInvalid
	}
	tx, e := a.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	var tenant string
	if e = tx.QueryRow(ctx, "SELECT tenant_id::text FROM public.books WHERE id=$1", book).Scan(&tenant); e != nil {
		return dbError(e)
	}
	if e = setScope(ctx, tx, Scope{TenantID: tenant, BookID: book}); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO public.closed_periods(tenant_id,book_id,id,start_date,end_date) VALUES($1,$2,$3,$4,$5)", tenant, book, ID(), from, through); e != nil {
		return dbError(e)
	}
	return dbError(tx.Commit(ctx))
}

type Export struct {
	Format   string    `json:"format"`
	Book     Book      `json:"book"`
	Accounts []Account `json:"accounts"`
	Entries  []Entry   `json:"entries"`
}

func (a Admin) Export(ctx context.Context, book string) (Export, error) {
	if !ValidID(book) {
		return Export{}, ErrInvalid
	}
	tx, e := a.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return Export{}, e
	}
	defer tx.Rollback(context.Background())
	var tenant string
	if e = tx.QueryRow(ctx, "SELECT tenant_id::text FROM public.books WHERE id=$1", book).Scan(&tenant); e != nil {
		return Export{}, dbError(e)
	}
	s := Scope{TenantID: tenant, BookID: book}
	out := Export{Format: "vult-journal-v1", Entries: []Entry{}}
	if out.Book, e = readBook(ctx, tx, s); e != nil {
		return Export{}, e
	}
	if out.Accounts, e = readAccounts(ctx, tx, s); e != nil {
		return Export{}, e
	}
	rows, e := tx.Query(ctx, "SELECT id::text FROM public.entries WHERE tenant_id=$1 AND book_id=$2 ORDER BY sequence LIMIT 100001", tenant, book)
	if e != nil {
		return Export{}, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return Export{}, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return Export{}, e
	}
	if len(ids) > 100000 {
		return Export{}, errors.New("export exceeds the supported entry limit")
	}
	for _, id := range ids {
		v, e := readEntry(ctx, tx, s, id)
		if e != nil {
			return Export{}, e
		}
		out.Entries = append(out.Entries, v)
	}
	if e = tx.Commit(ctx); e != nil {
		return Export{}, ErrUncertain
	}
	return out, nil
}

type Verification struct {
	BookID        string `json:"book_id"`
	Entries       int    `json:"entries"`
	Lines         int    `json:"lines"`
	JournalSHA256 string `json:"journal_sha256"`
	Status        string `json:"status"`
}

func (a Admin) Verify(ctx context.Context, book string) (Verification, error) {
	v, e := a.Export(ctx, book)
	if e != nil {
		return Verification{}, e
	}
	out := Verification{BookID: book, Entries: len(v.Entries), Status: "verified"}
	seenEntries, seenLines := map[string]Entry{}, map[string]bool{}
	for _, entry := range v.Entries {
		q := PostJournal{RequestKey: "verification", EconomicDate: entry.EconomicDate, AccountingDate: entry.AccountingDate, Description: entry.Description}
		for _, l := range entry.Lines {
			if seenLines[l.ID] || !ValidID(l.ID) {
				return Verification{}, ErrInvalid
			}
			seenLines[l.ID] = true
			out.Lines++
			q.Lines = append(q.Lines, PostLine{l.AccountID, l.Side, l.AmountMinor, l.Memo})
		}
		if validatePost(q) != nil {
			return Verification{}, ErrInvalid
		}
		if entry.ReversalOf != "" {
			old, ok := seenEntries[entry.ReversalOf]
			if !ok || len(old.Lines) != len(entry.Lines) || old.EconomicDate != entry.EconomicDate {
				return Verification{}, ErrInvalid
			}
			for i, l := range entry.Lines {
				o := old.Lines[i]
				if l.ReversesLineID != o.ID || l.AccountID != o.AccountID || l.AmountMinor != o.AmountMinor || l.Side == o.Side || l.Memo != o.Memo {
					return Verification{}, ErrInvalid
				}
			}
		}
		seenEntries[entry.ID] = entry
	}
	sum := sha256.Sum256(raw(v))
	out.JournalSHA256 = hex.EncodeToString(sum[:])
	return out, nil
}

// Snapshot is a versioned logical backup; connection grants are deliberately absent.
type Snapshot struct {
	Format       string                     `json:"format"`
	SchemaSHA256 string                     `json:"schema_sha256"`
	CreatedAt    time.Time                  `json:"created_at"`
	Tables       map[string]json.RawMessage `json:"tables"`
}

var snapshotTables = []string{"tenants", "books", "accounts", "account_revisions", "requests", "entries", "lines", "closed_periods"}

func snapshotRows(ctx context.Context, tx pgx.Tx, table string) ([]byte, error) {
	var b []byte
	projection := "to_jsonb(t)"
	if table == "books" {
		projection = "to_jsonb(t)-'write_epoch'"
	}
	e := tx.QueryRow(ctx, "SELECT convert_to(coalesce(jsonb_agg(rowdata ORDER BY rowdata::text),'[]'::jsonb)::text,'UTF8') FROM(SELECT "+projection+" AS rowdata FROM public."+pgx.Identifier{table}.Sanitize()+" t) rows").Scan(&b)
	return b, e
}
func (a Admin) Snapshot(ctx context.Context) (Snapshot, error) {
	tx, e := a.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return Snapshot{}, e
	}
	defer tx.Rollback(context.Background())
	out := Snapshot{Format: "vult-ledger-backup-v1", SchemaSHA256: SchemaSHA256(), CreatedAt: time.Now().UTC(), Tables: map[string]json.RawMessage{}}
	total := 0
	for _, t := range snapshotTables {
		b, e := snapshotRows(ctx, tx, t)
		if e != nil {
			return Snapshot{}, e
		}
		total += len(b)
		if total > 64<<20 {
			return Snapshot{}, errors.New("backup exceeds the supported size")
		}
		out.Tables[t] = b
	}
	if e = tx.Commit(ctx); e != nil {
		return Snapshot{}, ErrUncertain
	}
	return out, nil
}
func (a Admin) Restore(ctx context.Context, s Snapshot) error {
	if s.Format != "vult-ledger-backup-v1" || s.SchemaSHA256 != SchemaSHA256() || len(s.Tables) != len(snapshotTables) {
		return ErrInvalid
	}
	tx, e := a.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback(context.Background())
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(769305014)"); e != nil {
		return e
	}
	var occupied bool
	if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.books) OR EXISTS(SELECT 1 FROM public.connections)").Scan(&occupied); e != nil {
		return e
	}
	if occupied {
		return errors.New("restore requires an empty installation")
	}
	if _, e = tx.Exec(ctx, "DELETE FROM public.tenants"); e != nil {
		return e
	}
	columns := map[string][]string{
		"tenants":           {"id", "created_at"},
		"books":             {"tenant_id", "id", "name", "currency", "scale", "created_at"},
		"accounts":          {"tenant_id", "book_id", "id", "class", "created_at"},
		"account_revisions": {"tenant_id", "book_id", "account_id", "revision", "code", "name", "actor_id", "recorded_at"},
		"requests":          {"tenant_id", "book_id", "id", "actor_id", "request_key", "operation", "request_bytes", "result_bytes", "recorded_at"},
		"entries":           {"tenant_id", "book_id", "id", "sequence", "receipt_id", "actor_id", "economic_date", "accounting_date", "description", "reversal_of", "recorded_at"},
		"lines":             {"tenant_id", "book_id", "id", "entry_id", "position", "account_id", "side", "amount_minor", "memo", "reverses_line_id"},
		"closed_periods":    {"tenant_id", "book_id", "id", "start_date", "end_date", "closed_at"},
	}
	tables := map[string][]map[string]json.RawMessage{}
	for _, table := range snapshotTables {
		data, ok := s.Tables[table]
		if !ok || len(data) > 64<<20 {
			return ErrInvalid
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(data, &rows) != nil || rows == nil {
			return ErrInvalid
		}
		if table == "account_revisions" { // Replay revisions in their original order.
			sort.SliceStable(rows, func(i, j int) bool {
				var x, y int64
				json.Unmarshal(rows[i]["revision"], &x)
				json.Unmarshal(rows[j]["revision"], &y)
				return x < y
			})
		}
		if table == "entries" {
			sort.SliceStable(rows, func(i, j int) bool {
				var x, y int64
				json.Unmarshal(rows[i]["sequence"], &x)
				json.Unmarshal(rows[j]["sequence"], &y)
				return x < y
			})
		}
		tables[table] = rows
	}
	insert := func(table string, row map[string]json.RawMessage) error {
		list := []string{}
		for _, c := range columns[table] {
			if _, ok := row[c]; !ok {
				return ErrInvalid
			}
			list = append(list, pgx.Identifier{c}.Sanitize())
		}
		query := fmt.Sprintf("INSERT INTO public.%s(%s) SELECT %s FROM jsonb_populate_record(NULL::public.%s,$1::jsonb)", pgx.Identifier{table}.Sanitize(), strings.Join(list, ","), strings.Join(list, ","), pgx.Identifier{table}.Sanitize())
		_, err := tx.Exec(ctx, query, string(raw(row)))
		return dbError(err)
	}
	type bookKey struct{ tenant, book string }
	groups := map[bookKey]map[string][]map[string]json.RawMessage{}
	order := []bookKey{}
	for _, table := range []string{"tenants", "books"} {
		for _, row := range tables[table] {
			if e = insert(table, row); e != nil {
				return e
			}
			if table == "books" {
				var key bookKey
				if json.Unmarshal(row["tenant_id"], &key.tenant) != nil || json.Unmarshal(row["id"], &key.book) != nil {
					return ErrInvalid
				}
				groups[key] = map[string][]map[string]json.RawMessage{}
				order = append(order, key)
			}
		}
	}
	for _, table := range snapshotTables[2:] {
		for _, row := range tables[table] {
			var key bookKey
			if json.Unmarshal(row["tenant_id"], &key.tenant) != nil || json.Unmarshal(row["book_id"], &key.book) != nil {
				return ErrInvalid
			}
			group, ok := groups[key]
			if !ok {
				return ErrInvalid
			}
			group[table] = append(group[table], row)
		}
	}
	for _, key := range order {
		if e = setScope(ctx, tx, Scope{TenantID: key.tenant, BookID: key.book}); e != nil {
			return e
		}
		for _, table := range snapshotTables[2:] {
			for _, row := range groups[key][table] {
				if e = insert(table, row); e != nil {
					return e
				}
			}
		}
		// Deferred journal checks must run while this Book is still selected.
		// Every Book remains part of the same atomic restore transaction.
		if _, e = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); e != nil {
			return dbError(e)
		}
		if _, e = tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); e != nil {
			return e
		}
	}
	// Restored sequences are permanent, while the serialization counter is local.
	// Advance it beyond all retained entries before accepting another posting.
	if _, e = tx.Exec(ctx, `UPDATE public.books b SET write_epoch=greatest(b.write_epoch,
 coalesce((SELECT max(e.sequence) FROM public.entries e WHERE e.tenant_id=b.tenant_id AND e.book_id=b.id),0))`); e != nil {
		return e
	}
	// Compare every retained row before commit; reject any coercion or omission.
	for _, table := range snapshotTables {
		actual, e := snapshotRows(ctx, tx, table)
		if e != nil {
			return e
		}
		var same bool
		if e = tx.QueryRow(ctx, "SELECT $1::jsonb=$2::jsonb", string(actual), string(s.Tables[table])).Scan(&same); e != nil || !same {
			return errors.New("restored rows differ from the backup")
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return ErrUncertain
	}
	return nil
}
