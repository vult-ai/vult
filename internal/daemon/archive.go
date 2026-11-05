package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"

	"filippo.io/age"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

func (s *Server) archiveStream(ctx context.Context, uid uint32, q local.Request, size int64, source io.Reader, dst io.Writer) {
	fail := func(err error) { _ = json.NewEncoder(dst).Encode(local.Response{Error: safeError(err)}) }
	if !s.consume(uid, q) {
		fail(ledger.ErrDenied)
		return
	}
	if q.Method == "archive_restore" {
		var input local.RestoreRequest
		if strictjson.Decode(q.Payload, &input) != nil || !input.Valid() || input.Size != size {
			fail(ledger.ErrInvalid)
			return
		}
		result, err := s.restoreStream(ctx, input, source)
		if err != nil {
			fail(err)
			return
		}
		_ = json.NewEncoder(dst).Encode(local.Response{Result: result})
		return
	}
	var request local.ArchiveRequest
	if strictjson.Decode(q.Payload, &request) != nil || !request.Valid() || s.Ledger.Documents == nil {
		fail(ledger.ErrInvalid)
		return
	}
	if err := json.NewEncoder(dst).Encode(local.Response{StreamSize: -1, Result: marshal(local.ArchiveStream{Format: "vult-archive-stream-v1", Encrypted: request.Encrypted})}); err != nil {
		return
	}
	frames := &local.FrameWriter{Destination: dst}
	output := io.Writer(frames)
	var cipher io.WriteCloser
	var err error
	if request.Encrypted {
		if len(s.RecoveryKey) < 32 {
			err = ledger.ErrInvalid
		} else {
			var recipient *age.ScryptRecipient
			recipient, err = age.NewScryptRecipient(s.RecoveryKey)
			if err == nil {
				recipient.SetWorkFactor(18)
				cipher, err = age.Encrypt(frames, recipient)
				output = cipher
			}
		}
	}
	if err == nil {
		err = s.Admin.WriteArchive(ctx, s.Ledger.Documents, output, request.BookID)
	}
	if err == nil && cipher != nil {
		err = cipher.Close()
	}
	if frames.End() != nil {
		return
	}
	if err != nil {
		fail(err)
		return
	}
	_ = json.NewEncoder(dst).Encode(local.Response{Result: json.RawMessage(`{"status":"complete"}`)})
}

func (s *Server) restoreStream(ctx context.Context, q local.RestoreRequest, source io.Reader) (json.RawMessage, error) {
	approved := &approvedArchiveInput{source: &io.LimitedReader{R: source, N: q.Size}, hash: sha256.New(), want: q.SHA256}
	identity, err := age.NewScryptIdentity(q.RecoveryKey)
	if err != nil {
		return nil, documents.ErrIntegrity
	}
	identity.SetMaxWorkFactor(18)
	plain, err := age.Decrypt(approved, identity)
	if err != nil {
		return nil, documents.ErrIntegrity
	}
	reader := bufio.NewReaderSize(plain, 4096)
	first, err := reader.Peek(1)
	if err != nil {
		return nil, documents.ErrIntegrity
	}
	selected := ""
	provision := func(ctx context.Context, tenant string) error {
		if selected != "" && selected != tenant {
			return ledger.ErrInvalid
		}
		selected = tenant
		if s.ProvisionKeys != nil {
			return s.ProvisionKeys(ctx, tenant)
		}
		return nil
	}
	if first[0] == '{' {
		// Existing encrypted finance-only backups remain recoverable.
		data, err := io.ReadAll(io.LimitReader(reader, (64<<20)+1))
		if err != nil || len(data) > 64<<20 || !approved.verified {
			return nil, documents.ErrIntegrity
		}
		var snapshot ledger.Snapshot
		if strictjson.Decode(data, &snapshot) != nil {
			return nil, ledger.ErrInvalid
		}
		if err = s.restoreLegacySnapshot(ctx, snapshot); err != nil {
			return nil, err
		}
	} else {
		if s.Ledger.Documents == nil {
			return nil, ledger.ErrInvalid
		}
		if err = s.Admin.RestoreArchive(ctx, s.Ledger.Documents, reader, provision); err != nil {
			return nil, err
		}
	}
	if !approved.verified {
		return nil, documents.ErrIntegrity
	}
	s.mu.Lock()
	s.sessions = map[string]session{}
	s.mu.Unlock()
	return json.RawMessage(`{"status":"restored","connections":"reauthorization_required"}`), nil
}

// The encrypted stream must reach exact approved length/hash before age can
// report its authenticated EOF. Mismatches remain errors on every later Read.
type approvedArchiveInput struct {
	source   *io.LimitedReader
	hash     hash.Hash
	want     string
	verified bool
	failed   bool
}

func (r *approvedArchiveInput) Read(p []byte) (int, error) {
	if r.failed {
		return 0, documents.ErrIntegrity
	}
	if r.source.N == 0 {
		if !r.verified {
			r.failed = true
			return 0, documents.ErrIntegrity
		}
		return 0, io.EOF
	}
	n, err := r.source.Read(p)
	_, _ = r.hash.Write(p[:n])
	if r.source.N == 0 {
		if hex.EncodeToString(r.hash.Sum(nil)) != r.want {
			r.failed = true
			return n, documents.ErrIntegrity
		}
		r.verified = true
	} else if err != nil {
		r.failed = true
		return n, documents.ErrIntegrity
	}
	return n, err
}

func (s *Server) restoreLegacySnapshot(ctx context.Context, snapshot ledger.Snapshot) error {
	var tenants []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(snapshot.Tables["tenants"], &tenants) != nil || len(tenants) > 1 {
		return ledger.ErrInvalid
	}
	for _, tenant := range tenants {
		if !ledger.ValidID(tenant.ID) {
			return ledger.ErrInvalid
		}
		if s.ProvisionKeys != nil {
			if err := s.ProvisionKeys(ctx, tenant.ID); err != nil {
				return err
			}
		}
	}
	return s.Admin.Restore(ctx, snapshot)
}
