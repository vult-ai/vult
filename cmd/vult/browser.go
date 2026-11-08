package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/daemon"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/loopback"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
	"golang.org/x/term"
)

func localBrowserAddress(raw string, setup bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !loopback.ValidHostname(u.Hostname()) || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Opaque != "" || u.RawPath != "" {
		return nil, ledger.ErrInvalid
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1024 || port > 65535 || strconv.Itoa(port) != u.Port() {
		return nil, ledger.ErrInvalid
	}
	if setup {
		fragment, err := url.ParseQuery(u.Fragment)
		if err != nil || u.Path != "/auth/setup" || len(fragment) != 1 || len(fragment["setup"]) != 1 || len(fragment.Get("setup")) != 43 {
			return nil, ledger.ErrInvalid
		}
		for _, r := range fragment.Get("setup") {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return nil, ledger.ErrInvalid
			}
		}
	} else if u.Path != "" || u.Fragment != "" {
		return nil, ledger.ErrInvalid
	}
	return u, nil
}

func waitLocalBrowser(ctx context.Context, origin *url.URL) error {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	config, err := localBrowserTLSConfig(origin.Hostname())
	if err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: config, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", origin.Port()))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected local redirect") }}
	for {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String()+"/healthz", nil)
		if err != nil {
			return ledger.ErrInvalid
		}
		response, err := client.Do(r)
		var certificateError *tls.CertificateVerificationError
		if errors.As(err, &certificateError) {
			return errors.New("local HTTPS certificate approval did not complete; run vult open again")
		}
		if err == nil {
			io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
			response.Body.Close()
			if response.StatusCode == http.StatusNoContent {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("local browser is not ready or its hostname certificate is not trusted; reinstall or inspect the protected service")
		case <-time.After(time.Second):
		}
	}
}

func checkLocalBrowser(ctx context.Context) error {
	client, err := local.NewClient()
	if err != nil {
		return err
	}
	data, err := client.Call(ctx, "browser_status", "", struct{}{})
	if err != nil {
		return err
	}
	var status local.BrowserStatus
	if strictjson.Decode(data, &status) != nil {
		return ledger.ErrInvalid
	}
	origin, err := localBrowserAddress(status.Origin, false)
	if err != nil {
		return err
	}
	return waitLocalBrowser(ctx, origin)
}

func runBrowser(ctx context.Context, args []string, out io.Writer) error {
	reset := len(args) == 2 && args[0] == "browser" && args[1] == "reset"
	setup := len(args) >= 1 && args[0] == "setup"
	if !reset && !setup && !(len(args) == 1 && args[0] == "open") {
		return ledger.ErrInvalid
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return errors.New("browser setup requires a supported installation")
	}
	// A reset replaces the owner's sign-in, so it stays with the owner's own
	// terminal. Opening and setup can be started by an agent: secrets are shown
	// only in the browser, and the first setup still needs the owner's approval.
	if reset && (!term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd()))) {
		return errors.New("browser reset requires your interactive owner terminal")
	}
	c, err := local.NewClient()
	if err != nil {
		return err
	}
	if setup {
		draft, ok, err := setupDraft(args[1:])
		if err != nil {
			return err
		}
		if ok {
			if err = c.DraftBook(ctx, draft); err != nil {
				return err
			}
		}
	}
	data, err := c.Call(ctx, "browser_status", "", struct{}{})
	if err != nil {
		return err
	}
	var status local.BrowserStatus
	if strictjson.Decode(data, &status) != nil {
		return ledger.ErrInvalid
	}
	origin, err := localBrowserAddress(status.Origin, false)
	if err != nil {
		return err
	}
	if !status.Ready && !reset {
		fmt.Fprintln(out, "macOS asks the owner to approve twice: first to trust Vult's local address, so the browser opens it securely, then to finish setup.")
	}
	if err := daemon.RequestBrowserTrust(ctx); err != nil {
		return err
	}
	if err := waitLocalBrowser(ctx, origin); err != nil {
		return err
	}
	destination := origin.String()
	if reset || !status.Ready {
		method := "browser_setup"
		if reset {
			method = "browser_reset"
		}
		data, err = c.OwnerCall(ctx, method, struct{}{})
		if err != nil {
			return err
		}
		var setup local.BrowserSetup
		if strictjson.Decode(data, &setup) != nil || setup.Origin != status.Origin {
			return ledger.ErrInvalid
		}
		u, err := localBrowserAddress(setup.SetupURL, true)
		if err != nil || u.Host != origin.Host {
			return ledger.ErrInvalid
		}
		destination = u.String()
	}
	if err = openLocalBrowser(ctx, destination); err != nil {
		return errors.New("the local browser did not open; run vult open again")
	}
	if !status.Ready || reset {
		fmt.Fprintln(out, "Vult opened its setup page in the owner's browser at", origin.String()+".")
		fmt.Fprintln(out, "Next: the owner creates a passkey there, saves the recovery codes it shows, creates the first Book and may upload a statement.")
		fmt.Fprintln(out, "The codes appear only in the browser. Never ask the owner for them.")
		return nil
	}
	fmt.Fprintln(out, "Opened", origin.String())
	return nil
}

// setupDraft reads an agent's proposal for the owner's first Book. It creates
// nothing; the owner confirms it in the browser.
func setupDraft(args []string) (local.BookDraft, bool, error) {
	f := flags("setup")
	name := f.String("book-name", "", "proposed Book name")
	profile := f.String("profile", "", "personal or business")
	currency := f.String("currency", "", "ISO 4217 currency code, such as USD")
	timezone := f.String("timezone", "", "IANA timezone, such as Europe/Berlin")
	if parse(f, args) != nil {
		return local.BookDraft{}, false, errors.New("invalid arguments; run vult help")
	}
	if *name == "" && *profile == "" && *currency == "" && *timezone == "" {
		return local.BookDraft{}, false, nil
	}
	draft := local.BookDraft{Name: *name, Profile: *profile, Currency: strings.ToUpper(*currency), Timezone: *timezone}
	if !draft.Valid() {
		return local.BookDraft{}, false, errors.New("a Book draft needs --book-name and a three-letter --currency; --profile is personal or business")
	}
	return draft, true, nil
}
