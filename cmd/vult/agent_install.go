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

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/skills"
)

// installedAgent reports what vult mcp install did for one agent.
type installedAgent struct {
	Agent  string `json:"agent"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// runAgentInstall registers vult mcp with the agents found on this computer
// and installs the bookkeeping skill that ships with this version. It changes
// only the current user's agent settings and needs no approval.
func runAgentInstall(ctx context.Context, out io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || os.Geteuid() == 0 {
		return errors.New("run vult mcp install as yourself, not as root")
	}
	binary := local.BinaryPath
	if target, err := os.Readlink("/usr/local/bin/vult"); err == nil && target == local.BinaryPath {
		binary = "/usr/local/bin/vult"
	}
	results := []installedAgent{}
	register := func(agent, cli string, args ...string) {
		path, err := exec.LookPath(cli)
		if err != nil {
			return
		}
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var stderr bytes.Buffer
		command := exec.CommandContext(runCtx, path, args...)
		command.Stdout, command.Stderr = io.Discard, &stderr
		if err := command.Run(); err != nil {
			detail := stderr.String()
			if bytes.Contains(stderr.Bytes(), []byte("already exists")) {
				results = append(results, installedAgent{agent, "already_registered", ""})
				return
			}
			if len(detail) > 200 {
				detail = detail[:200]
			}
			results = append(results, installedAgent{agent, "failed", detail})
			return
		}
		results = append(results, installedAgent{agent, "registered", ""})
	}
	register("Claude Code", "claude", "mcp", "add", "--scope", "user", "vult", "--", binary, "mcp", "--name", "Claude Code")
	// A folder's own "vult" entry overrides the user-wide one. Say so instead of
	// reporting success while the agent here would reach a different server.
	if n := len(results); n > 0 && results[n-1].Agent == "Claude Code" && results[n-1].Status != "failed" {
		if shadowed, other := claudeShadowed(ctx, binary); shadowed {
			results[n-1] = installedAgent{"Claude Code", "shadowed", "In this folder Claude Code uses another server named vult (" + other + "). Start your agent from a different folder, or remove that entry here with: claude mcp remove vult -s local"}
		}
	}
	register("Codex", "codex", "mcp", "add", "vult", "--", binary, "mcp", "--name", "Codex")
	if desktop := filepath.Join(home, "Library", "Application Support", "Claude"); dirExists(desktop) {
		if desktopRunning(ctx) {
			// The app rewrites its settings while open and would drop the entry.
			results = append(results, installedAgent{"Claude Desktop", "skipped", "Claude Desktop is open and would overwrite its settings. Quit it, then run vult mcp install again to add Vult there."})
		} else {
			results = append(results, registerDesktop(filepath.Join(desktop, "claude_desktop_config.json"), binary))
		}
	}
	if dirExists(filepath.Join(home, ".claude")) {
		results = append(results, installSkill(filepath.Join(home, ".claude", "skills")))
	}
	if len(results) == 0 {
		results = append(results, installedAgent{"none found", "manual", fmt.Sprintf("add an MCP server with command %q and args [\"mcp\"]", binary)})
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

// registerDesktop adds Vult to Claude Desktop's settings without touching any
// other server. An unreadable file is left alone.
func registerDesktop(path, binary string) installedAgent {
	const agent = "Claude Desktop"
	config := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(data, &config) != nil {
			return installedAgent{agent, "failed", "its settings file isn't valid JSON; it was left unchanged"}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return installedAgent{agent, "failed", "its settings file couldn't be read"}
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := config["mcpServers"]; ok && json.Unmarshal(raw, &servers) != nil {
		return installedAgent{agent, "failed", "its MCP settings aren't valid; they were left unchanged"}
	}
	if _, ok := servers["vult"]; ok {
		return installedAgent{agent, "already_registered", ""}
	}
	entry, _ := json.Marshal(map[string]any{"command": binary, "args": []string{"mcp", "--name", "Claude Desktop"}})
	servers["vult"] = entry
	config["mcpServers"], _ = json.Marshal(servers)
	updated, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return installedAgent{agent, "failed", "its settings couldn't be updated"}
	}
	if err := writeReplacing(path, append(updated, '\n')); err != nil {
		return installedAgent{agent, "failed", "its settings couldn't be saved"}
	}
	return installedAgent{agent, "registered", "restart Claude Desktop"}
}

// installSkill writes the bookkeeping skill that matches this version.
func installSkill(dir string) installedAgent {
	const agent = "Claude Code skill"
	data, err := skills.Files.ReadFile("vult-bookkeeping/SKILL.md")
	if err != nil {
		return installedAgent{agent, "failed", "the skill is missing from this build"}
	}
	target := filepath.Join(dir, "vult-bookkeeping")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return installedAgent{agent, "failed", "the skills folder couldn't be created"}
	}
	if err := writeReplacing(filepath.Join(target, "SKILL.md"), data); err != nil {
		return installedAgent{agent, "failed", "the skill couldn't be saved"}
	}
	return installedAgent{agent, "installed", "vult-bookkeeping"}
}

// writeReplacing replaces a file in one step, so an interrupted write never
// leaves half a settings file behind.
func writeReplacing(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Chmod(0o644)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
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

// desktopRunning reports whether the Claude Desktop app's main program is
// running, wherever the app is installed.
func desktopRunning(ctx context.Context) bool {
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "/bin/ps", "-axo", "comm=").Output()
	if err != nil {
		return false
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		if bytes.HasSuffix(bytes.TrimSpace(line), []byte("/Claude.app/Contents/MacOS/Claude")) {
			return true
		}
	}
	return false
}
