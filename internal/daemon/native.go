package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

const PostgresPath = "/Library/PrivilegedHelperTools/ai.vult.ledger/postgres"

type configuration struct {
	Format          string `json:"format"`
	State           string `json:"state"`
	AdminPassword   string `json:"admin_password"`
	RuntimePassword string `json:"runtime_password"`
	RecoveryKey     string `json:"recovery_key"`
}

func serviceIdentity() (uint32, uint32, error) {
	u, e := user.Lookup(local.ServiceUser)
	if e != nil {
		return 0, 0, errors.New("dedicated service account unavailable")
	}
	uid, e := strconv.ParseUint(u.Uid, 10, 32)
	if e != nil || uid == 0 {
		return 0, 0, errors.New("invalid service identity")
	}
	gid, e := strconv.ParseUint(u.Gid, 10, 32)
	if e != nil || gid == 0 {
		return 0, 0, errors.New("invalid service group")
	}
	return uint32(uid), uint32(gid), nil
}
func protected(path string, uid uint32, directory bool) error {
	info, e := os.Lstat(path)
	if e != nil {
		return errors.New("protected installation path unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || info.IsDir() != directory {
		return errors.New("installation ownership or permissions are unsafe")
	}
	if !directory && (!info.Mode().IsRegular() || stat.Nlink != 1) {
		return errors.New("regular protected file required")
	}
	acl := exec.Command("/bin/ls", "-lde", path)
	acl.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	text, e := acl.Output()
	if e != nil {
		return errors.New("installation access controls could not be verified")
	}
	for _, line := range strings.Split(string(text), "\n")[1:] {
		if strings.Contains(line, " allow ") {
			return errors.New("unsupported installation access control list")
		}
	}
	return nil
}
func installationCode() error {
	for _, p := range []string{"/Library", "/Library/PrivilegedHelperTools", "/Library/PrivilegedHelperTools/ai.vult.ledger", PostgresPath, filepath.Join(PostgresPath, "bin"), filepath.Join(PostgresPath, "lib"), filepath.Join(PostgresPath, "share")} {
		if e := protected(p, 0, true); e != nil {
			return e
		}
	}
	for _, p := range []string{local.BinaryPath, filepath.Join(PostgresPath, "bin/postgres"), filepath.Join(PostgresPath, "bin/initdb"), filepath.Join(PostgresPath, "bin/pg_ctl")} {
		if e := protected(p, 0, false); e != nil {
			return e
		}
	}
	for _, directory := range []string{filepath.Join(PostgresPath, "lib"), filepath.Join(PostgresPath, "share")} {
		if e := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return protected(path, 0, entry.IsDir())
		}); e != nil {
			return e
		}
	}
	return nil
}

func installationData(uid uint32) error {
	for _, path := range []string{"/Library", "/Library/Application Support", "/Library/Application Support/ai.vult.ledger"} {
		if e := protected(path, 0, true); e != nil {
			return e
		}
	}
	if e := protected(local.DataPath, uid, true); e != nil {
		return e
	}
	info, e := os.Stat(local.DataPath)
	if e != nil || info.Mode().Perm() != 0700 {
		return errors.New("private data directory required")
	}
	return nil
}

// InstallConfiguration runs only from the native installer, as root.
func InstallConfiguration() error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" || os.Geteuid() != 0 {
		return errors.New("native administrator installation required")
	}
	if e := installationCode(); e != nil {
		return e
	}
	uid, gid, e := serviceIdentity()
	if e != nil {
		return e
	}
	if e = installationData(uid); e != nil {
		return e
	}
	_, e = ensureConfiguration(local.DataPath, uid, gid)
	return e
}

func ensureConfiguration(data string, uid, gid uint32) (configuration, error) {
	path := filepath.Join(data, "configuration.json")
	if _, e := os.Lstat(path); e == nil {
		return readConfigurationFile(path, uid)
	} else if !os.IsNotExist(e) {
		return configuration{}, e
	}
	entries, e := os.ReadDir(data)
	if e != nil || len(entries) != 0 {
		return configuration{}, errors.New("existing storage has no configuration; recovery is required")
	}
	conf := configuration{Format: "vult-service-v1", State: "new", AdminPassword: secret(), RuntimePassword: secret(), RecoveryKey: secret()}
	b, _ := json.Marshal(conf)
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if e != nil {
		return configuration{}, e
	}
	defer f.Close()
	if e = f.Chown(int(uid), int(gid)); e != nil {
		return configuration{}, e
	}
	if _, e = f.Write(b); e != nil {
		return configuration{}, e
	}
	return conf, f.Sync()
}
func loadConfiguration(uid uint32) (configuration, error) {
	var c configuration
	if e := installationData(uid); e != nil {
		return c, e
	}
	return readConfigurationFile(filepath.Join(local.DataPath, "configuration.json"), uid)
}
func readConfigurationFile(path string, uid uint32) (configuration, error) {
	var c configuration
	f, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return c, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return c, e
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uid || stat.Nlink != 1 || info.Mode().Perm() != 0400 || info.Size() > 4096 {
		return c, errors.New("private read-only configuration required")
	}
	b, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || strictjson.Decode(b, &c) != nil || c.Format != "vult-service-v1" || (c.State != "new" && c.State != "ready") || len(c.AdminPassword) != 43 || len(c.RuntimePassword) != 43 || len(c.RecoveryKey) != 43 {
		return c, errors.New("invalid service configuration")
	}
	return c, nil
}
func runPostgres(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("missing database command")
	}
	exe := filepath.Join(PostgresPath, "bin", args[0])
	if e := protected(exe, 0, false); e != nil {
		return e
	}
	cmd := exec.CommandContext(ctx, exe, args[1:]...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TMPDIR=" + local.DataPath}
	output, e := cmd.CombinedOutput()
	if e != nil {
		_ = output
		return errors.New("native database command failed")
	}
	return nil
}
func prepareDatabase(ctx context.Context, c configuration) error {
	database := filepath.Join(local.DataPath, "database")
	socket := filepath.Join(local.DataPath, "postgres")
	if e := os.MkdirAll(socket, 0700); e != nil {
		return e
	}
	fresh, e := needsDatabaseInitialization(database, c.State)
	if e != nil {
		return e
	}
	if fresh {
		pass, e := os.CreateTemp(local.DataPath, ".bootstrap-")
		if e != nil {
			return e
		}
		name := pass.Name()
		defer os.Remove(name)
		if _, e = pass.WriteString(c.AdminPassword); e != nil {
			pass.Close()
			return e
		}
		if e = pass.Close(); e != nil {
			return e
		}
		if e = runPostgres(ctx, "initdb", "-D", database, "-U", "vult_owner", "--pwfile="+name, "--auth-local=scram-sha-256", "--auth-host=reject", "--encoding=UTF8", "--no-locale"); e != nil {
			return e
		}
		conf := fmt.Sprintf("\nlisten_addresses = ''\nport = 5432\nunix_socket_directories = '%s'\nunix_socket_permissions = 0700\nmax_connections = 16\nshared_buffers = '32MB'\nwork_mem = '4MB'\nmaintenance_work_mem = '32MB'\nfsync = on\nfull_page_writes = on\nsynchronous_commit = on\npassword_encryption = 'scram-sha-256'\nlog_statement = 'none'\nlog_min_error_statement = 'panic'\nlog_min_messages = 'warning'\n", socket)
		f, e := os.OpenFile(filepath.Join(database, "postgresql.conf"), os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = f.WriteString(conf)
		f.Close()
		if e != nil {
			return e
		}
	}
	if e := runPostgres(ctx, "pg_ctl", "-D", database, "status"); e == nil {
		return nil
	}
	// Enforce the private transport even if initial configuration writing was
	// interrupted. PostgreSQL defaults must never open a TCP listener.
	options := fmt.Sprintf("-c listen_addresses='' -c unix_socket_directories='%s' -c unix_socket_permissions=0700", socket)
	return runPostgres(ctx, "pg_ctl", "-D", database, "-o", options, "-l", filepath.Join(local.DataPath, "postgres.log"), "-w", "-t", "30", "start")
}

// Existing installations never treat missing storage as a fresh installation.
func needsDatabaseInitialization(database, state string) (bool, error) {
	if state != "new" && state != "ready" {
		return false, errors.New("unknown installation state")
	}
	if info, e := os.Lstat(database); e == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("database storage is not a real directory")
		}
	} else if !os.IsNotExist(e) {
		return false, e
	}
	path := filepath.Join(database, "PG_VERSION")
	info, e := os.Lstat(path)
	if e == nil {
		if !info.Mode().IsRegular() || info.Size() > 16 {
			return false, errors.New("invalid PostgreSQL version file")
		}
		b, e := os.ReadFile(path)
		if e != nil || strings.TrimSpace(string(b)) != "18" {
			return false, errors.New("existing database needs a supported PostgreSQL upgrade")
		}
		return false, nil
	}
	if !os.IsNotExist(e) {
		return false, e
	}
	if state == "ready" {
		return false, errors.New("stored database is missing; recovery is required")
	}
	entries, e := os.ReadDir(database)
	if e != nil && !os.IsNotExist(e) {
		return false, e
	}
	if len(entries) > 0 {
		return false, errors.New("refusing to initialize nonempty storage")
	}
	return true, nil
}

func requireExisting(state, kind string, exists bool) error {
	if state != "new" && state != "ready" {
		return errors.New("unknown installation state")
	}
	if state == "ready" && !exists {
		return fmt.Errorf("stored %s is missing; recovery is required", kind)
	}
	return nil
}

func verifyOrInitializeSchema(ctx context.Context, admin *pgxpool.Pool, role, state string) error {
	if state == "ready" {
		return ledger.Upgrade(ctx, admin, role)
	}
	if state != "new" {
		return errors.New("unknown installation state")
	}
	return ledger.Apply(ctx, admin, role)
}

func markConfigurationReady(path string, c configuration) error {
	if c.State != "new" {
		return errors.New("configuration is not awaiting initialization")
	}
	c.State = "ready"
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".configuration-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0400); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(name, path); e != nil {
		return e
	}
	directory, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer directory.Close()
	return directory.Sync()
}
func nativePoolConfig(user, password, database string) (*pgxpool.Config, error) {
	u := url.URL{Scheme: "postgresql", User: url.UserPassword(user, password), Path: "/" + database}
	q := url.Values{"host": {filepath.Join(local.DataPath, "postgres")}, "sslmode": {"disable"}, "port": {"5432"}}
	// PostgreSQL connection URIs use percent-escaped spaces, not form encoding.
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	config, e := pgxpool.ParseConfig(u.String())
	if e != nil {
		return nil, errors.New("invalid private database configuration")
	}
	config.MaxConns = 4
	config.MinConns = 0
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	config.ConnConfig.RuntimeParams["timezone"] = "UTC"
	config.ConnConfig.RuntimeParams["application_name"] = "vult"
	config.ConnConfig.RuntimeParams["statement_timeout"] = "60000"
	config.ConnConfig.RuntimeParams["lock_timeout"] = "15000"
	config.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "90000"
	return config, nil
}
func pool(ctx context.Context, user, password, database string) (*pgxpool.Pool, error) {
	config, e := nativePoolConfig(user, password, database)
	if e != nil {
		return nil, e
	}
	p, e := pgxpool.NewWithConfig(ctx, config)
	if e != nil {
		return nil, errors.New("private database unavailable")
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, errors.New("private database unavailable")
	}
	return p, nil
}
func ServeNative(ctx context.Context) error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("native service requires Apple Silicon macOS")
	}
	uid, _, e := serviceIdentity()
	if e != nil || uint32(os.Geteuid()) != uid {
		return errors.New("service must run under its dedicated account")
	}
	if e = installationCode(); e != nil {
		return e
	}
	conf, e := loadConfiguration(uid)
	if e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(local.DataPath, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("service already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	startup, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if e = prepareDatabase(startup, conf); e != nil {
		return e
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		_ = runPostgres(stop, "pg_ctl", "-D", filepath.Join(local.DataPath, "database"), "-m", "fast", "-w", "-t", "20", "stop")
	}()
	bootstrap, e := pool(startup, "vult_owner", conf.AdminPassword, "postgres")
	if e != nil {
		return e
	}
	var version int
	if e = bootstrap.QueryRow(startup, "SELECT current_setting('server_version_num')::integer").Scan(&version); e != nil || version < 180000 || version >= 190000 {
		bootstrap.Close()
		return errors.New("PostgreSQL18 is required")
	}
	var exists bool
	if e = bootstrap.QueryRow(startup, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname='vult')").Scan(&exists); e != nil {
		bootstrap.Close()
		return errors.New("database initialization failed")
	}
	if !exists {
		if e = requireExisting(conf.State, "ledger database", false); e != nil {
			bootstrap.Close()
			return e
		}
		if _, e = bootstrap.Exec(startup, "CREATE DATABASE vult"); e != nil {
			bootstrap.Close()
			return errors.New("database initialization failed")
		}
	}
	if e = bootstrap.QueryRow(startup, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='vult_runtime')").Scan(&exists); e != nil {
		bootstrap.Close()
		return e
	}
	if !exists {
		if e = requireExisting(conf.State, "runtime role", false); e != nil {
			bootstrap.Close()
			return e
		}
		_, e = bootstrap.Exec(startup, "CREATE ROLE vult_runtime LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD '"+conf.RuntimePassword+"'")
		if e != nil {
			bootstrap.Close()
			return errors.New("restricted role initialization failed")
		}
	}
	var unsafe bool
	if e = bootstrap.QueryRow(startup, "SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolinherit OR rolreplication OR rolbypassrls FROM pg_roles WHERE rolname='vult_runtime'").Scan(&unsafe); e != nil || unsafe {
		bootstrap.Close()
		return errors.New("unsafe runtime database role")
	}
	if _, e = bootstrap.Exec(startup, "REVOKE CREATE,TEMPORARY ON DATABASE vult FROM PUBLIC"); e != nil {
		bootstrap.Close()
		return e
	}
	bootstrap.Close()
	admin, e := pool(startup, "vult_owner", conf.AdminPassword, "vult")
	if e != nil {
		return e
	}
	defer admin.Close()
	if e = verifyOrInitializeSchema(startup, admin, "vult_runtime", conf.State); e != nil {
		return errors.New("ledger schema needs recovery or a supported forward upgrade")
	}
	runtimePool, e := pool(startup, "vult_runtime", conf.RuntimePassword, "vult")
	if e != nil {
		return e
	}
	defer runtimePool.Close()
	if e = ledger.Ready(startup, runtimePool); e != nil {
		return e
	}
	// Persist readiness before opening any owner or agent endpoint.
	if conf.State == "new" {
		if e = markConfigurationReady(filepath.Join(local.DataPath, "configuration.json"), conf); e != nil {
			return e
		}
	}
	socketDir := filepath.Dir(local.SocketPath)
	if e = protected(socketDir, uid, true); e != nil {
		return e
	}
	if info, e := os.Lstat(local.SocketPath); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("refusing to replace an unexpected socket path")
		}
		probe, e := local.NewClient()
		if e == nil {
			probeCtx, done := context.WithTimeout(ctx, time.Second)
			_, e = probe.Call(probeCtx, "status", "", struct{}{})
			done()
			if e == nil {
				return errors.New("service already running")
			}
		}
		if e = os.Remove(local.SocketPath); e != nil {
			return e
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	listener, e := Listen(local.SocketPath)
	if e != nil {
		return e
	}
	defer listener.Close()
	server := &Server{Admin: ledger.Admin{DB: admin}, Ledger: ledger.Store{Runtime: runtimePool}, RecoveryKey: conf.RecoveryKey}
	e = server.Serve(ctx, listener)
	if e != nil && !strings.Contains(e.Error(), "closed") {
		return errors.New("local service stopped unexpectedly")
	}
	return nil
}
