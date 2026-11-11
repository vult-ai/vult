package daemon

import (
	"context"
	"errors"
)

// nativeRunning reports whether the ledger service is active.
func nativeRunning(ctx context.Context) bool {
	_, err := systemctl(ctx, "is-active", "--quiet", "ai.vult.ledger.service")
	return err == nil
}

// nativeStart starts the ledger and then the browser service.
func nativeStart(ctx context.Context) error {
	if _, err := systemctl(ctx, "start", "ai.vult.ledger.service"); err != nil {
		return errors.New("could not start the ledger service")
	}
	if _, err := systemctl(ctx, "start", "ai.vult.browser.service"); err != nil {
		return errors.New("could not start the browser service")
	}
	return nil
}
