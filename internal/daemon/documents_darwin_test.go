package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/filebridge"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/mcpserver"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestDocumentCompanionUsesRealSocketAndCurrentAuthority(t *testing.T) {
	db := testutil.New(t)
	_, scope := db.Book(t, "Example Book")
	storage := t.TempDir()
	if err := os.Chmod(storage, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(storage)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	keyJSON, err := documents.NewKeyFile(scope.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.ParseKeyring(keyJSON)
	if err != nil {
		t.Fatal(err)
	}
	db.Store.Documents = &documents.Vault{Storage: disk, Keys: keys}
	uid := uint32(os.Getuid())
	token := secret()
	s := &Server{Admin: db.Owner, Ledger: db.Store, sessions: map[string]session{tokenKey(token): {Scope: scope, UID: uid, Expires: time.Now().Add(time.Hour)}}}
	directory, err := os.MkdirTemp("/tmp", "vult-documents-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "service.sock")
	listener, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(db.Context)
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		listener.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(7 * time.Second):
			t.Error("document socket did not stop")
		}
	})
	client := local.Client{Socket: socket, ExpectedUID: uid}
	imports, exports := t.TempDir(), t.TempDir()
	if err := os.Chmod(exports, 0700); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("synthetic original\n"), 300000)
	if err = os.WriteFile(filepath.Join(imports, "example.pdf"), data, 0600); err != nil {
		t.Fatal(err)
	}
	files, err := filebridge.Open(&local.DocumentTransport{Client: client, Authorization: token}, imports, exports)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ss, err := mcpserver.New(files.Call, "write", mcpserver.Options{Documents: true, LocalFiles: true}).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "example-client", Version: "1.0.0"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "add_document", Arguments: map[string]any{"request_key": "original", "file": "example.pdf"}})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatal("companion upload failed", err, result)
	}
	var doc ledger.Document
	if json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &doc) != nil || doc.State != "ready" {
		t.Fatal("upload not confirmed")
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_document", Arguments: map[string]any{"document_id": doc.ID, "output": "returned.pdf"}})
	if err != nil || result.IsError {
		t.Fatal("companion download failed", err, result)
	}
	returned, err := os.ReadFile(filepath.Join(exports, "returned.pdf"))
	if err != nil || !bytes.Equal(returned, data) {
		t.Fatal("socket changed original", err)
	}
	// A caller without a session cannot make the service await a large body.
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(time.Second))
	if err = json.NewEncoder(conn).Encode(local.Header{Method: "document_upload", Size: 64, StreamSize: documents.MaxSize}); err != nil {
		t.Fatal(err)
	}
	denied, err := local.ReadMessage(conn)
	conn.Close()
	if err != nil || !bytes.Contains(denied, []byte("access denied")) {
		t.Fatal("unauthenticated byte body was admitted", err)
	}
	if err = db.Owner.Revoke(ctx, scope.ConnectionID); err != nil {
		t.Fatal(err)
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_document", Arguments: map[string]any{"document_id": doc.ID, "output": "revoked.pdf"}})
	if err == nil && !result.IsError {
		t.Fatal("revoked companion exported original")
	}
	entries, err := os.ReadDir(exports)
	if err != nil || len(entries) != 1 {
		t.Fatal("failed transfer left output", err)
	}
}
