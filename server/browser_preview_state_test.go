//go:build browserpreview

package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crypto/rand"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// The kept preview uses one database, its roles, the synthetic owner and a
// document folder that all survive restarts. Only the preview uses it; tests
// keep their own throwaway databases.
const (
	previewDatabase    = "vult_preview"
	previewRuntimeRole = "vult_preview_runtime"
	previewAuthRole    = "vult_preview_auth"
	previewOwnerRole   = "vult_preview_owner"
)

type previewSecrets struct {
	Runtime string `json:"runtime"`
	Auth    string `json:"auth"`
	Owner   string `json:"owner"`
}

func previewSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// keptPreview opens the preview kept in dir (VULT_PREVIEW_STATE), creating it
// on first use. fresh is true when it was just created and needs its synthetic
// Books.
func keptPreview(t *testing.T, dir string) (f *documentServerFixture, fresh bool) {
	t.Helper()
	ctx := context.Background()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := os.Getenv("VULT_TEST_DSN_FILE")
	raw, err := os.ReadFile(file)
	if err != nil || os.Getenv("VULT_TEST_ISOLATED") != "1" {
		t.Fatal("VULT_TEST_DSN_FILE and VULT_TEST_ISOLATED=1 must name the preview's own PostgreSQL")
	}
	config, err := pgxpool.ParseConfig(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal("invalid preview database configuration")
	}
	if host := config.ConnConfig.Host; host != "127.0.0.1" && host != "localhost" && !strings.HasPrefix(host, "/") {
		t.Fatal("the preview database must be local")
	}
	root, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	secretsFile := filepath.Join(dir, "roles.json")
	var secrets previewSecrets
	if data, err := os.ReadFile(secretsFile); err == nil {
		if json.Unmarshal(data, &secrets) != nil || secrets.Runtime == "" || secrets.Auth == "" || secrets.Owner == "" {
			t.Fatal("unreadable " + secretsFile)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		secrets = previewSecrets{previewSecret(), previewSecret(), previewSecret()}
		data, _ := json.Marshal(secrets)
		if err := os.WriteFile(secretsFile, data, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal(err)
	}
	role := func(name, password, attributes string) {
		var exists bool
		if err := root.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)", name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		verb := "ALTER"
		if !exists {
			verb = "CREATE"
		}
		if _, err := root.Exec(ctx, verb+" ROLE "+pgx.Identifier{name}.Sanitize()+" LOGIN PASSWORD '"+password+"' "+attributes); err != nil {
			t.Fatal(err)
		}
	}
	role(previewRuntimeRole, secrets.Runtime, "NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS")
	role(previewAuthRole, secrets.Auth, "NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS")
	role(previewOwnerRole, secrets.Owner, "NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS")
	if err := root.QueryRow(ctx, "SELECT NOT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", previewDatabase).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	if fresh {
		if _, err := root.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{previewDatabase}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}

	adminConfig := config.Copy()
	adminConfig.ConnConfig.Database = previewDatabase
	adminConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	// Kept data moves forward exactly as an installed Vult upgrades: the
	// maintenance migration, forward only, with the fact check. A new database
	// is initialized the same way. Nothing here ever deletes data: if the
	// code's migrations no longer match the ones the data was made with (an
	// unreleased migration was edited), the preview stops and says so, and the
	// backup preview.sh took before starting is kept.
	if result, err := ledger.Migrate(ctx, admin, previewRuntimeRole, fresh); err != nil {
		t.Fatalf("preview data can't be moved to this code (%v). Nothing was changed. "+
			"This happens when a migration the data already has was edited; a release never does that. "+
			"The data and its backups are kept in the preview folder; ask the owner before resetting.", err)
	} else if result.To != result.From && !fresh {
		t.Logf("preview data moved forward from migration %d to %d", result.From, result.To)
	}
	pool := func(user, password string, conns int32) *pgxpool.Pool {
		c := adminConfig.Copy()
		c.ConnConfig.User, c.ConnConfig.Password = user, password
		if conns > 0 {
			c.MaxConns = conns
		}
		p, err := pgxpool.NewWithConfig(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	runtime := pool(previewRuntimeRole, secrets.Runtime, 8)
	f = &documentServerFixture{
		db:       &testutil.Database{Context: ctx, Admin: admin, Runtime: runtime, Owner: ledger.Admin{DB: admin}, Store: ledger.Store{Runtime: runtime}, Role: previewRuntimeRole, Name: previewDatabase},
		operator: access.Operator{DB: admin},
	}
	if err := f.operator.ConfigureRoles(ctx, previewRuntimeRole, previewAuthRole, previewOwnerRole); err != nil {
		t.Fatal(err)
	}
	f.auth, f.owners = pool(previewAuthRole, secrets.Auth, 0), pool(previewOwnerRole, secrets.Owner, 0)
	if f.sessions, err = access.New(f.auth, f.owners, configuration().Origin); err != nil {
		t.Fatal(err)
	}
	f.identity = access.Identity{Issuer: "https://identity.example.com", Subject: "example", AuthenticatedAt: time.Now().UTC()}
	err = admin.QueryRow(ctx, `SELECT o.tenant_id::text FROM access.owners o JOIN access.identities i ON i.id=o.identity_id
 WHERE i.issuer=$1 AND i.subject=$2 ORDER BY o.tenant_id LIMIT 1`, f.identity.Issuer, f.identity.Subject).Scan(&f.tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		f.tenant, err = f.operator.BindOwner(ctx, "", f.identity)
	}
	if err != nil {
		t.Fatal(err)
	}
	if f.session, err = f.sessions.IssueSession(ctx, f.identity); err != nil {
		t.Fatal(err)
	}
	if fresh {
		if err = f.sessions.WithOwner(ctx, f.session, f.tenant, true, func(owner ledger.TenantAdmin) error {
			f.book, err = owner.CreateBook(ctx, "Example Book", "USD", 2)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Originals and their key live beside the database, so sources survive too.
	f.dir = filepath.Join(dir, "documents")
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dir, "documents-key.json")
	keyJSON, err := os.ReadFile(keyFile)
	if errors.Is(err, os.ErrNotExist) {
		if keyJSON, err = documents.NewKeyFile(f.tenant); err == nil {
			err = os.WriteFile(keyFile, keyJSON, 0o600)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.ParseKeyring(keyJSON)
	if err != nil {
		t.Fatal(err)
	}
	f.vault = &documents.Vault{Storage: disk, Keys: keys}
	human, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	if f.server, err = New(configuration(), Options{Database: runtime, Access: &Access{Sessions: f.sessions, HTTP: human}, Documents: f.vault}); err != nil {
		t.Fatal(err)
	}
	return f, fresh
}
