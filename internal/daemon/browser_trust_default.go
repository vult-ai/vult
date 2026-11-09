//go:build !linux

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func ensureBrowserCertificateStored(ctx context.Context, c browserConfiguration) error {
	if !c.valid() {
		return errors.New("local browser certificate is unavailable")
	}
	certificate, _ := pem.Decode([]byte(c.Certificate))
	stored := func() bool {
		cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-certificate", "-a", "-c", c.Hostname, "-p", "/Library/Keychains/System.keychain")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		data, err := cmd.Output()
		if err != nil || len(data) > 32768 {
			return false
		}
		for len(data) > 0 {
			var block *pem.Block
			block, data = pem.Decode(data)
			if block == nil {
				return false
			}
			if block.Type == "CERTIFICATE" && bytes.Equal(block.Bytes, certificate.Bytes) {
				return true
			}
		}
		return false
	}
	if stored() {
		return nil
	}
	// The staging directory is root-controlled, including against the service UID.
	f, err := os.CreateTemp(filepath.Dir(browserTrustIdentity), ".browser-certificate-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(c.Certificate); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "add-certificates", "-k", "/Library/Keychains/System.keychain", f.Name())
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	// Importing a public certificate does not approve any trust settings.
	_ = cmd.Run()
	if !stored() {
		return errors.New("local browser certificate was not stored")
	}
	return nil
}

// forgetBrowserCertificate removes only the exact old public certificate.
// Failure keeps its durable cleanup record for the next installation attempt.
func forgetBrowserCertificate(certificate string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "/usr/bin/security", args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
		return cmd.CombinedOutput()
	}
	return removeBrowserCertificate(certificate, filepath.Dir(browserTrustIdentity), run)
}

func removeBrowserCertificate(certificate, directory string, run func(...string) ([]byte, error)) error {
	leaf, err := legacyBrowserCertificate(certificate)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(directory, ".browser-old-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.WriteString(certificate); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	const missing = "The specified item could not be found in the keychain."
	data, err := run("remove-trusted-cert", "-d", f.Name())
	if err != nil && strings.TrimSpace(string(data)) != "SecTrustSettingsRemoveTrustSettings: "+missing {
		return errors.New("old local browser trust could not be removed")
	}
	sum := sha256.Sum256(leaf.Raw)
	const keychain = "/Library/Keychains/System.keychain"
	if _, err := run("delete-certificate", "-Z", hex.EncodeToString(sum[:]), keychain); err == nil {
		return nil
	}
	// A previous attempt may have removed the entry before it recorded success.
	// A name lookup is read-only; deletion always uses the exact fingerprint.
	data, err = run("find-certificate", "-a", "-c", leaf.DNSNames[0], "-p", keychain)
	if err != nil {
		return errors.New("old local browser certificate could not be checked")
	}
	if len(data) > 32768 {
		return errors.New("old local browser certificate reply exceeds supported size")
	}
	for len(bytes.TrimSpace(data)) > 0 {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || bytes.Equal(block.Bytes, leaf.Raw) {
			return errors.New("old local browser certificate could not be removed")
		}
	}
	return nil
}

// RequestBrowserTrust launches the visible, unprivileged setup application.
// The operating system handles authorization for its exact-host SSL setting.
func RequestBrowserTrust(ctx context.Context) error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("local browser setup requires Apple Silicon macOS")
	}
	if err := installationCode(); err != nil {
		return err
	}
	for _, path := range []string{browserSetupApp, filepath.Join(browserSetupApp, "Contents"), filepath.Join(browserSetupApp, "Contents/MacOS")} {
		if err := protected(path, 0, true); err != nil {
			return err
		}
	}
	for _, path := range []string{browserTrustIdentity, filepath.Join(browserSetupApp, "Contents/Info.plist"), filepath.Join(browserSetupApp, "Contents/MacOS/browser-setup")} {
		if err := protected(path, 0, false); err != nil {
			return err
		}
	}
	cmd := exec.CommandContext(ctx, filepath.Join(browserSetupApp, "Contents/MacOS/browser-setup"))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	if err := cmd.Run(); err != nil {
		return errors.New("local browser setup did not complete")
	}
	return nil
}
