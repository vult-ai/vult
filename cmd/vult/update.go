package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/vult-ai/vult/internal/daemon"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/update"
)

// runUpdate verifies the latest release's installer, backs up every Book, then
// runs the installer. --check only says
// whether a newer release is published, for owners and their agents.
func runUpdate(ctx context.Context, args []string, out io.Writer) error {
	f := flags("update")
	check := f.Bool("check", false, "only report whether a newer release is published")
	if err := parse(f, args); err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	release, err := update.LatestRelease(ctx, client, update.ManifestURL)
	if err != nil {
		return errors.New("could not check for a newer Vult release; check the connection and try again")
	}
	latest := release.Version
	status := update.Status{Current: daemon.Version, Latest: latest, Available: update.Newer(daemon.Version, latest)}
	if *check {
		return jsonOut(out, status)
	}
	if !status.Available {
		fmt.Fprintf(out, "Vult %s is up to date.\n", daemon.Version)
		return nil
	}
	if runtime.GOOS != "darwin" {
		fmt.Fprintf(out, "Vult %s is available. Download its Ubuntu archive and install.sh from https://github.com/vult-ai/vult/releases/tag/v%s, then run: sh install.sh ./vult-%s-ubuntu-24.04-amd64.tar.gz\n", latest, latest, latest)
		return nil
	}
	if os.Geteuid() == 0 {
		return errors.New("run vult update as yourself; macOS asks for administrator approval")
	}
	backup, err := updateBackupPath(latest)
	if err != nil {
		return err
	}
	return installUpdate(ctx, release, backup, client, out, runArchive)
}

func installUpdate(ctx context.Context, release update.Release, backup string, client *http.Client, out io.Writer, backupBooks func(context.Context, []string, io.Writer) error) error {
	fmt.Fprintf(out, "Updating Vult %s to %s. Checking the installer.\n", daemon.Version, release.Version)
	script, err := downloadInstaller(ctx, client, release)
	if err != nil {
		return fmt.Errorf("the installer could not be verified, so nothing was installed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("the update was canceled before installation: %w", err)
	}
	fmt.Fprintf(out, "A backup of every Book: %s\n", backup)
	if err := backupBooks(ctx, []string{"backup", "--output", backup}, io.Discard); err != nil {
		return fmt.Errorf("the backup before updating did not complete, so nothing was installed: %w", err)
	}
	// Native approval owns cancellation after handoff. Killing only this shell
	// could leave its privileged installer running with no result to wait for.
	cmd := exec.Command("/bin/sh")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(script), out, os.Stderr
	fmt.Fprintln(out, "Complete or cancel the macOS approval prompt. Vult waits for the installer to finish.")
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("the update was canceled before installation; the backup is at %s: %w", backup, err)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("the update did not complete; inspect the system install log before retrying; the backup is at %s: %w", backup, err)
	}
	fmt.Fprintf(out, "Vult %s is installed. Run %s open to continue.\n", release.Version, local.BinaryPath)
	return nil
}

func downloadInstaller(ctx context.Context, client *http.Client, release update.Release) ([]byte, error) {
	expected, err := hex.DecodeString(release.InstallerSHA256)
	if err != nil || len(expected) != sha256.Size {
		return nil, errors.New("the release manifest has no valid installer checksum")
	}
	url := fmt.Sprintf("https://github.com/vult-ai/vult/releases/download/v%s/install.sh", release.Version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("installer download returned HTTP %d", res.StatusCode)
	}
	const maxInstaller = 1 << 20
	script, err := io.ReadAll(io.LimitReader(res.Body, maxInstaller+1))
	if err != nil {
		return nil, fmt.Errorf("installer download did not complete: %w", err)
	}
	if len(script) > maxInstaller {
		return nil, errors.New("installer download exceeds 1 MiB")
	}
	digest := sha256.Sum256(script)
	if !bytes.Equal(digest[:], expected) {
		return nil, errors.New("installer checksum mismatch")
	}
	return script, nil
}

// updateBackupPath is a new file in the owner's own Vult backups folder.
func updateBackupPath(next string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "Library", "Application Support", "Vult", "Backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("before-%s-%s.age", next, time.Now().Format("20060102-150405"))), nil
}
