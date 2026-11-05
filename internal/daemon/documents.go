package daemon

import (
	"context"
	"encoding/json"
	"io"

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
}
