package documents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

type testKeys struct {
	tenant string
	keys   map[string]*age.X25519Identity
}

func (k testKeys) Current(_ context.Context, tenant string) (string, error) {
	if tenant != k.tenant {
		return "", ErrUnavailable
	}
	return "current", nil
}
func (k testKeys) Recipient(ctx context.Context, tenant, version string) (age.Recipient, error) {
	identity, err := k.Identity(ctx, tenant, version)
	if err != nil {
		return nil, err
	}
	return identity.(*age.X25519Identity).Recipient(), nil
}
func (k testKeys) Identity(_ context.Context, tenant, version string) (age.Identity, error) {
	if tenant != k.tenant || k.keys[version] == nil {
		return nil, errors.New("private key path must not escape")
	}
	return k.keys[version], nil
}

func vaultFixture(t *testing.T, plain []byte) (Vault, Object, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { disk.Close() })
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(plain)
	o := Object{
		TenantID: "11111111-1111-4111-8111-111111111111", BookID: "22222222-2222-4222-8222-222222222222",
		DocumentID: "33333333-3333-4333-8333-333333333333", StorageID: strings.Repeat("a", 64),
		KeyID: "current", SHA256: hex.EncodeToString(hash[:]), Size: int64(len(plain)),
	}
	return Vault{disk, testKeys{o.TenantID, map[string]*age.X25519Identity{"current": identity}}}, o, dir
}

func TestEncryptedOriginalRoundTrip(t *testing.T) {
	plain := bytes.Repeat([]byte("synthetic original bytes\x00\xff"), 210000)
	v, o, dir := vaultFixture(t, plain)
	if err := v.Write(context.Background(), o, bytes.NewReader(plain)); err != nil {
		t.Fatal(err)
	}
	cipher, err := os.ReadFile(filepath.Join(dir, o.StorageID))
	if err != nil || bytes.Contains(cipher, []byte("synthetic original")) || bytes.Contains(cipher, []byte(o.TenantID)) {
		t.Fatal("original or manifest was not encrypted", err)
	}
	var out bytes.Buffer
	if err := v.Read(context.Background(), o, &out); err != nil || !bytes.Equal(out.Bytes(), plain) {
		t.Fatal("original bytes changed", err)
	}
	if err := v.Write(context.Background(), o, bytes.NewReader(plain)); !errors.Is(err, ErrExists) {
		t.Fatal("existing object was overwritten", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatal("temporary ciphertext was left behind", err)
	}
}

func TestEncryptedManifestRejectsOtherIdentityBeforeOutput(t *testing.T) {
	plain := []byte("synthetic original")
	v, original, _ := vaultFixture(t, plain)
	if err := v.Write(context.Background(), original, bytes.NewReader(plain)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"tenant", "book", "document", "size", "hash", "key"} {
		t.Run(field, func(t *testing.T) {
			o := original
			keys := v.Keys.(testKeys)
			switch field {
			case "tenant":
				o.TenantID = "44444444-4444-4444-8444-444444444444"
				// Even reusing a key across tenants cannot swap encrypted objects.
				keys.tenant = o.TenantID
			case "book":
				o.BookID = "44444444-4444-4444-8444-444444444444"
			case "document":
				o.DocumentID = "44444444-4444-4444-8444-444444444444"
			case "size":
				o.Size++
			case "hash":
				o.SHA256 = strings.Repeat("b", 64)
			case "key":
				o.KeyID = "previous"
				keys.keys = map[string]*age.X25519Identity{"previous": keys.keys["current"]}
			}
			vault := Vault{v.Storage, keys}
			var out bytes.Buffer
			if err := vault.Read(context.Background(), o, &out); !errors.Is(err, ErrIntegrity) || out.Len() != 0 {
				t.Fatal("mismatched manifest released bytes", err)
			}
		})
	}
}

func TestIncompleteAndWrongUploadsNeverPublish(t *testing.T) {
	for _, condition := range []string{"short", "long", "hash", "reader failure", "canceled"} {
		t.Run(condition, func(t *testing.T) {
			plain := bytes.Repeat([]byte("synthetic bytes"), 10000)
			v, o, dir := vaultFixture(t, plain)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var input io.Reader = bytes.NewReader(plain)
			switch condition {
			case "short":
				input = bytes.NewReader(plain[:len(plain)-1])
			case "long":
				input = bytes.NewReader(append(plain, 'x'))
			case "hash":
				o.SHA256 = strings.Repeat("f", 64)
			case "reader failure":
				input = io.MultiReader(bytes.NewReader(plain[:100]), brokenReader{})
			case "canceled":
				cancel()
			}
			if err := v.Write(ctx, o, input); err == nil {
				t.Fatal("bad upload succeeded")
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Fatal("bad upload published or left temporary bytes", err)
			}
		})
	}
}

func TestOldKeyRemainsReadableAfterRotation(t *testing.T) {
	plain := []byte("synthetic original")
	v, old, _ := vaultFixture(t, plain)
	keys := v.Keys.(testKeys)
	keys.keys["previous"] = keys.keys["current"]
	old.KeyID = "previous"
	if err := v.Write(context.Background(), old, bytes.NewReader(plain)); err != nil {
		t.Fatal(err)
	}
	rotated, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	keys.keys["current"] = rotated
	latest := old
	latest.KeyID, latest.StorageID = "current", strings.Repeat("c", 64)
	latest.DocumentID = "55555555-5555-4555-8555-555555555555"
	if err := v.Write(context.Background(), latest, bytes.NewReader(plain)); err != nil {
		t.Fatal(err)
	}
	for _, original := range []Object{old, latest} {
		var out bytes.Buffer
		if err := v.Read(context.Background(), original, &out); err != nil || !bytes.Equal(out.Bytes(), plain) {
			t.Fatal("rotation made a retained original unreadable", err)
		}
	}
	delete(keys.keys, "previous")
	if err := v.Read(context.Background(), old, io.Discard); err != ErrUnavailable {
		t.Fatal("missing historical key did not fail closed", err)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("private source name") }

func TestCorruptedAndTruncatedCiphertextFails(t *testing.T) {
	for _, condition := range []string{"header", "middle", "last", "truncated", "trailing"} {
		t.Run(condition, func(t *testing.T) {
			plain := bytes.Repeat([]byte("synthetic bytes"), 10000)
			v, o, dir := vaultFixture(t, plain)
			if err := v.Write(context.Background(), o, bytes.NewReader(plain)); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, o.StorageID)
			cipher, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch condition {
			case "header":
				cipher[0] ^= 1
			case "middle":
				cipher[len(cipher)/2] ^= 1
			case "last":
				cipher[len(cipher)-1] ^= 1
			case "truncated":
				cipher = cipher[:len(cipher)-1]
			case "trailing":
				cipher = append(cipher, []byte("unexpected bytes")...)
			}
			if err := os.WriteFile(path, cipher, 0600); err != nil {
				t.Fatal(err)
			}
			if err := v.Read(context.Background(), o, io.Discard); !errors.Is(err, ErrIntegrity) {
				t.Fatal("ciphertext integrity failure was accepted", err)
			}
		})
	}
}

func TestDiskRejectsTraversalLinksAndSharedPermissions(t *testing.T) {
	v, o, dir := vaultFixture(t, []byte("synthetic original"))
	ctx := context.Background()
	for _, invalid := range []string{"../outside", "/outside", "a/b", "", strings.Repeat("x", 64)} {
		if _, err := v.Storage.Open(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid object name accepted", err)
		}
		if err := v.Storage.Create(ctx, invalid, func(io.Writer) error { return nil }); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid output name accepted", err)
		}
		if err := v.Storage.Remove(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid removal name accepted", err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("must stay outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, o.StorageID)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Storage.Open(ctx, o.StorageID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("symlink was followed", err)
	}
	if err := v.Write(ctx, o, strings.NewReader("synthetic original")); !errors.Is(err, ErrExists) {
		t.Fatal("symlink was replaced", err)
	}
	if err := os.Remove(filepath.Join(dir, o.StorageID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(dir, o.StorageID)); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Storage.Open(ctx, o.StorageID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("hard-linked object was read", err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if disk, err := OpenDisk(dir); err == nil {
		disk.Close()
		t.Fatal("shared storage directory accepted")
	}
}

func TestStorageFailuresAndKeyErrorsDoNotLeakDetails(t *testing.T) {
	plain := []byte("synthetic original")
	v, o, _ := vaultFixture(t, plain)
	o.KeyID = "missing"
	if err := v.Write(context.Background(), o, bytes.NewReader(plain)); err != ErrUnavailable {
		t.Fatal("key provider details escaped", err)
	}
	if err := v.Read(context.Background(), o, io.Discard); err != ErrUnavailable {
		t.Fatal("key provider details escaped", err)
	}
	o.KeyID = "current"
	v.Storage = unavailableStorage{}
	if err := v.Write(context.Background(), o, bytes.NewReader(plain)); err != ErrUnavailable {
		t.Fatal("storage details escaped", err)
	}
	if err := v.Read(context.Background(), o, io.Discard); err != ErrUnavailable {
		t.Fatal("storage details escaped", err)
	}
}

type unavailableStorage struct{}

func (unavailableStorage) Create(_ context.Context, _ string, write func(io.Writer) error) error {
	return write(failingWriter{})
}
func (unavailableStorage) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("private storage path")
}
func (unavailableStorage) Remove(context.Context, string) error { return nil }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("private full disk path") }

type wrappedStorageError struct {
	Storage
	err error
}

func (s wrappedStorageError) Create(context.Context, string, func(io.Writer) error) error {
	return fmt.Errorf("private object path: %w", s.err)
}

func TestWrappedStorageErrorsRemainCanonical(t *testing.T) {
	plain := []byte("synthetic original")
	v, o, _ := vaultFixture(t, plain)
	for _, expected := range []error{ErrInvalid, ErrExists, ErrIntegrity} {
		t.Run(expected.Error(), func(t *testing.T) {
			wrapped := Vault{wrappedStorageError{Storage: v.Storage, err: expected}, v.Keys}
			if err := wrapped.Write(context.Background(), o, bytes.NewReader(plain)); err != expected {
				t.Fatal("storage provider leaked a wrapped error", err)
			}
			if err := v.Storage.Create(context.Background(), o.StorageID, func(io.Writer) error {
				return fmt.Errorf("private callback path: %w", expected)
			}); err != expected {
				t.Fatal("disk callback leaked a wrapped error", err)
			}
		})
	}
}

func TestInvalidBoundsAndUnavailableVault(t *testing.T) {
	v, original, dir := vaultFixture(t, []byte("synthetic original"))
	for _, size := range []int64{0, -1, MaxSize + 1} {
		o := original
		o.Size = size
		if err := v.Write(context.Background(), o, brokenReader{}); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid size reached storage", err)
		}
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatal("invalid input touched storage", err)
	}
	if err := (Vault{}).Write(context.Background(), original, strings.NewReader("synthetic original")); err != ErrUnavailable {
		t.Fatal("unconfigured vault was available", err)
	}
}
