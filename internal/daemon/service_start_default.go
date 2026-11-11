//go:build !linux

package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"github.com/vult-ai/vult/internal/local"
)

func launchctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C"}
	return cmd.Run()
}

// nativeRunning reports whether the ledger service is loaded.
func nativeRunning(ctx context.Context) bool {
	return launchctl(ctx, "print", "system/ai.vult.ledger") == nil
}

// nativeStart starts the ledger, waits until it answers, then starts the
// browser service if it is installed.
func nativeStart(ctx context.Context) error {
	if err := launchctl(ctx, "bootstrap", "system", ledgerStartup); err != nil && !nativeRunning(ctx) {
		return errors.New("could not start the ledger service")
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if c, err := local.NewClient(); err == nil {
			probe, done := context.WithTimeout(ctx, 2*time.Second)
			_, err = c.Call(probe, "status", "", struct{}{})
			done()
			if err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			return errors.New("the ledger service did not become ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if _, err := os.Lstat(browserStartup); err == nil {
		if err := launchctl(ctx, "bootstrap", "system", browserStartup); err != nil && launchctl(ctx, "print", "system/ai.vult.browser") != nil {
			return errors.New("could not start the browser service")
		}
	}
	return nil
}
