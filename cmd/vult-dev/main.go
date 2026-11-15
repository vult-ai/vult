//go:build devweb

// Command vult-dev runs a development Vult: the same browser and native
// services as an installed Vult, in one process, from a private folder.
// Development builds only (-tags devweb).
//
//	vult-dev --dir FOLDER            run the development Vult
//	vult-dev mcp [--dir FOLDER]      serve an agent on this computer (MCP on standard input and output)
//	vult-dev mcp install             register that agent bridge with Claude Code, Codex and Claude Desktop
//
// FOLDER defaults to the folder this program is in.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/vult-ai/vult/internal/agentbridge"
	"github.com/vult-ai/vult/internal/daemon"
	"github.com/vult-ai/vult/internal/local"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "vult-dev runs as yourself, not as root")
		os.Exit(2)
	}
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		if err := runMCP(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "vult-dev mcp:", err)
			os.Exit(1)
		}
		return
	}
	dir := flag.String("dir", "", "the development Vult's private folder")
	flag.Parse()
	if *dir == "" || !filepath.IsAbs(*dir) {
		fmt.Fprintln(os.Stderr, "Usage: vult-dev --dir FOLDER (an absolute path)")
		os.Exit(2)
	}
	if err := daemon.ServeDevelopmentBrowser(ctx, *dir); err != nil {
		fmt.Fprintln(os.Stderr, "vult-dev:", err)
		os.Exit(1)
	}
}

// ownFolder is the folder this program is in, where preview builds it beside
// the development Vult's private folder contents.
func ownFolder() (string, string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return self, filepath.Dir(self), nil
}

func runMCP(ctx context.Context, args []string, out io.Writer) error {
	self, folder, err := ownFolder()
	if err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "install" {
		return install(ctx, self, out)
	}
	f := flag.NewFlagSet("vult-dev mcp", flag.ContinueOnError)
	dir := f.String("dir", folder, "the development Vult's private folder")
	name := f.String("name", "", "how this agent describes itself to the owner")
	if err := f.Parse(args); err != nil || !filepath.IsAbs(*dir) {
		return errors.New("usage: vult-dev mcp [--dir FOLDER] [--name NAME]")
	}
	// The development Vult runs as this user, on a socket only this user can open.
	c := local.Client{Socket: filepath.Join(*dir, "native.sock"), ExpectedUID: uint32(os.Getuid())}
	return agentbridge.Run(ctx, c, *name, "", "")
}

// install registers this development Vult with every agent app found (Claude
// Code, Codex, Claude Desktop), under its own name so it never replaces an
// installed Vult's entry.
func install(ctx context.Context, self string, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return errors.New("run vult-dev mcp install as yourself")
	}
	results := agentbridge.RegisterAll(ctx, home, agentbridge.Entry{Server: "vult-dev", Program: self, Args: []string{"mcp"}})
	if len(results) == 0 {
		results = append(results, agentbridge.Installed{Agent: "none found", Status: "manual", Detail: fmt.Sprintf("Add an MCP server named vult-dev with command %q and args [\"mcp\"]", self)})
	}
	return json.NewEncoder(out).Encode(struct {
		Agents []agentbridge.Installed `json:"agents"`
		Next   string                  `json:"next"`
	}{results, "Restart the app or reload its MCP servers. Its first Vult call waits for you to let it in, in the development Vult in your browser."})
}
