package ledger

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
)

type JournalRead struct {
	RecordedState
	SchemaVersion int              `json:"schema_version"`
	Version       int              `json:"version"`
	BookID        string           `json:"book_id"`
	Currency      string           `json:"currency"`
	Scale         int              `json:"scale"`
	Entry         Entry            `json:"entry"`
	RecordedBy    *JournalRecorder `json:"recorded_by,omitempty"`
}

// JournalRecorder is the display identity of the entry's recorded actor.
// It does not change the entry or its receipt.
type JournalRecorder struct {
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	ConnectionID string `json:"connection_id,omitempty"`
}

type JournalDocumentsRead struct {
	Version   int             `json:"version"`
	BookID    string          `json:"book_id"`
	EntryID   string          `json:"entry_id"`
	Documents json.RawMessage `json:"documents"`
}

// Journal returns the full immutable entry, including its separate measurements.
func (a TenantAdmin) Journal(ctx context.Context, book, entry string) (JournalRead, error) {
	if !ValidID(entry) {
		return JournalRead{}, ErrInvalid
	}
	var out JournalRead
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, header Book) error {
		// Resolve the actor in the entry header query. A connection retains
		// its identity after revocation. A profile is visible only when the
		// actor is the currently authenticated owner of this Book.
		var connectionID, connectionName, ownerName *string
		row := tx.QueryRow(ctx, "SELECT "+entryColumns+`, recorder.connection_id, recorder.connection_name,
			CASE WHEN recorder.connection_id IS NULL AND access.owner_identity(e.tenant_id)=e.actor_id
			THEN coalesce((SELECT display_name FROM access.user_settings WHERE identity_id=e.actor_id),'') END
			FROM public.entries e
			LEFT JOIN LATERAL (
				SELECT id::text AS connection_id, name AS connection_name FROM public.connections
				WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND actor_id=e.actor_id
			) recorder ON true
			WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.id=$3`, scope.TenantID, scope.BookID, entry)
		value, err := readEntryRow(ctx, tx, scope, row, &connectionID, &connectionName, &ownerName)
		if err != nil {
			return dbError(err)
		}
		state, err := captureReadState(ctx, tx, scope, header, recordedReadScope{AllAccepted: true})
		if err != nil {
			return err
		}
		out = JournalRead{RecordedState: state.RecordedState, SchemaVersion: 1, Version: 1, BookID: book, Currency: header.Currency, Scale: header.Scale, Entry: value}
		if connectionID != nil && connectionName != nil {
			out.RecordedBy = &JournalRecorder{Kind: "agent", Name: *connectionName, ConnectionID: *connectionID}
		} else if ownerName != nil {
			name := strings.TrimSpace(*ownerName)
			if name == "" {
				name = "You"
			}
			out.RecordedBy = &JournalRecorder{Kind: "owner", Name: name}
		}
		return nil
	})
	if err != nil {
		return JournalRead{}, err
	}
	return out, nil
}

// JournalDocuments preserves the same independent source/evidence cursors and
// history as the agent read. It does not flatten those relationships into files.
func (a TenantAdmin) JournalDocuments(ctx context.Context, book string, query EntryDocumentsQuery) (JournalDocumentsRead, error) {
	var out JournalDocumentsRead
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, _ Book) error {
		value, err := (Store{}).citationCall(ctx, tx, scope, "get_entry_documents", raw(query))
		if err != nil {
			return err
		}
		out = JournalDocumentsRead{Version: 1, BookID: book, EntryID: query.EntryID, Documents: value}
		return nil
	})
	if err != nil {
		return JournalDocumentsRead{}, err
	}
	return out, nil
}

func (a TenantAdmin) withBookRead(ctx context.Context, book string, read func(pgx.Tx, Scope, Book) error) error {
	return a.withBookTransaction(ctx, book, pgx.RepeatableRead, read)
}

func (a TenantAdmin) withBookTransaction(ctx context.Context, book string, level pgx.TxIsoLevel, call func(pgx.Tx, Scope, Book) error) error {
	if !a.valid() {
		return ErrDenied
	}
	if !ValidID(book) {
		return ErrInvalid
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: level})
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback(context.Background())
	scope := Scope{TenantID: a.tenantID, BookID: book}
	if err = setScope(ctx, tx, scope); err != nil {
		return dbError(err)
	}
	header, err := readBook(ctx, tx, scope)
	if err != nil {
		return dbError(err)
	}
	if err = call(tx, scope, header); err != nil {
		return err
	}
	var allowed bool
	if err = tx.QueryRow(ctx, "SELECT public.ledger_import_owner() OR access.owner_allowed($1)", a.tenantID).Scan(&allowed); err != nil || !allowed {
		return ErrDenied
	}
	return dbError(tx.Commit(ctx))
}
