package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// ReviseBookSettings changes presentation and calendar facts, not journal effects.
// Omitted optional fields retain their previous accepted value. Empty strings,
// or a zero month/day pair, explicitly clear an optional setting.
type ReviseBookSettings struct {
	BookID               string  `json:"book_id"`
	RequestKey           string  `json:"idempotency_key"`
	Revision             int64   `json:"revision"`
	Name                 string  `json:"name"`
	Founder              string  `json:"founder"`
	Profile              *string `json:"profile,omitempty"`
	StartDate            *string `json:"start_date,omitempty"`
	FiscalYearStartMonth *int    `json:"fiscal_year_start_month,omitempty"`
	FiscalYearStartDay   *int    `json:"fiscal_year_start_day,omitempty"`
	AccountingTimezone   *string `json:"accounting_timezone,omitempty"`
}

func validBookText(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max
}

func applyBookSettings(book Book, q ReviseBookSettings) (Book, error) {
	if !validBookText(q.Name, 120) || strings.TrimSpace(q.Name) == "" || !validBookText(q.Founder, 120) {
		return Book{}, ErrInvalid
	}
	book.Name, book.Founder = q.Name, q.Founder
	if q.Profile != nil {
		if *q.Profile != "" && *q.Profile != "personal" && *q.Profile != "business" {
			return Book{}, ErrInvalid
		}
		book.Profile = *q.Profile
	}
	if q.StartDate != nil {
		if *q.StartDate != "" && !ValidDate(*q.StartDate) {
			return Book{}, ErrInvalid
		}
		book.StartDate = *q.StartDate
	}
	if (q.FiscalYearStartMonth == nil) != (q.FiscalYearStartDay == nil) {
		return Book{}, ErrInvalid
	}
	if q.FiscalYearStartMonth != nil {
		month, day := *q.FiscalYearStartMonth, *q.FiscalYearStartDay
		if month == 0 && day == 0 {
			book.FiscalYearStartMonth, book.FiscalYearStartDay = nil, nil
		} else {
			date := time.Date(2001, time.Month(month), day, 0, 0, 0, 0, time.UTC)
			if month < 1 || month > 12 || day < 1 || day > 31 || date.Month() != time.Month(month) || date.Day() != day {
				return Book{}, ErrInvalid
			}
			book.FiscalYearStartMonth, book.FiscalYearStartDay = q.FiscalYearStartMonth, q.FiscalYearStartDay
		}
	}
	if q.AccountingTimezone != nil {
		zone := *q.AccountingTimezone
		if zone != "" {
			if zone == "Local" || len(zone) > 100 {
				return Book{}, ErrInvalid
			}
			if _, err := time.LoadLocation(zone); err != nil {
				return Book{}, ErrInvalid
			}
		}
		book.AccountingTimezone = zone
	}
	revision := int64(1)
	if book.MetadataRevision != nil {
		revision = *book.MetadataRevision + 1
	}
	if revision < 1 {
		return Book{}, ErrInvalid
	}
	book.MetadataRevision = &revision
	return book, nil
}

// ReviseBook retains the accepted settings and outcome in the same immutable
// revision. Its actor must be the live owner identity from the enclosing lease.
// The new-effect admission callback is never invoked for a retained outcome.
func (a TenantAdmin) ReviseBook(ctx context.Context, actor string, q ReviseBookSettings, admit func(context.Context, pgx.Tx) error) (json.RawMessage, error) {
	if !a.valid() || !ValidID(actor) || !ValidID(q.BookID) || !validKey(q.RequestKey) || q.Revision < 0 {
		return nil, ErrInvalid
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, dbError(err)
	}
	defer tx.Rollback(context.Background())
	scope := Scope{TenantID: a.tenantID, BookID: q.BookID, ActorID: actor, Access: "write"}
	if err = setScope(ctx, tx, scope); err != nil {
		return nil, dbError(err)
	}
	var live string
	if err = tx.QueryRow(ctx, "SELECT access.require_owner($1,false)::text", a.tenantID).Scan(&live); err != nil || live != actor {
		return nil, ErrDenied
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "book-settings:"+a.tenantID+":"+actor+":"+q.RequestKey); err != nil {
		return nil, dbError(err)
	}
	canonical := raw(q)
	var previous, result []byte
	err = tx.QueryRow(ctx, "SELECT request_bytes,result_bytes FROM public.book_revisions WHERE tenant_id=$1 AND actor_id=$2 AND request_key=$3", a.tenantID, actor, q.RequestKey).Scan(&previous, &result)
	if err == nil {
		if !bytes.Equal(previous, canonical) {
			return nil, ErrConflict
		}
		return result, dbError(tx.Commit(ctx))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, dbError(err)
	}
	if err = tx.QueryRow(ctx, "SELECT access.require_owner($1,true)::text", a.tenantID).Scan(&live); err != nil || live != actor {
		return nil, ErrDenied
	}
	if _, err = tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", a.tenantID, q.BookID); err != nil {
		return nil, dbError(err)
	}
	before, err := readBook(ctx, tx, scope)
	if err != nil {
		return nil, dbError(err)
	}
	revision := int64(0)
	if before.MetadataRevision != nil {
		revision = *before.MetadataRevision
	}
	if revision != q.Revision {
		return nil, ErrConflict
	}
	after, err := applyBookSettings(before, q)
	if err != nil {
		return nil, err
	}
	// A Book's type and accounting timezone are chosen once. The type decides
	// how the Book is kept; the timezone decides which day each entry falls on,
	// so every report and closed period uses one calendar.
	if before.Profile != "" && after.Profile != before.Profile ||
		before.AccountingTimezone != "" && after.AccountingTimezone != before.AccountingTimezone {
		return nil, ErrConflict
	}
	if admit != nil {
		if err = admit(ctx, tx); err != nil {
			return nil, ErrDenied
		}
	}
	result = raw(after)
	_, err = tx.Exec(ctx, `INSERT INTO public.book_revisions(tenant_id,book_id,revision,actor_id,request_key,prior_name,name,founder,profile,start_date,
 fiscal_year_start_month,fiscal_year_start_day,accounting_timezone,request_bytes,result_bytes,logo)
 VALUES($1,$2,$3,$4,$5,$6,$7,nullif($8,''),nullif($9,''),nullif($10,'')::date,$11,$12,nullif($13,''),$14,$15,$16)`,
		a.tenantID, q.BookID, *after.MetadataRevision, actor, q.RequestKey, before.Name, after.Name, after.Founder, after.Profile, after.StartDate,
		after.FiscalYearStartMonth, after.FiscalYearStartDay, after.AccountingTimezone, []byte(canonical), result, after.Logo)
	if err != nil {
		return nil, dbError(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, dbError(err)
	}
	return result, nil
}

func normalizeBookTables(s Snapshot, archive bool) (Snapshot, error) {
	old := releasedSnapshotTables
	book := bookSnapshotTables
	prior, details, current := proposalSnapshotTables, bookDetailSnapshotTables, snapshotTables
	if archive {
		old = append(append([]string{}, old...), archiveDocumentTables...)
		prior, details, current = priorArchiveTables, bookDetailArchiveTables, archiveTables
	}
	names, restore := current, current
	switch {
	case strings.HasSuffix(s.Format, "-v1"), strings.HasSuffix(s.Format, "-v2"):
		names, restore = old, prior
	case strings.HasSuffix(s.Format, "-v3"):
		names, restore = book, prior
	case strings.HasSuffix(s.Format, "-v4"):
		names, restore = prior, prior
	case strings.HasSuffix(s.Format, "-v5"):
		names, restore = details, details
	}
	if len(s.Tables) != len(names) {
		return Snapshot{}, ErrInvalid
	}
	for _, name := range names {
		if len(s.Tables[name]) < 2 {
			return Snapshot{}, ErrInvalid
		}
	}
	if len(names) != len(restore) {
		copy := map[string]json.RawMessage{}
		for k, v := range s.Tables {
			copy[k] = v
		}
		for _, name := range restore {
			if _, present := copy[name]; !present {
				copy[name] = json.RawMessage("[]")
			}
		}
		s.Tables = copy
	}
	return s, nil
}
