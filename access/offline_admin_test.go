package access_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func TestOfflineIdentityRevocationInvalidatesOnlyChosenCredentials(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	first, firstSession, identity := f.owner(t, "alpha")
	other, otherSession, _ := f.owner(t, "beta")
	book, otherBook := f.book(t, first, firstSession), f.book(t, other, otherSession)
	client := f.client(t)
	tokens := f.tokens(t, client, firstSession, first, book.ID, "write", "")
	otherTokens := f.tokens(t, client, otherSession, other, otherBook.ID, "read", "")

	directory := t.TempDir()
	binary := filepath.Join(directory, "vult-server-admin")
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("Go must be available on PATH for the offline command test", err)
	}
	build := exec.CommandContext(ctx, goBinary, "build", "-trimpath", "-buildvcs=false", "-o", binary, "./cmd/vult-server-admin")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("offline command build failed: %v\n%s", err, output)
	}
	dsn, input, output := filepath.Join(directory, "operator.dsn"), filepath.Join(directory, "input.json"), filepath.Join(directory, "result.json")
	if err := os.WriteFile(dsn, []byte(testutil.DSN(f.db.Admin)), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(id access.Identity) error {
		t.Helper()
		payload, err := json.Marshal(map[string]string{"issuer": id.Issuer, "subject": id.Subject})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(input, payload, 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, binary, "revoke-identity", "--dsn-file", dsn, "--input", input, "--output", output)
		console, err := command.CombinedOutput()
		if bytes.Contains(console, []byte(id.Subject)) || bytes.Contains(console, []byte(id.Issuer)) {
			t.Fatal("identity details reached console")
		}
		return err
	}
	for _, id := range []access.Identity{{Issuer: identity.Issuer, Subject: "unknown"}, {Issuer: "http://identity.example.com", Subject: identity.Subject}} {
		if run(id) == nil {
			t.Fatal("unknown or invalid identity succeeded")
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatal("failed revocation published a success result", err)
		}
		if _, err := f.service.Identity(ctx, firstSession); err != nil {
			t.Fatal("invalid command revoked a live identity", err)
		}
		if _, err := f.service.ResolveAgent(ctx, tokens.Access); err != nil {
			t.Fatal("invalid command revoked an agent", err)
		}
	}
	if err := run(identity); err != nil {
		t.Fatal("offline revocation failed", err)
	}
	result, err := os.ReadFile(output)
	if err != nil || !bytes.Contains(result, []byte(`"status":"identity_credentials_revoked"`)) {
		t.Fatal("private revocation result missing", err)
	}
	if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("revocation result is not private", err)
	}
	if _, err := f.service.Identity(ctx, firstSession); !errors.Is(err, access.ErrDenied) {
		t.Fatal("revoked identity kept its browser session", err)
	}
	if _, err := f.service.ResolveAgent(ctx, tokens.Access); !errors.Is(err, access.ErrDenied) {
		t.Fatal("revoked identity kept its agent access", err)
	}
	if _, err := f.service.Exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {client.ID}, "resource": {f.service.Resource()}, "refresh_token": {tokens.Refresh}}); !errors.Is(err, access.ErrDenied) {
		t.Fatal("revoked identity refreshed its agent access", err)
	}
	if _, err := f.service.Identity(ctx, otherSession); err != nil {
		t.Fatal("revocation crossed identities", err)
	}
	if _, err := f.service.ResolveAgent(ctx, otherTokens.Access); err != nil {
		t.Fatal("revocation crossed agent credentials", err)
	}
	if err := f.service.WithOwner(ctx, otherSession, other, false, func(owner ledger.TenantAdmin) error {
		_, err := owner.Book(ctx, otherBook.ID)
		return err
	}); err != nil {
		t.Fatal("unrelated ownership changed", err)
	}
	// Revocation preserves ownership and does not replace provider-side disable.
	freshSession, err := f.service.IssueSession(ctx, identity)
	if err != nil {
		t.Fatal("fresh verified authentication was disabled", err)
	}
	if err = f.service.WithOwner(ctx, freshSession, first, false, func(owner ledger.TenantAdmin) error {
		_, err := owner.Book(ctx, book.ID)
		return err
	}); err != nil {
		t.Fatal("revocation changed the Book or ownership", err)
	}
	if run(identity) == nil {
		t.Fatal("operator output was overwritten")
	}
	if _, err = f.service.Identity(ctx, freshSession); err != nil {
		t.Fatal("existing output did not stop the second revocation", err)
	}
}
