package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestSocketLostReplyAndEncryptedEmptyTargetRecovery(t *testing.T) {
	db := testutil.New(t)
	s := &Server{Admin: db.Owner, Ledger: db.Store, RecoveryKey: secret()}
	uid := uint32(os.Getuid())
	book, _ := db.Book(t, "Example recovery")
	var connection ledger.Connection
	json.Unmarshal(ownerRequest(t, s, uid, "connection_create", map[string]any{"book_id": book.ID, "name": "Example connection", "access": "write"}), &connection)
	var opened struct {
		Token     string    `json:"token"`
		Access    string    `json:"access"`
		BookID    string    `json:"book_id"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	json.Unmarshal(ownerRequest(t, s, uid, "open_session", map[string]any{"connection_id": connection.ID}), &opened)
	if opened.Token == "" || opened.BookID != book.ID {
		t.Fatal("session did not select its fixed Book")
	}
	dir, e := os.MkdirTemp("/tmp", "vult-journey-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "service.sock")
	listener, e := Listen(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(db.Context)
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(7 * time.Second):
			t.Error("socket did not stop")
		}
	})
	client := local.Client{Socket: path, ExpectedUID: uid}
	accounts := []ledger.Account{}
	for _, q := range []ledger.CreateAccount{{RequestKey: "cash", Class: "asset", Code: "1000", Name: "Example cash"}, {RequestKey: "capital", Class: "equity", Code: "3000", Name: "Example capital"}} {
		result, e := client.Call(ctx, "create_account", opened.Token, q)
		if e != nil {
			t.Fatal(e)
		}
		var account ledger.Account
		if json.Unmarshal(result, &account) != nil {
			t.Fatal("invalid account response")
		}
		accounts = append(accounts, account)
	}
	request := ledger.PostJournal{RequestKey: "lost-reply", EconomicDate: "2026-01-01", AccountingDate: "2026-01-02", Description: "Example funding", Lines: []ledger.PostLine{{AccountID: accounts[0].ID, Side: "debit", AmountMinor: "1250"}, {AccountID: accounts[1].ID, Side: "credit", AmountMinor: "1250"}}}
	input := marshal(request)
	conn, e := net.Dial("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	header := marshal(local.Header{Method: "post_journal", Authorization: opened.Token, Size: len(input)})
	if _, e = conn.Write(append(header, '\n')); e != nil {
		t.Fatal(e)
	}
	if _, e = io.Copy(conn, bytes.NewReader(input)); e != nil {
		t.Fatal(e)
	}
	conn.Close() // Deliberately discard the reply after sending the whole request.
	deadline := time.Now().Add(5 * time.Second)
	recorded := false
	for time.Now().Before(deadline) {
		var n int
		if e = db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.entries").Scan(&n); e != nil {
			t.Fatal(e)
		}
		if n == 1 {
			recorded = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !recorded {
		t.Fatal("test did not establish a committed lost reply")
	}
	out, e := client.Call(ctx, "get_request_outcome", opened.Token, ledger.OutcomeQuery{RequestKey: request.RequestKey})
	if e != nil {
		t.Fatal(e)
	}
	var outcome ledger.Outcome
	json.Unmarshal(out, &outcome)
	retry, e := client.Call(ctx, "post_journal", opened.Token, request)
	if e != nil || outcome.Status != "recorded" || string(retry) != string(outcome.Result) {
		t.Fatal("socket retry did not recover the exact result", e)
	}
	if e = db.Owner.ClosePeriod(ctx, book.ID, "2026-01-01", "2026-01-31"); e != nil {
		t.Fatal(e)
	}
	var backup struct {
		Data []byte `json:"data"`
	}
	json.Unmarshal(ownerRequest(t, s, uid, "backup", struct{}{}), &backup)
	if len(backup.Data) == 0 {
		t.Fatal("encrypted backup missing")
	}
	target := testutil.New(t)
	replacement := &Server{Admin: target.Owner, Ledger: target.Store, RecoveryKey: secret()}
	ownerRequest(t, replacement, uid, "restore", map[string]any{"data": backup.Data, "recovery_key": s.RecoveryKey})
	before, e := db.Owner.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	after, e := target.Owner.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for name, want := range before.Tables {
		if string(want) != string(after.Tables[name]) {
			t.Fatal("encrypted recovery changed a row", name)
		}
	}
	ownerRequest(t, s, uid, "connection_revoke", map[string]any{"connection_id": connection.ID})
	if _, e = client.Call(ctx, "get_book", opened.Token, struct{}{}); e == nil {
		t.Fatal("revoked socket session remained usable")
	}
}
