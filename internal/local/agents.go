package local

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// An agent on this computer asks for access without any owner secret. The
// owner approves it in the local browser with a recent passkey sign-in, and
// the agent's bridge then receives its session.

const (
	AgentWaiting  = "waiting"
	AgentApproved = "approved"
	AgentDenied   = "denied"
	AgentExpired  = "expired"
)

type AgentRequest struct {
	Client string `json:"client"`
}

// AgentClientName keeps a short, printable label that the approval page shows
// as the agent's own description of itself.
func AgentClientName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 80 {
		return "", false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return "", false
		}
	}
	return s, true
}

type AgentTicket struct {
	Request    string `json:"request"`
	ApproveURL string `json:"approve_url"`
}

type AgentRequestStatus struct {
	Request string `json:"request"`
}

type AgentStatus struct {
	State     string     `json:"state"`
	Token     string     `json:"token,omitempty"`
	Access    string     `json:"access,omitempty"`
	BookID    string     `json:"book_id,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type AgentRequestView struct {
	Request string `json:"request"`
}

type AgentView struct {
	Client    string    `json:"client"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentApproval struct {
	Request string `json:"request"`
	BookID  string `json:"book_id,omitempty"`
	Access  string `json:"access,omitempty"`
	Deny    bool   `json:"deny,omitempty"`
}

func (a AgentApproval) Valid() bool {
	if !validRequestID(a.Request) {
		return false
	}
	if a.Deny {
		return a.BookID == "" && a.Access == ""
	}
	return ledger.ValidID(a.BookID) && ledger.ValidAccess(a.Access)
}

func validRequestID(s string) bool {
	if len(s) != 43 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// ValidAgentRequestID reports whether s has the shape of an agent request ID.
func ValidAgentRequestID(s string) bool { return validRequestID(s) }

// RequestAgent asks the ledger service for a new agent approval request.
func (c Client) RequestAgent(ctx context.Context, client string) (AgentTicket, error) {
	var out AgentTicket
	data, err := c.Call(ctx, "agent_request", "", AgentRequest{Client: client})
	if err != nil {
		return out, err
	}
	if strictjson.Decode(data, &out) != nil || !validRequestID(out.Request) {
		return AgentTicket{}, ledger.ErrInvalid
	}
	return out, nil
}

// AgentStatus reports whether the owner has decided. An approved session is
// returned once, to the process user that asked.
func (c Client) AgentStatus(ctx context.Context, request string) (AgentStatus, error) {
	var out AgentStatus
	data, err := c.Call(ctx, "agent_request_status", "", AgentRequestStatus{Request: request})
	if err != nil {
		return out, err
	}
	if strictjson.Decode(data, &out) != nil {
		return AgentStatus{}, ledger.ErrInvalid
	}
	return out, nil
}

// ViewAgent is called by the local browser service with the owner's session.
func (c Client) ViewAgent(ctx context.Context, session, request string) (AgentView, error) {
	var out AgentView
	data, err := c.Call(ctx, "owner_agent_view", session, AgentRequestView{Request: request})
	if err != nil {
		return out, err
	}
	if strictjson.Decode(data, &out) != nil {
		return AgentView{}, ledger.ErrInvalid
	}
	return out, nil
}

// ApproveAgent is called by the local browser service with the owner's session.
func (c Client) ApproveAgent(ctx context.Context, session string, approval AgentApproval) error {
	_, err := c.Call(ctx, "owner_agent_approve", session, approval)
	return err
}

// BookDraft is what an agent proposes for the owner's first Book. It creates
// nothing until the owner confirms it in the browser.
type BookDraft struct {
	Name     string `json:"name"`
	Profile  string `json:"profile,omitempty"`
	Currency string `json:"currency"`
	Timezone string `json:"timezone,omitempty"`
}

func (d BookDraft) Valid() bool {
	name := strings.TrimSpace(d.Name)
	if name == "" || len(d.Name) > 120 || len(d.Currency) != 3 || strings.ToUpper(d.Currency) != d.Currency {
		return false
	}
	if d.Profile != "" && d.Profile != "personal" && d.Profile != "business" {
		return false
	}
	if len(d.Timezone) > 64 {
		return false
	}
	for _, r := range d.Name + d.Timezone {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// DraftBook stores the agent's proposal for the owner's first Book.
func (c Client) DraftBook(ctx context.Context, draft BookDraft) error {
	_, err := c.Call(ctx, "book_draft", "", draft)
	return err
}

// BookDraftView is called by the local browser service with the owner's session.
func (c Client) BookDraftView(ctx context.Context, session string) (*BookDraft, error) {
	data, err := c.Call(ctx, "owner_book_draft", session, struct{}{})
	if err != nil {
		return nil, err
	}
	if string(data) == "null" {
		return nil, nil
	}
	var out BookDraft
	if strictjson.Decode(data, &out) != nil {
		return nil, ledger.ErrInvalid
	}
	return &out, nil
}

type OwnerBookCreate struct {
	Name     string `json:"name"`
	Currency string `json:"currency"`
	Scale    int    `json:"scale"`
}

// CreateBookForOwner is called by the local browser service with the owner's
// session. The ledger service requires a recent passkey sign-in.
func (c Client) CreateBookForOwner(ctx context.Context, session string, q OwnerBookCreate) (ledger.Book, error) {
	var out ledger.Book
	data, err := c.Call(ctx, "owner_book_create", session, q)
	if err != nil {
		return out, err
	}
	if json.Unmarshal(data, &out) != nil || !ledger.ValidID(out.ID) {
		return ledger.Book{}, ledger.ErrInvalid
	}
	return out, nil
}

// SetupSecrets are shown once in the owner's browser after a passkey is set up.
type SetupSecrets struct {
	RecoveryCodes []string `json:"recovery_codes"`
	BackupKey     string   `json:"backup_key,omitempty"`
}

// SetupSecrets is called by the local browser service with the owner's new
// session. The ledger service gives them out once.
func (c Client) SetupSecrets(ctx context.Context, session string) (SetupSecrets, error) {
	var out SetupSecrets
	data, err := c.Call(ctx, "owner_recovery", session, struct{}{})
	if err != nil {
		return out, err
	}
	if strictjson.Decode(data, &out) != nil {
		return SetupSecrets{}, ledger.ErrInvalid
	}
	return out, nil
}

// Tools the ledger service answers itself for agents on this computer.
const (
	WaitForDocumentMethod = "wait_for_document"
	ReportUnusableMethod  = "report_unusable_document"
)

// AgentActivity is one step of an agent's work in a Book, as the service saw
// it. Kind is opened, checked, account, entry, review, compared, balanced or
// unusable.
type AgentActivity struct {
	Seq         int64     `json:"seq"`
	At          time.Time `json:"at"`
	Agent       string    `json:"agent"`
	Kind        string    `json:"kind"`
	Document    string    `json:"document,omitempty"`
	Name        string    `json:"name,omitempty"`
	Date        string    `json:"date,omitempty"`
	Description string    `json:"description,omitempty"`
	Reason      string    `json:"reason,omitempty"`
}

// WaitingAgent is an agent on this computer asking the owner to let it in.
type WaitingAgent struct {
	Request   string    `json:"request"`
	Client    string    `json:"client"`
	CreatedAt time.Time `json:"created_at"`
}

type ActivityQuery struct {
	BookID string `json:"book_id"`
	After  int64  `json:"after,omitempty"`
}

type ActivityPage struct {
	Events  []AgentActivity `json:"events"`
	Next    int64           `json:"next"`
	Waiting []WaitingAgent  `json:"waiting"`
	// Connected names the agents let in to this Book right now.
	Connected []string `json:"connected"`
}

// OwnerActivity is called by the local browser service with the owner's session.
func (c Client) OwnerActivity(ctx context.Context, session string, q ActivityQuery) (ActivityPage, error) {
	var out ActivityPage
	data, err := c.Call(ctx, "owner_agent_activity", session, q)
	if err != nil {
		return out, err
	}
	if strictjson.Decode(data, &out) != nil {
		return ActivityPage{}, ledger.ErrInvalid
	}
	return out, nil
}
