package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5"
)

// ErrNeedsMaintenance means the database is at an earlier schema this
// release can update, but updating it is the maintenance step's job, never
// ordinary startup's.
var ErrNeedsMaintenance = errors.New("the database needs this version's maintenance step before Vult can start")

// SchemaState compares the database's receipts with this release's
// migrations.
type SchemaState struct {
	Applied int    // migrations recorded in the database
	Known   int    // migrations this release has
	Last    string // the newest recorded migration's number, such as "0008"
}

// Current reports whether the database is exactly at this release's schema.
func (s SchemaState) Current() bool { return s.Applied == s.Known }

// Schema reads the database's receipts. It fails if they don't match this
// release's migrations in order and content, or if the database has more
// than this release knows. A database behind this release is not an error.
func Schema(ctx context.Context, db receiptReader) (SchemaState, error) {
	names := migrationNames()
	n, err := verifyReceipts(ctx, db, names, false)
	if err != nil {
		return SchemaState{}, err
	}
	return SchemaState{Applied: n, Known: len(names), Last: names[n-1][:4]}, nil
}

// RequireCurrent is the startup check: the schema must be exactly this
// release's. A database behind it returns ErrNeedsMaintenance.
func RequireCurrent(ctx context.Context, db receiptReader) error {
	s, err := Schema(ctx, db)
	if err != nil {
		return err
	}
	if !s.Current() {
		return ErrNeedsMaintenance
	}
	return nil
}

// FactFingerprint summarizes the recorded facts that no schema change may
// alter: Books, accounts, entries and their lines (identifiers, dates,
// accounts, sides, amounts, reversals) and attached evidence (documents and
// their hashes). It is the same before and after any migration.
type FactFingerprint struct {
	SHA256 string           `json:"sha256"`
	Counts map[string]int64 `json:"counts"`
}

// factQueries use only columns present in every released schema, so the
// fingerprint means the same thing on both sides of a migration. Each row is
// one line of text; rows are ordered by identifier.
var factQueries = []struct {
	table, since, query string
}{
	{"books", "", `SELECT concat_ws(E'\t',id,tenant_id,currency,scale) FROM public.books ORDER BY id`},
	{"accounts", "", `SELECT concat_ws(E'\t',id,tenant_id,book_id,class) FROM public.accounts ORDER BY id`},
	{"entries", "", `SELECT concat_ws(E'\t',id,tenant_id,book_id,sequence,receipt_id,economic_date,accounting_date,coalesce(reversal_of::text,'-')) FROM public.entries ORDER BY id`},
	{"lines", "", `SELECT concat_ws(E'\t',id,tenant_id,book_id,entry_id,position,account_id,side,amount_minor,coalesce(reverses_line_id::text,'-')) FROM public.lines ORDER BY id`},
	{"documents", "documents", `SELECT concat_ws(E'\t',id,tenant_id,book_id,sha256,size_bytes) FROM public.documents ORDER BY id`},
	{"evidence_links", "evidence_links", `SELECT concat_ws(E'\t',id,tenant_id,book_id,link_id,revision,entry_id,document_id,action) FROM public.evidence_links ORDER BY id`},
}

type factReader interface {
	receiptReader
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Fingerprint computes the fact fingerprint. Run it inside the transaction
// whose view it should describe.
func Fingerprint(ctx context.Context, db factReader) (FactFingerprint, error) {
	h := sha256.New()
	io.WriteString(h, "vult-facts-v1\n")
	counts := map[string]int64{}
	for _, q := range factQueries {
		if q.since != "" {
			var present bool
			if err := db.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "public."+q.since).Scan(&present); err != nil {
				return FactFingerprint{}, err
			}
			if !present {
				continue
			}
		}
		rows, err := db.Query(ctx, q.query)
		if err != nil {
			return FactFingerprint{}, fmt.Errorf("fingerprint %s: %w", q.table, err)
		}
		var n int64
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				return FactFingerprint{}, err
			}
			io.WriteString(h, q.table+"\t"+row+"\n")
			n++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return FactFingerprint{}, err
		}
		counts[q.table] = n
	}
	return FactFingerprint{SHA256: hex.EncodeToString(h.Sum(nil)), Counts: counts}, nil
}

// ErrFactsChanged means a schema change would have altered recorded facts.
// The change is rolled back.
var ErrFactsChanged = errors.New("the schema change would alter recorded facts; nothing was changed")

// MigrationResult is what a maintenance migration did.
type MigrationResult struct {
	From        int             `json:"from"` // migrations recorded before
	To          int             `json:"to"`   // migrations recorded after
	Fingerprint FactFingerprint `json:"fingerprint"`
	Initialized bool            `json:"initialized,omitempty"`
}
