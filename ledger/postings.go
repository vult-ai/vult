package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/jackc/pgx/v5"
)

type PostingLine struct {
	ID string `json:"id"`
	// The browser wire name predates editable GL codes. This is the stable
	// account ID; human-assigned codes are separate account attributes.
	AccountID   string `json:"account_code"`
	Side        string `json:"side"`
	AmountMinor string `json:"amount_minor"`
}

type PostingEntry struct {
	ID    string        `json:"id"`
	Lines []PostingLine `json:"lines"`
}

// ScopedPostings is a minimal Book-currency effects projection, not a complete
// entry export. Its digest covers the canonical financial content, excluding
// the digest itself and the separately identified recorded-state observation.
type ScopedPostings struct {
	RecordedState
	Version       int            `json:"version"`
	BookID        string         `json:"book_id"`
	Currency      string         `json:"currency"`
	Scale         int            `json:"scale"`
	Entries       []PostingEntry `json:"entries"`
	ContentSHA256 string         `json:"content_sha256,omitempty"`
}

// JournalPostings returns every line for up to 100 distinct entries, with a
// 10,000-line response bound. A missing or foreign entry rejects the whole read.
// The enclosing owner lease and row policies still govern remote callers.
func (a TenantAdmin) JournalPostings(ctx context.Context, book string, ids []string) (ScopedPostings, error) {
	if !a.valid() {
		return ScopedPostings{}, ErrDenied
	}
	if !ValidID(book) || len(ids) < 1 || len(ids) > 100 {
		return ScopedPostings{}, ErrInvalid
	}
	ids = append([]string{}, ids...)
	sort.Strings(ids)
	for i, id := range ids {
		if !ValidID(id) || i > 0 && id == ids[i-1] {
			return ScopedPostings{}, ErrInvalid
		}
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return ScopedPostings{}, dbError(err)
	}
	defer tx.Rollback(context.Background())
	scope := Scope{TenantID: a.tenantID, BookID: book}
	if err = setScope(ctx, tx, scope); err != nil {
		return ScopedPostings{}, dbError(err)
	}
	header, err := readBook(ctx, tx, scope)
	if err != nil {
		return ScopedPostings{}, dbError(err)
	}
	state, err := captureReadState(ctx, tx, scope, header, recordedReadScope{AllAccepted: true})
	if err != nil {
		return ScopedPostings{}, err
	}
	rows, err := tx.Query(ctx, `WITH selected AS MATERIALIZED (
 SELECT id FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND id=ANY($3::uuid[])
), selection AS (SELECT count(*)=cardinality($3::uuid[]) AS complete FROM selected)
 SELECT selection.complete,coalesce(e.id::text,''),coalesce(l.id::text,''),
 coalesce(l.account_id::text,''),coalesce(l.side,''),coalesce(l.amount_minor::text,'')
 FROM selection LEFT JOIN (selected e JOIN public.lines l
 ON l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=e.id) ON selection.complete
 ORDER BY e.id,l.position LIMIT 10001`, a.tenantID, book, ids)
	if err != nil {
		return ScopedPostings{}, dbError(err)
	}
	defer rows.Close()
	out := ScopedPostings{Version: 1, BookID: book, Currency: header.Currency, Scale: header.Scale, Entries: make([]PostingEntry, len(ids))}
	positions := make(map[string]int, len(ids))
	for i, id := range ids {
		out.Entries[i] = PostingEntry{ID: id, Lines: []PostingLine{}}
		positions[id] = i
	}
	count, complete := 0, false
	for rows.Next() {
		var entry string
		var line PostingLine
		if err = rows.Scan(&complete, &entry, &line.ID, &line.AccountID, &line.Side, &line.AmountMinor); err != nil {
			return ScopedPostings{}, dbError(err)
		}
		if !complete {
			break
		}
		count++
		if count > 10000 {
			break
		}
		i := positions[entry]
		out.Entries[i].Lines = append(out.Entries[i].Lines, line)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return ScopedPostings{}, dbError(err)
	}
	// Row locks prevent revocation, but they cannot stop a session's clock
	// expiry. Reject a read if time-sensitive row policies could hide lines.
	var allowed bool
	if err = tx.QueryRow(ctx, "SELECT public.ledger_import_owner() OR access.owner_allowed($1)", a.tenantID).Scan(&allowed); err != nil {
		return ScopedPostings{}, ErrDenied
	}
	if !allowed {
		return ScopedPostings{}, ErrDenied
	}
	if !complete {
		return ScopedPostings{}, ErrNotFound
	}
	if count > 10000 {
		return ScopedPostings{}, ErrLimit
	}
	for _, entry := range out.Entries {
		if len(entry.Lines) < 2 {
			return ScopedPostings{}, ErrNotFound
		}
	}
	digest := sha256.Sum256(raw(out))
	out.ContentSHA256 = hex.EncodeToString(digest[:])
	out.RecordedState = state.RecordedState
	if err = tx.Commit(ctx); err != nil {
		return ScopedPostings{}, dbError(err)
	}
	return out, nil
}
