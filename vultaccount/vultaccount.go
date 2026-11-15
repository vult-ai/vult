// Package vultaccount connects a Vult a person runs themselves to their
// Vult account, so Vult's own agent can work in it.
//
// The person signs in on the account service's own page and approves this
// Vult there; the Vult never sees their sign-in. It receives a credential
// for itself alone, keeps it sealed, and presents short-lived tokens made
// from it. The exchange is OAuth 2.0's authorization code flow with PKCE,
// as for any app on a person's own computer.
//
// Nothing here reaches a Book. A connection says who the person is to
// Vult's agent service; what the agent may do in a Book is still decided
// by that Book's own grants.
package vultaccount

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ClientID is how every Vult a person runs names itself to the account
// service. It is public: what proves a Vult is the one that began a
// sign-in is the code verifier, which never leaves it.
const ClientID = "vult-installed"

// Config is where the account service is and where it sends the person
// back to.
type Config struct {
	// Issuer is the account service's address, over HTTPS.
	Issuer string
	// Redirect is this Vult's own address the account service returns the
	// person to, with the one-time code.
	Redirect string
	// Name is what the person sees this Vult called on the account page,
	// such as the computer's name.
	Name string
	// HTTP makes the calls; nil uses a client with a short timeout.
	HTTP *http.Client
}

// Store keeps the connection between starts. Whoever provides it seals what
// it is given; nothing here is written in the clear.
type Store interface {
	Load(ctx context.Context) ([]byte, error)
	Save(ctx context.Context, sealed []byte) error
	Clear(ctx context.Context) error
}

// ErrNoConnection means this Vult isn't connected to a Vult account.
var ErrNoConnection = errors.New("vultaccount: not connected to a Vult account")

// ErrRefused means the account service wouldn't complete the sign-in or no
// longer accepts this Vult's credential.
var ErrRefused = errors.New("vultaccount: the account service refused")

// ErrUnavailable means the account service couldn't be reached or answered
// with something that isn't an answer.
var ErrUnavailable = errors.New("vultaccount: the account service can't be reached")

// Connection is what a connected Vult knows about its account.
type Connection struct {
	// Account is the account's identifier at the account service.
	Account string `json:"account_id"`
	// Label is what the person recognises the account by, such as an
	// email address.
	Label string `json:"account_label"`
	// AgentService is where Vult's agent is reached for this account.
	AgentService string    `json:"agent_service"`
	ConnectedAt  time.Time `json:"connected_at"`
}

// kept is what the Store holds.
type kept struct {
	Connection
	Credential string `json:"credential"`
}

type pending struct {
	verifier string
	started  time.Time
}

const (
	pendingFor  = 10 * time.Minute
	maxPending  = 8
	maxAnswer   = 64 << 10
	earlyExpiry = 30 * time.Second
)

// Client is one Vult's connection to the account service.
type Client struct {
	config Config
	store  Store
	now    func() time.Time

	mu sync.Mutex
	// began are sign-ins started here and not yet finished, by state.
	began map[string]pending
	// token is the short-lived token in use, until expires.
	token   string
	expires time.Time
}

// Open prepares a Vult's connection. The issuer is HTTPS, or this computer
// for development; the redirect is this Vult's own address.
func Open(c Config, store Store) (*Client, error) {
	if store == nil || !safeAddress(c.Issuer) || !safeRedirect(c.Redirect) {
		return nil, errors.New("vultaccount: an HTTPS account service, this Vult's own return address and a store are required")
	}
	c.Issuer = strings.TrimRight(c.Issuer, "/")
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if len(c.Name) > 80 {
		c.Name = c.Name[:80]
	}
	return &Client{config: c, store: store, now: time.Now, began: map[string]pending{}}, nil
}

// safeAddress accepts an HTTPS address with no credentials, query or
// fragment, or plain HTTP on this computer.
func safeAddress(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(raw) > 512 {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())
}

func safeRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && len(raw) <= 512
}

func random() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Begin starts a sign-in and returns the account service page to send the
// person to. Each sign-in can be finished once, within ten minutes.
func (c *Client) Begin() (string, error) {
	state, err := random()
	if err != nil {
		return "", err
	}
	verifier, err := random()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	now := c.now()
	for s, p := range c.began {
		if now.Sub(p.started) > pendingFor {
			delete(c.began, s)
		}
	}
	// A page that starts sign-ins over and over keeps only the newest few.
	for len(c.began) >= maxPending {
		oldest := ""
		for s, p := range c.began {
			if oldest == "" || p.started.Before(c.began[oldest].started) {
				oldest = s
			}
		}
		delete(c.began, oldest)
	}
	c.began[state] = pending{verifier: verifier, started: now}
	c.mu.Unlock()
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {ClientID},
		"redirect_uri":          {c.config.Redirect},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	if c.config.Name != "" {
		q.Set("vult_name", c.config.Name)
	}
	return c.config.Issuer + "/connect/authorize?" + q.Encode(), nil
}

// answer is what the account service's token address returns.
type answer struct {
	Credential   string `json:"refresh_token"`
	Token        string `json:"access_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Account      string `json:"account_id"`
	Label        string `json:"account_label"`
	AgentService string `json:"agent_service"`
}

func (c *Client) ask(ctx context.Context, path string, form url.Values) (answer, error) {
	var out answer
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.Issuer+path, strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "application/json")
	w, err := c.config.HTTP.Do(r)
	if err != nil {
		return out, ErrUnavailable
	}
	defer w.Body.Close()
	body, err := io.ReadAll(io.LimitReader(w.Body, maxAnswer+1))
	if err != nil || len(body) > maxAnswer {
		return out, ErrUnavailable
	}
	// The account service says no with 400 or 401; anything else that
	// isn't a plain yes is it not working.
	switch {
	case w.StatusCode == http.StatusBadRequest || w.StatusCode == http.StatusUnauthorized:
		return out, ErrRefused
	case w.StatusCode != http.StatusOK:
		return out, ErrUnavailable
	}
	if json.Unmarshal(body, &out) != nil {
		return out, ErrUnavailable
	}
	return out, nil
}

// Finish completes a sign-in the account service sent back, with the state
// and one-time code from its return. It keeps the credential and reports
// the account this Vult is now connected to.
func (c *Client) Finish(ctx context.Context, state, code string) (Connection, error) {
	c.mu.Lock()
	var found pending
	ok := false
	for s, p := range c.began {
		if subtle.ConstantTimeCompare([]byte(s), []byte(state)) == 1 {
			found, ok = p, true
			// A state is used once, whatever comes of it.
			delete(c.began, s)
		}
	}
	now := c.now()
	c.mu.Unlock()
	if !ok || code == "" || len(code) > 2048 || now.Sub(found.started) > pendingFor {
		return Connection{}, ErrRefused
	}
	got, err := c.ask(ctx, "/connect/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"code_verifier": {found.verifier},
		"redirect_uri":  {c.config.Redirect},
	})
	if err != nil {
		return Connection{}, err
	}
	if got.Credential == "" || got.Account == "" || len(got.Account) > 200 || len(got.Label) > 200 || !safeAddress(got.AgentService) {
		return Connection{}, ErrUnavailable
	}
	k := kept{Connection: Connection{Account: got.Account, Label: got.Label, AgentService: strings.TrimRight(got.AgentService, "/"), ConnectedAt: now.UTC()}, Credential: got.Credential}
	raw, err := json.Marshal(k)
	if err != nil {
		return Connection{}, err
	}
	if err := c.store.Save(ctx, raw); err != nil {
		return Connection{}, err
	}
	c.mu.Lock()
	c.token, c.expires = got.Token, c.until(now, got)
	c.mu.Unlock()
	return k.Connection, nil
}

func (c *Client) until(now time.Time, got answer) time.Time {
	if got.Token == "" || got.ExpiresIn <= 0 {
		return time.Time{}
	}
	return now.Add(time.Duration(got.ExpiresIn)*time.Second - earlyExpiry)
}

func (c *Client) load(ctx context.Context) (kept, error) {
	raw, err := c.store.Load(ctx)
	if err != nil {
		return kept{}, err
	}
	var k kept
	if len(raw) == 0 || json.Unmarshal(raw, &k) != nil || k.Credential == "" || k.Account == "" {
		return kept{}, ErrNoConnection
	}
	return k, nil
}

// Status reports the account this Vult is connected to, or ErrNoConnection.
// It asks the account service nothing.
func (c *Client) Status(ctx context.Context) (Connection, error) {
	k, err := c.load(ctx)
	return k.Connection, err
}

// Token returns a short-lived token to present to Vult's agent service,
// with the connection it speaks for. When the account service no longer
// accepts this Vult, the connection is forgotten and ErrNoConnection
// returned: the person signs in again.
func (c *Client) Token(ctx context.Context) (string, Connection, error) {
	k, err := c.load(ctx)
	if err != nil {
		return "", Connection{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.expires) {
		return c.token, k.Connection, nil
	}
	got, err := c.ask(ctx, "/connect/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {ClientID}, "refresh_token": {k.Credential}})
	if errors.Is(err, ErrRefused) {
		c.token = ""
		_ = c.store.Clear(ctx)
		return "", Connection{}, ErrNoConnection
	}
	if err != nil {
		return "", Connection{}, err
	}
	if got.Token == "" || got.ExpiresIn <= 0 {
		return "", Connection{}, ErrUnavailable
	}
	// The account service may replace the credential each time it is used.
	if got.Credential != "" && got.Credential != k.Credential {
		k.Credential = got.Credential
		raw, err := json.Marshal(k)
		if err != nil {
			return "", Connection{}, err
		}
		if err := c.store.Save(ctx, raw); err != nil {
			return "", Connection{}, err
		}
	}
	c.token, c.expires = got.Token, c.until(c.now(), got)
	return c.token, k.Connection, nil
}

// Disconnect forgets the connection here and tells the account service to
// stop accepting this Vult's credential. Forgetting never waits on the
// account service: a Vult that can't reach it is still disconnected.
func (c *Client) Disconnect(ctx context.Context) error {
	k, err := c.load(ctx)
	if errors.Is(err, ErrNoConnection) {
		return nil
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.token, c.expires = "", time.Time{}
	c.mu.Unlock()
	if err := c.store.Clear(ctx); err != nil {
		return err
	}
	_, _ = c.ask(ctx, "/connect/revoke", url.Values{"client_id": {ClientID}, "token": {k.Credential}})
	return nil
}
