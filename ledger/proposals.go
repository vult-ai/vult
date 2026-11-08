package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/operationref"
	"github.com/vult-ai/vult/internal/strictjson"
)

var (
	ErrProposalStale    = errors.New("proposal dependencies changed; prepare a fresh operation")
	ErrApprovalRequired = errors.New("owner approval required; review the exact proposal before resuming")
	ErrProposalExpired  = errors.New("proposal or approval expired; prepare a fresh operation")
	ErrProposalDecided  = errors.New("proposal already decided; read its recorded outcome")
	ErrFreshOwnerProof  = errors.New("fresh owner authentication required; sign in again and review this operation")
)

type PrepareOperation struct {
	RequestKey    string          `json:"request_key"`
	Operation     string          `json:"operation"`
	Arguments     json.RawMessage `json:"arguments"`
	Assumptions   []string        `json:"assumptions,omitempty"`
	OpenQuestions []string        `json:"open_questions,omitempty"`
}

type ProposalQuery struct {
	ProposalID string `json:"proposal_id,omitempty"`
	RequestKey string `json:"request_key,omitempty"`
}

type ExecuteProposal struct {
	ProposalID string `json:"proposal_id"`
	SHA256     string `json:"sha256"`
}

type OperationPreview struct {
	Version           int                      `json:"version"`
	Currency          string                   `json:"currency"`
	Scale             int                      `json:"scale"`
	EntriesAffected   int                      `json:"entries_affected"`
	EconomicDate      string                   `json:"economic_date"`
	AccountingDate    string                   `json:"accounting_date"`
	Description       string                   `json:"description"`
	ReversalOf        string                   `json:"reversal_of,omitempty"`
	Lines             []PostLine               `json:"lines"`
	Sources           []SourceInput            `json:"sources"`
	SourceUsage       []OperationSourceUsage   `json:"source_usage"`
	Accounts          []OperationAccountEffect `json:"accounts"`
	Classes           []OperationClassEffect   `json:"classes"`
	MeasurementPolicy *MeasurementPolicy       `json:"measurement_policy,omitempty"`
}

type OperationSourceUsage struct {
	DocumentID    string   `json:"document_id"`
	Location      Location `json:"location"`
	ActiveEntries int64    `json:"active_entries"`
}

type OperationAccountEffect struct {
	AccountID   string `json:"account_id"`
	Class       string `json:"class"`
	BeforeMinor string `json:"before_minor"`
	ChangeMinor string `json:"change_minor"`
	AfterMinor  string `json:"after_minor"`
}

type OperationClassEffect struct {
	Class       string `json:"class"`
	DebitMinor  string `json:"debit_minor"`
	CreditMinor string `json:"credit_minor"`
	ChangeMinor string `json:"change_minor"`
}

type operationAccountState struct {
	AccountID        string `json:"account_id"`
	Class            string `json:"class"`
	LastSequence     int64  `json:"last_sequence"`
	BalanceMinor     string `json:"balance_minor"`
	CurrencyRevision int64  `json:"currency_revision"`
	RoleRevision     int64  `json:"role_revision"`
	OriginRevision   int64  `json:"origin_revision"`
}

// settled leaves out what other entries change in passing, an account's
// balance and latest entry, so a prepared entry stays approvable while the
// Book grows. Everything it rests on is kept.
func (d operationDependencies) settled() operationDependencies {
	out := d
	out.Accounts = make([]operationAccountState, len(d.Accounts))
	for i, a := range d.Accounts {
		a.LastSequence = 0
		a.BalanceMinor = ""
		out.Accounts[i] = a
	}
	return out
}

type operationDocumentState struct {
	DocumentID    string   `json:"document_id"`
	SHA256        string   `json:"sha256"`
	State         string   `json:"state"`
	Location      Location `json:"location"`
	ActiveEntries int64    `json:"active_entries"`
	LastCitation  int64    `json:"last_citation"`
}

type operationDependencies struct {
	Accounts      []operationAccountState  `json:"accounts"`
	Documents     []operationDocumentState `json:"documents"`
	ClosedPeriods []string                 `json:"closed_periods"`
	Reversed      bool                     `json:"reversed"`
}

type operationPayload struct {
	Version       int                   `json:"version"`
	Operation     string                `json:"operation"`
	Arguments     json.RawMessage       `json:"arguments"`
	Assumptions   []string              `json:"assumptions"`
	OpenQuestions []string              `json:"open_questions"`
	Dependencies  operationDependencies `json:"dependencies"`
	Preview       OperationPreview      `json:"preview"`
}

// OperationProposal is immutable prepared work. Status and decisions are read
// separately so a preparation retry returns the original result unchanged.
type OperationProposal struct {
	ID            string           `json:"id"`
	Sequence      int64            `json:"sequence"`
	BookID        string           `json:"book_id"`
	ActorID       string           `json:"actor_id"`
	ConnectionID  string           `json:"connection_id"`
	RequestKey    string           `json:"request_key"`
	Operation     string           `json:"operation"`
	OperationKey  string           `json:"operation_key"`
	SHA256        string           `json:"sha256"`
	CreatedAt     time.Time        `json:"created_at"`
	ExpiresAt     *time.Time       `json:"expires_at"`
	Arguments     json.RawMessage  `json:"arguments"`
	Assumptions   []string         `json:"assumptions"`
	OpenQuestions []string         `json:"open_questions"`
	Preview       OperationPreview `json:"preview"`
	Approval      OperationReview  `json:"approval"`

	payload operationPayload
}

// OperationReview is an address for human review, not approval or a credential.
// Message is a plain sentence the agent can show the owner.
type OperationReview struct {
	Flow      string `json:"flow"`
	Operation string `json:"operation"`
	Href      string `json:"href"`
	Message   string `json:"message"`
}

const reviewMessage = "Queued for your approval in Vult. Approving it there posts this exact entry."

const proposalColumns = `id::text,sequence,book_id::text,actor_id::text,connection_id::text,request_key,operation,operation_key,sha256,created_at,expires_at,payload_bytes`

func scanProposal(row pgx.Row) (OperationProposal, error) {
	var p OperationProposal
	var data []byte
	err := row.Scan(&p.ID, &p.Sequence, &p.BookID, &p.ActorID, &p.ConnectionID, &p.RequestKey, &p.Operation, &p.OperationKey, &p.SHA256, &p.CreatedAt, &p.ExpiresAt, &data)
	if err != nil {
		return p, err
	}
	if strictjson.Decode(data, &p.payload) != nil || p.payload.Version != 1 || p.payload.Operation != p.Operation {
		return p, ErrInvalid
	}
	p.Arguments, p.Assumptions, p.OpenQuestions, p.Preview = p.payload.Arguments, p.payload.Assumptions, p.payload.OpenQuestions, p.payload.Preview
	ref := operationref.Encode(p.BookID, p.ID)
	p.Approval = OperationReview{Flow: "page", Operation: ref, Href: "/auth/approve?operation=" + ref, Message: reviewMessage}
	return p, nil
}

func readProposal(ctx context.Context, tx pgx.Tx, s Scope, id string) (OperationProposal, error) {
	return scanProposal(tx.QueryRow(ctx, "SELECT "+proposalColumns+" FROM public.operation_proposals WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, id))
}

func canonicalOperation(operation string, input []byte) (json.RawMessage, string, error) {
	switch operation {
	case "post_journal":
		var q PostJournal
		if strictjson.Decode(input, &q) != nil || validatePost(q) != nil {
			return nil, "", ErrInvalid
		}
		return raw(q), q.RequestKey, nil
	case "reverse_journal":
		var q ReverseJournal
		if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !ValidID(q.EntryID) || !ValidDate(q.AccountingDate) || len(q.Reason) < 1 || len(q.Reason) > 4000 {
			return nil, "", ErrInvalid
		}
		return raw(q), q.RequestKey, nil
	}
	return nil, "", ErrInvalid
}

func validProposalNotes(notes []string) bool {
	if len(notes) > 16 {
		return false
	}
	for _, note := range notes {
		if len(strings.TrimSpace(note)) < 1 || len(note) > 1000 {
			return false
		}
	}
	return true
}

func operationSources(operation string, args []byte) ([]SourceInput, error) {
	sources := []SourceInput{}
	if operation == "post_journal" {
		var q PostJournal
		if strictjson.Decode(args, &q) != nil {
			return nil, ErrInvalid
		}
		sources = append(sources, q.Sources...)
	}
	return sources, nil
}

func readOperationDependencies(ctx context.Context, tx pgx.Tx, s Scope, operation string, args []byte) (operationDependencies, error) {
	var dependencies operationDependencies
	var data []byte
	err := tx.QueryRow(ctx, "SELECT public.operation_dependencies($1,$2,$3,$4::jsonb,NULL)", s.TenantID, s.BookID, operation, string(args)).Scan(&data)
	if err != nil {
		return dependencies, dbError(err)
	}
	if strictjson.Decode(data, &dependencies) != nil {
		return dependencies, ErrInvalid
	}
	return dependencies, nil
}

func operationPreview(book Book, entry Entry, sources []SourceInput, deps operationDependencies) OperationPreview {
	p := OperationPreview{Version: 1, Currency: book.Currency, Scale: book.Scale, EntriesAffected: 1, EconomicDate: entry.EconomicDate, AccountingDate: entry.AccountingDate,
		Description: entry.Description, ReversalOf: entry.ReversalOf, Lines: []PostLine{}, Sources: sources, SourceUsage: []OperationSourceUsage{}, Accounts: []OperationAccountEffect{}, Classes: []OperationClassEffect{}, MeasurementPolicy: entry.MeasurementPolicy}
	for _, doc := range deps.Documents {
		p.SourceUsage = append(p.SourceUsage, OperationSourceUsage{doc.DocumentID, doc.Location, doc.ActiveEntries})
	}
	deltas := map[string]*big.Int{}
	classes := map[string][2]*big.Int{}
	accountClass := map[string]string{}
	for _, a := range deps.Accounts {
		accountClass[a.AccountID] = a.Class
		deltas[a.AccountID] = new(big.Int)
	}
	for _, line := range entry.Lines {
		p.Lines = append(p.Lines, PostLine{AccountID: line.AccountID, Side: line.Side, AmountMinor: line.AmountMinor, Memo: line.Memo, Native: line.Native, Contexts: line.Contexts, Valuation: line.Valuation})
		amount, _ := new(big.Int).SetString(line.AmountMinor, 10)
		class := accountClass[line.AccountID]
		totals, ok := classes[class]
		if !ok {
			totals = [2]*big.Int{new(big.Int), new(big.Int)}
		}
		if line.Side == "debit" {
			totals[0].Add(totals[0], amount)
		} else {
			totals[1].Add(totals[1], amount)
			amount.Neg(amount)
		}
		classes[class] = totals
		deltas[line.AccountID].Add(deltas[line.AccountID], amount)
	}
	for _, a := range deps.Accounts {
		before, _ := new(big.Int).SetString(a.BalanceMinor, 10)
		p.Accounts = append(p.Accounts, OperationAccountEffect{a.AccountID, a.Class, a.BalanceMinor, deltas[a.AccountID].String(), new(big.Int).Add(before, deltas[a.AccountID]).String()})
	}
	for _, class := range []string{"asset", "liability", "equity", "income", "expense"} {
		if totals, ok := classes[class]; ok {
			p.Classes = append(p.Classes, OperationClassEffect{class, totals[0].String(), totals[1].String(), new(big.Int).Sub(totals[0], totals[1]).String()})
		}
	}
	return p
}

var errOperationPreview = errors.New("operation preview complete")

// maxWaitingProposals bounds one agent's undecided work in a Book. Proposals
// wait for the owner without a time limit, so the queue needs a size limit.
const maxWaitingProposals = 128

// proposalWaiting matches proposals p that still need their first decision.
const proposalWaiting = `NOT EXISTS(SELECT 1 FROM public.operation_decisions d
        WHERE d.tenant_id=p.tenant_id AND d.book_id=p.book_id AND d.proposal_id=p.id)`

func retainedPreparation(ctx context.Context, tx pgx.Tx, scope Scope, q PrepareOperation) (OperationProposal, bool, error) {
	p, err := scanProposal(tx.QueryRow(ctx, "SELECT "+proposalColumns+" FROM public.operation_proposals WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4", scope.TenantID, scope.BookID, scope.ActorID, q.RequestKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, dbError(err)
	}
	if p.Operation != q.Operation || !bytes.Equal(p.Arguments, q.Arguments) || !bytes.Equal(raw(p.Assumptions), raw(q.Assumptions)) || !bytes.Equal(raw(p.OpenQuestions), raw(q.OpenQuestions)) {
		return p, true, ErrConflict
	}
	return p, true, nil
}

func checkNewPreparation(ctx context.Context, tx pgx.Tx, scope Scope, key string) error {
	var occupied bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.requests WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4)", scope.TenantID, scope.BookID, scope.ActorID, key).Scan(&occupied); err != nil {
		return dbError(err)
	}
	if occupied {
		return ErrConflict
	}
	var full bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.operation_proposals p WHERE p.tenant_id=$1 AND p.book_id=$2 AND actor_id=$3
        AND (expires_at IS NULL OR expires_at>clock_timestamp()) AND `+proposalWaiting+` OFFSET $4)`,
		scope.TenantID, scope.BookID, scope.ActorID, maxWaitingProposals-1).Scan(&full); err != nil {
		return dbError(err)
	}
	if full {
		return ErrLimit
	}
	return nil
}

// Preparation validates a complete posting in a top-level transaction that
// always rolls back. A second transaction retains the preview only if its
// dependencies still match. No temporary financial record can be committed.
func (s Store) prepareOperation(ctx context.Context, scope Scope, input []byte) (json.RawMessage, error) {
	var q PrepareOperation
	if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || !validProposalNotes(q.Assumptions) || !validProposalNotes(q.OpenQuestions) {
		return nil, ErrInvalid
	}
	args, key, err := canonicalOperation(q.Operation, q.Arguments)
	if err != nil {
		return nil, err
	}
	q.Arguments = args
	if q.Assumptions == nil {
		q.Assumptions = []string{}
	}
	if q.OpenQuestions == nil {
		q.OpenQuestions = []string{}
	}
	var payload operationPayload
	result, err := s.transact(ctx, scope, "prepare_operation", input, func(tx pgx.Tx) (json.RawMessage, error) {
		prior, found, err := retainedPreparation(ctx, tx, scope, q)
		if err != nil || found {
			return raw(prior), err
		}
		if err = checkNewPreparation(ctx, tx, scope, key); err != nil {
			return nil, err
		}
		if s.Authorize != nil {
			if err = s.Authorize(ctx, tx, scope, q.Operation, args); err != nil {
				return nil, err
			}
		}
		dependencies, err := readOperationDependencies(ctx, tx, scope, q.Operation, args)
		if err != nil {
			return nil, err
		}
		sources, err := operationSources(q.Operation, args)
		if err != nil {
			return nil, err
		}
		effect, err := dispatch(ctx, tx, scope, q.Operation, args, nil)
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return nil, dbError(err)
		}
		var entry Entry
		if json.Unmarshal(effect, &entry) != nil {
			return nil, ErrInvalid
		}
		book, err := readBook(ctx, tx, scope)
		if err != nil {
			return nil, dbError(err)
		}
		payload = operationPayload{1, q.Operation, args, q.Assumptions, q.OpenQuestions, dependencies, operationPreview(book, entry, sources, dependencies)}
		return nil, errOperationPreview
	})
	if !errors.Is(err, errOperationPreview) {
		return result, err
	}
	data := raw(payload)
	if len(data) > 262144 {
		return nil, ErrLimit
	}
	return s.transact(ctx, scope, "prepare_operation", input, func(tx pgx.Tx) (json.RawMessage, error) {
		prior, found, err := retainedPreparation(ctx, tx, scope, q)
		if err != nil || found {
			return raw(prior), err
		}
		if err = checkNewPreparation(ctx, tx, scope, key); err != nil {
			return nil, err
		}
		if s.Authorize != nil {
			if err = s.Authorize(ctx, tx, scope, q.Operation, args); err != nil {
				return nil, err
			}
		}
		current, err := readOperationDependencies(ctx, tx, scope, q.Operation, args)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(raw(current), raw(payload.Dependencies)) {
			return nil, ErrProposalStale
		}
		if s.AdmitWrite != nil {
			if err = s.AdmitWrite(ctx, tx, scope, "prepare_operation", raw(q)); err != nil {
				return nil, err
			}
		}
		proposal, err := scanProposal(tx.QueryRow(ctx, `INSERT INTO public.operation_proposals(tenant_id,book_id,id,sequence,actor_id,connection_id,request_key,operation,operation_key,payload_bytes)
            VALUES($1,$2,$3,1,$4,$5,$6,$7,$8,$9) RETURNING `+proposalColumns, scope.TenantID, scope.BookID, ID(), scope.ActorID, scope.ConnectionID, q.RequestKey, q.Operation, key, []byte(data)))
		return raw(proposal), dbError(err)
	})
}

func (s Store) proposalCall(ctx context.Context, tx pgx.Tx, scope Scope, method string, input []byte) (json.RawMessage, error) {
	switch method {
	case "get_operation_proposal":
		var q ProposalQuery
		if strictjson.Decode(input, &q) != nil || (q.ProposalID == "") == (q.RequestKey == "") || q.ProposalID != "" && !ValidID(q.ProposalID) || q.RequestKey != "" && !validKey(q.RequestKey) {
			return nil, ErrInvalid
		}
		var p OperationProposal
		var err error
		if q.ProposalID != "" {
			p, err = readProposal(ctx, tx, scope, q.ProposalID)
		} else {
			p, err = scanProposal(tx.QueryRow(ctx, "SELECT "+proposalColumns+" FROM public.operation_proposals WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4", scope.TenantID, scope.BookID, scope.ActorID, q.RequestKey))
		}
		if err != nil {
			return nil, dbError(err)
		}
		state, err := readProposalState(ctx, tx, scope, p, true)
		if err == nil {
			state, err = withDuplicate(ctx, tx, scope, state)
		}
		return raw(state), err
	case "execute_proposal":
		var q ExecuteProposal
		if strictjson.Decode(input, &q) != nil || !ValidID(q.ProposalID) || !validProposalSHA(q.SHA256) {
			return nil, ErrInvalid
		}
		return s.executeProposal(ctx, tx, scope, q)
	}
	return nil, ErrInvalid
}
