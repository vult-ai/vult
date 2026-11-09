package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/daemon"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/loopback"
)

func localBrowserTLSConfig(host string) (*tls.Config, error) {
	leaf, err := daemon.BrowserCertificate(host)
	if err != nil {
		return nil, err
	}
	// This verifies service readiness against the protected exact leaf.
	// Chrome's separate NSS trust is checked by RequestBrowserTrust.
	return pinnedBrowserTLS(host, leaf)
}

func publicBrowserDestination(destination string) (string, string, error) {
	parsed, err := url.Parse(destination)
	if err != nil {
		return "", "", loopback.ErrCertificate
	}
	setup := parsed.Path == "/auth/setup"
	parsed, err = localBrowserAddress(destination, setup)
	if err != nil {
		return "", "", err
	}
	var code string
	if setup {
		fragment, _ := url.ParseQuery(parsed.Fragment)
		code = fragment.Get("setup")
	}
	parsed.Fragment, parsed.RawFragment = "", ""
	return parsed.String(), code, nil
}

func openLocalBrowser(ctx context.Context, destination string) error {
	public, code, err := publicBrowserDestination(destination)
	if err != nil {
		return err
	}
	session, err := local.CurrentDesktopSession()
	if err != nil {
		return err
	}
	launchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(launchCtx, "/usr/bin/gio", "launch", "/usr/share/applications/google-chrome.desktop", public)
	command.Env = session.Environment
	if err := command.Run(); err != nil {
		return errors.New("Chrome did not open; install the supported Chrome desktop application")
	}
	if code == "" {
		return nil
	}
	promptCtx, cancelPrompt := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelPrompt()
	prompt := exec.CommandContext(promptCtx, "/usr/bin/zenity", "--text-info", "--filename=/dev/stdin", "--title=Vult setup", "--ok-label=Done", "--cancel-label=Cancel", "--width=640", "--height=280")
	prompt.Env = session.Environment
	prompt.Stdin = strings.NewReader("Enter this setup code in Vult's Chrome window. It works once and expires after five minutes.\n\n" + code + "\n\nKeep this dialog private. Close it when passkey setup is complete.")
	if err := prompt.Run(); err != nil {
		return errors.New("local setup dialog was canceled or did not complete")
	}
	return nil
}
