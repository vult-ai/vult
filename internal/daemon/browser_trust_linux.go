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
	"strings"
	"syscall"
	"time"

	"github.com/vult-ai/vult/internal/fssecurity"
	"github.com/vult-ai/vult/internal/local"
)

func ensureBrowserCertificateStored(_ context.Context, c browserConfiguration) error {
	if !c.valid() {
		return errors.New("local browser certificate is unavailable")
	}
	// The installer publishes the public identity record, not user trust.
	// Chrome approval belongs to the interactive desktop account.
	return nil
}

func nssDatabase(home string, create bool) (string, bool, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || home == "/" {
		return "", false, errors.New("desktop home is unavailable")
	}
	directory := filepath.Join(home, ".local/share/pki/nssdb")
	legacy := filepath.Join(home, ".pki/nssdb")
	if _, err := os.Lstat(legacy); err == nil {
		directory = legacy
	} else if !os.IsNotExist(err) {
		return "", false, errors.New("Chrome certificate database is unavailable")
	}
	uid := uint32(os.Geteuid())
	var parents []string
	// Home and its system-owned ancestors were checked with the desktop identity.
	// Only the selected profile below that trusted home is traversed here.
	for parent := directory; ; parent = filepath.Dir(parent) {
		parents = append(parents, parent)
		if parent == home {
			break
		}
	}
	missing := false
	for i := len(parents) - 1; i >= 0; i-- {
		path := parents[i]
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			missing = true
			if !create {
				continue
			}
			if err := os.Mkdir(path, 0700); err != nil {
				return "", false, errors.New("Chrome certificate directory could not be created")
			}
			info, err = os.Lstat(path)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return "", false, errors.New("Chrome certificate directory is unsafe")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (stat.Uid != 0 && stat.Uid != uid) {
			return "", false, errors.New("Chrome certificate directory has an unexpected owner")
		}
		file, err := os.Open(path)
		if err != nil {
			return "", false, errors.New("Chrome certificate directory is unavailable")
		}
		valid := fssecurity.NoExtendedACL(file)
		file.Close()
		if !valid {
			return "", false, errors.New("Chrome certificate access controls are unsupported")
		}
	}
	if missing && !create {
		return directory, false, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", false, errors.New("Chrome certificate database is unavailable")
	}
	if len(entries) == 0 {
		return directory, false, nil
	}
	for _, name := range []string{"cert9.db", "key4.db"} {
		path := filepath.Join(directory, name)
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return "", false, errors.New("existing Chrome certificate database needs recovery")
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return "", false, errors.New("existing Chrome certificate database is unavailable")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		valid := err == nil && ok && info.Mode().IsRegular() && stat.Uid == uid && stat.Nlink == 1 && info.Mode().Perm()&0022 == 0 && fssecurity.NoExtendedACL(file)
		if name == "key4.db" && info.Mode().Perm()&0077 != 0 {
			valid = false
		}
		file.Close()
		if !valid {
			return "", false, errors.New("existing Chrome certificate database is unsafe")
		}
	}
	return directory, true, nil
}

func nssPeerTrust(data []byte) bool {
	const heading = "Certificate Trust Flags:"
	if strings.Count(string(data), heading) != 1 {
		return false
	}
	index := strings.LastIndex(string(data), heading)
	if index < 0 {
		return false
	}
	var lines []string
	for _, line := range strings.Split(string(data[index:]), "\n") {
		if text := strings.TrimSpace(line); text != "" {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n") == "Certificate Trust Flags:\nSSL Flags:\nTerminal Record\nTrusted\nEmail Flags:\nObject Signing Flags:"
}

func RequestBrowserTrust(ctx context.Context) error {
	if !nativeSupported() {
		return errors.New("local browser requires a supported native desktop")
	}
	if err := installationCode(); err != nil {
		return err
	}
	session, err := local.CurrentDesktopSession()
	if err != nil {
		return err
	}
	leaf, err := installedBrowserCertificate()
	if err != nil {
		return err
	}
	for _, path := range []string{"/usr", "/usr/bin", "/usr/share", "/usr/share/applications"} {
		if err := protected(path, 0, true); err != nil {
			return err
		}
	}
	for _, path := range []string{"/usr/bin/certutil", "/usr/bin/zenity", "/usr/bin/gio", "/usr/share/applications/google-chrome.desktop"} {
		if err := protected(path, 0, false); err != nil {
			return errors.New("browser setup requires the system certificate and desktop dialog tools")
		}
	}
	directory, exists, err := nssDatabase(session.Home, false)
	if err != nil {
		return err
	}
	run := func(input []byte, args ...string) ([]byte, error) {
		commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		command := exec.CommandContext(commandCtx, "/usr/bin/certutil", args...)
		command.Env = session.Environment
		if input != nil {
			command.Stdin = bytes.NewReader(input)
		}
		output, err := command.Output()
		if len(output) > 65536 {
			return nil, errors.New("certificate reply exceeds supported size")
		}
		return output, err
	}
	known := false
	check := func() (bool, error) {
		data, err := run(nil, "-L", "-d", "sql:"+directory, "-n", leaf.DNSNames[0], "-a")
		if err != nil {
			return false, nil
		}
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 || !bytes.Equal(block.Bytes, leaf.Raw) {
			return false, errors.New("Chrome has a different certificate for this installation")
		}
		known = true
		data, err = run(nil, "-L", "-d", "sql:"+directory, "-n", leaf.DNSNames[0])
		return err == nil && nssPeerTrust(data), nil
	}
	if exists {
		if _, err := run(nil, "-L", "-d", "sql:"+directory); err != nil {
			return errors.New("existing Chrome certificate database could not be read; nothing was replaced")
		}
		if trusted, err := check(); err != nil || trusted {
			return err
		}
	}
	fingerprint := sha256.Sum256(leaf.Raw)
	text := "Trust Vult's local HTTPS address in Chrome?\n\n" + leaf.DNSNames[0] + "\n\nSHA-256: " + hex.EncodeToString(fingerprint[:]) + "\n\nThis approves only this self-signed server certificate, not a certificate authority. Other websites are unchanged."
	promptCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	prompt := exec.CommandContext(promptCtx, "/usr/bin/zenity", "--text-info", "--filename=/dev/stdin", "--title=Vult HTTPS", "--ok-label=Trust Vult", "--cancel-label=Cancel", "--width=640", "--height=320")
	prompt.Env, prompt.Stdin = session.Environment, strings.NewReader(text)
	if err := prompt.Run(); err != nil {
		return errors.New("local certificate approval was canceled or did not complete")
	}
	if !exists {
		var nowExists bool
		directory, nowExists, err = nssDatabase(session.Home, true)
		if err != nil || nowExists {
			return errors.New("Chrome certificate database changed; run vult open again")
		}
		if _, err := run(nil, "-N", "--empty-password", "-d", "sql:"+directory); err != nil {
			return errors.New("new Chrome certificate database could not be prepared")
		}
	}
	if known {
		_, err = run(nil, "-M", "-d", "sql:"+directory, "-n", leaf.DNSNames[0], "-t", "P,,")
	} else {
		certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
		_, err = run(certificate, "-A", "-d", "sql:"+directory, "-n", leaf.DNSNames[0], "-t", "P,,", "-i", "/dev/stdin")
	}
	if err != nil {
		return errors.New("Chrome did not accept the local certificate; no setup code was issued")
	}
	trusted, err := check()
	if err != nil || !trusted {
		return errors.New("Chrome local certificate trust could not be confirmed")
	}
	return nil
}
