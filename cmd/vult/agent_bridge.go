package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/internal/filebridge"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/mcpserver"
	"github.com/vult-ai/vult/ledger"
)

// agentTransport lets an agent start Vult without a connection ID. Its first
// call asks the owner for approval, which they give in Vult in the browser.
// Each call waits a while for that approval before it explains what to do.
type agentTransport struct {
	client local.Client
	name   string
	// wait is how long one call waits for the owner; poll is how often it looks.
	wait, poll time.Duration

	mu      sync.Mutex
	ticket  local.AgentTicket
	session local.DocumentTransport
	ready   bool
}

type approvalPending struct {
	url    string
	denied bool
}

func (e approvalPending) Error() string {
	if e.denied {
		return "The owner didn't allow this agent to use Vult. Ask them before trying again. Vult has a new request waiting; they can allow it in Vult or at " + e.url
	}
	return "Vult is waiting for the owner to let this agent in. Tell them: \"Open Vult (" + bookAddress(e.url) + ") and choose Allow at the top.\" Then call this tool again; it waits for them."
}

// bookAddress is the owner's Vult address, where the request shows at the top
// of the Book; the approval page stays the fallback.
func bookAddress(approval string) string {
	if i := strings.Index(approval, "/auth/"); i > 0 {
		return approval[:i]
	}
	return approval
}

func (t *agentTransport) authorization(ctx context.Context) (local.DocumentTransport, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ready {
		return t.session, nil
	}
	denied := false
	if t.ticket.Request == "" {
		ticket, err := t.client.RequestAgent(ctx, t.name)
		if err != nil {
			return local.DocumentTransport{}, fmt.Errorf("Vult couldn't ask the owner for approval (%v). Check the installation with: %s doctor", err, local.BinaryPath)
		}
		t.ticket = ticket
	}
	deadline := time.Now().Add(t.wait)
	for {
		status, err := t.client.AgentStatus(ctx, t.ticket.Request)
		if err != nil {
			return local.DocumentTransport{}, err
		}
		switch status.State {
		case local.AgentApproved:
			t.session = local.DocumentTransport{Client: t.client, Authorization: status.Token}
			t.ready = true
			return t.session, nil
		case local.AgentWaiting:
			if time.Now().Add(t.poll).Before(deadline) {
				select {
				case <-ctx.Done():
					return local.DocumentTransport{}, ctx.Err()
				case <-time.After(t.poll):
				}
				continue
			}
			return local.DocumentTransport{}, approvalPending{url: t.ticket.ApproveURL}
		case local.AgentDenied:
			denied = true
		}
		// Denied or expired: ask again, so the owner can still change their mind.
		ticket, err := t.client.RequestAgent(ctx, t.name)
		if err != nil {
			return local.DocumentTransport{}, fmt.Errorf("Vult couldn't ask the owner for approval (%v). Check the installation with: %s doctor", err, local.BinaryPath)
		}
		t.ticket = ticket
		if denied {
			return local.DocumentTransport{}, approvalPending{url: ticket.ApproveURL, denied: true}
		}
	}
}

// expire drops a session the service no longer accepts, so the next call asks
// the owner again.
func (t *agentTransport) expire(err error) error {
	if err != nil && err.Error() == ledger.ErrDenied.Error() {
		t.mu.Lock()
		t.ready = false
		t.ticket = local.AgentTicket{}
		t.mu.Unlock()
		return fmt.Errorf("%w. If this agent's 8-hour access ended, call the tool again to ask the owner", err)
	}
	return err
}

func (t *agentTransport) Call(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
	s, err := t.authorization(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.Call(ctx, method, input)
	return out, t.expire(err)
}

func (t *agentTransport) Upload(ctx context.Context, id string, size int64, source io.Reader) (json.RawMessage, error) {
	s, err := t.authorization(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.Upload(ctx, id, size, source)
	return out, t.expire(err)
}

func (t *agentTransport) Download(ctx context.Context, id string, dst io.Writer) error {
	s, err := t.authorization(ctx)
	if err != nil {
		return err
	}
	return t.expire(s.Download(ctx, id, dst))
}

func runAgentBridge(ctx context.Context, name, imports, exports string) error {
	if name == "" {
		name = "An agent on this computer"
	}
	client, ok := local.AgentClientName(name)
	if !ok {
		return ledger.ErrInvalid
	}
	if exports == "" {
		// Statements the owner uploads reach the agent as copies in a folder
		// only this user can open. Copies from earlier sessions are cleared.
		folder, err := local.DocumentCopiesFolder(true)
		if err != nil {
			return err
		}
		_, _ = local.ClearDocumentCopies(folder, local.CopyLife)
		exports = folder
	}
	if imports != "" || exports != "" {
		if !(local.SessionRequest{ConnectionID: ledger.ID(), ImportDirectory: imports, ExportDirectory: exports}).Valid() {
			return ledger.ErrInvalid
		}
	}
	c, err := local.NewClient()
	if err != nil {
		return err
	}
	transport := &agentTransport{client: c, name: client, wait: 40 * time.Second, poll: 2 * time.Second}
	files, err := filebridge.Open(transport, imports, exports)
	if err != nil {
		return err
	}
	defer files.Close()
	// Tools are listed for write access; a read-only approval refuses writes.
	server := mcpserver.New(files.Call, "write", mcpserver.Options{Documents: true, LocalFiles: true, OwnerUploads: true})
	return server.Run(ctx, &mcp.StdioTransport{})
}
