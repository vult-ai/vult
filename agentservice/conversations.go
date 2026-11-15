package agentservice

import (
	"errors"
	"sync"

	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

// Conversation is what was said in a Book, as the owner reads it.
type Conversation struct {
	Revision string            `json:"revision,omitempty"`
	Turns    []server.ChatTurn `json:"turns"`
}

// Conversations keeps each Book's conversation.
type Conversations interface {
	Load(tenant, book string) (Conversation, error)
	Save(tenant, book string, c Conversation) error
	Forget(tenant, book string) error
}

// ErrConversationChanged means a reply belongs to an older conversation.
var ErrConversationChanged = errors.New("conversation changed while the reply was running")

// ConditionalConversations prevents an older reply overwriting a clear or
// another writer. The revision is the one returned by Load.
type ConditionalConversations interface {
	Conversations
	SaveIfUnchanged(tenant, book string, c Conversation) error
}

// Memory keeps conversations until the program stops. Its zero value is
// ready to use.
type Memory struct {
	mu sync.Mutex
	m  map[string]Conversation
}

func (r *Memory) Load(tenant, book string) (Conversation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.m[tenant+"/"+book]
	c.Turns = append([]server.ChatTurn(nil), c.Turns...)
	return c, nil
}

func (r *Memory) Save(tenant, book string, c Conversation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]Conversation{}
	}
	c.Revision = ledger.ID()
	c.Turns = append([]server.ChatTurn(nil), c.Turns...)
	r.m[tenant+"/"+book] = c
	return nil
}

func (r *Memory) Forget(tenant, book string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]Conversation{}
	}
	r.m[tenant+"/"+book] = Conversation{Revision: ledger.ID()}
	return nil
}

func (r *Memory) SaveIfUnchanged(tenant, book string, c Conversation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m[tenant+"/"+book].Revision != c.Revision {
		return ErrConversationChanged
	}
	if r.m == nil {
		r.m = map[string]Conversation{}
	}
	c.Revision = ledger.ID()
	c.Turns = append([]server.ChatTurn(nil), c.Turns...)
	r.m[tenant+"/"+book] = c
	return nil
}
