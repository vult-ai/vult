package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

func (s *Server) documentStream(ctx context.Context, uid uint32, q local.Request, size int64, source io.Reader, dst io.Writer) {
	fail := func(err error) { _ = json.NewEncoder(dst).Encode(local.Response{Error: safeError(err)}) }
	scope, ok := s.scope(uid, q.Authorization)
	if !ok {
		fail(ledger.ErrDenied)
		return
	}
	if q.Method == "document_upload" {
		var request struct {
			UploadID string `json:"upload_id"`
		}
		if strictjson.Decode(q.Payload, &request) != nil {
			fail(ledger.ErrInvalid)
			return
		}
		body := &io.LimitedReader{R: source, N: size}
		result, err := s.Ledger.Upload(ctx, scope, request.UploadID, body)
		if err != nil {
			fail(err)
			return
		}
		// A retained retry may not read the body. Consume exactly this frame so
		// the client can finish sending before it waits for the saved response.
		if _, err = io.Copy(io.Discard, body); err != nil || body.N != 0 {
			fail(ledger.ErrUncertain)
			return
		}
		_ = json.NewEncoder(dst).Encode(local.Response{Result: result})
		return
	}
	var query ledger.DocumentQuery
	if strictjson.Decode(q.Payload, &query) != nil || query.UploadID != "" {
		fail(ledger.ErrInvalid)
		return
	}
	metadata, err := s.Ledger.Call(ctx, scope, "get_document", q.Payload)
	if err != nil {
		fail(err)
		return
	}
	var inspection ledger.DocumentInspection
	if strictjson.Decode(metadata, &inspection) != nil || inspection.Document.State != "ready" {
		fail(ledger.ErrNotFound)
		return
	}
	if err = json.NewEncoder(dst).Encode(local.Response{Result: marshal(inspection.Document), StreamSize: inspection.Document.Size}); err != nil {
		return
	}
	if err = s.Ledger.Download(ctx, scope, query.DocumentID, dst); err != nil {
		// EOF or a missing final result makes even length-complete bytes fail.
		return
	}
	_ = json.NewEncoder(dst).Encode(local.Response{Result: json.RawMessage(`{"status":"complete"}`)})
	s.note(scope, local.AgentActivity{Kind: "opened", Document: clip(inspection.Document.Name, 255)})
}

func (s *Server) ownerDocumentPeer(uid uint32) bool {
	return s.Browser != nil && s.Browser.uid != 0 && uid == s.Browser.uid && s.Browser.documents != nil && s.Ledger.Documents != nil
}

func (s *Server) ownerDocumentStream(ctx context.Context, uid uint32, q local.Request, dst io.Writer) {
	fail := func(err error) {
		if errors.Is(err, access.ErrDenied) {
			err = ledger.ErrDenied
		}
		_ = json.NewEncoder(dst).Encode(local.Response{Error: safeError(err)})
	}
	if !s.ownerDocumentPeer(uid) {
		fail(ledger.ErrDenied)
		return
	}
	var query local.OwnerDocumentQuery
	if strictjson.Decode(q.Payload, &query) != nil || !ledger.ValidID(query.BookID) || !ledger.ValidID(query.DocumentID) {
		fail(ledger.ErrInvalid)
		return
	}
	reader := s.Browser.documents
	metadata, err := reader.Document(ctx, q.Authorization, query.BookID, query.DocumentID)
	if err != nil {
		fail(err)
		return
	}
	if metadata.Document.State != "ready" {
		fail(ledger.ErrNotFound)
		return
	}
	if err = json.NewEncoder(dst).Encode(local.Response{Result: marshal(metadata.Document), StreamSize: metadata.Document.Size}); err != nil {
		return
	}
	if err = reader.Download(ctx, q.Authorization, query.BookID, query.DocumentID, s.Ledger.Documents, dst); err != nil {
		return
	}
	_ = json.NewEncoder(dst).Encode(local.Response{Result: json.RawMessage(`{"status":"complete"}`)})
}

func (s *Server) ownerDocumentUpload(ctx context.Context, uid uint32, q local.Request, size int64, source io.Reader, dst io.Writer) {
	var response local.Response
	switch {
	case !s.ownerDocumentPeer(uid):
		response.Error = ledger.ErrDenied.Error()
	default:
		var request local.OwnerDocumentUploadRequest
		if strictjson.Decode(q.Payload, &request) != nil || request.Document.Size != size {
			response.Error = ledger.ErrInvalid.Error()
			break
		}
		result, err := s.Browser.documents.Upload(ctx, q.Authorization, request.BookID, request.Document, s.Ledger.Documents, source)
		if errors.Is(err, access.ErrDenied) {
			err = ledger.ErrDenied
		}
		if err != nil {
			response.Error = safeError(err)
		} else {
			response.Result = marshal(result)
		}
	}
	_ = json.NewEncoder(dst).Encode(response)
}

type ownerUploadBody struct {
	io.LimitedReader
	complete func()
}

func (b *ownerUploadBody) Read(dst []byte) (int, error) {
	n, err := b.LimitedReader.Read(dst)
	if b.N == 0 && b.complete != nil {
		// The framed bytes are consumed. Only the disconnect watcher can
		// read the socket while the service verifies and records the upload.
		b.complete()
		b.complete = nil
	}
	return n, err
}
