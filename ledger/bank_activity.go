package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Bank activity is what a bank connection reports, kept apart from the
// ledger. Each transaction stays unrecorded until an entry that records it is
// linked to it, and it is linked at most once. Nothing here posts on its own:
// an agent, the owner, or the owner's approval of a proposal records it.
//
// A transaction's amount is signed the way the linked Book account's balance
// moves, as on a statement: a deposit is positive on a cash (asset) account
// and a charge is positive on a card (liability) account.

type BankActivityQuery struct {
	// Status is unrecorded (the default), recorded, pending, removed or all.
	Status        string `json:"status,omitempty"`
	BankAccountID string `json:"bank_account_id,omitempty"`
	// After continues a page from its next value.
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
	// firstOnly leaves the summary to the first page, which is the only one
	// that shows it; it is counted over all of a Book's activity.
	firstOnly bool
}

type BankLink struct {
	ID         string    `json:"id"`
	EntryID    string    `json:"entry_id"`
	ActorID    string    `json:"actor_id"`
	RecordedAt time.Time `json:"recorded_at"`
	Basis      string    `json:"basis"`
	Reason     string    `json:"reason,omitempty"`
	// OtherAccountName is the entry's other account when it has just two
	// lines, the shape a rule repeats.
	OtherAccountName string `json:"other_account_name,omitempty"`
}

type BankTransaction struct {
	ID            string `json:"id"`
	BankAccountID string `json:"bank_account_id"`
	// AccountID is the Book account the bank account is linked to, if any.
	AccountID string `json:"account_id,omitempty"`
	// BankType (checking, savings or credit card) and SuggestedAccountID are
	// given for activity on a bank account not yet linked: the Book account
	// it would be linked to, chosen by role and code, never by name.
	BankType           string    `json:"bank_type,omitempty"`
	SuggestedAccountID string    `json:"suggested_account_id,omitempty"`
	AccountClass       string    `json:"account_class,omitempty"`
	AccountName        string    `json:"account_name"`
	Institution        string    `json:"institution,omitempty"`
	Date               string    `json:"date"`
	AuthorizedDate     string    `json:"authorized_date,omitempty"`
	AmountMinor        string    `json:"amount_minor"`
	Currency           string    `json:"currency"`
	Scale              int       `json:"scale"`
	Description        string    `json:"description"`
	Counterparty       string    `json:"counterparty,omitempty"`
	Status             string    `json:"status"`
	Link               *BankLink `json:"link,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
	// PossibleRepeatOf names an item this one may repeat, after a login
	// reconnected; mark_bank_duplicate settles it.
	PossibleRepeatOf string `json:"possible_repeat_of,omitempty"`
	// Source is bank or statement: where the item first came from. A
	// statement line, with its statement and document, may also be attached
	// to an item from the bank, and the other way round.
	Source         string `json:"source"`
	StatementRowID string `json:"statement_row_id,omitempty"`
	StatementID    string `json:"statement_id,omitempty"`
	DocumentID     string `json:"document_id,omitempty"`
	// KindKey names the item's kind of activity, as list_agent_kinds and
	// ask_question use it: "<Book account>,<in|out>,<counterparty key>".
	// The ledger forms it; agents use it as it is.
	KindKey string `json:"kind_key,omitempty"`
	// SuggestedBeneficiaries are the linked Books this Book pays for that
	// used an account for the same counterparty before, for unrecorded money
	// paid out: where a pair for this item would likely go.
	SuggestedBeneficiaries []SuggestedBeneficiary `json:"suggested_beneficiaries,omitempty"`
	// Project is the project a recorded item's spending went to, when it
	// all went to one. SuggestedProject is, for an unrecorded item, the
	// project a rule or the counterparty's past labels point to.
	Project          *ProjectRef        `json:"project,omitempty"`
	SuggestedProject *ProjectSuggestion `json:"suggested_project,omitempty"`
	// RecordedBefore are, for an unrecorded item, the accounts this Book's
	// recorded items of the same kind went to: the same account, direction
	// and counterparty. Most used first, three at most.
	RecordedBefore []RecordedBefore `json:"recorded_before,omitempty"`
}

// RecordedBefore is an account earlier items of a kind were recorded to,
// and how many of them.
type RecordedBefore struct {
	AccountID string `json:"account_id"`
	Times     int    `json:"times"`
	// LargestMinor is the largest of those items, unsigned, so an item far
	// larger than its kind has been can be noticed.
	LargestMinor string `json:"largest_minor"`
	// LastDate is when the latest of those items happened.
	LastDate string `json:"last_date"`
}

// ProjectRef names a project.
type ProjectRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProjectSuggestion is a project an item likely belongs to, and why: a
// rule for its counterparty ("rule") or the counterparty's past labels
// ("history"), with the account those used.
type ProjectSuggestion struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AccountID string `json:"account_id,omitempty"`
	Why       string `json:"why"`
}

// SuggestedBeneficiary is a linked Book and the account it used before for
// the item's counterparty, with the reason in words.
type SuggestedBeneficiary struct {
	BookID    string `json:"book_id"`
	AccountID string `json:"account_id"`
	Why       string `json:"why"`
}

type BankActivitySummary struct {
	Unrecorded int64 `json:"unrecorded"`
	Pending    int64 `json:"pending"`
	// NeedsAccount counts posted activity on bank accounts not yet linked to a
	// Book account.
	NeedsAccount int64 `json:"needs_account"`
	// RecordedChanged counts recorded activity removed or changed by the
	// bank after it was recorded, for a person to review.
	RecordedChanged int64      `json:"recorded_changed"`
	LastSyncedAt    *time.Time `json:"last_synced_at,omitempty"`
}

type BankActivityPage struct {
	Items   []BankTransaction   `json:"items"`
	Next    string              `json:"next,omitempty"`
	Summary BankActivitySummary `json:"summary"`
}

type LinkBankActivity struct {
	RequestKey    string `json:"request_key"`
	TransactionID string `json:"transaction_id"`
	EntryID       string `json:"entry_id"`
	// Basis is judgement (the default) or existing_entry when accepting
	// Vult's match; Reason says why, in the recorder's words.
	Basis  string `json:"basis,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type UnlinkBankActivity struct {
	RequestKey string `json:"request_key"`
	LinkID     string `json:"link_id"`
	Reason     string `json:"reason"`
}

// RecordBankActivity posts a journal for one bank transaction and links it,
// in one request. Its lines must move the bank account's Book account by the
// transaction amount. Dates default to the transaction's date.
type RecordBankActivity struct {
	RequestKey     string `json:"request_key"`
	TransactionID  string `json:"transaction_id"`
	EconomicDate   string `json:"economic_date,omitempty"`
	AccountingDate string `json:"accounting_date,omitempty"`
	Description    string `json:"description"`
	// OtherAccountID records the whole amount against one account: Vult
	// writes the two lines, sides following the accounts' classes. Give
	// Lines instead to split it.
	OtherAccountID string        `json:"other_account_id,omitempty"`
	Lines          []PostLine    `json:"lines,omitempty"`
	Sources        []SourceInput `json:"sources,omitempty"`
	// Reason says why the entry records the transaction this way.
	Reason string `json:"reason,omitempty"`
	// PairKey makes this the first half of a pair: the Book that paid
	// records its bank item as money spent for a linked Book. Prepared,
	// never direct.
	PairKey string `json:"pair_key,omitempty"`
	// AssignedBy says how the agent chose the Book a pair is for: an owner's
	// rule, a vendor only that Book pays, the model, or the owner's answer.
	AssignedBy string `json:"assigned_by,omitempty"`
	// Settlement makes the pair a settlement: money one linked Book pays
	// the other for what it owes, each half recording its own bank item to
	// its owed account. Repays names the cost pairs it repays, by key.
	Settlement bool    `json:"settlement,omitempty"`
	Repays     []Repay `json:"repays,omitempty"`
	// ProjectID labels the entry's one spending line for a project, in
	// full. ProjectSplits labels parts of it instead, and NewProjectName
	// names a project that doesn't exist yet. At most one of the three; a
	// label never changes an account or an amount.
	ProjectID      string         `json:"project_id,omitempty"`
	ProjectSplits  []ProjectSplit `json:"project_splits,omitempty"`
	NewProjectName string         `json:"new_project_name,omitempty"`
}

// labelled reports whether the request gives the entry a project.
func (q RecordBankActivity) labelled() bool {
	return q.ProjectID != "" || len(q.ProjectSplits) > 0 || q.NewProjectName != ""
}

// validProjectArguments checks the project a bank item is recorded for.
func validProjectArguments(q *RecordBankActivity, input []byte) error {
	var given struct {
		ProjectID      json.RawMessage `json:"project_id"`
		ProjectSplits  json.RawMessage `json:"project_splits"`
		NewProjectName json.RawMessage `json:"new_project_name"`
	}
	_ = json.Unmarshal(input, &given)
	has := func(v json.RawMessage) bool { return len(v) > 0 && string(v) != "null" }
	count := 0
	for _, v := range []json.RawMessage{given.ProjectID, given.ProjectSplits, given.NewProjectName} {
		if has(v) {
			count++
		}
	}
	if count == 0 {
		return nil
	}
	if count > 1 {
		return &RefusedError{Reason: "Give one of project_id, project_splits or new_project_name, not several."}
	}
	switch {
	case has(given.ProjectID):
		if !ValidID(q.ProjectID) {
			return &RefusedError{Reason: "project_id is a project's ID from list_projects."}
		}
	case has(given.NewProjectName):
		q.NewProjectName = strings.TrimSpace(q.NewProjectName)
		if !validProjectName(q.NewProjectName) {
			return &RefusedError{Reason: "new_project_name is the project's name, up to 120 characters."}
		}
	default:
		if len(q.ProjectSplits) == 0 || len(q.ProjectSplits) > 50 {
			return &RefusedError{Reason: "project_splits takes 1 to 50 parts of {project_id, amount_minor}."}
		}
		for _, sp := range q.ProjectSplits {
			if sp.LineID != "" {
				return &RefusedError{Reason: "project_splits doesn't take line_id here: the parts are of the entry's one spending line."}
			}
			if !ValidID(sp.ProjectID) || sp.AmountMinor != "" && !amountPattern.MatchString(sp.AmountMinor) {
				return &RefusedError{Reason: "Each of project_splits is {project_id, amount_minor}: a project's ID from list_projects and its part as a string of digits."}
			}
		}
	}
	if q.PairKey != "" {
		return &RefusedError{Reason: "A cost paid for a linked Book isn't this Book's spending, so it takes no project here."}
	}
	return nil
}

// Repay is a cost a repayment covers: by its pair key alone, what is left
// of it, or with AmountMinor, that part of it. In JSON it is the key, or
// {"pair_key", "amount_minor"}.
type Repay struct {
	PairKey     string `json:"pair_key"`
	AmountMinor string `json:"amount_minor,omitempty"`
}

func (r *Repay) UnmarshalJSON(data []byte) error {
	var key string
	if json.Unmarshal(data, &key) == nil {
		*r = Repay{PairKey: key}
		return nil
	}
	type plain Repay
	var v plain
	if strictjson.Decode(data, &v) != nil || v.PairKey == "" {
		return ErrInvalid
	}
	*r = Repay(v)
	return nil
}

func (r Repay) MarshalJSON() ([]byte, error) {
	if r.AmountMinor == "" {
		return json.Marshal(r.PairKey)
	}
	type plain Repay
	return json.Marshal(plain(r))
}

// validSettlement reports whether settlement arguments are well formed:
// only on a pair, and repays only on a settlement, each a pair key, once.
func validSettlement(settlement bool, repays []Repay, key string) bool {
	if settlement && key == "" || !settlement && repays != nil || len(repays) > 50 {
		return false
	}
	seen := map[string]bool{}
	for _, r := range repays {
		if !validPairKey(r.PairKey) || seen[r.PairKey] || r.AmountMinor != "" && (!amountPattern.MatchString(r.AmountMinor) || len(r.AmountMinor) > 18) {
			return false
		}
		seen[r.PairKey] = true
	}
	return true
}

// validAssignedBy reports whether a pair's assigned_by is one of its values,
// given only with a pair key.
func validAssignedBy(by, key string) bool {
	switch by {
	case "":
		return true
	case "rule", "known_vendor", "model", "answer":
		return key != ""
	}
	return false
}

// RecordBankTransfer records two transactions on the owner's own accounts as
// one transfer: money out of one and the same money into the other.
type RecordBankTransfer struct {
	RequestKey     string   `json:"request_key"`
	TransactionIDs []string `json:"transaction_ids"`
	AccountingDate string   `json:"accounting_date,omitempty"`
	Description    string   `json:"description,omitempty"`
	Reason         string   `json:"reason,omitempty"`
}

type BankLinkResult struct {
	Links []BankLinkRecord `json:"links"`
	Entry *Entry           `json:"entry,omitempty"`
	// MovedWithinAccount is set when a transfer's two items share a Book
	// account: they are recorded together, with no entry.
	MovedWithinAccount bool `json:"moved_within_account,omitempty"`
	// Projects are the projects the entry's spending was labelled for.
	Projects []EntryProject `json:"projects,omitempty"`
}

type BankLinkRecord struct {
	ID            string `json:"id"`
	TransactionID string `json:"transaction_id"`
	EntryID       string `json:"entry_id"`
	Revoked       bool   `json:"revoked,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// BankSuggestion is a deterministic match, recorded nowhere. An existing
// entry suggestion found exactly one unlinked entry moving the account by the
// amount within a few days; a transfer suggestion found exactly one opposite
// movement of the same money on another of the owner's accounts.
type BankSuggestion struct {
	Kind           string   `json:"kind"`
	TransactionIDs []string `json:"transaction_ids"`
	EntryID        string   `json:"entry_id,omitempty"`
	AmountMinor    string   `json:"amount_minor"`
	Reason         string   `json:"reason"`
}

const (
	bankDefaultPage = 50
	bankMaxPage     = 200
	// bankMatchDays is how far apart an entry's date and a transaction's date
	// may be for a suggested match, and the two sides of a transfer.
	bankMatchDays = 5
)

func bankMethod(method string) bool {
	switch method {
	case "list_bank_activity", "suggest_bank_matches", "link_bank_activity", "unlink_bank_activity", "record_bank_activity", "record_bank_transfer",
		"get_entry_origin", "prepare_bank_connection", "list_recording_rules", "record_by_rule", "list_operation_proposals",
		"list_agent_kinds", "get_agent_summary", "get_book_link", "get_book_links", "get_linked_balances", "list_assignment_rules":
		return true
	}
	if bankManagementMethod(method) {
		return true
	}
	return false
}

func bankWriting(method string) bool {
	switch method {
	case "link_bank_activity", "unlink_bank_activity", "record_bank_activity", "record_bank_transfer", "record_by_rule":
		return true
	}
	return bankManagementMethod(method)
}

func validBankChange(method string, input []byte) (any, string, error) {
	q, key, err := validBankChangeShape(method, input)
	if errors.Is(err, ErrInvalid) {
		var refused *RefusedError
		if errors.As(err, &refused) {
			return q, key, err
		}
		if problem := bankArgumentProblem(method, input); problem != nil {
			return q, key, problem
		}
	}
	return q, key, err
}

// bankArgumentProblem names unknown or missing arguments for a bank
// operation, so an agent can correct its call.
func bankArgumentProblem(method string, input []byte) error {
	shapes := map[string]struct {
		shape    any
		required []string
	}{
		"link_bank_activity":     {LinkBankActivity{}, []string{"request_key", "transaction_id", "entry_id"}},
		"unlink_bank_activity":   {UnlinkBankActivity{}, []string{"request_key", "link_id", "reason"}},
		"record_bank_activity":   {RecordBankActivity{}, []string{"request_key", "transaction_id", "description"}},
		"record_bank_transfer":   {RecordBankTransfer{}, []string{"request_key", "transaction_ids"}},
		"record_by_rule":         {RecordByRule{}, []string{"request_key", "transaction_id", "rule_id"}},
		"link_bank_account":      {LinkBankAccountChange{}, []string{"request_key", "bank_account_id", "account_id"}},
		"disconnect_bank":        {DisconnectBank{}, []string{"request_key", "connection_id", "reason"}},
		"pause_bank_connection":  {PauseBankConnection{}, []string{"request_key", "connection_id", "reason"}},
		"resume_bank_connection": {ResumeBankConnection{}, []string{"request_key", "connection_id"}},
		"mark_bank_duplicate":    {MarkBankDuplicate{}, []string{"request_key", "transaction_id", "canonical_transaction_id", "reason"}},
	}
	v, ok := shapes[method]
	if !ok {
		return nil
	}
	err := argumentProblem(method, input, v.shape, v.required...)
	if errors.Is(err, ErrInvalid) {
		var refused *RefusedError
		if errors.As(err, &refused) {
			return err
		}
	}
	return nil
}

func validBankChangeShape(method string, input []byte) (any, string, error) {
	switch method {
	case "link_bank_activity":
		var q LinkBankActivity
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.TransactionID) || !ValidID(q.EntryID) ||
			q.Basis != "" && q.Basis != "judgement" && q.Basis != "existing_entry" || len(q.Reason) > 4000 {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "unlink_bank_activity":
		var q UnlinkBankActivity
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.LinkID) || !validStatementText(q.Reason, 4000) || len(q.Reason) == 0 {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "record_bank_activity":
		var q RecordBankActivity
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || len(q.RequestKey) > 120 || !ValidID(q.TransactionID) ||
			q.EconomicDate != "" && !ValidDate(q.EconomicDate) || q.AccountingDate != "" && !ValidDate(q.AccountingDate) || len(q.Reason) > 4000 ||
			q.PairKey != "" && !validPairKey(q.PairKey) || !validAssignedBy(q.AssignedBy, q.PairKey) || !validSettlement(q.Settlement, q.Repays, q.PairKey) {
			return nil, "", ErrInvalid
		}
		if err := validProjectArguments(&q, input); err != nil {
			return nil, "", err
		}
		if (q.OtherAccountID == "") == (len(q.Lines) == 0) {
			return nil, "", &RefusedError{Reason: "record_bank_activity takes either other_account_id (the whole amount to one account) or lines (to split it), not both and not neither."}
		}
		// The journal itself must be valid once dates and amounts are filled in.
		lines := q.Lines
		if q.OtherAccountID != "" {
			if !ValidID(q.OtherAccountID) {
				return nil, "", ErrInvalid
			}
			lines = []PostLine{{AccountID: q.OtherAccountID, Side: "debit", AmountMinor: "1"}, {AccountID: ID(), Side: "credit", AmountMinor: "1"}}
		}
		probe := PostJournal{RequestKey: q.RequestKey + "/entry", EconomicDate: "2000-01-01", AccountingDate: "2000-01-01", Description: q.Description, Lines: lines, Sources: q.Sources}
		if validatePost(probe) != nil {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	case "record_bank_transfer":
		var q RecordBankTransfer
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || len(q.RequestKey) > 120 || len(q.TransactionIDs) != 2 ||
			!ValidID(q.TransactionIDs[0]) || !ValidID(q.TransactionIDs[1]) || q.TransactionIDs[0] == q.TransactionIDs[1] ||
			q.AccountingDate != "" && !ValidDate(q.AccountingDate) || len(q.Description) > 4000 || len(q.Reason) > 4000 {
			return nil, "", ErrInvalid
		}
		return q, q.RequestKey, nil
	}
	if method == "record_by_rule" {
		q, err := validRecordByRule(input)
		return q, q.RequestKey, err
	}
	return validBankManagement(method, input)
}

func bankRead(ctx context.Context, tx pgx.Tx, s Scope, method string, input []byte) (json.RawMessage, error) {
	switch method {
	case "list_bank_activity":
		var q BankActivityQuery
		if strictjson.Decode(input, &q) != nil {
			return nil, ErrInvalid
		}
		page, err := readBankActivity(ctx, tx, s, q)
		return raw(page), err
	case "suggest_bank_matches":
		if strictjson.Decode(input, &struct{}{}) != nil {
			return nil, ErrInvalid
		}
		out, err := suggestBankMatches(ctx, tx, s)
		return raw(out), err
	case "get_entry_origin":
		var q EntryQuery
		if strictjson.Decode(input, &q) != nil || !ValidID(q.EntryID) {
			return nil, ErrInvalid
		}
		out, err := readEntryOrigin(ctx, tx, s, q.EntryID)
		return raw(out), err
	case "list_operation_proposals":
		var q struct {
			Limit  int   `json:"limit,omitempty"`
			Before int64 `json:"before,omitempty"`
		}
		if strictjson.Decode(input, &q) != nil || q.Limit < 0 || q.Limit > 100 || q.Before < 0 {
			return nil, ErrInvalid
		}
		if q.Limit == 0 {
			q.Limit = 50
		}
		out, err := readOwnProposals(ctx, tx, s, q.Before, q.Limit)
		var next int64
		if len(out) == q.Limit {
			next = out[len(out)-1].Sequence
		}
		return raw(struct {
			Proposals  []AgentProposal `json:"proposals"`
			NextBefore int64           `json:"next_before,omitempty"`
		}{out, next}), err
	case "list_recording_rules":
		if strictjson.Decode(input, &struct{}{}) != nil {
			return nil, ErrInvalid
		}
		rules, err := readRules(ctx, tx, s)
		return raw(struct {
			Rules []RecordingRule `json:"rules"`
		}{rules}), err
	case "prepare_bank_connection":
		var q PrepareBankConnection
		if strictjson.Decode(input, &q) != nil || q.ConnectionID != "" && !ValidID(q.ConnectionID) {
			return nil, ErrInvalid
		}
		out, err := prepareBankConnection(ctx, tx, s, q)
		return raw(out), err
	}
	return nil, ErrInvalid
}

func bankWrite(ctx context.Context, tx pgx.Tx, s Scope, method string, input []byte, admit func(context.Context, pgx.Tx, Scope, string, json.RawMessage) error) (json.RawMessage, error) {
	q, key, err := validBankChange(method, input)
	if err != nil {
		return nil, err
	}
	request := raw(q)
	return write(ctx, tx, s, method, key, request, admit, func(receipt string) (json.RawMessage, error) {
		if bankManagementMethod(method) {
			out, err := bankManage(ctx, tx, s, q, receipt)
			if err != nil {
				return nil, bankError(err)
			}
			return raw(out), nil
		}
		var out BankLinkResult
		var err error
		switch v := q.(type) {
		case LinkBankActivity:
			out, err = linkBankActivity(ctx, tx, s, v, receipt)
		case UnlinkBankActivity:
			out, err = unlinkBankActivity(ctx, tx, s, v, receipt)
		case RecordBankActivity:
			out, err = recordBankActivity(ctx, tx, s, v, receipt)
		case RecordBankTransfer:
			out, err = recordBankTransfer(ctx, tx, s, v, receipt)
		case RecordByRule:
			out, err = recordByRule(ctx, tx, s, v, receipt)
		}
		if err != nil {
			return nil, bankError(err)
		}
		if err = bankChecked(ctx, tx); err != nil {
			return nil, bankError(err)
		}
		return raw(out), nil
	})
}

// bankError turns the database's plain-language refusals into a reason the
// agent or owner can act on.
func bankError(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		if p.ConstraintName == "bank_accounts_book_account" {
			return &RefusedError{Reason: "That Book account is already linked to another bank account."}
		}
		return &RefusedError{Reason: "This bank activity is already recorded by a live link."}
	}
	if err = statementError(err); errors.As(err, &p) {
		return dbError(err)
	}
	return err
}

// bankChecked runs a link's deferred amount check now, so a refusal keeps
// its reason. Receipt checks stay at commit.
func bankChecked(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SET CONSTRAINTS bank_link_final, bank_link_once, confident_link_final, rule_kind_trusted IMMEDIATE")
	return err
}

type bankItem struct {
	id, bankAccount, account, class, currency string
	date                                      string
	amount                                    int64
	scale                                     int
	pending, removed, linked                  bool
	// moved is recorded as money moved within its Book account.
	moved bool
	// text is what the bank wrote, its description and counterparty, and
	// accountName the Book account's name; matching reads both.
	text, accountName string
	// bankName is what the bank calls the account.
	bankName string
	// wallet is a payment app's balance, such as PayPal, rather than a bank's.
	wallet bool
}

func readBankItem(ctx context.Context, tx pgx.Tx, s Scope, id string) (bankItem, error) {
	var v bankItem
	var account *string
	err := tx.QueryRow(ctx, `SELECT t.id::text,coalesce(t.bank_account_id::text,''),coalesce(a.account_id,t.account_id)::text,coalesce(ac.class,''),t.currency,
 t.occurred_on::text,t.amount_minor,t.scale,t.pending,t.removed_at IS NOT NULL,
 EXISTS(SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id=t.tenant_id AND l.book_id=t.book_id AND l.transaction_id=t.id AND l.revoked_at IS NULL),
 EXISTS(SELECT 1 FROM public.bank_transaction_moves m WHERE m.tenant_id=t.tenant_id AND m.book_id=t.book_id AND m.transaction_id=t.id)
 FROM public.bank_transactions t LEFT JOIN public.bank_accounts a ON a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=t.bank_account_id
 LEFT JOIN public.accounts ac ON ac.tenant_id=t.tenant_id AND ac.book_id=t.book_id AND ac.id=coalesce(a.account_id,t.account_id)
 WHERE t.tenant_id=$1 AND t.book_id=$2 AND t.id=$3`, s.TenantID, s.BookID, id).Scan(
		&v.id, &v.bankAccount, &account, &v.class, &v.currency, &v.date, &v.amount, &v.scale, &v.pending, &v.removed, &v.linked, &v.moved)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if account != nil {
		v.account = *account
	}
	return v, err
}

// recordable refuses, in words, a transaction that can't be recorded now.
func (v bankItem) recordable() error {
	switch {
	case v.linked:
		return &RefusedError{Reason: "this bank transaction is already recorded; unlink it first to record it differently"}
	case v.moved:
		return &RefusedError{Reason: "this bank transaction is already recorded as money moved within its Book account"}
	case v.pending:
		return &RefusedError{Reason: "pending bank activity is recorded once it posts"}
	case v.removed:
		return &RefusedError{Reason: "the bank removed this transaction; there is nothing to record"}
	case v.account == "":
		return &RefusedError{Reason: "link the bank account to a Book account first"}
	}
	return nil
}

func insertBankLink(ctx context.Context, tx pgx.Tx, s Scope, v bankItem, entry, receipt, basis, reason string) (BankLinkRecord, error) {
	link := BankLinkRecord{ID: ID(), TransactionID: v.id, EntryID: entry, Reason: reason}
	if basis == "" {
		basis = "judgement"
	}
	_, err := tx.Exec(ctx, `INSERT INTO public.bank_transaction_links(tenant_id,book_id,id,transaction_id,bank_account_id,account_id,entry_id,actor_id,receipt_id,basis,reason)
 VALUES($1,$2,$3,$4,nullif($5,'')::uuid,$6,$7,$8,$9,$10,$11)`, s.TenantID, s.BookID, link.ID, v.id, v.bankAccount, v.account, entry, s.ActorID, receipt, basis, reason)
	return link, err
}

func linkBankActivity(ctx context.Context, tx pgx.Tx, s Scope, q LinkBankActivity, receipt string) (BankLinkResult, error) {
	v, err := readBankItem(ctx, tx, s, q.TransactionID)
	if err != nil {
		return BankLinkResult{}, err
	}
	if err = v.recordable(); err != nil {
		return BankLinkResult{}, err
	}
	if _, err = readEntry(ctx, tx, s, q.EntryID); err != nil {
		return BankLinkResult{}, err
	}
	link, err := insertBankLink(ctx, tx, s, v, q.EntryID, receipt, q.Basis, q.Reason)
	if err != nil {
		return BankLinkResult{}, err
	}
	return BankLinkResult{Links: []BankLinkRecord{link}}, nil
}

func unlinkBankActivity(ctx context.Context, tx pgx.Tx, s Scope, q UnlinkBankActivity, receipt string) (BankLinkResult, error) {
	var link BankLinkRecord
	err := tx.QueryRow(ctx, `UPDATE public.bank_transaction_links SET revoked_by=$4,revoke_reason=$5,revoke_receipt_id=$6,revoked_at=clock_timestamp()
 WHERE tenant_id=$1 AND book_id=$2 AND id=$3 AND revoked_at IS NULL RETURNING id::text,transaction_id::text,entry_id::text`,
		s.TenantID, s.BookID, q.LinkID, s.ActorID, q.Reason, receipt).Scan(&link.ID, &link.TransactionID, &link.EntryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return BankLinkResult{}, ErrNotFound
	}
	if err != nil {
		return BankLinkResult{}, err
	}
	// An entry of a pair keeps its bank item; undoing reverses both entries.
	if partner, err := readEntryPair(ctx, tx, s, link.EntryID); err != nil {
		return BankLinkResult{}, err
	} else if partner != nil {
		return BankLinkResult{}, pairUndoRefusal(partner)
	}
	link.Revoked, link.Reason = true, q.Reason
	return BankLinkResult{Links: []BankLinkRecord{link}}, nil
}

// postBankEntry posts a journal under its own post_journal receipt, keyed
// after the bank request, as a retirement's transfer is.
func postBankEntry(ctx context.Context, tx pgx.Tx, s Scope, post PostJournal) (Entry, error) {
	if err := validatePost(post); err != nil {
		return Entry{}, err
	}
	receipt := ID()
	v, err := insertJournal(ctx, tx, s, post, receipt)
	if err != nil {
		return Entry{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO public.requests(tenant_id,book_id,id,actor_id,request_key,operation,request_bytes,result_bytes) VALUES($1,$2,$3,$4,$5,'post_journal',$6,$7)",
		s.TenantID, s.BookID, receipt, s.ActorID, post.RequestKey, []byte(raw(post)), []byte(raw(v))); err != nil {
		return Entry{}, err
	}
	return v, nil
}

func recordBankActivity(ctx context.Context, tx pgx.Tx, s Scope, q RecordBankActivity, receipt string) (BankLinkResult, error) {
	v, err := readBankItem(ctx, tx, s, q.TransactionID)
	if err != nil {
		return BankLinkResult{}, err
	}
	if err = v.recordable(); err != nil {
		return BankLinkResult{}, err
	}
	if q.OtherAccountID != "" {
		if q.OtherAccountID == v.account {
			return BankLinkResult{}, &RefusedError{Reason: "other_account_id is the bank account's own Book account; give the account the money came from or went to."}
		}
		q.Lines = itemLines(v, q.OtherAccountID)
	}
	onAccount := false
	for _, line := range q.Lines {
		onAccount = onAccount || line.AccountID == v.account
	}
	if !onAccount {
		return BankLinkResult{}, &RefusedError{Reason: "the entry must post to the bank account's Book account"}
	}
	post := PostJournal{RequestKey: q.RequestKey + "/entry", EconomicDate: q.EconomicDate, AccountingDate: q.AccountingDate,
		Description: q.Description, Lines: q.Lines, Sources: q.Sources}
	if post.EconomicDate == "" {
		post.EconomicDate = v.date
	}
	if post.AccountingDate == "" {
		post.AccountingDate = v.date
	}
	entry, err := postBankEntry(ctx, tx, s, post)
	if err != nil {
		return BankLinkResult{}, err
	}
	link, err := insertBankLink(ctx, tx, s, v, entry.ID, receipt, "judgement", q.Reason)
	if err != nil {
		return BankLinkResult{}, err
	}
	out := BankLinkResult{Links: []BankLinkRecord{link}, Entry: &entry}
	if q.labelled() {
		// The entry is posted as it would be without a project; the label
		// is added to it afterwards.
		if out.Projects, err = labelRecorded(ctx, tx, s, v, q, entry.ID, receipt); err != nil {
			return BankLinkResult{}, err
		}
	}
	return out, nil
}

// debitPositive is a transaction's effect on its Book account as a debit
// (positive) or credit (negative), from the account's class alone.
func (v bankItem) debitPositive() int64 {
	if v.class == "liability" {
		return -v.amount
	}
	return v.amount
}

func transferable(a, b bankItem) bool {
	if a.account == b.account {
		return movedWithin(a, b)
	}
	return a.currency == b.currency &&
		(a.class == "asset" || a.class == "liability") && (b.class == "asset" || b.class == "liability") &&
		a.debitPositive() == -b.debitPositive() && a.amount != 0
}

// maxTransferDays is the most days between the two sides of one transfer.
const maxTransferDays = 31

// daysApart is the whole days between two dates, or 0 when either is
// unreadable.
func daysApart(a, b string) int {
	x, e1 := time.Parse(time.DateOnly, a)
	y, e2 := time.Parse(time.DateOnly, b)
	if e1 != nil || e2 != nil {
		return 0
	}
	d := int(x.Sub(y).Hours() / 24)
	if d < 0 {
		d = -d
	}
	return d
}

// movedWithin is money that left one bank account and reached another that
// share a Book account, as when a bank's new account takes over an old one:
// the Book account's balance doesn't change.
func movedWithin(a, b bankItem) bool {
	return a.account != "" && a.account == b.account && a.bankAccount != "" && b.bankAccount != "" && a.bankAccount != b.bankAccount &&
		a.currency == b.currency && a.amount != 0 && a.amount == -b.amount
}

func recordBankTransfer(ctx context.Context, tx pgx.Tx, s Scope, q RecordBankTransfer, receipt string) (BankLinkResult, error) {
	// The Book's write lock, held by every writer, keeps both unchanged.
	items := map[string]bankItem{}
	for _, id := range q.TransactionIDs {
		v, err := readBankItem(ctx, tx, s, id)
		if err != nil {
			return BankLinkResult{}, err
		}
		if err = v.recordable(); err != nil {
			return BankLinkResult{}, err
		}
		items[id] = v
	}
	a, b := items[q.TransactionIDs[0]], items[q.TransactionIDs[1]]
	book, err := readBook(ctx, tx, s)
	if err != nil {
		return BankLinkResult{}, err
	}
	if !transferable(a, b) {
		return BankLinkResult{}, &RefusedError{Reason: "a transfer moves the same money out of one of the Book's asset or liability accounts and into another"}
	}
	// Money moved between a person's own accounts arrives within days. Two
	// items far apart that happen to be equal are two things.
	if apart := daysApart(a.date, b.date); apart > maxTransferDays {
		return BankLinkResult{}, &RefusedError{Reason: fmt.Sprintf("these two items are %d days apart, too far to be one transfer; record each on its own", apart)}
	}
	if a.currency != book.Currency || a.scale != book.Scale {
		return BankLinkResult{}, &RefusedError{Reason: "record a transfer in another currency as an ordinary entry"}
	}
	for _, v := range []bankItem{a, b} {
		acct, err := readAccount(ctx, tx, s, v.account)
		if err != nil {
			return BankLinkResult{}, err
		}
		if acct.Currency != "" && acct.Currency != book.Currency {
			return BankLinkResult{}, &RefusedError{Reason: "record a transfer in another currency as an ordinary entry"}
		}
	}
	if movedWithin(a, b) {
		// Both items are recorded together with no entry: the money never
		// left the Book account.
		reason := q.Reason
		if reason == "" {
			reason = "Money moved between two bank accounts that share this Book account"
		}
		for _, pair := range [][2]bankItem{{a, b}, {b, a}} {
			if _, err := tx.Exec(ctx, `INSERT INTO public.bank_transaction_moves(tenant_id,book_id,transaction_id,other_transaction_id,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, s.TenantID, s.BookID, pair[0].id, pair[1].id, s.ActorID, clip(reason, 4000), receipt); err != nil {
				return BankLinkResult{}, bankError(err)
			}
		}
		return BankLinkResult{Links: []BankLinkRecord{}, MovedWithinAccount: true}, nil
	}
	debited, credited := a, b
	if a.debitPositive() < 0 {
		debited, credited = b, a
	}
	amount := strconv.FormatInt(debited.debitPositive(), 10)
	date := a.date
	if b.date > date {
		date = b.date
	}
	if q.AccountingDate != "" {
		date = q.AccountingDate
	}
	description := q.Description
	if description == "" {
		description = "Transfer between the Book's own accounts"
	}
	post := PostJournal{RequestKey: q.RequestKey + "/entry", EconomicDate: date, AccountingDate: date, Description: description,
		Lines: []PostLine{{AccountID: debited.account, Side: "debit", AmountMinor: amount}, {AccountID: credited.account, Side: "credit", AmountMinor: amount}}}
	entry, err := postBankEntry(ctx, tx, s, post)
	if err != nil {
		return BankLinkResult{}, err
	}
	out := BankLinkResult{Entry: &entry}
	for _, v := range []bankItem{a, b} {
		link, err := insertBankLink(ctx, tx, s, v, entry.ID, receipt, "transfer", q.Reason)
		if err != nil {
			return BankLinkResult{}, err
		}
		out.Links = append(out.Links, link)
	}
	return out, nil
}

// bankStatus is a transaction's place in the inbox.
const bankStatusSQL = `CASE WHEN live.id IS NOT NULL AND NOT public.statement_entry_reversed(t.tenant_id,t.book_id,live.entry_id) THEN 'recorded'
 WHEN EXISTS(SELECT 1 FROM public.bank_transaction_moves m WHERE m.tenant_id=t.tenant_id AND m.book_id=t.book_id AND m.transaction_id=t.id) THEN 'recorded'
 WHEN t.removed_at IS NOT NULL THEN 'removed'
 WHEN EXISTS(SELECT 1 FROM public.bank_transaction_duplicates d WHERE d.tenant_id=t.tenant_id AND d.book_id=t.book_id AND d.transaction_id=t.id) THEN 'repeat'
 WHEN t.pending THEN 'pending'
 WHEN coalesce(a.account_id,t.account_id) IS NULL THEN 'needs_account' ELSE 'unrecorded' END`

const bankActivityFrom = ` FROM public.bank_transactions t
 LEFT JOIN public.bank_accounts a ON a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=t.bank_account_id
 LEFT JOIN public.bank_connections c ON c.tenant_id=a.tenant_id AND c.book_id=a.book_id AND c.id=a.connection_id
 LEFT JOIN public.accounts ac ON ac.tenant_id=t.tenant_id AND ac.book_id=t.book_id AND ac.id=coalesce(a.account_id,t.account_id)
 LEFT JOIN public.statement_rows sr ON sr.tenant_id=t.tenant_id AND sr.book_id=t.book_id AND sr.id=t.statement_row_id
 LEFT JOIN public.statements st ON st.tenant_id=sr.tenant_id AND st.book_id=sr.book_id AND st.id=sr.statement_id
 LEFT JOIN public.bank_transaction_links live ON live.tenant_id=t.tenant_id AND live.book_id=t.book_id AND live.transaction_id=t.id AND live.revoked_at IS NULL`

func readBankActivity(ctx context.Context, tx pgx.Tx, s Scope, q BankActivityQuery) (BankActivityPage, error) {
	status := q.Status
	if status == "" {
		status = "unrecorded"
	}
	switch status {
	case "unrecorded", "recorded", "pending", "removed", "needs_account", "repeat", "all":
	default:
		return BankActivityPage{}, ErrInvalid
	}
	limit := q.Limit
	if limit == 0 {
		limit = bankDefaultPage
	}
	if limit < 1 || limit > bankMaxPage || q.BankAccountID != "" && !ValidID(q.BankAccountID) {
		return BankActivityPage{}, ErrInvalid
	}
	afterDate, afterID := "9999-12-31", "ffffffff-ffff-ffff-ffff-ffffffffffff"
	if q.After != "" {
		if len(q.After) != 47 || q.After[10] != '/' || !ValidDate(q.After[:10]) || !ValidID(q.After[11:]) {
			return BankActivityPage{}, ErrInvalid
		}
		afterDate, afterID = q.After[:10], q.After[11:]
	}
	// An item still in the inbox was never removed, so those statuses look
	// only at activity that wasn't, and its index can lead the way.
	live := ""
	switch status {
	case "unrecorded", "pending", "needs_account", "repeat":
		live = " AND t.removed_at IS NULL"
	}
	// The page's items are chosen first, by status and date alone, so the
	// rest is worked out for one page and not for all of the Book's activity.
	rows, err := tx.Query(ctx, `SELECT * FROM (SELECT t.id::text AS id,coalesce(t.bank_account_id::text,''),coalesce(coalesce(a.account_id,t.account_id)::text,''),coalesce(ac.class,''),
 CASE WHEN a.id IS NULL THEN coalesce(ac.name,'Account') ELSE coalesce(nullif(a.name,''),'Bank account')||CASE WHEN a.mask<>'' THEN ' ••'||a.mask ELSE '' END END,coalesce(c.institution_name,''),
 t.occurred_on,coalesce(t.authorized_on::text,''),t.amount_minor::text,t.currency,t.scale,t.description,t.counterparty,`+bankStatusSQL+` AS status,
 live.id::text AS link_id,live.entry_id::text AS link_entry,live.actor_id::text AS link_actor,live.recorded_at AS linked_at,t.updated_at,
 coalesce(t.possible_repeat_of::text,'') AS possible_repeat_of,coalesce(live.basis,'') AS link_basis,coalesce(live.reason,'') AS link_reason,
 t.source,coalesce(t.statement_row_id::text,'') AS statement_row_id,coalesce(st.document_id::text,'') AS document_id,coalesce(st.id::text,'') AS statement_id,
 coalesce(coalesce(a.account_id,t.account_id)::text||','||CASE WHEN t.amount_minor>0 THEN 'in' ELSE 'out' END||','||left(public.rule_key(coalesce(nullif(t.counterparty,''),t.description)),200),'') AS kind_key,
 coalesce((SELECT o.name FROM public.lines x JOIN public.accounts o ON o.tenant_id=x.tenant_id AND o.book_id=x.book_id AND o.id=x.account_id
  WHERE x.tenant_id=live.tenant_id AND x.book_id=live.book_id AND x.entry_id=live.entry_id AND x.account_id<>live.account_id
  AND (SELECT count(*) FROM public.lines y WHERE y.tenant_id=live.tenant_id AND y.book_id=live.book_id AND y.entry_id=live.entry_id)=2 LIMIT 1),'') AS link_other`+bankActivityFrom+`
 WHERE t.tenant_id=$1 AND t.book_id=$2 AND ($3='' OR t.bank_account_id::text=$3)`+live+`
 AND (t.occurred_on,t.id)<($4::date,$5::uuid)
 AND t.id IN (SELECT p.id FROM (SELECT t.id,t.occurred_on,`+bankStatusSQL+` AS status`+bankActivityFrom+`
   WHERE t.tenant_id=$1 AND t.book_id=$2 AND ($3='' OR t.bank_account_id::text=$3)`+live+` AND (t.occurred_on,t.id)<($4::date,$5::uuid)) p
  WHERE $6='all' OR p.status=$6 ORDER BY p.occurred_on DESC,p.id DESC LIMIT $7)) v WHERE $6='all' OR v.status=$6
 ORDER BY occurred_on DESC,id DESC LIMIT $7`, s.TenantID, s.BookID, q.BankAccountID, afterDate, afterID, status, limit+1)
	if err != nil {
		return BankActivityPage{}, dbError(err)
	}
	defer rows.Close()
	page := BankActivityPage{Items: []BankTransaction{}}
	for rows.Next() {
		var v BankTransaction
		var date time.Time
		var linkID, entry, actor *string
		var linked *time.Time
		var basis, reason, other string
		if err := rows.Scan(&v.ID, &v.BankAccountID, &v.AccountID, &v.AccountClass, &v.AccountName, &v.Institution, &date, &v.AuthorizedDate,
			&v.AmountMinor, &v.Currency, &v.Scale, &v.Description, &v.Counterparty, &v.Status, &linkID, &entry, &actor, &linked, &v.UpdatedAt,
			&v.PossibleRepeatOf, &basis, &reason, &v.Source, &v.StatementRowID, &v.DocumentID, &v.StatementID, &v.KindKey, &other); err != nil {
			return BankActivityPage{}, dbError(err)
		}
		v.Date = date.Format(time.DateOnly)
		if linkID != nil {
			v.Link = &BankLink{ID: *linkID, EntryID: *entry, ActorID: *actor, RecordedAt: *linked, Basis: basis, Reason: reason, OtherAccountName: other}
		}
		page.Items = append(page.Items, v)
	}
	if err := rows.Err(); err != nil {
		return BankActivityPage{}, dbError(err)
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.Next = last.Date + "/" + last.ID
	}
	// Activity on a bank account not yet linked says which Book account it
	// would be linked to, once per bank account.
	type suggestion struct{ bankType, account string }
	suggested := map[string]suggestion{}
	for i, v := range page.Items {
		if v.Status != "needs_account" || v.BankAccountID == "" {
			continue
		}
		got, ok := suggested[v.BankAccountID]
		if !ok {
			if got.bankType, got.account, err = suggestBankLink(ctx, tx, s, v.BankAccountID); err != nil {
				return BankActivityPage{}, err
			}
			suggested[v.BankAccountID] = got
		}
		page.Items[i].BankType, page.Items[i].SuggestedAccountID = got.bankType, got.account
	}
	if err = suggestBeneficiaries(ctx, tx, s, page.Items); err != nil {
		return BankActivityPage{}, err
	}
	if err = itemProjects(ctx, tx, s, page.Items); err != nil {
		return BankActivityPage{}, err
	}
	// Where a kind was recorded before is for an agent choosing an account;
	// the owner's own pages don't show it.
	if s.ConnectionID != "" {
		if err = recordedBefore(ctx, tx, s, page.Items); err != nil {
			return BankActivityPage{}, err
		}
	}
	if q.firstOnly && q.After != "" {
		return page, nil
	}
	page.Summary, err = readBankSummary(ctx, tx, s)
	return page, err
}

// suggestBeneficiaries fills in, for unrecorded items in a Book that pays
// for linked Books, where each would likely be paired.
func suggestBeneficiaries(ctx context.Context, tx pgx.Tx, s Scope, items []BankTransaction) error {
	ids, positions := []string{}, []int{}
	for i, v := range items {
		if v.Status == "unrecorded" {
			ids = append(ids, v.ID)
			positions = append(positions, i)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var pays bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id=x.tenant_id AND l.id=x.link_id
 WHERE x.tenant_id=$1 AND x.payer_book_id=$2 AND x.active AND l.active)`, s.TenantID, s.BookID).Scan(&pays); err != nil || !pays {
		return dbError(err)
	}
	rows, err := tx.Query(ctx, `SELECT ordinal-1,public.suggested_beneficiaries($1,$2,id)
 FROM unnest($3::uuid[]) WITH ORDINALITY AS item(id,ordinal)`, s.TenantID, s.BookID, ids)
	if err != nil {
		return dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var raw []byte
		if err := rows.Scan(&index, &raw); err != nil {
			return dbError(err)
		}
		if index < 0 || index >= len(positions) {
			return ErrUncertain
		}
		if err := json.Unmarshal(raw, &items[positions[index]].SuggestedBeneficiaries); err != nil {
			return ErrUncertain
		}
	}
	return dbError(rows.Err())
}

// recordedBefore fills in, for unrecorded items, where this Book recorded
// items of the same kind before. Only entries with one other account count,
// and none that was reversed: the Book's record as it stands.
func recordedBefore(ctx context.Context, tx pgx.Tx, s Scope, items []BankTransaction) error {
	ids, positions := []string{}, map[string]int{}
	for i, v := range items {
		if v.Status == "unrecorded" && v.AccountID != "" {
			ids = append(ids, v.ID)
			positions[v.ID] = i
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `WITH item AS (
    SELECT t.id, coalesce(a.account_id,t.account_id) AS account, t.amount_minor>0 AS up, public.rule_key(coalesce(nullif(t.counterparty,''),t.description)) AS key
    FROM public.bank_transactions t LEFT JOIN public.bank_accounts a ON a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=t.bank_account_id
    WHERE t.tenant_id=$1 AND t.book_id=$2 AND t.id = ANY($3::uuid[])
), past AS (
    SELECT k.account_id AS account, t.amount_minor>0 AS up, public.rule_key(coalesce(nullif(t.counterparty,''),t.description)) AS key, k.entry_id, abs(t.amount_minor) AS size, t.occurred_on AS day
    FROM public.bank_transaction_links k JOIN public.bank_transactions t ON t.tenant_id=k.tenant_id AND t.book_id=k.book_id AND t.id=k.transaction_id
    WHERE k.tenant_id=$1 AND k.book_id=$2 AND k.revoked_at IS NULL AND k.account_id IN (SELECT account FROM item)
        AND NOT public.statement_entry_reversed(k.tenant_id,k.book_id,k.entry_id)
), used AS (
    SELECT p.account, p.up, p.key, l.account_id AS other, count(*) AS times, max(p.size) AS largest, max(p.day) AS last
    FROM past p JOIN public.lines l ON l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=p.entry_id AND l.account_id<>p.account
    WHERE p.key IN (SELECT key FROM item)
        AND (SELECT count(*) FROM public.lines y WHERE y.tenant_id=$1 AND y.book_id=$2 AND y.entry_id=p.entry_id)=2
    GROUP BY p.account, p.up, p.key, l.account_id
)
SELECT id, other, times, largest, last FROM (SELECT i.id::text AS id, u.other::text AS other, u.times, u.largest::text AS largest, u.last::text AS last, row_number() OVER (PARTITION BY i.id ORDER BY u.times DESC, u.other) AS place
    FROM item i JOIN used u ON u.account=i.account AND u.up=i.up AND u.key=i.key) ranked
WHERE place<=3 ORDER BY id, place`, s.TenantID, s.BookID, ids)
	if err != nil {
		return dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var before RecordedBefore
		if err := rows.Scan(&id, &before.AccountID, &before.Times, &before.LargestMinor, &before.LastDate); err != nil {
			return dbError(err)
		}
		i, ok := positions[id]
		if !ok {
			return ErrUncertain
		}
		items[i].RecordedBefore = append(items[i].RecordedBefore, before)
	}
	return dbError(rows.Err())
}

func readBankSummary(ctx context.Context, tx pgx.Tx, s Scope) (BankActivitySummary, error) {
	var out BankActivitySummary
	// Activity of a disconnected login with no Book account waits for
	// nothing: no new account link can come for it.
	err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='unrecorded'),count(*) FILTER (WHERE status='pending'),
 count(*) FILTER (WHERE status='needs_account' AND NOT gone),count(*) FILTER (WHERE changed),
 (SELECT max(last_synced_at) FROM public.bank_connections WHERE tenant_id=$1 AND book_id=$2 AND disconnected_at IS NULL)
 FROM (SELECT `+bankStatusSQL+` AS status,
 live.id IS NOT NULL AND (t.removed_at IS NOT NULL OR t.updated_at>live.recorded_at AND t.revision>1) AS changed,
 coalesce(c.disconnected_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM public.bank_account_aliases x JOIN public.bank_connections xc
   ON xc.tenant_id=x.tenant_id AND xc.book_id=x.book_id AND xc.id=x.connection_id WHERE x.bank_account_id=a.id AND xc.disconnected_at IS NULL),false) AS gone`+bankActivityFrom+`
 WHERE t.tenant_id=$1 AND t.book_id=$2) v`, s.TenantID, s.BookID).Scan(&out.Unrecorded, &out.Pending, &out.NeedsAccount, &out.RecordedChanged, &out.LastSyncedAt)
	return out, dbError(err)
}

// suggestBankMatches finds, without recording anything, the unrecorded
// transactions that already have exactly one matching entry, and pairs that
// are one transfer between the Book's own accounts. Ambiguous candidates are
// left for a person or an agent to decide.
func suggestBankMatches(ctx context.Context, tx pgx.Tx, s Scope) ([]BankSuggestion, error) {
	open, err := openBankItems(ctx, tx, s)
	if err != nil {
		return nil, err
	}
	out := []BankSuggestion{}
	// 1. Entries already posted for the transaction.
	rows, err := tx.Query(ctx, `SELECT transaction_id::text,entry_id::text FROM public.bank_entry_matches($1,$2,$3::uuid[],$4)`, s.TenantID, s.BookID, keys(open), bankMatchDays)
	if err != nil {
		return nil, dbError(err)
	}
	byTx, byEntry := map[string][]string{}, map[string][]string{}
	for rows.Next() {
		var t, e string
		if err := rows.Scan(&t, &e); err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		byTx[t], byEntry[e] = append(byTx[t], e), append(byEntry[e], t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	// An entry that only moves money between the Book's own accounts is that
	// item only when the bank's words say so, as for a transfer.
	others, err := entryOtherSides(ctx, tx, s, sortedKeys(byEntry))
	if err != nil {
		return nil, err
	}
	matched := map[string]bool{}
	for _, t := range sortedKeys(byTx) {
		entries := byTx[t]
		if len(entries) == 1 && len(byEntry[entries[0]]) == 1 && entryFits(open[t], others[entries[0]]) {
			matched[t] = true
			out = append(out, BankSuggestion{Kind: "existing_entry", TransactionIDs: []string{t}, EntryID: entries[0], AmountMinor: strconv.FormatInt(open[t].amount, 10),
				Reason: "One entry already moves this account by the same amount within " + strconv.Itoa(bankMatchDays) + " days"})
		}
	}
	// 2. Transfers between the Book's own accounts.
	// Two items can only be one transfer when they move the same amount in
	// opposite directions, so only items of one size are compared.
	candidates := map[string][]string{}
	ids := sortedKeys(open)
	type size struct {
		currency string
		amount   int64
	}
	sameSize := map[size][]string{}
	for _, x := range ids {
		a := open[x]
		if matched[x] || a.amount == 0 {
			continue
		}
		k := size{a.currency, a.amount}
		if k.amount < 0 {
			k.amount = -k.amount
		}
		sameSize[k] = append(sameSize[k], x)
	}
	for _, group := range sameSize {
		for i, x := range group {
			for _, y := range group[i+1:] {
				a, b := open[x], open[y]
				if !transferable(a, b) || dayGap(a.date, b.date) > bankMatchDays || !saysMove(a, b) && !saysMove(b, a) {
					continue
				}
				candidates[x], candidates[y] = append(candidates[x], y), append(candidates[y], x)
			}
		}
	}
	for _, x := range ids {
		if len(candidates[x]) != 1 {
			continue
		}
		y := candidates[x][0]
		if x > y || len(candidates[y]) != 1 {
			continue
		}
		a := open[x]
		amount := a.debitPositive()
		if amount < 0 {
			amount = -amount
		}
		reason := "The same amount left one of the Book's accounts and reached another within " + strconv.Itoa(bankMatchDays) + " days"
		if movedWithin(a, open[y]) {
			reason = "The same amount left one bank account and reached another that share this Book account, within " + strconv.Itoa(bankMatchDays) + " days; it records with no entry"
		}
		out = append(out, BankSuggestion{Kind: "transfer", TransactionIDs: []string{x, y}, AmountMinor: strconv.FormatInt(amount, 10), Reason: reason})
	}
	return out, nil
}

// openBankItems are posted, reported, unlinked transactions on linked
// accounts: the ones a suggestion can be about.
func openBankItems(ctx context.Context, tx pgx.Tx, s Scope) (map[string]bankItem, error) {
	rows, err := tx.Query(ctx, `SELECT t.id::text,coalesce(t.bank_account_id::text,''),ac.id::text,ac.class,t.currency,t.occurred_on::text,t.amount_minor,t.scale,
 coalesce(t.description,'')||' '||coalesce(t.counterparty,''),ac.name,coalesce(a.name,''),coalesce(a.provider_subtype,'')
 FROM public.bank_transactions t LEFT JOIN public.bank_accounts a ON a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=t.bank_account_id
 JOIN public.accounts ac ON ac.tenant_id=t.tenant_id AND ac.book_id=t.book_id AND ac.id=coalesce(a.account_id,t.account_id)
 WHERE t.tenant_id=$1 AND t.book_id=$2 AND NOT t.pending AND t.removed_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id=t.tenant_id AND l.book_id=t.book_id AND l.transaction_id=t.id
   AND l.revoked_at IS NULL AND NOT public.statement_entry_reversed(l.tenant_id,l.book_id,l.entry_id))
 AND NOT EXISTS(SELECT 1 FROM public.bank_transaction_duplicates d WHERE d.tenant_id=t.tenant_id AND d.book_id=t.book_id AND d.transaction_id=t.id)
 AND NOT EXISTS(SELECT 1 FROM public.bank_transaction_moves m WHERE m.tenant_id=t.tenant_id AND m.book_id=t.book_id AND m.transaction_id=t.id)
 ORDER BY t.occurred_on DESC,t.id LIMIT 2000`, s.TenantID, s.BookID)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := map[string]bankItem{}
	for rows.Next() {
		var v bankItem
		var subtype string
		if err := rows.Scan(&v.id, &v.bankAccount, &v.account, &v.class, &v.currency, &v.date, &v.amount, &v.scale, &v.text, &v.accountName, &v.bankName, &subtype); err != nil {
			return nil, dbError(err)
		}
		v.wallet = isWallet(subtype + " " + v.bankName + " " + v.accountName)
		out[v.id] = v
	}
	return out, dbError(rows.Err())
}

func keys[V any](m map[string]V) []string { return sortedKeys(m) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dayGap(a, b string) int {
	x, err1 := time.Parse(time.DateOnly, a)
	y, err2 := time.Parse(time.DateOnly, b)
	if err1 != nil || err2 != nil {
		return 1 << 30
	}
	d := x.Sub(y)
	if d < 0 {
		d = -d
	}
	return int(d.Hours() / 24)
}

// bankPreview describes approved bank work for review. Recording activity or
// a transfer previews its entry like any journal; a link names the pair.
func bankPreview(book Book, operation string, effect json.RawMessage, sources []SourceInput, deps operationDependencies) (OperationPreview, error) {
	if bankManagementMethod(operation) {
		return bankManagementPreview(book, operation, effect, sources)
	}
	var out BankLinkResult
	if json.Unmarshal(effect, &out) != nil || len(out.Links) == 0 {
		return OperationPreview{}, ErrInvalid
	}
	change := &OperationBankChange{Change: map[string]string{"link_bank_activity": "link", "unlink_bank_activity": "unlink",
		"record_bank_activity": "record", "record_bank_transfer": "transfer"}[operation]}
	for _, l := range out.Links {
		change.TransactionIDs = append(change.TransactionIDs, l.TransactionID)
		change.EntryID = l.EntryID
		change.Reason = l.Reason
	}
	if out.Entry != nil {
		// The entry's own lines and account effects, previewed as a journal
		// against the dependencies read before it was posted.
		p := operationPreview(book, *out.Entry, sources, deps)
		change.EntryID = ""
		for _, project := range out.Projects {
			if project.New {
				// A new project has no ID until it is created.
				project.ProjectID = ""
			}
			change.Projects = append(change.Projects, project)
		}
		p.BankChange = change
		return p, nil
	}
	p := emptyOperationPreview(book, sources)
	p.BankChange = change
	p.Description = map[string]string{"link": "Record bank activity with an existing entry", "unlink": "Unlink bank activity from its entry"}[change.Change]
	return p, nil
}

// OperationBankChange is the bank activity an approved operation records.
type OperationBankChange struct {
	Change         string   `json:"change"`
	TransactionIDs []string `json:"transaction_ids,omitempty"`
	ConnectionID   string   `json:"connection_id,omitempty"`
	BankAccountID  string   `json:"bank_account_id,omitempty"`
	AccountID      string   `json:"account_id,omitempty"`
	EntryID        string   `json:"entry_id,omitempty"`
	Reason         string   `json:"reason,omitempty"`
	// Projects are the projects the recorded spending is labelled for; one
	// marked new is created when the work is recorded.
	Projects []EntryProject `json:"projects,omitempty"`
}

// bankMatchWindow is how many days apart a statement line and the same
// transaction from a connected bank may be dated.
const bankMatchWindow = 3

// addStatementItems puts a just-imported statement's lines in the inbox.
// A line that is the same transaction a connected bank already reported for
// the same account, by amount and date, joins that item instead; several
// candidates add the line marked as a possible repeat. Nothing is recorded.
func addStatementItems(ctx context.Context, tx pgx.Tx, s Scope, statement, receipt string) error {
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.occurred_on::text,r.amount_minor,r.description,r.lifecycle,st.account_id::text,st.currency,st.scale
 FROM public.statement_rows r JOIN public.statements st ON st.tenant_id=r.tenant_id AND st.book_id=r.book_id AND st.id=r.statement_id
 WHERE r.tenant_id=$1 AND r.book_id=$2 AND r.statement_id=$3 AND r.amount_minor<>0 AND r.lifecycle<>'cancelled' ORDER BY r.row_number`, s.TenantID, s.BookID, statement)
	if err != nil {
		return dbError(err)
	}
	type line struct {
		id, date, description, lifecycle, account, currency string
		amount                                              int64
		scale                                               int
	}
	var lines []line
	for rows.Next() {
		var v line
		if err := rows.Scan(&v.id, &v.date, &v.amount, &v.description, &v.lifecycle, &v.account, &v.currency, &v.scale); err != nil {
			rows.Close()
			return dbError(err)
		}
		lines = append(lines, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return dbError(err)
	}
	claimed := map[string]bool{}
	for _, v := range lines {
		repeat := ""
		if v.lifecycle != "pending" {
			found, err := sameTransaction(ctx, tx, s, `t.bank_account_id IS NOT NULL AND t.statement_row_id IS NULL AND a.account_id=$3`, v.account, v.date, v.amount, claimed)
			if err != nil {
				return err
			}
			if len(found) == 1 {
				claimed[found[0]] = true
				if _, err := tx.Exec(ctx, "UPDATE public.bank_transactions SET statement_row_id=$4 WHERE tenant_id=$1 AND book_id=$2 AND id=$3",
					s.TenantID, s.BookID, found[0], v.id); err != nil {
					return statementError(err)
				}
				continue
			}
			if len(found) > 1 {
				repeat = found[0]
			}
		}
		// The same line on an earlier statement of the account: the same
		// date and amount, already here from that statement. One such item
		// makes this line its repeat; several only flag it.
		canonical := ""
		if repeat == "" && v.lifecycle != "pending" {
			earlier, err := earlierStatementLines(ctx, tx, s, statement, v.account, v.date, v.amount, claimed)
			if err != nil {
				return err
			}
			switch {
			case len(earlier) == 1:
				canonical = earlier[0]
				claimed[canonical] = true
			case len(earlier) > 1:
				repeat = earlier[0]
			}
		}
		id := ID()
		if _, err := tx.Exec(ctx, `INSERT INTO public.bank_transactions(tenant_id,book_id,id,source,account_id,statement_row_id,provider_transaction_id,
 occurred_on,amount_minor,currency,scale,description,pending,possible_repeat_of)
 VALUES($1,$2,$3,'statement',$4,$5::uuid,$13,$6::date,$7,$8,$9,$10,$11,nullif($12,'')::uuid)`, s.TenantID, s.BookID, id, v.account, v.id,
			v.date, v.amount, v.currency, v.scale, clip(v.description, 1000), v.lifecycle == "pending", repeat, v.id); err != nil {
			return statementError(err)
		}
		if canonical != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO public.bank_transaction_duplicates(tenant_id,book_id,transaction_id,canonical_transaction_id,actor_id,reason,receipt_id)
 VALUES($1,$2,$3,$4,$5,'The same line as on an earlier statement.',$6)`, s.TenantID, s.BookID, id, canonical, s.ActorID, receipt); err != nil {
				return statementError(err)
			}
		}
	}
	return nil
}

// earlierStatementLines finds items on a Book account that a different
// statement already brought in, or joined, with this date and amount.
func earlierStatementLines(ctx context.Context, tx pgx.Tx, s Scope, statement, account, date string, amount int64, claimed map[string]bool) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT t.id::text FROM public.bank_transactions t
 JOIN public.statement_rows r ON r.tenant_id=t.tenant_id AND r.book_id=t.book_id AND r.id=t.statement_row_id
 LEFT JOIN public.bank_accounts a ON a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=t.bank_account_id
 WHERE t.tenant_id=$1 AND t.book_id=$2 AND r.statement_id<>$3::uuid AND coalesce(a.account_id,t.account_id)=$4::uuid
 AND t.occurred_on=$5::date AND t.amount_minor=$6 AND NOT t.pending AND t.removed_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM public.bank_transaction_duplicates d WHERE d.tenant_id=t.tenant_id AND d.book_id=t.book_id AND d.transaction_id=t.id)
 ORDER BY t.id`, s.TenantID, s.BookID, statement, account, date, amount)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, dbError(err)
		}
		if !claimed[id] {
			out = append(out, id)
		}
	}
	return out, dbError(rows.Err())
}

// sameTransaction finds unclaimed, posted, reported items on a Book account
// with this amount within the match window, under an extra condition.
func sameTransaction(ctx context.Context, tx pgx.Tx, s Scope, where, account, date string, amount int64, claimed map[string]bool) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT t.id::text FROM public.bank_transactions t
 LEFT JOIN public.bank_accounts a ON a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=t.bank_account_id
 WHERE t.tenant_id=$1 AND t.book_id=$2 AND `+where+` AND t.amount_minor=$4 AND NOT t.pending AND t.removed_at IS NULL
 AND t.occurred_on BETWEEN $5::date-$6::integer AND $5::date+$6::integer ORDER BY abs(t.occurred_on-$5::date),t.id`,
		s.TenantID, s.BookID, account, amount, date, bankMatchWindow)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, dbError(err)
		}
		if !claimed[id] {
			out = append(out, id)
		}
	}
	return out, dbError(rows.Err())
}

// itemLines records a whole bank item against one other account: the item's
// account moves the way its balance moved, and the other side balances it.
// Sides follow the account's class alone.
func itemLines(v bankItem, other string) []PostLine {
	amount := v.amount
	if amount < 0 {
		amount = -amount
	}
	side, opposite := "credit", "debit"
	if v.debitPositive() > 0 {
		side, opposite = "debit", "credit"
	}
	value := strconv.FormatInt(amount, 10)
	return []PostLine{{AccountID: v.account, Side: side, AmountMinor: value}, {AccountID: other, Side: opposite, AmountMinor: value}}
}

// moveWords are what a bank writes when money moves between a customer's own
// accounts; paymentWords, when it pays what an account owes, such as a card.
// Words a bank also writes on ordinary purchases and receipts, such as "ACH",
// "online" or "mobile", aren't among them: they'd pair any two items of one
// amount.
var (
	moveWords    = map[string]bool{"transfer": true, "xfer": true, "trnsfr": true, "tfr": true, "withdrawal": true, "wire": true, "internal": true, "sweep": true}
	paymentWords = map[string]bool{"payment": true, "pymt": true, "pmt": true, "epay": true, "autopay": true, "thank": true}
	// genericNameWords never pick out one account by themselves.
	genericNameWords = map[string]bool{"bank": true, "card": true, "credit": true, "debit": true, "account": true, "checking": true, "savings": true, "business": true, "usd": true, "loan": true, "payable": true}
	// walletWords name payment apps that hold a balance; a bank's account type
	// says so too, as "paypal" or "prepaid".
	walletWords = map[string]bool{"paypal": true, "venmo": true, "cashapp": true, "wise": true, "payoneer": true, "prepaid": true}
)

func bankWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// isWallet is whether an account's type or name says it is a payment app's
// balance rather than a bank account.
func isWallet(described string) bool {
	words := bankWords(described)
	for i, w := range words {
		if walletWords[w] || w == "cash" && i+1 < len(words) && words[i+1] == "app" {
			return true
		}
	}
	return false
}

// saysMove is whether what the bank wrote for an item gives a reason to think
// it moved money to or from another of the Book's accounts: it says it pays
// what is owed, says it moved money (between accounts that aren't owed), or
// names that account by a word or number only its name has. "Deposit"
// counts only when money goes into or comes from a wallet, since a bank also
// writes it for every check it takes in. Two items of the same amount and
// nothing more are not a transfer.
func saysMove(item, other bankItem) bool {
	owed := item.class == "liability" || other.class == "liability"
	said := map[string]bool{}
	for _, w := range bankWords(item.text) {
		if paymentWords[w] || !owed && (moveWords[w] || w == "deposit" && (item.wallet || other.wallet)) {
			return true
		}
		said[w] = true
	}
	// A word both accounts' names share, such as the business's own name or
	// one bank's, doesn't pick out the other one.
	own := map[string]bool{}
	for _, w := range bankWords(item.accountName + " " + item.bankName) {
		own[w] = true
	}
	for _, w := range bankWords(other.accountName) {
		if said[w] && !own[w] && !genericNameWords[w] && len(w) >= 4 {
			return true
		}
	}
	return false
}

type entrySide struct{ account, name, class string }

// entryOtherSides reads the accounts each entry posts to.
func entryOtherSides(ctx context.Context, tx pgx.Tx, s Scope, entries []string) (map[string][]entrySide, error) {
	out := map[string][]entrySide{}
	if len(entries) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT l.entry_id::text,a.id::text,a.name,a.class FROM public.lines l
 JOIN public.accounts a ON a.tenant_id=l.tenant_id AND a.book_id=l.book_id AND a.id=l.account_id
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.entry_id=ANY($3::uuid[])`, s.TenantID, s.BookID, entries)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e string
		var v entrySide
		if err := rows.Scan(&e, &v.account, &v.name, &v.class); err != nil {
			return nil, dbError(err)
		}
		out[e] = append(out[e], v)
	}
	return out, dbError(rows.Err())
}

// entryFits is whether an entry already recorded can be the item: always,
// unless the entry only pays what one of the Book's own accounts owes, such
// as a card, when the bank's words must say so. A move to a wallet such as
// PayPal is left alone: the bank often names whom the wallet paid instead.
func entryFits(item bankItem, sides []entrySide) bool {
	own, owed := true, false
	var others []entrySide
	for _, v := range sides {
		if v.account == item.account {
			continue
		}
		others = append(others, v)
		if v.class != "asset" && v.class != "liability" {
			own = false
		}
		if v.class == "liability" {
			owed = true
		}
	}
	if !own || !owed || len(others) == 0 {
		return true
	}
	for _, v := range others {
		if saysMove(item, bankItem{accountName: v.name, class: v.class}) {
			return true
		}
	}
	return false
}
