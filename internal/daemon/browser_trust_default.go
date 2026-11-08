//go:build !linux

package daemon

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
