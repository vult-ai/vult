// Package agentconfig is the slot for Vult's own agent: whether one is
// connected to this Vult, and whether it is on in each Book.
//
// A Vult runs no model and keeps no model key. Vult's agent is a service
// the Vult is connected to by signing in to a Vult account; without that
// there is no built-in agent, and a person connects their own over MCP.
// Either way the agent acts only through the same tools and grants as any
// agent, from a proposing connection made for it.
package agentconfig

import (
	"context"

	"github.com/vult-ai/vult/ledger"
)

// Provided describes the agent this Vult is connected to, for the settings
// screen: on or off in each Book.
type Provided struct {
	Name  string `json:"name"`
	About string `json:"about"`
}

// Settings are what the agent's runner needs for a Book.
type Settings struct {
	Enabled bool
	// Scope is the agent's own proposing connection in the Book.
	Scope ledger.Scope
}

// Input is what the owner chooses for a Book.
type Input struct {
	Enabled          bool  `json:"enabled"`
	ExpectedRevision int64 `json:"expected_revision"`
}

// Service reads and saves the settings.
type Service struct {
	Store ledger.Store
	// Provided is set when this Vult is connected to Vult's agent.
	Provided *Provided
	// SignInURL is where a person signs in to a Vult account to connect
	// Vult's agent, when this Vult knows such a place.
	SignInURL string
}

// NotConnected refuses turning on an agent this Vult isn't connected to.
var NotConnected = &ledger.RefusedError{Reason: "Vult’s agent isn’t connected to this Vult. Sign in to Vult to use it, or connect your own agent."}

// Save turns the Book's agent on or off. With no agent connected there is
// nothing to turn on; turning one off is always allowed.
func (s *Service) Save(ctx context.Context, o ledger.TenantAdmin, book, actor string, q Input) (ledger.AgentSettings, error) {
	if s.Provided == nil && q.Enabled {
		return ledger.AgentSettings{}, NotConnected
	}
	return o.SaveAgentSettings(ctx, book, actor, ledger.AgentSettingsChange{Enabled: q.Enabled, ExpectedRevision: q.ExpectedRevision})
}

// Load reads a Book's settings for the agent's runner.
func (s *Service) Load(ctx context.Context, tenant, book string) (Settings, error) {
	v, err := s.Store.AgentSecret(ctx, tenant, book)
	if err != nil {
		return Settings{}, err
	}
	return Settings{Enabled: v.Enabled, Scope: ledger.Scope{TenantID: tenant, BookID: book, ActorID: v.ActorID, ConnectionID: v.ConnectionID, Access: "propose"}}, nil
}

// Enabled lists the Books whose built-in agent is on.
func (s *Service) Enabled(ctx context.Context) ([]ledger.AgentTarget, error) {
	return s.Store.AgentBooks(ctx)
}
