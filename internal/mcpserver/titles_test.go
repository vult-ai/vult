package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
)

// Agents' apps show a tool's title; every tool in every catalog has one.
func TestEveryToolHasATitle(t *testing.T) {
	for _, access := range []string{"read", "write"} {
		for _, options := range []Options{{}, {Documents: true, OwnerUploads: true}, {Documents: true, LocalFiles: true}} {
			c := connect(t, context.Background(), func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			}, access, options)
			list, err := c.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, tool := range list.Tools {
				if tool.Title == "" || tool.Annotations == nil || tool.Annotations.Title != tool.Title {
					t.Fatalf("%s has no title", tool.Name)
				}
			}
		}
	}
}

// A proposing agent gets every reading tool and preparing entries, and nothing
// else that writes.
func TestProposingAgentsGetReadsAndPreparation(t *testing.T) {
	c := connect(t, context.Background(), func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}, "propose", Options{Documents: true, OwnerUploads: true})
	list, err := c.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range list.Tools {
		got[tool.Name] = true
	}
	if !got["prepare_operation"] || !got["list_accounts"] || !got["get_operation_proposal"] || got["post_journal"] || got["execute_proposal"] || got["add_document"] {
		t.Fatalf("proposing tools differ: %v", got)
	}
}
