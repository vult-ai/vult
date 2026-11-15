// Package agentservice connects a Book's agent chat to an agent that runs
// somewhere else: an agent service.
//
// The service is given no Book. For each turn this package tells the service
// what the person said and which tools the agent's grant allows. The service
// answers with tool calls; each one runs through the ledger under the
// agent's own grant in that Book, and what it returned goes back to the
// service, until the service says the turn is done. Nothing here decides
// anything about the books.
//
// The protocol is two JSON requests, each answered with a step:
//
//	POST /v1/turns                  {"account", "plan"?, "book", "kind", "text", "tools"}
//	POST /v1/turns/{turn}/results   {"results": [{"id", "content", "refused"?, "failed"?}]}
//
// A step is {"turn", "calls": [{"id", "tool", "input"}], "events": [{"name",
// "data"}], "done", "problem"}. Both requests carry the service's token as a
// bearer credential.
//
// A Client implements server.AgentChat, and its ActivityArrived method fits
// server.Options.ActivityArrived, so new bank activity is sorted as it
// arrives.
package agentservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Service is where an agent service is reached and the credential presented
// to it.
type Service struct {
	address, token string
	client         *http.Client
}

// ErrAddress is returned for an address that isn't an HTTPS origin, or an
// HTTP origin on this computer, or for a missing token.
var ErrAddress = errors.New("agent service: the address must be an HTTPS origin, or an HTTP one on this computer, and a token is required")

// NewService checks an agent service's address and token. The address is an
// origin such as https://agent.example.com; anything but this computer is
// reached over HTTPS only. Any path is ignored.
func NewService(address, token string) (*Service, error) {
	u, err := url.Parse(address)
	if err != nil || token == "" || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return nil, ErrAddress
	}
	if ip := net.ParseIP(u.Hostname()); u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return nil, ErrAddress
	}
	return &Service{address: u.Scheme + "://" + u.Host, token: token, client: &http.Client{Timeout: 6 * time.Minute}}, nil
}

// FromEnvironment reads VULT_AGENT_SERVICE (the address) and
// VULT_AGENT_SERVICE_TOKEN (the credential). It returns nil when either is
// missing or the address isn't one NewService accepts: there is then no
// agent service to talk to.
func FromEnvironment() *Service {
	s, err := NewService(os.Getenv("VULT_AGENT_SERVICE"), os.Getenv("VULT_AGENT_SERVICE_TOKEN"))
	if err != nil {
		return nil
	}
	return s
}

// step is the service's answer: tools to run now, what to show, and whether
// the turn is over.
type step struct {
	Turn  string `json:"turn"`
	Calls []struct {
		ID    string          `json:"id"`
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	} `json:"calls"`
	Events  []event `json:"events"`
	Done    bool    `json:"done"`
	Problem string  `json:"problem"`
}

type event struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

func (s *Service) post(ctx context.Context, path string, body any) (step, error) {
	var out step
	raw, err := json.Marshal(body)
	if err != nil {
		return out, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, s.address+path, bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+s.token)
	w, err := s.client.Do(r)
	if err != nil {
		return out, err
	}
	defer w.Body.Close()
	if w.StatusCode != http.StatusOK {
		return out, errors.New("agent service: " + w.Status)
	}
	return out, json.NewDecoder(io.LimitReader(w.Body, 8<<20)).Decode(&out)
}
