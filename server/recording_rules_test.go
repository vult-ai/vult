package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestOwnerMakesRulesAndGrantsRecordingWithinThem(t *testing.T) {
	driver, fixture := bankPaths(t, "screen")
	d := driver.(screenDriver)
	f, s, book := d.f, d.s, d.book
	ctx := f.db.Context
	fee := ""
	for _, v := range d.Inbox(t, "").Items {
		if v.Description == "Example bank fee" {
			fee = v.ID
		}
	}
	entry := fixture.Post(t, ledger.PostJournal{RequestKey: "fee", EconomicDate: "2026-03-13", AccountingDate: "2026-03-13", Description: "Bank fee",
		Lines: []ledger.PostLine{{AccountID: fixture.Expense, Side: "debit", AmountMinor: "500"}, {AccountID: fixture.Checking, Side: "credit", AmountMinor: "500"}}})
	if _, err := d.Do(t, "link_bank_activity", ledger.LinkBankActivity{RequestKey: "fee", TransactionID: fee, EntryID: entry.ID}); err != nil {
		t.Fatal(err)
	}
	body := `{"book_id":"` + book + `","transaction_id":"` + fee + `","min_minor":"100","max_minor":"1000","min_days_between":25}`
	if _, err := f.db.Admin.Exec(ctx, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(f.session)); err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, recordingRulesPath, body); w.Code != http.StatusForbidden {
		t.Fatal("a rule was made without a fresh sign-in", w.Code)
	}
	if _, err := f.db.Admin.Exec(ctx, "UPDATE access.sessions SET authenticated_at=clock_timestamp() WHERE token_hash=$1", access.Hash(f.session)); err != nil {
		t.Fatal(err)
	}
	w := bankCall(t, s, f.session, http.MethodPost, recordingRulesPath, body)
	var rule ledger.RecordingRule
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &rule) != nil || rule.Direction != "out" || rule.MaxMinor != "1000" {
		t.Fatal("rule", w.Code, w.Body.String())
	}
	var proposer ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		proposer, err = o.CreateConnection(ctx, book, "Example proposer", "propose")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, recordingRulesPath+"/grant", `{"book_id":"`+book+`","connection_id":"`+proposer.ID+`","on":true}`); w.Code != http.StatusOK {
		t.Fatal("grant", w.Code, w.Body.String())
	}
	w = bankCall(t, s, f.session, http.MethodGet, recordingRulesPath+"?book_id="+book, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"granted":true`) || !strings.Contains(w.Body.String(), rule.ID) {
		t.Fatal("list", w.Code, w.Body.String())
	}
	if w := bankCall(t, s, f.session, http.MethodPost, recordingRulesPath+"/state", `{"book_id":"`+book+`","rule_id":"`+rule.ID+`","on":false}`); w.Code != http.StatusOK {
		t.Fatal("turn off", w.Code, w.Body.String())
	}
}
