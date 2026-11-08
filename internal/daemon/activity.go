package daemon

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// The service keeps a short record of what agents do in each Book, so the
// owner can watch their work as it happens. It holds what Vult itself sees:
// which original an agent opened, the accounts and entries it wrote and the
// checks it ran. It never holds document contents and is lost on restart.

const (
	maxActivityPerBook = 200
	maxActivityBooks   = 64
	// Agents wait in bounded steps so no client times out the call.
	maxAgentWait = 45 * time.Second
	agentPoll    = 2 * time.Second
)

type bookActivity struct {
	next   int64
	events []local.AgentActivity
	used   time.Time
}

func (s *Server) agentName(scope ledger.Scope) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name := s.agentNames[scope.ConnectionID]; name != "" {
		return name
	}
	return "Your agent"
}

// note records one event for the Book in scope. Repeats of the same step are
// kept once, so polling or retries don't flood the record.
func (s *Server) note(scope ledger.Scope, event local.AgentActivity) {
	if !ledger.ValidID(scope.BookID) {
		return
	}
	event.Agent = s.agentName(scope)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activity == nil {
		s.activity = map[string]*bookActivity{}
	}
	book := s.activity[scope.BookID]
	if book == nil {
		if len(s.activity) >= maxActivityBooks {
			oldest := ""
			for id, b := range s.activity {
				if oldest == "" || b.used.Before(s.activity[oldest].used) {
					oldest = id
				}
			}
			delete(s.activity, oldest)
		}
		book = &bookActivity{}
		s.activity[scope.BookID] = book
	}
	now := time.Now()
	book.used = now
	if n := len(book.events); n > 0 {
		last := book.events[n-1]
		if last.Kind == event.Kind && last.Document == event.Document && last.Name == event.Name && last.Description == event.Description && last.Date == event.Date && event.Kind != "entry" {
			return
		}
	}
	book.next++
	event.Seq = book.next
	event.At = now.UTC()
	book.events = append(book.events, event)
	if len(book.events) > maxActivityPerBook {
		book.events = append([]local.AgentActivity(nil), book.events[len(book.events)-maxActivityPerBook:]...)
	}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// observe turns a finished agent call into an event, when it is one the owner
// would recognise as progress. Opening an original is noted only when its bytes
// are actually read, not when its name and size are looked up.
func (s *Server) observe(scope ledger.Scope, method string, input, result json.RawMessage) {
	switch method {
	case "get_source_usage":
		s.note(scope, local.AgentActivity{Kind: "checked"})
	case "create_account":
		var q struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(input, &q) == nil && q.Name != "" {
			s.note(scope, local.AgentActivity{Kind: "account", Name: clip(q.Name, 120)})
		}
	case "post_journal":
		var q struct {
			Date        string `json:"accounting_date"`
			Description string `json:"description"`
		}
		if json.Unmarshal(input, &q) == nil {
			s.note(scope, local.AgentActivity{Kind: "entry", Date: clip(q.Date, 10), Description: clip(q.Description, 200)})
		}
	case "prepare_operation":
		var q struct {
			Arguments struct {
				Description string `json:"description"`
			} `json:"arguments"`
		}
		_ = json.Unmarshal(input, &q)
		s.note(scope, local.AgentActivity{Kind: "review", Description: clip(q.Arguments.Description, 200)})
	case "get_trial_balance":
		s.note(scope, local.AgentActivity{Kind: "balanced"})
	case "get_balance":
		s.note(scope, local.AgentActivity{Kind: "compared"})
	}
}

// agentTool answers the tools the service provides itself rather than the
// ledger: waiting for the owner's next original, and reporting one the agent
// can't use.
func (s *Server) agentTool(ctx context.Context, scope ledger.Scope, method string, input json.RawMessage) (json.RawMessage, error, bool) {
	switch method {
	case local.WaitForDocumentMethod:
		var q struct {
			Seconds int `json:"seconds,omitempty"`
		}
		if strictjson.Decode(input, &q) != nil || q.Seconds < 0 || q.Seconds > int(maxAgentWait/time.Second) {
			return nil, ledger.ErrInvalid, true
		}
		wait := maxAgentWait
		if q.Seconds > 0 {
			wait = time.Duration(q.Seconds) * time.Second
		}
		out, err := s.waitForDocument(ctx, scope, wait)
		return out, err, true
	case local.ReportUnusableMethod:
		if scope.Access != "write" {
			return nil, ledger.ErrDenied, true
		}
		var q struct {
			DocumentID string `json:"document_id"`
			Reason     string `json:"reason"`
		}
		if strictjson.Decode(input, &q) != nil || strings.TrimSpace(q.Reason) == "" || len(q.Reason) > 1000 {
			return nil, ledger.ErrInvalid, true
		}
		query := marshal(struct {
			DocumentID string `json:"document_id"`
		}{q.DocumentID})
		metadata, err := s.Ledger.Call(ctx, scope, "get_document", query)
		if err != nil {
			return nil, err, true
		}
		var inspection ledger.DocumentInspection
		if json.Unmarshal(metadata, &inspection) != nil {
			return nil, ledger.ErrNotFound, true
		}
		s.note(scope, local.AgentActivity{Kind: "unusable", Document: clip(inspection.Document.Name, 255), Reason: clip(q.Reason, 300)})
		return marshal(struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}{"reported", "The owner sees your reason in Vult and can upload another file. Record nothing from this one."}), nil, true
	}
	return nil, nil, false
}

// waitForDocument returns the first ready original no entry uses yet, or
// none after the wait, so the agent can call again.
func (s *Server) waitForDocument(ctx context.Context, scope ledger.Scope, wait time.Duration) (json.RawMessage, error) {
	deadline := time.Now().Add(wait)
	for {
		found, err := s.unusedDocument(ctx, scope)
		if err != nil {
			return nil, err
		}
		if found != nil {
			return marshal(struct {
				Status   string          `json:"status"`
				Document json.RawMessage `json:"document"`
			}{"ready", found}), nil
		}
		if !time.Now().Add(agentPoll).Before(deadline) {
			return marshal(struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			}{"none_yet", "No new original yet. Call wait_for_document again while the owner is uploading, or stop and tell the owner you're ready when they are."}), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(agentPoll):
		}
	}
}

func (s *Server) unusedDocument(ctx context.Context, scope ledger.Scope) (json.RawMessage, error) {
	after := int64(0)
	for range 20 {
		page, err := s.Ledger.Call(ctx, scope, "list_documents", marshal(struct {
			After int64 `json:"after,omitempty"`
			Limit int   `json:"limit"`
		}{after, 100}))
		if err != nil {
			return nil, err
		}
		var q struct {
			Documents []json.RawMessage `json:"documents"`
			NextAfter int64             `json:"next_after"`
			HasMore   bool              `json:"has_more"`
		}
		if json.Unmarshal(page, &q) != nil {
			return nil, ledger.ErrInvalid
		}
		for _, raw := range q.Documents {
			var d struct {
				State  string  `json:"state"`
				UsedAt *string `json:"used_at"`
			}
			if json.Unmarshal(raw, &d) == nil && d.State == "ready" && (d.UsedAt == nil || *d.UsedAt == "") {
				return raw, nil
			}
		}
		if !q.HasMore || q.NextAfter <= after {
			return nil, nil
		}
		after = q.NextAfter
	}
	return nil, nil
}

// ownerActivity returns a Book's events after a sequence number to the
// owner's browser, with the agents waiting for the owner's approval.
func (s *Server) ownerActivity(ctx context.Context, session string, input json.RawMessage) (json.RawMessage, error) {
	var q local.ActivityQuery
	if strictjson.Decode(input, &q) != nil || !ledger.ValidID(q.BookID) || q.After < 0 {
		return nil, ledger.ErrInvalid
	}
	if err := s.Browser.documents.OwnsBook(ctx, session, q.BookID); err != nil {
		return nil, ledger.ErrDenied
	}
	out := local.ActivityPage{Events: []local.AgentActivity{}, Waiting: []local.WaitingAgent{}, Connected: []string{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	if book := s.activity[q.BookID]; book != nil {
		for _, e := range book.events {
			if e.Seq > q.After {
				out.Events = append(out.Events, e)
			}
		}
		out.Next = book.next
	}
	s.pruneAgentsLocked(time.Now())
	for id, r := range s.agents {
		if r.state == local.AgentWaiting {
			out.Waiting = append(out.Waiting, local.WaitingAgent{Request: id, Client: r.client, CreatedAt: r.created.UTC()})
		}
	}
	now := time.Now()
	seen := map[string]bool{}
	for _, live := range s.sessions {
		if live.Scope.BookID != q.BookID || !now.Before(live.Expires) {
			continue
		}
		name := s.agentNames[live.Scope.ConnectionID]
		if name == "" {
			name = "Your agent"
		}
		if !seen[name] {
			seen[name] = true
			out.Connected = append(out.Connected, name)
		}
	}
	sort.Strings(out.Connected)
	sort.Slice(out.Waiting, func(i, j int) bool { return out.Waiting[i].CreatedAt.Before(out.Waiting[j].CreatedAt) })
	return marshal(out), nil
}
