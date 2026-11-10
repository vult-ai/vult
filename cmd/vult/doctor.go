package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/vult-ai/vult/internal/daemon"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
)

// check is one line of vult doctor. Remedy is the exact next step, written
// for the person or agent reading it.
type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Remedy string `json:"remedy,omitempty"`
	// An agent check is optional on its own: one connected agent is enough.
	Agent bool `json:"agent,omitempty"`
}

// ready reports whether every required check passes and at least one agent
// is connected.
func ready(checks []check) bool {
	agent := false
	for _, c := range checks {
		if c.Agent {
			agent = agent || c.OK
		} else if !c.OK {
			return false
		}
	}
	return agent
}

const installCommand = "curl -fsSL https://vult.sh | sh"

func runDoctor(ctx context.Context, args []string, out io.Writer) error {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 1 || len(args) == 1 && !asJSON {
		return fmt.Errorf("use vult doctor or vult doctor --json")
	}
	checks := doctorChecks(ctx)
	if asJSON {
		return json.NewEncoder(out).Encode(struct {
			Version string  `json:"version"`
			Ready   bool    `json:"ready"`
			Checks  []check `json:"checks"`
		}{daemon.Version, ready(checks), checks})
	}
	for _, c := range checks {
		mark := "ok  "
		if !c.OK {
			mark = "fix "
		}
		line := mark + c.Name
		if c.Detail != "" {
			line += ": " + c.Detail
		}
		fmt.Fprintln(out, line)
		if !c.OK && c.Remedy != "" {
			fmt.Fprintln(out, "     "+c.Remedy)
		}
	}
	return nil
}

func doctorChecks(ctx context.Context) []check {
	checks := []check{}
	client, err := local.NewClient()
	if err == nil {
		if info, statErr := os.Stat(local.BinaryPath); statErr != nil || !info.Mode().IsRegular() {
			err = fmt.Errorf("the program is missing from %s", local.BinaryPath)
		}
	}
	if err != nil {
		return append(checks, check{Name: "Vult installed", Detail: err.Error(), Remedy: "Install Vult: " + installCommand})
	}
	checks = append(checks, check{Name: "Vult installed", OK: true, Detail: local.BinaryPath})
	statusCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	data, err := client.Call(statusCtx, "status", "", struct{}{})
	cancel()
	var status struct {
		Version string `json:"version"`
		Ready   bool   `json:"ready"`
	}
	if err != nil || json.Unmarshal(data, &status) != nil || !status.Ready {
		return append(checks, check{Name: "Ledger service running", Remedy: "Reinstall Vult to restart its service; Books are kept: " + installCommand})
	}
	checks = append(checks, check{Name: "Ledger service running", OK: true, Detail: "version " + status.Version})
	browserCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	data, err = client.Call(browserCtx, "browser_status", "", struct{}{})
	cancel()
	var browser local.BrowserStatus
	switch {
	case err != nil || strictjson.Decode(data, &browser) != nil:
		checks = append(checks, check{Name: "Browser sign-in set up", Remedy: "Reinstall Vult; the local browser service isn't answering: " + installCommand})
	case !browser.Ready:
		checks = append(checks, check{Name: "Browser sign-in set up", Detail: "no passkey yet", Remedy: "Run " + local.BinaryPath + " setup. The owner creates a passkey in the browser page it opens."})
	default:
		checks = append(checks, check{Name: "Browser sign-in set up", OK: true, Detail: browser.Origin})
	}
	return append(checks, agentChecks(ctx)...)
}

// agentChecks reports whether each agent found on this computer can start Vult.
func agentChecks(ctx context.Context) []check {
	checks := []check{}
	registered := func(cli string, args ...string) (bool, bool) {
		path, err := exec.LookPath(cli)
		if err != nil {
			return false, false
		}
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		command := exec.CommandContext(runCtx, path, args...)
		command.Stdout, command.Stderr = io.Discard, io.Discard
		return true, command.Run() == nil
	}
	for _, agent := range []struct{ name, cli string }{{"Claude Code", "claude"}, {"Codex", "codex"}} {
		found, ok := registered(agent.cli, "mcp", "get", "vult")
		if !found {
			continue
		}
		c := check{Name: agent.name + " connected", OK: ok, Agent: true}
		if !ok {
			c.Remedy = "Run " + local.BinaryPath + " mcp install, then restart " + agent.name + "."
		}
		checks = append(checks, c)
	}
	if home, err := os.UserHomeDir(); err == nil {
		desktop := filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
		if data, err := os.ReadFile(desktop); err == nil {
			var config struct {
				Servers map[string]json.RawMessage `json:"mcpServers"`
			}
			_, ok := config.Servers["vult"]
			if json.Unmarshal(data, &config) == nil {
				_, ok = config.Servers["vult"]
			}
			c := check{Name: "Claude Desktop connected", OK: ok, Agent: true}
			if !ok {
				c.Remedy = "Optional. Quit Claude Desktop, run " + local.BinaryPath + " mcp install, then open Claude Desktop again."
			}
			checks = append(checks, c)
		}
	}
	return checks
}
