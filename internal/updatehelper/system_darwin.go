package updatehelper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/vult-ai/vult/internal/local"
)

// DefaultLayout is where a macOS installation keeps things.
var DefaultLayout = Layout{
	Program: "/Library/PrivilegedHelperTools/ai.vult.ledger",
	Data:    local.DataPath,
	Copies:  "/Library/Application Support/ai.vult.updater/copies",
	State:   "/Library/Application Support/ai.vult.updater",
}

const (
	ledgerPlist  = "/Library/LaunchDaemons/ai.vult.ledger.plist"
	browserPlist = "/Library/LaunchDaemons/ai.vult.browser.plist"
)

// MacSystem is the real System on macOS: launchd runs the services.
type MacSystem struct{ unixSystem }

// NewSystem returns the System for this computer.
func NewSystem(l Layout) System { return MacSystem{unixSystem{Layout: l}} }

func launchctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	cmd.Env = fixedEnv()
	return cmd.Run()
}

func (m MacSystem) StopServices(ctx context.Context) error {
	for _, label := range []string{"system/ai.vult.browser", "system/ai.vult.ledger"} {
		if launchctl(ctx, "print", label) == nil {
			if err := launchctl(ctx, "bootout", label); err != nil && launchctl(ctx, "print", label) == nil {
				return fmt.Errorf("could not stop %s", label)
			}
		}
	}
	return m.waitForLedgerStop(ctx)
}

func (m MacSystem) StartServices(ctx context.Context) error {
	if launchctl(ctx, "print", "system/ai.vult.ledger") != nil {
		if err := launchctl(ctx, "bootstrap", "system", ledgerPlist); err != nil {
			return errors.New("could not start the ledger")
		}
	}
	if err := m.Healthy(ctx); err != nil {
		return err
	}
	if _, err := os.Lstat(browserPlist); err == nil && launchctl(ctx, "print", "system/ai.vult.browser") != nil {
		if err := launchctl(ctx, "bootstrap", "system", browserPlist); err != nil {
			return errors.New("could not start the browser service")
		}
	}
	return nil
}
