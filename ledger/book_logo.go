package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/strictjson"
)

const MaxBookLogoBytes = 144 << 10

type BookLogo struct {
	ID          string `json:"id"`
	SHA256      string `json:"sha256"`
	ContentType string `json:"content_type"`
	Size        int    `json:"size"`
}

// A validated image or, for the zero value, removal of the current image.
type BookLogoChange struct {
	data        []byte
	contentType string
	sha256      string
}

func NewBookLogoChange(contentType, digest string, data []byte) (BookLogoChange, error) {
	if len(data) > MaxBookLogoBytes {
		return BookLogoChange{}, ErrLimit
	}
	var format string
	switch contentType {
	case "image/jpeg":
		format = "jpeg"
	case "image/png":
		format = "png"
	default:
		return BookLogoChange{}, ErrInvalid
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return BookLogoChange{}, ErrInvalid
	}
	config, decoded, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || decoded != format || config.Width < 1 || config.Height < 1 || config.Width > 256 || config.Height > 256 {
		return BookLogoChange{}, ErrInvalid
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return BookLogoChange{}, ErrInvalid
	}
	return BookLogoChange{data: data, contentType: contentType, sha256: digest}, nil
}

const setBookLogoOperation = "set_book_logo"

func (c BookLogoChange) Request(key string) json.RawMessage {
	return raw(struct {
		RequestKey  string `json:"request_key,omitempty"`
		SHA256      string `json:"sha256,omitempty"`
		ContentType string `json:"content_type,omitempty"`
		Size        int    `json:"size,omitempty"`
		Remove      bool   `json:"remove,omitempty"`
	}{key, c.sha256, c.contentType, len(c.data), c.data == nil})
}

func decodeBookLogoTool(input json.RawMessage) (BookLogoChange, string, error) {
	var q struct {
		RequestKey  string `json:"request_key"`
		ContentType string `json:"content_type"`
		SHA256      string `json:"sha256"`
		Image       string `json:"image_base64"`
		Remove      bool   `json:"remove"`
	}
	if strictjson.DecodeNonNull(input, &q, "") != nil || !validKey(q.RequestKey) {
		return BookLogoChange{}, "", ErrInvalid
	}
	if q.Remove {
		if q.Image != "" || q.SHA256 != "" || q.ContentType != "" {
			return BookLogoChange{}, "", ErrInvalid
		}
		return BookLogoChange{}, q.RequestKey, nil
	}
	if len(q.Image) > base64.StdEncoding.EncodedLen(MaxBookLogoBytes) {
		return BookLogoChange{}, "", ErrLimit
	}
	data, err := base64.StdEncoding.Strict().DecodeString(q.Image)
	if err != nil {
		return BookLogoChange{}, "", ErrInvalid
	}
	change, err := NewBookLogoChange(q.ContentType, q.SHA256, data)
	return change, q.RequestKey, err
}

// SetBookLogo holds the agent connection and Book through the image change
// and any keyed request receipt. Only the fingerprint is kept in the receipt.
func (s Store) SetBookLogo(ctx context.Context, scope Scope, key string, change BookLogoChange) (json.RawMessage, error) {
	request := change.Request(key)
	return s.transact(ctx, scope, setBookLogoOperation, request, func(tx pgx.Tx) (json.RawMessage, error) {
		if key == "" {
			if s.AdmitWrite != nil {
				if err := s.AdmitWrite(ctx, tx, scope, setBookLogoOperation, request); err != nil {
					return nil, err
				}
			}
			return setBookLogo(ctx, tx, scope, ID(), change)
		}
		return write(ctx, tx, scope, setBookLogoOperation, key, request, s.AdmitWrite, func(receipt string) (json.RawMessage, error) {
			return setBookLogo(ctx, tx, scope, receipt, change)
		})
	})
}

func setBookLogo(ctx context.Context, tx pgx.Tx, scope Scope, revisionKey string, change BookLogoChange) (json.RawMessage, error) {
	var result []byte
	err := tx.QueryRow(ctx, "SELECT public.ledger_set_book_logo($1,$2,$3,$4,$5,$6)",
		scope.TenantID, scope.BookID, revisionKey, change.contentType, change.sha256, change.data).Scan(&result)
	return result, dbError(err)
}

func (a TenantAdmin) SetBookLogo(ctx context.Context, book string, change BookLogoChange) (json.RawMessage, error) {
	if !a.valid() || !ValidID(book) {
		return nil, ErrInvalid
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, dbError(err)
	}
	defer tx.Rollback(context.Background())
	scope := Scope{TenantID: a.tenantID, BookID: book}
	if err = setScope(ctx, tx, scope); err != nil {
		return nil, dbError(err)
	}
	result, err := setBookLogo(ctx, tx, scope, ID(), change)
	if err != nil {
		return nil, err
	}
	return result, dbError(tx.Commit(ctx))
}

func (a TenantAdmin) BookLogoImage(ctx context.Context, book, id, digest string) (string, []byte, error) {
	if !a.valid() || !ValidID(book) || !ValidID(id) {
		return "", nil, ErrInvalid
	}
	var contentType string
	var data []byte
	err := a.db.QueryRow(ctx, `SELECT content_type,data FROM public.book_logo_images
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND sha256=$4`, a.tenantID, book, id, digest).Scan(&contentType, &data)
	return contentType, data, dbError(err)
}

type BookLogoRevision struct {
	Revision   int64     `json:"revision"`
	ActorID    string    `json:"actor_id"`
	ActorKind  string    `json:"actor_kind"`
	RecordedAt time.Time `json:"recorded_at"`
	Logo       *BookLogo `json:"logo"`
}

type BookLogoHistory struct {
	SchemaVersion    int                `json:"schema_version"`
	MetadataRevision int64              `json:"metadata_revision"`
	Logo             *BookLogo          `json:"logo"`
	History          []BookLogoRevision `json:"history"`
	NextBefore       *int64             `json:"next_before_revision,omitempty"`
}

func (a TenantAdmin) BookLogoHistory(ctx context.Context, book string, before int64) (BookLogoHistory, error) {
	out := BookLogoHistory{SchemaVersion: 1, History: []BookLogoRevision{}}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, scope Scope, header Book) error {
		out.Logo = header.Logo
		if header.MetadataRevision == nil {
			return nil
		}
		out.MetadataRevision = *header.MetadataRevision
		if before == 0 {
			before = out.MetadataRevision + 1
		}
		const pageSize = 20
		rows, err := tx.Query(ctx, `SELECT revision,actor_id::text,logo_actor,recorded_at,logo FROM public.book_revisions
 WHERE tenant_id=$1 AND book_id=$2 AND logo_actor IS NOT NULL AND revision<$3 ORDER BY revision DESC LIMIT $4`,
			scope.TenantID, scope.BookID, before, pageSize+1)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var rev BookLogoRevision
			if err = rows.Scan(&rev.Revision, &rev.ActorID, &rev.ActorKind, &rev.RecordedAt, &rev.Logo); err != nil {
				return dbError(err)
			}
			if len(out.History) == pageSize {
				last := out.History[pageSize-1].Revision
				out.NextBefore = &last
				break
			}
			out.History = append(out.History, rev)
		}
		return dbError(rows.Err())
	})
	return out, err
}
