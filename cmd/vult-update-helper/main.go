//go:build darwin || linux

// vult-update-helper installs Vult updates when the owner asks, on macOS and
// Ubuntu. launchd or systemd starts it as root when the owner presses Update
// (the app writes the request file) and at boot, to finish or undo an
// interrupted update. It
// accepts no arguments and ignores its environment; it installs only the
// newest release that both signatures agree on, verified with its own trust
// store.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/internal/updatehelper"
	"github.com/vult-ai/vult/internal/updatetrust"
	"github.com/vult-ai/vult/internal/version"
)

// launchedByLaunchd checks how this process was started: only by launchd
// (process 1), as root, with no arguments.
func launchedByLaunchd(args []string, ppid, euid int) error {
	if len(args) != 1 {
		return errors.New("vult-update-helper takes no arguments; launchd starts it")
	}
	if ppid != 1 {
		return errors.New("vult-update-helper runs only from launchd or systemd")
	}
	if euid != 0 {
		return errors.New("vult-update-helper must run as root")
	}
	return nil
}

// repository is the trust store's record of where updates come from.
type repository struct {
	MetadataURL string `json:"metadata_url"`
	TargetsURL  string `json:"targets_url"`
}

func trustConfig(l updatehelper.Layout) (updatetrust.Config, error) {
	root, err := os.ReadFile(filepath.Join(l.Shipped(), "root.json"))
	if err != nil {
		return updatetrust.Config{}, errors.New("the trust store has no root")
	}
	data, err := os.ReadFile(filepath.Join(l.Shipped(), "repository.json"))
	var repo repository
	if err != nil || strictjson.Decode(data, &repo) != nil || !strings.HasPrefix(repo.MetadataURL, "https://") || !strings.HasPrefix(repo.TargetsURL, "https://") {
		return updatetrust.Config{}, errors.New("the trust store has no repository address")
	}
	return updatetrust.Config{InitialRoot: root, MetadataDir: filepath.Join(l.Trust(), "metadata"), MetadataURL: repo.MetadataURL, TargetsURL: repo.TargetsURL}, nil
}

func run(ctx context.Context) error {
	if err := launchedByLaunchd(os.Args, os.Getppid(), os.Geteuid()); err != nil {
		return err
	}
	l := updatehelper.DefaultLayout
	trust, err := trustConfig(l)
	if err != nil {
		return err
	}
	h := &updatehelper.Helper{
		Layout:       l,
		System:       updatehelper.NewSystem(l),
		Trust:        trust,
		Platform:     platform,
		OSVersion:    osVersion(),
		Version:      version.Version,
		Request:      l.State,
		CheckRelease: checkRelease(l),
	}
	status, err := h.Run(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s\n", status.State, status.Installed, status.Reason)
	return nil
}

func main() {
	// The helper finishes or undoes what it started even if asked to stop;
	// launchd's own timeout is the backstop.
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "vult-update-helper:", err)
		os.Exit(1)
	}
}
