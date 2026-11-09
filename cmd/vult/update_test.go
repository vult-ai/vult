package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/update"
)

func TestUpdateRejectsUnknownOptionsBeforeCheckingTheNetwork(t *testing.T) {
	for _, args := range [][]string{{"--now"}, {"latest"}, {"--check", "extra"}} {
		if err := runUpdate(context.Background(), args, io.Discard); err == nil || err.Error() != "invalid arguments; run vult help" {
			t.Fatalf("vult update %v: %v", args, err)
		}
	}
}

type updateTransport func(*http.Request) (*http.Response, error)

func (f updateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func installerRelease(script string) update.Release {
	digest := sha256.Sum256([]byte(script))
	return update.Release{Version: "0.5.2", InstallerSHA256: hex.EncodeToString(digest[:])}
}

func TestUpdateNeverExecutesTheVerifiedInstallerWhenBackupFails(t *testing.T) {
	const script = "printf 'synthetic installer ran\\n'\n"
	backupError := errors.New("synthetic backup failure")
	downloaded := false
	client := &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
		downloaded = true
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(script))}, nil
	})}
	var output bytes.Buffer
	err := installUpdate(context.Background(), installerRelease(script), "before.age", client, &output, func(_ context.Context, args []string, _ io.Writer) error {
		if !downloaded {
			t.Fatal("backup started before the installer was verified")
		}
		if !slices.Equal(args, []string{"backup", "--output", "before.age"}) {
			t.Fatalf("backup arguments: %v", args)
		}
		return backupError
	})
	if !errors.Is(err, backupError) || !strings.Contains(err.Error(), "nothing was installed") {
		t.Fatalf("backup failure: %v", err)
	}
	if strings.Contains(output.String(), "is installed.") || strings.Contains(output.String(), "synthetic installer ran") {
		t.Fatal("failed backup ran the installer or reported success")
	}
}

func TestUpdateRunsOnlyTheVerifiedInstallerForTheSelectedRelease(t *testing.T) {
	const script = "printf 'synthetic installer ran\\n'\n"
	backedUp, requested := false, false
	client := &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
		requested = true
		if backedUp {
			t.Fatal("installer was downloaded after the backup")
		}
		if r.URL.String() != "https://github.com/vult-ai/vult/releases/download/v0.5.2/install.sh" {
			t.Fatalf("installer was not pinned to the selected release: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(script))}, nil
	})}
	var output bytes.Buffer
	err := installUpdate(context.Background(), installerRelease(script), "before.age", client, &output, func(context.Context, []string, io.Writer) error {
		if !requested {
			t.Fatal("backup started before the installer download")
		}
		backedUp = true
		return nil
	})
	if err != nil || !requested || !strings.Contains(output.String(), "synthetic installer ran") || !strings.Contains(output.String(), "Vult 0.5.2 is installed.") {
		t.Fatalf("verified installation: %v; output: %s", err, &output)
	}
	if !strings.Contains(output.String(), "Run "+local.BinaryPath+" open to continue.") {
		t.Fatalf("follow-up command depends on a shell shortcut: %s", &output)
	}
}

type incompleteInstaller struct{}

func (incompleteInstaller) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestUpdateNeverRunsAnUnverifiedOrIncompleteDownload(t *testing.T) {
	const script = "printf 'synthetic installer ran\\n'\n"
	for _, name := range []string{"network failure", "HTTP failure", "partial download", "checksum mismatch", "oversized download"} {
		t.Run(name, func(t *testing.T) {
			release := installerRelease(script)
			client := &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
				status := http.StatusOK
				var body io.Reader = strings.NewReader(script)
				switch name {
				case "network failure":
					return nil, errors.New("synthetic network failure")
				case "HTTP failure":
					status = http.StatusServiceUnavailable
				case "partial download":
					body = io.MultiReader(body, incompleteInstaller{})
				case "oversized download":
					oversized := script + strings.Repeat(" ", 1<<20)
					body = strings.NewReader(oversized)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(body)}, nil
			})}
			if name == "checksum mismatch" {
				release.InstallerSHA256 = strings.Repeat("0", 64)
			} else if name == "oversized download" {
				release = installerRelease(script + strings.Repeat(" ", 1<<20))
			}
			var output bytes.Buffer
			err := installUpdate(context.Background(), release, "before.age", client, &output, func(context.Context, []string, io.Writer) error {
				t.Fatal("backup started for an unverified installer")
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "nothing was installed") {
				t.Fatalf("unverified installer: %v", err)
			}
			if name == "partial download" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("download failure lost its cause: %v", err)
			}
			if strings.Contains(output.String(), "synthetic installer ran") || strings.Contains(output.String(), "is installed.") {
				t.Fatalf("unverified installer ran or reported success: %s", &output)
			}
		})
	}
}

func TestUpdateRejectsInvalidInstallerChecksumsBeforeDownloading(t *testing.T) {
	for _, checksum := range []string{"", "ab", strings.Repeat("z", 64)} {
		client := &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("installer requested without a valid checksum")
			return nil, nil
		})}
		var output bytes.Buffer
		err := installUpdate(context.Background(), update.Release{Version: "0.5.2", InstallerSHA256: checksum}, "before.age", client, &output, func(context.Context, []string, io.Writer) error {
			t.Fatal("backup started without a valid installer checksum")
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "release manifest has no valid installer checksum") {
			t.Fatalf("invalid installer checksum %q: %v", checksum, err)
		}
	}
}

func TestUpdateAcceptsUppercaseInstallerChecksums(t *testing.T) {
	const script = "exit 0\n"
	release := installerRelease(script)
	release.InstallerSHA256 = strings.ToUpper(release.InstallerSHA256)
	client := &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(script))}, nil
	})}
	if _, err := downloadInstaller(context.Background(), client, release); err != nil {
		t.Fatal("valid uppercase checksum was rejected", err)
	}
}

func TestUpdateCancellationBeforeHandoffNeverStartsTheInstaller(t *testing.T) {
	const script = "printf 'synthetic installer ran\\n'\n"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(script))}, nil
	})}
	var output bytes.Buffer
	err := installUpdate(ctx, installerRelease(script), "before.age", client, &output, func(context.Context, []string, io.Writer) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || strings.Contains(output.String(), "synthetic installer ran") || strings.Contains(output.String(), "is installed.") {
		t.Fatalf("cancellation before handoff: %v; output: %s", err, &output)
	}
}

type installerOutput struct {
	buffer  bytes.Buffer
	started chan struct{}
	once    sync.Once
}

func (w *installerOutput) Write(data []byte) (int, error) {
	n, err := w.buffer.Write(data)
	if bytes.Contains(w.buffer.Bytes(), []byte("synthetic installer started\n")) {
		w.once.Do(func() { close(w.started) })
	}
	return n, err
}

func (w *installerOutput) String() string { return w.buffer.String() }

func TestUpdateWaitsForTheInstallerAfterHandoffEvenIfTheCallerCancels(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "installer-gate")
	if err := syscall.Mkfifo(gate, 0600); err != nil {
		t.Fatal(err)
	}
	control, err := os.OpenFile(gate, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	quotedGate := "'" + strings.ReplaceAll(gate, "'", "'\\''") + "'"
	script := "/bin/sh -c 'printf \"synthetic installer started\\n\"; read decision < \"$1\"; printf \"synthetic installer completed\\n\"' synthetic " + quotedGate + "\n"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(script))}, nil
	})}
	output := &installerOutput{started: make(chan struct{})}
	done := make(chan error, 1)
	finished := false
	go func() {
		done <- installUpdate(ctx, installerRelease(script), "before.age", client, output, func(context.Context, []string, io.Writer) error { return nil })
	}()
	defer func() {
		// Always release the synthetic child, even if an assertion fails.
		_, _ = io.WriteString(control, "continue\n")
		if !finished {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("synthetic installer did not finish during cleanup")
			}
		}
	}()
	select {
	case <-output.started:
	case err := <-done:
		finished = true
		t.Fatal("installer exited before handoff", err)
	case <-time.After(5 * time.Second):
		t.Fatal("synthetic installer did not start")
	}
	cancel()
	select {
	case err := <-done:
		finished = true
		t.Fatal("updater returned while its installer child was still running", err)
	case <-time.After(25 * time.Millisecond):
	}
	if _, err := io.WriteString(control, "continue\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		finished = true
		if err != nil || !strings.Contains(output.String(), "synthetic installer completed") || !strings.Contains(output.String(), "Vult 0.5.2 is installed.") {
			t.Fatalf("installer result was not awaited: %v; output: %s", err, output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("updater did not collect its installer child")
	}
}

func TestUpdateReportsInstallerFailureWithoutPromisingUnchangedBooks(t *testing.T) {
	const script = "printf 'synthetic installer failed\\n'; exit 23\n"
	client := &http.Client{Transport: updateTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(script))}, nil
	})}
	var output bytes.Buffer
	err := installUpdate(context.Background(), installerRelease(script), "before.age", client, &output, func(context.Context, []string, io.Writer) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "exit status 23") || !strings.Contains(err.Error(), "backup is at before.age") || strings.Contains(err.Error(), "Books are unchanged") {
		t.Fatalf("installer failure: %v", err)
	}
	if !strings.Contains(output.String(), "synthetic installer failed") || strings.Contains(output.String(), "is installed.") {
		t.Fatalf("failed installer output: %s", &output)
	}
}
