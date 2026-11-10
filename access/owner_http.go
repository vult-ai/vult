package access

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// OwnerHTTP exposes only scoped administration through human session and CSRF
// proofs. It has no installation, recovery, database or identity-provider powers.
type OwnerHTTP struct {
	s     *Service
	login *HTTP
	admit OwnerAdmission
}

func NewOwnerHTTP(s *Service, login *HTTP, admit OwnerAdmission) *OwnerHTTP {
	return &OwnerHTTP{s, login, admit}
}
func ownerFailure(w http.ResponseWriter, e error) {
	status := 503
	code := "outcome_unresolved"
	switch {
	case errors.Is(e, ErrDenied) || errors.Is(e, ledger.ErrDenied):
		status, code = 403, "access_denied"
	case errors.Is(e, ledger.ErrInvalid):
		status, code = 400, "invalid_request"
	case errors.Is(e, ledger.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(e, ledger.ErrConflict):
		status, code = 409, "request_key_conflict"
	case errors.Is(e, ledger.ErrLimit):
		status, code = 413, "record_limit_exceeded"
	}
	oauthJSON(w, status, map[string]string{"error": code})
}
func (h *OwnerHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 {
		ownerFailure(w, ErrDenied)
		return
	}
	session, e := r.Cookie(SessionCookie)
	if e != nil {
		ownerFailure(w, ErrDenied)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "owner" || parts[1] != "tenants" || !ledger.ValidID(parts[2]) {
		ownerFailure(w, ledger.ErrNotFound)
		return
	}
	tenant := parts[2]
	if r.Method == "GET" {
		var result any
		if len(parts) == 5 && parts[3] == "requests" {
			result, e = h.s.OwnerOutcome(r.Context(), session.Value, tenant, parts[4])
		} else {
			e = h.s.WithOwner(r.Context(), session.Value, tenant, false, func(o ledger.TenantAdmin) error {
				switch {
				case len(parts) == 4 && parts[3] == "books":
					list, err := o.BooksBounded(r.Context(), 1000)
					result = list
					return err
				case len(parts) == 4 && parts[3] == "connections":
					list, err := o.ConnectionsBounded(r.Context(), 1000)
					result = list
					return err
				default:
					return ledger.ErrNotFound
				}
			})
		}
		if e != nil {
			ownerFailure(w, e)
			return
		}
		oauthJSON(w, 200, result)
		return
	}
	if r.Method != "POST" || !h.login.CheckCSRF(r, session.Value) || len(r.Header.Values("Content-Type")) != 1 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		ownerFailure(w, ErrDenied)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	body, e := io.ReadAll(r.Body)
	if e != nil {
		ownerFailure(w, ledger.ErrInvalid)
		return
	}
	var operation, key string
	var canonical json.RawMessage
	var call func(ledger.TenantAdmin, string) (json.RawMessage, error)
	switch {
	case len(parts) == 4 && parts[3] == "books":
		var q struct {
			Key      string `json:"request_key"`
			Name     string `json:"name"`
			Currency string `json:"currency"`
			Scale    int    `json:"scale"`
		}
		if strictjson.Decode(body, &q) != nil {
			ownerFailure(w, ledger.ErrInvalid)
			return
		}
		operation, key = "create_book", q.Key
		canonical, _ = json.Marshal(q)
		call = func(o ledger.TenantAdmin, _ string) (json.RawMessage, error) {
			v, e := o.CreateBook(r.Context(), q.Name, q.Currency, q.Scale)
			b, _ := json.Marshal(v)
			return b, e
		}
	case len(parts) == 6 && parts[3] == "connections" && ledger.ValidID(parts[4]) && parts[5] == "revoke":
		var q struct {
			Key string `json:"request_key"`
		}
		if strictjson.Decode(body, &q) != nil {
			ownerFailure(w, ledger.ErrInvalid)
			return
		}
		operation, key = "revoke_connection", q.Key
		canonical, _ = json.Marshal(struct{ Key, Connection string }{q.Key, parts[4]})
		call = func(o ledger.TenantAdmin, _ string) (json.RawMessage, error) {
			if e := o.Revoke(r.Context(), parts[4]); e != nil {
				return nil, e
			}
			return json.RawMessage(`{"revoked":true}`), nil
		}
	case len(parts) == 6 && parts[3] == "books" && ledger.ValidID(parts[4]) && parts[5] == "close-period":
		var q struct {
			Key     string `json:"request_key"`
			From    string `json:"from"`
			Through string `json:"through"`
			Reason  string `json:"reason,omitempty"`
		}
		if strictjson.Decode(body, &q) != nil {
			ownerFailure(w, ledger.ErrInvalid)
			return
		}
		operation, key = "close_period", q.Key
		canonical, _ = json.Marshal(struct{ Key, Book, From, Through, Reason string }{q.Key, parts[4], q.From, q.Through, q.Reason})
		// A close is the owner's recorded decision, with the owner as actor.
		call = func(o ledger.TenantAdmin, who string) (json.RawMessage, error) {
			args, _ := json.Marshal(ledger.ClosePeriod{RequestKey: q.Key, From: q.From, Through: q.Through, Reason: q.Reason})
			if _, e := o.AccountChange(r.Context(), parts[4], who, "close_period", args); e != nil {
				return nil, e
			}
			return json.RawMessage(`{"closed":true}`), nil
		}
	default:
		ownerFailure(w, ledger.ErrNotFound)
		return
	}
	result, e := h.s.OwnerRequestAs(r.Context(), session.Value, tenant, key, operation, canonical, h.admit, call)
	if e != nil {
		ownerFailure(w, e)
		return
	}
	oauthJSON(w, 200, result)
}
