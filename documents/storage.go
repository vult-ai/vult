// Package documents stores opaque, encrypted originals without parsing them.
package documents

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"filippo.io/age"
)

const MaxSize int64 = 32 << 20

var (
	ErrInvalid     = errors.New("invalid document request")
	ErrUnavailable = errors.New("document storage unavailable")
	ErrIntegrity   = errors.New("document integrity check failed")
	ErrMediaType   = errors.New("unsupported document media type")
	ErrExists      = errors.New("document object already exists")
	objectPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	idPattern      = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	keyPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// Storage publishes a whole object atomically, never a partially written one.
// Create must not replace an existing object. Open and Remove accept opaque
// object IDs, never filenames, paths or URLs supplied by a client.
// ErrExists means the object predated this attempt and must not be removed by
// its failure cleanup. Another create error may follow atomic publication;
// callers retain or clean up their fresh object identity accordingly.
type Storage interface {
	Create(context.Context, string, func(io.Writer) error) error
	Open(context.Context, string) (io.ReadCloser, error)
	Remove(context.Context, string) error
}

// Keys resolves a versioned service-held key, outside the ledger database.
// Implementations must enforce tenant ownership and retain old decryption keys.
type Keys interface {
	Current(context.Context, string) (string, error)
	Recipient(context.Context, string, string) (age.Recipient, error)
	Identity(context.Context, string, string) (age.Identity, error)
}

// Object is the authenticated storage manifest. Ledger authorization must be
// checked before constructing it; possessing this metadata grants no access.
type Object struct {
	TenantID   string `json:"tenant_id"`
	BookID     string `json:"book_id"`
	DocumentID string `json:"document_id"`
	StorageID  string `json:"storage_id"`
	KeyID      string `json:"key_id"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
}

func (o Object) valid() bool {
	return idPattern.MatchString(o.TenantID) && idPattern.MatchString(o.BookID) &&
		idPattern.MatchString(o.DocumentID) && objectPattern.MatchString(o.StorageID) &&
		objectPattern.MatchString(o.SHA256) && keyPattern.MatchString(o.KeyID) &&
		o.Size > 0 && o.Size <= MaxSize
}

type Vault struct {
	Storage Storage
	Keys    Keys
}

// Write authenticates the declared size and hash before publishing ciphertext.
// The encrypted manifest binds the bytes to this tenant, Book and document.
func (v Vault) Write(ctx context.Context, o Object, plain io.Reader) error {
	if !o.valid() || plain == nil {
		return ErrInvalid
	}
	if v.Storage == nil || v.Keys == nil {
		return ErrUnavailable
	}
	key, err := v.Keys.Recipient(ctx, o.TenantID, o.KeyID)
	if err != nil || key == nil {
		return ErrUnavailable
	}
	manifest, err := json.Marshal(o)
	if err != nil {
		return ErrInvalid
	}
	err = v.Storage.Create(ctx, o.StorageID, func(dst io.Writer) error {
		cipher, err := age.Encrypt(dst, key)
		if err != nil {
			return ErrUnavailable
		}
		if err = binary.Write(cipher, binary.BigEndian, uint32(len(manifest))); err != nil {
			return ErrUnavailable
		}
		if _, err = cipher.Write(manifest); err != nil {
			return ErrUnavailable
		}
		hash := sha256.New()
		n, err := io.Copy(io.MultiWriter(cipher, hash), io.LimitReader(contextReader{ctx, plain}, o.Size+1))
		if err != nil {
			return ErrUnavailable
		}
		if n != o.Size || hex.EncodeToString(hash.Sum(nil)) != o.SHA256 {
			return ErrIntegrity
		}
		if err = cipher.Close(); err != nil {
			return ErrUnavailable
		}
		return nil
	})
	return storageError(err)
}

// Read verifies the encrypted manifest before releasing any document bytes.
// Callers must treat the output as incomplete until Read returns nil, and discard
// partial output on error. The final authenticated frame and hash are mandatory.
func (v Vault) Read(ctx context.Context, o Object, dst io.Writer) error {
	if !o.valid() || dst == nil {
		return ErrInvalid
	}
	if v.Storage == nil || v.Keys == nil {
		return ErrUnavailable
	}
	key, err := v.Keys.Identity(ctx, o.TenantID, o.KeyID)
	if err != nil || key == nil {
		return ErrUnavailable
	}
	source, err := v.Storage.Open(ctx, o.StorageID)
	if err != nil {
		return ErrUnavailable
	}
	defer source.Close()
	plain, err := age.Decrypt(contextReader{ctx, io.LimitReader(source, MaxSize+(1<<20))}, key)
	if err != nil {
		return ErrIntegrity
	}
	var length uint32
	if binary.Read(plain, binary.BigEndian, &length) != nil || length == 0 || length > 4096 {
		return ErrIntegrity
	}
	manifest := make([]byte, length)
	if _, err = io.ReadFull(plain, manifest); err != nil {
		return ErrIntegrity
	}
	var actual Object
	if json.Unmarshal(manifest, &actual) != nil || actual != o {
		return ErrIntegrity
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(plain, o.Size+1))
	if err != nil || n != o.Size || hex.EncodeToString(hash.Sum(nil)) != o.SHA256 {
		return ErrIntegrity
	}
	return nil
}

func storageError(err error) error {
	if err == nil {
		return nil
	}
	for _, safe := range []error{ErrInvalid, ErrExists, ErrIntegrity} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ErrUnavailable
}

type contextReader struct {
	ctx context.Context
	src io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(p)
}
