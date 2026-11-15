package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/pdftext"
	"github.com/vult-ai/vult/ledger"
)

// standInProver records report proofs as the passkey service does; the
// passkey ceremony itself is the service's own, tested with it. Only the
// credential "good" confirms.
type standInProver struct{ f *reportFixture }

func (p standInProver) BeginBookProof(context.Context, string, string, string, string) (access.PasskeyChallenge, error) {
	return access.PasskeyChallenge{}, access.ErrDenied
}

func (p standInProver) BeginReportProof(ctx context.Context, session, tenant, book, purpose, bound string) (access.PasskeyChallenge, error) {
	token := access.Random()
	_, err := p.f.lt.f.db.Admin.Exec(ctx, `INSERT INTO access.book_deletion_proofs(token_hash,identity_id,epoch,session_hash,tenant_id,book_ref,purpose,bound_to,ceremony)
 SELECT $1,s.identity_id,i.epoch,s.token_hash,$2,$3,$4,$5,'{}' FROM access.sessions s JOIN access.identities i ON i.id=s.identity_id WHERE s.token_hash=$6`,
		access.Hash(token), tenant, book, purpose, bound, access.Hash(session))
	return access.PasskeyChallenge{Token: token, Options: json.RawMessage(`{}`)}, err
}

func (p standInProver) FinishBookProof(ctx context.Context, token, session string, response json.RawMessage) error {
	if string(response) != `"good"` {
		return access.ErrDenied
	}
	tag, err := p.f.lt.f.db.Admin.Exec(ctx, "UPDATE access.book_deletion_proofs SET taken_at=clock_timestamp(),verified_at=clock_timestamp() WHERE token_hash=$1 AND session_hash=$2 AND verified_at IS NULL",
		access.Hash(token), access.Hash(session))
	if err == nil && tag.RowsAffected() != 1 {
		err = errors.New("no such proof")
	}
	return err
}

func newRouteFixture(t *testing.T) (*reportFixture, *http.Server) {
	f := newReportFixture(t)
	s, err := New(configuration(), Options{Database: f.lt.f.db.Runtime, Access: &Access{Sessions: f.lt.f.sessions, HTTP: mustLogin(t, f)},
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}}, Documents: f.lt.f.vault, BookProver: standInProver{f}})
	if err != nil {
		t.Fatal(err)
	}
	return f, s
}

func mustLogin(t *testing.T, f *reportFixture) *access.HTTP {
	login, err := access.NewHTTP(f.lt.f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	return login
}

func reportCall(t *testing.T, s *http.Server, session, method, path string, body any) (*httptest.ResponseRecorder, map[string]json.RawMessage) {
	t.Helper()
	var w *httptest.ResponseRecorder
	if method == http.MethodPost {
		data, _ := json.Marshal(body)
		w = bankCall(t, s, session, method, path, string(data))
	} else {
		w = bankCall(t, s, session, method, path, "")
	}
	var out map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// The routes: only the owner's session, a passkey answer bound to the
// revision, and the approved revision's PDF stored once in the business's
// Documents.
func TestReportRoutesForTheOwnerWithTheirPasskey(t *testing.T) {
	f, s := newRouteFixture(t)
	session := f.lt.f.session
	// Only the business paying Kit back keeps expense reports.
	if w := bankCall(t, s, session, http.MethodGet, booksPath, ""); strings.Count(w.Body.String(), `"expense_reports":true`) != 1 {
		t.Fatal("the Book read doesn't offer expense reports to the business alone", w.Body.String())
	}
	w, out := reportCall(t, s, session, http.MethodPost, expenseReportsPath, map[string]any{"link_id": f.link, "person_id": f.person})
	if w.Code != http.StatusCreated {
		t.Fatal("create", w.Code, w.Body.String())
	}
	var report ledger.ReportDetail
	json.Unmarshal(out["report"], &report)
	ref := report.Reference
	// An agent's token is never a sign-in here.
	r := browserRequest(http.MethodGet, expenseReportsPath+"/"+ref, session)
	r.Header.Set("Authorization", "Bearer synthetic-agent-token")
	rec := httptest.NewRecorder()
	s.Handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatal("agent token accepted", rec.Code)
	}
	if w, _ := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/claims", map[string]any{"pair_key": f.pairKey("EXAMPLE AIR"), "purpose": "Flight"}); w.Code != http.StatusOK {
		t.Fatal("claim", w.Code, w.Body.String())
	}
	w, out = reportCall(t, s, session, http.MethodGet, expenseReportsPath+"/"+ref, nil)
	json.Unmarshal(out["report"], &report)
	// A stale revision is refused before any passkey is asked.
	if w, _ := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/proof", map[string]any{"purpose": "submit_approve", "revision": report.Revision, "snapshot_sha256": strings.Repeat("0", 64)}); w.Code != http.StatusConflict ||
		!strings.Contains(w.Body.String(), "revision_changed") {
		t.Fatal("stale proof", w.Code, w.Body.String())
	}
	w, out = reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/proof", map[string]any{"purpose": "submit_approve", "revision": report.Revision, "snapshot_sha256": report.SnapshotSHA256})
	if w.Code != http.StatusOK {
		t.Fatal("proof", w.Code, w.Body.String())
	}
	var token string
	json.Unmarshal(out["token"], &token)
	submit := map[string]any{"revision": report.Revision, "snapshot_sha256": report.SnapshotSHA256, "attestation": true, "approve": true, "token": token, "credential": "bad"}
	if w, _ := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/submit", submit); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "passkey_required") {
		t.Fatal("submitted without the passkey", w.Code, w.Body.String())
	}
	// The failed answer spent nothing; a fresh challenge and a good answer.
	w, out = reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/proof", map[string]any{"purpose": "submit_approve", "revision": report.Revision, "snapshot_sha256": report.SnapshotSHA256})
	json.Unmarshal(out["token"], &token)
	submit["token"], submit["credential"] = token, "good"
	w, out = reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/submit", submit)
	if w.Code != http.StatusOK || !strings.Contains(string(out["events"]), `"submitted"`) || !strings.Contains(string(out["events"]), `"approved"`) {
		t.Fatal("submit", w.Code, w.Body.String())
	}
	json.Unmarshal(out["report"], &report)
	if report.Workflow != "approved" || len(report.Revisions) != 1 || report.Revisions[0].PDFDocumentID == "" {
		t.Fatal("approved without its document", report.Workflow, report.Revisions)
	}
	pdf := report.Revisions[0].PDFDocumentID
	var sha, group string
	if err := f.lt.f.db.Admin.QueryRow(f.lt.f.db.Context, "SELECT d.sha256,coalesce(g.document_group,'') FROM public.documents d LEFT JOIN public.document_groups g ON g.tenant_id=d.tenant_id AND g.book_id=d.book_id AND g.document_id=d.id WHERE d.id=$1 AND d.book_id=$2",
		pdf, f.business()).Scan(&sha, &group); err != nil || group != "company" {
		t.Fatal("the PDF isn't in the business's company records", group, err)
	}
	var stored string
	if err := f.lt.f.db.Admin.QueryRow(f.lt.f.db.Context, "SELECT pdf_sha256 FROM public.report_revisions WHERE report_id=$1 AND revision=1", report.ID).Scan(&stored); err != nil || stored != sha {
		t.Fatal("the revision doesn't hold its PDF's hash", stored, sha, err)
	}
	// The stored PDF reads back with the reference and says it was
	// self-approved.
	var download strings.Builder
	if err := f.lt.f.sessions.WithOwner(f.lt.f.db.Context, session, f.lt.f.tenant, false, func(o ledger.TenantAdmin) error {
		return o.DownloadDocument(f.lt.f.db.Context, f.business(), pdf, f.lt.f.vault, &download)
	}); err != nil {
		t.Fatal(err)
	}
	pages, err := pdftext.Pages([]byte(download.String()))
	if err != nil || !strings.Contains(strings.Join(pages, " "), ref) || !strings.Contains(strings.Join(pages, " "), "Self-approved") {
		t.Fatal("the PDF", err)
	}
	// A statement is issued as its own dated document.
	w, out = reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/statements", map[string]any{})
	json.Unmarshal(out["report"], &report)
	if w.Code != http.StatusOK || len(report.Statements) != 1 || report.Statements[0].PDFDocumentID == "" || report.Statements[0].PDFDocumentID == pdf {
		t.Fatal("statement", w.Code, w.Body.String())
	}
	// A draft never submitted is deleted; the approved one isn't.
	w, out = reportCall(t, s, session, http.MethodPost, expenseReportsPath, map[string]any{"link_id": f.link, "person_id": f.person})
	var draft ledger.ReportDetail
	json.Unmarshal(out["report"], &draft)
	if w, _ := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+draft.Reference+"/delete", map[string]any{}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deleted"`) {
		t.Fatal("delete", w.Code, w.Body.String())
	}
	if w, _ := reportCall(t, s, session, http.MethodGet, expenseReportsPath+"/"+draft.Reference, nil); w.Code != http.StatusNotFound {
		t.Fatal("the deleted draft still reads", w.Code)
	}
	if w, _ := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/delete", map[string]any{}); w.Code != http.StatusUnprocessableEntity {
		t.Fatal("an approved report was deleted", w.Code, w.Body.String())
	}
	// The list for the link.
	w, out = reportCall(t, s, session, http.MethodGet, expenseReportsPath+"?link_id="+f.link, nil)
	if w.Code != http.StatusOK || !strings.Contains(string(out["reports"]), ref) || !strings.Contains(string(out["reports"]), `"self_approved":true`) {
		t.Fatal("list", w.Code, w.Body.String())
	}
}

// A receipt in the person's Book is copied into the business's Documents:
// that receipt alone, with the same bytes.
func TestAReceiptFromThePersonsBookIsCopiedAlone(t *testing.T) {
	f, s := newRouteFixture(t)
	session := f.lt.f.session
	ref := f.newReport("", false)
	claim := f.claim(ref, "HARBOR HOTEL", "Customer meeting stay")
	kit := f.lt.books["kit"].ID
	upload := decodeOwnerUpload(t, runOwnerUpload(f.lt.s, ownerUploadRequest(session, kit, "kit-receipt", "Harbor receipt.pdf", "application/pdf", []byte("%PDF-1.7\nSynthetic hotel receipt\n"))), http.StatusCreated)
	decodeOwnerUpload(t, runOwnerUpload(f.lt.s, ownerUploadRequest(session, kit, "kit-statement", "Card statement.pdf", "application/pdf", []byte("%PDF-1.7\nSynthetic card statement\n"))), http.StatusCreated)
	before := f.count("SELECT count(*) FROM public.documents WHERE book_id=$1", f.business())
	w, out := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/claims/"+claim+"/evidence", map[string]any{"document_id": upload.Document.ID, "source_book_id": kit})
	_ = out
	if w.Code != http.StatusOK {
		t.Fatal("evidence", w.Code, w.Body.String())
	}
	var copied string
	json.Unmarshal(out["document_id"], &copied)
	if copied == upload.Document.ID || f.count("SELECT count(*) FROM public.documents WHERE book_id=$1", f.business()) != before+1 ||
		f.count("SELECT count(*) FROM public.documents WHERE book_id=$1 AND id=$2 AND sha256=$3", f.business(), copied, upload.Document.SHA256) != 1 {
		t.Fatal("the receipt wasn't copied alone with its bytes")
	}
	// A redacted copy says what it came from and how, and every document
	// is kept until the retention years pass.
	focused := decodeOwnerUpload(t, runOwnerUpload(f.lt.s, ownerUploadRequest(session, kit, "kit-extract", "Card line.pdf", "application/pdf", []byte("%PDF-1.7\nSynthetic card line\n"))), http.StatusCreated)
	if w, _ := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/claims/"+claim+"/evidence", map[string]any{"document_id": focused.Document.ID, "source_book_id": kit,
		"kind": "payment_proof_extract", "redacted_from_document_id": upload.Document.ID}); w.Code == http.StatusOK {
		t.Fatal("a redacted copy without its provenance was accepted")
	}
	if w, _ := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/claims/"+claim+"/evidence", map[string]any{"document_id": focused.Document.ID, "source_book_id": kit,
		"kind": "payment_proof_extract", "redacted_from_document_id": upload.Document.ID, "redaction_provenance": "The hotel's line from the card statement, other lines removed"}); w.Code != http.StatusOK {
		t.Fatal("extract", w.Code, w.Body.String())
	}
	d := f.detail(ref)
	ev := d.Lines[0].Evidence
	if len(ev) != 2 || ev[0].Kind != "receipt" || ev[0].Derived || ev[1].Kind != "payment_proof_extract" || !ev[1].Derived ||
		ev[1].RetentionUntil != time.Now().AddDate(7, 0, 0).Format("2006-01-02") {
		t.Fatal("evidence", ev)
	}
	// A statement extract alone proves payment, not what was bought: the
	// receipt is still missing.
	air := f.claim(ref, "EXAMPLE AIR", "Flight")
	if w, _ := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/claims/"+air+"/evidence", map[string]any{"document_id": focused.Document.ID, "source_book_id": kit,
		"kind": "payment_proof_extract", "redacted_from_document_id": upload.Document.ID, "redaction_provenance": "The flight's line from the card statement"}); w.Code != http.StatusOK {
		t.Fatal("extract alone", w.Code, w.Body.String())
	}
	d = f.detail(ref)
	missing := false
	for _, x := range d.Warnings {
		missing = missing || x.Kind == "receipt_required" && x.ClaimID == air
	}
	if l := d.Lines[1]; l.ClaimID != air || l.HasReceipt || !l.HasPaymentProof || !missing || !d.Lines[0].HasReceipt {
		t.Fatal("an extract counted as a receipt", l.HasReceipt, l.HasPaymentProof, missing)
	}
	// Nothing to record in the business's own Book answers empty.
	if w, out := reportCall(t, s, session, http.MethodGet, expenseReportsPath+"/person-side?book_id="+f.business(), nil); w.Code != http.StatusOK || string(out["items"]) != "[]" {
		t.Fatal("person side", w.Code, w.Body.String())
	}
	// A personal note stays in the person's Book; a shared one is the
	// business's.
	if w, _ := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/notes", map[string]any{"audience": "personal", "text": "Ask about the minibar"}); w.Code != http.StatusOK {
		t.Fatal("personal note", w.Code, w.Body.String())
	}
	w, out = reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/notes", map[string]any{"audience": "shared", "text": "Customer meeting on the 4th"})
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "minibar") || !strings.Contains(w.Body.String(), "Customer meeting on the 4th") {
		t.Fatal("the business's read", w.Code, w.Body.String())
	}
	if w, _ := reportCall(t, s, session, http.MethodGet, expenseReportsPath+"/"+ref+"/personal-notes", nil); !strings.Contains(w.Body.String(), "minibar") {
		t.Fatal("personal notes", w.Body.String())
	}
	if f.count("SELECT count(*) FROM public.report_notes WHERE book_id=$1 AND audience='personal'", kit) != 1 ||
		f.count("SELECT count(*) FROM public.report_notes WHERE book_id=$1 AND audience='personal'", f.business()) != 0 {
		t.Fatal("the personal note isn't kept in the person's Book alone")
	}
	// A document of another Book altogether is refused.
	if w, _ := reportCall(t, s, session, http.MethodPost, expenseReportsPath+"/"+ref+"/claims/"+claim+"/evidence", map[string]any{"document_id": upload.Document.ID, "source_book_id": f.lt.books["studio"].ID}); w.Code == http.StatusOK {
		t.Fatal("a third Book's document was copied")
	}
}
