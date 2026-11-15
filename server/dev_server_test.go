//go:build devserver && browserpreview

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/loopback"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

// The development Vult: the real server in local-browser mode, on its own
// database, with no test routes. These two operator steps prepare it and fill
// it. Neither is reachable from a browser.
//
//   TestDevSetup  creates the database, its roles and the server's files, and
//                 prints the one-time link where the owner makes their passkey,
//                 as an installed Vult's first run does.
//   TestDevSeed   after the owner's first run, adds the synthetic Books to the
//                 owner's Vult. It refuses to run twice.
//
// VULT_DEV_STATE names the private folder for the server's files,
// VULT_TEST_DSN_FILE the local PostgreSQL admin connection, and VULT_DEV_ORIGIN
// the address (https://dev.vult.localhost:8080).

const devOwnerSubject = "local-owner"

// The database and its roles: vult_dev for the owner's development Vult, or
// another name from VULT_DEV_DATABASE for a throwaway copy, so a check never
// touches the owner's.
var (
	devDatabase    = devName()
	devRuntimeRole = devDatabase + "_runtime"
	devAuthRole    = devDatabase + "_auth"
	devOwnerRole   = devDatabase + "_owner"
)

func devName() string {
	if name := os.Getenv("VULT_DEV_DATABASE"); name != "" {
		return name
	}
	return "vult_dev"
}

type devEnv struct {
	dir, origin string
	root        *pgxpool.Pool
	admin       *pgxpool.Pool
	config      *pgxpool.Config
	secrets     map[string]string
}

func openDevEnv(t *testing.T, create bool) *devEnv {
	t.Helper()
	ctx := context.Background()
	e := &devEnv{dir: os.Getenv("VULT_DEV_STATE"), origin: os.Getenv("VULT_DEV_ORIGIN")}
	u, err := url.Parse(e.origin)
	if e.dir == "" || !filepath.IsAbs(e.dir) || err != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".localhost") || !strings.HasPrefix(devDatabase, "vult_dev") {
		t.Fatal("VULT_DEV_STATE must be an absolute folder and VULT_DEV_ORIGIN an https .localhost address")
	}
	raw, err := os.ReadFile(os.Getenv("VULT_TEST_DSN_FILE"))
	if err != nil {
		t.Fatal("VULT_TEST_DSN_FILE must name the local PostgreSQL admin connection")
	}
	if e.config, err = pgxpool.ParseConfig(strings.TrimSpace(string(raw))); err != nil {
		t.Fatal("invalid database configuration")
	}
	if host := e.config.ConnConfig.Host; host != "127.0.0.1" && host != "localhost" && !strings.HasPrefix(host, "/") {
		t.Fatal("the development database must be local")
	}
	if e.root, err = pgxpool.NewWithConfig(ctx, e.config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.root.Close)
	secretsFile := filepath.Join(e.dir, "roles.json")
	if data, err := os.ReadFile(secretsFile); err == nil {
		if json.Unmarshal(data, &e.secrets) != nil || len(e.secrets) != 3 {
			t.Fatal("unreadable " + secretsFile)
		}
	} else if errors.Is(err, os.ErrNotExist) && create {
		e.secrets = map[string]string{devRuntimeRole: devSecret(), devAuthRole: devSecret(), devOwnerRole: devSecret()}
		data, _ := json.Marshal(e.secrets)
		if err := os.MkdirAll(e.dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(secretsFile, data, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal("run the setup step first")
	}
	return e
}

func devSecret() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (e *devEnv) pool(t *testing.T, user string, conns int32) *pgxpool.Pool {
	t.Helper()
	c := e.config.Copy()
	c.ConnConfig.Database = devDatabase
	c.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if user != "" {
		c.ConnConfig.User, c.ConnConfig.Password = user, e.secrets[user]
	}
	if conns > 0 {
		c.MaxConns = conns
	}
	p, err := pgxpool.NewWithConfig(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// dsn is a connection string the server reads from a private file.
func (e *devEnv) dsn(user string) string {
	c := e.config.ConnConfig
	u := url.URL{Scheme: "postgres", User: url.UserPassword(user, e.secrets[user]), Host: fmt.Sprintf("%s:%d", c.Host, c.Port), Path: "/" + devDatabase, RawQuery: "sslmode=disable"}
	return u.String()
}

func TestDevSetup(t *testing.T) {
	ctx := context.Background()
	e := openDevEnv(t, true)
	for _, name := range []string{devRuntimeRole, devAuthRole, devOwnerRole} {
		var exists bool
		if err := e.root.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)", name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		verb := "CREATE"
		if exists {
			verb = "ALTER"
		}
		attrs := "NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"
		if name == devRuntimeRole {
			attrs += " NOINHERIT"
		}
		if _, err := e.root.Exec(ctx, verb+" ROLE "+pgx.Identifier{name}.Sanitize()+" LOGIN PASSWORD '"+e.secrets[name]+"' "+attrs); err != nil {
			t.Fatal(err)
		}
	}
	var fresh bool
	if err := e.root.QueryRow(ctx, "SELECT NOT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", devDatabase).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	if fresh {
		if _, err := e.root.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{devDatabase}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	admin := e.pool(t, "", 0)
	// The same migration an installed Vult runs when it upgrades: forward
	// only, with the fact check. It never deletes data.
	if _, err := ledger.Migrate(ctx, admin, devRuntimeRole, fresh); err != nil {
		t.Fatalf("the development data can't be moved to this code (%v). Nothing was changed; ask the owner before resetting.", err)
	}
	operator := access.Operator{DB: admin}
	if err := operator.ConfigureRoles(ctx, devRuntimeRole, devAuthRole, devOwnerRole); err != nil {
		t.Fatal(err)
	}
	tenant, err := ledger.Admin{DB: admin}.EnsureTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(e.dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("runtime.dsn", []byte(e.dsn(devRuntimeRole)+"\n"))
	// The native service's own connection, as the installed service has.
	adminURL := url.URL{Scheme: "postgres", User: url.UserPassword(e.config.ConnConfig.User, e.config.ConnConfig.Password),
		Host: fmt.Sprintf("%s:%d", e.config.ConnConfig.Host, e.config.ConnConfig.Port), Path: "/" + devDatabase, RawQuery: "sslmode=disable"}
	write("admin.dsn", []byte(adminURL.String()+"\n"))
	write("auth.dsn", []byte(e.dsn(devAuthRole)+"\n"))
	write("owner.dsn", []byte(e.dsn(devOwnerRole)+"\n"))
	if err := os.MkdirAll(filepath.Join(e.dir, "documents"), 0o700); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(e.dir, "documents-key.json")
	if _, err := os.Stat(keyFile); errors.Is(err, os.ErrNotExist) {
		key, err := documents.NewKeyFile(tenant)
		if err != nil {
			t.Fatal(err)
		}
		write("documents-key.json", key)
	}
	u, _ := url.Parse(e.origin)
	// The address's certificate, made the way an installed Vult makes its own:
	// self-signed, not a certificate authority, for this one name only.
	if _, err := os.Stat(filepath.Join(e.dir, "certificate.pem")); errors.Is(err, os.ErrNotExist) {
		certificate, key, err := loopback.NewCertificate(u.Hostname())
		if err != nil {
			t.Fatal(err)
		}
		write("key.pem", key)
		write("certificate.pem", certificate)
	}
	config := Config{Origin: e.origin, Listen: "127.0.0.1:" + u.Port(), LocalBrowser: true,
		LocalCertificateFile: filepath.Join(e.dir, "certificate.pem"), LocalKeyFile: filepath.Join(e.dir, "key.pem"),
		Authentication: &Authentication{Mode: "passkey", Username: devOwnerSubject, AuthDSNFile: filepath.Join(e.dir, "auth.dsn"), OwnerDSNFile: filepath.Join(e.dir, "owner.dsn")},
		Documents:      &DocumentStorage{Directory: filepath.Join(e.dir, "documents"), KeyFile: keyFile},
		Banks:          &BankConnections{KeyringFile: filepath.Join(e.dir, "bank-keyring.json")}}
	// A development Vult can try a limit on bank logins: bank-limit.json in
	// its folder is the configuration's banks.limit.
	if raw, err := os.ReadFile(filepath.Join(e.dir, "bank-limit.json")); err == nil {
		var limit BankLimitSetting
		if json.Unmarshal(raw, &limit) != nil || limit.validate() != nil {
			t.Fatal("bank-limit.json isn't a valid bank limit")
		}
		config.Banks.Limit = &limit
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	write("config.json", data)

	// The first run: until the owner has made a passkey, a one-time link to
	// make one, exactly as an installed Vult opens after installing.
	var ready bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.passkey_profiles p JOIN access.identities i ON i.id=p.identity_id
 WHERE i.issuer=$1 AND i.subject=$2 AND p.origin=$3 AND p.state='ready')`, access.BuiltinIssuer, devOwnerSubject, e.origin).Scan(&ready); err != nil {
		t.Fatal(err)
	}
	if ready {
		fmt.Println("DEV OWNER READY")
		return
	}
	setup, err := operator.InitializePasskeys(ctx, tenant, devOwnerSubject, e.origin)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("DEV SETUP " + e.origin + "/auth/setup#setup=" + setup.Token)
}

func TestDevSeed(t *testing.T) {
	ctx := context.Background()
	e := openDevEnv(t, false)
	marker := filepath.Join(e.dir, "seeded")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("this development Vult was already seeded; it is never seeded twice")
	}
	admin := e.pool(t, "", 0)
	var tenant string
	err := admin.QueryRow(ctx, `SELECT o.tenant_id::text FROM access.owners o JOIN access.identities i ON i.id=o.identity_id
 JOIN access.passkey_profiles p ON p.identity_id=i.id
 WHERE i.issuer=$1 AND i.subject=$2 AND p.origin=$3 AND p.state='ready' LIMIT 1`, access.BuiltinIssuer, devOwnerSubject, e.origin).Scan(&tenant)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("the owner hasn't finished the first run yet: make the passkey first, then seed")
	}
	if err != nil {
		t.Fatal(err)
	}
	var taken bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.book_revisions WHERE tenant_id=$1 AND name IN ('Eightball Inc.','Kit Blanche'))`, tenant).Scan(&taken); err != nil {
		t.Fatal(err)
	}
	if taken {
		t.Fatal("Eightball Inc. or Kit Blanche already exists here; nothing was added")
	}
	runtime := e.pool(t, devRuntimeRole, 8)
	f := &documentServerFixture{
		db:       &testutil.Database{Context: ctx, Admin: admin, Runtime: runtime, Owner: ledger.Admin{DB: admin}, Store: ledger.Store{Runtime: runtime}, Role: devRuntimeRole, Name: devDatabase},
		operator: access.Operator{DB: admin},
		tenant:   tenant,
		identity: access.Identity{Issuer: access.BuiltinIssuer, Subject: devOwnerSubject, AuthenticatedAt: time.Now().UTC()},
	}
	f.auth, f.owners = e.pool(t, devAuthRole, 0), e.pool(t, devOwnerRole, 0)
	if f.sessions, err = access.New(f.auth, f.owners, e.origin); err != nil {
		t.Fatal(err)
	}
	if f.session, err = f.sessions.IssueSession(ctx, f.identity); err != nil {
		t.Fatal(err)
	}
	disk, err := documents.OpenDisk(filepath.Join(e.dir, "documents"))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := documents.OpenKeyring(filepath.Join(e.dir, "documents-key.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.vault = &documents.Vault{Storage: disk, Keys: keys}
	seedPreview(t, f)
	seedPreviewBank(t, f)
	seedPersonalCardBank(t, f)
	if err := f.sessions.EndSession(ctx, f.session); err != nil {
		t.Log("the seeding session ends on its own")
	}
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("DEV SEEDED")
}
