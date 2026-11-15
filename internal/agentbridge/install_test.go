package agentbridge

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAppNameShowsTheProductAnAppIs(t *testing.T) {
	for _, c := range []struct{ title, name, want string }{
		{"", "codex-mcp-client", "Codex"},
		{"Codex", "codex_cli_rs", "Codex"},
		{"", "claude-code", "Claude Code"},
		{"", "claude-ai", "Claude Desktop"},
		{"Zed Agent", "zed", "Zed Agent"},
		{"", "Księgowy", "Księgowy"},
	} {
		if got := AppName(c.title, c.name); got != c.want {
			t.Fatalf("AppName(%q, %q) = %q, want %q", c.title, c.name, got, c.want)
		}
	}
}

func TestDesktopKeepsAnotherServerOfTheSameName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"vult-dev":{"command":"/somewhere/else"},"other":{"command":"x"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := RegisterDesktop(path, Entry{Server: "vult-dev", Program: "/dev/vult-dev", Args: []string{"mcp"}}); got.Status != "kept" {
		t.Fatalf("status %q, want kept", got.Status)
	}
	data, _ := os.ReadFile(path)
	var config struct {
		Servers map[string]struct{ Command string } `json:"mcpServers"`
	}
	if json.Unmarshal(data, &config) != nil || config.Servers["vult-dev"].Command != "/somewhere/else" || config.Servers["other"].Command != "x" {
		t.Fatalf("settings changed: %s", data)
	}
}

// A stand-in codex that keeps one server per file, as `codex mcp get/add` do.
const fakeCodex = `#!/bin/sh
dir="$FAKE_CODEX_DIR"
if [ "$2" = get ]; then
  [ -f "$dir/$3" ] || { echo "Error: No MCP server named '$3' found." >&2; exit 1; }
  printf '{"name":"%s","transport":{"type":"stdio","command":"%s"}}' "$3" "$(cat "$dir/$3")"; exit 0
fi
if [ "$2" = add ]; then printf '%s' "$5" > "$dir/$3"; echo "Added global MCP server '$3'."; exit 0; fi
exit 2
`

func TestCodexIsAddedOnceAndAnotherServerIsKept(t *testing.T) {
	bin, store := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(fakeCodex), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/bin:/usr/bin")
	t.Setenv("FAKE_CODEX_DIR", store)
	t.Setenv("CODEX_HOME", "")
	e := Entry{Server: "vult-dev", Program: "/dev/vult-dev", Args: []string{"mcp"}}
	ctx := context.Background()
	if got, ok := RegisterCodex(ctx, e); !ok || got.Status != "registered" {
		t.Fatalf("first install: %+v", got)
	}
	if got, _ := RegisterCodex(ctx, e); got.Status != "already_registered" {
		t.Fatalf("second install: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(store, "vult-dev"), []byte("/somewhere/else"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := RegisterCodex(ctx, e); got.Status != "kept" {
		t.Fatalf("another server of that name: %+v", got)
	}
	if data, _ := os.ReadFile(filepath.Join(store, "vult-dev")); string(data) != "/somewhere/else" {
		t.Fatalf("another server was replaced: %s", data)
	}
}
