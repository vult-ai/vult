package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/internal/daemon"
	"github.com/vult-ai/vult/internal/filebridge"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/mcpserver"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
	"golang.org/x/term"
)

const help = `Vult ` + daemon.Version + ` — local general ledger

  vult setup [--book-name NAME --currency CODE [--profile personal|business] [--timezone ZONE]]
  vult init
  vult status
  vult doctor [--json]
  vult open
  vult browser reset
  vult book create --name NAME --currency CODE --scale N
  vult book list
  vult connection create --name NAME --book ID --access read|propose|write
  vult connection list
  vult connection revoke ID
  vult mcp install
  vult mcp [--name AGENT] [--import-dir DIRECTORY] [--export-dir DIRECTORY]
  vult mcp --connection ID [--import-dir DIRECTORY] [--export-dir DIRECTORY]
  vult period close --book ID --from YYYY-MM-DD --through YYYY-MM-DD [--reason TEXT]
  vult verify --book ID
  vult export --book ID --output FILE.json
  vult export --documents --book ID --output FILE.tar
  vult backup --output FILE.age
  vult restore --input FILE.age
  sudo vult recovery-point
  sudo vult restore --recovery-point FILE
  vult update [--check]
  vult uninstall
  vult --version
  vult help

Owner operations require system administrator authorization.
Agent connections are scoped to one Book. An agent started with vult mcp asks
the owner to approve it in the browser with their passkey; connection IDs
alone confer no access.
`

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backup", "restore", "export":
			// A broken status pipe cannot undo a completed archive operation.
			signal.Ignore(syscall.SIGPIPE)
		}
	}
	if e := run(os.Args[1:], os.Stdout, os.Stderr); e != nil {
		fmt.Fprintln(os.Stderr, "vult:", e)
		os.Exit(1)
	}
}
func flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}
func parse(f *flag.FlagSet, args []string) error {
	if f.Parse(args) != nil || f.NArg() != 0 {
		return errors.New("invalid arguments; run vult help")
	}
	return nil
}
func jsonOut(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func run(args []string, out, errout io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		if len(args) > 1 {
			return errors.New("use vult help")
		}
		fmt.Fprint(out, help)
		return nil
	}
	if args[0] == "--version" {
		if len(args) != 1 {
			return ledger.ErrInvalid
		}
		fmt.Fprintln(out, "Vult", daemon.Version)
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if args[0] == "doctor" {
		return runDoctor(ctx, args[1:], out)
	}
	if args[0] == "update" {
		return runUpdate(ctx, args[1:], out)
	}
	if archiveCommand(args) {
		return runArchive(ctx, args, out)
	}
	if args[0] == "open" || args[0] == "browser" || args[0] == "setup" {
		return runBrowser(ctx, args, out)
	}
	if args[0] == "_serve_browser" {
		if len(args) != 1 {
			return ledger.ErrInvalid
		}
		return daemon.ServeNativeBrowser(ctx)
	}
	if args[0] == "_browser_install" {
		if len(args) != 1 {
			return ledger.ErrInvalid
		}
		return daemon.InstallNativeBrowser(ctx)
	}
	if args[0] == "_browser_check" {
		if len(args) != 1 || (runtime.GOOS != "linux" && runtime.GOOS != "darwin") || os.Geteuid() != 0 {
			return ledger.ErrDenied
		}
		return checkLocalBrowser(ctx)
	}
	if args[0] == "_install" {
		if len(args) != 1 {
			return ledger.ErrInvalid
		}
		return daemon.InstallConfiguration()
	}
	if args[0] == "_serve" {
		if len(args) != 1 {
			return ledger.ErrInvalid
		}
		return daemon.ServeNative(ctx)
	}
	if args[0] == "recovery-point" {
		if len(args) != 1 {
			return ledger.ErrInvalid
		}
		return daemon.TakeRecoveryPoint(ctx, out)
	}
	if args[0] == "_maintenance" {
		if len(args) != 2 {
			return ledger.ErrInvalid
		}
		return daemon.Maintenance(ctx, args[1], os.Stdin, out)
	}
	if args[0] == "_stop" {
		if len(args) != 1 {
			return ledger.ErrInvalid
		}
		return daemon.NativeStop(ctx)
	}
	if args[0] == "_uninstall" {
		if len(args) != 1 {
			return ledger.ErrInvalid
		}
		return daemon.UninstallNative(ctx, out)
	}
	if args[0] == "uninstall" {
		if len(args) != 1 {
			return ledger.ErrInvalid
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return errors.New("uninstall requires an interactive owner terminal")
		}
		return daemon.Uninstall(ctx, out, errout)
	}
	if args[0] == "_authorize" {
		if runtime.GOOS != "darwin" || len(args) != 4 || os.Geteuid() != 0 {
			return ledger.ErrDenied
		}
		uid, e := strconv.ParseUint(args[3], 10, 32)
		if e != nil {
			return ledger.ErrInvalid
		}
		c, e := local.NewClient()
		if e != nil {
			return e
		}
		result, e := c.Call(ctx, "authorize", "", local.Authorization{Action: args[1], Digest: args[2], UID: uint32(uid)})
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(out, string(result))
		return e
	}
	if args[0] == "_owner_authorization" {
		if runtime.GOOS != "linux" || len(args) != 1 {
			return ledger.ErrDenied
		}
		return local.OwnerAuthorizationHelper(ctx, os.Stdin, out)
	}
	method := ""
	var payload any
	outputPath := ""
	switch args[0] {
	case "init", "status", "verify", "export":
		method = args[0]
		f := flags(method)
		book := f.String("book", "", "Book ID")
		output := f.String("output", "", "new output file")
		if parse(f, args[1:]) != nil {
			return ledger.ErrInvalid
		}
		switch method {
		case "init", "status":
			if *book != "" || *output != "" {
				return ledger.ErrInvalid
			}
			payload = struct{}{}
		case "verify", "export":
			if !ledger.ValidID(*book) || (method == "verify" && *output != "") || (method == "export" && *output == "") {
				return ledger.ErrInvalid
			}
			payload = struct {
				BookID string `json:"book_id"`
			}{*book}
			outputPath = *output
		}
	case "book":
		if len(args) < 2 {
			return ledger.ErrInvalid
		}
		if args[1] == "list" {
			if len(args) != 2 {
				return ledger.ErrInvalid
			}
			method = "book_list"
			payload = struct{}{}
		} else if args[1] == "create" {
			f := flags("book create")
			name := f.String("name", "", "Book name")
			currency := f.String("currency", "", "accounting currency")
			scale := f.Int("scale", 2, "decimal precision")
			if parse(f, args[2:]) != nil || strings.TrimSpace(*name) == "" || len(*currency) != 3 || *scale < 0 || *scale > 4 {
				return ledger.ErrInvalid
			}
			method = "book_create"
			payload = struct {
				Name     string `json:"name"`
				Currency string `json:"currency"`
				Scale    int    `json:"scale"`
			}{*name, *currency, *scale}
		} else {
			return ledger.ErrInvalid
		}
	case "connection":
		if len(args) < 2 {
			return ledger.ErrInvalid
		}
		switch args[1] {
		case "list":
			if len(args) != 2 {
				return ledger.ErrInvalid
			}
			method = "connection_list"
			payload = struct{}{}
		case "revoke":
			if len(args) != 3 || !ledger.ValidID(args[2]) {
				return ledger.ErrInvalid
			}
			method = "connection_revoke"
			payload = struct {
				ConnectionID string `json:"connection_id"`
			}{args[2]}
		case "create":
			f := flags("connection create")
			name := f.String("name", "", "connection label")
			book := f.String("book", "", "Book ID")
			access := f.String("access", "", "read, propose or write")
			if parse(f, args[2:]) != nil || strings.TrimSpace(*name) == "" || !ledger.ValidID(*book) || !ledger.ValidAccess(*access) {
				return ledger.ErrInvalid
			}
			method = "connection_create"
			payload = struct {
				Name   string `json:"name"`
				BookID string `json:"book_id"`
				Access string `json:"access"`
			}{*name, *book, *access}
		default:
			return ledger.ErrInvalid
		}
	case "period":
		if len(args) < 2 || args[1] != "close" {
			return ledger.ErrInvalid
		}
		f := flags("period close")
		book := f.String("book", "", "Book ID")
		from := f.String("from", "", "first accounting date")
		through := f.String("through", "", "last accounting date")
		reason := f.String("reason", "", "reason for closing with open review items")
		if parse(f, args[2:]) != nil || !ledger.ValidID(*book) || !ledger.ValidDate(*from) || !ledger.ValidDate(*through) || *from > *through {
			return ledger.ErrInvalid
		}
		method = "period_close"
		payload = local.PeriodCloseRequest{BookID: *book, RequestKey: ledger.ID(), From: *from, Through: *through, Reason: *reason}
	case "mcp":
		if len(args) == 2 && args[1] == "install" {
			return runAgentInstall(ctx, out)
		}
		f := flags("mcp")
		connection := f.String("connection", "", "connection ID")
		name := f.String("name", "", "how this agent describes itself to the owner")
		imports := f.String("import-dir", "", "approved document import directory")
		exports := f.String("export-dir", "", "approved document export directory")
		if parse(f, args[1:]) != nil {
			return ledger.ErrInvalid
		}
		if *connection == "" {
			// No connection ID: the owner approves this agent in the browser.
			return runAgentBridge(ctx, *name, *imports, *exports)
		}
		if *name != "" {
			return ledger.ErrInvalid
		}
		request := local.SessionRequest{ConnectionID: *connection, ImportDirectory: *imports, ExportDirectory: *exports}
		if !request.Valid() {
			return ledger.ErrInvalid
		}
		c, e := local.NewClient()
		if e != nil {
			return e
		}
		transport := &local.DocumentTransport{Client: c}
		files, e := filebridge.Open(transport, *imports, *exports)
		if e != nil {
			return e
		}
		defer files.Close()
		result, e := c.OwnerCall(ctx, "open_session", request)
		if e != nil {
			return e
		}
		var session struct {
			Token     string    `json:"token"`
			Access    string    `json:"access"`
			BookID    string    `json:"book_id"`
			ExpiresAt time.Time `json:"expires_at"`
		}
		if strictjson.Decode(result, &session) != nil || session.Token == "" {
			return errors.New("invalid agent session")
		}
		transport.Authorization = session.Token
		server := mcpserver.New(files.Call, session.Access, mcpserver.Options{Documents: true, LocalFiles: true, OwnerUploads: true})
		return server.Run(ctx, &mcp.StdioTransport{})
	default:
		return errors.New("unknown command; run vult help")
	}
	c, e := local.NewClient()
	if e != nil {
		return e
	}
	var tty *os.File
	if method == "init" {
		tty, e = os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if e != nil || !term.IsTerminal(int(tty.Fd())) {
			if tty != nil {
				tty.Close()
			}
			return errors.New("recovery operations require an interactive owner terminal")
		}
		defer tty.Close()
	}

	var result json.RawMessage
	if method == "status" {
		statusCtx, done := context.WithTimeout(ctx, 2*time.Second)
		result, e = c.Call(statusCtx, method, "", payload)
		done()
	} else {
		result, e = c.OwnerCall(ctx, method, payload)
	}
	if e != nil {
		return e
	}
	switch method {
	case "init":
		var v struct {
			TenantID    string `json:"tenant_id"`
			RecoveryKey string `json:"recovery_key"`
		}
		if strictjson.Decode(result, &v) != nil {
			return ledger.ErrInvalid
		}
		fmt.Fprintln(tty, "Store this backup recovery key offline. Never give it to an agent:")
		fmt.Fprintln(tty, v.RecoveryKey)
		if runtime.GOOS == "linux" {
			fmt.Fprintln(errout, "Protect the local database with disk encryption such as LUKS. Disk-encryption status was not checked.")
		} else {
			check := exec.CommandContext(ctx, "/usr/bin/fdesetup", "status")
			check.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
			b, e := check.Output()
			if e != nil || !strings.Contains(string(b), "FileVault is On") {
				fmt.Fprintln(errout, "Warning: FileVault is off or its status is unknown. Protect the local database with disk encryption.")
			}
		}
		return jsonOut(out, struct {
			Status   string `json:"status"`
			TenantID string `json:"tenant_id"`
		}{"initialized", v.TenantID})
	case "connection_create":
		var connection ledger.Connection
		if strictjson.Decode(result, &connection) != nil {
			return ledger.ErrInvalid
		}
		return jsonOut(out, struct {
			Connection ledger.Connection `json:"connection"`
			MCP        any               `json:"mcp"`
		}{connection, struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}{local.BinaryPath, []string{"mcp", "--connection", connection.ID}}})
	case "export":
		if e = local.WriteNewStream(outputPath, func(dst io.Writer) error {
			_, err := dst.Write(append(result, '\n'))
			return err
		}); e != nil {
			return e
		}
		_ = jsonOut(out, struct {
			Status string `json:"status"`
		}{"plaintext_export_written"})
		return nil
	}
	_, e = fmt.Fprintln(out, string(result))
	return e
}
