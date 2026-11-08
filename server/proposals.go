package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/operationref"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// Apply the underlying posting policy under the recorded preparer scope and
// the live owner authorization. An exact decision retry runs neither hook.
func operationDecisionAdmission(policy Admission, owner access.OwnerAdmission) ledger.DecisionAdmission {
	if policy == nil && owner == nil {
		return nil
	}
	transaction, _ := policy.(TransactionAdmission)
	return func(ctx context.Context, tx pgx.Tx, scope ledger.Scope, operation string, input json.RawMessage) error {
		if operation != "decide_operation" {
			if policy != nil && policy.Check(ctx, scope, operation) != nil {
				return ledger.ErrDenied
			}
			if transaction != nil {
				if transaction.CheckTransaction(ctx, tx, scope, operation, input) != nil {
					return ledger.ErrDenied
				}
				return nil
			}
		}
		if owner != nil {
			return owner(ctx, tx, scope.TenantID, operation, input)
		}
		return nil
	}
}

func proposalQuery(r *http.Request, cursorName string) (string, int64, int, error) {
	if len(r.URL.RawQuery) > 1024 {
		return "", 0, 0, ledger.ErrInvalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["book_id"]) != 1 || !ledger.ValidID(q.Get("book_id")) {
		return "", 0, 0, ledger.ErrInvalid
	}
	if cursorName == "before" && q.Has("status") {
		if len(q["status"]) != 1 || q.Get("status") != "approval_required" {
			return "", 0, 0, ledger.ErrInvalid
		}
		q.Del("status")
	}
	var cursor int64
	limit := 25
	for key, values := range q {
		if len(values) != 1 {
			return "", 0, 0, ledger.ErrInvalid
		}
		if key == "book_id" {
			continue
		}
		if cursorName == "" || key != "limit" && key != cursorName {
			return "", 0, 0, ledger.ErrInvalid
		}
		n, e := strconv.ParseInt(values[0], 10, 64)
		if e != nil || n < 0 {
			return "", 0, 0, ledger.ErrInvalid
		}
		if key == "limit" {
			if n < 1 || n > 100 {
				return "", 0, 0, ledger.ErrInvalid
			}
			limit = int(n)
		} else {
			cursor = n
		}
	}
	return q.Get("book_id"), cursor, limit, nil
}

func (a *webAPI) proposalRead(w http.ResponseWriter, r *http.Request, session string) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/agent-owner/plans")
	id := ""
	history := false
	cursorName := "before"
	if path != "" {
		parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
		if len(parts) > 2 || !ledger.ValidID(parts[0]) || len(parts) == 2 && parts[1] != "decisions" {
			apiFailure(w, ledger.ErrNotFound)
			return
		}
		id = parts[0]
		history = len(parts) == 2
		cursorName = ""
		if history {
			cursorName = "after"
		}
	}
	book, cursor, limit, err := proposalQuery(r, cursorName)
	if err != nil {
		apiFailure(w, err)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out any
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		switch {
		case id == "":
			out, err = owner.OperationProposals(r.Context(), book, cursor, limit, r.URL.Query().Get("status") == "approval_required")
		case history:
			out, err = owner.OperationDecisions(r.Context(), book, id, cursor, limit)
		default:
			out, err = owner.OperationProposal(r.Context(), book, id)
		}
		return err
	})
	if err != nil {
		proposalFailure(w, err, "")
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *webAPI) proposalDecision(w http.ResponseWriter, r *http.Request, session string) {
	if r.URL.RawQuery != "" || len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	var q struct {
		BookID           string `json:"book_id"`
		ProposalID       string `json:"plan_id"`
		SHA256           string `json:"sha256"`
		RequestKey       string `json:"request_key"`
		ExpectedRevision int64  `json:"expected_decision_revision"`
		Reason           string `json:"reason"`
	}
	if strictjson.Decode(data, &q) != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	for _, key := range []string{"book_id", "plan_id", "sha256", "request_key", "expected_decision_revision", "reason"} {
		if _, ok := fields[key]; !ok {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	decision := "approve"
	if r.URL.Path == "/api/v1/agent-owner/reject" {
		decision = "reject"
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if err != nil {
		apiFailure(w, err)
		return
	}
	out, err := a.sessions.DecideOperation(r.Context(), session, tenant, ledger.DecideOperation{BookID: q.BookID, ProposalID: q.ProposalID, SHA256: q.SHA256, RequestKey: q.RequestKey, ExpectedRevision: q.ExpectedRevision, Decision: decision, Reason: q.Reason}, a.admitDecision)
	if err != nil {
		proposalFailure(w, err, operationref.Encode(q.BookID, q.ProposalID))
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		Version int `json:"version"`
		ledger.OperationDecisionResult
	}{1, out})
}

func proposalFailure(w http.ResponseWriter, err error, review string) {
	status, code, message, next := 0, "", "", ""
	switch {
	case errors.Is(err, ledger.ErrProposalStale):
		status, code, message, next = http.StatusConflict, "proposal_stale", "The operation's dependencies changed. Prepare and review a fresh proposal.", "prepare_operation"
	case errors.Is(err, ledger.ErrProposalDecided):
		status, code, message, next = http.StatusConflict, "proposal_decided", "This proposal was already posted. Open it to see the entry.", "read_proposal"
	case errors.Is(err, ledger.ErrProposalExpired):
		status, code, message, next = http.StatusConflict, "proposal_expired", "The proposal expired. Prepare and review a fresh proposal.", "prepare_operation"
	case errors.Is(err, ledger.ErrUncertain):
		status, code, message, next = http.StatusServiceUnavailable, "decision_outcome_unresolved", "Check the proposal for the decision and any posted entry before deciding again.", "read_proposal"
	case errors.Is(err, ledger.ErrFreshOwnerProof):
		status, code, message, next = http.StatusForbidden, "fresh_authentication_required", "Sign in again, then review this same proposal.", "sign_in_and_review"
	default:
		apiFailure(w, err)
		return
	}
	w.WriteHeader(status)
	out := map[string]string{"error": code, "message": message, "next_step": next}
	if errors.Is(err, ledger.ErrFreshOwnerProof) && review != "" {
		out["sign_in_href"] = "/auth/login?operation=" + review
	}
	_ = json.NewEncoder(w).Encode(out)
}

func reviewReference(r *http.Request) (book, proposal, operation string, err error) {
	if len(r.URL.RawQuery) > 128 {
		return "", "", "", ledger.ErrInvalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 1 || len(q["operation"]) != 1 {
		return "", "", "", ledger.ErrInvalid
	}
	operation = q.Get("operation")
	book, proposal, ok := operationref.Decode(operation)
	if !ok {
		return "", "", "", ledger.ErrInvalid
	}
	return book, proposal, operation, nil
}

func (a *webAPI) reviewState(r *http.Request, session string) (ledger.ProposalState, error) {
	var out ledger.ProposalState
	book, proposal, _, err := reviewReference(r)
	if err != nil {
		return out, err
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		return out, err
	}
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		out, err = owner.OperationProposal(r.Context(), book, proposal)
		return err
	})
	return out, err
}

func (a *webAPI) proposalReview(w http.ResponseWriter, r *http.Request, session string) {
	out, err := a.reviewState(r, session)
	if err != nil {
		proposalFailure(w, err, "")
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *webAPI) proposalReviewPage(w http.ResponseWriter, r *http.Request, app *webApp) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if len(r.Header.Values("Authorization")) != 0 || r.Method != http.MethodGet && r.Method != http.MethodHead {
		failure(w, http.StatusForbidden)
		return
	}
	_, _, operation, err := reviewReference(r)
	if err != nil {
		apiFailure(w, err)
		return
	}
	cookies := r.CookiesNamed(access.SessionCookie)
	if len(cookies) > 1 {
		failure(w, http.StatusForbidden)
		return
	}
	if len(cookies) == 0 {
		http.Redirect(w, r, "/auth/login?operation="+operation, http.StatusSeeOther)
		return
	}
	if _, err = a.sessions.Identity(r.Context(), cookies[0].Value); err != nil {
		http.Redirect(w, r, "/auth/login?operation="+operation, http.StatusSeeOther)
		return
	}
	if _, err = a.reviewState(r, cookies[0].Value); err != nil {
		proposalFailure(w, err, "")
		return
	}
	app.ServeHTTP(w, r)
}
