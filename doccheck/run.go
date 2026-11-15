package doccheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/vult-ai/vult/internal/docread"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

// OwnerAct runs one call as the Book's signed-in owner, with their actor.
type OwnerAct func(call func(o ledger.TenantAdmin, actor string) error) error

// Assist gives the checker an assistant for a Book, or nil when the Book
// has none.
type Assist func(context.Context, ledger.AgentTarget) Assistant

// toolsFor gives the checker the Book's assistant, when the program has
// one, and this machine's text recognition.
func toolsFor(ctx context.Context, assist Assist, t ledger.AgentTarget) Tools {
	tools := Tools{OCR: docread.LocalOCR()}
	if assist != nil {
		tools.Assistant = assist(ctx, t)
	}
	return tools
}

// Hook adapts the checker to the server's DocumentAdded option: a file the
// owner added, or fields the owner confirmed beside it.
func Hook(assist Assist) func(context.Context, ledger.AgentTarget, server.OwnerAct, server.AddedDocument) {
	return func(ctx context.Context, t ledger.AgentTarget, act server.OwnerAct, d server.AddedDocument) {
		book := ownerBook{book: t.BookID, act: OwnerAct(act)}
		doc := Document{ID: d.ID, Name: d.Name, MediaType: d.MediaType, Group: d.Group, Data: d.Data}
		tools := toolsFor(ctx, assist, t)
		if d.Confirmed != nil {
			var fields docread.Reading
			if json.Unmarshal(d.Confirmed, &fields) == nil {
				_ = Propose(ctx, tools, book, doc, fields, ledger.ReadByOwner, "")
			}
			return
		}
		_ = Check(ctx, tools, book, doc)
	}
}

// Proposal is what an agent sends to propose_document_reading: the
// document and the fields it read. Only fields Vult couldn't read are
// taken, and only once they check out against the document.
type Proposal struct {
	DocumentID string          `json:"document_id"`
	Reading    docread.Reading `json:"reading"`
}

// ProposeHook adapts Propose to the server's ProposeReading option, for an
// agent's proposal arriving over MCP.
func ProposeHook(assist Assist) server.ProposeReading {
	return func(ctx context.Context, store ledger.Store, scope ledger.Scope, agentName string, input json.RawMessage) (json.RawMessage, error) {
		return ProposeFromAgent(ctx, toolsFor(ctx, assist, ledger.AgentTarget{TenantID: scope.TenantID, BookID: scope.BookID}), store, scope, agentName, input)
	}
}

// ProposeFromAgent verifies an agent's proposed reading in the agent's own
// request and, when it checks out, runs Vult's checks on it.
func ProposeFromAgent(ctx context.Context, tools Tools, store ledger.Store, scope ledger.Scope, agentName string, input json.RawMessage) (json.RawMessage, error) {
	if scope.Access == "read" {
		return nil, ledger.ErrDenied
	}
	var p Proposal
	if strictjson.Decode(input, &p) != nil || !ledger.ValidID(p.DocumentID) || len(p.Reading.Lines) > 5000 {
		return nil, ledger.ErrInvalid
	}
	var metadata ledger.DocumentInspection
	raw, err := store.Call(ctx, scope, "get_document", mustJSON(map[string]string{"document_id": p.DocumentID}))
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &metadata) != nil || metadata.Document.State != "ready" {
		return nil, ledger.ErrNotFound
	}
	var data bytes.Buffer
	if err := store.Download(ctx, scope, p.DocumentID, &data); err != nil {
		return nil, err
	}
	doc := Document{ID: p.DocumentID, Name: metadata.Document.Name, MediaType: metadata.Document.MediaType, Data: data.Bytes()}
	err = Propose(ctx, tools, ledger.AgentRequest{Store: store, Scope: scope}, doc, p.Reading, ledger.ReadByAgent, agentName)
	var rejected *docread.Rejection
	if errors.As(err, &rejected) {
		return mustJSON(map[string]string{"status": "rejected", "reason": rejected.Reason}), nil
	}
	if err != nil {
		return nil, err
	}
	return mustJSON(map[string]string{"status": "accepted",
		"message": "Vult verified the reading against the document and ran its checks. Read the results with list_document_checks."}), nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// ownerBook reads and records through the owner's session, as Vult.
type ownerBook struct {
	book string
	act  OwnerAct
}

func (b ownerBook) Facts(ctx context.Context) (ledger.DocumentFacts, error) {
	var f ledger.DocumentFacts
	err := b.act(func(o ledger.TenantAdmin, _ string) error {
		var err error
		f, err = o.DocumentFacts(ctx, b.book)
		return err
	})
	return f, err
}

func (b ownerBook) Write(ctx context.Context, c ledger.DocumentCheck) error {
	return b.act(func(o ledger.TenantAdmin, actor string) error { return o.WriteDocumentCheck(ctx, b.book, actor, c) })
}

func (b ownerBook) Numbers(ctx context.Context, q ledger.StatementReading) ([]ledger.DocumentCheck, error) {
	var out []ledger.DocumentCheck
	err := b.act(func(o ledger.TenantAdmin, actor string) error {
		var err error
		out, err = o.CheckStatementNumbers(ctx, b.book, actor, q)
		return err
	})
	return out, err
}

func (b ownerBook) Receipt(ctx context.Context, q ledger.RecordReceiptDetails) ([]ledger.DocumentCheck, error) {
	var out []ledger.DocumentCheck
	err := b.act(func(o ledger.TenantAdmin, actor string) error {
		var err error
		out, err = o.RecordReceiptAsVult(ctx, b.book, actor, q)
		return err
	})
	return out, err
}

func (b ownerBook) Reading(ctx context.Context, document string) (ledger.DocumentReading, bool, error) {
	var r ledger.DocumentReading
	var ok bool
	err := b.act(func(o ledger.TenantAdmin, _ string) error {
		var err error
		r, ok, err = o.LatestDocumentReading(ctx, b.book, document)
		return err
	})
	return r, ok, err
}

func (b ownerBook) SaveReading(ctx context.Context, r ledger.DocumentReading) error {
	return b.act(func(o ledger.TenantAdmin, actor string) error { return o.SaveDocumentReading(ctx, b.book, actor, r) })
}
