package agentservice

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vult-ai/vult/agentconfig"
	"github.com/vult-ai/vult/internal/mcpserver"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

// Config is what a Client needs.
type Config struct {
	// Service is the agent service turns go to. Required.
	Service *Service
	// Agents holds each Book's agent settings: whether it is on, and the
	// proposing connection whose grant its tools run under. Required; an
	// *agentconfig.Service, such as server.OpenAgent returns, is one.
	Agents Agents
	// Store runs the tools the service asks for.
	Store ledger.Store
	// Conversations keeps what was said in each Book. Nil keeps it in
	// memory until the program stops.
	Conversations Conversations
	// Person, when set, names who a tenant's turns are for: the account the
	// service knows them by, and the plan they are on, if any. A plan is
	// sent with each turn when it isn't empty. An error, or an empty
	// account, refuses the turn before the service is asked anything; an
	// error that is a server.ChatUnavailable is shown to the person as it
	// reads. Nil sends the tenant's ID as the account and no plan.
	Person func(ctx context.Context, tenant string) (account, plan string, err error)
}

// Agents reads a Book's agent settings.
type Agents interface {
	Load(ctx context.Context, tenant, book string) (agentconfig.Settings, error)
}

// Client passes what a person writes to the agent service and does what it
// asks, within the grant of the Book's own agent connection: the one the
// owner turned on, which may only prepare changes for approval. It
// implements server.AgentChat.
type Client struct {
	config  Agents
	store   ledger.Store
	service *Service
	kept    Conversations
	person  func(ctx context.Context, tenant string) (account, plan string, err error)

	// One turn at a time in a Book, so its conversation keeps its order.
	mu    sync.Mutex
	books map[string]*bookTurns
}

var _ server.AgentChat = (*Client)(nil)

// NewClient returns a Client for a configuration.
func NewClient(c Config) *Client {
	kept := c.Conversations
	if kept == nil {
		kept = &Memory{}
	}
	return &Client{config: c.Agents, store: c.Store, service: c.Service, kept: kept, person: c.Person}
}

// keptTurns is how many turns of a conversation are kept to show.
const keptTurns = 400

// A turn is bounded whatever the service sends: how many times it may ask
// for tools, how many at once, and how much it may say.
const (
	maxSteps        = 6000
	maxCallsPerStep = 16
	maxAnswerText   = 16000
	maxReplies      = 3
	maxReplyText    = 40
	maxShown        = 200
	maxProblemText  = 300
)

// short cuts text to a number of characters.
func short(text string, most int) string {
	if r := []rune(text); len(r) > most {
		return string(r[:most])
	}
	return text
}

// bookTurns is one Book's turns: the lock that orders them, and whether
// new activity is being sorted or has arrived since that began.
type bookTurns struct {
	turn sync.Mutex
	// asked counts this Book's turns, running or waiting their turn.
	asked          atomic.Int32
	sorting, again bool
	generation     uint64 // protected by Client.mu
	// left are the kinds of activity sorting left for the owner, as the
	// service described them, oldest first. They are sent back with each
	// catch-up so the service doesn't ask its model about them again.
	left     []json.RawMessage // protected by Client.mu
	leftSeen map[string]bool   // protected by Client.mu
}

const (
	// maxLeftKinds is the most kinds left for the owner a Book remembers;
	// past it the oldest are asked about again.
	maxLeftKinds = 2000
	// maxLeftKind is the longest one kind's description may be.
	maxLeftKind = 1400
)

// noteLeft keeps the kinds a step says sorting left for the owner.
func (c *Client) noteLeft(b *bookTurns, s step) {
	for _, e := range s.Events {
		if e.Name != "left_for_owner" {
			continue
		}
		var d struct {
			Kinds []json.RawMessage `json:"kinds"`
		}
		if json.Unmarshal(e.Data, &d) != nil {
			continue
		}
		c.mu.Lock()
		for _, k := range d.Kinds {
			key := string(k)
			if len(k) == 0 || len(k) > maxLeftKind || b.leftSeen[key] {
				continue
			}
			if b.leftSeen == nil {
				b.leftSeen = map[string]bool{}
			}
			b.leftSeen[key] = true
			b.left = append(b.left, append(json.RawMessage(nil), k...))
			if n := len(b.left); n > maxLeftKinds {
				for _, old := range b.left[:n-maxLeftKinds] {
					delete(b.leftSeen, string(old))
				}
				b.left = append([]json.RawMessage(nil), b.left[n-maxLeftKinds:]...)
			}
		}
		c.mu.Unlock()
	}
}

func (c *Client) book(tenant, book string) *bookTurns {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.books == nil {
		c.books = map[string]*bookTurns{}
	}
	b := c.books[tenant+"/"+book]
	if b == nil {
		b = &bookTurns{}
		c.books[tenant+"/"+book] = b
	}
	return b
}

// unrecorded is how many of a Book's bank items wait to be recorded, as
// its agent sees them. It reports false when the agent is off.
func (c *Client) unrecorded(ctx context.Context, tenant, book string) (int, bool) {
	settings, err := c.config.Load(ctx, tenant, book)
	if err != nil || !settings.Enabled {
		return 0, false
	}
	out, err := c.store.Call(ctx, settings.Scope, "list_bank_activity", []byte(`{"status":"unrecorded","limit":1}`))
	if err != nil {
		return 0, false
	}
	var page struct {
		Summary struct {
			Unrecorded int `json:"unrecorded"`
		} `json:"summary"`
	}
	if json.Unmarshal(out, &page) != nil {
		return 0, false
	}
	return page.Summary.Unrecorded, true
}

// keepUpWait lets a sync's pages settle before new activity is sorted.
const keepUpWait = 20 * time.Second

// KeepUp sorts a Book's new bank activity without being asked, when its
// agent is on, by starting a "catch_up" turn. One sort runs at a time in a
// Book; activity that arrives while it runs gets one more sort after it. It
// returns at once.
func (c *Client) KeepUp(tenant, book string) {
	b := c.book(tenant, book)
	c.mu.Lock()
	if b.sorting {
		b.again = true
		c.mu.Unlock()
		return
	}
	b.sorting = true
	c.mu.Unlock()
	go func() {
		for {
			time.Sleep(keepUpWait)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			// An arrival can replace an item the owner just recorded, so
			// its count need not grow. The notification is the reason to
			// check; an empty inbox needs no work.
			if n, ok := c.unrecorded(ctx, tenant, book); ok && n > 0 {
				if err := c.turn(ctx, tenant, book, "catch_up", "", "", func(string, any) {}); err != nil && err != errAgentOff {
					slog.Warn("new bank activity wasn't sorted", "error", err.Error())
				}
			}
			cancel()
			c.mu.Lock()
			if !b.again {
				b.sorting = false
				c.mu.Unlock()
				return
			}
			b.again = false
			c.mu.Unlock()
		}
	}()
}

// ActivityArrived sorts the Book's new activity as KeepUp does. It fits
// server.Options.ActivityArrived.
func (c *Client) ActivityArrived(_ context.Context, t ledger.AgentTarget, _ server.OwnerAct) {
	c.KeepUp(t.TenantID, t.BookID)
}

var errAgentOff = server.ChatUnavailable{Reason: "Vult is off in this Book.", Code: "agent_off"}

// errNoPerson is why a turn is refused when Person names no one.
var errNoPerson = server.ChatUnavailable{Reason: "Vult isn’t available for this account right now."}

// Turns is the Book's conversation so far, oldest first.
func (c *Client) Turns(_ context.Context, tenant, book string) ([]server.ChatTurn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	talk, err := c.kept.Load(tenant, book)
	return talk.Turns, err
}

// Forget deletes the Book's conversation.
func (c *Client) Forget(_ context.Context, tenant, book string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.kept.Forget(tenant, book); err != nil {
		return err
	}
	if b := c.books[tenant+"/"+book]; b != nil {
		b.generation++
	}
	return nil
}

// Chat sends one message to the agent service as a "message" turn.
func (c *Client) Chat(ctx context.Context, tenant, book, message string, event func(string, any)) error {
	return c.turn(ctx, tenant, book, "message", message, message, event)
}

// catchUpAsked is what the owner's request reads as in the conversation.
const catchUpAsked = "Sort my bank items"

// CatchUp asks the agent service to sort everything waiting in the Book, as
// a "catch_up" turn.
func (c *Client) CatchUp(ctx context.Context, tenant, book string, event func(string, any)) error {
	return c.turn(ctx, tenant, book, "catch_up", "", catchUpAsked, event)
}

// refusals are the ledger saying no to a request. Anything else, such as a
// lost database connection, is a failure.
var refusals = []error{ledger.ErrInvalid, ledger.ErrDenied, ledger.ErrConflict, ledger.ErrNotFound,
	ledger.ErrProposalStale, ledger.ErrApprovalRequired, ledger.ErrProposalExpired, ledger.ErrProposalDecided,
	// As many changes as may wait for the owner already do.
	ledger.ErrLimit}

type toolResult struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Refused bool   `json:"refused,omitempty"`
	Failed  bool   `json:"failed,omitempty"`
}

// run does one tool the service asked for, as the Book's agent connection.
// The ledger checks that connection's grant in the same transaction, so the
// service can ask for nothing the owner didn't allow.
func (c *Client) run(ctx context.Context, scope ledger.Scope, offered map[string]bool, id, tool string, input json.RawMessage) toolResult {
	// Only a tool this turn offered is run, whatever else the grant allows.
	if !offered[tool] || !mcpserver.Allowed(tool, scope.Access) {
		return toolResult{ID: id, Content: "This agent's access doesn't include " + tool + ".", Refused: true}
	}
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	out, err := c.store.Call(ctx, scope, tool, input)
	if err == nil {
		return toolResult{ID: id, Content: string(out)}
	}
	for _, refusal := range refusals {
		if errors.Is(err, refusal) {
			return toolResult{ID: id, Content: err.Error(), Refused: true}
		}
	}
	slog.Warn("agent tool failed", "tool", tool, "error", err.Error())
	return toolResult{ID: id, Failed: true}
}

// takeEvents keeps and passes on what a step says to show the owner, within
// bounds: text of a limited length, a few short replies, and only proposals
// this turn's own calls prepared.
func takeEvents(answer *server.ChatTurn, prepared map[string]bool, s step, send func(string, any)) {
	for _, e := range s.Events {
		var d struct {
			Text       string   `json:"text"`
			ProposalID string   `json:"proposal_id"`
			Summary    string   `json:"summary"`
			Then       string   `json:"then"`
			Options    []string `json:"options"`
		}
		_ = json.Unmarshal(e.Data, &d)
		switch e.Name {
		case "delta":
			if len(answer.Text) >= maxAnswerText {
				continue
			}
			d.Text = short(d.Text, maxAnswerText-len(answer.Text))
			if answer.Text != "" {
				answer.Text += "\n"
			}
			answer.Text += d.Text
			e.Data, _ = json.Marshal(map[string]string{"text": d.Text})
		case "remembered":
			if len(answer.Remembered) >= maxShown {
				continue
			}
			d.Text = short(d.Text, maxProblemText)
			answer.Remembered = append(answer.Remembered, d.Text)
			e.Data, _ = json.Marshal(map[string]string{"text": d.Text})
		case "proposal":
			if !prepared[d.ProposalID] || len(answer.Proposals) >= maxShown {
				continue
			}
			shown := map[string]string{"proposal_id": d.ProposalID, "summary": short(d.Summary, maxProblemText)}
			answer.Proposals = append(answer.Proposals, server.ChatProposal{ProposalID: d.ProposalID, Summary: shown["summary"]})
			if d.Then == "catch_up" {
				answer.Then, shown["then"] = d.Then, d.Then
			}
			e.Data, _ = json.Marshal(shown)
		case "replies":
			answer.Replies = nil
			for _, option := range d.Options {
				if option = strings.TrimSpace(option); option != "" && len(answer.Replies) < maxReplies {
					answer.Replies = append(answer.Replies, short(option, maxReplyText))
				}
			}
			e.Data, _ = json.Marshal(map[string]any{"options": answer.Replies})
		case "activity":
		default:
			continue
		}
		send(e.Name, e.Data)
	}
}

// turnStart is the request that starts a turn.
type turnStart struct {
	Account string `json:"account"`
	// Plan is sent only when Person names one.
	Plan    string           `json:"plan,omitempty"`
	Book    string           `json:"book"`
	Kind    string           `json:"kind"`
	Text    string           `json:"text"`
	Tools   []mcpserver.Tool `json:"tools"`
	History []historyMessage `json:"history,omitempty"`
	// LeftForOwner are kinds earlier catch-ups left for the owner.
	LeftForOwner []json.RawMessage `json:"left_for_owner,omitempty"`
}

// whose is who a tenant's turns are for.
func (c *Client) whose(ctx context.Context, tenant string) (account, plan string, err error) {
	if c.person == nil {
		return tenant, "", nil
	}
	account, plan, err = c.person(ctx, tenant)
	if err != nil {
		slog.Warn("agent turn refused", "error", err.Error())
		var u server.ChatUnavailable
		if errors.As(err, &u) && u.Reason != "" {
			return "", "", server.ChatUnavailable{Reason: short(u.Reason, maxProblemText)}
		}
		return "", "", errNoPerson
	}
	if account == "" {
		return "", "", errNoPerson
	}
	return account, plan, nil
}

func (c *Client) turn(ctx context.Context, tenant, book, kind, text, asked string, send func(string, any)) error {
	settings, err := c.config.Load(ctx, tenant, book)
	if errors.Is(err, ledger.ErrNotFound) || err == nil && !settings.Enabled {
		return errAgentOff
	}
	if err != nil {
		return err
	}
	// One turn at a time in a Book. A turn that waits gives up when its
	// request does, so waiting turns can't pile up behind a slow one.
	turns := c.book(tenant, book)
	c.mu.Lock()
	generation := turns.generation
	c.mu.Unlock()
	busy := server.ChatUnavailable{Reason: "Vult is still working on the last thing you asked. Try again in a moment."}
	if turns.asked.Add(1) > 2 {
		// One running and one waiting is all a Book holds.
		turns.asked.Add(-1)
		return busy
	}
	defer turns.asked.Add(-1)
	for !turns.turn.TryLock() {
		select {
		case <-ctx.Done():
			return busy
		case <-time.After(200 * time.Millisecond):
		}
	}
	defer turns.turn.Unlock()
	c.mu.Lock()
	if turns.generation != generation {
		c.mu.Unlock()
		return server.ChatUnavailable{Reason: "The conversation was cleared. Send your message again."}
	}
	conversation, loadErr := c.kept.Load(tenant, book)
	c.mu.Unlock()
	if loadErr != nil {
		return server.ChatUnavailable{Reason: "The conversation couldn’t be loaded. Try again in a moment."}
	}
	answer := server.ChatTurn{ID: ledger.ID(), Role: "agent"}
	account, plan, refused := c.whose(ctx, tenant)
	if refused != nil {
		c.keep(tenant, book, conversation, generation, asked, answer, refused)
		return refused
	}
	// prepared is the proposals this turn's own tool calls made. The
	// service can show the owner those and no others.
	prepared := map[string]bool{}
	show := func(s step) {
		c.noteLeft(turns, s)
		c.mu.Lock()
		current := turns.generation == generation
		c.mu.Unlock()
		if current {
			takeEvents(&answer, prepared, s, send)
		}
	}
	tools := mcpserver.Tools(settings.Scope.Access)
	offered := map[string]bool{}
	for _, tool := range tools {
		offered[tool.Name] = true
	}
	start := turnStart{Account: account, Plan: plan, Book: book, Kind: kind, Text: text, Tools: tools, History: recentHistory(conversation)}
	if kind == "catch_up" {
		c.mu.Lock()
		start.LeftForOwner = append([]json.RawMessage(nil), turns.left...)
		c.mu.Unlock()
	}
	s, err := c.service.post(ctx, "/v1/turns", start)
	for n := 0; err == nil && !s.Done; n++ {
		if n >= maxSteps || len(s.Calls) > maxCallsPerStep {
			err = errors.New("agent service: too many steps")
			break
		}
		// What a step shows comes from the calls before it, so a proposal
		// it names is one this turn already made.
		show(s)
		results := make([]toolResult, 0, len(s.Calls))
		for _, call := range s.Calls {
			result := c.run(ctx, settings.Scope, offered, call.ID, call.Tool, call.Input)
			if call.Tool == "prepare_operation" && !result.Refused && !result.Failed {
				var made struct {
					ID       string `json:"id"`
					Proposal struct {
						ID string `json:"id"`
					} `json:"proposal"`
				}
				if json.Unmarshal([]byte(result.Content), &made) == nil {
					prepared[made.ID], prepared[made.Proposal.ID] = true, true
					delete(prepared, "")
				}
			}
			results = append(results, result)
		}

		s, err = c.service.post(ctx, "/v1/turns/"+url.PathEscape(s.Turn)+"/results", map[string]any{"results": results})
	}
	var out error
	switch {
	case err != nil:
		slog.Warn("agent turn failed", "error", err.Error())
		out = server.ChatUnavailable{Reason: "Vult couldn’t be reached. Try again in a moment."}
	default:
		show(s)
		if s.Problem != "" {
			out = server.ChatUnavailable{Reason: short(s.Problem, maxProblemText)}
		}
	}
	c.keep(tenant, book, conversation, generation, asked, answer, out)
	return out
}

// keep adds an exchange to the Book's conversation. A message that got no
// answer is kept too, with why, so the page shows the same conversation
// after a reload.
func (c *Client) keep(tenant, book string, kept Conversation, generation uint64, asked string, answer server.ChatTurn, failed error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if b := c.books[tenant+"/"+book]; b == nil || b.generation != generation {
		return
	}
	if failed != nil {
		answer.Problem = "That didn’t go through."
		if u, ok := failed.(server.ChatUnavailable); ok {
			answer.Problem = u.Reason
		}
	}
	now := time.Now().UTC()
	answer.At = now
	if asked == "" {
		// Work nobody asked for: kept only when it has something to say.
		if failed != nil || answer.Text == "" && len(answer.Proposals) == 0 {
			return
		}
		kept.Turns = append(kept.Turns, answer)
	} else {
		kept.Turns = append(kept.Turns, server.ChatTurn{ID: ledger.ID(), Role: "person", Text: asked, At: now}, answer)
	}
	if n := len(kept.Turns); n > keptTurns {
		kept.Turns = kept.Turns[n-keptTurns:]
	}
	save := c.kept.Save
	if conditional, ok := c.kept.(ConditionalConversations); ok {
		save = conditional.SaveIfUnchanged
	}
	if err := save(tenant, book, kept); err != nil && !errors.Is(err, ErrConversationChanged) {
		slog.Warn("conversation not kept", "error", err.Error())
	}
}
