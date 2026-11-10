package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

const (
	accountChangesPath = "/api/v1/account-changes"
	starterChartsPath  = "/api/v1/starter-charts"
	payeesPath         = "/api/v1/payees"
	statementsPath     = "/api/v1/statements"
)

// accountChangeRequest is one change the owner makes to the chart of
// accounts (a parent, heading, retirement with its transfer, reactivation or
// starter chart) or to payees (a payee, or an entry's payee). Arguments are
// the ledger operation's own request.
type accountChangeRequest struct {
	BookID    string          `json:"book_id"`
	Operation string          `json:"operation"`
	Arguments json.RawMessage `json:"arguments"`
}

func (a *webAPI) accountChange(w http.ResponseWriter, r *http.Request, session string) {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	var q accountChangeRequest
	var key struct {
		RequestKey string `json:"request_key"`
	}
	if err != nil || strictjson.Decode(body, &q) != nil || !ledger.ValidID(q.BookID) || json.Unmarshal(q.Arguments, &key) != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	switch q.Operation {
	case "revise_account", "retire_account", "reactivate_account", "apply_starter_chart", "create_payee", "revise_payee", "set_entry_payee":
	case "mark_statement_duplicate", "match_statement", "revoke_statement_match", "record_statement_adjustment", "review_statement":
	default:
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if err != nil {
		apiFailure(w, err)
		return
	}
	input, _ := json.Marshal(q)
	result, err := a.sessions.OwnerRequestAs(r.Context(), session, tenant, key.RequestKey, q.Operation, input, a.admit, func(o ledger.TenantAdmin, who string) (json.RawMessage, error) {
		return o.AccountChange(r.Context(), q.BookID, who, q.Operation, q.Arguments)
	})
	var remaining *ledger.RemainingBalanceError
	if errors.As(err, &remaining) {
		// The app asks where the money went, with the exact amounts.
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "balance_remaining", "message": remaining.Error(),
			"account_id": remaining.AccountID, "remaining": remaining.Remaining})
		return
	}
	var refused *ledger.RefusedError
	if errors.As(err, &refused) {
		// A rule the owner can act on, such as an unexplained statement.
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

func (a *webAPI) starterCharts(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "charts": ledger.StarterCharts()})
}

// payees lists a Book's payees for the owner, optionally by status.
func (a *webAPI) payees(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	book, status := q.Get("book_id"), q.Get("status")
	if !ledger.ValidID(book) || len(q) > 2 || len(q) == 2 && !q.Has("status") {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out []ledger.Payee
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		out, err = o.Payees(r.Context(), book, status)
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "payees": out})
}

// statements lists a Book's statements, or with statement_id compares one
// statement with the Book.
func (a *webAPI) statements(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	book, statement, account := q.Get("book_id"), q.Get("statement_id"), q.Get("account_id")
	for key, values := range q {
		if len(values) != 1 || key != "book_id" && key != "statement_id" && key != "account_id" {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	if !ledger.ValidID(book) || statement != "" && !ledger.ValidID(statement) || account != "" && !ledger.ValidID(account) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out any
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		if statement == "" {
			list, err := o.Statements(r.Context(), book, account)
			out = map[string]any{"schema_version": 1, "statements": list}
			return err
		}
		report, suggestions, err := o.StatementReconciliation(r.Context(), book, statement)
		out = map[string]any{"schema_version": 1, "reconciliation": report, "suggestions": suggestions}
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
