package ledger

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/reporting"
)

const maxExportRecords = 100000
const journalExportFormat = "vult-journal-v1"

func visitExportEntries(ctx context.Context, tx pgx.Tx, scope Scope, write func(Entry) error) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM public.entries
		WHERE tenant_id=$1 AND book_id=$2 LIMIT $3) bounded`, scope.TenantID, scope.BookID, maxExportRecords+1).Scan(&count); err != nil {
		return dbError(err)
	}
	if count > maxExportRecords {
		return ErrLimit
	}
	if count == 0 {
		return nil
	}
	var after int64
	for {
		page, more, err := readEntryPage(ctx, tx, scope, after, 100)
		if err != nil {
			return dbError(err)
		}
		for _, entry := range page {
			if err := write(entry); err != nil {
				return err
			}
		}
		if !more {
			return nil
		}
		after = page[len(page)-1].Sequence
	}
}

func visitExportAccounts(ctx context.Context, tx pgx.Tx, scope Scope, write func(Account) error) error {
	rows, err := tx.Query(ctx, "SELECT "+accountColumns+` FROM public.accounts WHERE tenant_id=$1 AND book_id=$2
		ORDER BY code,id LIMIT $3`, scope.TenantID, scope.BookID, maxExportRecords+1)
	if err != nil {
		return dbError(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > maxExportRecords {
			return ErrLimit
		}
		account, err := scanAccount(rows)
		if err != nil {
			return dbError(err)
		}
		if err = write(account); err != nil {
			return err
		}
	}
	return dbError(rows.Err())
}

type jsonExport struct {
	dst io.Writer
	err error
}

func (w *jsonExport) text(value string) {
	if w.err == nil {
		_, w.err = io.WriteString(w.dst, value)
	}
}

func (w *jsonExport) value(value any) {
	if w.err == nil {
		var data []byte
		data, w.err = json.Marshal(value)
		if w.err == nil {
			_, w.err = w.dst.Write(data)
		}
	}
}

func writeJournalExport(ctx context.Context, tx pgx.Tx, scope Scope, book Book, dst io.Writer) error {
	// Match Export's field order, encoding and trailing newline used by the CLI.
	w := &jsonExport{dst: dst}
	w.text(`{"format":`)
	w.value(journalExportFormat)
	w.text(`,"book":`)
	w.value(book)
	w.text(`,"accounts":[`)
	separator := ""
	if err := visitExportAccounts(ctx, tx, scope, func(account Account) error {
		w.text(separator)
		w.value(account)
		separator = ","
		return w.err
	}); err != nil {
		return err
	}
	w.text(`],"entries":[`)
	separator = ""
	if err := visitExportEntries(ctx, tx, scope, func(entry Entry) error {
		w.text(separator)
		w.value(entry)
		separator = ","
		return w.err
	}); err != nil {
		return err
	}
	w.text("]}\n")
	return w.err
}

func writeBookCSV(ctx context.Context, tx pgx.Tx, scope Scope, book Book, format string, dst io.Writer) error {
	w := csv.NewWriter(dst)
	safe := reporting.SafeCSVText
	var err error
	switch format {
	case "accounts.csv":
		if err = w.Write([]string{"account_id", "code", "name", "class", "revision", "native_currency", "native_scale", "role"}); err != nil {
			return err
		}
		err = visitExportAccounts(ctx, tx, scope, func(a Account) error {
			scale := ""
			if a.Scale != nil {
				scale = strconv.Itoa(*a.Scale)
			}
			return w.Write([]string{a.ID, safe(a.Code), safe(a.Name), a.Class, strconv.FormatInt(a.Revision, 10), a.Currency, scale, a.Role})
		})
	case "transactions.csv":
		if err = w.Write([]string{"entry_id", "sequence", "economic_date", "accounting_date", "recorded_at", "description", "reversal_of", "currency", "scale", "debit_total_minor", "line_count"}); err != nil {
			return err
		}
		err = writeEntryCSVPages(ctx, tx, scope, book, w, writeTransactionCSVPage)
	case "journal-lines.csv":
		if err = w.Write([]string{"entry_id", "line_id", "position", "account_id", "side", "amount_minor", "currency", "scale", "memo", "reverses_line_id", "native_currency", "native_scale", "native_amount_minor"}); err != nil {
			return err
		}
		err = writeEntryCSVPages(ctx, tx, scope, book, w, writeLineCSVPage)
	default:
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

type entryCSVPage func(context.Context, pgx.Tx, Scope, Book, *csv.Writer, int64, int) (int64, int, error)

func writeEntryCSVPages(ctx context.Context, tx pgx.Tx, scope Scope, book Book, w *csv.Writer, write entryCSVPage) error {
	var after int64
	remaining := maxExportRecords
	for {
		limit := min(100, remaining+1)
		last, count, err := write(ctx, tx, scope, book, w, after, limit)
		if err != nil {
			return err
		}
		remaining -= count
		if remaining < 0 {
			return ErrLimit
		}
		if count < limit {
			return nil
		}
		after = last
	}
}

func writeTransactionCSVPage(ctx context.Context, tx pgx.Tx, scope Scope, book Book, w *csv.Writer, after int64, limit int) (int64, int, error) {
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rows, err := tx.Query(queryCtx, `WITH page AS MATERIALIZED (
		SELECT id,sequence,economic_date,accounting_date,recorded_at,description,reversal_of
		FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND sequence>$3
		ORDER BY sequence LIMIT $4
	)
	SELECT e.id::text,e.sequence,e.economic_date::text,e.accounting_date::text,e.recorded_at,
		e.description,coalesce(e.reversal_of::text,''),totals.debit_minor,totals.line_count
	FROM page e CROSS JOIN LATERAL (
		SELECT coalesce(sum(l.amount_minor) FILTER(WHERE l.side='debit'),0)::text AS debit_minor, count(*) AS line_count
		FROM public.lines l WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=e.id
	) totals ORDER BY e.sequence`, scope.TenantID, scope.BookID, after, limit)
	if err != nil {
		return 0, 0, dbError(err)
	}
	defer func() { cancel(); rows.Close() }()
	scale := strconv.Itoa(book.Scale)
	var count int
	for rows.Next() {
		var id, economicDate, accountingDate, description, reversalOf, debit string
		var recordedAt time.Time
		var lineCount int64
		if err := rows.Scan(&id, &after, &economicDate, &accountingDate, &recordedAt, &description, &reversalOf, &debit, &lineCount); err != nil {
			return 0, 0, dbError(err)
		}
		count++
		if err := w.Write([]string{id, strconv.FormatInt(after, 10), economicDate, accountingDate,
			recordedAt.UTC().Format(time.RFC3339Nano), reporting.SafeCSVText(description), reversalOf,
			book.Currency, scale, debit, strconv.FormatInt(lineCount, 10)}); err != nil {
			return 0, 0, err
		}
	}
	return after, count, dbError(rows.Err())
}

func writeLineCSVPage(ctx context.Context, tx pgx.Tx, scope Scope, book Book, w *csv.Writer, after int64, limit int) (int64, int, error) {
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	rows, err := tx.Query(queryCtx, `WITH page AS MATERIALIZED (
		SELECT id,sequence FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND sequence>$3
		ORDER BY sequence LIMIT $4
	)
	SELECT e.sequence,l.entry_id::text,l.id::text,l.position,l.account_id::text,l.side,l.amount_minor::text,
		l.memo,coalesce(l.reverses_line_id::text,''),coalesce(l.native_currency,''),
		coalesce(l.native_scale::text,''),coalesce(l.native_amount_minor::text,'')
	FROM page e JOIN public.lines l ON l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=e.id
	ORDER BY e.sequence,l.position`, scope.TenantID, scope.BookID, after, limit)
	if err != nil {
		return 0, 0, dbError(err)
	}
	defer func() { cancel(); rows.Close() }()
	scale := strconv.Itoa(book.Scale)
	var count int
	for rows.Next() {
		var entryID, lineID, accountID, side, minor, memo, reverses, nativeCurrency, nativeScale, nativeMinor string
		var seq int64
		var position int
		if err := rows.Scan(&seq, &entryID, &lineID, &position, &accountID, &side, &minor,
			&memo, &reverses, &nativeCurrency, &nativeScale, &nativeMinor); err != nil {
			return 0, 0, dbError(err)
		}
		if seq != after {
			after = seq
			count++
		}
		if err := w.Write([]string{entryID, lineID, strconv.Itoa(position), accountID, side, minor, book.Currency, scale,
			reporting.SafeCSVText(memo), reverses, nativeCurrency, nativeScale, nativeMinor}); err != nil {
			return 0, 0, err
		}
	}
	return after, count, dbError(rows.Err())
}

// WriteBookExport streams a bounded projection from one recorded Book snapshot.
func (a TenantAdmin) WriteBookExport(ctx context.Context, book, format string, dst io.Writer) (RecordedState, error) {
	var state RecordedState
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, b Book) error {
		snapshot, err := captureReadState(ctx, tx, scope, b, recordedReadScope{AllAccepted: true})
		if err != nil {
			return err
		}
		state = snapshot.RecordedState
		if format == "journal.json" {
			return writeJournalExport(ctx, tx, scope, b, &archiveBoundWriter{dst: dst, remaining: maxArchiveMetadata})
		}
		return writeBookCSV(ctx, tx, scope, b, format, &archiveBoundWriter{dst: dst, remaining: reporting.MaxCSVBytes})
	})
	return state, err
}
