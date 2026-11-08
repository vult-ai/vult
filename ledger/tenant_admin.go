package ledger

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TenantAdmin administers one tenant's Books and connections. It deliberately
// excludes tenant provisioning and installation-wide backup and restore.
type TenantAdmin struct {
	db       administrationDatabase
	tenantID string
}

type administrationDatabase interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}
type transactionDatabase struct{ pgx.Tx }

func (t transactionDatabase) BeginTx(ctx context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return t.Tx.Begin(ctx)
}

// TenantTransaction binds an already-authorized transaction. Remote callers
// require a live database owner lease; row policies still enforce that lease.
// The caller controls the outer isolation level and commits or rolls back it.
func TenantTransaction(tx pgx.Tx, tenant string) (TenantAdmin, error) {
	if tx == nil || !ValidID(tenant) {
		return TenantAdmin{}, ErrDenied
	}
	return TenantAdmin{db: transactionDatabase{tx}, tenantID: tenant}, nil
}

// ForTenant binds administration to an existing tenant. The caller must first
// authorize ownership through trusted application policy; this method does not
// authenticate a user or establish membership. Never pass a request's tenant ID
// here as proof of authority.
func (a Admin) ForTenant(ctx context.Context, tenantID string) (TenantAdmin, error) {
	if a.DB == nil || !ValidID(tenantID) {
		return TenantAdmin{}, ErrDenied
	}
	var exists bool
	if e := a.DB.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.tenants WHERE id=$1)", tenantID).Scan(&exists); e != nil {
		return TenantAdmin{}, dbError(e)
	}
	if !exists {
		return TenantAdmin{}, ErrDenied
	}
	return TenantAdmin{db: a.DB, tenantID: tenantID}, nil
}

func (a TenantAdmin) valid() bool { return a.db != nil && ValidID(a.tenantID) }

func (a TenantAdmin) CreateBook(ctx context.Context, name, currency string, scale int) (Book, error) {
	if !a.valid() {
		return Book{}, ErrDenied
	}
	if len(strings.TrimSpace(name)) < 1 || len(name) > 120 || !currencyPattern.MatchString(currency) || scale < 0 || scale > 4 {
		return Book{}, ErrInvalid
	}
	b := Book{TenantID: a.tenantID, ID: ID(), Name: name, Currency: currency, Scale: scale}
	e := a.db.QueryRow(ctx, "INSERT INTO public.books(tenant_id,id,name,currency,scale) VALUES($1,$2,$3,$4,$5) RETURNING created_at", a.tenantID, b.ID, name, currency, scale).Scan(&b.CreatedAt)
	if e != nil {
		return Book{}, dbError(e)
	}
	return b, nil
}

func (a TenantAdmin) Books(ctx context.Context) ([]Book, error) {
	return a.books(ctx, 0)
}

func (a TenantAdmin) BooksBounded(ctx context.Context, limit int) ([]Book, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	return a.books(ctx, limit)
}
func (a TenantAdmin) books(ctx context.Context, limit int) ([]Book, error) {
	if !a.valid() {
		return nil, ErrDenied
	}
	query := "SELECT " + bookColumns + bookFrom + " WHERE b.tenant_id=$1 ORDER BY b.created_at,b.id"
	args := []any{a.tenantID}
	if limit > 0 {
		query += " LIMIT $2"
		args = append(args, limit+1)
	}
	rows, e := a.db.Query(ctx, query, args...)
	if e != nil {
		return nil, dbError(e)
	}
	defer rows.Close()
	out := []Book{}
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, dbError(err)
		}
		out = append(out, b)
	}
	if limit > 0 && len(out) > limit {
		return nil, ErrLimit
	}
	return out, dbError(rows.Err())
}

func (a TenantAdmin) Book(ctx context.Context, book string) (Book, error) {
	if !a.valid() {
		return Book{}, ErrDenied
	}
	if !ValidID(book) {
		return Book{}, ErrInvalid
	}
	b, err := scanBook(a.db.QueryRow(ctx, "SELECT "+bookColumns+bookFrom+" WHERE b.tenant_id=$1 AND b.id=$2", a.tenantID, book))
	return b, dbError(err)
}

func (a TenantAdmin) CreateConnection(ctx context.Context, book, name, access string) (Connection, error) {
	if !a.valid() {
		return Connection{}, ErrDenied
	}
	if !ValidID(book) || len(strings.TrimSpace(name)) < 1 || len(name) > 80 || (access != "read" && access != "write") {
		return Connection{}, ErrInvalid
	}
	c := Connection{ID: ID(), ActorID: ID(), BookID: book, Name: name, Access: access}
	e := a.db.QueryRow(ctx, `INSERT INTO public.connections(tenant_id,book_id,id,actor_id,name,access)
 SELECT tenant_id,id,$3,$4,$5,$6 FROM public.books WHERE tenant_id=$1 AND id=$2
 RETURNING tenant_id::text,created_at`, a.tenantID, book, c.ID, c.ActorID, name, access).Scan(&c.TenantID, &c.CreatedAt)
	if e != nil {
		return Connection{}, dbError(e)
	}
	return c, nil
}

func (a TenantAdmin) Connections(ctx context.Context) ([]Connection, error) {
	return a.connections(ctx, 0, false)
}
func (a TenantAdmin) ConnectionsBounded(ctx context.Context, limit int) ([]Connection, error) {
	return a.connectionsBounded(ctx, limit, false)
}

func (a TenantAdmin) ActiveConnectionsBounded(ctx context.Context, limit int) ([]Connection, error) {
	return a.connectionsBounded(ctx, limit, true)
}

func (a TenantAdmin) connectionsBounded(ctx context.Context, limit int, activeOnly bool) ([]Connection, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	return a.connections(ctx, limit, activeOnly)
}

func (a TenantAdmin) connections(ctx context.Context, limit int, activeOnly bool) ([]Connection, error) {
	if !a.valid() {
		return nil, ErrDenied
	}
	query := "SELECT id::text,tenant_id::text,book_id::text,actor_id::text,name,access,created_at,revoked_at FROM public.connections WHERE tenant_id=$1"
	if activeOnly {
		query += " AND revoked_at IS NULL"
	}
	query += " ORDER BY created_at,id"
	args := []any{a.tenantID}
	if limit > 0 {
		query += " LIMIT $2"
		args = append(args, limit+1)
	}
	rows, e := a.db.Query(ctx, query, args...)
	if e != nil {
		return nil, dbError(e)
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		var c Connection
		if e = rows.Scan(&c.ID, &c.TenantID, &c.BookID, &c.ActorID, &c.Name, &c.Access, &c.CreatedAt, &c.RevokedAt); e != nil {
			return nil, dbError(e)
		}
		out = append(out, c)
	}
	if limit > 0 && len(out) > limit {
		return nil, ErrLimit
	}
	return out, dbError(rows.Err())
}

func (a TenantAdmin) Connection(ctx context.Context, id string) (Connection, error) {
	return a.connection(ctx, id, false)
}

// ConnectionRecord includes revoked connections for owner inspection.
func (a TenantAdmin) ConnectionRecord(ctx context.Context, id string) (Connection, error) {
	return a.connection(ctx, id, true)
}

func (a TenantAdmin) connection(ctx context.Context, id string, includeRevoked bool) (Connection, error) {
	if !a.valid() {
		return Connection{}, ErrDenied
	}
	if !ValidID(id) {
		return Connection{}, ErrInvalid
	}
	var c Connection
	query := "SELECT id::text,tenant_id::text,book_id::text,actor_id::text,name,access,created_at,revoked_at FROM public.connections WHERE tenant_id=$1 AND id=$2"
	if !includeRevoked {
		query += " AND revoked_at IS NULL"
	}
	e := a.db.QueryRow(ctx, query, a.tenantID, id).Scan(&c.ID, &c.TenantID, &c.BookID, &c.ActorID, &c.Name, &c.Access, &c.CreatedAt, &c.RevokedAt)
	if e != nil {
		return Connection{}, dbError(e)
	}
	return c, nil
}

func (a TenantAdmin) Revoke(ctx context.Context, id string) error {
	if !a.valid() {
		return ErrDenied
	}
	if !ValidID(id) {
		return ErrInvalid
	}
	tag, e := a.db.Exec(ctx, "UPDATE public.connections SET revoked_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2 AND revoked_at IS NULL", a.tenantID, id)
	if e != nil {
		return dbError(e)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if e = a.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.connections WHERE tenant_id=$1 AND id=$2)", a.tenantID, id).Scan(&exists); e != nil {
			return dbError(e)
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

func (a TenantAdmin) ClosePeriod(ctx context.Context, book, from, through string) error {
	if !a.valid() {
		return ErrDenied
	}
	if !ValidID(book) || !ValidDate(from) || !ValidDate(through) || from > through {
		return ErrInvalid
	}
	tx, e := a.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return dbError(e)
	}
	defer tx.Rollback(context.Background())
	var id string
	if e = tx.QueryRow(ctx, "SELECT id::text FROM public.books WHERE tenant_id=$1 AND id=$2", a.tenantID, book).Scan(&id); e != nil {
		return dbError(e)
	}
	if e = setScope(ctx, tx, Scope{TenantID: a.tenantID, BookID: book}); e != nil {
		return dbError(e)
	}
	if _, e = tx.Exec(ctx, "INSERT INTO public.closed_periods(tenant_id,book_id,id,start_date,end_date) VALUES($1,$2,$3,$4,$5)", a.tenantID, book, ID(), from, through); e != nil {
		return dbError(e)
	}
	return dbError(tx.Commit(ctx))
}

func (a TenantAdmin) Export(ctx context.Context, book string) (Export, error) {
	if !a.valid() {
		return Export{}, ErrDenied
	}
	if !ValidID(book) {
		return Export{}, ErrInvalid
	}
	tx, e := a.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return Export{}, dbError(e)
	}
	defer tx.Rollback(context.Background())
	out, e := exportBook(ctx, tx, Scope{TenantID: a.tenantID, BookID: book})
	if e != nil {
		return Export{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return Export{}, ErrUncertain
	}
	return out, nil
}

func (a TenantAdmin) Verify(ctx context.Context, book string) (Verification, error) {
	v, e := a.Export(ctx, book)
	if e != nil {
		return Verification{}, e
	}
	return verifyExport(v)
}
