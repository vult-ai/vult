package agentservice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/vult-ai/vult/agentconfig"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

// agentOn says every Book's agent is on, with a proposing connection.
type agentOn struct{}

func (agentOn) Load(_ context.Context, tenant, book string) (agentconfig.Settings, error) {
	return agentconfig.Settings{Enabled: true, Scope: ledger.Scope{TenantID: tenant, BookID: book, Access: "propose"}}, nil
}

// standIn is an agent service that ends every turn at once and keeps what
// each turn began with.
type standIn struct {
	mu     sync.Mutex
	starts []map[string]any
	auth   []string
}

func (s *standIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/turns" {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.starts, s.auth = append(s.starts, body), append(s.auth, r.Header.Get("Authorization"))
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"turn": "t1", "done": true, "events": []map[string]any{{"name": "delta", "data": map[string]string{"text": "Done."}}}})
}

func clientFor(t *testing.T, person func(context.Context, string) (string, string, error)) (*Client, *standIn) {
	t.Helper()
	stand := &standIn{}
	ts := httptest.NewServer(stand)
	t.Cleanup(ts.Close)
	service, err := NewService(ts.URL, "service-token")
	if err != nil {
		t.Fatal(err)
	}
	return NewClient(Config{Service: service, Agents: agentOn{}, Person: person}), stand
}

func TestWithoutPersonTheTurnIsForTheTenant(t *testing.T) {
	c, stand := clientFor(t, nil)
	if err := c.Chat(t.Context(), "tenant-1", "book-1", "hello", func(string, any) {}); err != nil {
		t.Fatal(err)
	}
	if len(stand.starts) != 1 {
		t.Fatalf("turns started: %d", len(stand.starts))
	}
	got := stand.starts[0]
	if got["account"] != "tenant-1" || got["book"] != "book-1" || got["kind"] != "message" || got["text"] != "hello" {
		t.Fatalf("turn began with %v", got)
	}
	if _, ok := got["plan"]; ok {
		t.Fatalf("a plan was sent: %v", got["plan"])
	}
	if _, ok := got["tools"].([]any); !ok {
		t.Fatalf("no tools were offered: %v", got["tools"])
	}
	if stand.auth[0] != "Bearer service-token" {
		t.Fatalf("credential: %q", stand.auth[0])
	}
}

func TestPersonNamesTheAccountAndPlan(t *testing.T) {
	var asked []string
	c, stand := clientFor(t, func(_ context.Context, tenant string) (string, string, error) {
		asked = append(asked, tenant)
		return "account-7", "standard", nil
	})
	if err := c.CatchUp(t.Context(), "tenant-1", "book-1", func(string, any) {}); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "tenant-1" {
		t.Fatalf("Person was asked about %v", asked)
	}
	got := stand.starts[0]
	if got["account"] != "account-7" || got["plan"] != "standard" || got["kind"] != "catch_up" {
		t.Fatalf("turn began with %v", got)
	}
}

func TestPersonWithoutAPlanSendsNoPlan(t *testing.T) {
	c, stand := clientFor(t, func(context.Context, string) (string, string, error) { return "account-7", "", nil })
	if err := c.Chat(t.Context(), "tenant-1", "book-1", "hello", func(string, any) {}); err != nil {
		t.Fatal(err)
	}
	if _, ok := stand.starts[0]["plan"]; ok || stand.starts[0]["account"] != "account-7" {
		t.Fatalf("turn began with %v", stand.starts[0])
	}
}

func TestAPersonErrorRefusesTheTurnBeforeTheService(t *testing.T) {
	for name, tc := range map[string]struct {
		account string
		err     error
		reason  string
	}{
		"failure":      {err: errors.New("lookup failed: connection reset"), reason: errNoPerson.Reason},
		"plain reason": {err: server.ChatUnavailable{Reason: "This account can't use Vult's agent."}, reason: "This account can't use Vult's agent."},
		"no account":   {reason: errNoPerson.Reason},
	} {
		c, stand := clientFor(t, func(context.Context, string) (string, string, error) { return tc.account, "standard", tc.err })
		err := c.Chat(t.Context(), "tenant-1", "book-1", "hello", func(string, any) {})
		var u server.ChatUnavailable
		if !errors.As(err, &u) || u.Reason != tc.reason {
			t.Fatalf("%s: %v", name, err)
		}
		if len(stand.starts) != 0 {
			t.Fatalf("%s: the service was asked: %v", name, stand.starts)
		}
		turns, _ := c.Turns(t.Context(), "tenant-1", "book-1")
		if len(turns) != 2 || turns[1].Problem != tc.reason {
			t.Fatalf("%s: conversation %+v", name, turns)
		}
	}
}
