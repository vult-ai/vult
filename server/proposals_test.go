package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/operationref"
	"github.com/vult-ai/vult/ledger"
)

func browserProposal(t *testing.T) (*documentServerFixture, *http.Server, ledger.Scope, ledger.OperationProposal) {
	t.Helper()
	f, s := browserServer(t)
	var c ledger.Connection
	if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		c, err = owner.CreateConnection(f.db.Context, f.book.ID, "Example writer", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: f.tenant, BookID: f.book.ID, ConnectionID: c.ID, ActorID: c.ActorID, Access: "write"}
	a, b := f.db.Account(t, scope, "1000", "asset"), f.db.Account(t, scope, "3000", "equity")
	q := ledger.PostJournal{RequestKey: "accepted-post", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example proposed funding", Lines: []ledger.PostLine{{AccountID: a.ID, Side: "debit", AmountMinor: "1250"}, {AccountID: b.ID, Side: "credit", AmountMinor: "1250"}}}
	args, _ := json.Marshal(q)
	var p ledger.OperationProposal
	if json.Unmarshal(f.db.Call(t, scope, "prepare_operation", ledger.PrepareOperation{RequestKey: "prepare", Operation: "post_journal", Arguments: args}), &p) != nil {
		t.Fatal("invalid synthetic proposal")
	}
	return f, s, scope, p
}

func proposalDecisionBody(book string, p ledger.OperationProposal, key string, revision int64) string {
	input := map[string]any{"book_id": book, "plan_id": p.ID, "sha256": p.SHA256, "request_key": key, "expected_decision_revision": revision, "reason": "Example accepted effects"}
	data, _ := json.Marshal(input)
	return string(data)
}

func proposalRequest(session, path, body string) *http.Request {
	r := settingsRequest(session, body)
	r.URL.Path = path
	return r
}

func TestBrowserProposalReadsAreScopedBoundedAndExplicit(t *testing.T) {
	f, s, scope, p := browserProposal(t)
	read := func(path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		if w.Code != want {
			t.Fatalf("read returned %d, want%d: %s", w.Code, want, w.Body.String())
		}
		return w
	}
	path := "/api/v1/agent-owner/plans?book_id=" + f.book.ID + "&limit=1"
	var first ledger.OperationProposalPage
	if json.Unmarshal(read(path, http.StatusOK).Body.Bytes(), &first) != nil || first.Version != 1 || len(first.Plans) != 1 || first.Plans[0].ID != p.ID || first.Plans[0].DependencyStatus != "not_checked" || first.HasMore {
		t.Fatal("queue falsely claims a checked preview")
	}
	var detail ledger.ProposalState
	detailPath := "/api/v1/agent-owner/plans/" + p.ID + "?book_id=" + f.book.ID
	if json.Unmarshal(read(detailPath, http.StatusOK).Body.Bytes(), &detail) != nil || detail.Version != 1 || detail.Status != "approval_required" || detail.Proposal.SHA256 != p.SHA256 || detail.Proposal.Preview.Classes[0].DebitMinor != "1250" {
		t.Fatal("exact detail lost effects or units")
	}
	var q ledger.PostJournal
	if json.Unmarshal(p.Arguments, &q) != nil {
		t.Fatal("invalid retained arguments")
	}
	q.RequestKey = "accepted-second"
	args, _ := json.Marshal(q)
	f.db.Call(t, scope, "prepare_operation", ledger.PrepareOperation{RequestKey: "prepare-second", Operation: "post_journal", Arguments: args})
	if json.Unmarshal(read(path, http.StatusOK).Body.Bytes(), &first) != nil || !first.HasMore || first.NextBefore <= p.Sequence {
		t.Fatal("queue did not advance a stable cursor")
	}
	var second ledger.OperationProposalPage
	if json.Unmarshal(read(path+"&before="+strconv.FormatInt(first.NextBefore, 10), http.StatusOK).Body.Bytes(), &second) != nil || len(second.Plans) != 1 || second.Plans[0].ID != p.ID || second.HasMore {
		t.Fatal("queue duplicated or lost a proposal")
	}
	var waiting ledger.OperationProposalPage
	if json.Unmarshal(read(path+"&status=approval_required", http.StatusOK).Body.Bytes(), &waiting) != nil || waiting.Waiting != 2 || len(waiting.Plans) != 1 || first.Waiting != 2 {
		t.Fatal("queue does not count waiting proposals")
	}
	for _, query := range []string{"&limit=101", "&limit=0", "&before=-1", "&book_id=" + f.book.ID, "&unexpected=x", "&before=9223372036854775808", "&status=committed", "&status=approval_required&status=approval_required"} {
		read("/api/v1/agent-owner/plans?book_id="+f.book.ID+query, http.StatusBadRequest)
	}
	read("/api/v1/agent-owner/plans/"+ledger.ID()+"?book_id="+f.book.ID, http.StatusNotFound)
	read("/api/v1/agent-owner/plans?book_id="+ledger.ID(), http.StatusNotFound)
}

func TestBrowserProposalDecisionRequiresOwnerOriginCSRFAndExactInput(t *testing.T) {
	f, s, scope, p := browserProposal(t)
	body := proposalDecisionBody(f.book.ID, p, "owner-decision", 0)
	for _, tc := range []struct {
		name   string
		change func(*http.Request)
		status int
	}{
		{"missing-session", func(r *http.Request) { r.Header.Del("Cookie") }, http.StatusUnauthorized},
		{"bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer example") }, http.StatusForbidden},
		{"foreign-origin", func(r *http.Request) { r.Header.Set("Origin", "https://other.example.com") }, http.StatusForbidden},
		{"missing-origin", func(r *http.Request) { r.Header.Del("Origin") }, http.StatusForbidden},
		{"missing-csrf", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, http.StatusForbidden},
		{"duplicate-csrf", func(r *http.Request) { r.Header.Add("X-CSRF-Token", access.Hash("csrf:"+f.session)) }, http.StatusForbidden},
		{"wrong-type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusBadRequest},
		{"duplicate-json", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.TrimSuffix(body, "}") + `,"reason":"Changed"}`))
		}, http.StatusBadRequest},
		{"missing-revision", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(body, `"expected_decision_revision":0,`, "", 1)))
		}, http.StatusBadRequest},
		{"executor-connection", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.TrimSuffix(body, "}") + `,"executor_connection_id":"` + scope.ConnectionID + `"}`))
		}, http.StatusBadRequest},
		{"injected-tenant", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.TrimSuffix(body, "}") + `,"tenant_id":"` + f.tenant + `"}`))
		}, http.StatusBadRequest},
		{"foreign-plan", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(body, p.ID, ledger.ID(), 1)))
		}, http.StatusNotFound},
		{"foreign-book", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(body, f.book.ID, ledger.ID(), 1)))
		}, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := proposalRequest(f.session, "/api/v1/agent-owner/approve", body)
			tc.change(r)
			w := httptest.NewRecorder()
			s.Handler.ServeHTTP(w, r)
			if w.Code != tc.status || strings.Contains(w.Body.String(), p.Preview.Description) {
				t.Fatalf("invalid decision returned%d, want%d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	var decisions int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.operation_decisions").Scan(&decisions); err != nil || decisions != 0 {
		t.Fatal("rejected requests saved an owner decision", err)
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, proposalRequest(f.session, "/api/v1/agent-owner/approve", body))
	if w.Code != http.StatusOK {
		t.Fatal("valid decision failed", w.Code, w.Body.String())
	}
	accepted := append([]byte{}, w.Body.Bytes()...)
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, proposalRequest(f.session, "/api/v1/agent-owner/approve", body))
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), accepted) {
		t.Fatal("lost-response retry changed the decision", w.Code)
	}
	var response struct {
		Version  int                      `json:"version"`
		Decision ledger.OperationDecision `json:"decision"`
		Status   string                   `json:"status"`
		Outcome  *ledger.Outcome          `json:"outcome"`
	}
	var entry ledger.Entry
	if json.Unmarshal(accepted, &response) != nil || response.Version != 1 || response.Status != "committed" || response.Outcome == nil || json.Unmarshal(response.Outcome.Result, &entry) != nil || entry.Description != p.Preview.Description {
		t.Fatal("approval response does not carry the posted entry", string(accepted))
	}
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/transactions/"+entry.ID+"?book_id="+f.book.ID, f.session))
	var journal ledger.JournalRead
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &journal) != nil ||
		journal.Entry.ActorID != scope.ActorID || journal.RecordedBy == nil ||
		*journal.RecordedBy != (ledger.JournalRecorder{Kind: "agent", Name: "Example writer", ConnectionID: scope.ConnectionID}) {
		t.Fatal("owner approval changed the preparing agent's journal attribution", w.Code)
	}

	var entries int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.entries").Scan(&entries); err != nil || entries != 1 {
		t.Fatal("browser approval did not post exactly once", entries, err)
	}
}

func TestBrowserProposalStaleAndFreshnessResponsesNameNextStep(t *testing.T) {
	for _, mode := range []string{"stale-period", "stale-proof"} {
		t.Run(mode, func(t *testing.T) {
			f, s, _, p := browserProposal(t)
			code, next, status := "proposal_stale", "prepare_operation", http.StatusConflict
			if mode == "stale-period" {
				if err := f.sessions.WithOwner(f.db.Context, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
					return owner.ClosePeriod(f.db.Context, f.book.ID, "2026-01-01", "2026-01-31", "Example close with review items acknowledged")
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(f.session)); err != nil {
					t.Fatal(err)
				}
				code, next, status = "fresh_authentication_required", "sign_in_and_review", http.StatusForbidden
			}
			w := httptest.NewRecorder()
			s.Handler.ServeHTTP(w, proposalRequest(f.session, "/api/v1/agent-owner/approve", proposalDecisionBody(f.book.ID, p, "decision", 0)))
			var response map[string]string
			if w.Code != status || json.Unmarshal(w.Body.Bytes(), &response) != nil || response["error"] != code || response["next_step"] != next || response["message"] == "" {
				t.Fatal("refusal lacks a concrete next step", w.Code, w.Body.String())
			}
			if mode == "stale-proof" && response["sign_in_href"] != "/auth/login?operation="+operationref.Encode(f.book.ID, p.ID) {
				t.Fatal("fresh sign-in does not return to the refused operation")
			}
		})
	}
}

func TestBrowserProposalRejectionHistoryAndCompletionReadback(t *testing.T) {
	f, s, _, p := browserProposal(t)
	reject := proposalDecisionBody(f.book.ID, p, "reject", 0)
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, proposalRequest(f.session, "/api/v1/agent-owner/reject", reject))
	if w.Code != http.StatusOK {
		t.Fatal("reject failed", w.Code, w.Body.String())
	}
	approve := proposalDecisionBody(f.book.ID, p, "approve", 1)
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, proposalRequest(f.session, "/api/v1/agent-owner/approve", approve))
	if w.Code != http.StatusOK {
		t.Fatal("replacement failed", w.Code, w.Body.String())
	}
	approved := append([]byte{}, w.Body.Bytes()...)
	var history ledger.OperationDecisionPage
	path := "/api/v1/agent-owner/plans/" + p.ID + "/decisions?book_id=" + f.book.ID + "&limit=1"
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &history) != nil || len(history.Decisions) != 1 || history.Decisions[0].Decision != "reject" || !history.HasMore {
		t.Fatal("history was erased or mislabeled", w.Code)
	}
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path+"&after=1", f.session))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &history) != nil || len(history.Decisions) != 1 || history.Decisions[0].Revision != 2 || history.Decisions[0].Decision != "approve" || history.HasMore {
		t.Fatal("history cursor duplicated a decision", w.Code)
	}
	var posted struct {
		Outcome *ledger.Outcome `json:"outcome"`
	}
	if json.Unmarshal(approved, &posted) != nil || posted.Outcome == nil {
		t.Fatal("replacement approval did not post")
	}
	result := posted.Outcome.Result
	var state ledger.ProposalState
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/agent-owner/plans/"+p.ID+"?book_id="+f.book.ID, f.session))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &state) != nil || state.Status != "committed" || state.Outcome == nil || !bytes.Equal(state.Outcome.Result, result) {
		t.Fatal("completed operation outcome missing", w.Code)
	}
}

func TestBrowserProposalQueueValidEmptyAndRevokedOwner(t *testing.T) {
	f, s := browserServer(t)
	path := "/api/v1/agent-owner/plans?book_id=" + f.book.ID
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
	var page ledger.OperationProposalPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Version != 1 || page.Plans == nil || len(page.Plans) != 0 {
		t.Fatal("valid empty queue is not explicit", w.Code)
	}
	if err := f.operator.RevokeIdentity(f.db.Context, access.Identity{Issuer: f.identity.Issuer, Subject: f.identity.Subject, AuthenticatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
	if w.Code != http.StatusUnauthorized {
		t.Fatal("revoked session read proposals", w.Code)
	}
}

func TestBrowserProposalUsesActualForeignBookDenial(t *testing.T) {
	f, s, _, p := browserProposal(t)
	other := access.Identity{Issuer: f.identity.Issuer, Subject: "other", AuthenticatedAt: time.Now().UTC()}
	tenant, err := f.operator.BindOwner(f.db.Context, "", other)
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.sessions.IssueSession(f.db.Context, other)
	if err != nil {
		t.Fatal(err)
	}
	var book ledger.Book
	if err = f.sessions.WithOwner(f.db.Context, session, tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		book, err = owner.CreateBook(f.db.Context, "Other example Book", "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/agent-owner/plans?book_id=" + book.ID, "/api/v1/agent-owner/plans/" + p.ID + "?book_id=" + book.ID, "/api/v1/agent-owner/plans/" + p.ID + "/decisions?book_id=" + book.ID} {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), book.Name) {
			t.Fatal("actual foreign Book leaked", w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, proposalRequest(session, "/api/v1/agent-owner/approve", proposalDecisionBody(f.book.ID, p, "foreign-decision", 0)))
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), p.Preview.Description) {
		t.Fatal("foreign owner changed a proposal", w.Code)
	}
}

func TestBrowserProposalOwnerCanReviewAndApproveStoredSources(t *testing.T) {
	f, s, scope, p := browserProposal(t)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { disk.Close() })
	keyJSON, err := documents.NewKeyFile(f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.ParseKeyring(keyJSON)
	if err != nil {
		t.Fatal(err)
	}
	f.db.Store.Documents = &documents.Vault{Storage: disk, Keys: keys}
	data := []byte("Synthetic original document")
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	if json.Unmarshal(f.db.Call(t, scope, "add_document", ledger.AddDocument{RequestKey: "original", Name: "example.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}), &intake) != nil {
		t.Fatal("invalid intake")
	}
	if _, err = f.db.Store.Upload(f.db.Context, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	var q ledger.PostJournal
	if json.Unmarshal(p.Arguments, &q) != nil {
		t.Fatal("invalid operation")
	}
	q.RequestKey = "accepted-source-post"
	q.Sources = []ledger.SourceInput{{DocumentID: intake.Document.ID}}
	args, _ := json.Marshal(q)
	if json.Unmarshal(f.db.Call(t, scope, "prepare_operation", ledger.PrepareOperation{RequestKey: "prepare-sourced", Operation: "post_journal", Arguments: args}), &p) != nil {
		t.Fatal("invalid sourced proposal")
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/agent-owner/plans/"+p.ID+"?book_id="+f.book.ID, f.session))
	var detail ledger.ProposalState
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &detail) != nil || len(detail.Proposal.Preview.Sources) != 1 || detail.Proposal.Preview.Sources[0].DocumentID != intake.Document.ID || detail.Proposal.Preview.SourceUsage[0].ActiveEntries != 0 {
		t.Fatal("owner could not verify scoped source facts", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, proposalRequest(f.session, "/api/v1/agent-owner/approve", proposalDecisionBody(f.book.ID, p, "source-decision", 0)))
	if w.Code != http.StatusOK {
		t.Fatal("source-backed approval failed", w.Code, w.Body.String())
	}
	var cited int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.source_citations WHERE document_id=$1", intake.Document.ID).Scan(&cited); err != nil || cited != 1 {
		t.Fatal("source-backed approval did not post its citation", cited, err)
	}
}
