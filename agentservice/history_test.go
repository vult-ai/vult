package agentservice

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vult-ai/vult/server"
)

func TestEachTurnSendsTheSavedConversation(t *testing.T) {
	c, stand := clientFor(t, nil)
	chat := func(text string) {
		t.Helper()
		if err := c.Chat(t.Context(), "tenant", "book", text, func(string, any) {}); err != nil {
			t.Fatal(err)
		}
	}
	chat("The synthetic project is Moonstone.")
	// A fresh client has no conversation state except its shared store.
	c = NewClient(Config{Service: c.service, Agents: agentOn{}, Conversations: c.kept})
	chat("Which project?")
	raw, _ := json.Marshal(stand.starts[1]["history"])
	if !strings.Contains(string(raw), "Moonstone") || !strings.Contains(string(raw), "Done.") {
		t.Fatalf("saved history missing: %s", raw)
	}
	if err := c.Forget(t.Context(), "tenant", "book"); err != nil {
		t.Fatal(err)
	}
	chat("Start again.")
	if history := stand.starts[2]["history"]; history != nil {
		t.Fatalf("cleared history sent: %v", history)
	}
}

func TestClearingConversationDiscardsRunningAndQueuedReplies(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"done":true,"events":[{"name":"delta","data":{"text":"Old reply"}}]}`))
	}))
	t.Cleanup(ts.Close)
	s, err := NewService(ts.URL, "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(Config{Service: s, Agents: agentOn{}})
	first, queued := make(chan error, 1), make(chan error, 1)
	go func() { first <- c.Chat(t.Context(), "t", "b", "Old question", func(string, any) {}) }()
	<-started
	go func() { queued <- c.Chat(t.Context(), "t", "b", "Queued question", func(string, any) {}) }()
	deadline := time.After(2 * time.Second)
	for c.book("t", "b").asked.Load() != 2 {
		select {
		case <-deadline:
			t.Fatal("second turn did not queue")
		case <-time.After(time.Millisecond):
		}
	}
	if err := c.Forget(t.Context(), "t", "b"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-queued; err == nil {
		t.Fatal("a queued old question was submitted after clearing")
	}
	if turns, err := c.Turns(t.Context(), "t", "b"); err != nil || len(turns) != 0 {
		t.Fatalf("cleared conversation restored: %+v %v", turns, err)
	}
}

func TestAnOldWriterCannotRestoreAClearedConversation(t *testing.T) {
	for _, initiallyEmpty := range []bool{false, true} {
		var kept Memory
		if !initiallyEmpty {
			if err := kept.Save("t", "b", Conversation{Turns: []server.ChatTurn{{Text: "Old question"}}}); err != nil {
				t.Fatal(err)
			}
		}
		old, _ := kept.Load("t", "b")
		if err := kept.Forget("t", "b"); err != nil {
			t.Fatal(err)
		}
		old.Turns = append(old.Turns, server.ChatTurn{Text: "Late answer"})
		if err := kept.SaveIfUnchanged("t", "b", old); !errors.Is(err, ErrConversationChanged) {
			t.Fatalf("late answer: %v", err)
		}
		fresh, _ := kept.Load("t", "b")
		fresh.Turns = append(fresh.Turns, server.ChatTurn{Text: "New question"})
		if err := kept.SaveIfUnchanged("t", "b", fresh); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryIsBoundedVisibleText(t *testing.T) {
	c := Conversation{}
	for i := 0; i < 50; i++ {
		c.Turns = append(c.Turns, server.ChatTurn{Role: "person", Text: "hello"})
	}
	c.Turns = append(c.Turns, server.ChatTurn{Role: "system", Text: "not authority"}, server.ChatTurn{Role: "agent", Text: "failed answer", Problem: "failed"})
	if got := recentHistory(c); len(got) != 40 || got[39].Role != "user" {
		t.Fatalf("unexpected history: %+v", got)
	}
	c.Turns = []server.ChatTurn{{Role: "person", Text: strings.Repeat("界", 9000)}, {Role: "agent", Text: strings.Repeat("é", 10000)}}
	got := recentHistory(c)
	if len(got) != 2 || len(got[0].Text)+len(got[1].Text) > 32000 {
		t.Fatal("history exceeded byte limit")
	}
	for _, m := range got {
		if !utf8.ValidString(m.Text) || len(m.Text) > 16000 {
			t.Fatal("invalid truncated text")
		}
	}
}
