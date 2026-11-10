package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/testutil"
)

func TestAgentsAreApprovedOnlyByTheOwnersRecentPasskeySession(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("run socket coverage as a regular user")
	}
	db := testutil.New(t)
	_, scope := db.Book(t, "Example Book")
	c, err := newBrowserConfiguration(51998)
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
	issue := func(at time.Time) string {
		t.Helper()
		raw := access.Random()
		var who string
		if err := db.Admin.QueryRow(db.Context, "SELECT access.issue_session($1,$2,$3,$4,$5)::text", owner.Issuer, owner.Subject, c.origin(), access.Hash(raw), at).Scan(&who); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	fresh, stale := issue(time.Now().UTC()), issue(time.Now().UTC())
	// A sign-in from ten minutes ago: still signed in, but not recent.
	if _, err := db.Admin.Exec(db.Context, "UPDATE access.sessions SET authenticated_at=clock_timestamp()-interval '10 minutes' WHERE token_hash=$1", access.Hash(stale)); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Getuid())
	start := func(t *testing.T, browserUID uint32) local.Client {
		t.Helper()
		directory, err := os.MkdirTemp("/tmp", "vult-agents-")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "service.sock")
		listener, err := Listen(path)
		if err != nil {
			os.RemoveAll(directory)
			t.Fatal(err)
		}
		s := &Server{Admin: db.Owner, Ledger: db.Store, Browser: &browserOwner{configuration: c, uid: browserUID, documents: reader}}
		ctx, cancel := context.WithCancel(db.Context)
		done := make(chan error, 1)
		go func() { done <- s.Serve(ctx, listener) }()
		t.Cleanup(func() {
			cancel()
			listener.Close()
			select {
			case <-done:
			case <-time.After(7 * time.Second):
				t.Error("agent socket did not stop")
			}
			os.RemoveAll(directory)
		})
		return local.Client{Socket: path, ExpectedUID: uid}
	}
	ctx := db.Context
	connections := func() int {
		t.Helper()
		var n int
		if err := db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.connections WHERE book_id=$1 AND revoked_at IS NULL", scope.BookID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := connections()

	// The browser service runs as its own account; any other peer is refused.
	foreign := start(t, uid+1)
	ticket, err := foreign.RequestAgent(ctx, "Claude Code")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.ViewAgent(ctx, fresh, ticket.Request); err == nil {
		t.Fatal("a process that isn't the browser service read an agent request")
	}
	if err := foreign.ApproveAgent(ctx, fresh, local.AgentApproval{Request: ticket.Request, BookID: scope.BookID, Access: "write"}); err == nil {
		t.Fatal("a process that isn't the browser service approved an agent")
	}

	client := start(t, uid)
	ticket, err = client.RequestAgent(ctx, "Claude Code")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ticket.ApproveURL, c.origin()+"/auth/agent?request=") {
		t.Fatal("the approval page is not on the local browser address", ticket.ApproveURL)
	}
	status, err := client.AgentStatus(ctx, ticket.Request)
	if err != nil || status.State != local.AgentWaiting || status.Token != "" {
		t.Fatal("a new request carried access", status, err)
	}
	view, err := client.ViewAgent(ctx, fresh, ticket.Request)
	if err != nil || view.Client != "Claude Code" {
		t.Fatal("the owner could not see who is asking", view, err)
	}
	for _, session := range []string{stale, access.Random()} {
		if err := client.ApproveAgent(ctx, session, local.AgentApproval{Request: ticket.Request, BookID: scope.BookID, Access: "write"}); err == nil {
			t.Fatal("an agent was approved without a recent passkey sign-in")
		}
	}
	if status, _ = client.AgentStatus(ctx, ticket.Request); status.State != local.AgentWaiting || connections() != before {
		t.Fatal("a refused approval changed the request or created a connection", status.State)
	}
	if err := client.ApproveAgent(ctx, fresh, local.AgentApproval{Request: ticket.Request, BookID: scope.BookID, Access: "write"}); err != nil {
		t.Fatal(err)
	}
	status, err = client.AgentStatus(ctx, ticket.Request)
	if err != nil || status.State != local.AgentApproved || status.Token == "" || status.Access != "write" || status.BookID != scope.BookID {
		t.Fatal("the approved session did not reach the agent", status, err)
	}
	if again, _ := client.AgentStatus(ctx, ticket.Request); again.State != local.AgentExpired || again.Token != "" {
		t.Fatal("an approved session was handed out twice", again.State)
	}
	if _, err := client.Call(ctx, "get_book", status.Token, struct{}{}); err != nil {
		t.Fatal("the approved session could not read its Book", err)
	}

	// Approving the same agent again keeps its connection.
	next, _ := client.RequestAgent(ctx, "Claude Code")
	if err := client.ApproveAgent(ctx, fresh, local.AgentApproval{Request: next.Request, BookID: scope.BookID, Access: "write"}); err != nil {
		t.Fatal(err)
	}
	if connections() != before+1 {
		t.Fatal("approving the same agent again created another connection", connections())
	}

	declined, _ := client.RequestAgent(ctx, "Unknown helper")
	if err := client.ApproveAgent(ctx, fresh, local.AgentApproval{Request: declined.Request, Deny: true}); err != nil {
		t.Fatal(err)
	}
	if status, _ = client.AgentStatus(ctx, declined.Request); status.State != local.AgentDenied || status.Token != "" {
		t.Fatal("a declined agent received access", status.State)
	}

	// The agent drafts a Book; only the owner's recent sign-in creates it.
	draft := local.BookDraft{Name: "Example Trading", Profile: "business", Currency: "EUR", Timezone: "Europe/Berlin"}
	if err := client.DraftBook(ctx, draft); err != nil {
		t.Fatal(err)
	}
	seen, err := client.BookDraftView(ctx, fresh)
	if err != nil || seen == nil || *seen != draft {
		t.Fatal("the owner could not see the drafted Book", seen, err)
	}
	if _, err := client.BookDraftView(ctx, access.Random()); err == nil {
		t.Fatal("the draft was readable without the owner's session")
	}
	if _, err := client.CreateBookForOwner(ctx, stale, local.OwnerBookCreate{Name: draft.Name, Currency: "EUR", Scale: 2}); err == nil {
		t.Fatal("a Book was created without a recent passkey sign-in")
	}
	book, err := client.CreateBookForOwner(ctx, fresh, local.OwnerBookCreate{Name: draft.Name, Currency: "EUR", Scale: 2})
	if err != nil || book.Currency != "EUR" || book.Name != draft.Name {
		t.Fatal("the owner could not create the drafted Book", book, err)
	}
	if seen, _ = client.BookDraftView(ctx, fresh); seen != nil {
		t.Fatal("the draft stayed after the Book was created")
	}

	// Setup secrets wait for the owner's browser and are shown once.
	s := &Server{Admin: db.Owner, Ledger: db.Store, Browser: &browserOwner{configuration: c, uid: uid, documents: reader}}
	s.recovery = &pendingRecovery{key: "example-backup-key", created: time.Now()}
	take := func(session string, peer uint32) (local.SetupSecrets, error) {
		var out local.SetupSecrets
		data, err := s.Handle(ctx, peer, local.Request{Method: "owner_recovery", Authorization: session, Payload: marshal(struct{}{})})
		if err == nil {
			err = json.Unmarshal(data, &out)
		}
		return out, err
	}
	for _, attempt := range []struct {
		session string
		peer    uint32
	}{{stale, uid}, {access.Random(), uid}, {fresh, uid + 1}} {
		if _, err := take(attempt.session, attempt.peer); err == nil {
			t.Fatal("setup secrets were shown without the owner's recent sign-in in the browser")
		}
	}
	secrets, err := take(fresh, uid)
	if err != nil || secrets.BackupKey != "example-backup-key" {
		t.Fatal("the owner could not see the setup secrets", secrets, err)
	}
	if _, err := take(fresh, uid); err == nil {
		t.Fatal("setup secrets were shown twice")
	}
}
