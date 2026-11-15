package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/bankconnect/banktest"
	"github.com/vult-ai/vult/ledger"
)

// The shared bank cases run through the owner's screen and through an
// agent over MCP. Both must take every action the same way.
func TestBankActionsTakeTheSameWayOnScreenAndOverMCP(t *testing.T) {
	for _, path := range []string{"screen", "mcp"} {
		t.Run(path, func(t *testing.T) {
			banktest.Run(t, func(t *testing.T) (banktest.Driver, banktest.Fixture) { return bankPaths(t, path) })
		})
	}
}

type screenDriver struct {
	t    *testing.T
	s    *http.Server
	f    *documentServerFixture
	book string
}

type mcpDriver struct {
	screenDriver
	client *mcp.ClientSession
}

func (d screenDriver) Name() string { return "screen" }
func (d mcpDriver) Name() string    { return "mcp" }

func (d screenDriver) activity(t *testing.T, status string) (out struct {
	Page        ledger.BankActivityPage `json:"page"`
	Suggestions []ledger.BankSuggestion `json:"suggestions"`
}) {
	path := bankActivityPath + "?book_id=" + d.book
	if status != "" {
		path += "&status=" + status
	}
	w := bankCall(t, d.s, d.f.session, http.MethodGet, path, "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("activity", w.Code, w.Body.String())
	}
	return out
}

func (d screenDriver) Inbox(t *testing.T, status string) ledger.BankActivityPage {
	return d.activity(t, status).Page
}
func (d screenDriver) Suggestions(t *testing.T) []ledger.BankSuggestion {
	return d.activity(t, "").Suggestions
}

func (d screenDriver) Do(t *testing.T, operation string, args any) (json.RawMessage, error) {
	var path, body string
	switch v := args.(type) {
	case ledger.PauseBankConnection:
		path, body = bankPausePath, `{"book_id":"`+d.book+`","connection_id":"`+v.ConnectionID+`","reason":"`+v.Reason+`"}`
	case ledger.ResumeBankConnection:
		path, body = bankResumePath, `{"book_id":"`+d.book+`","connection_id":"`+v.ConnectionID+`"}`
	default:
		data, _ := json.Marshal(map[string]any{"book_id": d.book, "operation": operation, "arguments": args})
		path, body = accountChangesPath, string(data)
	}
	w := bankCall(t, d.s, d.f.session, http.MethodPost, path, body)
	if w.Code != http.StatusOK {
		return nil, errors.New(w.Body.String())
	}
	return w.Body.Bytes(), nil
}

func (d screenDriver) Origin(t *testing.T, entry string) ledger.EntryOrigin {
	var out ledger.EntryOrigin
	w := bankCall(t, d.s, d.f.session, http.MethodGet, entryOriginPath+"?book_id="+d.book+"&entry_id="+entry, "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("origin", w.Code, w.Body.String())
	}
	return out
}

// call goes through the MCP server over HTTP, as an agent's app does.
func (d mcpDriver) call(t *testing.T, method string, args any) (json.RawMessage, error) {
	result, err := d.client.CallTool(d.f.db.Context, &mcp.CallToolParams{Name: method, Arguments: args})
	if err != nil {
		return nil, err
	}
	text := ""
	if len(result.Content) > 0 {
		if c, ok := result.Content[0].(*mcp.TextContent); ok {
			text = c.Text
		}
	}
	if result.IsError {
		return nil, errors.New(text)
	}
	return json.RawMessage(text), nil
}

func (d mcpDriver) Inbox(t *testing.T, status string) ledger.BankActivityPage {
	var out ledger.BankActivityPage
	data, err := d.call(t, "list_bank_activity", ledger.BankActivityQuery{Status: status})
	if err != nil || json.Unmarshal(data, &out) != nil {
		t.Fatal("list_bank_activity", err)
	}
	return out
}

func (d mcpDriver) Suggestions(t *testing.T) []ledger.BankSuggestion {
	var out []ledger.BankSuggestion
	data, err := d.call(t, "suggest_bank_matches", struct{}{})
	if err != nil || json.Unmarshal(data, &out) != nil {
		t.Fatal("suggest_bank_matches", err)
	}
	return out
}

// Do takes the action as an agent: directly, or for the owner's decisions,
// by preparing it and having the owner approve it.
func (d mcpDriver) Do(t *testing.T, operation string, args any) (json.RawMessage, error) {
	if !ledger.BankOwnerDecision(operation) {
		return d.call(t, operation, args)
	}
	data, _ := json.Marshal(args)
	out, err := d.call(t, "prepare_operation", ledger.PrepareOperation{RequestKey: "prepare-" + ledger.ID(), Operation: operation, Arguments: data})
	if err != nil {
		return nil, err
	}
	var p ledger.OperationProposal
	if json.Unmarshal(out, &p) != nil {
		t.Fatal("invalid proposal")
	}
	w := bankCall(t, d.s, d.f.session, http.MethodPost, "/api/v1/agent-owner/approve", proposalDecisionBody(d.book, p, "approve-"+p.ID, 0))
	if w.Code != http.StatusOK {
		return nil, errors.New(w.Body.String())
	}
	return w.Body.Bytes(), nil
}

func (d mcpDriver) Origin(t *testing.T, entry string) ledger.EntryOrigin {
	var out ledger.EntryOrigin
	data, err := d.call(t, "get_entry_origin", ledger.EntryQuery{EntryID: entry})
	if err != nil || json.Unmarshal(data, &out) != nil {
		t.Fatal("get_entry_origin", err)
	}
	return out
}

func bankPaths(t *testing.T, path string) (banktest.Driver, banktest.Fixture) {
	f, s, _ := bankServer(t)
	ctx, book := f.db.Context, f.book.ID
	var writer ledger.Connection
	if err := f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(o ledger.TenantAdmin) error {
		var err error
		writer, err = o.CreateConnection(ctx, book, "Example agent", "write")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	agent := ledger.Scope{TenantID: f.tenant, BookID: book, ConnectionID: writer.ID, ActorID: writer.ActorID, Access: "write"}
	checking, card, expense := f.db.Account(t, agent, "1000", "asset"), f.db.Account(t, agent, "2100", "liability"), f.db.Account(t, agent, "6100", "expense")
	var login string
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		login, err = o.AddBankConnection(ctx, book, who, ledger.NewBankConnection{Provider: "mercury", Environment: "sandbox", ProviderItemID: "item-" + ledger.ID(),
			TokenSealed: make([]byte, 48), KeyVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	target := ledger.BankSyncTarget{TenantID: f.tenant, BookID: book, ConnectionID: login}
	if _, err := f.db.Store.ApplyBankSync(ctx, target, banktest.Activity(f.book.Currency, f.book.Scale)); err != nil {
		t.Fatal(err)
	}
	var views []ledger.BankConnectionView
	_ = f.sessions.WithOwner(ctx, f.session, f.tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		views, err = o.BankConnections(ctx, book)
		return err
	})
	for _, a := range views[0].Accounts {
		account := checking.ID
		if a.Kind == "card" {
			account = card.ID
		}
		if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
			return o.LinkBankAccount(ctx, book, who, a.ID, account)
		}); err != nil {
			t.Fatal(err)
		}
	}
	fixture := banktest.Fixture{Connection: login, Checking: checking.ID, Card: card.ID, Expense: expense.ID,
		Post: func(t *testing.T, q ledger.PostJournal) ledger.Entry {
			var e ledger.Entry
			if json.Unmarshal(f.db.Call(t, agent, "post_journal", q), &e) != nil {
				t.Fatal("post")
			}
			return e
		}}
	screen := screenDriver{t: t, s: s, f: f, book: book}
	if path == "mcp" {
		return mcpDriver{screenDriver: screen, client: f.client(t, f.token(t, "write"))}, fixture
	}
	return screen, fixture
}
