package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestMCPCurrencyQuoteAndZeroNativeJournal(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "local"
		if remote {
			name = "remote"
		}
		t.Run(name, func(t *testing.T) {
			db := testutil.New(t)
			_, scope := db.Book(t, "Example currency Book")
			call := func(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
				return db.Store.Call(ctx, scope, method, input)
			}
			makeServer := New
			if remote {
				makeServer = NewRemote
			}
			st, ct := mcp.NewInMemoryTransports()
			server, err := makeServer(call, "write").Connect(db.Context, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { server.Close() })
			client, err := mcp.NewClient(&mcp.Implementation{Name: "currency-client", Version: "1"}, nil).Connect(db.Context, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { client.Close() })
			scale := 0
			var cash, other ledger.Account
			if json.Unmarshal(invoke(t, db.Context, client, "create_account", ledger.CreateAccount{RequestKey: "cash", Class: "asset", Code: "1000", Name: "Example yen account", Currency: "JPY", Scale: &scale, Role: "cash", NativeOrigin: "empty_ledger"}), &cash) != nil {
				t.Fatal("invalid cash account")
			}
			if json.Unmarshal(invoke(t, db.Context, client, "create_account", ledger.CreateAccount{RequestKey: "capital", Class: "equity", Code: "3000", Name: "Example capital"}), &other) != nil {
				t.Fatal("invalid capital")
			}
			input := ledger.CurrencyAmount{Currency: "JPY", Scale: &scale, AmountMinor: "1"}
			q := ledger.PostJournal{RequestKey: "tiny-native", EconomicDate: "2026-01-02", AccountingDate: "2026-01-02", Description: "Example acquisition", Lines: []ledger.PostLine{
				{AccountID: cash.ID, Side: "debit", AmountMinor: "0", Native: &ledger.NativeMovement{Currency: "JPY", Scale: &scale, AmountMinor: "1"}, Valuation: &ledger.Valuation{Basis: "applied_quote", Policy: "quoted-rate-half-even-v1", Input: &input,
					Rate: &ledger.AppliedRate{Base: "JPY", Quote: "USD", Rate: "0.001", RequestedDate: "2026-01-02", ObservationDate: "2026-01-02", Provenance: "Hypothetical quote"}}},
				{AccountID: other.ID, Side: "credit", AmountMinor: "0"},
			}}
			first := invoke(t, db.Context, client, "post_journal", q)
			if !bytes.Equal(first, invoke(t, db.Context, client, "post_journal", q)) {
				t.Fatal("MCP retry changed valuation")
			}
			var b ledger.Balance
			if json.Unmarshal(invoke(t, db.Context, client, "get_balance", ledger.AccountQuery{AccountID: cash.ID}), &b) != nil || b.Native == nil || b.Native.Status != "complete" || b.Native.DebitPositiveMinor != "1" || b.DebitPositiveMinor != "0" {
				t.Fatal("MCP mixed native and Book amounts", b)
			}
			var e ledger.Entry
			json.Unmarshal(first, &e)
			var reversal ledger.Entry
			if json.Unmarshal(invoke(t, db.Context, client, "reverse_journal", ledger.ReverseJournal{RequestKey: "reverse", EntryID: e.ID, AccountingDate: "2026-01-03", Reason: "Example correction"}), &reversal) != nil || !bytes.Equal(valueJSON(reversal.Lines[0].Valuation), valueJSON(e.Lines[0].Valuation)) {
				t.Fatal("MCP reversal recalculated valuation")
			}
		})
	}
}
func valueJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
