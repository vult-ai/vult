// vult-server-admin is offline installation and recovery tooling.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/fssecurity"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

var errOperation = errors.New("offline operation unavailable; inspect configuration and retry only after checking the recorded outcome")

type request struct {
	RuntimeRole     string `json:"runtime_role,omitempty"`
	AuthRole        string `json:"auth_role,omitempty"`
	OwnerRole       string `json:"owner_role,omitempty"`
	TenantID        string `json:"tenant_id,omitempty"`
	Issuer          string `json:"issuer,omitempty"`
	Subject         string `json:"subject,omitempty"`
	Username        string `json:"username,omitempty"`
	Passphrase      string `json:"passphrase,omitempty"`
	KeyFile         string `json:"key_file,omitempty"`
	Name            string `json:"name,omitempty"`
	Currency        string `json:"currency,omitempty"`
	Scale           int    `json:"scale,omitempty"`
	BookID          string `json:"book_id,omitempty"`
	Directory       string `json:"directory,omitempty"`
	KeyDirectory    string `json:"key_directory,omitempty"`
	RecoveryKeyFile string `json:"recovery_key_file,omitempty"`
	ArchiveFile     string `json:"archive_file,omitempty"`
	Origin          string `json:"origin,omitempty"`
}

func openDatabase(ctx context.Context, path string) (*pgxpool.Pool, error) {
	b, e := server.ReadPrivateFile(path, 16<<10)
	if e != nil {
		return nil, errOperation
	}
	c, e := pgxpool.ParseConfig(strings.TrimSpace(string(b)))
	clear(b)
	if e != nil {
		return nil, errOperation
	}
	ip, _ := netip.ParseAddr(c.ConnConfig.Host)
	local := ip.IsLoopback() || strings.HasPrefix(c.ConnConfig.Host, "/")
	if !local && (c.ConnConfig.TLSConfig == nil || c.ConnConfig.TLSConfig.InsecureSkipVerify || c.ConnConfig.TLSConfig.ServerName == "" || len(c.ConnConfig.Fallbacks) != 0) {
		return nil, errOperation
	}
	c.MaxConns, c.MinConns = 1, 0
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	c.ConnConfig.RuntimeParams["application_name"] = "vult-server-admin"
	db, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, errOperation
	}
	if db.Ping(ctx) != nil {
		db.Close()
		return nil, errOperation
	}
	return db, nil
}

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errOperation
	}
	command := args[0]
	if command == "inspect-import" {
		return inspectImport(ctx, args[1:], out)
	}
	if command == "--help" {
		_, _ = fmt.Fprintln(out, "Usage: vult-server-admin migrate|bind-owner|revoke-identity|init|recover|passkey-setup|passkey-recover|book|document-key|expire-documents|backup|restore|export --dsn-file FILE --input FILE --output FILE")
		_, _ = fmt.Fprintln(out, "       vult-server-admin inspect-import --archive FILE --output FILE")
		return nil
	}
	if command != "migrate" && command != "bind-owner" && command != "revoke-identity" && command != "init" && command != "recover" && command != "passkey-setup" && command != "passkey-recover" && command != "book" && command != "document-key" && command != "expire-documents" && command != "backup" && command != "restore" && command != "export" {
		return errOperation
	}
	f := flag.NewFlagSet("vult-server-admin", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	dsn := f.String("dsn-file", "", "private operator database URL file")
	input := f.String("input", "", "private JSON input file")
	output := f.String("output", "", "new private result file")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || *dsn == "" || *input == "" || *output == "" {
		return errOperation
	}
	b, e := server.ReadPrivateFile(*input, 8192)
	if e != nil {
		return errOperation
	}
	var q request
	e = strictjson.Decode(b, &q)
	clear(b)
	if e != nil {
		return errOperation
	}
	if command == "backup" || command == "restore" || command == "export" {
		return runArchive(ctx, command, *dsn, q, *output, out)
	}
	// Reserve the private output before any write. Never overwrite a prior result.
	file, e := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return errOperation
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			os.Remove(*output)
		}
	}()
	if !fssecurity.NoExtendedACL(file) {
		return errOperation
	}
	db, e := openDatabase(ctx, *dsn)
	if e != nil {
		return e
	}
	defer db.Close()
	operator := access.Operator{DB: db}
	var result any
	switch command {
	case "migrate":
		if e = ledger.Apply(ctx, db, q.RuntimeRole); e == nil {
			e = operator.ConfigureRoles(ctx, q.RuntimeRole, q.AuthRole, q.OwnerRole)
		}
		result = map[string]bool{"ready": e == nil}
	case "bind-owner":
		var tenant string
		tenant, e = operator.BindOwner(ctx, q.TenantID, access.Identity{Issuer: q.Issuer, Subject: q.Subject})
		result = map[string]string{"tenant_id": tenant}
	case "revoke-identity":
		e = operator.RevokeIdentity(ctx, access.Identity{Issuer: q.Issuer, Subject: q.Subject})
		result = map[string]string{"status": "identity_credentials_revoked"}
	case "init", "recover":
		var key []byte
		key, e = server.ReadPrivateFile(q.KeyFile, 32)
		if e != nil || len(key) != 32 {
			return errOperation
		}
		defer clear(key)
		if command == "init" {
			result, e = operator.InitializeBuiltin(ctx, q.TenantID, q.Username, q.Passphrase, key)
		} else {
			result, e = operator.ResetBuiltin(ctx, q.Username, q.Passphrase, key)
		}
	case "passkey-setup", "passkey-recover":
		if q.Passphrase != "" || q.KeyFile != "" {
			return errOperation
		}
		var setup access.PasskeySetup
		if command == "passkey-setup" {
			setup, e = operator.InitializePasskeys(ctx, q.TenantID, q.Username, q.Origin)
		} else {
			setup, e = operator.ResetPasskeys(ctx, q.Username, q.Origin)
		}
		// The owner receives recovery codes in the browser after using this
		// link; they never appear in the operator's output.
		result = struct {
			TenantID  string `json:"tenant_id"`
			SetupURL  string `json:"setup_url"`
			ExpiresIn int    `json:"expires_in_seconds"`
		}{setup.TenantID, q.Origin + "/auth/setup#setup=" + setup.Token, 300}
	case "book":
		var owner ledger.TenantAdmin
		owner, e = (ledger.Admin{DB: db}).ForTenant(ctx, q.TenantID)
		if e == nil {
			result, e = owner.CreateBook(ctx, q.Name, q.Currency, q.Scale)
		}
	case "document-key":
		if !ledger.ValidID(q.TenantID) {
			return errOperation
		}
		if q.KeyDirectory != "" {
			var keys *documents.KeyDirectory
			keys, e = documents.OpenKeyDirectory(q.KeyDirectory)
			if e != nil {
				return errOperation
			}
			defer keys.Close()
			e = keys.Provision(ctx, q.TenantID)
			result = map[string]string{"tenant_id": q.TenantID, "status": "key_ready"}
		} else {
			var data []byte
			data, e = documents.NewKeyFile(q.TenantID)
			result = json.RawMessage(data)
		}
	case "expire-documents":
		result, e = expireDocuments(ctx, db, q)
	}
	if e != nil {
		return errOperation
	}
	if json.NewEncoder(file).Encode(result) != nil || file.Sync() != nil {
		return errOperation
	}
	complete = true
	if expiry, ok := result.(expiryResult); ok && expiry.More {
		_, _ = fmt.Fprintln(out, "Expiry batch completed; expired documents remain for another pass.")
		return nil
	}
	_, _ = fmt.Fprintln(out, "Completed. Result saved to the private output file.")
	return nil
}

func main() {
	// Every completed operation publishes its result privately before reporting.
	signal.Ignore(syscall.SIGPIPE)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if e := run(ctx, os.Args[1:], os.Stdout); e != nil {
		message := error(errOperation)
		for _, safe := range []error{ledger.ErrArchiveCleanup, ledger.ErrArchive, ledger.ErrUncertain, ledger.ErrLimit, errRestoreResult, errRestoreUncertain} {
			if errors.Is(e, safe) {
				message = safe
				break
			}
		}
		_, _ = fmt.Fprintln(os.Stderr, "vult-server-admin:", message)
		os.Exit(1)
	}
}
