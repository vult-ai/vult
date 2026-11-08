//go:build !linux

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"os/exec"
	"strings"
)

func localBrowserTLSConfig(string) (*tls.Config, error) {
	return &tls.Config{MinVersion: tls.VersionTLS13}, nil
}

func openLocalBrowser(ctx context.Context, destination string) error {
	// The one-use fragment is sent to the system opener through stdin.
	command := exec.CommandContext(ctx, "/usr/bin/osascript")
	command.Stdin = strings.NewReader(fmt.Sprintf("open location %q\n", destination))
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	return command.Run()
}
