//go:build devweb

package daemon

import (
	"testing"

	"github.com/vult-ai/vult/agentservice"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

func TestConversationsAreKeptPerBook(t *testing.T) {
	tenant, book, other := ledger.ID(), ledger.ID(), ledger.ID()
	for name, kept := range map[string]agentservice.Conversations{"while running": &agentservice.Memory{}, "in a folder": filed{folder: t.TempDir()}} {
		said := agentservice.Conversation{Turns: []server.ChatTurn{{ID: "1", Role: "person", Text: "hello"}}}
		if err := kept.Save(tenant, book, said); err != nil {
			t.Fatal(name, err)
		}
		got, err := kept.Load(tenant, book)
		if err != nil || len(got.Turns) != 1 || got.Turns[0].Text != "hello" {
			t.Fatalf("%s: %+v, %v", name, got, err)
		}
		if none, err := kept.Load(tenant, other); err != nil || len(none.Turns) != 0 {
			t.Fatalf("%s: another Book reads %+v, %v", name, none, err)
		}
		if err := kept.Forget(tenant, book); err != nil {
			t.Fatal(name, err)
		}
		if gone, _ := kept.Load(tenant, book); len(gone.Turns) != 0 {
			t.Fatalf("%s: a forgotten conversation is still read", name)
		}
	}
	if _, err := (filed{folder: t.TempDir()}).Load("../x", book); err == nil {
		t.Fatal("a name that isn't an ID was accepted")
	}
}
