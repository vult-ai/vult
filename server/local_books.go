package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// LocalBooks lets the local browser create the owner's Books through the
// protected service, which checks the owner's session itself. Draft is what
// an agent proposed for the first Book, or nil.
type LocalBooks interface {
	Draft(ctx context.Context, session string) (json.RawMessage, error)
	Create(ctx context.Context, session, name, currency string, scale int) (ledger.Book, error)
	// Activity is what agents did in the Book after a sequence number, with
	// the agents waiting for the owner's approval.
	Activity(ctx context.Context, session, book string, after int64) (json.RawMessage, error)
	// Decide lets an agent on this computer in to one Book, or turns it away.
	Decide(ctx context.Context, session, request, book, access string, deny bool) error
}

const (
	localBookDraftPath = "/api/v1/local/book-draft"
	localBooksPath     = "/api/v1/local/books"
	localActivityPath  = "/api/v1/local/agent-activity"
	localDecisionPath  = "/api/v1/local/agent-decision"
	// The service allows five minutes since the passkey sign-in; ask a little sooner.
	localBookFreshness = 4 * time.Minute
)

func (a *webAPI) localBook(w http.ResponseWriter, r *http.Request, session string) {
	if a.localBooks == nil || r.URL.RawQuery != "" {
		failure(w, http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		draft, err := a.localBooks.Draft(r.Context(), session)
		if err != nil {
			apiFailure(w, err)
			return
		}
		if len(draft) == 0 {
			draft = json.RawMessage("null")
		}
		_, _ = w.Write(append(draft, '\n'))
		return
	}
	id, err := a.sessions.Identity(r.Context(), session)
	if err != nil {
		failure(w, http.StatusUnauthorized)
		return
	}
	if time.Since(id.AuthenticatedAt) > localBookFreshness {
		// The browser signs in with the passkey again and comes back.
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "{\"error\":\"sign_in_again\"}\n")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	var q struct {
		Name     string `json:"name"`
		Currency string `json:"currency"`
		Scale    int    `json:"scale"`
	}
	if err != nil || strictjson.Decode(body, &q) != nil || strings.TrimSpace(q.Name) == "" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	book, err := a.localBooks.Create(r.Context(), session, strings.TrimSpace(q.Name), q.Currency, q.Scale)
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(book)
}

// localActivity serves what agents did in a Book, for the owner to watch.
func (a *webAPI) localActivity(w http.ResponseWriter, r *http.Request, session string) {
	query := r.URL.Query()
	book := query.Get("book_id")
	if a.localBooks == nil || !ledger.ValidID(book) || len(query) > 2 || len(query["book_id"]) != 1 || len(query["after"]) > 1 {
		failure(w, http.StatusNotFound)
		return
	}
	after := int64(0)
	if raw := query.Get("after"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		after = n
	}
	page, err := a.localBooks.Activity(r.Context(), session, book, after)
	if err != nil {
		apiFailure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(append(page, '\n'))
}

// localDecision answers an agent's request from the Book itself. Letting an
// agent in needs the same recent sign-in as creating a Book.
func (a *webAPI) localDecision(w http.ResponseWriter, r *http.Request, session string) {
	if a.localBooks == nil || r.URL.RawQuery != "" {
		failure(w, http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	var q struct {
		Request string `json:"request"`
		BookID  string `json:"book_id,omitempty"`
		Access  string `json:"access,omitempty"`
		Deny    bool   `json:"deny,omitempty"`
	}
	if err != nil || strictjson.Decode(body, &q) != nil || (!q.Deny && (!ledger.ValidID(q.BookID) || (q.Access != "write" && q.Access != "read"))) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	if !q.Deny {
		id, err := a.sessions.Identity(r.Context(), session)
		if err != nil {
			failure(w, http.StatusUnauthorized)
			return
		}
		if time.Since(id.AuthenticatedAt) > localBookFreshness {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "{\"error\":\"sign_in_again\"}\n")
			return
		}
	}
	if err := a.localBooks.Decide(r.Context(), session, q.Request, q.BookID, q.Access, q.Deny); err != nil {
		apiFailure(w, err)
		return
	}
	_, _ = io.WriteString(w, "{\"status\":\"decided\"}\n")
}
