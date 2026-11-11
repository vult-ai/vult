package updatehelper

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/vult-ai/vult/internal/local"
)

// DefaultLayout is where an Ubuntu installation keeps things. The release
// archive carries the program under opt/vult/ beside system files, which an
// update never changes.
var DefaultLayout = Layout{
	Program:       "/opt/vult",
	TrustFiles:    "/opt/vult-updater",
	ArchivePrefix: "opt/vult/",
	Data:          local.DataPath,
	Copies:        "/var/lib/vult/updater/copies",
	State:         "/var/lib/vult/updater",
}

// LinuxSystem is the real System on Ubuntu: systemd runs the services.
type LinuxSystem struct{ unixSystem }

// NewSystem returns the System for this computer.
func NewSystem(l Layout) System { return LinuxSystem{unixSystem{Layout: l}} }

func systemctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", args...)
	cmd.Env = fixedEnv()
	return cmd.Run()
}

func (s LinuxSystem) StopServices(ctx context.Context) error {
	for _, unit := range []string{"ai.vult.browser.service", "ai.vult.ledger.service"} {
		if err := systemctl(ctx, "stop", unit); err != nil {
			return fmt.Errorf("could not stop %s", unit)
		}
	}
	return s.waitForLedgerStop(ctx)
}

func (s LinuxSystem) StartServices(ctx context.Context) error {
	if err := systemctl(ctx, "start", "ai.vult.ledger.service"); err != nil {
		return errors.New("could not start the ledger")
	}
	if err := s.Healthy(ctx); err != nil {
		return err
	}
	if err := systemctl(ctx, "start", "ai.vult.browser.service"); err != nil {
		return errors.New("could not start the browser service")
	}
	return nil
}
