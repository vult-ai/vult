package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Admin is installation-wide administration for trusted operators and the
// native local owner. Remote tenant owners must use TenantAdmin instead.
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
	rows, e := a.DB.Query(ctx, "SELECT "+bookColumns+bookFrom+" ORDER BY b.created_at,b.id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Book{}
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
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
		if e = a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.connections WHERE id=$1)", id).Scan(&exists); e != nil {
			return dbError(e)
		}
		if !exists {
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
	out, e := exportBook(ctx, tx, Scope{TenantID: tenant, BookID: book})
	if e != nil {
		return Export{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Export{}, ErrUncertain
	}
	return out, nil
}

func exportBook(ctx context.Context, tx pgx.Tx, s Scope) (Export, error) {
	out := Export{Format: journalExportFormat, Entries: []Entry{}}
	var e error
	if out.Book, e = readBook(ctx, tx, s); e != nil {
		return Export{}, dbError(e)
	}
	if out.Accounts, e = readAccounts(ctx, tx, s); e != nil {
		return Export{}, dbError(e)
	}
	e = visitExportEntries(ctx, tx, s, func(entry Entry) error {
		out.Entries = append(out.Entries, entry)
		return nil
	})
	if e != nil {
		return Export{}, e
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
	return verifyExport(v)
}

func verifyExport(v Export) (Verification, error) {
	out := Verification{BookID: v.Book.ID, Entries: len(v.Entries), Status: "verified"}
	seenEntries, seenLines := map[string]Entry{}, map[string]bool{}
	for _, entry := range v.Entries {
		q := PostJournal{RequestKey: "verification", EconomicDate: entry.EconomicDate, AccountingDate: entry.AccountingDate, Description: entry.Description, MeasurementPolicy: entry.MeasurementPolicy}
		for _, l := range entry.Lines {
			if seenLines[l.ID] || !ValidID(l.ID) {
				return Verification{}, ErrInvalid
			}
			seenLines[l.ID] = true
			out.Lines++
			q.Lines = append(q.Lines, PostLine{AccountID: l.AccountID, Side: l.Side, AmountMinor: l.AmountMinor, Memo: l.Memo,
				Native: l.Native, Contexts: l.Contexts, Valuation: l.Valuation})
		}
		if validatePost(q) != nil || entryHasMeasurements(entry) && validateMeasurements(q, v.Book) != nil {
			return Verification{}, ErrInvalid
		}
		if entry.ReversalOf != "" {
			old, ok := seenEntries[entry.ReversalOf]
			if !ok || !bytes.Equal(raw(old.MeasurementPolicy), raw(entry.MeasurementPolicy)) || len(old.Lines) != len(entry.Lines) || old.EconomicDate != entry.EconomicDate {
				return Verification{}, ErrInvalid
			}
			for i, l := range entry.Lines {
				o := old.Lines[i]
				if l.ReversesLineID != o.ID || l.AccountID != o.AccountID || l.AmountMinor != o.AmountMinor || l.Side == o.Side || l.Memo != o.Memo || !bytes.Equal(raw(l.Native), raw(o.Native)) || !bytes.Equal(raw(l.Contexts), raw(o.Contexts)) || !bytes.Equal(raw(l.Valuation), raw(o.Valuation)) {
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

var releasedSnapshotTables = []string{"tenants", "books", "accounts", "account_revisions", "requests", "entries", "lines", "closed_periods"}
var bookSnapshotTables = append(append([]string{}, releasedSnapshotTables...), "book_revisions")
var proposalSnapshotTables = append(append([]string{}, bookSnapshotTables...), "operation_proposals", "operation_decisions", "operation_consumptions")
var snapshotTables = append(append([]string{}, proposalSnapshotTables...), "book_addresses", "book_identity_revisions", "book_logo_images")

func readSnapshotRows(ctx context.Context, tx pgx.Tx, table string, scope Scope, order string, owner bool, budget int) ([]byte, error) {
	if budget < 2 {
		return nil, ErrLimit
	}
	if order == "" {
		return nil, ErrInvalid
	}
	projection := "to_jsonb(t)"
	if table == "books" {
		projection = "to_jsonb(t)-'write_epoch'"
	}
	// Omit only absent columns introduced after the original backup format.
	// Recursive null stripping would alter retained source and request objects.
	if optional := snapshotOptionalColumns[table]; len(optional) > 0 {
		names := make([]string, len(optional))
		for i, name := range optional {
			names[i] = "'" + name + "'"
		}
		projection = "(" + projection + ") - ARRAY(SELECT key FROM jsonb_each(to_jsonb(t)) WHERE key IN(" + strings.Join(names, ",") + ") AND value='null'::jsonb)"
	}
	query := "SELECT (" + projection + ")::text AS rowdata FROM public." + pgx.Identifier{table}.Sanitize() + " t"
	var args []any
	if scope.BookID != "" {
		args = []any{scope.TenantID, scope.BookID}
		switch table {
		case "tenants":
			args = args[:1]
			if owner {
				query = "SELECT to_jsonb(t)::text AS rowdata FROM access.book_archive_tenant($1) t"
			} else {
				query += " WHERE t.id=$1"
			}
		case "books":
			query += " WHERE t.tenant_id=$1 AND t.id=$2"
		default:
			query += " WHERE t.tenant_id=$1 AND t.book_id=$2"
		}
	}
	queryCtx, cancel := context.WithCancel(ctx)
	rows, err := tx.Query(queryCtx, query+" ORDER BY "+order, args...)
	if err != nil {
		cancel()
		return nil, err
	}
	defer func() {
		// Closing a live pgx result drains its remaining rows.
		cancel()
		rows.Close()
	}()
	data := []byte{'['}
	var compact bytes.Buffer
	for rows.Next() {
		var row []byte
		if err := rows.Scan(&row); err != nil {
			return nil, err
		}
		compact.Reset()
		if err := json.Compact(&compact, row); err != nil {
			return nil, err
		}
		row = compact.Bytes()
		separator := 0
		if len(data) > 1 {
			separator = 1
		}
		// Reserve the comma and closing bracket before growing the result buffer.
		if len(row) > budget-len(data)-separator-1 {
			return nil, ErrLimit
		}
		if separator != 0 {
			data = append(data, ',')
		}
		data = append(data, row...)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return append(data, ']'), nil
}
func (a Admin) Snapshot(ctx context.Context) (Snapshot, error) {
	tx, e := a.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return Snapshot{}, e
	}
	defer tx.Rollback(context.Background())
	names := migrationNames()
	applied, e := verifyReceipts(ctx, tx, names, false)
	if e != nil {
		return Snapshot{}, e
	}
	tables, format := releasedSnapshotTables, "vult-ledger-backup-v1"
	if slices.Contains(names[:applied], recordedBookMigration) {
		tables, format = snapshotTables, "vult-ledger-backup-v5"
	}
	var documentTable bool
	if e = tx.QueryRow(ctx, "SELECT to_regclass('public.documents') IS NOT NULL").Scan(&documentTable); e != nil {
		return Snapshot{}, dbError(e)
	}
	if documentTable {
		var originals bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.documents)").Scan(&originals); e != nil {
			return Snapshot{}, dbError(e)
		}
		if originals {
			return Snapshot{}, errors.New("documents require a portable archive backup")
		}
	}
	out := Snapshot{Format: format, SchemaSHA256: SchemaSHA256(), CreatedAt: time.Now().UTC(), Tables: map[string]json.RawMessage{}}
	total := 0
	for _, t := range tables {
		b, e := readSnapshotRows(ctx, tx, t, Scope{}, "rowdata", false, int(maxArchiveMetadata)-total)
		if e != nil {
			return Snapshot{}, e
		}
		total += len(b)
		out.Tables[t] = b
	}
	if len(raw(out)) > int(maxArchiveMetadata) {
		return Snapshot{}, ErrLimit
	}
	if e = tx.Commit(ctx); e != nil {
		return Snapshot{}, ErrUncertain
	}
	return out, nil
}
func (a Admin) Restore(ctx context.Context, s Snapshot) error {
	if (s.Format != "vult-ledger-backup-v1" && s.Format != "vult-ledger-backup-v2" && s.Format != "vult-ledger-backup-v3" && s.Format != "vult-ledger-backup-v4" && s.Format != "vult-ledger-backup-v5") || s.SchemaSHA256 != SchemaSHA256() {
		return ErrInvalid
	}
	var normalizeErr error
	s, normalizeErr = normalizeBookTables(s, false)
	if normalizeErr != nil {
		return normalizeErr
	}
	tx, err := a.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback(context.Background())
	tables := proposalSnapshotTables
	if s.Format == "vult-ledger-backup-v5" {
		tables = snapshotTables
	}
	if err = restoreSnapshotRows(ctx, tx, s, tables, false); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return ErrUncertain
	}
	return nil
}

func restoreSnapshotRows(ctx context.Context, tx pgx.Tx, s Snapshot, tableOrder []string, archive bool) error {
	var e error
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(769305014)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "LOCK TABLE public.books IN SHARE ROW EXCLUSIVE MODE"); e != nil {
		return dbError(e)
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
		"tenants":                 {"id", "created_at"},
		"books":                   {"tenant_id", "id", "name", "currency", "scale", "created_at"},
		"accounts":                {"tenant_id", "book_id", "id", "class", "created_at"},
		"account_revisions":       {"tenant_id", "book_id", "account_id", "revision", "code", "name", "actor_id", "recorded_at"},
		"requests":                {"tenant_id", "book_id", "id", "actor_id", "request_key", "operation", "request_bytes", "result_bytes", "recorded_at"},
		"entries":                 {"tenant_id", "book_id", "id", "sequence", "receipt_id", "actor_id", "economic_date", "accounting_date", "description", "reversal_of", "recorded_at"},
		"lines":                   {"tenant_id", "book_id", "id", "entry_id", "position", "account_id", "side", "amount_minor", "memo", "reverses_line_id"},
		"closed_periods":          {"tenant_id", "book_id", "id", "start_date", "end_date", "closed_at"},
		"book_logo_images":        {"tenant_id", "book_id", "id", "sha256", "content_type", "data"},
		"book_addresses":          {"tenant_id", "book_id", "code", "slug", "revision", "aliases"},
		"book_identity_revisions": {"tenant_id", "book_id", "revision", "id", "prior_kind", "new_kind", "prior_identity", "new_identity", "actor_id", "reason", "effective_date", "recorded_at"},
		"book_revisions":          {"tenant_id", "book_id", "revision", "actor_id", "request_key", "prior_name", "name", "founder", "profile", "start_date", "fiscal_year_start_month", "fiscal_year_start_day", "accounting_timezone", "request_bytes", "result_bytes", "recorded_at"},
		"documents":               {"tenant_id", "book_id", "id", "sequence", "sha256", "size_bytes", "media_type", "original_name", "storage_id", "key_id", "state", "actor_id", "created_at", "ready_at", "expires_at", "used_at"},
		"document_uploads":        {"tenant_id", "book_id", "id", "document_id", "storage_id", "actor_id", "connection_id", "receipt_id", "created_at", "expires_at"},
		"source_citations":        {"tenant_id", "book_id", "id", "source_id", "revision", "sequence", "entry_id", "position", "document_id", "location", "actor_id", "receipt_id", "reason", "recorded_at"},
		"evidence_links":          {"tenant_id", "book_id", "id", "link_id", "revision", "sequence", "entry_id", "document_id", "role", "action", "actor_id", "receipt_id", "reason", "recorded_at"},
		"operation_proposals":     {"tenant_id", "book_id", "id", "sequence", "actor_id", "connection_id", "request_key", "operation", "operation_key", "payload_bytes", "created_at", "expires_at"},
		"operation_decisions":     {"tenant_id", "book_id", "proposal_id", "revision", "owner_id", "owner_epoch", "request_key", "sha256", "decision", "reason", "executor_actor_id", "executor_connection_id", "recorded_at", "expires_at"},
		"operation_consumptions":  {"tenant_id", "book_id", "proposal_id", "decision_revision", "receipt_id", "actor_id", "recorded_at"},
	}
	for table, optional := range snapshotOptionalColumns {
		if table != "accounts" {
			columns[table] = append(columns[table], optional...)
		}
	}
	tables := map[string][]map[string]json.RawMessage{}
	normalizedTables := map[string]json.RawMessage{}
	for _, table := range tableOrder {
		data, ok := s.Tables[table]
		if !ok || len(data) > 64<<20 {
			return ErrInvalid
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(data, &rows) != nil || rows == nil {
			return ErrInvalid
		}
		for _, row := range rows {
			for _, name := range snapshotOptionalColumns[table] {
				value, exists := row[name]
				if exists && string(value) != "null" && strings.HasSuffix(s.Format, "-v1") {
					return ErrInvalid
				}
				if exists && string(value) == "null" {
					delete(row, name)
				}
			}
		}
		// Normalize absent optional columns and timestamp display offsets. Every
		// retained value still participates in the complete-row comparison below.
		normalizedTables[table], e = canonicalSnapshotTimes(raw(rows))
		if e != nil {
			return e
		}
		if table == "account_revisions" || table == "book_revisions" || table == "operation_decisions" { // Replay revisions in their original order.
			sort.SliceStable(rows, func(i, j int) bool {
				var x, y int64
				json.Unmarshal(rows[i]["revision"], &x)
				json.Unmarshal(rows[j]["revision"], &y)
				return x < y
			})
		}
		if table == "entries" || table == "source_citations" || table == "evidence_links" {
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
			if _, ok := row[c]; !ok && !optionalSnapshotColumn(table, c) {
				return ErrInvalid
			}
			list = append(list, pgx.Identifier{c}.Sanitize())
		}
		query := fmt.Sprintf("INSERT INTO public.%s(%s) SELECT %s FROM jsonb_populate_record(NULL::public.%s,$1::jsonb)", pgx.Identifier{table}.Sanitize(), strings.Join(list, ","), strings.Join(list, ","), pgx.Identifier{table}.Sanitize())
		_, err := tx.Exec(ctx, query, string(raw(row)))
		return dbError(err)
	}
	retainAddresses := strings.HasSuffix(s.Format, "-v5")
	if retainAddresses {
		if _, e = tx.Exec(ctx, "SELECT set_config('vult.restore_book_details','yes',true)"); e != nil {
			return dbError(e)
		}
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
	if retainAddresses {
		if _, e = tx.Exec(ctx, "SELECT set_config('vult.restore_book_details','',true)"); e != nil {
			return dbError(e)
		}
	}
	for _, table := range tableOrder[2:] {
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
		if retainAddresses && len(groups[key]["book_addresses"]) != 1 {
			return ErrInvalid
		}
		if e = setScope(ctx, tx, Scope{TenantID: key.tenant, BookID: key.book}); e != nil {
			return e
		}
		// Only a trusted operator session can select bulk range validation.
		// Every other deferred journal constraint still runs for every entry.
		if _, e = tx.Exec(ctx, "SELECT set_config('vult.restore_book',$1,true)", key.tenant+"/"+key.book); e != nil {
			return dbError(e)
		}
		for _, table := range tableOrder[2:] {
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
		// Always check the complete Book before leaving its scope. No financial
		// rows change afterward; an invalid historical prefix rolls back all Books.
		if _, e = tx.Exec(ctx, "SELECT public.ledger_validate_restored_book($1,$2)", key.tenant, key.book); e != nil {
			return dbError(e)
		}
		if _, e = tx.Exec(ctx, "SELECT set_config('vult.restore_book','',true)"); e != nil {
			return dbError(e)
		}
		if _, e = tx.Exec(ctx, "SET CONSTRAINTS ALL DEFERRED"); e != nil {
			return e
		}
	}
	// Restored sequences are permanent, while the serialization counter is local.
	// Advance it beyond all retained entries before accepting another posting.
	if _, e = tx.Exec(ctx, `UPDATE public.books b SET write_epoch=greatest(b.write_epoch,
 coalesce((SELECT max(e.sequence) FROM public.entries e WHERE e.tenant_id=b.tenant_id AND e.book_id=b.id),0),
 coalesce((SELECT max(d.sequence) FROM public.documents d WHERE d.tenant_id=b.tenant_id AND d.book_id=b.id),0),
 coalesce((SELECT max(c.sequence) FROM public.source_citations c WHERE c.tenant_id=b.tenant_id AND c.book_id=b.id),0),
 coalesce((SELECT max(l.sequence) FROM public.evidence_links l WHERE l.tenant_id=b.tenant_id AND l.book_id=b.id),0),
 coalesce((SELECT max(p.sequence) FROM public.operation_proposals p WHERE p.tenant_id=b.tenant_id AND p.book_id=b.id),0))`); e != nil {
		return e
	}
	// Compare every retained row before commit; reject any coercion or omission.
	compare := "SELECT $1::jsonb=$2::jsonb"
	if archive {
		// Older archives use a different row order. Compare both multisets,
		// including duplicates, while requiring every field to be equal.
		compare = `SELECT NOT EXISTS (
			(SELECT value FROM jsonb_array_elements($1::jsonb) EXCEPT ALL
			 SELECT value FROM jsonb_array_elements($2::jsonb))
			UNION ALL
			(SELECT value FROM jsonb_array_elements($2::jsonb) EXCEPT ALL
			 SELECT value FROM jsonb_array_elements($1::jsonb)))`
	}
	remaining := int(maxArchiveMetadata)
	for _, table := range tableOrder {
		order := "rowdata"
		if archive {
			order = archiveRowOrder[table]
		}
		actual, e := readSnapshotRows(ctx, tx, table, Scope{}, order, false, remaining)
		if e != nil {
			return e
		}
		remaining -= len(actual)
		actual, e = canonicalSnapshotTimes(actual)
		if e != nil {
			return e
		}
		var same bool
		if e = tx.QueryRow(ctx, compare, string(actual), string(normalizedTables[table])).Scan(&same); e != nil || !same {
			return errors.New("restored rows differ from the backup")
		}
	}
	return nil
}

// canonicalSnapshotTimes compares timestamp instants independently of a pool's
// display timezone. Only these top-level SQL timestamptz fields are normalized;
// nested JSON, dates and opaque request/result bytes retain their exact values.
func canonicalSnapshotTimes(data []byte) ([]byte, error) {
	var rows []map[string]json.RawMessage
	if json.Unmarshal(data, &rows) != nil || rows == nil {
		return nil, ErrInvalid
	}
	for _, row := range rows {
		if row == nil {
			return nil, ErrInvalid
		}
		for _, name := range []string{"created_at", "recorded_at", "closed_at", "ready_at", "expires_at", "used_at"} {
			value, exists := row[name]
			if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				continue
			}
			instant, err := parseSnapshotTime(value)
			if err != nil {
				return nil, err
			}
			row[name] = raw(instant.UTC().Format(time.RFC3339Nano))
		}
	}
	return json.Marshal(rows)
}

func parseSnapshotTime(value json.RawMessage) (time.Time, error) {
	var text string
	if json.Unmarshal(value, &text) != nil {
		return time.Time{}, ErrInvalid
	}
	if fraction := strings.IndexAny(text, ".,"); fraction >= 0 {
		// time.Parse truncates beyond nanoseconds. Check the original decimal
		// too, so nonzero finer precision can never disappear.
		for i := fraction + 1; i < len(text) && text[i] >= '0' && text[i] <= '9'; i++ {
			if i > fraction+6 && text[i] != '0' {
				return time.Time{}, ErrInvalid
			}
		}
	}
	instant, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		// PostgreSQL's XSD encoder retains seconds in a POSIX timezone offset
		// when needed, beyond RFC3339's minute offsets.
		instant, err = time.Parse("2006-01-02T15:04:05.999999999Z07:00:00", text)
	}
	if err != nil || instant.Nanosecond()%1000 != 0 {
		// PostgreSQL timestamps have microsecond precision. Never hide a
		// coercion of an unrepresentable input behind canonical formatting.
		return time.Time{}, ErrInvalid
	}
	return instant.UTC(), nil
}
