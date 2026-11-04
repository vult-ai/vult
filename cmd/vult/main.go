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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/internal/daemon"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/mcpserver"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
	"golang.org/x/term"
)

const help = `Vult 0.1.0 — local general ledger

  vult init
  vult status
  vult book create --name NAME --currency CODE --scale N
  vult book list
  vult connection create --name NAME --book ID --access read|write
  vult connection list
  vult connection revoke ID
  vult mcp --connection ID
  vult period close --book ID --from YYYY-MM-DD --through YYYY-MM-DD
  vult verify --book ID
  vult export --book ID --output FILE.json
  vult backup --output FILE.age
  vult restore --input FILE.age
  vult uninstall
  vult --version
  vult help

Owner operations require macOS administrator authorization.
Agent connections are scoped to one Book. Native owner approval is required
when opening an agent session; connection IDs alone confer no access.
`

func main() {
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
		if len(args) != 4 || os.Geteuid() != 0 {
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
	method := ""
	var payload any
	outputPath := ""
	inputPath := ""
	switch args[0] {
	case "init", "status", "backup", "restore", "verify", "export":
		method = args[0]
		f := flags(method)
		book := f.String("book", "", "Book ID")
		output := f.String("output", "", "new output file")
		input := f.String("input", "", "encrypted backup")
		if parse(f, args[1:]) != nil {
			return ledger.ErrInvalid
		}
		switch method {
		case "init", "status":
			if *book != "" || *output != "" || *input != "" {
				return ledger.ErrInvalid
			}
			payload = struct{}{}
		case "backup":
			if *book != "" || *input != "" || *output == "" {
				return ledger.ErrInvalid
			}
			payload = struct{}{}
			outputPath = *output
		case "restore":
			if *book != "" || *output != "" || *input == "" {
				return ledger.ErrInvalid
			}
			inputPath = *input
		case "verify", "export":
			if !ledger.ValidID(*book) || *input != "" || (method == "verify" && *output != "") || (method == "export" && *output == "") {
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
			access := f.String("access", "", "read or write")
			if parse(f, args[2:]) != nil || strings.TrimSpace(*name) == "" || !ledger.ValidID(*book) || (*access != "read" && *access != "write") {
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
		if parse(f, args[2:]) != nil || !ledger.ValidID(*book) || !ledger.ValidDate(*from) || !ledger.ValidDate(*through) || *from > *through {
			return ledger.ErrInvalid
		}
		method = "period_close"
		payload = struct {
			BookID  string `json:"book_id"`
			From    string `json:"from"`
			Through string `json:"through"`
		}{*book, *from, *through}
	case "mcp":
		f := flags("mcp")
		connection := f.String("connection", "", "connection ID")
		if parse(f, args[1:]) != nil || !ledger.ValidID(*connection) {
			return ledger.ErrInvalid
		}
		c, e := local.NewClient()
		if e != nil {
			return e
		}
		result, e := c.OwnerCall(ctx, "open_session", struct {
			ConnectionID string `json:"connection_id"`
		}{*connection})
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
		server := mcpserver.New(func(ctx context.Context, name string, input json.RawMessage) (json.RawMessage, error) {
			return c.Call(ctx, name, session.Token, input)
		}, session.Access)
		return server.Run(ctx, &mcp.StdioTransport{})
	default:
		return errors.New("unknown command; run vult help")
	}
	c, e := local.NewClient()
	if e != nil {
		return e
	}
	var tty *os.File
	if method == "init" || method == "restore" {
		tty, e = os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if e != nil || !term.IsTerminal(int(tty.Fd())) {
			if tty != nil {
				tty.Close()
			}
			return errors.New("recovery operations require an interactive owner terminal")
		}
		defer tty.Close()
	}
	if method == "restore" {
		file, e := os.OpenFile(inputPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return errors.New("encrypted backup unavailable")
		}
		info, e := file.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Size() > 66<<20 {
			file.Close()
			return errors.New("invalid encrypted backup")
		}
		data, e := io.ReadAll(io.LimitReader(file, (66<<20)+1))
		file.Close()
		if e != nil || len(data) > 66<<20 {
			return errors.New("invalid encrypted backup")
		}
		fmt.Fprint(tty, "Backup recovery key: ")
		key, e := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		if e != nil {
			return errors.New("recovery key unavailable")
		}
		payload = struct {
			Data        []byte `json:"data"`
			RecoveryKey string `json:"recovery_key"`
		}{data, string(key)}
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
		check := exec.CommandContext(ctx, "/usr/bin/fdesetup", "status")
		check.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		b, e := check.Output()
		if e != nil || !strings.Contains(string(b), "FileVault is On") {
			fmt.Fprintln(errout, "Warning: FileVault is off or its status is unknown. Protect the local database with disk encryption.")
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
	case "backup":
		var v struct {
			Data []byte `json:"data"`
		}
		if strictjson.Decode(result, &v) != nil {
			return ledger.ErrInvalid
		}
		if e = writeNew(outputPath, v.Data); e != nil {
			return e
		}
		return jsonOut(out, struct {
			Status string `json:"status"`
		}{"encrypted_backup_written"})
	case "export":
		if e = writeNew(outputPath, append(result, '\n')); e != nil {
			return e
		}
		return jsonOut(out, struct {
			Status string `json:"status"`
		}{"plaintext_export_written"})
	}
	_, e = fmt.Fprintln(out, string(result))
	return e
}
func writeNew(path string, data []byte) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return errors.New("a new output path is required")
	}
	defer f.Close()
	if _, e = f.Write(data); e != nil {
		return e
	}
	return f.Sync()
}
