package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vult-ai/vult/ledger"
)

const agentChatPath = "/api/v1/agent-chat"

// agentChatTimeout is the longest one answer may take.
const agentChatTimeout = 15 * time.Minute

// AgentChat answers what a person writes to a Book's agent. It reports its
// work as events, in order: "activity" for what it is doing, "replies" for short answers the person
// might tap next, "delta" for
// reply text, "proposal" when it prepared a change for the owner to approve,
// and "question" when it asked the owner something. The route adds the first
// and last events itself. The agent acts only through its own connection
// and grant; nothing here lets it change a Book.
type AgentChat interface {
	Chat(ctx context.Context, tenant, book, message string, event func(name string, data any)) error
	// CatchUp has the agent sort everything waiting in the Book in one run,
	// and reports what it did as Chat does.
	CatchUp(ctx context.Context, tenant, book string, event func(name string, data any)) error
	// Turns is the Book's conversation so far, oldest first.
	Turns(ctx context.Context, tenant, book string) ([]ChatTurn, error)
	// Forget deletes the Book's conversation; it is the owner's.
	Forget(ctx context.Context, tenant, book string) error
}

// ChatTurn is one thing said in a Book's conversation: by the person, or
// by the agent with what it prepared.
type ChatTurn struct {
	ID        string         `json:"id"`
	Role      string         `json:"role"` // "person" or "agent"
	Text      string         `json:"text"`
	At        time.Time      `json:"at"`
	Proposals []ChatProposal `json:"proposals,omitempty"`
	// Then names the job that goes on once what this turn prepared is
	// approved: "catch_up" where sorting waits for the Book's chart.
	Then string `json:"then,omitempty"`
	// Remembered is what the agent noted about the person in this turn.
	Remembered []string `json:"remembered,omitempty"`
	// Replies are short answers the agent offered the person to tap.
	Replies []string `json:"replies,omitempty"`
	// Problem is why the agent could not answer, where it could not.
	Problem string `json:"problem,omitempty"`
}

// ChatProposal is a change the agent prepared in a turn.
type ChatProposal struct {
	ProposalID string `json:"proposal_id"`
	Summary    string `json:"summary"`
}

// agentConversation reads a Book's conversation, or deletes it.
func (a *webAPI) agentConversation(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	book := q.Get("book_id")
	if len(q) != 1 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, ok := a.bankBook(w, r, session, book)
	if !ok {
		return
	}
	turns := []ChatTurn{}
	if a.chat != nil {
		var err error
		if r.Method == http.MethodDelete {
			err = a.chat.Forget(r.Context(), tenant, book)
		} else if turns, err = a.chat.Turns(r.Context(), tenant, book); turns == nil {
			turns = []ChatTurn{}
		}
		if err != nil {
			apiFailure(w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "turns": turns})
}

// ChatUnavailable says the agent could not answer just now, in words the
// owner can read; any other error is reported without its text. Code, when
// set, lets the page offer the way out, as "agent_off" offers to turn the
// agent on for the Book.
type ChatUnavailable struct{ Reason, Code string }

func (e ChatUnavailable) Error() string { return e.Reason }

// agentChat takes one message for the Book's agent and streams its answer.
// A Vult with no agent to talk to answers agent_off before any stream starts.
func (a *webAPI) agentChat(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BookID  string `json:"book_id"`
		Message string `json:"message"`
		// Job asks for a whole piece of work instead of a message:
		// "catch_up" sorts everything waiting.
		Job string `json:"job"`
	}
	if !readRuleBody(w, r, &q) {
		return
	}
	message := strings.TrimSpace(q.Message)
	if n := len([]rune(message)); q.Job == "" && (n < 1 || n > 4000) || q.Job != "" && (q.Job != "catch_up" || n != 0) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	if a.chat == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "error": "agent_off", "message": "Set up Vult’s AI first, in Settings."})
		return
	}
	control := http.NewResponseController(w)
	// An answer takes longer than the server gives one request to be read.
	// Past that limit the request would be cancelled while the agent works.
	_ = control.SetReadDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// One writer at a time: events, and a comment line every few seconds so
	// the stream stays open while the agent works.
	var mu sync.Mutex
	write := func(text string) {
		mu.Lock()
		defer mu.Unlock()
		_ = control.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := io.WriteString(w, text); err == nil {
			_ = control.Flush()
		}
	}
	send := func(name string, data any) {
		body, err := json.Marshal(data)
		if err != nil {
			return
		}
		write(fmt.Sprintf("event: %s\ndata: %s\n\n", name, body))
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		beat := time.NewTicker(5 * time.Second)
		defer beat.Stop()
		for {
			select {
			case <-beat.C:
				write(": working\n\n")
			case <-stop:
				return
			}
		}
	}()
	turn := fmt.Sprintf("%d", time.Now().UnixNano())
	send("turn", map[string]string{"turn_id": turn})
	work := func() error { return a.chat.Chat(r.Context(), tenant, q.BookID, message, send) }
	if q.Job != "" {
		work = func() error { return a.chat.CatchUp(r.Context(), tenant, q.BookID, send) }
	}
	if err := work(); err != nil {
		reason, code := "That didn’t go through. Try again in a moment.", "unavailable"
		if u, ok := err.(ChatUnavailable); ok && u.Reason != "" {
			reason = u.Reason
			if u.Code != "" {
				code = u.Code
			}
		}
		send("error", map[string]string{"code": code, "message": reason})
		return
	}
	send("done", map[string]string{"turn_id": turn})
}
