package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Bank management actions. Each exists once: the owner takes it on the Bank
// screen, and an agent prepares the same operation over MCP for the owner's
// approval. The database lets only the owner, directly or by approving, take
// them; an agent never takes one on its own.

// LinkBankAccountChange links a bank account to a Book account, or unlinks it
// with an empty account_id.
type LinkBankAccountChange struct {
	RequestKey    string `json:"request_key"`
	BankAccountID string `json:"bank_account_id"`
	AccountID     string `json:"account_id"`
}

// DisconnectBank forgets a login's access. Its activity, links and entries
// stay; the server then asks the provider to end the access.
type DisconnectBank struct {
	RequestKey   string `json:"request_key"`
	ConnectionID string `json:"connection_id"`
	Reason       string `json:"reason"`
}

// PauseBankConnection stops syncing a login and keeps every record.
type PauseBankConnection struct {
	RequestKey   string `json:"request_key"`
	ConnectionID string `json:"connection_id"`
	Reason       string `json:"reason"`
}

// ResumeBankConnection syncs a paused login again. A login whose provider
// access was ended, an operator's, resumes by connecting it again instead.
type ResumeBankConnection struct {
	RequestKey   string `json:"request_key"`
	ConnectionID string `json:"connection_id"`
}

// MarkBankDuplicate records that a transaction repeats another of the same
// bank account, with a reason. A repeat is never recorded.
type MarkBankDuplicate struct {
	RequestKey             string `json:"request_key"`
	TransactionID          string `json:"transaction_id"`
	CanonicalTransactionID string `json:"canonical_transaction_id"`
	Reason                 string `json:"reason"`
}

// PrepareBankConnection asks the owner to connect a bank, or to reconnect
// one, in the provider's own screen. It records nothing.
type PrepareBankConnection struct {
	ConnectionID string `json:"connection_id,omitempty"`
}

type BankConnectionHandoff struct {
	Href    string `json:"href"`
	Message string `json:"message"`
}

// BankChange is a management action's recorded outcome.
type BankChange struct {
	Change        string `json:"change"`
	ConnectionID  string `json:"connection_id,omitempty"`
	BankAccountID string `json:"bank_account_id,omitempty"`
	AccountID     string `json:"account_id,omitempty"`
	TransactionID string `json:"transaction_id,omitempty"`
	CanonicalID   string `json:"canonical_transaction_id,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Access        string `json:"access,omitempty"`
	Status        string `json:"status,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

func bankManagementMethod(method string) bool {
	switch method {
	case "link_bank_account", "disconnect_bank", "pause_bank_connection", "resume_bank_connection", "mark_bank_duplicate":
		return true
	}
	return false
}

// BankOwnerDecision reports operations only the owner takes, directly or by
// approving an agent's proposal. Agents prepare them.
func BankOwnerDecision(method string) bool {
	return bankManagementMethod(method) && method != "mark_bank_duplicate"
}

func validReason(s string) bool { return validStatementText(s, 4000) && len(s) > 0 }

func validBankManagement(method string, input []byte) (any, string, error) {
	switch method {
	case "link_bank_account":
		var q LinkBankAccountChange
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.BankAccountID) || q.AccountID != "" && !ValidID(q.AccountID) {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "disconnect_bank":
		var q DisconnectBank
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.ConnectionID) || !validReason(q.Reason) {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "pause_bank_connection":
		var q PauseBankConnection
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.ConnectionID) || !validReason(q.Reason) || len(q.Reason) > 400 {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "resume_bank_connection":
		var q ResumeBankConnection
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.ConnectionID) {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "mark_bank_duplicate":
		var q MarkBankDuplicate
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.TransactionID) || !ValidID(q.CanonicalTransactionID) ||
			q.TransactionID == q.CanonicalTransactionID || !validReason(q.Reason) {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	}
	return nil, "", ErrInvalid
}

// bankOwnerCheck refuses, in words, a management action an agent takes on
// its own. The database refuses it too.
func bankOwnerCheck(ctx context.Context, tx pgx.Tx, s Scope) error {
	var allowed bool
	if err := tx.QueryRow(ctx, "SELECT public.bank_owner_allowed($1,$2)", s.TenantID, s.BookID).Scan(&allowed); err != nil {
		return dbError(err)
	}
	if !allowed {
		return &RefusedError{Reason: "Only the owner takes this action. Prepare it with prepare_operation for the owner's approval."}
	}
	return nil
}

func bankManage(ctx context.Context, tx pgx.Tx, s Scope, q any, receipt string) (BankChange, error) {
	if _, repeat := q.(MarkBankDuplicate); !repeat {
		if err := bankOwnerCheck(ctx, tx, s); err != nil {
			return BankChange{}, err
		}
	}
	switch v := q.(type) {
	case LinkBankAccountChange:
		if err := linkBankAccount(ctx, tx, s, v.BankAccountID, v.AccountID); err != nil {
			return BankChange{}, err
		}
		return BankChange{Change: "link_account", BankAccountID: v.BankAccountID, AccountID: v.AccountID}, nil
	case DisconnectBank:
		out := BankChange{Change: "disconnect", ConnectionID: v.ConnectionID, Reason: v.Reason, Status: "disconnected"}
		// The token stays sealed until the provider confirms the access ended.
		err := tx.QueryRow(ctx, `UPDATE public.bank_connections SET status='disconnected',status_reason=$4,
 disconnected_at=clock_timestamp(),sync_requested_at=NULL WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND disconnected_at IS NULL
 RETURNING provider,access`, s.TenantID, s.BookID, v.ConnectionID, clip(v.Reason, 400)).Scan(&out.Provider, &out.Access)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, ErrNotFound
		}
		return out, err
	case PauseBankConnection:
		out := BankChange{Change: "pause", ConnectionID: v.ConnectionID, Reason: v.Reason, Status: "paused"}
		err := tx.QueryRow(ctx, `UPDATE public.bank_connections SET status='paused',status_reason=$4,sync_requested_at=NULL
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND status IN ('active','needs_attention') RETURNING provider,access`,
			s.TenantID, s.BookID, v.ConnectionID, v.Reason).Scan(&out.Provider, &out.Access)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, &RefusedError{Reason: "Only a connected bank that isn't paused can be paused."}
		}
		return out, err
	case ResumeBankConnection:
		out := BankChange{Change: "resume", ConnectionID: v.ConnectionID, Status: "active"}
		var status string
		if err := tx.QueryRow(ctx, "SELECT status,access,provider FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3",
			s.TenantID, s.BookID, v.ConnectionID).Scan(&status, &out.Access, &out.Provider); err != nil {
			return out, dbError(err)
		}
		if status != "paused" {
			return out, &RefusedError{Reason: "This bank isn't paused."}
		}
		if out.Access == "operator" {
			return out, &RefusedError{Reason: "Reconnect this bank to resume it: prepare_bank_connection gives the owner the link."}
		}
		_, err := tx.Exec(ctx, `UPDATE public.bank_connections SET status='active',status_reason='',sync_requested_at=clock_timestamp()
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3`, s.TenantID, s.BookID, v.ConnectionID)
		return out, err
	case MarkBankDuplicate:
		_, err := tx.Exec(ctx, `INSERT INTO public.bank_transaction_duplicates(tenant_id,book_id,transaction_id,canonical_transaction_id,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, s.TenantID, s.BookID, v.TransactionID, v.CanonicalTransactionID, s.ActorID, v.Reason, receipt)
		return BankChange{Change: "repeat", TransactionID: v.TransactionID, CanonicalID: v.CanonicalTransactionID, Reason: v.Reason}, err
	}
	return BankChange{}, ErrInvalid
}

// linkBankAccount links a bank account to a Book account of its type.
func linkBankAccount(ctx context.Context, tx pgx.Tx, s Scope, bankAccount, account string) error {
	var linkedBy any
	if account != "" {
		linkedBy = s.ActorID
		var kind, class string
		var sameCurrency bool
		err := tx.QueryRow(ctx, `SELECT b.kind,a.class,coalesce(a.currency,k.currency)=b.currency FROM public.bank_accounts b
 JOIN public.accounts a ON a.tenant_id=b.tenant_id AND a.book_id=b.book_id AND a.id=$4
 JOIN public.books k ON k.tenant_id=b.tenant_id AND k.id=b.book_id
 WHERE b.tenant_id=$1 AND b.book_id=$2 AND b.id=$3`, s.TenantID, s.BookID, bankAccount, account).Scan(&kind, &class, &sameCurrency)
		if err != nil {
			return dbError(err)
		}
		if kind == "cash" && class != "asset" || kind == "card" && class != "liability" {
			return &RefusedError{Reason: "A cash account is linked to an asset account and a card account to a liability account."}
		}
		if !sameCurrency {
			return &RefusedError{Reason: "A bank account is linked to a Book account in its own currency."}
		}
	}
	if account != "" {
		// A Book account held by an account of a disconnected login, that no
		// live login took over, is free to link again. The earlier items keep
		// their entries.
		if _, err := tx.Exec(ctx, `UPDATE public.bank_accounts b SET account_id=NULL,linked_by=NULL,linked_at=NULL
 FROM public.bank_connections c WHERE c.tenant_id=b.tenant_id AND c.book_id=b.book_id AND c.id=b.connection_id AND c.disconnected_at IS NOT NULL
 AND b.tenant_id=$1 AND b.book_id=$2 AND b.account_id=$3 AND b.id<>$4
 AND NOT EXISTS(SELECT 1 FROM public.bank_account_aliases x JOIN public.bank_connections xc ON xc.tenant_id=x.tenant_id AND xc.book_id=x.book_id AND xc.id=x.connection_id
   WHERE x.bank_account_id=b.id AND xc.disconnected_at IS NULL)`, s.TenantID, s.BookID, account, bankAccount); err != nil {
			return dbError(err)
		}
		// Linking the bank's new account to the Book account of the account
		// it takes over confirms the takeover: the old account keeps that
		// Book account for its own history, and the two share it.
		if _, err := tx.Exec(ctx, `UPDATE public.bank_accounts o SET replaced_by=n.id FROM public.bank_accounts n
 WHERE n.tenant_id=o.tenant_id AND n.book_id=o.book_id AND n.id=$4 AND n.replaces=o.id
 AND o.tenant_id=$1 AND o.book_id=$2 AND o.account_id=$3 AND o.replaced_by IS NULL`, s.TenantID, s.BookID, account, bankAccount); err != nil {
			return dbError(err)
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE public.bank_accounts SET account_id=nullif($4,'')::uuid,linked_by=$5::uuid,
 linked_at=CASE WHEN $4='' THEN NULL ELSE clock_timestamp() END WHERE tenant_id=$1 AND book_id=$2 AND id=$3`,
		s.TenantID, s.BookID, bankAccount, account, linkedBy)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func bankManagementPreview(book Book, operation string, effect json.RawMessage, sources []SourceInput) (OperationPreview, error) {
	var out BankChange
	if json.Unmarshal(effect, &out) != nil || out.Change == "" {
		return OperationPreview{}, ErrInvalid
	}
	p := emptyOperationPreview(book, sources)
	p.BankChange = &OperationBankChange{Change: out.Change, ConnectionID: out.ConnectionID, BankAccountID: out.BankAccountID, AccountID: out.AccountID, Reason: out.Reason}
	if out.TransactionID != "" {
		p.BankChange.TransactionIDs = []string{out.TransactionID, out.CanonicalID}
	}
	p.Description = map[string]string{"link_account": "Link a bank account to a Book account", "disconnect": "Disconnect a bank",
		"pause": "Pause a bank's sync", "resume": "Resume a bank's sync", "repeat": "Mark bank activity as a repeat"}[out.Change]
	if out.Change == "link_account" && out.AccountID == "" {
		p.Description = "Unlink a bank account from the Book"
	}
	return p, nil
}

// prepareBankConnection hands the owner a link to the Bank screen, where
// they finish connecting or reconnecting in the provider's own screen.
func prepareBankConnection(ctx context.Context, tx pgx.Tx, s Scope, q PrepareBankConnection) (BankConnectionHandoff, error) {
	href := "/bank?book_id=" + s.BookID + "&connect=1"
	message := "Open Vult to connect a bank. You choose the bank and sign in on the provider's own screen; Vult never sees your bank password."
	if q.ConnectionID != "" {
		var name, status string
		if err := tx.QueryRow(ctx, "SELECT institution_name,status FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3",
			s.TenantID, s.BookID, q.ConnectionID).Scan(&name, &status); err != nil {
			return BankConnectionHandoff{}, dbError(err)
		}
		if status == "disconnected" {
			return BankConnectionHandoff{}, &RefusedError{Reason: "This bank is disconnected. Connect it again as a new bank."}
		}
		if name == "" {
			name = "this bank"
		}
		href += "&connection=" + q.ConnectionID
		message = "Open Vult to reconnect " + name + ". Its accounts and recorded activity stay as they are."
	}
	return BankConnectionHandoff{Href: href, Message: message}, nil
}

// EntryOrigin says why an entry is in the Book: the bank activity it
// records, and the reason given when it was recorded or approved.
type EntryOrigin struct {
	EntryID     string            `json:"entry_id"`
	Description string            `json:"description"`
	ActorID     string            `json:"actor_id"`
	RecordedAt  time.Time         `json:"recorded_at"`
	Bank        []EntryBankOrigin `json:"bank"`
	Proposal    *EntryProposal    `json:"proposal,omitempty"`
	ReversalOf  string            `json:"reversal_of,omitempty"`
	ReversedBy  string            `json:"reversed_by,omitempty"`
	// Pair is the other entry when this one is half of a pair: a business
	// cost paid personally, recorded in both linked Books together.
	Pair *EntryPair `json:"pair,omitempty"`
}

type EntryBankOrigin struct {
	TransactionID string    `json:"transaction_id"`
	Date          string    `json:"date"`
	AmountMinor   string    `json:"amount_minor"`
	Currency      string    `json:"currency"`
	Scale         int       `json:"scale"`
	Description   string    `json:"description"`
	Counterparty  string    `json:"counterparty,omitempty"`
	AccountName   string    `json:"account_name"`
	Basis         string    `json:"basis"`
	Reason        string    `json:"reason,omitempty"`
	RuleID        string    `json:"rule_id,omitempty"`
	LinkedBy      string    `json:"linked_by"`
	LinkedAt      time.Time `json:"linked_at"`
}

// EntryProposal is the agent's stated reasoning when the entry was approved
// from its proposal.
type EntryProposal struct {
	ProposalID    string   `json:"proposal_id"`
	Assumptions   []string `json:"assumptions"`
	OpenQuestions []string `json:"open_questions"`
	DecisionBy    string   `json:"decision_by,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

func readEntryOrigin(ctx context.Context, tx pgx.Tx, s Scope, entry string) (EntryOrigin, error) {
	e, err := readEntry(ctx, tx, s, entry)
	if err != nil {
		return EntryOrigin{}, dbError(err)
	}
	out := EntryOrigin{EntryID: e.ID, Description: e.Description, ActorID: e.ActorID, RecordedAt: e.RecordedAt, ReversalOf: e.ReversalOf, Bank: []EntryBankOrigin{}}
	if out.Pair, err = readEntryPair(ctx, tx, s, e.ID); err != nil {
		return EntryOrigin{}, err
	}
	if err := tx.QueryRow(ctx, "SELECT coalesce((SELECT id::text FROM public.entries WHERE tenant_id=$1 AND book_id=$2 AND reversal_of=$3 LIMIT 1),'')",
		s.TenantID, s.BookID, entry).Scan(&out.ReversedBy); err != nil {
		return out, dbError(err)
	}
	rows, err := tx.Query(ctx, `SELECT t.id::text,t.occurred_on::text,t.amount_minor::text,t.currency,t.scale,t.description,t.counterparty,
 coalesce(nullif(a.name,''),ac.name,'Account')||CASE WHEN a.mask<>'' THEN ' ••'||a.mask ELSE '' END,l.basis,l.reason,coalesce(l.rule_id::text,''),l.actor_id::text,l.recorded_at
 FROM public.bank_transaction_links l JOIN public.bank_transactions t ON t.tenant_id=l.tenant_id AND t.book_id=l.book_id AND t.id=l.transaction_id
 LEFT JOIN public.bank_accounts a ON a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=t.bank_account_id
 LEFT JOIN public.accounts ac ON ac.tenant_id=l.tenant_id AND ac.book_id=l.book_id AND ac.id=l.account_id
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=$3 AND l.revoked_at IS NULL ORDER BY t.occurred_on,t.id`, s.TenantID, s.BookID, entry)
	if err != nil {
		return out, dbError(err)
	}
	for rows.Next() {
		var b EntryBankOrigin
		if err := rows.Scan(&b.TransactionID, &b.Date, &b.AmountMinor, &b.Currency, &b.Scale, &b.Description, &b.Counterparty, &b.AccountName,
			&b.Basis, &b.Reason, &b.RuleID, &b.LinkedBy, &b.LinkedAt); err != nil {
			rows.Close()
			return out, dbError(err)
		}
		out.Bank = append(out.Bank, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, dbError(err)
	}
	// The proposal whose approval posted it: its receipt is the entry's own
	// receipt, or the bank request the entry's receipt is keyed after.
	var id, decisionBy, reason string
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT p.id::text,p.payload_bytes,coalesce(d.owner_id::text,''),coalesce(d.reason,'')
 FROM public.requests r JOIN public.operation_proposals p ON p.tenant_id=r.tenant_id AND p.book_id=r.book_id AND p.actor_id=r.actor_id
   AND (p.operation_key=r.request_key OR p.operation_key||'/entry'=r.request_key OR p.operation_key||'/transfer'=r.request_key)
 JOIN public.operation_consumptions c ON c.tenant_id=p.tenant_id AND c.book_id=p.book_id AND c.proposal_id=p.id
 LEFT JOIN LATERAL (SELECT owner_id,reason FROM public.operation_decisions x WHERE x.tenant_id=p.tenant_id AND x.book_id=p.book_id
   AND x.proposal_id=p.id ORDER BY revision DESC LIMIT 1) d ON true
 WHERE r.tenant_id=$1 AND r.book_id=$2 AND r.id=$3 LIMIT 1`, s.TenantID, s.BookID, e.ReceiptID).Scan(&id, &payload, &decisionBy, &reason)
	if err == nil {
		var p operationPayload
		if json.Unmarshal(payload, &p) == nil {
			out.Proposal = &EntryProposal{ProposalID: id, Assumptions: p.Assumptions, OpenQuestions: p.OpenQuestions, DecisionBy: decisionBy, Reason: reason}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, dbError(err)
	}
	return out, nil
}

// EntryOrigin reads, for the owner, why an entry is in the Book.
func (a TenantAdmin) EntryOrigin(ctx context.Context, book, entry string) (EntryOrigin, error) {
	var out EntryOrigin
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readEntryOrigin(ctx, tx, s, entry)
		return err
	})
	return out, err
}

// PauseBankConnection pauses an operator's login on the operator's own
// schedule, such as after a long time away, with no owner signed in. It sets
// the same status and reason an owner's pause does; the operator then ends
// the provider's access itself.
func (s Store) PauseBankConnection(ctx context.Context, t BankSyncTarget, reason string) error {
	if !validReason(reason) || len(reason) > 400 {
		return ErrInvalid
	}
	return s.withBankSync(ctx, t, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE public.bank_connections SET status='paused',status_reason=$4,sync_requested_at=NULL
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND access='operator' AND status IN ('active','needs_attention')`, t.TenantID, t.BookID, t.ConnectionID, reason)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return bankError(err)
	})
}

// BankRelease is a login whose provider access must still end.
type BankRelease struct {
	BankSyncTarget
	Access string
	Status string
}

// BankConnectionsToRelease lists logins whose provider access must still
// end: disconnected ones and an operator's paused ones.
func (s Store) BankConnectionsToRelease(ctx context.Context, limit int) ([]BankRelease, error) {
	if s.Runtime == nil {
		return nil, ErrDenied
	}
	rows, err := s.Runtime.Query(ctx, "SELECT tenant_id::text,book_id::text,id::text,access,status FROM public.bank_connections_to_release($1)", limit)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var out []BankRelease
	for rows.Next() {
		var v BankRelease
		if err := rows.Scan(&v.TenantID, &v.BookID, &v.ConnectionID, &v.Access, &v.Status); err != nil {
			return nil, dbError(err)
		}
		out = append(out, v)
	}
	return out, dbError(rows.Err())
}

// BankAccessEnded records that the provider ended a login's access, and
// forgets its token.
func (s Store) BankAccessEnded(ctx context.Context, t BankSyncTarget) error {
	return s.withBankSync(ctx, t, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE public.bank_connections SET access_ended_at=clock_timestamp(),token_sealed=NULL,key_version=NULL
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND access_ended_at IS NULL`, t.TenantID, t.BookID, t.ConnectionID)
		return bankError(err)
	})
}

// previewBankManagement describes an owner-only bank action for review
// without taking it: the agent preparing it can't, and the owner's approval
// takes it later. The preview names the same change taking it records.
func previewBankManagement(ctx context.Context, tx pgx.Tx, s Scope, book Book, operation string, args []byte) (OperationPreview, error) {
	q, _, err := validBankManagement(operation, args)
	if err != nil {
		return OperationPreview{}, err
	}
	var change BankChange
	var exists bool
	switch v := q.(type) {
	case LinkBankAccountChange:
		change = BankChange{Change: "link_account", BankAccountID: v.BankAccountID, AccountID: v.AccountID}
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.bank_accounts WHERE tenant_id=$1 AND book_id=$2 AND id=$3)", s.TenantID, s.BookID, v.BankAccountID).Scan(&exists)
	case DisconnectBank:
		change = BankChange{Change: "disconnect", ConnectionID: v.ConnectionID, Reason: v.Reason}
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND disconnected_at IS NULL)", s.TenantID, s.BookID, v.ConnectionID).Scan(&exists)
	case PauseBankConnection:
		change = BankChange{Change: "pause", ConnectionID: v.ConnectionID, Reason: v.Reason}
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND status IN ('active','needs_attention'))", s.TenantID, s.BookID, v.ConnectionID).Scan(&exists)
	case ResumeBankConnection:
		change = BankChange{Change: "resume", ConnectionID: v.ConnectionID}
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND status='paused' AND access='book')", s.TenantID, s.BookID, v.ConnectionID).Scan(&exists)
	default:
		return OperationPreview{}, ErrInvalid
	}
	if err != nil {
		return OperationPreview{}, dbError(err)
	}
	if !exists {
		return OperationPreview{}, &RefusedError{Reason: "That bank or bank account isn't in a state for this change."}
	}
	return bankManagementPreview(book, operation, raw(change), []SourceInput{})
}
