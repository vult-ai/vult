package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Owner changes to bank settings, logins and account links. Each needs a
// fresh owner sign-in, which the database checks again. Secrets arrive here
// already sealed by the caller; this package never sees them in the clear.

// BankProviderSettingsChange stores the owner's own provider keys.
type BankProviderSettingsChange struct {
	Provider     string
	Environment  string
	ClientID     string
	SecretSealed []byte
	KeyVersion   int
	WebhookURL   string
	// ExpectedRevision is 0 for the first settings.
	ExpectedRevision int64
}

// BankProviderSettings is what the owner sees of stored settings. The secret
// is never returned, sealed or not.
type BankProviderSettings struct {
	Provider    string    `json:"provider"`
	Environment string    `json:"environment"`
	ClientID    string    `json:"client_id"`
	WebhookURL  string    `json:"webhook_url"`
	Revision    int64     `json:"revision"`
	UpdatedAt   time.Time `json:"updated_at"`
	// VultWide is set when these are the Vult's keys, used by every Book in
	// it, rather than keys saved for one Book before.
	VultWide bool `json:"vult_wide"`
}

type NewBankConnection struct {
	// ID is the new login's ID, chosen by the caller when it seals the token
	// to the login before recording it. Empty means a new ID.
	ID              string
	Provider        string
	Environment     string
	ProviderItemID  string
	InstitutionID   string
	InstitutionName string
	TokenSealed     []byte
	KeyVersion      int
	// OperatorHeld records a login whose access the server's operator holds
	// and syncs with its own provider account. It stores no token here.
	OperatorHeld bool
}

type BankConnectionView struct {
	ID              string        `json:"id"`
	Provider        string        `json:"provider"`
	Environment     string        `json:"environment"`
	Access          string        `json:"access"`
	InstitutionName string        `json:"institution_name"`
	Status          string        `json:"status"`
	StatusReason    string        `json:"status_reason,omitempty"`
	LastSyncedAt    *time.Time    `json:"last_synced_at,omitempty"`
	CreatedAt       time.Time     `json:"created_at"`
	Accounts        []BankAccount `json:"accounts"`
	// FirstSyncChecks are the first sync's checks, for the summary of what
	// was found: each account, its transactions, and whether its balance
	// added up. Only connectors that check balances keep them.
	FirstSyncChecks json.RawMessage `json:"first_sync_checks,omitempty"`
}

type BankAccount struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Mask      string `json:"mask,omitempty"`
	Kind      string `json:"kind"`
	Subtype   string `json:"subtype,omitempty"`
	Currency  string `json:"currency"`
	AccountID string `json:"account_id,omitempty"`
	// Kept is set when this login took the account over from one the Book
	// disconnected: the same account, its link and its history.
	Kept bool `json:"kept_from_earlier_login,omitempty"`
	// TakesOver is the bank account the bank says this new one takes over,
	// as when it moves its customers to a new partner bank, and
	// TakeoverReason says why, for the owner. Until the owner confirms, by
	// linking this account to the old one's Book account, it is a proposal.
	TakesOver      string `json:"takes_over,omitempty"`
	TakeoverReason string `json:"takeover_reason,omitempty"`
	// TakenOverBy is the account that took this one over, once confirmed:
	// the two share a Book account.
	TakenOverBy string `json:"taken_over_by,omitempty"`
	// Closed is set when the bank closed the account. It no longer syncs and
	// needs nothing from the owner.
	Closed bool `json:"closed,omitempty"`
	// BankType and SuggestedAccountID are given for a bank account not yet
	// linked: the Book account it would be linked to.
	BankType           string `json:"bank_type,omitempty"`
	SuggestedAccountID string `json:"suggested_account_id,omitempty"`
	// SuggestedCreate is the Book account to create when there is none to
	// link to.
	SuggestedCreate *SuggestedAccount `json:"suggested_create,omitempty"`
}

// BankOwnerReady says whether the owner may connect a bank to this Book now,
// before anything is asked of the bank: a one-time token handed to a provider
// can't be handed over again after a sign-in.
func (a TenantAdmin) BankOwnerReady(ctx context.Context, book, actor string) error {
	return a.withBankOwner(ctx, book, actor, func(pgx.Tx, Scope) error { return nil })
}

func (a TenantAdmin) withBankOwner(ctx context.Context, book, actor string, call func(pgx.Tx, Scope) error) error {
	if !ValidID(actor) {
		return ErrDenied
	}
	return a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		scope.ActorID = actor
		if err := setScope(ctx, tx, scope); err != nil {
			return dbError(err)
		}
		var allowed bool
		if err := tx.QueryRow(ctx, "SELECT public.bank_owner_allowed($1,$2)", scope.TenantID, scope.BookID).Scan(&allowed); err != nil || !allowed {
			return ErrFreshOwnerProof
		}
		if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, scope.BookID); err != nil {
			return dbError(err)
		}
		return call(tx, scope)
	})
}

// SaveBankProviderSettings stores or replaces the owner's provider keys.
func (a TenantAdmin) SaveBankProviderSettings(ctx context.Context, book, actor string, q BankProviderSettingsChange) (BankProviderSettings, error) {
	var out BankProviderSettings
	if q.Provider != "plaid" || q.Environment != "sandbox" && q.Environment != "production" || q.ClientID == "" || len(q.ClientID) > 128 ||
		len(q.SecretSealed) == 0 || q.KeyVersion < 1 || q.ExpectedRevision < 0 {
		return out, ErrInvalid
	}
	err := a.withBankOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		// The keys are the Vult's. The first save replaces keys saved for a
		// Book before, whatever revision those had; later ones follow the
		// Vult's own revision.
		var current *int64
		if err := tx.QueryRow(ctx, "SELECT (SELECT revision FROM public.vult_bank_provider_settings WHERE tenant_id=$1 AND provider=$2 FOR UPDATE)",
			s.TenantID, q.Provider).Scan(&current); err != nil {
			return dbError(err)
		}
		var err error
		if current == nil {
			_, err = tx.Exec(ctx, `INSERT INTO public.vult_bank_provider_settings(tenant_id,provider,environment,client_id,secret_sealed,key_version,webhook_url,revision,actor_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,1,$8)`, s.TenantID, q.Provider, q.Environment, q.ClientID, q.SecretSealed, q.KeyVersion, q.WebhookURL, actor)
		} else {
			if *current != q.ExpectedRevision {
				return ErrConflict
			}
			_, err = tx.Exec(ctx, `UPDATE public.vult_bank_provider_settings SET environment=$3,client_id=$4,secret_sealed=$5,key_version=$6,webhook_url=$7,revision=revision+1,actor_id=$8
 WHERE tenant_id=$1 AND provider=$2`, s.TenantID, q.Provider, q.Environment, q.ClientID, q.SecretSealed, q.KeyVersion, q.WebhookURL, actor)
		}
		if err != nil {
			return dbError(err)
		}
		out, err = readBankProviderSettings(ctx, tx, s, q.Provider)
		return err
	})
	return out, err
}

func readBankProviderSettings(ctx context.Context, tx pgx.Tx, s Scope, provider string) (BankProviderSettings, error) {
	var v BankProviderSettings
	// The Vult's keys, else keys saved for a Book before, as reads of the
	// secret find them.
	err := tx.QueryRow(ctx, `SELECT provider,environment,client_id,webhook_url,revision,updated_at,true FROM public.vult_bank_provider_settings
 WHERE tenant_id=$1 AND provider=$3
 UNION ALL (SELECT provider,environment,client_id,webhook_url,revision,updated_at,false FROM public.bank_provider_settings
 WHERE tenant_id=$1 AND provider=$3 ORDER BY book_id=$2 DESC,updated_at DESC LIMIT 1) LIMIT 1`, s.TenantID, s.BookID, provider).Scan(
		&v.Provider, &v.Environment, &v.ClientID, &v.WebhookURL, &v.Revision, &v.UpdatedAt, &v.VultWide)
	return v, dbError(err)
}

// BankProviderSettings reads the owner's stored settings, without the secret.
func (a TenantAdmin) BankProviderSettings(ctx context.Context, book, provider string) (BankProviderSettings, error) {
	var out BankProviderSettings
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readBankProviderSettings(ctx, tx, s, provider)
		return err
	})
	return out, err
}

// BankProviderSecret reads the sealed settings for the owner's own use, such
// as starting the provider's connection screen.
func (a TenantAdmin) BankProviderSecret(ctx context.Context, book, provider string) (BankProviderSecret, error) {
	var out BankProviderSecret
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readBankProviderSecret(ctx, tx, s.TenantID, s.BookID, provider)
		return err
	})
	return out, err
}

// AddBankConnection records a login the owner just linked.
func (a TenantAdmin) AddBankConnection(ctx context.Context, book, actor string, q NewBankConnection) (string, error) {
	held := len(q.TokenSealed) > 0 && q.KeyVersion > 0
	if q.Provider != "plaid" && q.Provider != "mercury" || q.ProviderItemID == "" || len(q.ProviderItemID) > 256 || held == q.OperatorHeld || q.ID != "" && !ValidID(q.ID) ||
		q.OperatorHeld && (len(q.TokenSealed) > 0 || q.KeyVersion != 0) {
		return "", ErrInvalid
	}
	id := q.ID
	if id == "" {
		id = ID()
	}
	err := a.withBankOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		access, version := "book", any(q.KeyVersion)
		var token any = q.TokenSealed
		if q.OperatorHeld {
			access, version, token = "operator", nil, nil
		}
		// A login added after another was disconnected starts reconciling:
		// its accounts and history are matched with what the Book already
		// holds, so nothing arrives twice.
		_, err := tx.Exec(ctx, `INSERT INTO public.bank_connections(tenant_id,book_id,id,provider,environment,provider_item_id,access,institution_id,institution_name,token_sealed,key_version,actor_id,sync_requested_at,reconciling)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,clock_timestamp(),
 EXISTS(SELECT 1 FROM public.bank_connections c WHERE c.tenant_id=$1 AND c.book_id=$2 AND c.disconnected_at IS NOT NULL))`, s.TenantID, s.BookID, id, q.Provider, q.Environment, q.ProviderItemID, access,
			clip(q.InstitutionID, 128), clip(q.InstitutionName, 200), token, version, actor)
		return dbError(err)
	})
	return id, err
}

// ReplaceBankToken stores a new sealed token for a login, such as after a
// key rotation re-seals it.
func (a TenantAdmin) ReplaceBankToken(ctx context.Context, book, actor, connection string, sealed []byte, version int) error {
	if !ValidID(connection) || len(sealed) == 0 || version < 1 {
		return ErrInvalid
	}
	return a.withBankOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		tag, err := tx.Exec(ctx, "UPDATE public.bank_connections SET token_sealed=$4,key_version=$5 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND disconnected_at IS NULL",
			s.TenantID, s.BookID, connection, sealed, version)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return dbError(err)
	})
}

// DisconnectBankConnection forgets a login's token. Its accounts, activity
// and the entries that record it stay.
func (a TenantAdmin) DisconnectBankConnection(ctx context.Context, book, actor, connection string) (BankDisconnected, error) {
	var out BankDisconnected
	if !ValidID(connection) {
		return out, ErrInvalid
	}
	err := a.withBankOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		err := tx.QueryRow(ctx, `UPDATE public.bank_connections SET status='disconnected',status_reason='',
 disconnected_at=clock_timestamp(),sync_requested_at=NULL WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND disconnected_at IS NULL
 RETURNING provider,environment,provider_item_id`, s.TenantID, s.BookID, connection).Scan(&out.Provider, &out.Environment, &out.ProviderItemID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return dbError(err)
	})
	return out, err
}

// BankDisconnected names the login a disconnection forgot, so the caller can
// also ask the provider to revoke its access.
type BankDisconnected struct{ Provider, Environment, ProviderItemID string }

// LinkBankAccount links a bank account to a Book account, or unlinks it with
// an empty account. Only cash accounts link to assets and card accounts to
// liabilities, in their own currency.
func (a TenantAdmin) LinkBankAccount(ctx context.Context, book, actor, bankAccount, account string) error {
	if !ValidID(bankAccount) || account != "" && !ValidID(account) {
		return ErrInvalid
	}
	return a.withBankOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		if err := linkBankAccount(ctx, tx, s, bankAccount, account); err != nil {
			return bankError(err)
		}
		return nil
	})
}

// BankConnections lists the Book's logins and their accounts.
func (a TenantAdmin) BankConnections(ctx context.Context, book string) ([]BankConnectionView, error) {
	var out []BankConnectionView
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readBankConnections(ctx, tx, s)
		return err
	})
	return out, err
}

func readBankConnections(ctx context.Context, tx pgx.Tx, s Scope) ([]BankConnectionView, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,provider,environment,access,institution_name,status,status_reason,last_synced_at,created_at,first_sync_checks
 FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 ORDER BY disconnected_at NULLS FIRST,created_at`, s.TenantID, s.BookID)
	if err != nil {
		return nil, dbError(err)
	}
	out := []BankConnectionView{}
	index := map[string]int{}
	for rows.Next() {
		var v BankConnectionView
		var checks []byte
		if err := rows.Scan(&v.ID, &v.Provider, &v.Environment, &v.Access, &v.InstitutionName, &v.Status, &v.StatusReason, &v.LastSyncedAt, &v.CreatedAt, &checks); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		if len(checks) > 0 {
			v.FirstSyncChecks = checks
		}
		v.Accounts = []BankAccount{}
		index[v.ID] = len(out)
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	// An account a live login took over shows under that login.
	rows, err = tx.Query(ctx, `SELECT coalesce(live.connection_id,b.connection_id)::text,b.id::text,b.name,b.mask,b.kind,b.provider_subtype,b.currency,coalesce(b.account_id::text,''),
 live.connection_id IS NOT NULL AND live.connection_id<>b.connection_id,
 coalesce(b.replaces::text,''),b.replaces_reason,coalesce(b.replaced_by::text,''),b.closed_at IS NOT NULL
 FROM public.bank_accounts b LEFT JOIN LATERAL (SELECT x.connection_id FROM public.bank_account_aliases x JOIN public.bank_connections xc
   ON xc.tenant_id=x.tenant_id AND xc.book_id=x.book_id AND xc.id=x.connection_id
   WHERE x.bank_account_id=b.id AND xc.disconnected_at IS NULL ORDER BY x.recorded_at DESC LIMIT 1) live ON true
 WHERE b.tenant_id=$1 AND b.book_id=$2 ORDER BY b.name,b.mask,b.id`, s.TenantID, s.BookID)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var connection string
		var v BankAccount
		if err := rows.Scan(&connection, &v.ID, &v.Name, &v.Mask, &v.Kind, &v.Subtype, &v.Currency, &v.AccountID, &v.Kept,
			&v.TakesOver, &v.TakeoverReason, &v.TakenOverBy, &v.Closed); err != nil {
			return nil, dbError(err)
		}
		if i, ok := index[connection]; ok {
			out[i].Accounts = append(out[i].Accounts, v)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	rows.Close()
	linked := map[string]string{}
	for i := range out {
		for _, v := range out[i].Accounts {
			linked[v.ID] = v.AccountID
		}
	}
	for i := range out {
		for j, v := range out[i].Accounts {
			if v.AccountID != "" || v.Closed {
				continue
			}
			if old := linked[v.TakesOver]; old != "" {
				// The account it takes over keeps recording to its Book
				// account; confirming links this one there too.
				out[i].Accounts[j].SuggestedAccountID = old
				continue
			}
			bankType, account, err := suggestBankLink(ctx, tx, s, v.ID)
			if err != nil {
				return nil, err
			}
			out[i].Accounts[j].BankType, out[i].Accounts[j].SuggestedAccountID = bankType, account
			if account == "" {
				if out[i].Accounts[j].SuggestedCreate, err = suggestBankCreate(ctx, tx, s, v.ID, bankType); err != nil {
					return nil, err
				}
			}
		}
	}
	return out, nil
}

// BankActivity reads the inbox for the owner.
func (a TenantAdmin) BankActivity(ctx context.Context, book string, q BankActivityQuery) (BankActivityPage, []BankSuggestion, error) {
	var page BankActivityPage
	var suggestions []BankSuggestion
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		// Matches and counts are for the whole Book, and only the first page
		// shows them: a later page skips both.
		q.firstOnly = true
		if page, err = readBankActivity(ctx, tx, s, q); err != nil {
			return err
		}
		if q.After != "" {
			suggestions = []BankSuggestion{}
			return nil
		}
		suggestions, err = suggestBankMatches(ctx, tx, s)
		return err
	})
	return page, suggestions, err
}

// RequestBankSync asks for a sync of every live login in the Book now, as
// when the owner opens the app.
func (a TenantAdmin) RequestBankSync(ctx context.Context, book string) error {
	return a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, s Scope, _ Book) error {
		_, err := tx.Exec(ctx, `SELECT public.bank_request_sync(id) FROM public.bank_connections
 WHERE tenant_id=$1 AND book_id=$2 AND status='active' AND (last_synced_at IS NULL OR last_synced_at<clock_timestamp()-interval '2 minutes')`, s.TenantID, s.BookID)
		return dbError(err)
	})
}
