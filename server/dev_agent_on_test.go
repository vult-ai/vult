//go:build devserver && browserpreview

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// TestDevAgentOn turns on, as the owner, the agent the running service
// provides in one Book of the development Vult. VULT_DEV_AGENT_BOOK names
// the Book exactly as it is named now. A Book that already has agent
// settings is left as it is.
func TestDevAgentOn(t *testing.T) {
	ctx := context.Background()
	name := os.Getenv("VULT_DEV_AGENT_BOOK")
	if name == "" {
		t.Fatal("VULT_DEV_AGENT_BOOK names the Book")
	}
	f := seedFixture(t, openDevEnv(t, false))
	defer func() { _ = f.sessions.EndSession(ctx, f.session) }()
	if err := f.sessions.OwnerAct(ctx, f.session, f.tenant, func(o ledger.TenantAdmin, who string) error {
		books, err := o.Books(ctx)
		if err != nil {
			return err
		}
		var found []ledger.Book
		for _, b := range books {
			if b.Name == name {
				found = append(found, b)
			}
		}
		if len(found) != 1 {
			return fmt.Errorf("%q names %d Books here, not one", name, len(found))
		}
		if _, err := o.AgentSettings(ctx, found[0].ID); err == nil {
			fmt.Println("DEV AGENT ALREADY SET")
			return nil
		} else if !errors.Is(err, ledger.ErrNotFound) {
			return err
		}
		_, err = o.SaveAgentSettings(ctx, found[0].ID, who, ledger.AgentSettingsChange{Enabled: true})
		if err == nil {
			fmt.Println("DEV AGENT ON")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
