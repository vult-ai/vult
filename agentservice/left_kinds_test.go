package agentservice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Kinds sorting left for the owner are sent back with the next sort, in
// that Book only, so the service doesn't ask its model about them again.
func TestKindsLeftForTheOwnerAreSentBackWithTheNextSort(t *testing.T) {
	var mu sync.Mutex
	var starts []map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		mu.Lock()
		starts = append(starts, got)
		n := len(starts)
		mu.Unlock()
		// Each sort leaves one new kind, and repeats one it left before.
		fmt.Fprintf(w, `{"done":true,"events":[{"name":"left_for_owner","data":{"kinds":[{"bank_account":"card","direction":"out","counterparty":"synthetic lender %d"},{"bank_account":"card","direction":"out","counterparty":"synthetic lender 1"}]}}]}`, n)
	}))
	t.Cleanup(ts.Close)
	s, err := NewService(ts.URL, "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(Config{Service: s, Agents: agentOn{}})
	for range 2 {
		if err := c.CatchUp(t.Context(), "tenant", "book", func(string, any) {}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.CatchUp(t.Context(), "tenant", "other-book", func(string, any) {}); err != nil {
		t.Fatal(err)
	}
	if err := c.Chat(t.Context(), "tenant", "book", "hello", func(string, any) {}); err != nil {
		t.Fatal(err)
	}
	if _, ok := starts[0]["left_for_owner"]; ok {
		t.Fatal("the first sort has nothing to send back")
	}
	sent, _ := starts[1]["left_for_owner"].([]any)
	if len(sent) != 1 || sent[0].(map[string]any)["counterparty"] != "synthetic lender 1" {
		t.Fatalf("second sort sent %v", starts[1]["left_for_owner"])
	}
	if _, ok := starts[2]["left_for_owner"]; ok {
		t.Fatal("another Book's kinds were sent")
	}
	if _, ok := starts[3]["left_for_owner"]; ok {
		t.Fatal("a chat message carried sorting state")
	}
	b := c.book("tenant", "book")
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(b.left) != 3 {
		t.Fatalf("kept %d kinds, want 3 without repeats", len(b.left))
	}
}

// A Book remembers a bounded number of left kinds, dropping the oldest.
func TestLeftKindsAreBoundedPerBook(t *testing.T) {
	c := NewClient(Config{})
	b := c.book("tenant", "book")
	var events []event
	for i := range maxLeftKinds + 5 {
		raw, _ := json.Marshal(map[string]any{"kinds": []map[string]string{{"bank_account": "card", "direction": "out", "counterparty": fmt.Sprint("synthetic ", i)}}})
		events = append(events, event{Name: "left_for_owner", Data: raw})
	}
	long, _ := json.Marshal(map[string]any{"kinds": []string{string(make([]byte, maxLeftKind))}})
	events = append(events, event{Name: "left_for_owner", Data: long})
	c.noteLeft(b, step{Events: events})
	if len(b.left) != maxLeftKinds || len(b.leftSeen) != maxLeftKinds {
		t.Fatalf("kept %d (%d seen)", len(b.left), len(b.leftSeen))
	}
	var first map[string]string
	_ = json.Unmarshal(b.left[0], &first)
	if first["counterparty"] != "synthetic 5" {
		t.Fatalf("oldest kept is %v", first)
	}
}
