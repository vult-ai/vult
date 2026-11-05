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
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

var errOperation = errors.New("offline operation unavailable; inspect configuration and retry only after checking the recorded outcome")

type request struct {
	RuntimeRole string `json:"runtime_role,omitempty"`
	AuthRole    string `json:"auth_role,omitempty"`
	OwnerRole   string `json:"owner_role,omitempty"`
	TenantID    string `json:"tenant_id,omitempty"`
	Issuer      string `json:"issuer,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Username    string `json:"username,omitempty"`
	Passphrase  string `json:"passphrase,omitempty"`
	KeyFile     string `json:"key_file,omitempty"`
	Name        string `json:"name,omitempty"`
	Currency    string `json:"currency,omitempty"`
	Scale       int    `json:"scale,omitempty"`
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
	if command == "--help" {
		_, _ = fmt.Fprintln(out, "Usage: vult-server-admin migrate|bind-owner|init|recover|book --dsn-file FILE --input FILE --output FILE")
		return nil
	}
	if command != "migrate" && command != "bind-owner" && command != "init" && command != "recover" && command != "book" {
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
	case "book":
		var owner ledger.TenantAdmin
		owner, e = (ledger.Admin{DB: db}).ForTenant(ctx, q.TenantID)
		if e == nil {
			result, e = owner.CreateBook(ctx, q.Name, q.Currency, q.Scale)
		}
	}
	if e != nil {
		return errOperation
	}
	if json.NewEncoder(file).Encode(result) != nil || file.Sync() != nil {
		return errOperation
	}
	complete = true
	_, _ = fmt.Fprintln(out, "Completed. Result saved to the private output file.")
	return nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if run(ctx, os.Args[1:], os.Stdout) != nil {
		_, _ = fmt.Fprintln(os.Stderr, "vult-server-admin:", errOperation)
		os.Exit(1)
	}
}
