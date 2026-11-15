package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

const approveBatchPath = "/api/v1/agent-owner/approve-batch"

// batchReason is recorded with each decision a batch makes.
const batchReason = "Approved together with other routine items after review."

// maxBatchApprovals bounds one batch; the app sends larger groups in turns.
const maxBatchApprovals = 50

type batchApproval struct {
	ProposalID       string `json:"plan_id"`
	SHA256           string `json:"sha256"`
	ExpectedRevision int64  `json:"expected_decision_revision"`
}

type batchOutcome struct {
	ProposalID string `json:"plan_id"`
	Outcome    string `json:"outcome"`
}

// approveBatch approves several reviewed proposals with one fresh sign-in.
// Each is decided on its own, exactly as a single approval: one that went
// stale or was already decided is reported and the rest still proceed. A
// lapsed sign-in stops the batch where it is, and the owner signs in again
// to approve the rest.
func (a *webAPI) approveBatch(w http.ResponseWriter, r *http.Request, session string) {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16384))
	var q struct {
		BookID     string          `json:"book_id"`
		RequestKey string          `json:"request_key"`
		Items      []batchApproval `json:"items"`
	}
	if err != nil || strictjson.Decode(data, &q) != nil || !ledger.ValidID(q.BookID) || len(q.Items) == 0 || len(q.Items) > maxBatchApprovals ||
		len(q.RequestKey) < 1 || len(q.RequestKey) > 80 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	seen := map[string]bool{}
	for _, item := range q.Items {
		if !ledger.ValidID(item.ProposalID) || seen[item.ProposalID] {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		seen[item.ProposalID] = true
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if err != nil {
		apiFailure(w, err)
		return
	}
	out := []batchOutcome{}
	for _, item := range q.Items {
		// Each decision's key is fixed by the batch and the proposal, so a
		// retried batch recovers each decision instead of repeating it.
		_, err := a.sessions.DecideOperation(r.Context(), session, tenant, ledger.DecideOperation{BookID: q.BookID, ProposalID: item.ProposalID,
			SHA256: item.SHA256, RequestKey: q.RequestKey + "." + item.ProposalID, ExpectedRevision: item.ExpectedRevision, Decision: "approve", Reason: batchReason}, a.admitDecision)
		outcome := batchOutcome{ProposalID: item.ProposalID, Outcome: "approved"}
		switch {
		case err == nil:
		case errors.Is(err, ledger.ErrFreshOwnerProof):
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "fresh_authentication_required", "message": "Confirm it’s you to approve the rest.",
				"outcomes": out})
			return
		case errors.Is(err, ledger.ErrProposalStale):
			outcome.Outcome = "stale"
		case errors.Is(err, ledger.ErrProposalDecided):
			outcome.Outcome = "decided"
		case errors.Is(err, ledger.ErrProposalExpired):
			outcome.Outcome = "expired"
		default:
			outcome.Outcome = "failed"
			// Why an approval failed is otherwise known to nobody.
			slog.Warn("approval in a batch failed", "error", err.Error())
		}
		out = append(out, outcome)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "outcomes": out})
}
