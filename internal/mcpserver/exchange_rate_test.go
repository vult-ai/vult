package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/internal/fxrates"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// A rate from get_exchange_rate, passed unchanged, values a foreign posting
// exactly; a Book amount that disagrees with it is refused.
func TestExchangeRateValuesAForeignPostingExactly(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("base") != "EUR" || q.Get("quotes") != "USD" || q.Get("date") != "2026-03-02" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `[{"date":"2026-03-02","base":"EUR","quote":"USD","rate":1.1205,"providers":[{"key":"A","date":"2026-03-02","rate":1.1201}]}]`)
	}))
	defer provider.Close()
	saved := rates
	rates = fxrates.Client{HTTP: provider.Client(), BaseURL: provider.URL, Now: func() time.Time { return time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC) }}
	defer func() { rates = saved }()

	db := testutil.New(t)
	_, scope := db.Book(t, "Example dollar Book")
	call := func(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
		return db.Store.Call(ctx, scope, method, input)
	}
	st, ct := mcp.NewInMemoryTransports()
	server, err := New(call, "write").Connect(db.Context, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "rate-client", Version: "1"}, nil).Connect(db.Context, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var rate ledger.AppliedRate
	if json.Unmarshal(invoke(t, db.Context, client, "get_exchange_rate", map[string]string{"base": "EUR", "quote": "USD", "date": "2026-03-02"}), &rate) != nil ||
		rate.Rate != "1.1205" || rate.ObservationDate != "2026-03-02" || rate.Provenance == "" {
		t.Fatalf("unexpected rate %+v", rate)
	}
	two := 2
	var cash, capital ledger.Account
	if json.Unmarshal(invoke(t, db.Context, client, "create_account", ledger.CreateAccount{RequestKey: "eur", Class: "asset", Code: "1010", Name: "Example euro account", Currency: "EUR", Scale: &two, Role: "cash", NativeOrigin: "empty_ledger"}), &cash) != nil {
		t.Fatal("invalid euro account")
	}
	if json.Unmarshal(invoke(t, db.Context, client, "create_account", ledger.CreateAccount{RequestKey: "capital", Class: "equity", Code: "3000", Name: "Example capital"}), &capital) != nil {
		t.Fatal("invalid capital account")
	}
	journal := func(key, book string) ledger.PostJournal {
		input := ledger.CurrencyAmount{Currency: "EUR", Scale: &two, AmountMinor: "10000"}
		return ledger.PostJournal{RequestKey: key, EconomicDate: "2026-03-02", AccountingDate: "2026-03-02", Description: "Example euro funding", Lines: []ledger.PostLine{
			{AccountID: cash.ID, Side: "debit", AmountMinor: book, Native: &ledger.NativeMovement{Currency: "EUR", Scale: &two, AmountMinor: "10000"},
				Valuation: &ledger.Valuation{Basis: "applied_quote", Policy: "quoted-rate-half-even-v1", Input: &input, Rate: &rate}},
			{AccountID: capital.ID, Side: "credit", AmountMinor: book},
		}}
	}
	// 100.00 EUR at 1.1205 is exactly 112.05 USD.
	if r, err := client.CallTool(db.Context, &mcp.CallToolParams{Name: "post_journal", Arguments: journal("wrong", "11206")}); err == nil && !r.IsError {
		t.Fatal("a Book amount that disagrees with the applied rate was accepted")
	}
	first := invoke(t, db.Context, client, "post_journal", journal("funding", "11205"))
	if !bytes.Equal(first, invoke(t, db.Context, client, "post_journal", journal("funding", "11205"))) {
		t.Fatal("retry changed the posting")
	}
	var b ledger.Balance
	if json.Unmarshal(invoke(t, db.Context, client, "get_balance", ledger.AccountQuery{AccountID: cash.ID}), &b) != nil || b.Native == nil ||
		b.Native.DebitPositiveMinor != "10000" || b.DebitPositiveMinor != "11205" {
		t.Fatalf("euro account position %+v", b)
	}
	// Codes a Book cannot record have no rate, even if a provider quotes them.
	if r, err := client.CallTool(db.Context, &mcp.CallToolParams{Name: "get_exchange_rate", Arguments: map[string]string{"base": "XAU", "quote": "USD", "date": "2026-03-02"}}); err == nil && !r.IsError {
		t.Fatal("a metal code returned a rate")
	}
}
