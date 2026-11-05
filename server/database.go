package server

import (
	"context"
	"io"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/ledger"
)

// ReadPrivateFile refuses links, shared permissions and oversized credentials.
func ReadPrivateFile(path string, limit int64) ([]byte, error) {
	before, e := os.Lstat(path)
	if e != nil || !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 {
		return nil, ErrConfiguration
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, ErrConfiguration
	}
	defer f.Close()
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() || after.Mode().Perm()&0077 != 0 || after.Size() > limit {
		return nil, ErrConfiguration
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil || len(b) == 0 || int64(len(b)) > limit {
		return nil, ErrConfiguration
	}
	return b, nil
}

func databaseConfig(raw string) (*pgxpool.Config, error) {
	c, e := pgxpool.ParseConfig(strings.TrimSpace(raw))
	if e != nil || strings.TrimSpace(raw) == "" {
		return nil, ErrDatabase
	}
	ip, _ := netip.ParseAddr(c.ConnConfig.Host)
	local := ip.IsLoopback() || strings.HasPrefix(c.ConnConfig.Host, "/")
	if !local && (c.ConnConfig.TLSConfig == nil || c.ConnConfig.TLSConfig.InsecureSkipVerify || c.ConnConfig.TLSConfig.ServerName == "" || len(c.ConnConfig.Fallbacks) != 0) {
		return nil, ErrDatabase
	}
	c.MaxConns, c.MinConns = 8, 0
	c.ConnConfig.ConnectTimeout = 5 * time.Second
	c.ConnConfig.RuntimeParams["application_name"] = "vult-server"
	return c, nil
}

// OpenDatabase uses only restricted runtime credentials. It neither provisions
// tenants nor initializes/migrates a database and never returns driver details.
func OpenDatabase(ctx context.Context, path string) (*pgxpool.Pool, error) {
	b, e := ReadPrivateFile(path, 16<<10)
	if e != nil {
		return nil, ErrDatabase
	}
	c, e := databaseConfig(string(b))
	if e != nil {
		return nil, e
	}
	pool, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, ErrDatabase
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var restricted bool
	e = pool.QueryRow(check, `SELECT current_user=session_user AND NOT(rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)
 AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE member=r.oid)
 AND NOT EXISTS(SELECT 1 FROM pg_class WHERE relowner=r.oid AND relnamespace IN ('public'::regnamespace,'access'::regnamespace))
 AND NOT has_schema_privilege(current_user,'public','CREATE')
 AND NOT has_schema_privilege(current_user,'access','CREATE')
 AND NOT has_database_privilege(current_user,current_database(),'CREATE')
 FROM pg_roles r WHERE rolname=current_user`).Scan(&restricted)
	if e != nil || !restricted || ledger.Ready(check, pool) != nil {
		pool.Close()
		return nil, ErrDatabase
	}
	return pool, nil
}
