package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestOwnerWatchesWhatAnApprovedAgentDoes(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("run socket coverage as a regular user")
	}
	db := testutil.New(t)
	_, scope := db.Book(t, "Example Book")
	c, err := newBrowserConfiguration(51997)
	if err != nil {
		t.Fatal(err)
	}
	owner := access.Identity{Issuer: "https://identity.example.com", Subject: "example-owner", AuthenticatedAt: time.Now().UTC()}
	if _, err := (access.Operator{DB: db.Admin}).BindOwner(db.Context, scope.TenantID, owner); err != nil {
		t.Fatal(err)
	}
	reader, err := access.NewOwnerDocumentReader(db.Context, db.Admin, c.origin())
	if err != nil {
		t.Fatal(err)
	}
	raw := access.Random()
	var who string
	if err := db.Admin.QueryRow(db.Context, "SELECT access.issue_session($1,$2,$3,$4,clock_timestamp())::text", owner.Issuer, owner.Subject, c.origin(), access.Hash(raw)).Scan(&who); err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	if err := os.Chmod(storage, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(storage)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	keyJSON, err := documents.NewKeyFile(scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.ParseKeyring(keyJSON)
	if err != nil {
		t.Fatal(err)
	}
	db.Store.Documents = &documents.Vault{Storage: disk, Keys: keys}
	uid := uint32(os.Getuid())
	directory, err := os.MkdirTemp("/tmp", "vult-activity-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "service.sock")
	listener, err := Listen(path)
	if err != nil {
		os.RemoveAll(directory)
		t.Fatal(err)
	}
	s := &Server{Admin: db.Owner, Ledger: db.Store, Browser: &browserOwner{configuration: c, uid: uid, documents: reader}}
	ctx, cancel := context.WithCancel(db.Context)
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			t.Error("activity socket did not stop")
		}
		os.RemoveAll(directory)
	})
	client := local.Client{Socket: path, ExpectedUID: uid}

	// A waiting agent shows to the owner, without access to anything yet.
	ticket, err := client.RequestAgent(ctx, "Claude Code")
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.OwnerActivity(ctx, raw, local.ActivityQuery{BookID: scope.BookID})
	if err != nil || len(page.Waiting) != 1 || page.Waiting[0].Request != ticket.Request || page.Waiting[0].Client != "Claude Code" || len(page.Events) != 0 || len(page.Connected) != 0 {
		t.Fatal("the owner could not see the agent asking to be let in", page, err)
	}
	for _, attempt := range []struct{ session, book string }{{access.Random(), scope.BookID}, {raw, ledger.ID()}} {
		if _, err := client.OwnerActivity(ctx, attempt.session, local.ActivityQuery{BookID: attempt.book}); err == nil {
			t.Fatal("activity was readable without the owner's session for that Book")
		}
	}
	if err := client.ApproveAgent(ctx, raw, local.AgentApproval{Request: ticket.Request, BookID: scope.BookID, Access: "write"}); err != nil {
		t.Fatal(err)
	}
	status, err := client.AgentStatus(ctx, ticket.Request)
	if err != nil || status.Token == "" {
		t.Fatal("the agent was not let in", status, err)
	}
	call := func(method string, input any) (json.RawMessage, error) {
		return client.Call(ctx, method, status.Token, input)
	}
	for range 2 {
		if _, err := call("create_account", map[string]string{"request_key": "account-1", "class": "asset", "name": "Example Checking"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := call("get_trial_balance", struct{}{}); err != nil {
		t.Fatal(err)
	}
	waited, err := call(local.WaitForDocumentMethod, map[string]int{"seconds": 1})
	var outcome struct{ Status string }
	if err != nil || json.Unmarshal(waited, &outcome) != nil || outcome.Status != "none_yet" {
		t.Fatal("waiting with nothing uploaded did not say so", string(waited), err)
	}
	if _, err := call(local.WaitForDocumentMethod, map[string]int{"seconds": 600}); err == nil {
		t.Fatal("an agent could wait past the bounded limit")
	}
	if _, err := call(local.ReportUnusableMethod, map[string]string{"document_id": ledger.ID(), "reason": "Not a statement"}); err == nil {
		t.Fatal("a missing original was reported")
	}

	// An original that arrives is found by waiting, and can be reported.
	content := []byte("date,description,amount\n2026-10-01,Example,1.00\n")
	sum := sha256.Sum256(content)
	prepared, err := call("add_document", map[string]any{"request_key": "doc-1", "name": "example-photo.csv", "media_type": "text/csv", "size": len(content), "sha256": hex.EncodeToString(sum[:])})
	var added struct {
		UploadID string `json:"upload_id"`
	}
	if err != nil || json.Unmarshal(prepared, &added) != nil || added.UploadID == "" {
		t.Fatal("the original could not be prepared", string(prepared), err)
	}
	transport := local.DocumentTransport{Client: client, Authorization: status.Token}
	if _, err := transport.Upload(ctx, added.UploadID, int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	waited, err = call(local.WaitForDocumentMethod, map[string]int{"seconds": 1})
	var ready struct {
		Status   string
		Document struct{ ID, Name string }
	}
	if err != nil || json.Unmarshal(waited, &ready) != nil || ready.Status != "ready" || ready.Document.Name != "example-photo.csv" {
		t.Fatal("waiting did not find the new original", string(waited), err)
	}
	// Looking up an original's name and size isn't reading it; downloading it is.
	if _, err := call("get_document", map[string]string{"document_id": ready.Document.ID}); err != nil {
		t.Fatal(err)
	}
	if err := transport.Download(ctx, ready.Document.ID, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := call(local.ReportUnusableMethod, map[string]string{"document_id": ready.Document.ID, "reason": "This is a receipt, not a statement."}); err != nil {
		t.Fatal(err)
	}

	page, err = client.OwnerActivity(ctx, raw, local.ActivityQuery{BookID: scope.BookID})
	if err != nil || len(page.Waiting) != 0 || len(page.Events) != 4 || len(page.Connected) != 1 || page.Connected[0] != "Claude Code" {
		t.Fatal("the owner did not see the agent's work once", page, err)
	}
	if e := page.Events[0]; e.Kind != "account" || e.Name != "Example Checking" || e.Agent != "Claude Code" {
		t.Fatal("the account the agent created was not shown", e)
	}
	if page.Events[1].Kind != "balanced" {
		t.Fatal("the balance check was not shown", page.Events[1])
	}
	if e := page.Events[2]; e.Kind != "opened" || e.Document != "example-photo.csv" {
		t.Fatal("reading the original was not shown once", page.Events)
	}
	if e := page.Events[3]; e.Kind != "unusable" || e.Document != "example-photo.csv" || e.Reason != "This is a receipt, not a statement." || page.Next != 4 {
		t.Fatal("the unusable original was not shown with its reason", e, page.Next)
	}
	later, err := client.OwnerActivity(ctx, raw, local.ActivityQuery{BookID: scope.BookID, After: page.Next})
	if err != nil || len(later.Events) != 0 {
		t.Fatal("events were repeated after the owner's cursor", later, err)
	}
	if other, _ := client.OwnerActivity(ctx, raw, local.ActivityQuery{BookID: scope.BookID, After: -1}); len(other.Events) != 0 {
		t.Fatal("a negative cursor was accepted")
	}
}
