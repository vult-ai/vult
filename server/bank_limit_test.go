package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestAVultAtItsBankLimitIsToldBeforeTheProvidersScreenOpens(t *testing.T) {
	f, s, _ := bankServerWith(t, func(o *Options) {
		o.BankLimit = &BankLimitSetting{Connections: 1, Title: "You've hit this plan's limit", Note: "A larger plan connects the rest of your banks.",
			MoreLabel: "See plans", MoreURL: "https://plans.example.com"}
	})
	book := f.book.ID
	overview := func() bankOverview {
		t.Helper()
		var out bankOverview
		w := bankCall(t, s, f.session, http.MethodGet, bankPath+"?book_id="+book, "")
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("overview", w.Code, w.Body.String())
		}
		return out
	}
	// With room, the overview says how much and the screen opens.
	if l := overview().Limit; l == nil || l.Max != 1 || l.Used != 0 || l.More == nil || l.More.Href != "https://plans.example.com" {
		t.Fatalf("limit before any bank: %+v", l)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`"}`); w.Code != http.StatusOK {
		t.Fatal("link start with room", w.Code, w.Body.String())
	}
	w := bankCall(t, s, f.session, http.MethodPost, bankLinkFinishPath, `{"book_id":"`+book+`","public_token":"public-example"}`)
	var linked struct {
		ConnectionID string `json:"connection_id"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &linked) != nil || !ledger.ValidID(linked.ConnectionID) {
		t.Fatal("link finish", w.Code, w.Body.String())
	}
	// At the limit, the overview says so and a new login is refused in the
	// server's own words, with nothing sent to the provider.
	if l := overview().Limit; l == nil || l.Used != 1 || l.Title != "You've hit this plan's limit" {
		t.Fatalf("limit with one bank: %+v", l)
	}
	w = bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"error":"refused"`) || !strings.Contains(w.Body.String(), "A larger plan connects the rest of your banks.") {
		t.Fatal("a second bank at the limit", w.Code, w.Body.String())
	}
	// Signing in again to the bank already connected is never refused for room.
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`","connection_id":"`+linked.ConnectionID+`"}`); strings.Contains(w.Body.String(), "larger plan") {
		t.Fatal("signing in again was refused for room", w.Code, w.Body.String())
	}
}

func TestABankLimitSettingIsChecked(t *testing.T) {
	for name, l := range map[string]BankLimitSetting{
		"none":            {},
		"too many":        {Connections: 101},
		"label alone":     {Connections: 2, MoreLabel: "See plans"},
		"not https":       {Connections: 2, MoreLabel: "See plans", MoreURL: "http://plans.example.com"},
		"a long sentence": {Connections: 2, Note: strings.Repeat("a", 161)},
	} {
		if l.validate() == nil {
			t.Error("accepted:", name)
		}
	}
	ok := BankLimitSetting{Connections: 2}
	if ok.validate() != nil || (&BankLimit{Max: 2}).reason() != "This Vult connects 2 banks." || (&BankLimit{Max: 1}).reason() != "This Vult connects one bank." {
		t.Error("a plain limit")
	}
}
