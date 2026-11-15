// Package bankconnect connects a Book to the owner's bank through a bank
// connection provider, and keeps its activity current.
//
// Every provider sits behind one Connector interface, so another can be added
// without changing the inbox, the sync or the screens. A connector only
// reports what the bank says. It never records anything in the Book: synced
// activity lands in the bank activity inbox, and a person or an agent records
// it.
package bankconnect

import (
	"context"
	"errors"
)

// Credentials are the keys a Vult uses with one provider. They are held
// only for the call that needs them.
type Credentials struct {
	Environment string
	ClientID    string
	Secret      string
}

// LinkStart is what the browser needs to open the provider's own connection
// screen. It is short-lived and carries no lasting access.
type LinkStart struct {
	Provider string `json:"provider"`
	Token    string `json:"token"`
	// Expires is when Token stops working, as the provider reported it.
	Expires string `json:"expires,omitempty"`
}

// Linked is a login the provider confirmed, with its lasting access token.
type Linked struct {
	AccessToken     string
	ItemID          string
	InstitutionID   string
	InstitutionName string
}

// Account is one account the provider reports for a login.
type Account struct {
	ProviderAccountID string
	Name              string
	Mask              string
	Type              string
	Subtype           string
	// Kind is cash for money the owner has and card for money the owner
	// owes. Accounts that are neither, such as investments, are left out.
	Kind     string
	Currency string
	Scale    int
}

// Transaction is one transaction as the provider reports it, with its amount
// signed the way the account's balance moves.
type Transaction struct {
	ProviderAccountID     string
	ProviderTransactionID string
	Date                  string
	AuthorizedDate        string
	AmountMinor           int64
	Currency              string
	Scale                 int
	Description           string
	Counterparty          string
	Pending               bool
	PendingProviderID     string
}

// Changes are everything a provider reports since a cursor.
type Changes struct {
	Accounts   []Account
	Added      []Transaction
	Modified   []Transaction
	Removed    []string
	NextCursor string
	// Checks are the connector's own checks that each account's activity
	// adds up to the balance the bank reports, for connectors that make
	// them. An account whose check failed is listed in Accounts but none
	// of its activity is in Added, Modified or Removed.
	Checks []BalanceCheck
	// Replacements are accounts the bank opened to take over others, as
	// when a bank moves its customers' accounts to a new partner bank. Each
	// is a proposal for the owner to confirm, not something done for them.
	Replacements []Replacement
	// Closed are accounts the bank closed or archived. This sync reported
	// their last activity; later syncs leave them out. A closed account is
	// not a problem for the owner.
	Closed []string
}

// Replacement proposes that a new account continues an old one: the owner
// confirms once, and the new account's activity records to the same Book
// account as the old one's, so the money moving across nets to zero.
type Replacement struct {
	OldProviderAccountID string
	NewProviderAccountID string
	// Reason says, for the owner, why the bank's new account looks like the
	// old one's replacement.
	Reason string
}

// BalanceCheck says whether one account's activity added up to the balance
// the bank reports.
type BalanceCheck struct {
	ProviderAccountID string
	Name              string
	// Transactions is how many of the account's transactions this sync
	// reported.
	Transactions int
	// BalanceMinor is the balance the bank reports, signed like the
	// account's Kind: money held for cash, money owed for a card.
	BalanceMinor int64
	Currency     string
	Scale        int
	Passed       bool
	// Problem says, for the owner, why nothing was recorded for the
	// account when the check failed.
	Problem string
}

// Connector is one bank data provider.
type Connector interface {
	Provider() string
	// StartLink prepares the provider's connection screen for one owner.
	// webhook is where the provider should send update notifications, or
	// empty when this Vult can't be reached from the internet.
	StartLink(ctx context.Context, c Credentials, user, webhook string) (LinkStart, error)
	// FinishLink exchanges the screen's one-time result for lasting access.
	FinishLink(ctx context.Context, c Credentials, publicToken string) (Linked, error)
	// Changes reads everything since cursor (empty for the first sync).
	Changes(ctx context.Context, c Credentials, accessToken, cursor string) (Changes, error)
	// Remove asks the provider to end access for a login.
	Remove(ctx context.Context, c Credentials, accessToken string) error
}

var (
	// ErrReconnect means the bank needs the owner to sign in again through
	// the provider's screen; syncing stops until they do.
	ErrReconnect = errors.New("the bank needs you to reconnect")
	// ErrProvider is a provider failure worth retrying later.
	ErrProvider = errors.New("the bank connection provider is unavailable")
	// ErrCredentials means the provider refused the keys or the access.
	ErrCredentials = errors.New("the provider refused these keys")
	// ErrUnsupported is a request the provider can't serve, such as an
	// unknown currency.
	ErrUnsupported = errors.New("unsupported by this provider")
)

// SignInAgain is a connector that can reopen its screen for an existing
// login whose bank needs the owner to sign in again.
type SignInAgain interface {
	StartSignIn(ctx context.Context, c Credentials, user, webhook, accessToken string) (LinkStart, error)
}

// TokenConnector is a connector the owner starts by pasting an access token
// they created at their bank, with no provider screen and no provider keys.
// Its Credentials are always empty; the pasted token is the access token
// that Changes and Remove receive. StartLink and FinishLink return
// ErrUnsupported.
type TokenConnector interface {
	Connector
	// ConnectToken checks the pasted token with the bank and returns the
	// lasting access it gives. A token the bank refuses is ErrCredentials.
	ConnectToken(ctx context.Context, token string) (Linked, error)
}
