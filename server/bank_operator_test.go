package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/bankconnect"
	"github.com/vult-ai/vult/bankconnect/banktest"
	"github.com/vult-ai/vult/internal/secretbox"
	"github.com/vult-ai/vult/ledger"
)

type exampleOperator struct {
	finished          BankLinkFinish
	synced            int
	store             ledger.Store
	ended, paused     []string
	reconnectStartFor string
}

func (o *exampleOperator) StartLink(_ context.Context, _ ledger.TenantAdmin, _, _, _, connection string) (bankconnect.LinkStart, error) {
	o.reconnectStartFor = connection
	return bankconnect.LinkStart{Provider: screenKey, Token: "link-operator-example"}, nil
}

func (o *exampleOperator) FinishLink(ctx context.Context, admin ledger.TenantAdmin, _, book, actor string, q BankLinkFinish) (BankLinkResult, error) {
	o.finished = q
	if q.InstitutionID == "ins_limit" {
		return BankLinkResult{}, &ledger.RefusedError{Reason: "This plan connects one bank."}
	}
	if q.ConnectionID != "" {
		t := ledger.BankSyncTarget{TenantID: admin2tenant(ctx, admin, book), BookID: book, ConnectionID: q.ConnectionID}
		return BankLinkResult{ConnectionID: q.ConnectionID}, o.store.RestartBankConnection(ctx, t, "item-operator-2")
	}
	id, err := admin.AddBankConnection(ctx, book, actor, ledger.NewBankConnection{Provider: screenKey, Environment: "production", ProviderItemID: "item-operator", OperatorHeld: true})
	return BankLinkResult{ConnectionID: id, Notice: "Vult is using your existing connection here.", Reused: true}, err
}

func (o *exampleOperator) Disconnect(_ context.Context, _, _, connection string) error {
	o.ended = append(o.ended, connection)
	return nil
}

func (o *exampleOperator) Pause(_ context.Context, _, _, connection string) error {
	o.paused = append(o.paused, connection)
	return nil
}

func admin2tenant(ctx context.Context, admin ledger.TenantAdmin, book string) string {
	b, _ := admin.Book(ctx, book)
	return b.TenantID
}

func (o *exampleOperator) RequestSync(context.Context, string, string) error { o.synced++; return nil }

func TestAnOperatorCanProvideBankAccess(t *testing.T) {
	f := documentServer(t)
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	op := &exampleOperator{store: ledger.Store{Runtime: f.db.Runtime}}
	banks := &bankconnect.Service{Store: ledger.Store{Runtime: f.db.Runtime}, Keys: &secretbox.Cached{Source: secretbox.FileSource{Path: filepath.Join(t.TempDir(), "k.json")}},
		Connectors: map[string]bankconnect.Connector{screenKey: &banktest.Screen{Key: screenKey}}}
	s, err := New(configuration(), Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: login},
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}}, Banks: banks, BankOperator: op, BankScreen: exampleScreen()})
	if err != nil {
		t.Fatal(err)
	}
	book := f.book.ID
	if w := bankCall(t, s, f.session, http.MethodGet, bankPath+"?book_id="+book, ""); !strings.Contains(w.Body.String(), `"keys":"operator"`) {
		t.Fatal("overview", w.Code, w.Body.String())
	}
	// A Vult with an operator never asks for a key or token: the one
	// connector is the provider's own screen, and a pasted token isn't
	// taken.
	if w := bankCall(t, s, f.session, http.MethodGet, bankConnectorsPath+"?book_id="+book, ""); w.Code != http.StatusOK ||
		strings.TrimSpace(w.Body.String()) != `{"connectors":[{"id":"`+screenKey+`","name":"Example Connect","kinds":["bank","card"],"route":"partner","start":"screen","ready":true,"script":"/bank-screen.js"}]}` {
		t.Fatal("operator connectors", w.Code, w.Body.String())
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankConnectTokenPath, `{"book_id":"`+book+`","provider":"mercury","token":"secret-token:x"}`); w.Code != http.StatusNotFound {
		t.Fatal("a hosted Vult took a token", w.Code)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, "/api/v1/bank/settings", `{"book_id":"`+book+`","environment":"sandbox","client_id":"x","secret":"y","expected_revision":0}`); w.Code != http.StatusNotFound {
		t.Fatal("owner keys accepted beside an operator", w.Code)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`"}`); !strings.Contains(w.Body.String(), "link-operator-example") {
		t.Fatal("link start", w.Body.String())
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkFinishPath, `{"book_id":"`+book+`","public_token":"p","institution_id":"ins_limit"}`); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "one bank") {
		t.Fatal("refusal", w.Code, w.Body.String())
	}
	w := bankCall(t, s, f.session, http.MethodPost, bankLinkFinishPath, `{"book_id":"`+book+`","public_token":"p","institution_id":"ins_example","accounts":[{"mask":"0001","name":"Checking","type":"depository","subtype":"checking"}]}`)
	var out BankLinkResult
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Reused || out.Notice == "" || len(op.finished.Accounts) != 1 || op.finished.Accounts[0].Mask != "0001" {
		t.Fatal("finish", w.Code, w.Body.String())
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankSyncPath, `{"book_id":"`+book+`"}`); w.Code != http.StatusOK || op.synced != 1 {
		t.Fatal("sync", w.Code, op.synced)
	}
	// Pausing is the Book's decision; the sync timer then asks the operator
	// to end the provider's access, once.
	if w := bankCall(t, s, f.session, http.MethodPost, bankPausePath, `{"book_id":"`+book+`","connection_id":"`+out.ConnectionID+`","reason":"Away for a while"}`); w.Code != http.StatusOK {
		t.Fatal("pause", w.Code, w.Body.String())
	}
	release(t, banks)
	release(t, banks)
	if len(op.paused) != 1 || op.paused[0] != out.ConnectionID {
		t.Fatal("operator pause", op.paused)
	}
	// An operator's paused login resumes by reconnecting through the link.
	if w := bankCall(t, s, f.session, http.MethodPost, bankResumePath, `{"book_id":"`+book+`","connection_id":"`+out.ConnectionID+`"}`); w.Code != http.StatusUnprocessableEntity {
		t.Fatal("resume of an operator login without reconnecting", w.Code, w.Body.String())
	}
	bankCall(t, s, f.session, http.MethodPost, bankLinkStartPath, `{"book_id":"`+book+`","connection_id":"`+out.ConnectionID+`"}`)
	if op.reconnectStartFor != out.ConnectionID {
		t.Fatal("reconnect start", op.reconnectStartFor)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankLinkFinishPath, `{"book_id":"`+book+`","public_token":"p","connection_id":"`+out.ConnectionID+`"}`); w.Code != http.StatusOK {
		t.Fatal("reconnect finish", w.Code, w.Body.String())
	}
	var status string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT status FROM public.bank_connections WHERE id=$1", out.ConnectionID).Scan(&status); err != nil || status != "active" {
		t.Fatal("reconnected status", status, err)
	}
	if w := bankCall(t, s, f.session, http.MethodPost, bankDisconnectPath, `{"book_id":"`+book+`","connection_id":"`+out.ConnectionID+`"}`); w.Code != http.StatusOK {
		t.Fatal("disconnect", w.Code, w.Body.String())
	}
	release(t, banks)
	if len(op.ended) != 1 {
		t.Fatal("operator disconnect", op.ended)
	}
}

// release runs the sync timer's step that ends provider access.
func release(t *testing.T, banks *bankconnect.Service) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { banks.Run(ctx); close(done) }()
	for i := 0; i < 50; i++ {
		if list, _ := banks.Store.BankConnectionsToRelease(context.Background(), 10); len(list) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
}
