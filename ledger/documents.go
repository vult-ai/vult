package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/strictjson"
)

type AddDocument struct {
	RequestKey string `json:"request_key"`
	Name       string `json:"name"`
	MediaType  string `json:"media_type"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}

type Document struct {
	ID        string     `json:"id"`
	Sequence  int64      `json:"sequence"`
	Name      string     `json:"name"`
	MediaType string     `json:"media_type"`
	Size      int64      `json:"size"`
	SHA256    string     `json:"sha256"`
	State     string     `json:"state"`
	CreatedAt time.Time  `json:"created_at"`
	ReadyAt   *time.Time `json:"ready_at,omitempty"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
}

type DocumentIntake struct {
	Document  Document   `json:"document"`
	UploadID  string     `json:"upload_id,omitempty"`
	ExpiresAt *time.Time `json:"upload_expires_at,omitempty"`
}

type DocumentQuery struct {
	DocumentID string `json:"document_id"`
	UploadID   string `json:"upload_id,omitempty"`
}

type DocumentUploadState struct {
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	ExpiresAt *time.Time      `json:"expires_at,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
}

type DocumentInspection struct {
	Document Document             `json:"document"`
	Upload   *DocumentUploadState `json:"upload,omitempty"`
	// Transfer URLs are short-lived read results, never retained write outcomes.
	UploadURL   string `json:"upload_url,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

type storedDocument struct {
	Document
	storageID, keyID string
	live             bool
}

func (d storedDocument) object(s Scope) documents.Object {
	return documents.Object{TenantID: s.TenantID, BookID: s.BookID, DocumentID: d.ID,
		StorageID: d.storageID, KeyID: d.keyID, SHA256: d.SHA256, Size: d.Size}
}

func documentMethod(method string) bool {
	return method == "add_document" || method == "get_document" || method == "list_documents"
}

func validDocument(q AddDocument) bool {
	if !validKey(q.RequestKey) || q.Size < 1 || q.Size > documents.MaxSize || len(q.SHA256) != 64 ||
		len(q.Name) < 1 || len(q.Name) > 255 || !utf8.ValidString(q.Name) || strings.ContainsAny(q.Name, "/\\") {
		return false
	}
	if _, err := hex.DecodeString(q.SHA256); err != nil || strings.ToLower(q.SHA256) != q.SHA256 {
		return false
	}
	for _, r := range q.Name {
		if unicode.IsControl(r) {
			return false
		}
	}
	switch q.MediaType {
	case "application/pdf", "image/png", "image/jpeg", "image/webp", "text/csv":
		return true
	}
	return false
}

func scanDocument(row pgx.Row) (storedDocument, error) {
	var d storedDocument
	err := row.Scan(&d.ID, &d.Sequence, &d.Name, &d.MediaType, &d.Size, &d.SHA256, &d.State,
		&d.CreatedAt, &d.ReadyAt, &d.ExpiresAt, &d.UsedAt, &d.storageID, &d.keyID, &d.live)
	return d, err
}

const documentColumns = `id::text,sequence,original_name,media_type,size_bytes,sha256,state,
    created_at,ready_at,expires_at,used_at,storage_id,key_id,
    (used_at IS NOT NULL OR expires_at>clock_timestamp())`

func readDocument(ctx context.Context, tx pgx.Tx, s Scope, id string) (storedDocument, error) {
	return scanDocument(tx.QueryRow(ctx, "SELECT "+documentColumns+" FROM public.documents WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, id))
}

func (s Store) documentCall(ctx context.Context, tx pgx.Tx, scope Scope, method string, input []byte) (json.RawMessage, error) {
	if s.Documents == nil || s.Documents.Storage == nil || s.Documents.Keys == nil {
		return nil, ErrDenied
	}
	switch method {
	case "add_document":
		var q AddDocument
		if strictjson.Decode(input, &q) != nil || !validDocument(q) {
			return nil, ErrInvalid
		}
		// Admission sees new storage only once, while the pending row reserves it.
		d, err := scanDocument(tx.QueryRow(ctx, "SELECT "+documentColumns+" FROM public.documents WHERE tenant_id=$1 AND book_id=$2 AND sha256=$3", scope.TenantID, scope.BookID, q.SHA256))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, dbError(err)
		}
		newStorage := errors.Is(err, pgx.ErrNoRows) || d.State == "expired" || !d.live
		admit := s.AdmitWrite
		if !newStorage {
			admit = nil
		}
		return write(ctx, tx, scope, method, q.RequestKey, raw(q), admit, func(receipt string) (json.RawMessage, error) {
			if d.ID != "" && d.Size != q.Size {
				return nil, ErrInvalid
			}
			if d.ID != "" && d.State != "expired" && !d.live {
				if err := expireDocument(ctx, tx, scope, d, s.Documents.Storage); err != nil {
					return nil, err
				}
				d.State = "expired"
			}
			if d.ID == "" || d.State == "expired" {
				key, err := s.Documents.Keys.Current(ctx, scope.TenantID)
				if err != nil {
					return nil, documents.ErrUnavailable
				}
				storage := strings.ReplaceAll(ID()+ID(), "-", "")
				if d.ID == "" {
					d, err = scanDocument(tx.QueryRow(ctx, `INSERT INTO public.documents
                        (tenant_id,book_id,id,sequence,sha256,size_bytes,media_type,original_name,storage_id,key_id,actor_id,expires_at)
                        VALUES($1,$2,$3,1,$4,$5,$6,$7,$8,$9,$10,clock_timestamp()+interval '15 minutes') RETURNING `+documentColumns,
						scope.TenantID, scope.BookID, ID(), q.SHA256, q.Size, q.MediaType, q.Name, storage, key, scope.ActorID))
				} else {
					d, err = scanDocument(tx.QueryRow(ctx, `UPDATE public.documents SET state='pending',storage_id=$4,key_id=$5
                        WHERE tenant_id=$1 AND book_id=$2 AND id=$3 RETURNING `+documentColumns, scope.TenantID, scope.BookID, d.ID, storage, key))
				}
				if err != nil {
					return nil, err
				}
			}
			if d.State == "ready" {
				return raw(DocumentIntake{Document: d.Document}), nil
			}
			var upload string
			var expires time.Time
			err = tx.QueryRow(ctx, `INSERT INTO public.document_uploads
                    (tenant_id,book_id,id,document_id,storage_id,actor_id,connection_id,receipt_id,expires_at)
                    VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text,expires_at`,
				scope.TenantID, scope.BookID, ID(), d.ID, d.storageID, scope.ActorID, scope.ConnectionID, receipt, d.ExpiresAt).Scan(&upload, &expires)
			return raw(DocumentIntake{Document: d.Document, UploadID: upload, ExpiresAt: &expires}), err
		})
	case "get_document":
		var q DocumentQuery
		if strictjson.Decode(input, &q) != nil || !ValidID(q.DocumentID) || q.UploadID != "" && !ValidID(q.UploadID) {
			return nil, ErrInvalid
		}
		d, err := readDocument(ctx, tx, scope, q.DocumentID)
		if err != nil {
			return nil, dbError(err)
		}
		if !d.live {
			d.State = "expired"
		}
		inspection := DocumentInspection{Document: d.Document}
		if q.UploadID != "" {
			if scope.Access != "write" {
				return nil, ErrDenied
			}
			var storage string
			var expires time.Time
			var live bool
			err = tx.QueryRow(ctx, `SELECT storage_id,expires_at,expires_at>clock_timestamp() FROM public.document_uploads
                WHERE tenant_id=$1 AND book_id=$2 AND document_id=$3 AND id=$4 AND actor_id=$5 AND connection_id=$6`,
				scope.TenantID, scope.BookID, d.ID, q.UploadID, scope.ActorID, scope.ConnectionID).Scan(&storage, &expires, &live)
			if err != nil {
				return nil, dbError(err)
			}
			state := &DocumentUploadState{ID: q.UploadID, Status: "expired"}
			var result []byte
			err = tx.QueryRow(ctx, `SELECT result_bytes FROM public.requests
                WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4 AND operation='complete_document_upload'`,
				scope.TenantID, scope.BookID, scope.ActorID, "upload:"+q.UploadID).Scan(&result)
			if err == nil {
				state.Status = "recorded"
				state.Result = result
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return nil, dbError(err)
			} else if live && storage == d.storageID && d.State != "expired" && d.live {
				state.Status = "pending"
				state.ExpiresAt = &expires
			}
			inspection.Upload = state
		}
		return raw(inspection), nil
	case "list_documents":
		var q ListQuery
		if strictjson.Decode(input, &q) != nil || q.After < 0 || q.Limit < 0 || q.Limit > 100 {
			return nil, ErrInvalid
		}
		if q.Limit == 0 {
			q.Limit = 20
		}
		rows, err := tx.Query(ctx, "SELECT "+documentColumns+` FROM public.documents
            WHERE tenant_id=$1 AND book_id=$2 AND sequence>$3 AND state='ready'
            AND (used_at IS NOT NULL OR expires_at>clock_timestamp()) ORDER BY sequence LIMIT $4`, scope.TenantID, scope.BookID, q.After, q.Limit+1)
		if err != nil {
			return nil, dbError(err)
		}
		defer rows.Close()
		out := []Document{}
		for rows.Next() {
			d, err := scanDocument(rows)
			if err != nil {
				return nil, dbError(err)
			}
			out = append(out, d.Document)
		}
		if rows.Err() != nil {
			return nil, dbError(rows.Err())
		}
		more := len(out) > q.Limit
		if more {
			out = out[:q.Limit]
		}
		after := q.After
		if len(out) > 0 {
			after = out[len(out)-1].Sequence
		}
		return raw(struct {
			Documents []Document `json:"documents"`
			NextAfter int64      `json:"next_after"`
			HasMore   bool       `json:"has_more"`
		}{out, after, more}), nil
	}
	return nil, ErrInvalid
}

// Upload verifies a prepared intake and records a completed original atomically
// with its retained outcome. Streaming never bypasses the ordinary live lease.
func (s Store) Upload(ctx context.Context, scope Scope, upload string, body io.Reader) (json.RawMessage, error) {
	if !ValidID(upload) || body == nil || s.Documents == nil {
		return nil, ErrInvalid
	}
	q := struct {
		RequestKey string `json:"request_key"`
		UploadID   string `json:"upload_id"`
	}{"upload:" + upload, upload}
	return s.transact(ctx, scope, "complete_document_upload", raw(q), func(tx pgx.Tx) (json.RawMessage, error) {
		return write(ctx, tx, scope, "complete_document_upload", q.RequestKey, raw(q), s.AdmitWrite, func(string) (json.RawMessage, error) {
			var id, storage string
			var expires time.Time
			err := tx.QueryRow(ctx, `SELECT document_id::text,storage_id,expires_at FROM public.document_uploads
                WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND actor_id=$4 AND connection_id=$5
                AND expires_at>clock_timestamp()`,
				scope.TenantID, scope.BookID, upload, scope.ActorID, scope.ConnectionID).Scan(&id, &storage, &expires)
			if err != nil {
				return nil, err
			}
			d, err := readDocument(ctx, tx, scope, id)
			if err != nil {
				return nil, err
			}
			if storage != d.storageID || d.State == "expired" || !d.live {
				return nil, ErrInvalid
			}
			if d.State == "ready" {
				hash := sha256.New()
				n, err := io.Copy(hash, io.LimitReader(body, d.Size+1))
				if err != nil || n != d.Size || hex.EncodeToString(hash.Sum(nil)) != d.SHA256 {
					return nil, documents.ErrIntegrity
				}
			} else {
				hash := sha256.New()
				verified := &countedReader{source: io.TeeReader(io.LimitReader(body, d.Size+1), hash)}
				err = s.Documents.Write(ctx, d.object(scope), verified)
				if errors.Is(err, documents.ErrExists) {
					_, readErr := io.Copy(io.Discard, verified)
					if readErr != nil || verified.count != d.Size || hex.EncodeToString(hash.Sum(nil)) != d.SHA256 {
						err = documents.ErrIntegrity
					} else {
						err = s.Documents.Read(ctx, d.object(scope), io.Discard)
					}
				}
				if err != nil {
					return nil, err
				}
				d, err = scanDocument(tx.QueryRow(ctx, `UPDATE public.documents SET state='ready'
                    WHERE tenant_id=$1 AND book_id=$2 AND id=$3 RETURNING `+documentColumns, scope.TenantID, scope.BookID, id))
				if err != nil {
					return nil, err
				}
			}
			return raw(d.Document), nil
		})
	})
}

// Download holds the live Book/grant while reading, including the final hash.
// Callers must discard output unless both streaming and transaction completion
// succeed. Paths and storage identifiers never come from a client request.
func (s Store) Download(ctx context.Context, scope Scope, id string, dst io.Writer) error {
	if !ValidID(id) || dst == nil || s.Documents == nil {
		return ErrInvalid
	}
	_, err := s.transact(ctx, scope, "get_document", raw(DocumentQuery{DocumentID: id}), func(tx pgx.Tx) (json.RawMessage, error) {
		d, err := readDocument(ctx, tx, scope, id)
		if err != nil {
			return nil, dbError(err)
		}
		if d.State != "ready" || !d.live {
			return nil, ErrNotFound
		}
		if err := s.Documents.Read(ctx, d.object(scope), dst); err != nil {
			return nil, err
		}
		return raw(d.Document), nil
	})
	return err
}

func expireDocument(ctx context.Context, tx pgx.Tx, s Scope, d storedDocument, storage documents.Storage) error {
	if _, err := tx.Exec(ctx, "UPDATE public.documents SET state='expired' WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, d.ID); err != nil {
		return err
	}
	// The original is already expired and cannot be cited, even if this metadata
	// transaction later rolls back. A backup holds the same Book before reading.
	if err := storage.Remove(ctx, d.storageID); err != nil {
		return documents.ErrUnavailable
	}
	return nil
}

func expireDocumentBatch(ctx context.Context, tx pgx.Tx, scope Scope, storage documents.Storage, limit int) (int, error) {
	count := 0
	rows, err := tx.Query(ctx, "SELECT "+documentColumns+` FROM public.documents
            WHERE tenant_id=$1 AND book_id=$2 AND used_at IS NULL AND state<>'expired'
            AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT $3`, scope.TenantID, scope.BookID, limit)
	if err != nil {
		return 0, dbError(err)
	}
	batch := []storedDocument{}
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			rows.Close()
			return 0, dbError(err)
		}
		batch = append(batch, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, dbError(err)
	}
	for _, d := range batch {
		if err := expireDocument(ctx, tx, scope, d, storage); err != nil {
			return 0, err
		}
		count++
	}
	return count, nil
}

type countedReader struct {
	source io.Reader
	count  int64
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	r.count += int64(n)
	return n, err
}
