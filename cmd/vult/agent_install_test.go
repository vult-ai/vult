package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentInstallKeepsOtherDesktopServersAndInstallsTheSkill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude_desktop_config.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark","mcpServers":{"example":{"command":"example-server"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := registerDesktop(path, "/usr/local/bin/vult"); got.Status != "registered" {
		t.Fatal("Vult was not added to the desktop settings", got)
	}
	var config struct {
		Theme   string                     `json:"theme"`
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	data, _ := os.ReadFile(path)
	var example struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(data, &config) != nil || config.Theme != "dark" || len(config.Servers) != 2 || json.Unmarshal(config.Servers["example"], &example) != nil || example.Command != "example-server" {
		t.Fatal("other settings changed", string(data))
	}
	var vult struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if json.Unmarshal(config.Servers["vult"], &vult) != nil || vult.Command != "/usr/local/bin/vult" || len(vult.Args) < 1 || vult.Args[0] != "mcp" {
		t.Fatal("the Vult entry has no connection-free command", string(config.Servers["vult"]))
	}
	if got := registerDesktop(path, "/usr/local/bin/vult"); got.Status != "already_registered" {
		t.Fatal("a second run changed an existing entry", got)
	}
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := registerDesktop(broken, "/usr/local/bin/vult"); got.Status != "failed" {
		t.Fatal("an unreadable settings file was overwritten", got)
	}
	if data, _ := os.ReadFile(broken); string(data) != "{not json" {
		t.Fatal("an unreadable settings file changed")
	}
	if got := installSkill(filepath.Join(dir, "skills")); got.Status != "installed" {
		t.Fatal(got)
	}
	skill, err := os.ReadFile(filepath.Join(dir, "skills", "vult-bookkeeping", "SKILL.md"))
	if err != nil || len(skill) == 0 {
		t.Fatal("the bookkeeping skill was not written", err)
	}
}

func TestDoctorIsReadyWithOneConnectedAgent(t *testing.T) {
	base := []check{{Name: "Vult installed", OK: true}, {Name: "Ledger service running", OK: true}}
	with := func(extra ...check) []check { return append(append([]check{}, base...), extra...) }
	if !ready(with(check{Name: "Claude Code connected", OK: true, Agent: true}, check{Name: "Claude Desktop connected", Agent: true})) {
		t.Fatal("an optional agent kept Vult from being ready")
	}
	if ready(with(check{Name: "Claude Code connected", Agent: true})) {
		t.Fatal("Vult was ready with no agent connected")
	}
	if ready(append(with(check{Name: "Claude Code connected", OK: true, Agent: true}), check{Name: "Browser sign-in set up"})) {
		t.Fatal("Vult was ready with a required check failing")
	}
}
