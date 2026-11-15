package ledger

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// BookDeletionWait is how long a deleted Book stays locked before it is
// erased. The owner can cancel until then.
const BookDeletionWait = 7 * 24 * time.Hour

// BookRefusal is a refused archive or deletion step, with what the owner
// reads. Code says which kind: "proof" (a passkey assertion for this Book is
// needed), "name", "busy", "linked", "early", "limit" or "locked".
type BookRefusal struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func (e *BookRefusal) Error() string { return e.Reason }
func (e *BookRefusal) Unwrap() error { return ErrInvalid }

// ErrBookProofRequired is a deletion step taken without a fresh passkey
// assertion made for that Book.
var ErrBookProofRequired = errors.New("a passkey assertion for this Book is required")

func bookRefusal(e error) error {
	var p *pgconn.PgError
	if errors.As(e, &p) && strings.HasPrefix(p.Code, "VB") {
		code := map[string]string{"VB401": "proof", "VB409": "busy", "VB422": "name", "VB423": "locked", "VB425": "early", "VB429": "limit"}[p.Code]
		if code == "" {
			return errors.New("database operation failed")
		}
		if code == "busy" && strings.Contains(p.Message, "linked to") {
			code = "linked"
		}
		return &BookRefusal{Code: code, Reason: p.Message}
	}
	return dbError(e)
}

// ClientFacts is where a request came from, for the deletion record: the
// network address and the browser or program. Never financial data.
type ClientFacts struct {
	Address string
	Agent   string
}

func (c ClientFacts) clean() (string, string) {
	clip := func(s string, n int) string {
		s = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, s)
		if len(s) > n {
			s = s[:n]
		}
		return s
	}
	return clip(c.Address, 64), clip(c.Agent, 200)
}

// BookDeletionSummary is what deleting a Book removes.
type BookDeletionSummary struct {
	BookID          string   `json:"book_id"`
	Name            string   `json:"name"`
	Entries         int64    `json:"entries"`
	Documents       int64    `json:"documents"`
	BankConnections int64    `json:"bank_connections"`
	Accounts        int64    `json:"accounts"`
	LinkedBooks     []string `json:"linked_books"`
}

// BookDeletion is a Book being deleted.
type BookDeletion struct {
	State        string     `json:"state"`
	RequestedAt  time.Time  `json:"requested_at"`
	ConfirmAfter *time.Time `json:"confirm_after,omitempty"`
	EraseAfter   *time.Time `json:"erase_after,omitempty"`
	BanksWaiting int64      `json:"banks_waiting"`
}

// BookLifecycle says whether a Book is archived or being deleted.
type BookLifecycle struct {
	BookID     string        `json:"book_id"`
	ArchivedAt *time.Time    `json:"archived_at,omitempty"`
	Deletion   *BookDeletion `json:"deletion,omitempty"`
}

// BookDeletionOutcome is the state a deletion step left.
type BookDeletionOutcome struct {
	BookID            string     `json:"book_id"`
	State             string     `json:"state"`
	ConfirmAfter      *time.Time `json:"confirm_after,omitempty"`
	EraseAfter        *time.Time `json:"erase_after,omitempty"`
	BanksDisconnected int        `json:"banks_disconnected"`
}

// BookDeletionEvent is one line of the deletion record.
type BookDeletionEvent struct {
	BookID        string    `json:"book_id"`
	BookName      string    `json:"book_name"`
	Event         string    `json:"event"`
	Actor         string    `json:"actor"`
	Via           string    `json:"via"`
	ClientAddress string    `json:"client_address"`
	Client        string    `json:"client"`
	Detail        string    `json:"detail"`
	RecordedAt    time.Time `json:"recorded_at"`
}

func scanOutcome(row pgx.Row, book string) (BookDeletionOutcome, error) {
	out := BookDeletionOutcome{BookID: book}
	err := row.Scan(&out.State, &out.ConfirmAfter, &out.EraseAfter, &out.BanksDisconnected)
	return out, err
}

// ArchiveBook hides a Book from menus, or puts it back. Nothing in it
// changes.
func (o TenantAdmin) ArchiveBook(ctx context.Context, book string, archive bool, from ClientFacts) error {
	if !ValidID(book) {
		return ErrInvalid
	}
	address, agent := from.clean()
	var done bool
	return bookRefusal(o.db.QueryRow(ctx, "SELECT public.archive_book($1,$2,$3,$4,$5)", o.tenantID, book, archive, address, agent).Scan(&done))
}

// BookDeletionSummary counts what deleting the Book removes.
func (o TenantAdmin) BookDeletionSummary(ctx context.Context, book string) (BookDeletionSummary, error) {
	if !ValidID(book) {
		return BookDeletionSummary{}, ErrInvalid
	}
	out := BookDeletionSummary{BookID: book}
	err := o.db.QueryRow(ctx, "SELECT name,entries,documents,bank_connections,accounts,linked_books FROM public.book_deletion_summary($1,$2)", o.tenantID, book).
		Scan(&out.Name, &out.Entries, &out.Documents, &out.BankConnections, &out.Accounts, &out.LinkedBooks)
	if out.LinkedBooks == nil {
		out.LinkedBooks = []string{}
	}
	return out, bookRefusal(err)
}

// RequestBookDeletion locks a Book for erasure. proof is the hash of a
// verified passkey proof made for this Book in this session; typed must be
// the Book's exact name.
func (o TenantAdmin) RequestBookDeletion(ctx context.Context, book, proof, typed string, from ClientFacts) (BookDeletionOutcome, error) {
	if !ValidID(book) || len(typed) > 240 {
		return BookDeletionOutcome{}, ErrInvalid
	}
	address, agent := from.clean()
	out, err := scanOutcome(o.db.QueryRow(ctx, "SELECT * FROM public.request_book_deletion($1,$2,$3,$4,$5,$6)", o.tenantID, book, proof, typed, address, agent), book)
	return out, bookRefusal(err)
}

// ConfirmBookDeletion starts the waiting period of a held deletion.
func (o TenantAdmin) ConfirmBookDeletion(ctx context.Context, book, proof string, from ClientFacts) (BookDeletionOutcome, error) {
	if !ValidID(book) {
		return BookDeletionOutcome{}, ErrInvalid
	}
	address, agent := from.clean()
	out, err := scanOutcome(o.db.QueryRow(ctx, "SELECT * FROM public.confirm_book_deletion($1,$2,$3,$4,$5)", o.tenantID, book, proof, address, agent), book)
	return out, bookRefusal(err)
}

// CancelBookDeletion stops a deletion before erasure starts.
func (o TenantAdmin) CancelBookDeletion(ctx context.Context, book string, from ClientFacts) error {
	if !ValidID(book) {
		return ErrInvalid
	}
	address, agent := from.clean()
	_, err := o.db.Exec(ctx, "SELECT public.cancel_book_deletion($1,$2,$3,$4)", o.tenantID, book, address, agent)
	return bookRefusal(err)
}

// BookLifecycles lists the Vult's archived Books and the one being deleted.
func (o TenantAdmin) BookLifecycles(ctx context.Context) ([]BookLifecycle, error) {
	rows, err := o.db.Query(ctx, "SELECT book_id::text,archived_at,deletion_state,requested_at,confirm_after,erase_after,banks_waiting FROM public.book_lifecycle($1)", o.tenantID)
	if err != nil {
		return nil, bookRefusal(err)
	}
	defer rows.Close()
	out := []BookLifecycle{}
	for rows.Next() {
		var v BookLifecycle
		var state *string
		var requested *time.Time
		var d BookDeletion
		if err := rows.Scan(&v.BookID, &v.ArchivedAt, &state, &requested, &d.ConfirmAfter, &d.EraseAfter, &d.BanksWaiting); err != nil {
			return nil, bookRefusal(err)
		}
		if state != nil && requested != nil {
			d.State, d.RequestedAt = *state, *requested
			v.Deletion = &d
		}
		out = append(out, v)
	}
	return out, bookRefusal(rows.Err())
}

// BookDeletionHistory is the Vult's deletion record, newest first.
func (o TenantAdmin) BookDeletionHistory(ctx context.Context, limit int) ([]BookDeletionEvent, error) {
	return deletionHistory(ctx, o.db, o.tenantID, limit)
}

func deletionHistory(ctx context.Context, db administrationDatabase, tenant string, limit int) ([]BookDeletionEvent, error) {
	rows, err := db.Query(ctx, "SELECT book_ref::text,book_name,event,actor,via,client_address,client,detail,recorded_at FROM public.book_deletion_history($1,$2)", tenant, limit)
	if err != nil {
		return nil, bookRefusal(err)
	}
	defer rows.Close()
	out := []BookDeletionEvent{}
	for rows.Next() {
		var v BookDeletionEvent
		if err := rows.Scan(&v.BookID, &v.BookName, &v.Event, &v.Actor, &v.Via, &v.ClientAddress, &v.Client, &v.Detail, &v.RecordedAt); err != nil {
			return nil, bookRefusal(err)
		}
		out = append(out, v)
	}
	return out, bookRefusal(rows.Err())
}

func (a Admin) bookTenant(ctx context.Context, book string) (string, error) {
	if a.DB == nil || !ValidID(book) {
		return "", ErrInvalid
	}
	var tenant string
	if err := a.DB.QueryRow(ctx, "SELECT tenant_id::text FROM public.books WHERE id=$1", book).Scan(&tenant); err != nil {
		return "", dbError(err)
	}
	return tenant, nil
}

// OperatorDeleteBook is the machine operator's deletion, from an
// administrator command. It needs the database's administrator connection.
// With now, erasure is due at once instead of after the waiting period.
func (a Admin) OperatorDeleteBook(ctx context.Context, book, typed string, now bool) (BookDeletionOutcome, error) {
	tenant, err := a.bookTenant(ctx, book)
	if err != nil {
		return BookDeletionOutcome{}, err
	}
	out, err := scanOutcome(a.DB.QueryRow(ctx, "SELECT * FROM public.operator_book_deletion($1,$2,$3,$4)", tenant, book, typed, now), book)
	return out, bookRefusal(err)
}

// OperatorCancelBookDeletion stops a deletion before erasure starts.
func (a Admin) OperatorCancelBookDeletion(ctx context.Context, book string) error {
	tenant, err := a.bookTenant(ctx, book)
	if err != nil {
		return err
	}
	_, err = a.DB.Exec(ctx, "SELECT public.operator_cancel_book_deletion($1,$2)", tenant, book)
	return bookRefusal(err)
}

// OperatorBookDeletionHistory is a Vult's deletion record, for the
// operator.
func (a Admin) OperatorBookDeletionHistory(ctx context.Context, tenant string, limit int) ([]BookDeletionEvent, error) {
	if a.DB == nil || !ValidID(tenant) {
		return nil, ErrInvalid
	}
	return deletionHistory(ctx, a.DB, tenant, limit)
}

// ErasureDatabase runs erasure: the service's own connection. Erasure
// itself checks that the waiting period is over.
type ErasureDatabase interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// ErasureTarget is a Book due for erasure.
type ErasureTarget struct {
	TenantID string
	BookID   string
	State    string
}

// BookErasuresDue lists Books whose erasure is due or under way.
func BookErasuresDue(ctx context.Context, db ErasureDatabase, limit int) ([]ErasureTarget, error) {
	rows, err := db.Query(ctx, "SELECT tenant_id::text,book_id::text,state FROM public.book_erasures_due($1)", limit)
	if err != nil {
		return nil, bookRefusal(err)
	}
	defer rows.Close()
	var out []ErasureTarget
	for rows.Next() {
		var v ErasureTarget
		if err := rows.Scan(&v.TenantID, &v.BookID, &v.State); err != nil {
			return nil, bookRefusal(err)
		}
		out = append(out, v)
	}
	return out, bookRefusal(rows.Err())
}

// ErasurePlan is what erasure removes outside the database.
type ErasurePlan struct {
	Name        string
	StorageIDs  []string
	RequestedAt time.Time
}

// BeginBookErasure starts or resumes a due erasure.
func BeginBookErasure(ctx context.Context, db ErasureDatabase, tenant, book string) (ErasurePlan, error) {
	var p ErasurePlan
	err := db.QueryRow(ctx, "SELECT book_name,storage_ids,requested_at FROM public.begin_book_erasure($1,$2)", tenant, book).Scan(&p.Name, &p.StorageIDs, &p.RequestedAt)
	return p, bookRefusal(err)
}

// FinishBookErasure removes every row of the Book in one transaction.
func FinishBookErasure(ctx context.Context, db ErasureDatabase, tenant, book string) (int64, error) {
	var n int64
	err := db.QueryRow(ctx, "SELECT public.finish_book_erasure($1,$2)", tenant, book).Scan(&n)
	return n, bookRefusal(err)
}
