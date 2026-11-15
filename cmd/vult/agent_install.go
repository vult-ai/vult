package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/vult-ai/vult/internal/agentbridge"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/skills"
)

// installedAgent reports what vult mcp install did for one agent.
type installedAgent = agentbridge.Installed

// runAgentInstall registers vult mcp with the agents found on this computer
// (Claude Code, Codex, Claude Desktop) and installs the bookkeeping skill that
// ships with this version. It changes only the current user's agent settings,
// never replaces another server's entry, and needs no approval.
func runAgentInstall(ctx context.Context, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || os.Geteuid() == 0 {
		return errors.New("run vult mcp install as yourself, not as root")
	}
	binary := local.BinaryPath
	if target, err := os.Readlink("/usr/local/bin/vult"); err == nil && target == local.BinaryPath {
		binary = "/usr/local/bin/vult"
	}
	results := agentbridge.RegisterAll(ctx, home, agentbridge.Entry{Server: "vult", Program: binary, Args: []string{"mcp"}})
	// A folder's own "vult" entry overrides the user-wide one. Say so instead of
	// reporting success while the agent here would reach a different server.
	for i, r := range results {
		if r.Agent == "Claude Code" && r.Status != "failed" {
			if shadowed, other := claudeShadowed(ctx, binary); shadowed {
				results[i] = installedAgent{Agent: "Claude Code", Status: "shadowed", Detail: "In this folder Claude Code uses another server named vult (" + other + "). Start the agent from a different folder, or remove that entry here with: claude mcp remove vult -s local"}
			}
		}
	}
	if dirExists(filepath.Join(home, ".claude")) {
		results = append(results, installSkill(filepath.Join(home, ".claude", "skills")))
	}
	if len(results) == 0 {
		results = append(results, installedAgent{Agent: "none found", Status: "manual", Detail: fmt.Sprintf("add an MCP server with command %q and args [\"mcp\"]", binary)})
	}
	return json.NewEncoder(out).Encode(struct {
		Agents []installedAgent `json:"agents"`
		Next   string           `json:"next"`
	}{results, "Restart the agent or reload its MCP servers. Its first Vult call waits for the owner to let it in, in Vult in their browser."})
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// registerDesktop adds this Vult to Claude Desktop's settings file.
func registerDesktop(path, binary string) installedAgent {
	return agentbridge.RegisterDesktop(path, agentbridge.Entry{Server: "vult", Program: binary, Args: []string{"mcp"}})
}

// installSkill writes the bookkeeping skill that matches this version.
func installSkill(dir string) installedAgent {
	const agent = "Claude Code skill"
	data, err := skills.Files.ReadFile("vult-bookkeeping/SKILL.md")
	if err != nil {
		return installedAgent{Agent: agent, Status: "failed", Detail: "the skill is missing from this build"}
	}
	target := filepath.Join(dir, "vult-bookkeeping")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return installedAgent{Agent: agent, Status: "failed", Detail: "the skills folder couldn't be created"}
	}
	if err := agentbridge.WriteReplacing(filepath.Join(target, "SKILL.md"), data); err != nil {
		return installedAgent{Agent: agent, Status: "failed", Detail: "the skill couldn't be saved"}
	}
	return installedAgent{Agent: agent, Status: "installed", Detail: "vult-bookkeeping"}
}

// claudeShadowed reports whether Claude Code, in the current folder, resolves
// "vult" to something other than this installation.
func claudeShadowed(ctx context.Context, binary string) (bool, string) {
	path, err := exec.LookPath("claude")
	if err != nil {
		return false, ""
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, path, "mcp", "get", "vult").Output()
	if err != nil || bytes.Contains(out, []byte(binary)) {
		return false, ""
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("URL:")) || bytes.HasPrefix(line, []byte("Command:")) {
			return true, string(bytes.TrimSpace(line))
		}
	}
	return true, "an entry for this folder"
}
