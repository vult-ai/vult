package agentservice

import (
	"testing"

	"github.com/vult-ai/vult/server"
)

func TestAServiceIsReachedOverHTTPSOrOnThisComputer(t *testing.T) {
	for address, want := range map[string]string{
		"https://agent.example.com":             "https://agent.example.com",
		"https://agent.example.com/v1/turns":    "https://agent.example.com",
		"http://127.0.0.1:9000":                 "http://127.0.0.1:9000",
		"http://localhost:9000":                 "http://localhost:9000",
		"http://[::1]:9000":                     "http://[::1]:9000",
		"http://agent.example.com":              "",
		"https://user:secret@agent.example.com": "",
		"https://agent.example.com/?a=b":        "",
		"agent.example.com":                     "",
		"":                                      "",
	} {
		s, err := NewService(address, "token")
		switch {
		case want == "" && err == nil:
			t.Errorf("%q was accepted", address)
		case want != "" && (err != nil || s.address != want):
			t.Errorf("%q: %+v, %v", address, s, err)
		}
	}
	if _, err := NewService("https://agent.example.com", ""); err == nil {
		t.Error("a service without a token was accepted")
	}
}

func TestTheEnvironmentNamesTheService(t *testing.T) {
	t.Setenv("VULT_AGENT_SERVICE", "https://agent.example.com")
	t.Setenv("VULT_AGENT_SERVICE_TOKEN", "")
	if FromEnvironment() != nil {
		t.Fatal("a service without a token was configured")
	}
	t.Setenv("VULT_AGENT_SERVICE_TOKEN", "token")
	if s := FromEnvironment(); s == nil || s.address != "https://agent.example.com" || s.token != "token" {
		t.Fatalf("service: %+v", s)
	}
	t.Setenv("VULT_AGENT_SERVICE", "http://agent.example.com")
	if FromEnvironment() != nil {
		t.Fatal("a service over plain HTTP elsewhere was configured")
	}
}

func TestMemoryKeepsEachBooksConversation(t *testing.T) {
	var kept Memory
	said := Conversation{Turns: []server.ChatTurn{{ID: "1", Role: "person", Text: "hello"}}}
	if err := kept.Save("t1", "b1", said); err != nil {
		t.Fatal(err)
	}
	if got, err := kept.Load("t1", "b1"); err != nil || len(got.Turns) != 1 || got.Turns[0].Text != "hello" {
		t.Fatalf("kept: %+v, %v", got, err)
	}
	if got, _ := kept.Load("t2", "b1"); len(got.Turns) != 0 {
		t.Fatalf("another tenant reads %+v", got)
	}
	if err := kept.Forget("t1", "b1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := kept.Load("t1", "b1"); len(got.Turns) != 0 {
		t.Fatalf("a forgotten conversation is still read: %+v", got)
	}
}
