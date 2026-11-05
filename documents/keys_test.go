package documents

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
)

func TestKeyringScopesVersionsAndRetainsOldKeys(t *testing.T) {
	ctx := context.Background()
	a := "11111111-1111-4111-8111-111111111111"
	b := "22222222-2222-4222-8222-222222222222"
	old, _ := age.GenerateX25519Identity()
	next, _ := age.GenerateX25519Identity()
	other, _ := age.GenerateX25519Identity()
	data, _ := json.Marshal(KeyFile{Format: "vult-document-keys-v1", Tenants: map[string]TenantKeys{
		a: {Active: "next", Keys: map[string]string{"old": old.String(), "next": next.String()}},
		b: {Active: "primary", Keys: map[string]string{"primary": other.String()}},
	}})
	ring, err := ParseKeyring(data)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := ring.Current(ctx, a); err != nil || version != "next" {
		t.Fatal("wrong active key", err)
	}
	for _, version := range []string{"old", "next"} {
		identity, err := ring.Identity(ctx, a, version)
		if err != nil || identity == nil {
			t.Fatal("retained key unavailable", err)
		}
		recipient, err := ring.Recipient(ctx, a, version)
		if err != nil || recipient == nil {
			t.Fatal("recipient unavailable", err)
		}
	}
	for _, pair := range [][2]string{{b, "old"}, {a, "primary"}, {"33333333-3333-4333-8333-333333333333", "next"}} {
		if _, err := ring.Identity(ctx, pair[0], pair[1]); err != ErrUnavailable {
			t.Fatal("key escaped tenant/version scope", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := ring.Identity(canceled, a, "old"); err != ErrUnavailable {
		t.Fatal("canceled key lookup continued", err)
	}
}

func TestKeyFileFailsClosedAndRequiresPrivateRegularFile(t *testing.T) {
	tenant := "11111111-1111-4111-8111-111111111111"
	data, err := NewKeyFile(tenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{nil, []byte(`{}`), bytes.Repeat([]byte(" "), (1<<20)+1), bytes.Replace(data, []byte(`"primary"`), []byte(`"missing"`), 1), bytes.Replace(data, []byte(`"format":`), []byte(`"unknown":true,"format":`), 1), bytes.Replace(data, []byte(tenant), []byte("invalid"), 1)} {
		if _, err := ParseKeyring(bad); err != ErrUnavailable {
			t.Fatal("malformed key file accepted", err)
		}
	}
	if _, err := NewKeyFile("invalid"); err != ErrInvalid {
		t.Fatal("invalid tenant accepted", err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "keys.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenKeyring(file); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"symlink", "hardlink", "public", "missing", "directory"} {
		t.Run(variant, func(t *testing.T) {
			path := filepath.Join(dir, variant)
			switch variant {
			case "symlink":
				if err := os.Symlink(file, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(file, path); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(path)
			case "public":
				if err := os.WriteFile(path, data, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := OpenKeyring(path); err != ErrUnavailable {
				t.Fatal("unsafe key path accepted", err)
			}
		})
	}
}
