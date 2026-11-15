package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The bank sync writes what a provider reports into the bank tables, one
// login at a time. It is never an agent or the owner: the database lets it
// write only bank activity for the login it names, never entries or links.

// BankSyncTarget names one login.
type BankSyncTarget struct {
	TenantID     string `json:"tenant_id"`
	BookID       string `json:"book_id"`
	ConnectionID string `json:"connection_id"`
}

func (t BankSyncTarget) valid() bool {
	return ValidID(t.TenantID) && ValidID(t.BookID) && ValidID(t.ConnectionID)
}

// BankSyncState is what a sync needs to continue: the sealed token, the
// provider settings that open it, and where the last sync stopped.
type BankSyncState struct {
	Provider       string
	Environment    string
	ProviderItemID string
	// Access is book for the owner's own keys, or operator for a login the
	// server's operator syncs itself.
	Access      string
	Status      string
	TokenSealed []byte
	KeyVersion  int
	Cursor      string
	Settings    *BankProviderSecret
}

// BankProviderSecret is a provider's sealed settings as stored.
type BankProviderSecret struct {
	Provider     string
	Environment  string
	ClientID     string
	SecretSealed []byte
	KeyVersion   int
	WebhookURL   string
	Revision     int64
	// SealedForBook is the Book whose keys these are, sealed to it, when
	// the Vult has no keys of its own; empty for the Vult's keys.
	SealedForBook string
}

type BankSyncAccount struct {
	ProviderAccountID string
	Name              string
	Mask              string
	Type              string
	Subtype           string
	// Kind is cash or card; it decides how activity on the account is signed.
	Kind     string
	Currency string
	Scale    int
}

type BankSyncTransaction struct {
	ProviderAccountID     string
	ProviderTransactionID string
	Date                  string
	AuthorizedDate        string
	// AmountMinor is signed the way the account's balance moves.
	AmountMinor       int64
	Currency          string
	Scale             int
	Description       string
	Counterparty      string
	Pending           bool
	PendingProviderID string
}

// BankSyncBatch is one page of a provider's changes since FromCursor.
// Applying it moves the stored cursor to NextCursor in the same transaction
// as the changes, so a crash never skips or repeats part of a page.
type BankSyncBatch struct {
	FromCursor string
	NextCursor string
	Accounts   []BankSyncAccount
	Added      []BankSyncTransaction
	Modified   []BankSyncTransaction
	// Removed are provider transaction IDs the provider withdrew.
	Removed []string
	// Complete is true on the last page: the sync is current.
	Complete bool
	// FirstChecks are the connector's checks that each account's activity
	// adds up to the balance the bank reports, kept from the first sync.
	FirstChecks json.RawMessage
	// Takeovers are new accounts the bank says take over old ones, for the
	// owner to confirm.
	Takeovers []BankSyncTakeover
	// Closed are provider account IDs the bank closed. Their pending items
	// will never post.
	Closed []string
}

// BankSyncTakeover is the bank's new account taking over an old one, as
// when it moves its customers to a new partner bank.
type BankSyncTakeover struct {
	OldProviderAccountID string
	NewProviderAccountID string
	Reason               string
}

type BankSyncResult struct {
	Added    int `json:"added"`
	Modified int `json:"modified"`
	Removed  int `json:"removed"`
}

// ErrBankCursorMoved means another sync already applied changes from this
// cursor. The caller starts again from the stored cursor.
var ErrBankCursorMoved = errors.New("bank sync cursor moved")

func (s Store) withBankSync(ctx context.Context, t BankSyncTarget, call func(pgx.Tx) error) error {
	if s.Runtime == nil || !t.valid() {
		return ErrDenied
	}
	tx, err := s.Runtime.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return dbError(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT set_config('vult.tenant_id',$1,true),set_config('vult.book_id',$2,true),
 set_config('vult.actor_id','',true),set_config('vult.connection_id','',true),set_config('vult.bank_sync',$3,true)`, t.TenantID, t.BookID, t.ConnectionID); err != nil {
		return dbError(err)
	}
	if err = call(tx); err != nil {
		return err
	}
	return dbError(tx.Commit(ctx))
}

// BankSyncState reads a login's sync state for the sync.
func (s Store) BankSyncState(ctx context.Context, t BankSyncTarget) (BankSyncState, error) {
	var out BankSyncState
	err := s.withBankSync(ctx, t, func(tx pgx.Tx) error {
		var version *int
		err := tx.QueryRow(ctx, `SELECT provider,environment,provider_item_id,access,status,token_sealed,key_version,sync_cursor
 FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3`, t.TenantID, t.BookID, t.ConnectionID).Scan(
			&out.Provider, &out.Environment, &out.ProviderItemID, &out.Access, &out.Status, &out.TokenSealed, &version, &out.Cursor)
		if err != nil {
			return dbError(err)
		}
		if version != nil {
			out.KeyVersion = *version
		}
		settings, err := readBankProviderSecret(ctx, tx, t.TenantID, t.BookID, out.Provider)
		if err == nil {
			out.Settings = &settings
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return nil
	})
	return out, err
}

// readBankProviderSecret reads the keys a Book connects banks with: the
// Vult's, else keys saved for this Book before, else (for the owner) those
// saved for another of the Vult's Books, most recent first.
func readBankProviderSecret(ctx context.Context, tx pgx.Tx, tenant, book, provider string) (BankProviderSecret, error) {
	var v BankProviderSecret
	err := tx.QueryRow(ctx, `SELECT provider,environment,client_id,secret_sealed,key_version,webhook_url,revision,'' FROM public.vult_bank_provider_settings
 WHERE tenant_id=$1 AND provider=$3
 UNION ALL (SELECT provider,environment,client_id,secret_sealed,key_version,webhook_url,revision,book_id::text FROM public.bank_provider_settings
 WHERE tenant_id=$1 AND provider=$3 ORDER BY book_id=$2 DESC,updated_at DESC LIMIT 1) LIMIT 1`, tenant, book, provider).Scan(
		&v.Provider, &v.Environment, &v.ClientID, &v.SecretSealed, &v.KeyVersion, &v.WebhookURL, &v.Revision, &v.SealedForBook)
	return v, dbError(err)
}

// ApplyBankSync records one page of provider changes and moves the cursor.
func (s Store) ApplyBankSync(ctx context.Context, t BankSyncTarget, b BankSyncBatch) (BankSyncResult, error) {
	var out BankSyncResult
	if len(b.NextCursor) > 4096 || len(b.Accounts) > 500 || len(b.Added)+len(b.Modified)+len(b.Removed) > 50000 ||
		len(b.Takeovers) > 500 || len(b.Closed) > 500 {
		return out, ErrInvalid
	}
	err := s.withBankSync(ctx, t, func(tx pgx.Tx) error {
		var cursor, status string
		var reconciling bool
		if err := tx.QueryRow(ctx, "SELECT sync_cursor,status,reconciling FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3 FOR UPDATE",
			t.TenantID, t.BookID, t.ConnectionID).Scan(&cursor, &status, &reconciling); err != nil {
			return dbError(err)
		}
		switch {
		case status == "disconnected":
			return ErrDenied
		case status == "paused":
			return ErrBankPaused
		case cursor != b.FromCursor:
			return ErrBankCursorMoved
		}
		r := bankReconciler{ctx: ctx, tx: tx, t: t, reconciling: reconciling, accounts: map[string]string{}, claimed: map[string]bool{}}
		for _, a := range b.Accounts {
			if !validBankAccount(a) {
				return ErrInvalid
			}
			if err := r.account(a); err != nil {
				return err
			}
		}
		for _, v := range b.Added {
			if ok, err := r.transaction(v); err != nil {
				return err
			} else if ok {
				out.Added++
			}
		}
		for _, v := range b.Modified {
			if ok, err := r.transaction(v); err != nil {
				return err
			} else if ok {
				out.Modified++
			}
		}
		for _, id := range b.Removed {
			if id == "" || len(id) > 256 {
				return ErrInvalid
			}
			tag, err := tx.Exec(ctx, `UPDATE public.bank_transactions t SET removed_at=clock_timestamp() FROM public.bank_accounts a
 WHERE a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=t.bank_account_id AND a.connection_id=$3
 AND t.tenant_id=$1 AND t.book_id=$2 AND t.removed_at IS NULL AND (t.provider_transaction_id=$4 OR t.id IN (
   SELECT x.transaction_id FROM public.bank_transaction_aliases x WHERE x.bank_account_id=a.id AND x.provider_transaction_id=$4))`,
				t.TenantID, t.BookID, t.ConnectionID, id)
			if err != nil {
				return dbError(err)
			}
			out.Removed += int(tag.RowsAffected())
		}
		for _, v := range b.Takeovers {
			if v.OldProviderAccountID == v.NewProviderAccountID || len(v.Reason) > 1000 {
				return ErrInvalid
			}
			old, err := r.lookupAccount(v.OldProviderAccountID)
			if err != nil {
				return err
			}
			taking, err := r.lookupAccount(v.NewProviderAccountID)
			if err != nil {
				return err
			}
			if old == "" || taking == "" {
				continue
			}
			// The bank says it once; a later sync saying it again changes
			// nothing.
			if _, err := tx.Exec(ctx, `UPDATE public.bank_accounts SET replaces=$4,replaces_reason=$5
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND replaces IS NULL`, t.TenantID, t.BookID, taking, old, v.Reason); err != nil {
				return dbError(err)
			}
		}
		for _, id := range b.Closed {
			account, err := r.lookupAccount(id)
			if err != nil {
				return err
			}
			if account == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE public.bank_accounts SET closed_at=clock_timestamp()
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND closed_at IS NULL`, t.TenantID, t.BookID, account); err != nil {
				return dbError(err)
			}
			// Pending activity on a closed account will never post.
			tag, err := tx.Exec(ctx, `UPDATE public.bank_transactions SET removed_at=clock_timestamp()
 WHERE tenant_id=$1 AND book_id=$2 AND bank_account_id=$3 AND pending AND removed_at IS NULL`, t.TenantID, t.BookID, account)
			if err != nil {
				return dbError(err)
			}
			out.Removed += int(tag.RowsAffected())
		}
		var first *string
		if len(b.FirstChecks) > 0 && b.FromCursor == "" {
			v := string(b.FirstChecks)
			first = &v
		}
		_, err := tx.Exec(ctx, `UPDATE public.bank_connections SET sync_cursor=$4,last_attempt_at=clock_timestamp(),
 last_synced_at=CASE WHEN $5 THEN clock_timestamp() ELSE last_synced_at END,
 sync_requested_at=CASE WHEN $5 THEN NULL ELSE sync_requested_at END,status='active',status_reason='',
 reconciling=reconciling AND NOT $5,first_sync_checks=coalesce($6::jsonb,first_sync_checks)
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3`, t.TenantID, t.BookID, t.ConnectionID, b.NextCursor, b.Complete, first)
		return dbError(err)
	})
	return out, err
}

// ErrBankPaused means the login is paused; nothing syncs until it resumes.
var ErrBankPaused = errors.New("bank login is paused")

// bankReconciler applies one batch. Provider IDs resolve directly or through
// the aliases a reconnected login recorded. While a login is reconciling,
// after it restarted on a new provider login, unknown accounts and
// transactions are first matched with what the Book already holds: exactly
// one match is the same record and gets an alias; none is new; several are
// added, marked as possibly repeating the first, for a person to decide.
type bankReconciler struct {
	ctx         context.Context
	tx          pgx.Tx
	t           BankSyncTarget
	reconciling bool
	accounts    map[string]string
	claimed     map[string]bool
}

func (r *bankReconciler) account(a BankSyncAccount) error {
	ctx, tx, t := r.ctx, r.tx, r.t
	var id string
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT bank_account_id::text FROM public.bank_account_aliases WHERE connection_id=$3 AND provider_account_id=$4),
 (SELECT id::text FROM public.bank_accounts WHERE tenant_id=$1 AND book_id=$2 AND connection_id=$3 AND provider_account_id=$4),'')`,
		t.TenantID, t.BookID, t.ConnectionID, a.ProviderAccountID).Scan(&id)
	if err != nil {
		return dbError(err)
	}
	if id == "" && r.reconciling {
		// The same login's earlier accounts, or an account of a login the
		// Book disconnected that no live login has taken over.
		rows, err := tx.Query(ctx, `SELECT b.id::text FROM public.bank_accounts b
 WHERE b.tenant_id=$1 AND b.book_id=$2 AND (b.connection_id=$3 AND b.provider_account_id<>$8 OR b.connection_id<>$3 AND public.bank_login_disconnected($1,$2,b.connection_id))
 AND b.kind=$4 AND b.currency=$5 AND b.mask=$6 AND b.provider_subtype=$7
 AND NOT EXISTS(SELECT 1 FROM public.bank_account_aliases x WHERE x.bank_account_id=b.id AND NOT public.bank_login_disconnected($1,$2,x.connection_id))
 ORDER BY b.id`,
			t.TenantID, t.BookID, t.ConnectionID, a.Kind, a.Currency, clip(a.Mask, 8), clip(a.Subtype, 64), a.ProviderAccountID)
		if err != nil {
			return dbError(err)
		}
		var found []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return dbError(err)
			}
			if !r.claimed["account:"+v] {
				found = append(found, v)
			}
		}
		rows.Close()
		if len(found) == 1 {
			id = found[0]
			r.claimed["account:"+id] = true
			if _, err := tx.Exec(ctx, `INSERT INTO public.bank_account_aliases(tenant_id,book_id,connection_id,provider_account_id,bank_account_id)
 VALUES($1,$2,$3,$4,$5)`, t.TenantID, t.BookID, t.ConnectionID, a.ProviderAccountID, id); err != nil {
				return dbError(err)
			}
		}
	}
	if id != "" {
		_, err := tx.Exec(ctx, `UPDATE public.bank_accounts SET name=$4,mask=$5,provider_type=$6,provider_subtype=$7 WHERE tenant_id=$1 AND book_id=$2 AND id=$3`,
			t.TenantID, t.BookID, id, clip(a.Name, 200), clip(a.Mask, 8), clip(a.Type, 64), clip(a.Subtype, 64))
		r.accounts[a.ProviderAccountID] = id
		return dbError(err)
	}
	id = ID()
	if _, err := tx.Exec(ctx, `INSERT INTO public.bank_accounts(tenant_id,book_id,id,connection_id,provider_account_id,name,mask,provider_type,provider_subtype,kind,currency,scale)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, t.TenantID, t.BookID, id, t.ConnectionID, a.ProviderAccountID, clip(a.Name, 200), clip(a.Mask, 8),
		clip(a.Type, 64), clip(a.Subtype, 64), a.Kind, a.Currency, a.Scale); err != nil {
		return dbError(err)
	}
	r.accounts[a.ProviderAccountID] = id
	return nil
}

func (r *bankReconciler) lookupAccount(provider string) (string, error) {
	if id, ok := r.accounts[provider]; ok {
		return id, nil
	}
	var id string
	err := r.tx.QueryRow(r.ctx, `SELECT coalesce((SELECT bank_account_id::text FROM public.bank_account_aliases WHERE connection_id=$3 AND provider_account_id=$4),
 (SELECT id::text FROM public.bank_accounts WHERE tenant_id=$1 AND book_id=$2 AND connection_id=$3 AND provider_account_id=$4),'')`,
		r.t.TenantID, r.t.BookID, r.t.ConnectionID, provider).Scan(&id)
	// Activity on an account the provider didn't describe isn't kept.
	r.accounts[provider] = id
	return id, dbError(err)
}

// transaction records one reported transaction and reports whether anything
// changed.
func (r *bankReconciler) transaction(v BankSyncTransaction) (bool, error) {
	ctx, tx, t := r.ctx, r.tx, r.t
	if !validBankTransaction(v) {
		return false, ErrInvalid
	}
	account, err := r.lookupAccount(v.ProviderAccountID)
	if err != nil || account == "" {
		return false, err
	}
	var existing string
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM public.bank_transactions WHERE tenant_id=$1 AND book_id=$2 AND bank_account_id=$3 AND provider_transaction_id=$4),
 (SELECT transaction_id::text FROM public.bank_transaction_aliases WHERE bank_account_id=$3 AND provider_transaction_id=$4),'')`,
		t.TenantID, t.BookID, account, v.ProviderTransactionID).Scan(&existing); err != nil {
		return false, dbError(err)
	}
	repeat := ""
	if existing == "" && r.reconciling && !v.Pending {
		rows, err := tx.Query(ctx, `SELECT id::text FROM public.bank_transactions WHERE tenant_id=$1 AND book_id=$2 AND bank_account_id=$3
 AND occurred_on=$4::date AND amount_minor=$5 AND NOT pending AND removed_at IS NULL ORDER BY id`, t.TenantID, t.BookID, account, v.Date, v.AmountMinor)
		if err != nil {
			return false, dbError(err)
		}
		var found []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return false, dbError(err)
			}
			if !r.claimed[id] {
				found = append(found, id)
			}
		}
		rows.Close()
		switch {
		case len(found) == 1:
			r.claimed[found[0]] = true
			_, err := tx.Exec(ctx, `INSERT INTO public.bank_transaction_aliases(tenant_id,book_id,bank_account_id,provider_transaction_id,transaction_id)
 VALUES($1,$2,$3,$4,$5)`, t.TenantID, t.BookID, account, v.ProviderTransactionID, found[0])
			// The same transaction again: the Book's record stands.
			return false, dbError(err)
		case len(found) > 1:
			repeat = found[0]
		}
	}
	if existing == "" && repeat == "" && !v.Pending {
		// A statement line may already have brought this transaction in for
		// the same Book account: the bank joins that item.
		var book *string
		if err := tx.QueryRow(ctx, "SELECT account_id::text FROM public.bank_accounts WHERE tenant_id=$1 AND book_id=$2 AND id=$3", t.TenantID, t.BookID, account).Scan(&book); err != nil {
			return false, dbError(err)
		}
		if book != nil {
			found, err := sameTransaction(ctx, tx, Scope{TenantID: t.TenantID, BookID: t.BookID}, `t.bank_account_id IS NULL AND t.source='statement' AND t.account_id=$3`, *book, v.Date, v.AmountMinor, r.claimed)
			if err != nil {
				return false, err
			}
			switch {
			case len(found) == 1:
				r.claimed[found[0]] = true
				if _, err := tx.Exec(ctx, `INSERT INTO public.bank_transaction_aliases(tenant_id,book_id,bank_account_id,provider_transaction_id,transaction_id)
 VALUES($1,$2,$3,$4,$5)`, t.TenantID, t.BookID, account, v.ProviderTransactionID, found[0]); err != nil {
					return false, dbError(err)
				}
				existing = found[0]
				if _, err := tx.Exec(ctx, "UPDATE public.bank_transactions SET bank_account_id=$4 WHERE tenant_id=$1 AND book_id=$2 AND id=$3", t.TenantID, t.BookID, existing, account); err != nil {
					return false, dbError(err)
				}
			case len(found) > 1:
				repeat = found[0]
			}
		}
	}
	if existing != "" {
		tag, err := tx.Exec(ctx, `UPDATE public.bank_transactions SET occurred_on=$4::date,authorized_on=nullif($5,'')::date,amount_minor=$6,description=$7,
 counterparty=$8,pending=$9,pending_provider_id=nullif($10,''),removed_at=NULL WHERE tenant_id=$1 AND book_id=$2 AND id=$3`,
			t.TenantID, t.BookID, existing, v.Date, v.AuthorizedDate, v.AmountMinor, clip(v.Description, 1000), clip(v.Counterparty, 200), v.Pending, v.PendingProviderID)
		return err == nil && tag.RowsAffected() > 0, dbError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO public.bank_transactions(tenant_id,book_id,id,bank_account_id,provider_transaction_id,occurred_on,authorized_on,
 amount_minor,currency,scale,description,counterparty,pending,pending_provider_id,possible_repeat_of)
 VALUES($1,$2,$3,$4,$5,$6::date,nullif($7,'')::date,$8,$9,$10,$11,$12,$13,nullif($14,''),nullif($15,'')::uuid)`,
		t.TenantID, t.BookID, ID(), account, v.ProviderTransactionID, v.Date, v.AuthorizedDate, v.AmountMinor, v.Currency, v.Scale,
		clip(v.Description, 1000), clip(v.Counterparty, 200), v.Pending, v.PendingProviderID, repeat)
	return err == nil, dbError(err)
}

// RecordBankSyncProblem keeps how the last attempt went. needsAttention
// marks a login the owner must reconnect, such as an expired bank sign-in;
// other problems are retried on the next schedule.
func (s Store) RecordBankSyncProblem(ctx context.Context, t BankSyncTarget, reason string, needsAttention bool) error {
	return s.withBankSync(ctx, t, func(tx pgx.Tx) error {
		status := "active"
		if needsAttention {
			status = "needs_attention"
		}
		_, err := tx.Exec(ctx, `UPDATE public.bank_connections SET last_attempt_at=clock_timestamp(),status=$4,status_reason=$5
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND status<>'disconnected'`, t.TenantID, t.BookID, t.ConnectionID, status, clip(reason, 400))
		return dbError(err)
	})
}

// BankConnectionsDue lists logins to sync: those asked for, then those not
// tried within every.
func (s Store) BankConnectionsDue(ctx context.Context, every time.Duration, limit int) ([]BankSyncTarget, error) {
	if s.Runtime == nil {
		return nil, ErrDenied
	}
	rows, err := s.Runtime.Query(ctx, "SELECT tenant_id::text,book_id::text,id::text FROM public.bank_connections_due($1,$2)", every, limit)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var out []BankSyncTarget
	for rows.Next() {
		var t BankSyncTarget
		if err := rows.Scan(&t.TenantID, &t.BookID, &t.ConnectionID); err != nil {
			return nil, dbError(err)
		}
		out = append(out, t)
	}
	return out, dbError(rows.Err())
}

// RestartBankConnection moves an operator's login to a new provider login,
// such as after a pause ended the old one, and syncs it again from the start.
// Pending activity from the old login is withdrawn; the new login reports
// what posted.
// Its accounts, activity and the Book's records stay; the operator sends new
// activity under the accounts' existing provider IDs.
func (s Store) RestartBankConnection(ctx context.Context, t BankSyncTarget, item string) error {
	if item == "" || len(item) > 256 {
		return ErrInvalid
	}
	return s.withBankSync(ctx, t, func(tx pgx.Tx) error {
		// The old login's pending activity is never reported again: the new
		// login reports its posted version. Withdraw it so nothing shows twice.
		if _, err := tx.Exec(ctx, `UPDATE public.bank_transactions t SET removed_at=clock_timestamp() FROM public.bank_accounts a
 WHERE a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=t.bank_account_id AND a.connection_id=$3
 AND t.tenant_id=$1 AND t.book_id=$2 AND t.pending AND t.removed_at IS NULL`, t.TenantID, t.BookID, t.ConnectionID); err != nil {
			return bankError(err)
		}
		tag, err := tx.Exec(ctx, `UPDATE public.bank_connections SET provider_item_id=$4,sync_cursor='',status='active',status_reason='',
 reconciling=true,access_ended_at=NULL,sync_requested_at=clock_timestamp() WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND access='operator' AND disconnected_at IS NULL`,
			t.TenantID, t.BookID, t.ConnectionID, item)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return bankError(err)
	})
}

// FindBankConnectionForItem finds the live login a provider notification
// names. It changes nothing; the caller verifies the notification first.
func (s Store) FindBankConnectionForItem(ctx context.Context, provider, item string) (BankSyncTarget, bool, error) {
	var t BankSyncTarget
	if s.Runtime == nil || provider == "" || item == "" || len(item) > 256 {
		return t, false, ErrInvalid
	}
	err := s.Runtime.QueryRow(ctx, "SELECT tenant_id::text,book_id::text,id::text FROM public.bank_connection_for_item($1,$2)", provider, item).Scan(&t.TenantID, &t.BookID, &t.ConnectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, false, nil
	}
	return t, err == nil, dbError(err)
}

// RequestBankSync asks for a sync of one login as soon as the sync runs.
func (s Store) RequestBankSync(ctx context.Context, t BankSyncTarget) error {
	if s.Runtime == nil || !t.valid() {
		return ErrInvalid
	}
	_, err := s.Runtime.Exec(ctx, "SELECT public.bank_request_sync($1)", t.ConnectionID)
	return dbError(err)
}

func validBankAccount(a BankSyncAccount) bool {
	return a.ProviderAccountID != "" && len(a.ProviderAccountID) <= 256 && (a.Kind == "cash" || a.Kind == "card") &&
		validCurrency(a.Currency) && a.Scale >= 0 && a.Scale <= 18
}

func validBankTransaction(v BankSyncTransaction) bool {
	return v.ProviderTransactionID != "" && len(v.ProviderTransactionID) <= 256 && v.ProviderAccountID != "" && ValidDate(v.Date) &&
		(v.AuthorizedDate == "" || ValidDate(v.AuthorizedDate)) && v.AmountMinor != 0 && validCurrency(v.Currency) &&
		v.Scale >= 0 && v.Scale <= 18 && len(v.PendingProviderID) <= 256
}

func validCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// clip shortens provider text to a column's limit without splitting a
// character.
func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	for len(string(r)) > max {
		r = r[:len(r)-1]
	}
	return string(r)
}
