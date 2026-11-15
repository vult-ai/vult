package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/vult-ai/vult/ledger"
)

// The checks Vult ran on each of a Book's documents, for the owner's screen:
// the latest of each check per document, after the last one it saw. The
// events stream announces area "documents" when there's a new one.
const documentChecksPath = "/api/v1/document-checks"

func (a *webAPI) documentChecks(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	for key := range q {
		if key != "book_id" && key != "document_id" && key != "after" {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	query := ledger.DocumentCheckQuery{DocumentID: q.Get("document_id")}
	if after := q.Get("after"); after != "" {
		n, err := strconv.ParseInt(after, 10, 64)
		if err != nil || n < 0 {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		query.After = n
	}
	book := q.Get("book_id")
	tenant, ok := a.bankBook(w, r, session, book)
	if !ok {
		return
	}
	var page ledger.DocumentCheckPage
	err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		page, err = o.ReadDocumentChecks(r.Context(), book, query)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		SchemaVersion int `json:"schema_version"`
		ledger.DocumentCheckPage
	}{1, page})
}

// AddedDocument is a file the owner just added, for Vult's document checker.
// Confirmed, when set, holds the fields the owner confirmed beside it.
type AddedDocument struct {
	ID        string
	Name      string
	MediaType string
	// Group is where the owner filed it, such as receipt; empty when unfiled.
	Group     string
	Data      []byte
	Confirmed json.RawMessage
}

// ProposeReading verifies an agent's proposed reading of a document in the
// agent's own request, with a store that can read the document, and runs
// Vult's checks on it. agentName may be empty; the ledger then names the
// connection.
type ProposeReading func(ctx context.Context, store ledger.Store, scope ledger.Scope, agentName string, input json.RawMessage) (json.RawMessage, error)

const documentReadingPath = "/api/v1/document-reading"

// documentReading takes the fields the owner confirmed beside a document
// Vult couldn't fully read, and runs the checks on them.
func (a *webAPI) documentReading(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BookID     string          `json:"book_id"`
		DocumentID string          `json:"document_id"`
		Fields     json.RawMessage `json:"fields"`
	}
	if !readRuleBody(w, r, &q) {
		return
	}
	if !ledger.ValidID(q.DocumentID) || len(q.Fields) == 0 || len(q.Fields) > 1<<20 || a.added == nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	var doc ledger.DocumentInspection
	err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		doc, err = o.Document(r.Context(), q.BookID, q.DocumentID)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	if doc.Document.Size > maxCheckedDocument {
		apiFailure(w, ledger.ErrLimit)
		return
	}
	if !a.reserveDocumentCheck(w) {
		return
	}
	if !a.handOff(r, session, tenant, q.BookID, doc.Document, q.Fields) {
		a.checkQueue.release()
		failure(w, http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "status": "checking"})
}

// maxCheckedDocument bounds the file the checker reads.
const maxCheckedDocument = 20 << 20

// checkerTime bounds one document's checks, model calls included.
const checkerTime = 5 * time.Minute

// documentAdded hands a newly added statement, receipt, scan or export to
// the checker, after the upload has answered.
func (a *webAPI) documentAdded(r *http.Request, session, book string, doc ledger.Document) bool {
	if a.added == nil || doc.Size > maxCheckedDocument {
		return false
	}
	switch doc.MediaType {
	case "application/pdf", "image/png", "image/jpeg", "image/webp", "image/gif", "text/csv":
	default:
		return false
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		return false
	}
	return a.handOff(r, session, tenant, book, doc, nil)
}

func (a *webAPI) reserveDocumentCheck(w http.ResponseWriter) bool {
	if a.checkQueue == nil {
		failure(w, http.StatusServiceUnavailable)
		return false
	}
	if !a.checkQueue.reserve() {
		w.Header().Set("Retry-After", "1")
		failure(w, http.StatusTooManyRequests)
		return false
	}
	return true
}

// handOff reads the document's bytes and gives them to the checker, after
// the request has answered.
func (a *webAPI) handOff(r *http.Request, session, tenant, book string, doc ledger.Document, confirmed json.RawMessage) bool {
	if doc.Size > maxCheckedDocument {
		return false
	}
	key := documentCheckKey{document: tenant + "/" + book + "/" + doc.ID, session: sha256.Sum256([]byte(session)), reading: sha256.Sum256(confirmed)}
	return a.checkQueue.start(r.Context(), key, func(ctx context.Context) {
		var data bytes.Buffer
		var err error
		limited := &limitedWriter{w: &data, n: maxCheckedDocument}
		if a.readDocument != nil {
			err = a.readDocument(ctx, session, book, doc.ID, limited)
		} else {
			err = a.sessions.WithOwner(ctx, session, tenant, false, func(owner ledger.TenantAdmin) error {
				return owner.DownloadDocument(ctx, book, doc.ID, a.documents, limited)
			})
		}
		if err != nil {
			return
		}
		a.added(ctx, ledger.AgentTarget{TenantID: tenant, BookID: book}, func(call func(ledger.TenantAdmin, string) error) error {
			return a.sessions.OwnerAct(ctx, session, tenant, call)
		}, AddedDocument{ID: doc.ID, Name: doc.Name, MediaType: doc.MediaType, Group: doc.Group, Data: data.Bytes(), Confirmed: confirmed})
	})
}

type limitedWriter struct {
	w io.Writer
	n int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.n {
		return 0, errors.New("document too large to check")
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}
