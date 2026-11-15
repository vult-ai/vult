package bankconnect

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/vult-ai/vult/internal/secretbox"
	"github.com/vult-ai/vult/ledger"
)

// Service keeps bank connections: linked logins and their activity. Owner calls take a ledger.TenantAdmin the caller already bound to
// the owner's signed-in session; the database checks that session again.
type Service struct {
	Store ledger.Store
	// Keys seals and opens access tokens. Its keyring lives outside the
	// database.
	Keys       *secretbox.Cached
	Connectors map[string]Connector
	// ProviderKeys supplies the keys this Vult uses with a provider, for
	// connectors that need keys of their own. Nil means connectors are
	// called with empty Credentials. An error wrapping ErrNotConfigured
	// means this Vult has no keys for that provider.
	ProviderKeys func(ctx context.Context, provider string) (Credentials, error)
	// WebhookURL is where providers send update notifications, when this
	// Vult can be reached from the internet. Empty means it syncs on a
	// schedule and whenever the app is opened.
	WebhookURL string
	// Every is how often each login is synced without a notification.
	Every time.Duration
	// Changed is told when a Book's bank activity changed, for live updates.
	Changed func(tenant, book string)
	Log     *slog.Logger
	// Operator ends provider access for logins a server operator holds,
	// after the Book disconnected or paused them.
	Operator OperatorAccess

	wake         chan struct{}
	institutions institutionCache
}

// OperatorAccess ends a server operator's provider access for a login after
// the Book recorded the decision. Each call is retried until it succeeds.
type OperatorAccess interface {
	Disconnect(ctx context.Context, tenant, book, connection string) error
	Pause(ctx context.Context, tenant, book, connection string) error
}

// DefaultEvery is the schedule for a Vult that receives no notifications.
const DefaultEvery = time.Hour

// tokenPurpose binds every sealed access token. It is part of each sealed
// value, so it never changes: tokens saved before would no longer open.
const tokenPurpose = "plaid.access_token"

// ErrNotConfigured means this Vult has no bank connection service for the
// provider asked for.
var ErrNotConfigured = errors.New("no bank connection service is set up")

// Reason is a provider's own words for a request it refused, when the error
// carries words the owner may see: an error with a method
// OwnerReason() string. It is empty otherwise.
func Reason(err error) string {
	var r interface{ OwnerReason() string }
	if errors.As(err, &r) {
		return r.OwnerReason()
	}
	return ""
}

func (s *Service) connector(provider string) (Connector, error) {
	c, ok := s.Connectors[provider]
	if !ok {
		return nil, ErrUnsupported
	}
	return c, nil
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) seal(ctx context.Context, b secretbox.Binding, value string) ([]byte, int, error) {
	ring, err := s.Keys.Keyring(ctx)
	if err != nil {
		return nil, 0, err
	}
	sealed, err := secretbox.Seal(ring, b, []byte(value))
	if err != nil {
		return nil, 0, err
	}
	return sealed, int(ring.Current()), nil
}

func (s *Service) open(ctx context.Context, b secretbox.Binding, sealed []byte) (string, error) {
	ring, err := s.Keys.Keyring(ctx)
	if err != nil {
		return "", err
	}
	plain, err := secretbox.Open(ring, b, sealed)
	if err != nil {
		return "", err
	}
	defer clear(plain)
	return string(plain), nil
}

func tokenSeal(tenant, book, connection string) secretbox.Binding {
	return secretbox.Binding{Purpose: tokenPurpose, TenantID: tenant, BookID: book, ConnectionID: connection}
}

// providerKeys are this Vult's keys for a provider, or empty Credentials
// when no one supplies keys.
func (s *Service) providerKeys(ctx context.Context, provider string) (Credentials, error) {
	if s.ProviderKeys == nil {
		return Credentials{}, nil
	}
	return s.ProviderKeys(ctx, provider)
}

// StartLink opens the provider's connection screen for the owner.
func (s *Service) StartLink(ctx context.Context, o ledger.TenantAdmin, tenant, book, actor, provider string) (LinkStart, error) {
	c, err := s.connector(provider)
	if err != nil {
		return LinkStart{}, err
	}
	// A sign-in, if one is due, comes before the bank's screen, not after it.
	if err := o.BankOwnerReady(ctx, book, actor); err != nil {
		return LinkStart{}, err
	}
	creds, err := s.providerKeys(ctx, provider)
	if err != nil {
		return LinkStart{}, err
	}
	return c.StartLink(ctx, creds, actor, s.WebhookURL)
}

// FinishLink exchanges the screen's result for lasting access, seals the
// token to the new login, records it and syncs it right away.
func (s *Service) FinishLink(ctx context.Context, o ledger.TenantAdmin, tenant, book, actor, provider, publicToken string) (string, error) {
	c, err := s.connector(provider)
	if err != nil || publicToken == "" || len(publicToken) > 512 {
		return "", ledger.ErrInvalid
	}
	// Checked before the one-time token goes to the provider, which accepts it
	// only once: asking for a sign-in afterwards would leave nothing to retry.
	if err := o.BankOwnerReady(ctx, book, actor); err != nil {
		return "", err
	}
	creds, err := s.providerKeys(ctx, provider)
	if err != nil {
		return "", err
	}
	linked, err := c.FinishLink(ctx, creds, publicToken)
	if err != nil {
		return "", err
	}
	id := ledger.ID()
	sealed, version, err := s.seal(ctx, tokenSeal(tenant, book, id), linked.AccessToken)
	if err != nil {
		return "", err
	}
	if _, err = o.AddBankConnection(ctx, book, actor, ledger.NewBankConnection{ID: id, Provider: provider, Environment: environment(creds),
		ProviderItemID: linked.ItemID, InstitutionID: linked.InstitutionID, InstitutionName: linked.InstitutionName,
		TokenSealed: sealed, KeyVersion: version}); err != nil {
		// Don't leave an access the Book doesn't know about.
		_ = c.Remove(context.WithoutCancel(ctx), creds, linked.AccessToken)
		return "", err
	}
	s.Wake()
	return id, nil
}

// Disconnect records the owner's disconnection, the same operation an agent
// prepares for approval. The sync timer then asks the provider to end the
// access and forgets the token.
func (s *Service) Disconnect(ctx context.Context, o ledger.TenantAdmin, tenant, book, actor, connection, reason string) error {
	if reason == "" {
		reason = "Disconnected in Vult."
	}
	input, _ := json.Marshal(ledger.DisconnectBank{RequestKey: "disconnect-" + connection, ConnectionID: connection, Reason: reason})
	if _, err := o.AccountChange(ctx, book, actor, "disconnect_bank", input); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// release ends provider access for logins the Book disconnected, or an
// operator's paused ones, and records it. A failure is tried again on the
// next tick.
func (s *Service) release(ctx context.Context) {
	due, err := s.Store.BankConnectionsToRelease(ctx, 20)
	if err != nil {
		return
	}
	for _, r := range due {
		var err error
		switch {
		case r.Access == "operator" && s.Operator == nil:
			continue
		case r.Access == "operator" && r.Status == "paused":
			err = s.Operator.Pause(ctx, r.TenantID, r.BookID, r.ConnectionID)
		case r.Access == "operator":
			err = s.Operator.Disconnect(ctx, r.TenantID, r.BookID, r.ConnectionID)
		default:
			err = s.removeOwnAccess(ctx, r.BankSyncTarget)
		}
		if err != nil {
			s.log().Info("a bank's access couldn't be ended yet", "error", errorKind(err))
			continue
		}
		if err := s.Store.BankAccessEnded(ctx, r.BankSyncTarget); err != nil {
			s.log().Warn("could not record that a bank's access ended")
		}
	}
}

// removeOwnAccess asks the provider to end a login whose token the Book
// keeps. When the keys or token can no longer be opened there is nothing
// Vult can end; the access is removed at the provider instead.
func (s *Service) removeOwnAccess(ctx context.Context, t ledger.BankSyncTarget) error {
	state, err := s.Store.BankSyncState(ctx, t)
	if err != nil {
		return err
	}
	if len(state.TokenSealed) == 0 {
		return nil
	}
	c, err := s.connector(state.Provider)
	if err != nil {
		return nil
	}
	var creds Credentials
	if _, direct := c.(TokenConnector); !direct {
		if creds, err = s.providerKeys(ctx, state.Provider); err != nil {
			return nil
		}
	}
	token, err := s.open(ctx, tokenSeal(t.TenantID, t.BookID, t.ConnectionID), state.TokenSealed)
	if err != nil {
		return nil
	}
	err = c.Remove(ctx, creds, token)
	if errors.Is(err, ErrCredentials) {
		// Already gone at the provider.
		return nil
	}
	return err
}

func (s *Service) changed(tenant, book string) {
	if s.Changed != nil {
		s.Changed(tenant, book)
	}
}

// Sync brings one login current from where its last sync stopped.
func (s *Service) Sync(ctx context.Context, target ledger.BankSyncTarget) (ledger.BankSyncResult, error) {
	state, err := s.Store.BankSyncState(ctx, target)
	if err != nil {
		return ledger.BankSyncResult{}, err
	}
	if state.Status == "disconnected" || state.Access == "operator" {
		// The server's operator syncs the logins it holds itself.
		return ledger.BankSyncResult{}, nil
	}
	c, err := s.connector(state.Provider)
	if err != nil {
		return ledger.BankSyncResult{}, s.problem(ctx, target, "This provider isn't available in this version of Vult.", false, err)
	}
	// A bank read with the owner's own token needs no provider keys.
	_, direct := c.(TokenConnector)
	var creds Credentials
	if !direct {
		creds, err = s.providerKeys(ctx, state.Provider)
		if errors.Is(err, ErrNotConfigured) {
			return ledger.BankSyncResult{}, s.problem(ctx, target, "This Vult has no bank connection service for this bank, so it can't sync.", true, err)
		}
		if err != nil {
			return ledger.BankSyncResult{}, s.problem(ctx, target, "The last sync didn't finish. Vult will try again.", false, err)
		}
	}
	token, err := s.open(ctx, tokenSeal(target.TenantID, target.BookID, target.ConnectionID), state.TokenSealed)
	if err != nil {
		return ledger.BankSyncResult{}, s.problem(ctx, target, "Vult can't open this bank's access. Reconnect the bank.", true, err)
	}
	changes, err := c.Changes(ctx, creds, token, state.Cursor)
	switch {
	case errors.Is(err, ErrReconnect) && direct:
		return ledger.BankSyncResult{}, s.problem(ctx, target, providerName(state.Provider)+" no longer accepts this token. Paste a new one to keep syncing.", true, err)
	case errors.Is(err, ErrReconnect):
		return ledger.BankSyncResult{}, s.problem(ctx, target, "The bank needs you to sign in again. Reconnect it to keep syncing.", true, err)
	case errors.Is(err, ErrCredentials) && direct:
		return ledger.BankSyncResult{}, s.problem(ctx, target, providerName(state.Provider)+" refused this token. Paste a new read-only token to keep syncing.", true, err)
	case errors.Is(err, ErrCredentials):
		return ledger.BankSyncResult{}, s.problem(ctx, target, "The bank connection service refused access to this bank. Reconnect it to keep syncing.", true, err)
	case err != nil:
		return ledger.BankSyncResult{}, s.problem(ctx, target, "The last sync didn't finish. Vult will try again.", false, err)
	}
	batch := ledger.BankSyncBatch{FromCursor: state.Cursor, NextCursor: changes.NextCursor, Removed: changes.Removed, Complete: true}
	if len(changes.Checks) > 0 && state.Cursor == "" {
		batch.FirstChecks = checksJSON(changes.Checks)
	}
	for _, a := range changes.Accounts {
		batch.Accounts = append(batch.Accounts, ledger.BankSyncAccount(a))
	}
	for _, t := range changes.Added {
		batch.Added = append(batch.Added, ledger.BankSyncTransaction(t))
	}
	for _, t := range changes.Modified {
		batch.Modified = append(batch.Modified, ledger.BankSyncTransaction(t))
	}
	for _, v := range changes.Replacements {
		batch.Takeovers = append(batch.Takeovers, ledger.BankSyncTakeover{OldProviderAccountID: v.OldProviderAccountID,
			NewProviderAccountID: v.NewProviderAccountID, Reason: v.Reason})
	}
	batch.Closed = changes.Closed
	out, err := s.Store.ApplyBankSync(ctx, target, batch)
	if errors.Is(err, ledger.ErrBankCursorMoved) {
		// Another sync got there first; its result stands.
		return out, nil
	}
	if err != nil {
		return out, s.problem(ctx, target, "The last sync didn't finish. Vult will try again.", false, err)
	}
	if out.Added+out.Modified+out.Removed > 0 || state.Cursor == "" || len(batch.Takeovers)+len(batch.Closed) > 0 {
		s.changed(target.TenantID, target.BookID)
	}
	// An account whose activity didn't add up to its balance recorded
	// nothing; the owner hears why, while the rest of the login syncs.
	for _, check := range changes.Checks {
		if !check.Passed && check.Problem != "" {
			_ = s.problem(ctx, target, check.Problem, true, nil)
		}
	}
	return out, nil
}

// providerName is how a provider is named to the owner.
func providerName(provider string) string {
	switch provider {
	case "mercury":
		return "Mercury"
	}
	return "The bank"
}

// checksJSON keeps balance checks as the owner's summary reads them.
func checksJSON(checks []BalanceCheck) json.RawMessage {
	type check struct {
		ProviderAccountID string `json:"provider_account_id"`
		Name              string `json:"name"`
		Transactions      int    `json:"transactions"`
		BalanceMinor      string `json:"balance_minor"`
		Currency          string `json:"currency"`
		Scale             int    `json:"scale"`
		Passed            bool   `json:"passed"`
		Problem           string `json:"problem,omitempty"`
	}
	out := make([]check, 0, len(checks))
	for _, c := range checks {
		out = append(out, check{c.ProviderAccountID, c.Name, c.Transactions, strconv.FormatInt(c.BalanceMinor, 10), c.Currency, c.Scale, c.Passed, c.Problem})
	}
	data, _ := json.Marshal(out)
	return data
}

// ConnectToken connects a bank the owner reads with their own read-only
// token, such as Mercury: the token is checked with the bank, sealed to the
// new login, and the login syncs right away. No provider keys are needed.
func (s *Service) ConnectToken(ctx context.Context, o ledger.TenantAdmin, tenant, book, actor, provider, token string) (string, error) {
	c, err := s.connector(provider)
	if err != nil {
		return "", err
	}
	direct, ok := c.(TokenConnector)
	token = strings.TrimSpace(token)
	if !ok || token == "" || len(token) > 4096 {
		return "", ledger.ErrInvalid
	}
	if err := o.BankOwnerReady(ctx, book, actor); err != nil {
		return "", err
	}
	linked, err := direct.ConnectToken(ctx, token)
	if err != nil {
		return "", err
	}
	id := ledger.ID()
	sealed, version, err := s.seal(ctx, tokenSeal(tenant, book, id), linked.AccessToken)
	if err != nil {
		return "", err
	}
	if _, err = o.AddBankConnection(ctx, book, actor, ledger.NewBankConnection{ID: id, Provider: provider, Environment: "production",
		ProviderItemID: linked.ItemID, InstitutionID: linked.InstitutionID, InstitutionName: linked.InstitutionName,
		TokenSealed: sealed, KeyVersion: version}); err != nil {
		return "", err
	}
	s.Wake()
	return id, nil
}

func (s *Service) problem(ctx context.Context, t ledger.BankSyncTarget, reason string, attention bool, cause error) error {
	if err := s.Store.RecordBankSyncProblem(context.WithoutCancel(ctx), t, reason, attention); err != nil {
		s.log().Warn("could not record a bank sync problem")
	}
	s.changed(t.TenantID, t.BookID)
	return cause
}

// Wake asks the sync loop to look for due logins now.
func (s *Service) Wake() {
	if s.wake == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run syncs due logins until ctx ends: those a notification or the open app
// asked for, then each one on the schedule.
func (s *Service) Run(ctx context.Context) {
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	every := s.Every
	if every <= 0 {
		every = DefaultEvery
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		s.release(ctx)
		due, err := s.Store.BankConnectionsDue(ctx, every, 20)
		if err == nil {
			for _, t := range due {
				if ctx.Err() != nil {
					return
				}
				one, cancel := context.WithTimeout(ctx, 5*time.Minute)
				if _, err := s.Sync(one, t); err != nil {
					s.log().Info("bank sync did not finish", "error", errorKind(err))
				}
				cancel()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.wake:
		}
	}
}

// errorKind logs what went wrong without provider text or identifiers.
func errorKind(err error) string {
	for _, known := range []error{ErrReconnect, ErrCredentials, ErrProvider, ErrUnsupported, ErrNotConfigured} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	return "unexpected"
}

// environment is the provider environment a new login is recorded in.
func environment(c Credentials) string {
	if c.Environment == "" {
		return "production"
	}
	return c.Environment
}
