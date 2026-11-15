package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/vult-ai/vult/agentconfig"
	"github.com/vult-ai/vult/ledger"
)

// A Vult keeps no model and no model key. With no agent connected, the
// settings say so and offer signing in where the Vult knows a place to;
// a model, an address or a key is refused outright.
func TestAgentSettingsTakeOnlyOnOrOff(t *testing.T) {
	var agent *agentconfig.Service
	f, s, _ := bankServerWith(t, func(o *Options) {
		agent = &agentconfig.Service{Store: ledger.Store{Runtime: o.Database}}
		o.Agent = agent
	})
	read := func() map[string]any {
		t.Helper()
		w := bankCall(t, s, f.session, http.MethodGet, agentSettingsPath+"?book_id="+f.book.ID, "")
		var out map[string]any
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("read: %d %s", w.Code, w.Body.String())
		}
		return out
	}
	save := func(body string) (int, string) {
		t.Helper()
		w := bankCall(t, s, f.session, http.MethodPost, agentSettingsPath, body)
		return w.Code, w.Body.String()
	}
	book := `"book_id":"` + f.book.ID + `"`

	// Not connected: nothing to turn on, and no sign-in offered until the
	// Vult knows where.
	out := read()
	if out["connected"] != false || out["settings"] != nil || out["provided"] != nil || out["sign_in_url"] != nil || out["presets"] != nil {
		t.Fatalf("not connected: %v", out)
	}
	agent.SignInURL = "https://account.example.com/connect"
	if out = read(); out["sign_in_url"] != "https://account.example.com/connect" {
		t.Fatalf("sign-in: %v", out)
	}
	if code, body := save(`{` + book + `,"enabled":true}`); code != http.StatusUnprocessableEntity || !strings.Contains(body, "Sign in to Vult") {
		t.Fatalf("turning on an agent that isn't connected: %d %s", code, body)
	}
	// A model, an address or a key is never taken, connected or not.
	for _, extra := range []string{`"key":"sk-synthetic"`, `"provider":"anthropic"`, `"base_url":"https://api.example.com"`, `"model":"example-model"`} {
		if code, body := save(`{` + book + `,"enabled":false,` + extra + `}`); code != http.StatusBadRequest {
			t.Fatalf("%s was taken: %d %s", extra, code, body)
		}
	}
	for _, gone := range []string{"/test", "/remove-key"} {
		if w := bankCall(t, s, f.session, http.MethodPost, agentSettingsPath+gone, `{`+book+`}`); w.Code != http.StatusNotFound {
			t.Fatalf("%s still answers: %d", gone, w.Code)
		}
	}

	// Connected: the agent is on from the first look, and turns off and on.
	agent.Provided = &agentconfig.Provided{Name: "Example agent", About: "An example agent."}
	out = read()
	settings, _ := out["settings"].(map[string]any)
	if out["connected"] != true || out["sign_in_url"] != nil || settings == nil || settings["enabled"] != true {
		t.Fatalf("connected: %v", out)
	}
	for _, kept := range []string{"provider", "base_url", "model", "has_key", "key_state"} {
		if _, ok := settings[kept]; ok {
			t.Fatalf("settings still carry %s: %v", kept, settings)
		}
	}
	if code, body := save(`{` + book + `,"enabled":false,"expected_revision":1}`); code != http.StatusOK || !strings.Contains(body, `"enabled":false`) {
		t.Fatalf("turning off: %d %s", code, body)
	}
}
