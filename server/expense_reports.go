package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// Expense reports, for the owner in the browser only: a session, never an
// agent's token. Agents draft and propose through their own tools; they
// never submit, approve, match or resolve.
const expenseReportsPath = "/api/v1/expense-reports"

func expenseReportsRoute(path string) bool {
	return path == expenseReportsPath || strings.HasPrefix(path, expenseReportsPath+"/")
}

// ReportProver asks the owner's passkey to approve one action on one
// report, bound to its revision and snapshot.
type ReportProver interface {
	BeginReportProof(ctx context.Context, session, tenant, book, purpose, bound string) (access.PasskeyChallenge, error)
	FinishBookProof(ctx context.Context, token, session string, response json.RawMessage) error
}

func (a *webAPI) expenseReports(w http.ResponseWriter, r *http.Request, session string) {
	if len(r.Header.Values("Authorization")) != 0 {
		failure(w, http.StatusForbidden)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, expenseReportsPath), "/")
	parts := []string{}
	if rest != "" {
		parts = strings.Split(rest, "/")
	}
	get, post := r.Method == http.MethodGet, r.Method == http.MethodPost
	switch {
	case get && len(parts) == 0:
		a.reportList(w, r, session)
	case post && len(parts) == 0:
		a.reportCreate(w, r, session)
	case get && len(parts) == 1 && parts[0] == "candidates":
		a.reportCandidates(w, r, session)
	case get && len(parts) == 1 && parts[0] == "reconciliation":
		a.reportReconciliation(w, r, session)
	case len(parts) == 1 && parts[0] == "people":
		a.reportPeople(w, r, session)
	case len(parts) == 1 && parts[0] == "policy":
		a.reportPolicy(w, r, session)
	case post && len(parts) == 3 && parts[0] == "allocations" && parts[2] == "remove":
		a.reportAllocationRemove(w, r, session, parts[1])
	case post && len(parts) == 3 && parts[0] == "recoverables" && parts[2] == "resolve":
		a.reportResolve(w, r, session, parts[1])
	case post && len(parts) == 3 && parts[0] == "recoverables" && parts[2] == "review":
		a.reportReview(w, r, session, parts[1])
	case post && len(parts) == 3 && parts[0] == "residuals" && parts[2] == "classify":
		a.reportClassify(w, r, session, parts[1])
	case get && len(parts) == 1 && parts[0] == "person-side":
		a.personSideList(w, r, session)
	case post && len(parts) == 2 && parts[0] == "person-side":
		a.personSideRecord(w, r, session, parts[1])
	case post && len(parts) == 3 && parts[0] == "person-side" && parts[1] == "corrections":
		a.personSideCorrect(w, r, session, parts[2])
	case get && len(parts) == 1:
		a.reportRead(w, r, session, parts[0])
	case len(parts) >= 2:
		a.reportAction(w, r, session, parts)
	default:
		failure(w, http.StatusNotFound)
	}
}

// reportAnswer writes a report change's failure in the screens' terms.
func reportAnswer(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var conflict *ledger.ReportConflict
	switch {
	case errors.Is(err, ledger.ErrRevisionChanged):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "error": "revision_changed", "message": ledger.ErrRevisionChanged.Error()})
	case errors.As(err, &conflict):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "error": "claim_taken", "message": conflict.Reason})
	default:
		apiFailure(w, err)
	}
	return true
}

func readReportBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil || strictjson.Decode(body, v) != nil {
		apiFailure(w, ledger.ErrInvalid)
		return false
	}
	return true
}

// linkTenant is the owner's tenant holding a link that can have reports,
// and the business Book under it.
func (a *webAPI) linkTenant(ctx context.Context, session, link string) (string, string, error) {
	tenants, err := a.sessions.Tenants(ctx, session)
	if err != nil {
		return "", "", err
	}
	last := error(ledger.ErrNotFound)
	for _, t := range tenants {
		var business string
		err := a.sessions.WithOwner(ctx, session, t, false, func(o ledger.TenantAdmin) error {
			var err error
			business, err = o.LinkBusiness(ctx, link)
			return err
		})
		if err == nil {
			return t, business, nil
		}
		last = err
	}
	return "", "", last
}

// reportTenant finds the tenant and business Book of a reference.
func (a *webAPI) reportTenant(ctx context.Context, session, reference string) (string, string, error) {
	tenants, err := a.sessions.Tenants(ctx, session)
	if err != nil {
		return "", "", err
	}
	for _, t := range tenants {
		var book string
		err := a.sessions.WithOwner(ctx, session, t, false, func(o ledger.TenantAdmin) error {
			var err error
			book, err = o.ReportBook(ctx, reference)
			return err
		})
		if err == nil {
			return t, book, nil
		}
	}
	return "", "", ledger.ErrNotFound
}

func (a *webAPI) reportList(w http.ResponseWriter, r *http.Request, session string) {
	link := r.URL.Query().Get("link_id")
	tenant, _, err := a.linkTenant(r.Context(), session, link)
	if reportAnswer(w, err) {
		return
	}
	var out []ledger.ReportSummary
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		out, err = o.Reports(r.Context(), link)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "reports": out})
}

func (a *webAPI) reportCreate(w http.ResponseWriter, r *http.Request, session string) {
	var q ledger.NewReport
	if !readReportBody(w, r, &q) {
		return
	}
	tenant, book, err := a.linkTenant(r.Context(), session, q.LinkID)
	if reportAnswer(w, err) {
		return
	}
	var d ledger.ReportDetail
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		ref, err := o.CreateReport(r.Context(), who, q)
		if err != nil {
			return err
		}
		d, err = o.ExpenseReport(r.Context(), book, ref)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "report": d})
}

func (a *webAPI) reportCandidates(w http.ResponseWriter, r *http.Request, session string) {
	link := r.URL.Query().Get("link_id")
	tenant, _, err := a.linkTenant(r.Context(), session, link)
	if reportAnswer(w, err) {
		return
	}
	var out []ledger.Candidate
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		out, err = o.ReportCandidates(r.Context(), link)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "costs": out})
}

func (a *webAPI) reportReconciliation(w http.ResponseWriter, r *http.Request, session string) {
	link := r.URL.Query().Get("link_id")
	tenant, _, err := a.linkTenant(r.Context(), session, link)
	if reportAnswer(w, err) {
		return
	}
	var out ledger.Reconciliation
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		out, err = o.ReportReconciliation(r.Context(), link)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "reconciliation": out})
}

// reportPeople reads and writes the people a business pays back. Writing
// needs a recent sign-in: their capacity is the business's record.
func (a *webAPI) reportPeople(w http.ResponseWriter, r *http.Request, session string) {
	if r.Method == http.MethodGet {
		book := r.URL.Query().Get("book_id")
		tenant, err := a.sessions.BookTenant(r.Context(), session, book)
		if reportAnswer(w, err) {
			return
		}
		var out []ledger.Person
		err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			out, err = o.People(r.Context(), book)
			return err
		})
		if reportAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "people": out})
		return
	}
	if r.Method != http.MethodPost {
		failure(w, http.StatusNotFound)
		return
	}
	var q struct {
		BookID string `json:"book_id"`
		ledger.PersonChange
	}
	if !readReportBody(w, r, &q) {
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if reportAnswer(w, err) {
		return
	}
	var out ledger.Person
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		out, err = o.SavePerson(r.Context(), q.BookID, who, q.PersonChange)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "person": out})
}

func (a *webAPI) reportPolicy(w http.ResponseWriter, r *http.Request, session string) {
	if r.Method == http.MethodGet {
		book := r.URL.Query().Get("book_id")
		tenant, err := a.sessions.BookTenant(r.Context(), session, book)
		if reportAnswer(w, err) {
			return
		}
		var out ledger.ReportPolicy
		var versions []ledger.ReportPolicy
		err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			if out, err = o.ReportPolicy(r.Context(), book); err != nil {
				return err
			}
			versions, err = o.ReportPolicies(r.Context(), book)
			return err
		})
		if reportAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "policy": out, "versions": versions})
		return
	}
	if r.Method != http.MethodPost {
		failure(w, http.StatusNotFound)
		return
	}
	var q struct {
		BookID string `json:"book_id"`
		ledger.PolicyAdoption
	}
	if !readReportBody(w, r, &q) {
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if reportAnswer(w, err) {
		return
	}
	// Adopting is the owner's own act, with a recent sign-in.
	var out ledger.ReportPolicy
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		out, err = o.AdoptReportPolicy(r.Context(), q.BookID, who, who, q.PolicyAdoption)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "policy": out})
}

func (a *webAPI) reportRead(w http.ResponseWriter, r *http.Request, session, reference string) {
	tenant, book, err := a.reportTenant(r.Context(), session, reference)
	if reportAnswer(w, err) {
		return
	}
	var d ledger.ReportDetail
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		d, err = o.ExpenseReport(r.Context(), book, reference)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "report": d})
}

// reportAction is every change to one report, addressed by its reference.
func (a *webAPI) reportAction(w http.ResponseWriter, r *http.Request, session string, parts []string) {
	reference := parts[0]
	tenant, book, err := a.reportTenant(r.Context(), session, reference)
	if reportAnswer(w, err) {
		return
	}
	if r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "matches" {
		var left string
		var proposals []ledger.MatchProposal
		err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			left, proposals, err = o.ReportMatches(r.Context(), book, reference)
			return err
		})
		if reportAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "left_minor": left, "proposals": proposals})
		return
	}
	if r.Method == http.MethodGet && len(parts) == 2 && parts[1] == "personal-notes" {
		// The person's own notes, read from their Book.
		var notes []ledger.ReportNote
		err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			notes, err = o.PersonalReportNotes(r.Context(), book, reference)
			return err
		})
		if reportAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "notes": notes})
		return
	}
	if r.Method != http.MethodPost {
		failure(w, http.StatusNotFound)
		return
	}
	act := func(call func(o ledger.TenantAdmin, who string) error) bool {
		err := a.sessions.OwnerAct(r.Context(), session, tenant, call)
		return !reportAnswer(w, err)
	}
	reply := func(extra map[string]any) {
		var d ledger.ReportDetail
		err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			d, err = o.ExpenseReport(r.Context(), book, reference)
			return err
		})
		if reportAnswer(w, err) {
			return
		}
		out := map[string]any{"schema_version": 1, "report": d}
		for k, v := range extra {
			out[k] = v
		}
		_ = json.NewEncoder(w).Encode(out)
	}
	switch {
	case len(parts) == 2 && parts[1] == "claims":
		var q ledger.ClaimInput
		if !readReportBody(w, r, &q) {
			return
		}
		var claim string
		if act(func(o ledger.TenantAdmin, who string) error {
			var err error
			claim, err = o.AddReportClaim(r.Context(), book, who, reference, q)
			return err
		}) {
			reply(map[string]any{"claim_id": claim})
		}
	case len(parts) == 4 && parts[1] == "claims" && parts[3] == "remove":
		if r.ContentLength > 0 && !readReportBody(w, r, &struct{}{}) {
			return
		}
		if act(func(o ledger.TenantAdmin, who string) error {
			return o.RemoveReportClaim(r.Context(), book, who, reference, parts[2])
		}) {
			reply(nil)
		}
	case len(parts) == 3 && parts[1] == "claims":
		var q ledger.ClaimInput
		if !readReportBody(w, r, &q) {
			return
		}
		if act(func(o ledger.TenantAdmin, who string) error {
			return o.EditReportClaim(r.Context(), book, who, reference, parts[2], q)
		}) {
			reply(nil)
		}
	case len(parts) == 4 && parts[1] == "claims" && parts[3] == "recorded-account":
		var q struct {
			AccountID string `json:"account_id"`
		}
		if !readReportBody(w, r, &q) {
			return
		}
		if act(func(o ledger.TenantAdmin, who string) error {
			return o.ChangeClaimRecordedAccount(r.Context(), book, who, reference, parts[2], q.AccountID)
		}) {
			reply(nil)
		}
	case len(parts) == 4 && parts[1] == "claims" && parts[3] == "evidence":
		a.reportEvidence(w, r, session, tenant, book, reference, parts[2], reply)
	case len(parts) == 2 && parts[1] == "proof":
		a.reportProof(w, r, session, tenant, book, reference)
	case len(parts) == 2 && (parts[1] == "submit" || parts[1] == "approve"):
		a.reportDecide(w, r, session, tenant, book, reference, parts[1] == "approve", reply)
	case len(parts) == 2 && (parts[1] == "return" || parts[1] == "withdraw" || parts[1] == "amend" || parts[1] == "reconcile"):
		var q struct {
			Note   string `json:"note,omitempty"`
			Reason string `json:"reason,omitempty"`
		}
		if !readReportBody(w, r, &q) {
			return
		}
		if act(func(o ledger.TenantAdmin, who string) error {
			switch parts[1] {
			case "return":
				return o.ReturnReport(r.Context(), book, who, who, reference, q.Note)
			case "withdraw":
				return o.WithdrawReport(r.Context(), book, who, who, reference, q.Reason)
			case "reconcile":
				return o.ReconcileReport(r.Context(), book, who, reference, q.Note)
			}
			return o.AmendReport(r.Context(), book, who, reference, q.Reason)
		}) {
			reply(nil)
		}
	case len(parts) == 2 && parts[1] == "notes":
		var q struct {
			Audience string `json:"audience"`
			Text     string `json:"text"`
		}
		if !readReportBody(w, r, &q) {
			return
		}
		var note ledger.ReportNote
		if act(func(o ledger.TenantAdmin, who string) error {
			var err error
			note, err = o.AddReportNote(r.Context(), book, who, reference, q.Audience, q.Text)
			return err
		}) {
			if q.Audience == "personal" {
				// A personal note never comes back in the business's read.
				_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "note": note})
				return
			}
			reply(map[string]any{"note": note})
		}
	case len(parts) == 2 && parts[1] == "delete":
		if !readReportBody(w, r, &struct{}{}) {
			return
		}
		if act(func(o ledger.TenantAdmin, who string) error {
			return o.DeleteDraftReport(r.Context(), book, who, reference)
		}) {
			_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "deleted": reference})
		}
	case len(parts) == 2 && parts[1] == "payments":
		var q struct {
			BankItemID  string              `json:"bank_item_id,omitempty"`
			EntryID     string              `json:"entry_id,omitempty"`
			AmountMinor string              `json:"amount_minor,omitempty"`
			Also        []ledger.ReportPart `json:"also,omitempty"`
		}
		if !readReportBody(w, r, &q) {
			return
		}
		amount := int64(0)
		if q.AmountMinor != "" {
			n, err := strconv.ParseInt(q.AmountMinor, 10, 64)
			if err != nil || n <= 0 {
				apiFailure(w, ledger.ErrInvalid)
				return
			}
			amount = n
		}
		var out ledger.PaymentResult
		if act(func(o ledger.TenantAdmin, who string) error {
			entry := q.EntryID
			if entry == "" {
				var err error
				if entry, err = o.RecordReportPayment(r.Context(), book, who, reference, q.BankItemID); err != nil {
					return err
				}
			}
			var err error
			out, err = o.ApplyReportPayment(r.Context(), book, who, reference, entry, amount, q.Also)
			return err
		}) {
			reply(map[string]any{"allocations": out.Allocations, "excess_minor": out.ExcessMinor, "residual_id": out.ResidualID})
		}
	case len(parts) == 2 && parts[1] == "statements":
		if r.ContentLength > 0 && !readReportBody(w, r, &struct{}{}) {
			return
		}
		statement, err := a.issueStatement(r.Context(), session, tenant, book, reference)
		if reportAnswer(w, err) {
			return
		}
		reply(map[string]any{"statement": statement})
	default:
		failure(w, http.StatusNotFound)
	}
}

// reportProof starts the passkey confirmation for submitting or approving
// the report as it is now.
func (a *webAPI) reportProof(w http.ResponseWriter, r *http.Request, session, tenant, book, reference string) {
	var q struct {
		Purpose        string `json:"purpose"`
		Revision       int    `json:"revision"`
		SnapshotSHA256 string `json:"snapshot_sha256"`
	}
	if !readReportBody(w, r, &q) {
		return
	}
	prover, ok := a.prover.(ReportProver)
	if !ok || prover == nil {
		passkeyRequired(w, "Submitting a report needs a passkey. Add one to your sign-in, then try again.")
		return
	}
	var target ledger.ProofTarget
	err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		target, err = o.ReportProofTarget(r.Context(), book, reference)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	if target.Revision != q.Revision || target.SnapshotSHA256 != q.SnapshotSHA256 {
		reportAnswer(w, ledger.ErrRevisionChanged)
		return
	}
	challenge, err := prover.BeginReportProof(r.Context(), session, tenant, book, q.Purpose, target.Binding())
	if errors.Is(err, access.ErrProofLimit) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "refused", "reason": "limit", "message": "Too many passkey requests in the last hour. Try again later."})
		return
	}
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "token": challenge.Token, "options": challenge.Options})
}

// reportDecide submits (and, where the owner may, approves) or approves a
// report with the passkey's answer, then stores the approved revision's
// PDF in the business's Documents.
func (a *webAPI) reportDecide(w http.ResponseWriter, r *http.Request, session, tenant, book, reference string, approver bool, reply func(map[string]any)) {
	var q struct {
		Revision       int             `json:"revision"`
		SnapshotSHA256 string          `json:"snapshot_sha256"`
		Attestation    bool            `json:"attestation,omitempty"`
		Approve        bool            `json:"approve,omitempty"`
		Token          string          `json:"token"`
		Credential     json.RawMessage `json:"credential"`
	}
	if !readReportBody(w, r, &q) {
		return
	}
	prover, ok := a.prover.(ReportProver)
	if !ok || prover == nil || prover.FinishBookProof(r.Context(), q.Token, session, q.Credential) != nil {
		passkeyRequired(w, "Your passkey didn't confirm this. Try again.")
		return
	}
	var events []ledger.ReportEvent
	err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		if approver {
			events, err = o.ApproveReport(r.Context(), book, who, who, reference, q.Revision, q.SnapshotSHA256, q.Token)
		} else {
			events, err = o.SubmitReport(r.Context(), book, who, who, reference, ledger.SubmitReport{Revision: q.Revision, SnapshotSHA256: q.SnapshotSHA256,
				Attestation: q.Attestation, Approve: q.Approve, ProofToken: q.Token})
		}
		return err
	})
	if err != nil && strings.Contains(err.Error(), "someone other than") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "error": "independent_approval_required", "message": err.Error()})
		return
	}
	if reportAnswer(w, err) {
		return
	}
	for _, e := range events {
		if e.Kind == "approved" {
			// The approved revision's document; a failure here leaves the
			// approval standing and the document to be made again.
			_ = a.storeRevisionPDF(r.Context(), session, tenant, book, reference, e.Revision)
		}
	}
	reply(map[string]any{"events": events})
}

// reportEvidence attaches a receipt to a claim. A receipt in the person's
// Book is copied into the business's Documents first: only that receipt,
// never the statement or anything else of theirs.
func (a *webAPI) reportEvidence(w http.ResponseWriter, r *http.Request, session, tenant, book, reference, claim string, reply func(map[string]any)) {
	var q struct {
		DocumentID   string `json:"document_id"`
		SourceBookID string `json:"source_book_id,omitempty"`
		ledger.EvidenceInput
	}
	if !readReportBody(w, r, &q) {
		return
	}
	if !ledger.ValidID(q.DocumentID) || q.SourceBookID != "" && !ledger.ValidID(q.SourceBookID) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	document := q.DocumentID
	if q.SourceBookID != "" && q.SourceBookID != book {
		var d ledger.ReportDetail
		err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			d, err = o.ExpenseReport(r.Context(), book, reference)
			return err
		})
		if reportAnswer(w, err) {
			return
		}
		if d.Person.PersonalBookID != q.SourceBookID {
			apiFailure(w, &ledger.RefusedError{Reason: "A receipt comes from the business's Documents or the person's own Book."})
			return
		}
		copied, err := a.copyDocument(r.Context(), session, tenant, q.SourceBookID, book, q.DocumentID)
		if reportAnswer(w, err) {
			return
		}
		document = copied
	}
	err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		return o.AddClaimEvidence(r.Context(), book, who, claim, document, q.EvidenceInput)
	})
	if reportAnswer(w, err) {
		return
	}
	reply(map[string]any{"document_id": document})
}

func (a *webAPI) reportAllocationRemove(w http.ResponseWriter, r *http.Request, session, allocation string) {
	var q struct {
		BookID string `json:"book_id"`
		Reason string `json:"reason"`
	}
	if !readReportBody(w, r, &q) {
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if reportAnswer(w, err) {
		return
	}
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		return o.RemoveReportAllocation(r.Context(), q.BookID, who, allocation, q.Reason)
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "removed": allocation})
}

func (a *webAPI) reportResolve(w http.ResponseWriter, r *http.Request, session, recoverable string) {
	var q struct {
		BookID string `json:"book_id"`
		// Status moves the recovery or the tax track; resolution is its
		// earlier name.
		Status        string `json:"status,omitempty"`
		Resolution    string `json:"resolution,omitempty"`
		EntryID       string `json:"entry_id,omitempty"`
		Reference     string `json:"reference,omitempty"`
		Note          string `json:"note,omitempty"`
		TreatmentNote string `json:"treatment_note,omitempty"`
	}
	if !readReportBody(w, r, &q) {
		return
	}
	status, note := q.Status, q.Note
	if status == "" {
		status = q.Resolution
	}
	if note == "" {
		note = q.TreatmentNote
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if reportAnswer(w, err) {
		return
	}
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		return o.ResolveRecoverable(r.Context(), q.BookID, who, recoverable, status, q.EntryID, q.Reference, note)
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "resolved": recoverable, "status": status})
}

// reportReview keeps a review of a recoverable.
func (a *webAPI) reportReview(w http.ResponseWriter, r *http.Request, session, recoverable string) {
	var q struct {
		BookID string `json:"book_id"`
		ledger.RecoverableReview
	}
	if !readReportBody(w, r, &q) {
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if reportAnswer(w, err) {
		return
	}
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		return o.ReviewRecoverable(r.Context(), q.BookID, who, recoverable, q.RecoverableReview)
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "reviewed": recoverable})
}

// reportClassify says what a payment's leftover was.
func (a *webAPI) reportClassify(w http.ResponseWriter, r *http.Request, session, residual string) {
	var q struct {
		BookID         string `json:"book_id"`
		Classification string `json:"classification"`
		Reference      string `json:"reference,omitempty"`
		Note           string `json:"note,omitempty"`
	}
	if !readReportBody(w, r, &q) {
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if reportAnswer(w, err) {
		return
	}
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		return o.ClassifyResidual(r.Context(), q.BookID, who, residual, q.Classification, q.Reference, q.Note)
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "classified": residual, "classification": q.Classification})
}

// storeDocument keeps bytes Vult made, or copied, as an ordinary document
// of the Book, filed with the company's records.
func (a *webAPI) storeDocument(ctx context.Context, session, tenant, book, key, name string, data []byte) (string, string, error) {
	sum := sha256.Sum256(data)
	q := ledger.AddDocument{RequestKey: key, Name: name, MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	var result ledger.OwnerDocumentUpload
	var err error
	if a.uploadDocument != nil {
		result, err = a.uploadDocument(ctx, session, book, q, bytes.NewReader(data))
	} else {
		result, err = a.sessions.UploadOwnerDocument(ctx, session, book, q, a.documents, bytes.NewReader(data), a.admit)
	}
	if err != nil {
		return "", "", err
	}
	err = a.sessions.OwnerAct(ctx, session, tenant, func(o ledger.TenantAdmin, who string) error {
		_, err := o.SetDocumentGroup(ctx, book, who, ledger.SetDocumentGroup{DocumentID: result.Document.ID, Group: "company"})
		return err
	})
	return result.Document.ID, q.SHA256, err
}

// copyDocument copies one document of one Book into another, as a new
// document there with the same bytes.
func (a *webAPI) copyDocument(ctx context.Context, session, tenant, from, to, id string) (string, error) {
	var original bytes.Buffer
	var doc ledger.Document
	err := a.sessions.WithOwner(ctx, session, tenant, false, func(o ledger.TenantAdmin) error {
		inspection, err := o.Document(ctx, from, id)
		doc = inspection.Document
		return err
	})
	if err != nil {
		return "", err
	}
	if doc.Size > 32<<20 {
		return "", ledger.ErrInvalid
	}
	if a.readDocument != nil {
		err = a.readDocument(ctx, session, from, id, &original)
	} else {
		err = a.sessions.WithOwner(ctx, session, tenant, false, func(o ledger.TenantAdmin) error {
			return o.DownloadDocument(ctx, from, id, a.documents, &original)
		})
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(original.Bytes())
	q := ledger.AddDocument{RequestKey: "receipt-" + id, Name: doc.Name, MediaType: doc.MediaType, Size: int64(original.Len()), SHA256: hex.EncodeToString(sum[:])}
	var result ledger.OwnerDocumentUpload
	if a.uploadDocument != nil {
		result, err = a.uploadDocument(ctx, session, to, q, bytes.NewReader(original.Bytes()))
	} else {
		result, err = a.sessions.UploadOwnerDocument(ctx, session, to, q, a.documents, bytes.NewReader(original.Bytes()), a.admit)
	}
	if err != nil {
		return "", err
	}
	return result.Document.ID, nil
}

// personSideList is what the person still records in their own Book for
// cash claims a business accepted, with the fed charges that match.
func (a *webAPI) personSideList(w http.ResponseWriter, r *http.Request, session string) {
	book := r.URL.Query().Get("book_id")
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if reportAnswer(w, err) {
		return
	}
	var items []ledger.PersonSideItem
	var corrections []ledger.PersonSideCorrection
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		if items, err = o.PersonSideItems(r.Context(), book); err != nil {
			return err
		}
		corrections, err = o.PersonSideCorrections(r.Context(), book)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "items": items, "corrections": corrections})
}

// personSideRecord records the person's side, as the owner of their Book
// confirms: a chosen fed charge, or a new entry for cash.
func (a *webAPI) personSideRecord(w http.ResponseWriter, r *http.Request, session, claim string) {
	var q struct {
		BookID     string `json:"book_id"`
		BankItemID string `json:"bank_item_id,omitempty"`
	}
	if !readReportBody(w, r, &q) {
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if reportAnswer(w, err) {
		return
	}
	var entry string
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		entry, err = o.RecordPersonSide(r.Context(), q.BookID, who, claim, q.BankItemID)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "claim_id": claim, "entry_id": entry})
}

// personSideCorrect records a correction of a cash claim in the person's
// Book, as its owner confirms.
func (a *webAPI) personSideCorrect(w http.ResponseWriter, r *http.Request, session, correction string) {
	var q struct {
		BookID    string `json:"book_id"`
		AccountID string `json:"account_id,omitempty"`
	}
	if !readReportBody(w, r, &q) {
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if reportAnswer(w, err) {
		return
	}
	var entry string
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		entry, err = o.RecordPersonCorrection(r.Context(), q.BookID, who, correction, q.AccountID)
		return err
	})
	if reportAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "correction_id": correction, "entry_id": entry})
}
