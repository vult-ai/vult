package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

// Raising the agents' limit works from a fresh sign-in, and a stale one is
// asked to sign in again, never refused as a permission or server problem.
func TestOwnerSavesTheAgentsLimit(t *testing.T) {
	f, s, _ := bankServer(t)
	// Raising the limit from the default 500.00 gives an agent more.
	higher := `{"book_id":"` + f.book.ID + `","amount_cap_minor":"90000","expected_revision":0}`
	body := `{"book_id":"` + f.book.ID + `","amount_cap_minor":"5000","expected_revision":0}`
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '1 hour' WHERE token_hash=$1", access.Hash(f.session)); err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, recordingRulesPath+"/policy", higher); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "fresh_authentication_required") {
		t.Fatal("a stale sign-in wasn't asked to sign in again", w.Code, w.Body.String())
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp() WHERE token_hash=$1", access.Hash(f.session)); err != nil {
		t.Fatal(err)
	}
	w := bankCall(t, s, f.session, http.MethodPost, recordingRulesPath+"/policy", body)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"amount_cap_minor":"5000"`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

// On a sign-in that is no longer recent, the owner can still narrow what an
// agent does, as they can disconnect it: by rules instead of when sure,
// drafts only, a rule turned off, a lower limit. Giving it more asks them to
// sign in again.
func TestAnyValidSignInNarrowsAnAgent(t *testing.T) {
	d, fixture := bankPaths(t, "mcp")
	f := d.(mcpDriver).f
	s := d.(mcpDriver).s
	ctx, book := f.db.Context, f.book.ID
	var agent ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		agent, err = o.CreateConnection(ctx, book, "Example agent", "propose")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var item ledger.BankTransaction
	for _, x := range d.Inbox(t, "unrecorded").Items {
		if x.AmountMinor[0] == '-' && item.ID == "" {
			item = x
		}
	}
	if _, err := d.Do(t, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "rec", TransactionID: item.ID, Description: "Supplies", OtherAccountID: fixture.Expense}); err != nil {
		t.Fatal(err)
	}
	var rule ledger.RecordingRule
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		if rule, err = o.CreateRecordingRule(ctx, book, who, ledger.RuleFromActivity{TransactionID: item.ID}); err != nil {
			return err
		}
		if err = o.SetRecordingMode(ctx, book, who, agent.ID, ledger.RecordWhenSure); err != nil {
			return err
		}
		_, err = o.SetAgentCap(ctx, book, who, "5000", 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '1 hour' WHERE token_hash=$1", access.Hash(f.session)); err != nil {
		t.Fatal(err)
	}
	call := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return bankCall(t, s, f.session, http.MethodPost, recordingRulesPath+path, body)
	}
	ok := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatal(name, w.Code, w.Body.String())
		}
	}
	signIn := func(name string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "fresh_authentication_required") {
			t.Fatal(name, w.Code, w.Body.String())
		}
	}
	grant := func(mode string, on bool) string {
		return `{"book_id":"` + book + `","connection_id":"` + agent.ID + `","mode":"` + mode + `","on":` + map[bool]string{true: "true", false: "false"}[on] + `}`
	}
	ok("by rules", call("/grant", grant("rules", true)))
	signIn("when sure again", call("/grant", grant("confident", true)))
	ok("drafts only", call("/grant", grant("", false)))
	signIn("by rules again", call("/grant", grant("rules", true)))
	ok("rule off", call("/state", `{"book_id":"`+book+`","rule_id":"`+rule.ID+`","on":false}`))
	signIn("rule on", call("/state", `{"book_id":"`+book+`","rule_id":"`+rule.ID+`","on":true}`))
	ok("lower limit", call("/policy", `{"book_id":"`+book+`","amount_cap_minor":"4000","expected_revision":1}`))
	signIn("higher limit", call("/policy", `{"book_id":"`+book+`","amount_cap_minor":"9000","expected_revision":2}`))
}
