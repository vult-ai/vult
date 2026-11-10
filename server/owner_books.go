package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

const (
	booksPath         = "/api/v1/books"
	periodsPath       = "/api/v1/periods"
	periodsClosePath  = "/api/v1/periods/close"
	periodsReopenPath = "/api/v1/periods/reopen"
	periodsReviewPath = "/api/v1/periods/review"
)

type createBookRequest struct {
	RequestKey string `json:"request_key"`
	Name       string `json:"name"`
	Currency   string `json:"currency"`
	Scale      int    `json:"scale"`
}

// createBook adds a Book for the signed-in owner. On this computer the local
// service creates it under the owner's recent passkey sign-in; on a server it
// is an owner request in the owner's single workspace, so retries return the
// same Book and the server's admission policy applies.
func (a *webAPI) createBook(w http.ResponseWriter, r *http.Request, session string) {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	var q createBookRequest
	if err != nil || strictjson.Decode(body, &q) != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" || len(q.Name) > 120 || len(q.Currency) != 3 || strings.ToUpper(q.Currency) != q.Currency || q.Scale < 0 || q.Scale > 4 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	if a.localBooks != nil {
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
		book, err := a.localBooks.Create(r.Context(), session, q.Name, q.Currency, q.Scale)
		if err != nil {
			apiFailure(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(browserBook(book, time.Now()))
		return
	}
	tenants, err := a.sessions.TenantsBounded(r.Context(), session, 2)
	if err != nil {
		apiFailure(w, err)
		return
	}
	if len(tenants) != 1 {
		// An owner of several workspaces must choose one; the browser does not offer that yet.
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	input, _ := json.Marshal(struct {
		Name     string `json:"name"`
		Currency string `json:"currency"`
		Scale    int    `json:"scale"`
	}{q.Name, q.Currency, q.Scale})
	result, err := a.sessions.OwnerRequest(r.Context(), session, tenants[0], q.RequestKey, "create_book", input, a.admit, func(o ledger.TenantAdmin) (json.RawMessage, error) {
		book, err := o.CreateBook(r.Context(), q.Name, q.Currency, q.Scale)
		if err != nil {
			return nil, err
		}
		return json.Marshal(browserBook(book, time.Now()))
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_, _ = w.Write(result)
}

// readClosedPeriods lists the periods of one Book that accept no postings.
func (a *webAPI) readClosedPeriods(w http.ResponseWriter, r *http.Request, session string) {
	book, err := ownerBookID(r)
	if err != nil {
		apiFailure(w, err)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var periods []ledger.ClosedPeriod
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		periods, err = o.ClosedPeriods(r.Context(), book)
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		BookID  string                `json:"book_id"`
		Periods []ledger.ClosedPeriod `json:"periods"`
	}{book, periods})
}

// periodChange records the owner's close or reopening of a period: a close
// with open review items needs a reason, and only the owner reopens.
// Retrying the same request returns its result.
func (a *webAPI) periodChange(w http.ResponseWriter, r *http.Request, session, operation string) {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	var q struct {
		BookID    string          `json:"book_id"`
		Arguments json.RawMessage `json:"arguments"`
	}
	var key struct {
		RequestKey string `json:"request_key"`
	}
	if err != nil || strictjson.Decode(body, &q) != nil || !ledger.ValidID(q.BookID) || json.Unmarshal(q.Arguments, &key) != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if err != nil {
		apiFailure(w, err)
		return
	}
	input, _ := json.Marshal(struct {
		BookID    string          `json:"book_id"`
		Operation string          `json:"operation"`
		Arguments json.RawMessage `json:"arguments"`
	}{q.BookID, operation, q.Arguments})
	result, err := a.sessions.OwnerRequestAs(r.Context(), session, tenant, key.RequestKey, operation, input, a.admit, func(o ledger.TenantAdmin, who string) (json.RawMessage, error) {
		return o.AccountChange(r.Context(), q.BookID, who, operation, q.Arguments)
	})
	var refused *ledger.RefusedError
	if errors.As(err, &refused) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "refused", "message": refused.Reason})
		return
	}
	if errors.Is(err, ledger.ErrFreshOwnerProof) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "fresh_authentication_required", "message": "Sign in again, then make this change."})
		return
	}
	if err != nil {
		apiFailure(w, err)
		return
	}
	_, _ = w.Write(result)
}

// periodReview is what the owner sees before closing a range.
func (a *webAPI) periodReview(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	book, from, through := q.Get("book_id"), q.Get("from"), q.Get("through")
	if len(q) != 3 || !ledger.ValidID(book) || !ledger.ValidDate(from) || !ledger.ValidDate(through) || from > through {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var review ledger.PeriodReview
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		review, err = o.PeriodReview(r.Context(), book, from, through)
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(review)
}
