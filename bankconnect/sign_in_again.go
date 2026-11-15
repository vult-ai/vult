package bankconnect

import (
	"context"
	"errors"

	"github.com/vult-ai/vult/internal/secretbox"
	"github.com/vult-ai/vult/ledger"
)

// StartSignIn opens the provider's screen for a login of this Book whose bank
// needs the owner to sign in again. Afterwards a sync brings it back.
func (s *Service) StartSignIn(ctx context.Context, o ledger.TenantAdmin, tenant, book, actor, connection string) (LinkStart, error) {
	target := ledger.BankSyncTarget{TenantID: tenant, BookID: book, ConnectionID: connection}
	state, err := s.Store.BankSyncState(ctx, target)
	if err != nil {
		return LinkStart{}, err
	}
	if state.Status == "disconnected" || state.Access == "operator" {
		return LinkStart{}, ledger.ErrInvalid
	}
	c, err := s.connector(state.Provider)
	if err != nil {
		return LinkStart{}, err
	}
	again, ok := c.(SignInAgain)
	if !ok {
		return LinkStart{}, ErrUnsupported
	}
	creds, err := s.providerKeys(ctx, state.Provider)
	if err != nil {
		return LinkStart{}, err
	}
	token, err := s.open(ctx, tokenSeal(tenant, book, connection), state.TokenSealed)
	if errors.Is(err, secretbox.ErrSealed) {
		// Access kept under another key, as after Books are copied from
		// another Vult, can't be used to sign in again: only a new login can.
		return LinkStart{}, &ledger.RefusedError{Reason: "Vult can't open this bank's saved access, so it can't sign in again. Disconnect it, then connect it again."}
	}
	if err != nil {
		return LinkStart{}, err
	}
	return again.StartSignIn(ctx, creds, actor, s.WebhookURL, token)
}

// FinishSignIn records that the owner signed in again: the login keeps its
// access, and a sync now brings it current.
func (s *Service) FinishSignIn(ctx context.Context, o ledger.TenantAdmin, tenant, book, connection string) error {
	state, err := s.Store.BankSyncState(ctx, ledger.BankSyncTarget{TenantID: tenant, BookID: book, ConnectionID: connection})
	if err != nil {
		return err
	}
	if state.Status == "disconnected" || state.Access == "operator" {
		return ledger.ErrInvalid
	}
	if err := o.RequestBankSync(ctx, book); err != nil {
		return err
	}
	s.Wake()
	return nil
}
