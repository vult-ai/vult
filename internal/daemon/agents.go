package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// Agents on this computer ask for access here instead of through a native
// administrator prompt. A request carries no authority: it waits until the
// owner approves it in the local browser, where the ledger service checks the
// owner's own session and a passkey sign-in from the last five minutes.

const (
	agentRequestLife = 10 * time.Minute
	agentSessionLife = 8 * time.Hour
	maxAgentRequests = 16
)

// The backup key waits this long for the owner's browser to show it.
const recoveryDisplayLife = 30 * time.Minute

type pendingRecovery struct {
	key     string
	created time.Time
}

type agentRequest struct {
	uid     uint32
	client  string
	created time.Time
	state   string
	token   string
	scope   ledger.Scope
	expires time.Time
}

func (s *Server) ownerPeer(uid uint32) bool {
	return s.Browser != nil && s.Browser.uid != 0 && uid == s.Browser.uid && s.Browser.documents != nil
}

// admitAgent reports whether a header for an agent or owner-browser method is
// acceptable, and whether the method is one of these at all.
func (s *Server) admitAgent(uid uint32, h local.Header) (ok, handled bool) {
	switch h.Method {
	case "agent_request", "agent_request_status", "book_draft":
		return uid != 0 && h.Authorization == "" && h.Size <= 4096, true
	case "owner_agent_view", "owner_agent_approve", "owner_book_draft", "owner_book_create", "owner_recovery", "owner_agent_activity":
		return s.ownerPeer(uid) && len(h.Authorization) == 43 && h.Size <= 4096, true
	}
	return false, false
}

func (s *Server) handleAgent(ctx context.Context, uid uint32, q local.Request) (json.RawMessage, error, bool) {
	switch q.Method {
	case "agent_request":
		out, err := s.agentRequest(uid, q.Payload)
		return out, err, true
	case "agent_request_status":
		out, err := s.agentStatus(uid, q.Payload)
		return out, err, true
	case "book_draft":
		var draft local.BookDraft
		if strictjson.Decode(q.Payload, &draft) != nil || !draft.Valid() {
			return nil, ledger.ErrInvalid, true
		}
		s.mu.Lock()
		s.draft = &draft
		s.mu.Unlock()
		return marshal(struct {
			Status string `json:"status"`
		}{"drafted"}), nil, true
	case "owner_agent_view", "owner_agent_approve", "owner_book_draft", "owner_book_create", "owner_recovery", "owner_agent_activity":
	default:
		return nil, nil, false
	}
	if !s.ownerPeer(uid) {
		return nil, ledger.ErrDenied, true
	}
	switch q.Method {
	case "owner_agent_activity":
		out, err := s.ownerActivity(ctx, q.Authorization, q.Payload)
		return out, err, true
	case "owner_agent_view":
		out, err := s.agentView(q.Payload)
		return out, err, true
	case "owner_agent_approve":
		out, err := s.agentApprove(ctx, q.Authorization, q.Payload)
		return out, err, true
	case "owner_book_draft":
		if strictjson.Decode(q.Payload, &struct{}{}) != nil {
			return nil, ledger.ErrInvalid, true
		}
		// Reading the draft still needs the owner's live session.
		if _, err := s.Browser.documents.OwnerTenant(ctx, q.Authorization); err != nil {
			return nil, ledger.ErrDenied, true
		}
		s.mu.Lock()
		draft := s.draft
		s.mu.Unlock()
		return marshal(draft), nil, true
	case "owner_recovery":
		if strictjson.Decode(q.Payload, &struct{}{}) != nil {
			return nil, ledger.ErrInvalid, true
		}
		if err := s.Browser.documents.RecentOwner(ctx, q.Authorization); err != nil {
			return nil, ledger.ErrDenied, true
		}
		s.mu.Lock()
		pending := s.recovery
		s.recovery = nil
		s.mu.Unlock()
		if pending == nil || time.Since(pending.created) > recoveryDisplayLife {
			return nil, ledger.ErrNotFound, true
		}
		return marshal(local.SetupSecrets{BackupKey: pending.key}), nil, true
	case "owner_book_create":
		var create local.OwnerBookCreate
		if strictjson.Decode(q.Payload, &create) != nil {
			return nil, ledger.ErrInvalid, true
		}
		book, err := s.Browser.documents.CreateBook(ctx, q.Authorization, create.Name, create.Currency, create.Scale)
		if err != nil {
			return nil, err, true
		}
		s.mu.Lock()
		s.draft = nil
		s.mu.Unlock()
		return marshal(book), nil, true
	}
	return nil, ledger.ErrInvalid, true
}

func (s *Server) pruneAgentsLocked(now time.Time) {
	for id, r := range s.agents {
		if r.state == local.AgentApproved && now.Before(r.expires) {
			continue
		}
		if now.Sub(r.created) > agentRequestLife {
			delete(s.agents, id)
		}
	}
}

func (s *Server) agentRequest(uid uint32, input json.RawMessage) (json.RawMessage, error) {
	var q local.AgentRequest
	if strictjson.Decode(input, &q) != nil {
		return nil, ledger.ErrInvalid
	}
	client, ok := local.AgentClientName(q.Client)
	if !ok {
		return nil, ledger.ErrInvalid
	}
	if s.Browser == nil || !s.Browser.configuration.valid() {
		return nil, errors.New("the local browser is not installed; reinstall Vult")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agents == nil {
		s.agents = map[string]*agentRequest{}
	}
	now := time.Now()
	s.pruneAgentsLocked(now)
	if len(s.agents) >= maxAgentRequests {
		return nil, errors.New("too many agent requests are waiting; approve or wait for them to expire")
	}
	id := secret()
	s.agents[id] = &agentRequest{uid: uid, client: client, created: now, state: local.AgentWaiting}
	return marshal(local.AgentTicket{Request: id, ApproveURL: s.Browser.configuration.origin() + "/auth/agent?request=" + id}), nil
}

func (s *Server) agentStatus(uid uint32, input json.RawMessage) (json.RawMessage, error) {
	var q local.AgentRequestStatus
	if strictjson.Decode(input, &q) != nil || !local.ValidAgentRequestID(q.Request) {
		return nil, ledger.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.pruneAgentsLocked(now)
	r, ok := s.agents[q.Request]
	if !ok || r.uid != uid {
		return marshal(local.AgentStatus{State: local.AgentExpired}), nil
	}
	switch r.state {
	case local.AgentApproved:
		// The session goes to the bridge once; the request is then spent.
		delete(s.agents, q.Request)
		if !now.Before(r.expires) {
			return marshal(local.AgentStatus{State: local.AgentExpired}), nil
		}
		s.initMaps()
		s.sessions[tokenKey(r.token)] = session{r.scope, uid, r.expires}
		expires := r.expires
		return marshal(local.AgentStatus{State: local.AgentApproved, Token: r.token, Access: r.scope.Access, BookID: r.scope.BookID, ExpiresAt: &expires}), nil
	case local.AgentDenied:
		delete(s.agents, q.Request)
		return marshal(local.AgentStatus{State: local.AgentDenied}), nil
	}
	return marshal(local.AgentStatus{State: local.AgentWaiting}), nil
}

func (s *Server) agentView(input json.RawMessage) (json.RawMessage, error) {
	var q local.AgentRequestView
	if strictjson.Decode(input, &q) != nil || !local.ValidAgentRequestID(q.Request) {
		return nil, ledger.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneAgentsLocked(time.Now())
	r, ok := s.agents[q.Request]
	if !ok || r.state != local.AgentWaiting {
		return nil, ledger.ErrNotFound
	}
	return marshal(local.AgentView{Client: r.client, CreatedAt: r.created}), nil
}

func (s *Server) agentApprove(ctx context.Context, ownerSession string, input json.RawMessage) (json.RawMessage, error) {
	var q local.AgentApproval
	if strictjson.Decode(input, &q) != nil || !q.Valid() {
		return nil, ledger.ErrInvalid
	}
	s.mu.Lock()
	r, ok := s.agents[q.Request]
	waiting := ok && r.state == local.AgentWaiting && time.Since(r.created) <= agentRequestLife
	client := ""
	if waiting {
		client = r.client
	}
	s.mu.Unlock()
	if !waiting {
		return nil, ledger.ErrNotFound
	}
	if q.Deny {
		// Declining still needs the owner's session, so a stray request can't
		// be cancelled by anyone else.
		if _, err := s.Browser.documents.OwnerTenant(ctx, ownerSession); err != nil {
			return nil, ledger.ErrDenied
		}
		s.mu.Lock()
		if r, ok := s.agents[q.Request]; ok && r.state == local.AgentWaiting {
			r.state = local.AgentDenied
		}
		s.mu.Unlock()
		return marshal(struct {
			Status string `json:"status"`
		}{"denied"}), nil
	}
	c, err := s.Browser.documents.ConnectAgent(ctx, ownerSession, q.BookID, client, q.Access)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok = s.agents[q.Request]
	if !ok || r.state != local.AgentWaiting {
		return nil, ledger.ErrNotFound
	}
	r.state = local.AgentApproved
	if s.agentNames == nil {
		s.agentNames = map[string]string{}
	}
	s.agentNames[c.ID] = client
	r.token = secret()
	r.scope = ledger.Scope{TenantID: c.TenantID, BookID: c.BookID, ActorID: c.ActorID, ConnectionID: c.ID, Access: c.Access}
	r.expires = time.Now().Add(agentSessionLife)
	return marshal(struct {
		Status     string `json:"status"`
		Connection string `json:"connection"`
		Access     string `json:"access"`
	}{"approved", c.Name, c.Access}), nil
}

// localAgents lets the local browser's sign-in pages decide agent requests
// through the ledger service.
type localAgents struct{ client local.Client }

func (a localAgents) AgentClient(ctx context.Context, session, request string) (string, error) {
	view, err := a.client.ViewAgent(ctx, session, request)
	return view.Client, err
}

func (a localAgents) DecideAgent(ctx context.Context, session, request, book, access string, deny bool) error {
	return a.client.ApproveAgent(ctx, session, local.AgentApproval{Request: request, BookID: book, Access: access, Deny: deny})
}

// localBooks lets the local browser create Books through the ledger service.
type localBooks struct{ client local.Client }

func (b localBooks) Draft(ctx context.Context, session string) (json.RawMessage, error) {
	draft, err := b.client.BookDraftView(ctx, session)
	if err != nil {
		return nil, err
	}
	return marshal(draft), nil
}

func (b localBooks) Create(ctx context.Context, session, name, currency string, scale int) (ledger.Book, error) {
	return b.client.CreateBookForOwner(ctx, session, local.OwnerBookCreate{Name: name, Currency: currency, Scale: scale})
}

func (b localBooks) Activity(ctx context.Context, session, book string, after int64) (json.RawMessage, error) {
	page, err := b.client.OwnerActivity(ctx, session, local.ActivityQuery{BookID: book, After: after})
	if err != nil {
		return nil, err
	}
	return marshal(page), nil
}

func (b localBooks) Decide(ctx context.Context, session, request, book, access string, deny bool) error {
	return b.client.ApproveAgent(ctx, session, local.AgentApproval{Request: request, BookID: book, Access: access, Deny: deny})
}

func (b localAgents) TakeBackupKey(ctx context.Context, session string) (string, error) {
	secrets, err := b.client.SetupSecrets(ctx, session)
	return secrets.BackupKey, err
}
