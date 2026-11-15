package documents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"

	"filippo.io/age"
	"github.com/vult-ai/vult/internal/strictjson"
)

// BookKeyID is the key ID of a document sealed with its Book's own key.
// Destroying that key leaves every copy of the Book's documents unreadable,
// backups included.
const BookKeyID = "book-v1"

// ErrBookErased is a key asked for after its Book was erased.
var ErrBookErased = errors.New("the Book was erased")

// BookKeys holds one key per Book, outside the database. A key is made when
// the Book first stores a document and destroyed when the Book is erased;
// after that it is never made again.
type BookKeys interface {
	ProvisionBook(ctx context.Context, tenant, book string) error
	BookRecipient(ctx context.Context, tenant, book string) (age.Recipient, error)
	BookIdentity(ctx context.Context, tenant, book string) (age.Identity, error)
	DestroyBook(ctx context.Context, tenant, book string) error
}

type bookKeyFile struct {
	Format string `json:"format"`
	Tenant string `json:"tenant"`
	Book   string `json:"book"`
	Key    string `json:"key,omitempty"`
}

func bookObject(kind, tenant, book string) string {
	sum := sha256.Sum256([]byte(kind + ":" + tenant + ":" + book))
	return hex.EncodeToString(sum[:])
}

func (k *KeyDirectory) erased(tenant, book string) (bool, error) {
	_, err := k.disk.root.Lstat(bookObject("document-book-erased", tenant, book))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, ErrUnavailable
}

func (k *KeyDirectory) bookIdentity(ctx context.Context, tenant, book string) (*age.X25519Identity, error) {
	if !idPattern.MatchString(tenant) || !idPattern.MatchString(book) {
		return nil, ErrInvalid
	}
	if gone, err := k.erased(tenant, book); err != nil || gone {
		if gone {
			return nil, ErrBookErased
		}
		return nil, err
	}
	file, err := k.disk.Open(ctx, bookObject("document-book-key", tenant, book))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer clear(data)
	var f bookKeyFile
	if len(data) > 4096 || strictjson.Decode(data, &f) != nil || f.Format != "vult-book-key-v1" || f.Tenant != tenant || f.Book != book {
		return nil, ErrUnavailable
	}
	identity, err := age.ParseX25519Identity(f.Key)
	if err != nil {
		return nil, ErrUnavailable
	}
	return identity, nil
}

// ProvisionBook makes the Book's key if it has none. It never replaces a
// key, and never makes one for an erased Book.
func (k *KeyDirectory) ProvisionBook(ctx context.Context, tenant, book string) error {
	if !idPattern.MatchString(tenant) || !idPattern.MatchString(book) {
		return ErrInvalid
	}
	if gone, err := k.erased(tenant, book); err != nil || gone {
		if gone {
			return ErrBookErased
		}
		return err
	}
	object := bookObject("document-book-key", tenant, book)
	if _, err := k.disk.root.Lstat(object); err == nil {
		_, err = k.bookIdentity(ctx, tenant, book)
		return err
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return ErrUnavailable
	}
	data, err := json.Marshal(bookKeyFile{Format: "vult-book-key-v1", Tenant: tenant, Book: book, Key: identity.String()})
	if err != nil {
		return ErrUnavailable
	}
	defer clear(data)
	err = k.disk.Create(ctx, object, func(dst io.Writer) error { _, err := dst.Write(data); return err })
	if errors.Is(err, ErrExists) {
		_, err = k.bookIdentity(ctx, tenant, book)
	}
	return err
}

func (k *KeyDirectory) BookRecipient(ctx context.Context, tenant, book string) (age.Recipient, error) {
	identity, err := k.bookIdentity(ctx, tenant, book)
	if err != nil {
		return nil, err
	}
	return identity.Recipient(), nil
}

func (k *KeyDirectory) BookIdentity(ctx context.Context, tenant, book string) (age.Identity, error) {
	identity, err := k.bookIdentity(ctx, tenant, book)
	if err != nil {
		return nil, err
	}
	return identity, nil
}

// DestroyBook marks the Book erased, then removes its key. Repeating it is
// harmless; after it, no key is made for the Book again.
func (k *KeyDirectory) DestroyBook(ctx context.Context, tenant, book string) error {
	if !idPattern.MatchString(tenant) || !idPattern.MatchString(book) {
		return ErrInvalid
	}
	marker, _ := json.Marshal(bookKeyFile{Format: "vult-book-erased-v1", Tenant: tenant, Book: book})
	err := k.disk.Create(ctx, bookObject("document-book-erased", tenant, book), func(dst io.Writer) error { _, err := dst.Write(marker); return err })
	if err != nil && !errors.Is(err, ErrExists) {
		return ErrUnavailable
	}
	if err := k.disk.Remove(ctx, bookObject("document-book-key", tenant, book)); err != nil {
		if _, statErr := k.disk.root.Lstat(bookObject("document-book-key", tenant, book)); !errors.Is(statErr, os.ErrNotExist) {
			return ErrUnavailable
		}
	}
	return nil
}

// KeyFor names the key a new document of this Book is sealed with: the
// Book's own key where the key store keeps one, else the Vult's current key.
func (v Vault) KeyFor(ctx context.Context, tenant, book string) (string, error) {
	if v.Keys == nil {
		return "", ErrUnavailable
	}
	if keys, ok := v.Keys.(BookKeys); ok {
		if err := keys.ProvisionBook(ctx, tenant, book); err != nil {
			return "", ErrUnavailable
		}
		return BookKeyID, nil
	}
	return v.Keys.Current(ctx, tenant)
}

// DestroyBookKey destroys the Book's own key, where there is one.
func (v Vault) DestroyBookKey(ctx context.Context, tenant, book string) error {
	if keys, ok := v.Keys.(BookKeys); ok {
		return keys.DestroyBook(ctx, tenant, book)
	}
	return nil
}

func (v Vault) recipient(ctx context.Context, o Object) (age.Recipient, error) {
	if o.KeyID == BookKeyID {
		if keys, ok := v.Keys.(BookKeys); ok {
			return keys.BookRecipient(ctx, o.TenantID, o.BookID)
		}
		return nil, ErrUnavailable
	}
	return v.Keys.Recipient(ctx, o.TenantID, o.KeyID)
}

func (v Vault) identity(ctx context.Context, o Object) (age.Identity, error) {
	if o.KeyID == BookKeyID {
		if keys, ok := v.Keys.(BookKeys); ok {
			return keys.BookIdentity(ctx, o.TenantID, o.BookID)
		}
		return nil, ErrUnavailable
	}
	return v.Keys.Identity(ctx, o.TenantID, o.KeyID)
}
