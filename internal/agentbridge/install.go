package agentbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Installed reports what an install did for one agent app.
type Installed struct {
	Agent  string `json:"agent"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Entry is the MCP server an install registers: its name in each app's
// settings, and the program and arguments that start the bridge. Each app
// adds "--name <app>" so the owner sees which app asks to connect.
type Entry struct {
	Server  string
	Program string
	Args    []string
}

func (e Entry) argsFor(app string) []string {
	return append(append([]string{}, e.Args...), "--name", app)
}

// RegisterAll registers e with every agent app found for this user: Claude
// Code, Codex and Claude Desktop. An entry of the same name that starts
// another program is never replaced; the result says where it is instead.
func RegisterAll(ctx context.Context, home string, e Entry) []Installed {
	results := []Installed{}
	if r, ok := RegisterClaudeCode(ctx, e); ok {
		results = append(results, r)
	}
	if r, ok := RegisterCodex(ctx, e); ok {
		results = append(results, r)
	}
	if desktop := filepath.Join(home, "Library", "Application Support", "Claude"); dirExists(desktop) {
		if DesktopRunning(ctx) {
			// The app rewrites its settings while open and would drop the entry.
			results = append(results, Installed{"Claude Desktop", "skipped", "Claude Desktop is open and would overwrite its settings. Quit it, then run this again to add it there."})
		} else {
			results = append(results, RegisterDesktop(filepath.Join(desktop, "claude_desktop_config.json"), e))
		}
	}
	return results
}

func run(ctx context.Context, path string, args ...string) ([]byte, []byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(runCtx, path, args...)
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func clip(b []byte) string {
	b = bytes.TrimSpace(b)
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

// RegisterClaudeCode adds e to Claude Code for this user. ok is false when
// Claude Code isn't installed.
func RegisterClaudeCode(ctx context.Context, e Entry) (Installed, bool) {
	const agent = "Claude Code"
	path, err := exec.LookPath("claude")
	if err != nil {
		return Installed{}, false
	}
	args := append([]string{"mcp", "add", "--scope", "user", e.Server, "--", e.Program}, e.argsFor(agent)...)
	if _, stderr, err := run(ctx, path, args...); err != nil {
		if bytes.Contains(stderr, []byte("already exists")) {
			return Installed{agent, "already_registered", "~/.claude.json, server " + e.Server}, true
		}
		return Installed{agent, "failed", clip(stderr)}, true
	}
	return Installed{agent, "registered", "~/.claude.json, server " + e.Server}, true
}

// RegisterCodex adds e to Codex for this user (its config.toml). Codex's own
// add replaces an entry of the same name, so an existing one is checked first
// and kept when it starts another program.
func RegisterCodex(ctx context.Context, e Entry) (Installed, bool) {
	const agent = "Codex"
	path, err := exec.LookPath("codex")
	if err != nil {
		return Installed{}, false
	}
	where := "~/.codex/config.toml, [mcp_servers." + e.Server + "]"
	if home := os.Getenv("CODEX_HOME"); home != "" {
		where = filepath.Join(home, "config.toml") + ", [mcp_servers." + e.Server + "]"
	}
	if out, _, err := run(ctx, path, "mcp", "get", e.Server, "--json"); err == nil {
		var existing struct {
			Transport struct {
				Command string `json:"command"`
			} `json:"transport"`
		}
		if json.Unmarshal(out, &existing) == nil && existing.Transport.Command == e.Program {
			return Installed{agent, "already_registered", where}, true
		}
		return Installed{agent, "kept", "Codex already has a server named " + e.Server + " that starts " + existing.Transport.Command + "; it was left unchanged. Remove it with: codex mcp remove " + e.Server}, true
	}
	args := append([]string{"mcp", "add", e.Server, "--", e.Program}, e.argsFor(agent)...)
	if _, stderr, err := run(ctx, path, args...); err != nil {
		return Installed{agent, "failed", clip(stderr)}, true
	}
	return Installed{agent, "registered", where}, true
}

// RegisterDesktop adds e to Claude Desktop's settings file, keeping every
// other server and any entry of the same name.
func RegisterDesktop(path string, e Entry) Installed {
	const agent = "Claude Desktop"
	config := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err == nil {
		if json.Unmarshal(data, &config) != nil {
			return Installed{agent, "failed", "its settings file isn't valid JSON; it was left unchanged"}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Installed{agent, "failed", "its settings file couldn't be read"}
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := config["mcpServers"]; ok && json.Unmarshal(raw, &servers) != nil {
		return Installed{agent, "failed", "its MCP settings aren't valid; they were left unchanged"}
	}
	if raw, ok := servers[e.Server]; ok {
		var existing struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(raw, &existing) == nil && existing.Command != "" && existing.Command != e.Program {
			return Installed{agent, "kept", "Claude Desktop already has a server named " + e.Server + " that starts " + existing.Command + "; it was left unchanged"}
		}
		return Installed{agent, "already_registered", ""}
	}
	entry, _ := json.Marshal(map[string]any{"command": e.Program, "args": e.argsFor(agent)})
	servers[e.Server] = entry
	config["mcpServers"], _ = json.Marshal(servers)
	updated, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return Installed{agent, "failed", "its settings couldn't be updated"}
	}
	if err := WriteReplacing(path, append(updated, '\n')); err != nil {
		return Installed{agent, "failed", "its settings couldn't be saved"}
	}
	return Installed{agent, "registered", path + ", restart Claude Desktop"}
}

// WriteReplacing replaces a file in one step, so an interrupted write never
// leaves half a settings file behind.
func WriteReplacing(path string, data []byte) error {
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

// DesktopRunning reports whether the Claude Desktop app's main program is
// running, wherever the app is installed.
func DesktopRunning(ctx context.Context) bool {
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

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
