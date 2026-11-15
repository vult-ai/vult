package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

// A CSV statement the owner uploaded reaches the inbox without an agent:
// its lines join the same transactions the bank reported, and the rest are
// added. A PDF waits for an agent, and the refusal says so.
func TestUploadedCSVStatementsReachTheInboxWithoutAnAgent(t *testing.T) {
	var arrived []ledger.AgentTarget
	var asOwner []error
	f, s, _ := bankServerWith(t, func(o *Options) {
		o.ActivityArrived = func(ctx context.Context, t ledger.AgentTarget, owner OwnerAct) {
			arrived = append(arrived, t)
			// The callback can act as the owner who imported the statement.
			asOwner = append(asOwner, owner(func(o ledger.TenantAdmin, actor string) error {
				if actor == "" {
					return ledger.ErrDenied
				}
				if _, err := o.AgentSettings(ctx, t.BookID); err != nil && !errors.Is(err, ledger.ErrNotFound) {
					return err
				}
				return nil
			}))
		}
	})
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
	var checking, expense ledger.Account
	json.Unmarshal(f.db.Call(t, agent, "create_account", ledger.CreateAccount{RequestKey: "checking", Class: "asset", Code: "1000", Name: "Example checking", Role: "cash"}), &checking)
	json.Unmarshal(f.db.Call(t, agent, "create_account", ledger.CreateAccount{RequestKey: "expense", Class: "expense", Code: "6100", Name: "Example expense"}), &expense)
	// Money already in the Book before the file starts.
	f.db.Call(t, agent, "post_journal", ledger.PostJournal{RequestKey: "opening", EconomicDate: "2026-02-28", AccountingDate: "2026-02-28", Description: "Opening",
		Lines: []ledger.PostLine{{AccountID: checking.ID, Side: "debit", AmountMinor: "50000"}, {AccountID: expense.ID, Side: "credit", AmountMinor: "50000"}}})
	if _, err := f.db.Admin.Exec(ctx, "UPDATE access.sessions SET authenticated_at=clock_timestamp() WHERE token_hash=$1", access.Hash(f.session)); err != nil {
		t.Fatal(err)
	}
	upload := func(name, media string, data string) string {
		t.Helper()
		w := runOwnerUpload(s, ownerUploadRequest(f.session, book, "upload-"+name, name, media, []byte(data)))
		return decodeOwnerUpload(t, w, http.StatusCreated).Document.ID
	}
	csvFile := upload("march.csv", "text/csv", "Date,Description,Amount\n03/02/2026,EXAMPLE CAFE 0042,-18.50\n03/05/2026,EXAMPLE CLIENT CO,\"1,200.00\"\n")
	pdf := upload("march.pdf", "application/pdf", "%PDF-1.7\nSynthetic statement\n")
	add := func(doc, account string) *http.Response {
		w := bankCall(t, s, f.session, http.MethodPost, statementFilesPath, `{"book_id":"`+book+`","document_id":"`+doc+`","account_id":"`+account+`"}`)
		return w.Result()
	}
	if r := add(pdf, checking.ID); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatal("a PDF was read without an agent", r.StatusCode)
	}
	if r := add(csvFile, expense.ID); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatal("a statement went to an expense account", r.StatusCode)
	}
	w := bankCall(t, s, f.session, http.MethodPost, statementFilesPath, `{"book_id":"`+book+`","document_id":"`+csvFile+`","account_id":"`+checking.ID+`"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"lines":2`) || !strings.Contains(w.Body.String(), `"balances":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// Refused files never woke the agent; the imported one did, for its Book.
	if len(arrived) != 1 || arrived[0] != (ledger.AgentTarget{TenantID: f.tenant, BookID: book}) || asOwner[0] != nil {
		t.Fatal("the agent is told when a statement's activity arrives, and can act as its owner", arrived, asOwner)
	}
	var page ledger.BankActivityPage
	json.Unmarshal(f.db.Call(t, agent, "list_bank_activity", ledger.BankActivityQuery{Status: "all"}), &page)
	if len(page.Items) != 2 || page.Items[0].Source != "statement" || page.Items[0].AccountID != checking.ID {
		t.Fatalf("inbox = %+v", page.Items)
	}
	// Without balances in the file, the statement starts from the Book's own
	// balance and says so.
	var statements struct {
		Statements []ledger.Statement `json:"statements"`
	}
	json.Unmarshal(f.db.Call(t, agent, "list_statements", map[string]string{}), &statements)
	if len(statements.Statements) != 1 || statements.Statements[0].OpeningMinor != "50000" || statements.Statements[0].ClosingMinor != "168150" || statements.Statements[0].EvidenceState != "incomplete" {
		t.Fatalf("statements = %+v", statements.Statements)
	}
	// Adding it again adds nothing.
	if r := add(csvFile, checking.ID); r.StatusCode != http.StatusOK {
		t.Fatal("a repeat failed", r.StatusCode)
	}
	json.Unmarshal(f.db.Call(t, agent, "list_bank_activity", ledger.BankActivityQuery{Status: "all"}), &page)
	if len(page.Items) != 2 {
		t.Fatalf("a repeat doubled the inbox: %d", len(page.Items))
	}
}
