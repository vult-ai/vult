package ledger

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Recording rules are the owner's own decisions, made once and repeated:
// money moving one way on one of the owner's accounts, from one
// counterparty, within an amount range, records to one other account. Only
// the owner creates, changes or turns them off. A connection the owner
// grants it to records bank activity directly only by an active rule, as a
// transfer, or as a match to an existing entry; the database checks each.

type RecordingRule struct {
	ID              string    `json:"id"`
	AccountID       string    `json:"account_id"`
	Direction       string    `json:"direction"`
	Counterparty    string    `json:"counterparty"`
	OtherAccountID  string    `json:"other_account_id"`
	MinMinor        string    `json:"min_minor"`
	MaxMinor        string    `json:"max_minor"`
	MinDaysBetween  int       `json:"min_days_between"`
	Description     string    `json:"description"`
	Active          bool      `json:"active"`
	Revision        int64     `json:"revision"`
	UpdatedAt       time.Time `json:"updated_at"`
	RecordedEntries int64     `json:"recorded_entries"`
	// Parts split an item; the rest goes to OtherAccountID.
	Parts []RulePart `json:"parts,omitempty"`
}

// RecordByRule records one unrecorded bank item by an active rule: a
// two-line entry moving the item's account by its amount, the other side to
// the rule's account, linked with the rule as its reason.
type RecordByRule struct {
	RequestKey    string `json:"request_key"`
	TransactionID string `json:"transaction_id"`
	RuleID        string `json:"rule_id"`
}

// ruleRecordingMethods are what a connection granted recording within the
// rules may take directly; the database limits each to its basis.
// record_bank_activity is direct only under a confident grant; the ledger
// refuses it otherwise, and the agent prepares a proposal instead.
var ruleRecordingMethods = map[string]bool{"record_by_rule": true, "record_bank_transfer": true, "link_bank_activity": true, "record_bank_activity": true}

// RuleRecordingMethod reports a method a granted proposing connection may
// take directly, within the owner's rules.
func RuleRecordingMethod(method string) bool { return ruleRecordingMethods[method] }

const ruleColumns = `r.id::text,r.account_id::text,r.direction,r.counterparty_key,r.other_account_id::text,r.min_minor::text,r.max_minor::text,
 r.min_days_between,r.description,r.active,r.revision,r.updated_at,
 (SELECT count(*) FROM public.bank_transaction_links l WHERE l.tenant_id=r.tenant_id AND l.book_id=r.book_id AND l.rule_id=r.id AND l.revoked_at IS NULL)`

func scanRule(row pgx.Row) (RecordingRule, error) {
	var v RecordingRule
	err := row.Scan(&v.ID, &v.AccountID, &v.Direction, &v.Counterparty, &v.OtherAccountID, &v.MinMinor, &v.MaxMinor, &v.MinDaysBetween,
		&v.Description, &v.Active, &v.Revision, &v.UpdatedAt, &v.RecordedEntries)
	return v, err
}

func readRules(ctx context.Context, tx pgx.Tx, s Scope) ([]RecordingRule, error) {
	rows, err := tx.Query(ctx, "SELECT "+ruleColumns+" FROM public.recording_rules r WHERE r.tenant_id=$1 AND r.book_id=$2 ORDER BY r.active DESC,r.counterparty_key,r.id", s.TenantID, s.BookID)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []RecordingRule{}
	for rows.Next() {
		v, err := scanRule(rows)
		if err != nil {
			return nil, dbError(err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError(err)
	}
	rows.Close()
	ids := make([]string, len(out))
	for i, r := range out {
		ids[i] = r.ID
	}
	parts, err := readRuleParts(ctx, tx, s, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Parts = parts[out[i].ID]
	}
	return out, nil
}

func recordByRule(ctx context.Context, tx pgx.Tx, s Scope, q RecordByRule, receipt string) (BankLinkResult, error) {
	v, err := readBankItem(ctx, tx, s, q.TransactionID)
	if err != nil {
		return BankLinkResult{}, err
	}
	if err = v.recordable(); err != nil {
		return BankLinkResult{}, err
	}
	r, err := readRule(ctx, tx, s, q.RuleID)
	if err != nil {
		return BankLinkResult{}, err
	}
	if !r.Active {
		return BankLinkResult{}, &RefusedError{Reason: "This rule is turned off; prepare the entry as a proposal."}
	}
	amount := v.amount
	if amount < 0 {
		amount = -amount
	}
	if err := ruleCovers(ctx, tx, s, r, v, amount); err != nil {
		return BankLinkResult{}, err
	}
	lines, split := itemLines(v, r.OtherAccountID), ""
	if len(r.Parts) > 0 {
		if lines, split, err = splitItem(ctx, tx, s, r, v, amount); err != nil {
			return BankLinkResult{}, err
		}
	}
	post := PostJournal{RequestKey: q.RequestKey + "/entry", EconomicDate: v.date, AccountingDate: v.date, Description: r.Description, Lines: lines}
	entry, err := postBankEntry(ctx, tx, s, post)
	if err != nil {
		return BankLinkResult{}, err
	}
	link := BankLinkRecord{ID: ID(), TransactionID: v.id, EntryID: entry.ID, Reason: clip("By the rule: "+r.Description+ruleSplitWords(split), 4000)}
	if _, err := tx.Exec(ctx, `INSERT INTO public.bank_transaction_links(tenant_id,book_id,id,transaction_id,bank_account_id,account_id,entry_id,actor_id,receipt_id,basis,reason,rule_id)
 VALUES($1,$2,$3,$4,nullif($5,'')::uuid,$6,$7,$8,$9,'rule',$10,$11)`, s.TenantID, s.BookID, link.ID, v.id, v.bankAccount, v.account, entry.ID, s.ActorID, receipt,
		link.Reason, r.ID); err != nil {
		return BankLinkResult{}, err
	}
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS rule_link_final IMMEDIATE"); err != nil {
		return BankLinkResult{}, err
	}
	return BankLinkResult{Links: []BankLinkRecord{link}, Entry: &entry}, nil
}

func validRecordByRule(input []byte) (RecordByRule, error) {
	var q RecordByRule
	if strictjson.Decode(input, &q) != nil || !validKey(q.RequestKey) || len(q.RequestKey) > 120 || !ValidID(q.TransactionID) || !ValidID(q.RuleID) {
		return q, ErrInvalid
	}
	return q, nil
}

// RuleFromActivity makes a rule from a decision the owner already made: an
// item recorded by an entry of exactly two lines. The rule repeats that
// decision for the same counterparty, direction and accounts, within the
// amount range the owner sets.
type RuleFromActivity struct {
	TransactionID  string `json:"transaction_id"`
	MinMinor       string `json:"min_minor,omitempty"`
	MaxMinor       string `json:"max_minor,omitempty"`
	MinDaysBetween int    `json:"min_days_between,omitempty"`
	Description    string `json:"description,omitempty"`
}

// CreateRecordingRule records a rule from the owner's recorded decision.
func (a TenantAdmin) CreateRecordingRule(ctx context.Context, book, actor string, q RuleFromActivity) (RecordingRule, error) {
	var out RecordingRule
	if !ValidID(q.TransactionID) || q.MinDaysBetween < 0 || q.MinDaysBetween > 366 || len(q.Description) > 400 {
		return out, ErrInvalid
	}
	err := a.withRuleOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var account, other, counterparty, description, entry string
		var amount int64
		var lines int
		err := tx.QueryRow(ctx, `SELECT l.account_id::text,t.counterparty,t.description,t.amount_minor,l.entry_id::text,
 (SELECT count(*) FROM public.lines x WHERE x.tenant_id=l.tenant_id AND x.book_id=l.book_id AND x.entry_id=l.entry_id),
 coalesce((SELECT x.account_id::text FROM public.lines x WHERE x.tenant_id=l.tenant_id AND x.book_id=l.book_id AND x.entry_id=l.entry_id AND x.account_id<>l.account_id LIMIT 1),'')
 FROM public.bank_transaction_links l JOIN public.bank_transactions t ON t.tenant_id=l.tenant_id AND t.book_id=l.book_id AND t.id=l.transaction_id
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.transaction_id=$3 AND l.revoked_at IS NULL AND l.basis IN ('judgement','existing_entry','rule')`,
			s.TenantID, s.BookID, q.TransactionID).Scan(&account, &counterparty, &description, &amount, &entry, &lines, &other)
		if errors.Is(err, pgx.ErrNoRows) {
			return &RefusedError{Reason: "Make a rule from activity that's already recorded."}
		}
		if err != nil {
			return dbError(err)
		}
		if lines != 2 || other == "" {
			return &RefusedError{Reason: "A rule repeats a simple entry: one line for the bank account and one for the other account."}
		}
		direction := "in"
		if amount < 0 {
			direction, amount = "out", -amount
		}
		lo, hi := amount, amount
		if q.MinMinor != "" || q.MaxMinor != "" {
			var ok1, ok2 bool
			lo, ok1 = minorInt(q.MinMinor)
			hi, ok2 = minorInt(q.MaxMinor)
			if !ok1 || !ok2 || lo <= 0 || hi < lo {
				return ErrInvalid
			}
		}
		if strings.TrimSpace(counterparty) == "" {
			counterparty = description
		}
		var key string
		if err := tx.QueryRow(ctx, "SELECT public.rule_key($1)", counterparty).Scan(&key); err != nil {
			return dbError(err)
		}
		if key == "" {
			return &RefusedError{Reason: "This activity names no counterparty a rule could recognise."}
		}
		if q.Description == "" {
			// Named as the bank shows it, not by its matching key.
			q.Description = clip(strings.Join(strings.Fields(counterparty), " "), 400)
		}
		id := ID()
		if _, err := tx.Exec(ctx, `INSERT INTO public.recording_rules(tenant_id,book_id,id,account_id,direction,counterparty_key,other_account_id,min_minor,max_minor,min_days_between,description,actor_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, s.TenantID, s.BookID, id, account, direction, clip(key, 200), other, lo, hi, q.MinDaysBetween, q.Description, actor); err != nil {
			return bankError(err)
		}
		out, err = scanRule(tx.QueryRow(ctx, "SELECT "+ruleColumns+" FROM public.recording_rules r WHERE r.tenant_id=$1 AND r.book_id=$2 AND r.id=$3", s.TenantID, s.BookID, id))
		return dbError(err)
	})
	return out, err
}

// SetRecordingRule turns a rule on or off.
func (a TenantAdmin) SetRecordingRule(ctx context.Context, book, actor, rule string, active bool) error {
	if !ValidID(rule) {
		return ErrInvalid
	}
	return a.withAgentNarrowing(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		tag, err := tx.Exec(ctx, "UPDATE public.recording_rules SET active=$4,actor_id=$5 WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, rule, active, actor)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return bankError(err)
	})
}

// GrantRuleRecording lets a proposing connection record within the rules,
// or stops it.
func (a TenantAdmin) GrantRuleRecording(ctx context.Context, book, actor, connection string, on bool) error {
	if !ValidID(connection) {
		return ErrInvalid
	}
	return a.withAgentNarrowing(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var err error
		if on {
			_, err = tx.Exec(ctx, `INSERT INTO public.rule_recording_grants(tenant_id,book_id,connection_id,granted_by) VALUES($1,$2,$3,$4)
 ON CONFLICT (tenant_id,book_id,connection_id) DO UPDATE SET revoked_at=NULL,granted_by=EXCLUDED.granted_by,granted_at=clock_timestamp()`, s.TenantID, s.BookID, connection, actor)
		} else {
			_, err = tx.Exec(ctx, "UPDATE public.rule_recording_grants SET revoked_at=clock_timestamp() WHERE tenant_id=$1 AND book_id=$2 AND connection_id=$3 AND revoked_at IS NULL", s.TenantID, s.BookID, connection)
		}
		return bankError(err)
	})
}

// RuleGrant is a proposing connection and whether it records within rules.
type RuleGrant struct {
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Granted      bool   `json:"granted"`
	// Mode is how it records directly when granted: by rules, or also when
	// confident within the Book's limits.
	Mode string `json:"mode,omitempty"`
}

// RecordingRules lists the Book's rules and its proposing connections with
// their grant, for the owner.
func (a TenantAdmin) RecordingRules(ctx context.Context, book string) ([]RecordingRule, []RuleGrant, error) {
	var rules []RecordingRule
	grants := []RuleGrant{}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		if rules, err = readRules(ctx, tx, s); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT c.id::text,c.name,coalesce((SELECT g.recording_mode FROM public.rule_recording_grants g WHERE g.tenant_id=c.tenant_id AND g.book_id=c.book_id
 AND g.connection_id=c.id AND g.revoked_at IS NULL),'') FROM public.connections c WHERE c.tenant_id=$1 AND c.book_id=$2 AND c.access='propose' AND c.revoked_at IS NULL ORDER BY c.name,c.id`, s.TenantID, s.BookID)
		if err != nil {
			return dbError(err)
		}
		defer rows.Close()
		for rows.Next() {
			var g RuleGrant
			if err := rows.Scan(&g.ConnectionID, &g.Name, &g.Mode); err != nil {
				return dbError(err)
			}
			g.Granted = g.Mode != ""
			grants = append(grants, g)
		}
		return dbError(rows.Err())
	})
	return rules, grants, err
}

func (a TenantAdmin) withRuleOwner(ctx context.Context, book, actor string, call func(pgx.Tx, Scope) error) error {
	return a.withRuleOwnerChange(ctx, book, actor, false, call)
}

// withAgentNarrowing runs a change that may only narrow what agents may do,
// such as turning a rule off or lowering the limit. Any valid sign-in may
// make it, as disconnecting an agent; the database admits it only when it
// narrows, and anything wider asks for a recent sign-in.
func (a TenantAdmin) withAgentNarrowing(ctx context.Context, book, actor string, call func(pgx.Tx, Scope) error) error {
	return a.withRuleOwnerChange(ctx, book, actor, true, call)
}

func (a TenantAdmin) withRuleOwnerChange(ctx context.Context, book, actor string, narrowing bool, call func(pgx.Tx, Scope) error) error {
	if !ValidID(actor) {
		return ErrDenied
	}
	return a.withBookTransaction(ctx, book, pgx.ReadCommitted, func(tx pgx.Tx, scope Scope, _ Book) error {
		scope.ActorID = actor
		if err := setScope(ctx, tx, scope); err != nil {
			return dbError(err)
		}
		var fresh, signedIn bool
		if err := tx.QueryRow(ctx, "SELECT public.rule_owner_allowed($1,$2),public.rule_owner_narrowing($1,$2)", scope.TenantID, scope.BookID).Scan(&fresh, &signedIn); err != nil {
			return ErrFreshOwnerProof
		}
		if !fresh && !(narrowing && signedIn) {
			return ErrFreshOwnerProof
		}
		if _, err := tx.Exec(ctx, "SELECT public.ledger_lock($1,$2)", scope.TenantID, scope.BookID); err != nil {
			return dbError(err)
		}
		err := call(tx, scope)
		if !fresh && errors.Is(err, ErrDenied) {
			// Not a narrowing change: it needs a recent sign-in.
			return ErrFreshOwnerProof
		}
		return err
	})
}

// AgentProposal is one of this connection's proposals with its outcome, so
// an agent can see later which the owner approved or rejected, and why.
type AgentProposal struct {
	Sequence       int64                 `json:"sequence"`
	ProposalID     string                `json:"proposal_id"`
	RequestKey     string                `json:"request_key"`
	Operation      string                `json:"operation"`
	Description    string                `json:"description"`
	Status         string                `json:"status"`
	Decision       string                `json:"decision,omitempty"`
	Reason         string                `json:"reason,omitempty"`
	CreatedAt      time.Time             `json:"created_at"`
	TransactionIDs []string              `json:"transaction_ids,omitempty"`
	ActivityKind   *ProposalActivityKind `json:"activity_kind,omitempty"`
	ReassignedTo   string                `json:"reassigned_to,omitempty"`
}

// ProposalActivityKind identifies the ledger kind of a proposed bank item.
// Direction follows the linked account's balance, as list_bank_activity does.
type ProposalActivityKind struct {
	AccountID    string `json:"account_id"`
	AccountClass string `json:"account_class"`
	Direction    string `json:"direction"`
	Counterparty string `json:"counterparty"`
}

// readOwnProposals lists the latest proposals this connection's actor
// prepared, newest first.
func readOwnProposals(ctx context.Context, tx pgx.Tx, s Scope, before int64, limit int) ([]AgentProposal, error) {
	rows, err := tx.Query(ctx, "SELECT "+proposalColumns+" FROM public.operation_proposals WHERE tenant_id=$1 AND book_id=$2 AND actor_id=$3 AND ($5::bigint=0 OR sequence<$5) ORDER BY sequence DESC LIMIT $4",
		s.TenantID, s.BookID, s.ActorID, limit, before)
	if err != nil {
		return nil, dbError(err)
	}
	var proposals []OperationProposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			rows.Close()
			return nil, dbError(err)
		}
		proposals = append(proposals, p)
	}
	readErr := rows.Err()
	rows.Close()
	if readErr != nil {
		return nil, dbError(readErr)
	}
	out := []AgentProposal{}
	for _, p := range proposals {
		state, err := readProposalState(ctx, tx, s, p, false)
		if err != nil {
			return nil, err
		}
		v := AgentProposal{Sequence: p.Sequence, ProposalID: p.ID, RequestKey: p.RequestKey, Operation: p.Operation, Description: p.Preview.Description, Status: state.Status, CreatedAt: p.CreatedAt}
		if state.Decision != nil {
			v.Decision, v.Reason = state.Decision.Decision, state.Decision.Reason
			v.ReassignedTo = state.Decision.ReassignedTo
		}
		if change := p.Preview.BankChange; change != nil {
			v.TransactionIDs = change.TransactionIDs
			if p.Operation == "record_bank_activity" && len(change.TransactionIDs) == 1 {
				var kind ProposalActivityKind
				err := tx.QueryRow(ctx, `SELECT a.id::text,a.class,CASE WHEN x.amount_minor>0 THEN 'in' ELSE 'out' END,
left(public.rule_key(coalesce(nullif(x.counterparty,''),x.description)),200)
FROM public.bank_transactions x
LEFT JOIN public.bank_accounts b ON b.tenant_id=x.tenant_id AND b.book_id=x.book_id AND b.id=x.bank_account_id
JOIN public.accounts a ON a.tenant_id=x.tenant_id AND a.book_id=x.book_id AND a.id=coalesce(b.account_id,x.account_id)
WHERE x.tenant_id=$1 AND x.book_id=$2 AND x.id=$3`, s.TenantID, s.BookID, change.TransactionIDs[0]).Scan(&kind.AccountID, &kind.AccountClass, &kind.Direction, &kind.Counterparty)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, dbError(err)
				}
				if err == nil {
					v.ActivityKind = &kind
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// ruleCovers says which of the rule's conditions an item fails, so the
// caller can tell a wrong rule from an item no rule covers. The ledger
// checks the same conditions again when the link is made.
func ruleCovers(ctx context.Context, tx pgx.Tx, s Scope, r RecordingRule, v bankItem, amount int64) error {
	var key, accountName string
	if err := tx.QueryRow(ctx, `SELECT public.rule_key(coalesce(nullif(t.counterparty,''),t.description)),
 coalesce((SELECT a.name FROM public.accounts a WHERE a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=$4),'')
 FROM public.bank_transactions t WHERE t.tenant_id=$1 AND t.book_id=$2 AND t.id=$3`, s.TenantID, s.BookID, v.id, r.AccountID).Scan(&key, &accountName); err != nil {
		return dbError(err)
	}
	var failed []string
	if r.AccountID != v.account {
		failed = append(failed, "account: the rule is for "+ruleQuoted(accountName)+" and this item is on another account")
	}
	if direction := ruleDirection(v.amount); direction != r.Direction {
		failed = append(failed, "direction: the rule is for "+directionWords(r.Direction)+", and this item is "+direction)
	}
	if key != r.Counterparty {
		failed = append(failed, "counterparty: the rule's is "+ruleQuoted(r.Counterparty)+" and this item's is "+ruleQuoted(key))
	}
	min, _ := strconv.ParseInt(r.MinMinor, 10, 64)
	max, _ := strconv.ParseInt(r.MaxMinor, 10, 64)
	if amount < min || amount > max {
		failed = append(failed, "amount: the rule covers "+formatMinor(r.MinMinor, v.scale)+" to "+formatMinor(r.MaxMinor, v.scale)+
			" and this item is "+formatMinor(strconv.FormatInt(amount, 10), v.scale))
	}
	if len(failed) == 0 {
		return nil
	}
	return &RefusedError{Reason: "This activity isn't covered by the rule. " + capitalize(strings.Join(failed, "; ")) + ". Prepare it as a proposal, or use a rule that covers it."}
}

// ruleDirection is "in" when the item raises its account's balance: a
// deposit to a bank account, or a charge on a card. "out" lowers it: a
// payment from a bank account, or a payment or refund on a card.
func ruleDirection(amount int64) string {
	if amount > 0 {
		return "in"
	}
	return "out"
}

func directionWords(direction string) string {
	if direction == "in" {
		return "in (the account's balance rises: a deposit to a bank account, a charge on a card)"
	}
	return "out (the account's balance falls: a payment from a bank account, a payment or refund on a card)"
}

func ruleQuoted(s string) string { return "\u201c" + s + "\u201d" }

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func ruleSplitWords(split string) string {
	if split == "" {
		return ""
	}
	return " \u2014 " + split + "."
}
