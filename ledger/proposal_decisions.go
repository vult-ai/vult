package ledger

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/strictjson"
)

type OperationDecision struct {
	ProposalID           string `json:"proposal_id"`
	Revision             int64  `json:"revision"`
	OwnerID              string `json:"owner_id"`
	ownerEpoch           int64
	RequestKey           string     `json:"request_key"`
	SHA256               string     `json:"sha256"`
	Decision             string     `json:"decision"`
	Reason               string     `json:"reason"`
	ExecutorActorID      string     `json:"executor_actor_id,omitempty"`
	ExecutorConnectionID string     `json:"executor_connection_id,omitempty"`
	RecordedAt           time.Time  `json:"recorded_at"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
}

type DecideOperation struct {
	BookID           string `json:"book_id"`
	ProposalID       string `json:"plan_id"`
	SHA256           string `json:"sha256"`
	RequestKey       string `json:"request_key"`
	ExpectedRevision int64  `json:"expected_decision_revision"`
	Decision         string `json:"decision"`
	Reason           string `json:"reason"`
}

// OperationDecisionResult is a recorded decision and, for an approval, the
// entry it posted.
type OperationDecisionResult struct {
	Decision OperationDecision `json:"decision"`
	Status   string            `json:"status"`
	Outcome  *Outcome          `json:"outcome,omitempty"`
}

// DecisionAdmission applies deployment admission inside the decision's
// transaction: once for the decision and once for the posting it makes.
type DecisionAdmission func(ctx context.Context, tx pgx.Tx, scope Scope, operation string, input json.RawMessage) error

type ProposalState struct {
	Version  int                `json:"version"`
	Proposal OperationProposal  `json:"proposal"`
	Status   string             `json:"status"`
	Decision *OperationDecision `json:"decision,omitempty"`
	Outcome  *Outcome           `json:"outcome,omitempty"`
	// Balances are each account's balance now and after this entry, for an
	// entry not yet posted. Other entries may have moved them since it was
	// prepared; the entry itself is unchanged.
	Balances []OperationAccountEffect `json:"balances,omitempty"`
	// DuplicateOf is the sequence of a newer waiting proposal for the same
	// entry: the same date, lines and cited sources. Approving both can't post
	// twice, since the sources' use is checked, but only one needs a decision.
	DuplicateOf int64 `json:"duplicate_of,omitempty"`
}

const decisionColumns = `proposal_id::text,revision,owner_id::text,owner_epoch,request_key,sha256,decision,reason,
    coalesce(executor_actor_id::text,''),coalesce(executor_connection_id::text,''),recorded_at,expires_at`

func validProposalSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

// Acquire the approving identity lease before the Book write lock. The SQL
// consumption trigger also takes this lease for direct database callers.
func holdProposalOwner(ctx context.Context, tx pgx.Tx, s Scope, input []byte) error {
	var q ExecuteProposal
	if strictjson.Decode(input, &q) != nil || !ValidID(q.ProposalID) || !validProposalSHA(q.SHA256) {
		return ErrInvalid
	}
	var held bool
	err := tx.QueryRow(ctx, `SELECT access.hold_operation_owner(d.tenant_id,d.book_id,d.owner_id,d.owner_epoch)
        FROM public.operation_decisions d WHERE d.tenant_id=$1 AND d.book_id=$2 AND d.proposal_id=$3
        AND NOT EXISTS(SELECT 1 FROM public.operation_consumptions c WHERE c.tenant_id=d.tenant_id AND c.book_id=d.book_id AND c.proposal_id=d.proposal_id)
        ORDER BY revision DESC LIMIT 1`, s.TenantID, s.BookID, q.ProposalID).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return dbError(err)
}

func scanDecision(row pgx.Row) (OperationDecision, error) {
	var d OperationDecision
	err := row.Scan(&d.ProposalID, &d.Revision, &d.OwnerID, &d.ownerEpoch, &d.RequestKey, &d.SHA256, &d.Decision, &d.Reason,
		&d.ExecutorActorID, &d.ExecutorConnectionID, &d.RecordedAt, &d.ExpiresAt)
	return d, err
}

func proposalDependenciesMatch(ctx context.Context, tx pgx.Tx, s Scope, p OperationProposal) (bool, error) {
	matches, _, err := proposalDependenciesNow(ctx, tx, s, p)
	return matches, err
}

// proposalDependenciesNow reports whether a proposal still rests on the same
// facts, and each account's balance now and after the proposed entry.
func proposalDependenciesNow(ctx context.Context, tx pgx.Tx, s Scope, p OperationProposal) (bool, []OperationAccountEffect, error) {
	deps, err := readOperationDependencies(ctx, tx, s, p.Operation, p.Arguments)
	if err != nil {
		return false, nil, err
	}
	now := map[string]string{}
	for _, a := range deps.Accounts {
		now[a.AccountID] = a.BalanceMinor
	}
	balances := []OperationAccountEffect{}
	for _, a := range p.Preview.Accounts {
		before, ok := new(big.Int).SetString(now[a.AccountID], 10)
		change, ok2 := new(big.Int).SetString(a.ChangeMinor, 10)
		if !ok || !ok2 {
			continue
		}
		balances = append(balances, OperationAccountEffect{a.AccountID, a.Class, before.String(), change.String(), new(big.Int).Add(before, change).String()})
	}
	return bytes.Equal(raw(deps.settled()), raw(p.payload.Dependencies.settled())), balances, nil
}

func readProposalState(ctx context.Context, tx pgx.Tx, s Scope, p OperationProposal, checkDependencies bool) (ProposalState, error) {
	state := ProposalState{Version: 1, Proposal: p, Status: "approval_required"}
	var operation string
	var result []byte
	err := tx.QueryRow(ctx, `SELECT r.operation,r.result_bytes FROM public.operation_consumptions c JOIN public.requests r
        ON r.tenant_id=c.tenant_id AND r.book_id=c.book_id AND r.id=c.receipt_id
        WHERE c.tenant_id=$1 AND c.book_id=$2 AND c.proposal_id=$3`, s.TenantID, s.BookID, p.ID).Scan(&operation, &result)
	if err == nil {
		state.Status = "committed"
		state.Outcome = &Outcome{Status: "recorded", Operation: operation, Result: result}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return state, dbError(err)
	}
	decision, err := scanDecision(tx.QueryRow(ctx, "SELECT "+decisionColumns+" FROM public.operation_decisions WHERE tenant_id=$1 AND book_id=$2 AND proposal_id=$3 ORDER BY revision DESC LIMIT 1", s.TenantID, s.BookID, p.ID))
	if err == nil {
		state.Decision = &decision
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return state, dbError(err)
	}
	if state.Status == "committed" {
		return state, nil
	}
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return state, dbError(err)
	}
	if p.ExpiresAt != nil && !p.ExpiresAt.After(now) {
		state.Status = "expired"
		return state, nil
	}
	if state.Decision != nil {
		if decision.Decision == "reject" {
			state.Status = "rejected"
			return state, nil
		}
		// An owner's approval posts in its own transaction, so a visible approval
		// without a posting can only be one recorded for an agent to execute.
		if decision.ExpiresAt == nil || !decision.ExpiresAt.After(now) {
			state.Status = "expired"
			return state, nil
		}
		var active bool
		err = tx.QueryRow(ctx, `SELECT access.operation_executor_active($1,$2,$3,$4)
            AND access.operation_owner_allowed($1,$2,$5,$6)`, s.TenantID, s.BookID, decision.ExecutorConnectionID, decision.ExecutorActorID, decision.OwnerID, decision.ownerEpoch).Scan(&active)
		if err != nil {
			return state, dbError(err)
		}
		if !active {
			state.Status = "revoked"
			return state, nil
		}
		state.Status = "approved"
	}
	if checkDependencies {
		matches, balances, err := proposalDependenciesNow(ctx, tx, s, p)
		if err != nil {
			return state, err
		}
		if !matches {
			state.Status = "stale"
		} else {
			state.Balances = balances
		}
	}
	return state, nil
}

// withDuplicate marks a waiting proposal that a newer waiting one repeats: the
// same date, lines and cited sources. It is for showing the queue; approving
// either can't post twice, since the sources' use is checked.
func withDuplicate(ctx context.Context, tx pgx.Tx, s Scope, state ProposalState) (ProposalState, error) {
	if state.Status != "approval_required" {
		return state, nil
	}
	// Each payload is read once; newer proposals come from the sequence index.
	err := tx.QueryRow(ctx, `WITH p AS (
            SELECT tenant_id,book_id,sequence,operation,convert_from(payload_bytes,'UTF8')::jsonb->'preview' AS preview
            FROM public.operation_proposals WHERE tenant_id=$1 AND book_id=$2 AND id=$3),
        q AS (
            SELECT q.sequence,convert_from(q.payload_bytes,'UTF8')::jsonb->'preview' AS preview
            FROM public.operation_proposals q JOIN p ON q.tenant_id=p.tenant_id AND q.book_id=p.book_id
            WHERE q.sequence>p.sequence AND q.operation=p.operation
            AND (q.expires_at IS NULL OR q.expires_at>clock_timestamp())
            AND NOT EXISTS(SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id=q.tenant_id AND d.book_id=q.book_id AND d.proposal_id=q.id))
        SELECT coalesce(max(q.sequence),0) FROM q, p
        WHERE q.preview->'lines'=p.preview->'lines' AND q.preview->'sources'=p.preview->'sources'
            AND q.preview->>'accounting_date'=p.preview->>'accounting_date'`,
		s.TenantID, s.BookID, state.Proposal.ID).Scan(&state.DuplicateOf)
	return state, dbError(err)
}

func (s Store) executeProposal(ctx context.Context, tx pgx.Tx, scope Scope, q ExecuteProposal) (json.RawMessage, error) {
	p, err := readProposal(ctx, tx, scope, q.ProposalID)
	if err != nil {
		return nil, dbError(err)
	}
	if p.SHA256 != q.SHA256 {
		return nil, ErrProposalStale
	}
	var result, accepted []byte
	var actor, operation string
	err = tx.QueryRow(ctx, `SELECT c.actor_id::text,r.operation,r.request_bytes,r.result_bytes FROM public.operation_consumptions c
        JOIN public.requests r ON r.tenant_id=c.tenant_id AND r.book_id=c.book_id AND r.id=c.receipt_id
        WHERE c.tenant_id=$1 AND c.book_id=$2 AND c.proposal_id=$3`, scope.TenantID, scope.BookID, p.ID).Scan(&actor, &operation, &accepted, &result)
	if err == nil {
		if actor != scope.ActorID {
			return nil, ErrDenied
		}
		if operation != p.Operation || !bytes.Equal(accepted, p.Arguments) {
			return nil, ErrInvalid
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, dbError(err)
	}
	state, err := readProposalState(ctx, tx, scope, p, true)
	if err != nil {
		return nil, err
	}
	switch state.Status {
	case "expired":
		return nil, ErrProposalExpired
	case "stale":
		return nil, ErrProposalStale
	case "approved":
	default:
		return nil, ErrApprovalRequired
	}
	d := state.Decision
	if d.ExecutorActorID != scope.ActorID || d.ExecutorConnectionID != scope.ConnectionID {
		return nil, ErrDenied
	}
	var occupied bool
	err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.requests WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4)", scope.TenantID, scope.BookID, scope.ActorID, p.OperationKey).Scan(&occupied)
	if err != nil {
		return nil, dbError(err)
	}
	if occupied {
		return nil, ErrConflict
	}
	if s.Authorize != nil {
		if err = s.Authorize(ctx, tx, scope, p.Operation, p.Arguments); err != nil {
			return nil, err
		}
	}
	result, err = dispatch(ctx, tx, scope, p.Operation, p.Arguments, s.AdmitWrite)
	if err != nil {
		return nil, err
	}
	var entry Entry
	if json.Unmarshal(result, &entry) != nil {
		return nil, ErrInvalid
	}
	book, err := readBook(ctx, tx, scope)
	if err != nil {
		return nil, dbError(err)
	}
	sources, err := operationSources(p.Operation, p.Arguments)
	if err != nil {
		return nil, err
	}
	preview := operationPreview(book, entry, sources, p.payload.Dependencies)
	if !bytes.Equal(raw(preview), raw(p.Preview)) {
		return nil, ErrProposalStale
	}
	_, err = tx.Exec(ctx, `INSERT INTO public.operation_consumptions(tenant_id,book_id,proposal_id,decision_revision,receipt_id,actor_id)
        VALUES($1,$2,$3,$4,$5,$6)`, scope.TenantID, scope.BookID, p.ID, d.Revision, entry.ReceiptID, scope.ActorID)
	return result, dbError(err)
}

// decisionTx uses the owner's own transaction when one is bound, so rows the
// approval writes belong to that transaction rather than to a savepoint.
func (a TenantAdmin) decisionTx(ctx context.Context) (pgx.Tx, bool, error) {
	if bound, ok := a.db.(transactionDatabase); ok {
		return bound.Tx, false, nil
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	return tx, true, err
}

func readDecisionOutcome(ctx context.Context, tx pgx.Tx, s Scope, d OperationDecision) (OperationDecisionResult, error) {
	out := OperationDecisionResult{Decision: d, Status: "rejected"}
	if d.Decision != "approve" {
		return out, nil
	}
	var operation string
	var result []byte
	err := tx.QueryRow(ctx, `SELECT r.operation,r.result_bytes FROM public.operation_consumptions c JOIN public.requests r
        ON r.tenant_id=c.tenant_id AND r.book_id=c.book_id AND r.id=c.receipt_id
        WHERE c.tenant_id=$1 AND c.book_id=$2 AND c.proposal_id=$3 AND c.decision_revision=$4`, s.TenantID, s.BookID, d.ProposalID, d.Revision).Scan(&operation, &result)
	if err != nil {
		return out, dbError(err)
	}
	out.Status = "committed"
	out.Outcome = &Outcome{Status: "recorded", Operation: operation, Result: result}
	return out, nil
}

// DecideOperation appends a reasoned owner decision. An approval posts the
// exact prepared entry in the same transaction, after rechecking its
// dependencies; if they changed, nothing is recorded or posted. The entry's
// receipt keeps the preparing agent as author, and the consumption records the
// approving owner. A rejection records the reason and posts nothing.
func (a TenantAdmin) DecideOperation(ctx context.Context, ownerID string, q DecideOperation, admit DecisionAdmission) (OperationDecisionResult, error) {
	var out OperationDecisionResult
	if !a.valid() || !ValidID(ownerID) || !ValidID(q.BookID) || !ValidID(q.ProposalID) || !validProposalSHA(q.SHA256) || !validKey(q.RequestKey) || q.ExpectedRevision < 0 ||
		(q.Decision != "approve" && q.Decision != "reject") || len(strings.TrimSpace(q.Reason)) < 1 || len(q.Reason) > 4000 {
		return out, ErrInvalid
	}
	tx, owned, err := a.decisionTx(ctx)
	if err != nil {
		return out, dbError(err)
	}
	commit := func() error {
		if owned {
			return dbError(tx.Commit(ctx))
		}
		return nil
	}
	if owned {
		defer tx.Rollback(context.Background())
	}
	if _, err = tx.Exec(ctx, "SELECT set_config('vult.actor_id','',true),set_config('vult.connection_id','',true),set_config('vult.owner_posting','',true)"); err != nil {
		return out, dbError(err)
	}
	scope := Scope{TenantID: a.tenantID, BookID: q.BookID}
	if err = setScope(ctx, tx, scope); err != nil {
		return out, dbError(err)
	}
	var who string
	if err = tx.QueryRow(ctx, "SELECT access.require_owner($1,false)::text", a.tenantID).Scan(&who); err != nil || who != ownerID {
		return out, ErrDenied
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "operation-decision:"+a.tenantID+":"+ownerID+":"+q.RequestKey); err != nil {
		return out, dbError(err)
	}
	decision, err := scanDecision(tx.QueryRow(ctx, "SELECT "+decisionColumns+" FROM public.operation_decisions WHERE tenant_id=$1 AND book_id=$2 AND owner_id=$3 AND request_key=$4", a.tenantID, q.BookID, ownerID, q.RequestKey))
	if err == nil {
		if decision.ProposalID != q.ProposalID || decision.SHA256 != q.SHA256 || decision.Decision != q.Decision || decision.Reason != q.Reason || decision.Revision != q.ExpectedRevision+1 {
			return out, ErrConflict
		}
		if out, err = readDecisionOutcome(ctx, tx, scope, decision); err != nil {
			return out, err
		}
		return out, commit()
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, dbError(err)
	}
	var fresh bool
	if err = tx.QueryRow(ctx, "SELECT access.owner_write_allowed($1)", a.tenantID).Scan(&fresh); err != nil {
		return out, dbError(err)
	}
	if !fresh {
		return out, ErrFreshOwnerProof
	}
	if err = tx.QueryRow(ctx, "SELECT access.require_owner($1,true)::text", a.tenantID).Scan(&who); err != nil || who != ownerID {
		return out, ErrDenied
	}
	if _, err = tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", a.tenantID, q.BookID); err != nil {
		return out, dbError(err)
	}
	p, err := readProposal(ctx, tx, scope, q.ProposalID)
	if err != nil {
		return out, dbError(err)
	}
	if p.SHA256 != q.SHA256 {
		return out, ErrProposalStale
	}
	state, err := readProposalState(ctx, tx, scope, p, false)
	if err != nil {
		return out, err
	}
	if state.Status == "committed" {
		return out, ErrProposalDecided
	}
	var proposalExpired bool
	if err = tx.QueryRow(ctx, "SELECT $1::timestamptz IS NOT NULL AND $1::timestamptz<=clock_timestamp()", p.ExpiresAt).Scan(&proposalExpired); err != nil {
		return out, dbError(err)
	}
	if proposalExpired {
		return out, ErrProposalExpired
	}
	if q.Decision == "approve" {
		matches, e := proposalDependenciesMatch(ctx, tx, scope, p)
		if e != nil {
			return out, e
		}
		if !matches {
			return out, ErrProposalStale
		}
	}
	revision := int64(0)
	if state.Decision != nil {
		revision = state.Decision.Revision
	}
	if revision != q.ExpectedRevision {
		return out, ErrProposalStale
	}
	if admit != nil {
		if err = admit(ctx, tx, scope, "decide_operation", raw(q)); err != nil {
			return out, err
		}
	}
	decision, err = scanDecision(tx.QueryRow(ctx, `INSERT INTO public.operation_decisions(tenant_id,book_id,proposal_id,revision,owner_id,owner_epoch,request_key,sha256,decision,reason)
        VALUES($1,$2,$3,$4,$5,0,$6,$7,$8,$9) RETURNING `+decisionColumns,
		a.tenantID, q.BookID, q.ProposalID, revision+1, ownerID, q.RequestKey, q.SHA256, q.Decision, q.Reason))
	if err != nil {
		return out, dbError(err)
	}
	out = OperationDecisionResult{Decision: decision, Status: "rejected"}
	if q.Decision == "approve" {
		if out.Outcome, err = a.postApproved(ctx, tx, p, decision, admit); err != nil {
			return OperationDecisionResult{}, err
		}
		out.Status = "committed"
	}
	return out, commit()
}

// postApproved posts the approved proposal under the preparing agent's actor
// and financial request key. Database policy admits these writes only for the
// proposal this owner approved in this transaction.
func (a TenantAdmin) postApproved(ctx context.Context, tx pgx.Tx, p OperationProposal, d OperationDecision, admit DecisionAdmission) (*Outcome, error) {
	author := Scope{TenantID: a.tenantID, BookID: p.BookID, ActorID: p.ActorID, ConnectionID: p.ConnectionID, Access: "write"}
	var occupied bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.requests WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND request_key=$4)", author.TenantID, author.BookID, author.ActorID, p.OperationKey).Scan(&occupied); err != nil {
		return nil, dbError(err)
	}
	if occupied {
		return nil, ErrConflict
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('vult.actor_id',$1,true),set_config('vult.connection_id',$2,true),set_config('vult.owner_posting',$3,true)", author.ActorID, author.ConnectionID, p.ID); err != nil {
		return nil, dbError(err)
	}
	if admit != nil {
		if err := admit(ctx, tx, author, p.Operation, p.Arguments); err != nil {
			return nil, err
		}
	}
	result, err := dispatch(ctx, tx, author, p.Operation, p.Arguments, nil)
	if err != nil {
		return nil, err
	}
	var entry Entry
	if json.Unmarshal(result, &entry) != nil {
		return nil, ErrInvalid
	}
	book, err := readBook(ctx, tx, author)
	if err != nil {
		return nil, dbError(err)
	}
	sources, err := operationSources(p.Operation, p.Arguments)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(raw(operationPreview(book, entry, sources, p.payload.Dependencies)), raw(p.Preview)) {
		return nil, ErrProposalStale
	}
	if _, err = tx.Exec(ctx, `INSERT INTO public.operation_consumptions(tenant_id,book_id,proposal_id,decision_revision,receipt_id,actor_id)
        VALUES($1,$2,$3,$4,$5,$6)`, author.TenantID, author.BookID, p.ID, d.Revision, entry.ReceiptID, d.OwnerID); err != nil {
		return nil, dbError(err)
	}
	return &Outcome{Status: "recorded", Operation: p.Operation, Result: result}, nil
}
