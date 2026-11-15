//go:build devweb

package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/vult-ai/vult/agentservice"
	"github.com/vult-ai/vult/ledger"
)

// keptConversations keeps a development Vult's conversations in its own
// private folder, one file per Book, so they are still there after it is
// restarted. Other builds keep them only while they run.
func keptConversations() agentservice.Conversations {
	self, err := os.Executable()
	if err != nil {
		return &agentservice.Memory{}
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return filed{folder: filepath.Join(filepath.Dir(self), "conversations")}
}

type filed struct{ folder string }

func (f filed) path(tenant, book string) (string, error) {
	if !ledger.ValidID(tenant) || !ledger.ValidID(book) {
		return "", ledger.ErrInvalid
	}
	return filepath.Join(f.folder, tenant+"-"+book+".json"), nil
}

func (f filed) Load(tenant, book string) (agentservice.Conversation, error) {
	var c agentservice.Conversation
	path, err := f.path(tenant, book)
	if err != nil {
		return c, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	// A file that can't be read as a conversation starts a new one.
	_ = json.Unmarshal(raw, &c)
	return c, nil
}

func (f filed) Save(tenant, book string, c agentservice.Conversation) error {
	path, err := f.path(tenant, book)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.folder, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".new", raw, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".new", path)
}

func (f filed) Forget(tenant, book string) error {
	path, err := f.path(tenant, book)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
