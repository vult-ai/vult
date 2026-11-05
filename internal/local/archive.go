package local

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

const MaxEncryptedArchive = ledger.MaxArchiveSize + (4 << 20)
const MaxArchiveFrame = 64 << 10

type ArchiveRequest struct {
	BookID    string `json:"book_id,omitempty"`
	Encrypted bool   `json:"encrypted"`
}

func (q ArchiveRequest) Valid() bool {
	return (q.BookID == "" || ledger.ValidID(q.BookID)) && (q.Encrypted || q.BookID != "")
}

type RestoreRequest struct {
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	RecoveryKey string `json:"recovery_key"`
}

func (q RestoreRequest) Valid() bool {
	decoded, err := hex.DecodeString(q.SHA256)
	return err == nil && len(decoded) == 32 && strings.ToLower(q.SHA256) == q.SHA256 && q.Size > 0 && q.Size <= MaxEncryptedArchive && len(q.RecoveryKey) >= 32 && len(q.RecoveryKey) <= 128
}

type ArchiveStream struct {
	Format    string `json:"format"`
	Encrypted bool   `json:"encrypted"`
}

// FrameWriter bounds each streaming frame and the total response. End writes
// its terminator; the caller then sends the final success or failure response.
type FrameWriter struct {
	Destination io.Writer
	Total       int64
}

func (w *FrameWriter) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		n := min(len(p), MaxArchiveFrame)
		if w.Total > MaxEncryptedArchive-int64(n) {
			return written, ledger.ErrLimit
		}
		if err := binary.Write(w.Destination, binary.BigEndian, uint32(n)); err != nil {
			return written, err
		}
		count, err := w.Destination.Write(p[:n])
		written += count
		w.Total += int64(count)
		if err != nil {
			return written, err
		}
		if count != n {
			return written, io.ErrShortWrite
		}
		p = p[n:]
	}
	return written, nil
}
func (w *FrameWriter) End() error { return binary.Write(w.Destination, binary.BigEndian, uint32(0)) }

type FrameReader struct {
	Source    io.Reader
	remaining uint32
	total     int64
	ended     bool
}

func (r *FrameReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.ended {
		return 0, io.EOF
	}
	if r.remaining == 0 {
		var size uint32
		if binary.Read(r.Source, binary.BigEndian, &size) != nil {
			return 0, ledger.ErrUncertain
		}
		if size == 0 {
			r.ended = true
			return 0, io.EOF
		}
		if size > MaxArchiveFrame || r.total > MaxEncryptedArchive-int64(size) {
			return 0, ledger.ErrInvalid
		}
		r.remaining = size
		r.total += int64(size)
	}
	n, err := r.Source.Read(p[:min(len(p), int(r.remaining))])
	r.remaining -= uint32(n)
	if err != nil && r.remaining > 0 {
		return n, ledger.ErrUncertain
	}
	return n, err
}

func archiveDeadline(ctx context.Context, connection net.Conn) error {
	deadline := time.Now().Add(15 * time.Minute)
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	return connection.SetDeadline(deadline)
}

func (c Client) ArchiveBackup(ctx context.Context, authorization string, request ArchiveRequest, dst io.Writer) error {
	if !request.Valid() || dst == nil {
		return ledger.ErrInvalid
	}
	connection, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err = archiveDeadline(ctx, connection); err != nil {
		return ledger.ErrUncertain
	}
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	if err = streamRequest(connection, "archive_backup", authorization, request, 0); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(connection, 4096)
	prefix, err := streamReply(reader)
	if err != nil {
		return err
	}
	var stream ArchiveStream
	if prefix.StreamSize != -1 || strictjson.Decode(prefix.Result, &stream) != nil || stream.Format != "vult-archive-stream-v1" || stream.Encrypted != request.Encrypted {
		return ledger.ErrInvalid
	}
	if _, err = io.Copy(dst, &FrameReader{Source: reader}); err != nil {
		return err
	}
	last, err := streamReply(reader)
	if err != nil {
		return err
	}
	var status struct {
		Status string `json:"status"`
	}
	if strictjson.Decode(last.Result, &status) != nil || status.Status != "complete" || last.StreamSize != 0 {
		return ledger.ErrUncertain
	}
	return nil
}

func (c Client) ArchiveRestore(ctx context.Context, authorization string, request RestoreRequest, source io.Reader) (json.RawMessage, error) {
	if !request.Valid() || source == nil {
		return nil, ledger.ErrInvalid
	}
	connection, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	if err = archiveDeadline(ctx, connection); err != nil {
		return nil, ledger.ErrUncertain
	}
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	if err = streamRequest(connection, "archive_restore", authorization, request, request.Size); err != nil {
		return nil, err
	}
	if _, err = io.CopyN(connection, source, request.Size); err != nil {
		return nil, ledger.ErrUncertain
	}
	reply, err := streamReply(bufio.NewReaderSize(connection, 4096))
	if err != nil {
		return nil, err
	}
	if reply.StreamSize != 0 {
		return nil, ledger.ErrUncertain
	}
	return reply.Result, nil
}
