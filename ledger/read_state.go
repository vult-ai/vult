package ledger

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// RecordedState identifies the accepted Book population and calculation inputs
// seen by a read. CapturedAt is an observation time, never a last-write claim.
// KnowledgeCutoff is absent for reads of explicitly selected immutable records.
type RecordedState struct {
	StateRevision        string `json:"state_revision,omitempty"`
	StateCapturedAt      string `json:"state_captured_at,omitempty"`
	StateKnowledgeCutoff string `json:"state_knowledge_cutoff,omitempty"`
}

type recordedReadScope struct {
	Cutoff      string
	AllAccepted bool
}

type readState struct {
	RecordedState
	epoch            int64
	cutoff, captured time.Time
}

// Change the model version when financial read semantics change without a
// migration or currency-catalog change. Request filters have their own digests.
var figureDependencies = func() string {
	checksums := map[string]string{}
	for _, name := range migrationNames() {
		checksums[name] = migrationSum(name)
	}
	return archiveHash(raw(struct {
		Model      string
		Migrations map[string]string
		Currencies []Currency
	}{"recorded-ledger-figures-v1", checksums, Currencies()}))
}()

func immutableReadState(ctx context.Context, tx pgx.Tx, scope Scope) (RecordedState, error) {
	book, err := readBook(ctx, tx, scope)
	if err != nil {
		return RecordedState{}, dbError(err)
	}
	state, err := captureReadState(ctx, tx, scope, book, recordedReadScope{AllAccepted: true})
	return state.RecordedState, err
}

func captureReadState(ctx context.Context, tx pgx.Tx, scope Scope, book Book, selection recordedReadScope) (readState, error) {
	var out readState
	var requested *time.Time
	if selection.Cutoff != "" {
		value, err := time.Parse(time.RFC3339Nano, selection.Cutoff)
		if err != nil || selection.AllAccepted {
			return out, ErrInvalid
		}
		requested = &value
	}
	var captured time.Time
	var future int64
	var installation string
	frontier := "0::bigint"
	args := []any{scope.TenantID, scope.BookID}
	if !selection.AllAccepted {
		const entries = " FROM public.entries e WHERE e.tenant_id=$1 AND e.book_id=$2"
		cutoff := "observed.captured_at"
		if requested != nil {
			cutoff = "$3::timestamptz"
			args = append(args, *requested)
		}
		frontier = "(SELECT count(*)" + entries + " AND e.recorded_at>" + cutoff + ")"
		if requested == nil {
			// A current read usually has no future recordings. Check the
			// latest indexed timestamp before counting the exact frontier.
			frontier = "CASE WHEN (SELECT e.recorded_at" + entries +
				" ORDER BY e.recorded_at DESC LIMIT 1)>" + cutoff + " THEN " + frontier + " ELSE 0::bigint END"
		}
	}
	err := tx.QueryRow(ctx, `WITH observed AS MATERIALIZED (SELECT clock_timestamp() captured_at)
 SELECT b.write_epoch,observed.captured_at,`+frontier+`,
   (SELECT encode(sha256(convert_to(string_agg(filename||':'||sha256||':'||extract(epoch FROM applied_at)::text,',' ORDER BY filename),'UTF8')),'hex') FROM public.schema_receipts)
 FROM public.books b CROSS JOIN observed WHERE b.tenant_id=$1 AND b.id=$2`, args...).Scan(&out.epoch, &captured, &future, &installation)
	if err != nil {
		return out, dbError(err)
	}
	out.cutoff, out.captured = captured, captured
	if requested != nil {
		if requested.After(captured) {
			return readState{}, ErrInvalid
		}
		out.cutoff = *requested
	}
	out.StateCapturedAt = captured.UTC().Format(time.RFC3339Nano)
	if !selection.AllAccepted {
		out.StateKnowledgeCutoff = out.cutoff.UTC().Format(time.RFC3339Nano)
	}
	// At a fixed epoch, a recorded-time frontier has a monotonic population.
	// Count future records over the whole Book, independently of period/filter,
	// so a retained future recording cannot become visible under a stale marker.
	out.StateRevision = archiveHash(raw(struct {
		Tenant, Book, Dependencies, Installation string
		Epoch, Future                            int64
		Header                                   Book
	}{scope.TenantID, scope.BookID, figureDependencies, installation, out.epoch, future, book}))
	return out, nil
}

// BookWithRecordedState reads the Book and its marker from one snapshot.
func (a TenantAdmin) BookWithRecordedState(ctx context.Context, book string) (Book, RecordedState, error) {
	var out Book
	var state RecordedState
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, b Book) error {
		snapshot, err := captureReadState(ctx, tx, scope, b, recordedReadScope{AllAccepted: true})
		out, state = b, snapshot.RecordedState
		return err
	})
	return out, state, err
}
