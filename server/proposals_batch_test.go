package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

// The owner approves routine proposals together with one fresh sign-in,
// including bank activity a proposing agent prepared; each posts exactly
// what was reviewed, once.
func TestOwnerApprovesRoutineProposalsTogether(t *testing.T) {
	f, s, banks := bankServer(t)
	ctx, book := f.db.Context, f.book.ID
	call := func(path, body string) string {
		t.Helper()
		w := bankCall(t, s, f.session, http.MethodPost, path, body)
		if w.Code != http.StatusOK {
			t.Fatal(path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	var linked struct {
		ConnectionID string `json:"connection_id"`
	}
	json.Unmarshal([]byte(call(bankLinkFinishPath, `{"book_id":"`+book+`","public_token":"public-example"}`)), &linked)
	if _, err := banks.Sync(ctx, ledger.BankSyncTarget{TenantID: f.tenant, BookID: book, ConnectionID: linked.ConnectionID}); err != nil {
		t.Fatal(err)
	}
	connections := map[string]ledger.Connection{}
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		for _, level := range []string{"write", "propose"} {
			c, err := o.CreateConnection(ctx, book, "Example "+level, level)
			if err != nil {
				return err
			}
			connections[level] = c
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	scope := func(level string) ledger.Scope {
		c := connections[level]
		return ledger.Scope{TenantID: f.tenant, BookID: book, ConnectionID: c.ID, ActorID: c.ActorID, Access: level}
	}
	checking := f.db.Account(t, scope("write"), "1000", "asset")
	supplies := f.db.Account(t, scope("write"), "6100", "expense")
	equity := f.db.Account(t, scope("write"), "3000", "equity")
	var overview bankOverview
	json.Unmarshal([]byte(bankCall(t, s, f.session, http.MethodGet, bankPath+"?book_id="+book, "").Body.String()), &overview)
	call(bankAccountLinkPath, `{"book_id":"`+book+`","bank_account_id":"`+overview.Connections[0].Accounts[0].ID+`","account_id":"`+checking.ID+`"}`)
	var activity struct {
		Page ledger.BankActivityPage `json:"page"`
	}
	json.Unmarshal([]byte(bankCall(t, s, f.session, http.MethodGet, bankActivityPath+"?book_id="+book, "").Body.String()), &activity)
	prepare := func(key, operation string, args any) ledger.OperationProposal {
		t.Helper()
		data, _ := json.Marshal(args)
		var p ledger.OperationProposal
		if json.Unmarshal(f.db.Call(t, scope("propose"), "prepare_operation", ledger.PrepareOperation{RequestKey: key, Operation: operation, Arguments: data}), &p) != nil || p.ID == "" {
			t.Fatal("invalid proposal")
		}
		return p
	}
	proposals := []ledger.OperationProposal{
		prepare("bank", "record_bank_activity", ledger.RecordBankActivity{RequestKey: "record-bank", TransactionID: activity.Page.Items[0].ID, Description: "Office supplies",
			Lines: []ledger.PostLine{{AccountID: supplies.ID, Side: "debit", AmountMinor: "4550"}, {AccountID: checking.ID, Side: "credit", AmountMinor: "4550"}}}),
		prepare("fund", "post_journal", ledger.PostJournal{RequestKey: "fund", EconomicDate: "2026-03-01", AccountingDate: "2026-03-01", Description: "Example funding",
			Lines: []ledger.PostLine{{AccountID: checking.ID, Side: "debit", AmountMinor: "100000"}, {AccountID: equity.ID, Side: "credit", AmountMinor: "100000"}}}),
	}
	items := []map[string]any{}
	for _, p := range proposals {
		items = append(items, map[string]any{"plan_id": p.ID, "sha256": p.SHA256, "expected_decision_revision": 0})
	}
	body, _ := json.Marshal(map[string]any{"book_id": book, "request_key": "batch-1", "items": items})
	// A sign-in older than five minutes approves nothing.
	if _, err := f.db.Admin.Exec(ctx, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(f.session)); err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, approveBatchPath, string(body)); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "fresh_authentication_required") {
		t.Fatal("approved without a fresh sign-in", w.Code, w.Body.String())
	}
	if _, err := f.db.Admin.Exec(ctx, "UPDATE access.sessions SET authenticated_at=clock_timestamp() WHERE token_hash=$1", access.Hash(f.session)); err != nil {
		t.Fatal(err)
	}
	first := call(approveBatchPath, string(body))
	if strings.Count(first, `"approved"`) != 2 {
		t.Fatal("batch", first)
	}
	// A retried batch recovers its decisions instead of posting again.
	if again := call(approveBatchPath, string(body)); strings.Count(again, `"approved"`) != 2 {
		t.Fatal("retried batch", again)
	}
	var entries int
	if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.entries WHERE book_id=$1", book).Scan(&entries); err != nil || entries != 2 {
		t.Fatal("entries posted", entries, err)
	}
	json.Unmarshal([]byte(bankCall(t, s, f.session, http.MethodGet, bankActivityPath+"?book_id="+book, "").Body.String()), &activity)
	if activity.Page.Summary.Unrecorded != 0 {
		t.Fatal("the approved bank proposal didn't record the activity")
	}
}
