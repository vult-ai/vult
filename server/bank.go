package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vult-ai/vult/bankconnect"
	"github.com/vult-ai/vult/internal/secretbox"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// BankConnections configures connecting Books to the owner's bank. Without
// it, Vult offers no bank connection.
type BankConnections struct {
	// KeyringFile keeps the key that seals access tokens, in one private
	// file outside the database and its backups.
	KeyringFile string `json:"keyring_file,omitempty"`
	// KeyFolder keeps that key on a Mac: in this private folder, sealed by
	// the Mac's Secure Enclave, or in a locked file left out of Time Machine
	// on a Mac without one.
	KeyFolder string `json:"key_folder,omitempty"`
	// SyncMinutes is the schedule, 15 to 1440 minutes; 0 means hourly.
	SyncMinutes int `json:"sync_minutes,omitempty"`
	// Limit is how many bank logins each Vult may hold; none when unset.
	Limit *BankLimitSetting `json:"limit,omitempty"`
}

func (b *BankConnections) validate() error {
	if b == nil {
		return nil
	}
	if (b.KeyringFile == "") == (b.KeyFolder == "") || b.KeyringFile != "" && !filepath.IsAbs(b.KeyringFile) || b.KeyFolder != "" && !filepath.IsAbs(b.KeyFolder) ||
		b.SyncMinutes != 0 && (b.SyncMinutes < 15 || b.SyncMinutes > 1440) || b.Limit.validate() != nil {
		return ErrConfiguration
	}
	return nil
}

// OpenBanks prepares bank connections for a configuration. It returns nil
// when the configuration doesn't enable them. The caller runs the sync loop,
// and may add connectors of its own before the server starts.
func OpenBanks(c Config, store ledger.Store, changed func(tenant, book string)) (*bankconnect.Service, error) {
	b := c.Banks
	if b == nil {
		return nil, nil
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	var source secretbox.Source = secretbox.FileSource{Path: b.KeyringFile}
	if b.KeyFolder != "" {
		program, err := os.Executable()
		if err != nil {
			return nil, ErrConfiguration
		}
		// The helper ships beside the vult program.
		if source, err = secretbox.MacSource(context.Background(), b.KeyFolder, filepath.Join(filepath.Dir(program), "vult-enclave"), secretbox.LevelRead); err != nil {
			return nil, err
		}
	}
	every := bankconnect.DefaultEvery
	if b.SyncMinutes != 0 {
		every = time.Duration(b.SyncMinutes) * time.Minute
	}
	return &bankconnect.Service{Store: store, Keys: &secretbox.Cached{Source: source}, Every: every,
		Connectors: map[string]bankconnect.Connector{"mercury": &bankconnect.Mercury{}}, Changed: changed}, nil
}

// BankOperator provides bank access for the Books on this server with the
// operator's own provider account, for example someone running Vult for a
// family or a firm. When it is set, linking, disconnecting and syncing go to
// the operator, which records each login on
// the Book (held by the operator, with no token stored here) and syncs it
// into the same inbox through ledger.Store.ApplyBankSync. The inbox, account
// links, matches and agent tools stay the same. A refusal the owner can act
// on, such as a limit, is a *ledger.RefusedError.
//
// Disconnecting and pausing are the Book's own decisions, taken on the
// screen or approved from an agent's proposal. Afterwards the server's sync
// timer calls Disconnect or Pause (bankconnect.OperatorAccess) to end the
// provider's access, retrying until it succeeds. An automatic pause with no
// owner signed in is ledger.Store.PauseBankConnection, followed by the same
// call. Reconnecting a paused login is a link with BankLinkFinish.ConnectionID
// set; the operator restarts it with ledger.Store.RestartBankConnection.
type BankOperator interface {
	bankconnect.OperatorAccess
	// StartLink opens the provider's screen; connection names a login to
	// reconnect, or is empty for a new one.
	StartLink(ctx context.Context, o ledger.TenantAdmin, tenant, book, actor, connection string) (bankconnect.LinkStart, error)
	FinishLink(ctx context.Context, o ledger.TenantAdmin, tenant, book, actor string, q BankLinkFinish) (BankLinkResult, error)
	RequestSync(ctx context.Context, tenant, book string) error
}

// BankLinkFinish is what the provider's connection screen returned. The
// institution and accounts come from the screen's own metadata, so an
// operator can recognize a login already linked before exchanging the token.
type BankLinkFinish struct {
	PublicToken string `json:"public_token"`
	// ConnectionID names the login being reconnected, or is empty.
	ConnectionID  string                `json:"connection_id,omitempty"`
	InstitutionID string                `json:"institution_id,omitempty"`
	Accounts      []BankLinkAccountHint `json:"accounts,omitempty"`
}

type BankLinkAccountHint struct {
	Mask    string `json:"mask,omitempty"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type,omitempty"`
	Subtype string `json:"subtype,omitempty"`
}

// BankLinkResult names the Book's connection for the login. Reused is true
// when an existing login now feeds this Book too; Notice is a plain sentence
// for the owner.
type BankLinkResult struct {
	ConnectionID string `json:"connection_id"`
	Notice       string `json:"notice,omitempty"`
	Reused       bool   `json:"reused,omitempty"`
}

const (
	bankPath             = "/api/v1/bank"
	bankLinkStartPath    = "/api/v1/bank/link/start"
	bankLinkFinishPath   = "/api/v1/bank/link/finish"
	bankAccountLinkPath  = "/api/v1/bank/accounts/link"
	bankAccountsLinkAll  = "/api/v1/bank/accounts/link-all"
	bankConnectTokenPath = "/api/v1/bank/connect-token"
	bankConnectorsPath   = "/api/v1/bank/connectors"
	bankPopularPath      = "/api/v1/bank/institutions/popular"
	bankSearchPath       = "/api/v1/bank/institutions/search"
	bankDisconnectPath   = "/api/v1/bank/connections/disconnect"
	bankPausePath        = "/api/v1/bank/connections/pause"
	bankResumePath       = "/api/v1/bank/connections/resume"
	entryOriginPath      = "/api/v1/entry-origin"
	bankActivityPath     = "/api/v1/bank/activity"
	bankSyncPath         = "/api/v1/bank/sync"
	bankKindHistoryPath  = "/api/v1/bank/kind-history"
)

func bankRoute(path string) bool {
	return path == bankPath || strings.HasPrefix(path, bankPath+"/")
}

type bankOverview struct {
	SchemaVersion int                         `json:"schema_version"`
	Connections   []ledger.BankConnectionView `json:"connections"`
	Notifications bool                        `json:"notifications"`
	// Keys is operator when this server provides bank access through its
	// operator; empty otherwise.
	Keys string `json:"keys,omitempty"`
	// Limit is how many banks this Vult may connect, where there is a limit.
	Limit *BankLimit `json:"limit,omitempty"`
}

func (a *webAPI) bank(w http.ResponseWriter, r *http.Request, session string) {
	if a.banks == nil {
		failure(w, http.StatusNotFound)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == bankPath:
		a.bankOverview(w, r, session)
	case r.Method == http.MethodGet && r.URL.Path == bankActivityPath:
		a.bankActivity(w, r, session)
	case r.Method == http.MethodPost && r.URL.Path == bankLinkStartPath:
		a.bankLinkStart(w, r, session)
	case r.Method == http.MethodPost && r.URL.Path == bankLinkFinishPath:
		a.bankLinkFinish(w, r, session)
	case r.Method == http.MethodPost && r.URL.Path == institutionRequestsPath:
		a.institutionRequest(w, r, session)
	case r.Method == http.MethodGet && (r.URL.Path == bankPopularPath || r.URL.Path == bankSearchPath):
		a.bankInstitutions(w, r, session)
	case r.Method == http.MethodGet && r.URL.Path == bankConnectorsPath:
		a.bankConnectors(w, r, session)
	case r.Method == http.MethodPost && r.URL.Path == bankConnectTokenPath:
		a.bankConnectToken(w, r, session)
	case r.Method == http.MethodPost && r.URL.Path == bankAccountsLinkAll:
		a.bankAccountsLinkAll(w, r, session)
	case r.Method == http.MethodPost && r.URL.Path == bankAccountLinkPath:
		a.bankAccountLink(w, r, session)
	case r.Method == http.MethodPost && (r.URL.Path == bankDisconnectPath || r.URL.Path == bankPausePath || r.URL.Path == bankResumePath):
		a.bankConnectionChange(w, r, session)
	case r.Method == http.MethodPost && r.URL.Path == bankSyncPath:
		a.bankSync(w, r, session)
	case r.Method == http.MethodPost && r.URL.Path == bankKindHistoryPath:
		a.bankKindHistory(w, r, session)
	default:
		failure(w, http.StatusNotFound)
	}
}

func (a *webAPI) bankBook(w http.ResponseWriter, r *http.Request, session, book string) (string, bool) {
	if !ledger.ValidID(book) {
		apiFailure(w, ledger.ErrInvalid)
		return "", false
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return "", false
	}
	return tenant, true
}

// errNoBankService answers a request for a provider's connection screen on a
// Vult that has none.
var errNoBankService = &ledger.RefusedError{Reason: "No bank connection service is available on this Vult. Bring activity in by uploading a statement, or through your agent."}

func bankAnswer(w http.ResponseWriter, err error) bool {
	var refused *ledger.RefusedError
	switch {
	case err == nil:
		return false
	case errors.As(err, &refused):
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "refused", "message": refused.Reason})
	case errors.Is(err, ledger.ErrFreshOwnerProof):
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "fresh_authentication_required", "message": "Confirm it’s you, then make this change."})
	case errors.Is(err, bankconnect.ErrNotConfigured):
		return bankAnswer(w, errNoBankService)
	case errors.Is(err, secretbox.ErrKeyStore):
		// Nothing was sealed or saved; the cause, such as a folder this
		// service can't write, goes to the log for whoever looks after the Mac.
		log.Printf("bank: %v", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "key_store", "message": "Not saved. Vult couldn't keep keys on this Mac, so nothing changed. Reinstalling Vult sets up the folder it keeps them in."})
	case errors.Is(err, bankconnect.ErrProvider), errors.Is(err, bankconnect.ErrReconnect), errors.Is(err, bankconnect.ErrUnsupported), errors.Is(err, bankconnect.ErrCredentials):
		// The provider's reason goes to the log and, when it gave one the
		// owner may see, to the owner.
		log.Printf("bank: %v", err)
		message := "The bank connection service didn't complete this. Try again in a moment."
		if reason := bankconnect.Reason(err); reason != "" {
			message = "The bank connection service didn't complete this: " + reason
		}
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "provider", "message": message})
	default:
		// Unexpected: kept in the log, so the owner's "may already be saved" has a cause to find.
		log.Printf("bank: %v", err)
		apiFailure(w, err)
	}
	return true
}

func readBankBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil || strictjson.Decode(body, out) != nil {
		apiFailure(w, ledger.ErrInvalid)
		return false
	}
	return true
}

func (a *webAPI) bankOverview(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	book := q.Get("book_id")
	if len(q) != 1 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, ok := a.bankBook(w, r, session, book)
	if !ok {
		return
	}
	out := bankOverview{SchemaVersion: 1, Notifications: a.banks.WebhookURL != ""}
	if a.bankOperator != nil {
		out.Keys = "operator"
	}
	err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		if out.Connections, err = o.BankConnections(r.Context(), book); err != nil {
			return err
		}
		out.Limit, err = a.bankLimitFor(r.Context(), o, tenant, book)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *webAPI) bankActivity(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	for key := range q {
		if key != "book_id" && key != "status" && key != "after" && key != "bank_account_id" {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	book := q.Get("book_id")
	tenant, ok := a.bankBook(w, r, session, book)
	if !ok {
		return
	}
	var page ledger.BankActivityPage
	var suggestions []ledger.BankSuggestion
	err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		page, suggestions, err = o.BankActivity(r.Context(), book, ledger.BankActivityQuery{Status: q.Get("status"), After: q.Get("after"), BankAccountID: q.Get("bank_account_id")})
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "page": page, "suggestions": suggestions})
}

// bankKindHistory says how the Book recorded earlier bank items of each kind
// named, so waiting items can be told apart from what's new. It only reads;
// the kinds travel in the body because there can be many.
func (a *webAPI) bankKindHistory(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BookID   string   `json:"book_id"`
		KindKeys []string `json:"kind_keys"`
	}
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256*1024))
	if err != nil || strictjson.Decode(body, &q) != nil || len(q.KindKeys) > ledger.MaxKindHistoryKeys {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	var kinds []ledger.KindHistory
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		kinds, err = o.KindHistory(r.Context(), q.BookID, q.KindKeys)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "kinds": kinds})
}

func (a *webAPI) bankLinkStart(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BookID       string `json:"book_id"`
		ConnectionID string `json:"connection_id"`
		// InstitutionID names the bank the owner picked, and RoutingNumber,
		// for a new login, puts it first on the provider's screen. The
		// provider is never sent the id itself.
		InstitutionID string `json:"institution_id,omitempty"`
		RoutingNumber string `json:"routing_number,omitempty"`
		// CountryCodes are the countries the picker searched, so the
		// screen offers the bank picked.
		CountryCodes []string `json:"country_codes,omitempty"`
	}
	if !readBankBody(w, r, &q) || q.ConnectionID != "" && !ledger.ValidID(q.ConnectionID) {
		return
	}
	countries, valid := parseCountries(strings.Join(q.CountryCodes, ","))
	if len(q.CountryCodes) == 0 {
		countries, valid = nil, true
	}
	if q.RoutingNumber != "" && (q.ConnectionID != "" || !bankconnect.ValidRoutingNumber(q.RoutingNumber)) || len(q.InstitutionID) > 64 || !valid || len(q.CountryCodes) > 20 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	if a.bankScreen == nil {
		bankAnswer(w, errNoBankService)
		return
	}
	var out bankconnect.LinkStart
	err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		// A new login needs room; signing in to one again never does.
		if q.ConnectionID == "" {
			if err = a.bankRoomFor(r.Context(), o, tenant, q.BookID); err != nil {
				return err
			}
		}
		if a.bankOperator != nil {
			out, err = a.bankOperator.StartLink(r.Context(), o, tenant, q.BookID, who, q.ConnectionID)
			return err
		}
		if q.ConnectionID != "" {
			out, err = a.banks.StartSignIn(r.Context(), o, tenant, q.BookID, who, q.ConnectionID)
			return err
		}
		if countries == nil {
			if countries, err = vultCountries(r.Context(), o); err != nil {
				return err
			}
		}
		out, err = a.banks.StartLinkWith(r.Context(), o, tenant, q.BookID, who, a.bankScreen.Provider, bankconnect.LinkOptions{RoutingNumber: q.RoutingNumber, Countries: countries})
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *webAPI) bankLinkFinish(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BookID string `json:"book_id"`
		BankLinkFinish
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" || err != nil || json.Unmarshal(body, &q) != nil ||
		q.PublicToken == "" || len(q.PublicToken) > 512 || len(q.InstitutionID) > 128 || len(q.Accounts) > 64 ||
		q.ConnectionID != "" && !ledger.ValidID(q.ConnectionID) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	if a.bankScreen == nil {
		bankAnswer(w, errNoBankService)
		return
	}
	var out BankLinkResult
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		if a.bankOperator != nil {
			out, err = a.bankOperator.FinishLink(r.Context(), o, tenant, q.BookID, who, q.BankLinkFinish)
			return err
		}
		if q.ConnectionID != "" {
			// Signed in again: the same login, with the same access.
			out.ConnectionID = q.ConnectionID
			return a.banks.FinishSignIn(r.Context(), o, tenant, q.BookID, q.ConnectionID)
		}
		out.ConnectionID, err = a.banks.FinishLink(r.Context(), o, tenant, q.BookID, who, a.bankScreen.Provider, q.PublicToken)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *webAPI) bankAccountLink(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BookID        string `json:"book_id"`
		BankAccountID string `json:"bank_account_id"`
		AccountID     string `json:"account_id"`
	}
	if !readBankBody(w, r, &q) {
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	// The same operation an agent prepares for the owner's approval.
	input, _ := json.Marshal(ledger.LinkBankAccountChange{RequestKey: "link-account-" + ledger.ID(), BankAccountID: q.BankAccountID, AccountID: q.AccountID})
	err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		_, err := o.AccountChange(r.Context(), q.BookID, who, "link_bank_account", input)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	a.bankChanged(tenant, q.BookID)
	if q.AccountID != "" {
		// The bank account's activity is now the agent's to work.
		a.activityArrived(r, session, tenant, q.BookID)
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"linked": q.AccountID != ""})
}

// bankConnectionChange disconnects, pauses or resumes a login: the same
// operations an agent prepares for the owner's approval. The sync timer
// then ends the provider's access where that applies.
func (a *webAPI) bankConnectionChange(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BookID       string `json:"book_id"`
		ConnectionID string `json:"connection_id"`
		Reason       string `json:"reason"`
	}
	if !readBankBody(w, r, &q) {
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	key := "bank-" + ledger.ID()
	var operation string
	var args any
	switch r.URL.Path {
	case bankDisconnectPath:
		if q.Reason == "" {
			q.Reason = "Disconnected in Vult."
		}
		operation, args = "disconnect_bank", ledger.DisconnectBank{RequestKey: key, ConnectionID: q.ConnectionID, Reason: q.Reason}
	case bankPausePath:
		if q.Reason == "" {
			q.Reason = "Paused in Vult."
		}
		operation, args = "pause_bank_connection", ledger.PauseBankConnection{RequestKey: key, ConnectionID: q.ConnectionID, Reason: q.Reason}
	default:
		operation, args = "resume_bank_connection", ledger.ResumeBankConnection{RequestKey: key, ConnectionID: q.ConnectionID}
	}
	input, _ := json.Marshal(args)
	var result json.RawMessage
	err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		result, err = o.AccountChange(r.Context(), q.BookID, who, operation, input)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	a.banks.Wake()
	_, _ = w.Write(result)
}

// entryOrigin says why an entry is in the Book: the bank activity it
// records and the reason given, as get_entry_origin does over MCP.
func (a *webAPI) entryOrigin(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	book, entry := q.Get("book_id"), q.Get("entry_id")
	if len(q) != 2 || !ledger.ValidID(entry) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, ok := a.bankBook(w, r, session, book)
	if !ok {
		return
	}
	var out ledger.EntryOrigin
	err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		out, err = o.EntryOrigin(r.Context(), book, entry)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

// bankSync asks for a sync now, as when the owner opens the app. It needs no
// fresh sign-in: it reads from the bank and records nothing in the Book.
func (a *webAPI) bankSync(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BookID string `json:"book_id"`
	}
	if !readBankBody(w, r, &q) {
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		return o.RequestBankSync(r.Context(), q.BookID)
	})
	if err == nil && a.bankOperator != nil {
		err = a.bankOperator.RequestSync(r.Context(), tenant, q.BookID)
	}
	if bankAnswer(w, err) {
		return
	}
	a.banks.Wake()
	_ = json.NewEncoder(w).Encode(map[string]bool{"requested": true})
}

func (a *webAPI) bankChanged(tenant, book string) {
	if a.banks != nil && a.banks.Changed != nil {
		a.banks.Changed(tenant, book)
	}
}

// bankAccountsLinkAll links every bank account a new login brought at once,
// creating the Book accounts the owner accepted, then syncs: one
// confirmation for all of them, and all or nothing.
func (a *webAPI) bankAccountsLinkAll(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BookID string                         `json:"book_id"`
		Links  []ledger.BankAccountLinkChoice `json:"links"`
	}
	if !readBankBody(w, r, &q) {
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	var out []ledger.BankAccountLinked
	err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		out, err = o.LinkBankAccounts(r.Context(), q.BookID, who, q.Links)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	a.bankChanged(tenant, q.BookID)
	a.activityArrived(r, session, tenant, q.BookID)
	sync := a.banks != nil
	if sync {
		a.banks.Wake()
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"links": out, "sync_started": sync})
}

// BankConnector is one way the owner can connect banks from this Vult: the
// provider, what it connects, the route Vult takes, and how it starts.
type BankConnector struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Kinds []string `json:"kinds"`
	// Route is vult (Vult reads the bank directly) or partner (through a
	// bank connection provider).
	Route string `json:"route"`
	// Start is screen (the provider's own screen) or token (a read-only
	// token the owner pastes).
	Start string `json:"start"`
	Ready bool   `json:"ready"`
	// SetupReason says why it isn't ready, when it isn't.
	SetupReason string `json:"setup_reason,omitempty"`
	// Script is the same-origin script that opens a screen connector's
	// screen.
	Script string `json:"script,omitempty"`
}

// offerMercury says whether Vult offers its own Mercury connection. It's off:
// Mercury connects like any other bank. Connections already made
// with a Mercury token keep syncing and can be disconnected; the connector
// stays for when it's offered again.
const offerMercury = false

// bankConnectors lists the ways this Vult connects banks: Vult's own
// connector first, then the provider's screen, where this Vult has one. A
// Vult with an operator never asks for a token. Without either, the list is
// empty and Books bring activity in by upload and through agents.
func (a *webAPI) bankConnectors(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	book := q.Get("book_id")
	if len(q) != 1 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	if _, ok := a.bankBook(w, r, session, book); !ok {
		return
	}
	out := []BankConnector{}
	if _, direct := a.banks.Connectors["mercury"].(bankconnect.TokenConnector); direct && a.bankOperator == nil && offerMercury {
		out = append(out, BankConnector{ID: "mercury", Name: "Mercury", Kinds: []string{"bank", "card"}, Route: "vult", Start: "token", Ready: true})
	}
	if screen := a.bankScreen; screen != nil {
		out = append(out, BankConnector{ID: screen.Provider, Name: screen.Name, Kinds: []string{"bank", "card"}, Route: "partner", Start: "screen", Ready: true, Script: screen.Script})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"connectors": out})
}

// bankConnectToken connects a bank the owner reads with their own
// read-only token, such as Mercury. Only a local or self-hosted Vult takes
// a token; a hosted one connects through the provider's own screen.
func (a *webAPI) bankConnectToken(w http.ResponseWriter, r *http.Request, session string) {
	if a.bankOperator != nil {
		failure(w, http.StatusNotFound)
		return
	}
	var q struct {
		BookID   string `json:"book_id"`
		Provider string `json:"provider"`
		Token    string `json:"token"`
	}
	if !readBankBody(w, r, &q) {
		return
	}
	if q.Provider == "mercury" && !offerMercury {
		bankAnswer(w, &ledger.RefusedError{Reason: "Vult doesn't take a Mercury token. Bring Mercury's activity in like any other bank's."})
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	var connection string
	err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		connection, err = a.banks.ConnectToken(r.Context(), o, tenant, q.BookID, who, q.Provider, q.Token)
		return err
	})
	name := map[string]string{"mercury": "Mercury"}[q.Provider]
	switch {
	case errors.Is(err, bankconnect.ErrCredentials), errors.Is(err, bankconnect.ErrReconnect):
		err = &ledger.RefusedError{Reason: name + " didn't accept this token. Paste a read-only token from " + name + "'s settings."}
	case errors.Is(err, bankconnect.ErrUnsupported):
		err = &ledger.RefusedError{Reason: "This token can't see a checking, savings or credit account at " + name + "."}
	case errors.Is(err, bankconnect.ErrProvider):
		err = &ledger.RefusedError{Reason: name + " didn't answer. Try again in a moment."}
	}
	if bankAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"connection_id": connection})
}

// pickerBank is one bank on the owner's bank picker.
type pickerBank struct {
	ID             string   `json:"id,omitempty"`
	Name           string   `json:"name"`
	Logo           string   `json:"logo,omitempty"` // a data: URL
	Color          string   `json:"color,omitempty"`
	URL            string   `json:"url,omitempty"`
	RoutingNumbers []string `json:"routing_numbers,omitempty"`
	Connector      string   `json:"connector"` // the connector's id
}

func pickerBanks(list []bankconnect.Institution, max int, connector string) []pickerBank {
	out := []pickerBank{}
	for _, i := range list {
		if len(out) == max {
			break
		}
		b := pickerBank{ID: i.ID, Name: i.Name, Color: i.PrimaryColor, URL: i.URL, RoutingNumbers: i.RoutingNumbers, Connector: connector}
		if i.Logo != "" {
			b.Logo = "data:image/png;base64," + i.Logo
		}
		out = append(out, b)
	}
	return out
}

const pickerSearchMax = 10

// bankInstitutions serves the owner's bank picker: popular banks in a fixed
// order, or banks found by name, with logos and colors from the screen
// provider's directory. Without a screen provider it refuses. When the
// directory doesn't answer, the popular names still let the owner pick.
// Mercury connects directly with the owner's token where that is offered, so
// it's listed with its own connector.
func (a *webAPI) bankInstitutions(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	book := q.Get("book_id")
	popular := r.URL.Path == bankPopularPath
	// The screen may name countries; otherwise they come from the Vult's
	// Books.
	var asked []string
	extra := 0
	for _, name := range []string{"country", "country_codes"} {
		if list, ok := q[name]; ok {
			var valid bool
			if asked, valid = parseCountries(strings.Join(list, ",")); !valid || len(list) != 1 || extra != 0 || name == "country" && len(asked) != 1 {
				apiFailure(w, ledger.ErrInvalid)
				return
			}
			extra = 1
		}
	}
	if popular && len(q) != 1+extra || !popular && (len(q) != 2+extra || len(q["q"]) != 1) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, ok := a.bankBook(w, r, session, book)
	if !ok {
		return
	}
	if a.bankScreen == nil {
		bankAnswer(w, errNoBankService)
		return
	}
	provider := a.bankScreen.Provider
	out := map[string]any{"schema_version": 1}
	var list []bankconnect.Institution
	countries := asked
	err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		if countries == nil {
			if countries, err = vultCountries(r.Context(), o); err != nil {
				return err
			}
		}
		if a.bankOperator != nil {
			return nil
		}
		if popular {
			list, err = a.banks.Institutions(r.Context(), provider, "", countries)
		} else if strings.TrimSpace(q.Get("q")) != "" {
			list, err = a.banks.Institutions(r.Context(), provider, q.Get("q"), countries)
		}
		return err
	})
	// The countries searched, and every country the provider says it
	// serves, for the screen's country switch. A country the provider
	// doesn't serve for this Vult refuses on its screen.
	out["countries"] = countries
	if offer := a.requestsOffer(); offer != nil {
		out["requests"] = offer
	}
	supported := a.banks.Countries(provider)
	if supported == nil {
		supported = []string{}
	}
	out["supported_countries"] = supported
	switch {
	case err != nil && !popular:
		if bankAnswer(w, err) {
			return
		}
	case err != nil:
		// The directory didn't answer: the names still let the owner pick.
		list = nil
	}
	banks := []pickerBank{}
	if popular {
		if list == nil {
			for _, name := range bankconnect.PopularInstitutions {
				list = append(list, bankconnect.Institution{Name: name})
			}
		}
		if _, direct := a.banks.Connectors["mercury"].(bankconnect.TokenConnector); direct && a.bankOperator == nil && offerMercury {
			banks = append(banks, pickerBank{ID: "mercury", Name: "Mercury", Connector: "mercury", URL: "https://mercury.com"})
		}
		banks = append(banks, pickerBanks(list, len(bankconnect.PopularInstitutions), provider)...)
	} else {
		banks = pickerBanks(list, pickerSearchMax, provider)
	}
	out["institutions"] = banks
	_ = json.NewEncoder(w).Encode(out)
}

// parseCountries reads a comma-separated list of two-letter country codes,
// in either case, at most 20, sorted and without repeats; ok is false for
// anything else. Whether a provider serves a country is the provider's to
// say.
func parseCountries(s string) ([]string, bool) {
	seen := map[string]bool{}
	for _, c := range strings.Split(s, ",") {
		c = strings.ToUpper(strings.TrimSpace(c))
		if len(c) != 2 || c[0] < 'A' || c[0] > 'Z' || c[1] < 'A' || c[1] > 'Z' {
			return nil, false
		}
		seen[c] = true
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	slices.Sort(out)
	return out, len(out) <= 20
}

// vultCountries are the countries whose banks the Vult's Books are likely
// to use, from their currencies, since a Book records no country. Empty
// means the connector's own countries.
func vultCountries(ctx context.Context, o ledger.TenantAdmin) ([]string, error) {
	books, err := o.BooksBounded(ctx, 200)
	if err != nil {
		return nil, err
	}
	var currencies []string
	for _, b := range books {
		currencies = append(currencies, b.Currency)
	}
	return bankconnect.CountriesFor(currencies), nil
}
