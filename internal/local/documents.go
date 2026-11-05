package local

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"unicode"

	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// SessionRequest binds approved folders into the native session authorization.
// These are client folders: the service never opens or interprets their files.
type SessionRequest struct {
	ConnectionID    string `json:"connection_id"`
	ImportDirectory string `json:"import_directory,omitempty"`
	ExportDirectory string `json:"export_directory,omitempty"`
}

type DocumentTransport struct {
	Client        Client
	Authorization string
}

func (t *DocumentTransport) Call(ctx context.Context, method string, input json.RawMessage) (json.RawMessage, error) {
	return t.Client.Call(ctx, method, t.Authorization, input)
}
func (t *DocumentTransport) Upload(ctx context.Context, id string, size int64, source io.Reader) (json.RawMessage, error) {
	return t.Client.Upload(ctx, t.Authorization, id, size, source)
}
func (t *DocumentTransport) Download(ctx context.Context, id string, dst io.Writer) error {
	return t.Client.Download(ctx, t.Authorization, id, dst)
}

func (q SessionRequest) Valid() bool {
	if !ledger.ValidID(q.ConnectionID) {
		return false
	}
	for _, path := range []string{q.ImportDirectory, q.ExportDirectory} {
		if path == "" {
			continue
		}
		if len(path) > 1024 || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			return false
		}
		for _, r := range path {
			if unicode.IsControl(r) {
				return false
			}
		}
	}
	return true
}

func streamRequest(conn net.Conn, method, token string, payload any, size int64) error {
	data, err := json.Marshal(payload)
	if err != nil || len(data) > 4096 {
		return ledger.ErrInvalid
	}
	header, err := json.Marshal(Header{Method: method, Authorization: token, Size: len(data), StreamSize: size})
	if err != nil {
		return ledger.ErrInvalid
	}
	if _, err = conn.Write(append(header, '\n')); err != nil {
		return ledger.ErrUncertain
	}
	if _, err = conn.Write(data); err != nil {
		return ledger.ErrUncertain
	}
	return nil
}

func streamReply(reader *bufio.Reader) (Response, error) {
	var reply Response
	line, err := reader.ReadSlice('\n')
	if err != nil || len(line) > 4096 || strictjson.Decode(line[:len(line)-1], &reply) != nil {
		return reply, ledger.ErrUncertain
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	if reply.Result == nil {
		return reply, ledger.ErrUncertain
	}
	return reply, nil
}

func (c Client) Upload(ctx context.Context, token, uploadID string, size int64, source io.Reader) (json.RawMessage, error) {
	if !ledger.ValidID(uploadID) || size < 1 || size > documents.MaxSize || source == nil {
		return nil, ledger.ErrInvalid
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err = streamRequest(conn, "document_upload", token, struct {
		UploadID string `json:"upload_id"`
	}{uploadID}, size); err != nil {
		return nil, err
	}
	if _, err = io.CopyN(conn, source, size); err != nil {
		return nil, ledger.ErrUncertain
	}
	reply, err := streamReply(bufio.NewReaderSize(conn, 4096))
	if err != nil {
		return nil, err
	}
	if reply.StreamSize != 0 {
		return nil, ledger.ErrUncertain
	}
	return reply.Result, nil
}

// Download is successful only after exact bytes, hash and the final service
// result. The caller must discard partial output on any returned error.
func (c Client) Download(ctx context.Context, token, documentID string, dst io.Writer) error {
	if !ledger.ValidID(documentID) || dst == nil {
		return ledger.ErrInvalid
	}
	conn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = streamRequest(conn, "document_download", token, ledger.DocumentQuery{DocumentID: documentID}, 0); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(conn, 4096)
	prefix, err := streamReply(reader)
	if err != nil {
		return err
	}
	var document ledger.Document
	if strictjson.Decode(prefix.Result, &document) != nil || document.ID != documentID || document.State != "ready" || document.Size < 1 || document.Size > documents.MaxSize || prefix.StreamSize != document.Size {
		return documents.ErrIntegrity
	}
	hash := sha256.New()
	if _, err = io.CopyN(io.MultiWriter(dst, hash), reader, document.Size); err != nil {
		return documents.ErrIntegrity
	}
	if hex.EncodeToString(hash.Sum(nil)) != document.SHA256 {
		return documents.ErrIntegrity
	}
	last, err := streamReply(reader)
	if err != nil {
		return err
	}
	var outcome struct {
		Status string `json:"status"`
	}
	if last.StreamSize != 0 || strictjson.Decode(last.Result, &outcome) != nil || outcome.Status != "complete" {
		return documents.ErrIntegrity
	}
	return nil
}
