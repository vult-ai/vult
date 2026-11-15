package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/internal/strictjson"
)

// Earned autonomy: the owner lets an agent record what it's sure about,
// within limits the ledger checks at commit, and trust per kind of activity
// is kept here, the same for any agent.

// Recording modes a proposing connection may be given. Without a grant it
// drafts proposals only.
const (
	RecordByRules     = "rules"
	RecordWhenSure    = "confident"
	RecordDraftsOnly  = ""
	kindOfferAccepted = 15
)

// SetRecordingMode sets how a proposing connection may record directly:
// "rules", "confident", or "" for drafts only.
func (a TenantAdmin) SetRecordingMode(ctx context.Context, book, actor, connection, mode string) error {
	if !ValidID(connection) || mode != RecordByRules && mode != RecordWhenSure && mode != RecordDraftsOnly {
		return ErrInvalid
	}
	return a.withAgentNarrowing(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var err error
		if mode == RecordDraftsOnly {
			_, err = tx.Exec(ctx, "UPDATE public.rule_recording_grants SET revoked_at=clock_timestamp() WHERE tenant_id=$1 AND book_id=$2 AND connection_id=$3 AND revoked_at IS NULL", s.TenantID, s.BookID, connection)
			return bankError(err)
		}
		// From recording when sure to by rules only narrows, so it is an
		// update of the live grant alone.
		if mode == RecordByRules {
			tag, err := tx.Exec(ctx, "UPDATE public.rule_recording_grants SET recording_mode='rules' WHERE tenant_id=$1 AND book_id=$2 AND connection_id=$3 AND revoked_at IS NULL AND recording_mode='confident'",
				s.TenantID, s.BookID, connection)
			if err != nil || tag.RowsAffected() > 0 {
				return bankError(err)
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO public.rule_recording_grants(tenant_id,book_id,connection_id,granted_by,recording_mode) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT (tenant_id,book_id,connection_id) DO UPDATE SET revoked_at=NULL,granted_by=EXCLUDED.granted_by,granted_at=clock_timestamp(),recording_mode=EXCLUDED.recording_mode`,
			s.TenantID, s.BookID, connection, actor, mode)
		return bankError(err)
	})
}

// AgentPolicy is the Book's limits for agents recording on their own.
type AgentPolicy struct {
	AmountCapMinor string   `json:"amount_cap_minor"`
	Revision       int64    `json:"revision"`
	TaxSensitive   []string `json:"tax_sensitive_account_ids"`
}

func readAgentPolicy(ctx context.Context, tx pgx.Tx, s Scope) (AgentPolicy, error) {
	var out AgentPolicy
	if err := tx.QueryRow(ctx, `SELECT public.agent_amount_cap($1,$2)::text,coalesce((SELECT revision FROM public.agent_policy WHERE tenant_id=$1 AND book_id=$2),0)`,
		s.TenantID, s.BookID).Scan(&out.AmountCapMinor, &out.Revision); err != nil {
		return out, dbError(err)
	}
	rows, err := tx.Query(ctx, "SELECT account_id::text FROM public.agent_tax_sensitive_accounts WHERE tenant_id=$1 AND book_id=$2 ORDER BY account_id", s.TenantID, s.BookID)
	if err != nil {
		return out, dbError(err)
	}
	defer rows.Close()
	out.TaxSensitive = []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return out, dbError(err)
		}
		out.TaxSensitive = append(out.TaxSensitive, id)
	}
	return out, dbError(rows.Err())
}

// AgentPolicy reads the Book's limits for the owner.
func (a TenantAdmin) AgentPolicy(ctx context.Context, book string) (AgentPolicy, error) {
	var out AgentPolicy
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readAgentPolicy(ctx, tx, s)
		return err
	})
	return out, err
}

// SetAgentCap sets the largest item an agent may record on its own.
func (a TenantAdmin) SetAgentCap(ctx context.Context, book, actor, capMinor string, expected int64) (AgentPolicy, error) {
	var out AgentPolicy
	n, ok := minorInt(capMinor)
	if !ok || n <= 0 || expected < 0 {
		return out, ErrInvalid
	}
	err := a.withAgentNarrowing(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var err error
		if expected == 0 {
			_, err = tx.Exec(ctx, "INSERT INTO public.agent_policy(tenant_id,book_id,amount_cap_minor,revision,actor_id) VALUES($1,$2,$3,1,$4)", s.TenantID, s.BookID, n, actor)
		} else {
			var tag interface{ RowsAffected() int64 }
			tag, err = tx.Exec(ctx, "UPDATE public.agent_policy SET amount_cap_minor=$3,revision=revision+1,actor_id=$4 WHERE tenant_id=$1 AND book_id=$2 AND revision=$5",
				s.TenantID, s.BookID, n, actor, expected)
			if err == nil && tag.RowsAffected() == 0 {
				return ErrConflict
			}
		}
		if err != nil {
			return bankError(err)
		}
		out, err = readAgentPolicy(ctx, tx, s)
		return err
	})
	return out, err
}

// MarkTaxSensitive sets whether agents reach an account only by the owner's
// rules.
func (a TenantAdmin) MarkTaxSensitive(ctx context.Context, book, actor, account string, on bool) error {
	if !ValidID(account) {
		return ErrInvalid
	}
	return a.withAgentNarrowing(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var err error
		if on {
			_, err = tx.Exec(ctx, "INSERT INTO public.agent_tax_sensitive_accounts(tenant_id,book_id,account_id,actor_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", s.TenantID, s.BookID, account, actor)
		} else {
			_, err = tx.Exec(ctx, "DELETE FROM public.agent_tax_sensitive_accounts WHERE tenant_id=$1 AND book_id=$2 AND account_id=$3", s.TenantID, s.BookID, account)
		}
		return bankError(err)
	})
}

// AgentKind is the trust the Book's agents have earned for one kind of
// activity: a Book account, a direction and a counterparty.
type AgentKind struct {
	AccountID               string     `json:"account_id"`
	Direction               string     `json:"direction"`
	Counterparty            string     `json:"counterparty"`
	AcceptedUnchanged       int        `json:"accepted_unchanged"`
	AcceptedSinceCorrection int        `json:"accepted_since_correction"`
	Corrected               int        `json:"corrected"`
	LastCorrectedAt         *time.Time `json:"last_corrected_at,omitempty"`
	// Level is record (may be recorded when confident) or draft (proposals
	// only), after a correction or while the Book's agent hasn't earned it.
	Level string `json:"level"`
	// DecidedAccountID is the other account the owner last approved for the
	// kind; DeclinedAccountID the one they last turned down.
	DecidedAccountID  string `json:"decided_account_id,omitempty"`
	DeclinedAccountID string `json:"declined_account_id,omitempty"`
	// KindKey names the kind as list_bank_activity and ask_question do.
	KindKey string `json:"kind_key"`
	// Offered is set when the kind has earned an offer to record it by a
	// rule; RuleID once the owner accepted.
	Offered bool   `json:"offered"`
	RuleID  string `json:"rule_id,omitempty"`
}

func readAgentKinds(ctx context.Context, tx pgx.Tx, s Scope, keys ...string) ([]AgentKind, error) {
	rows, err := tx.Query(ctx, `SELECT account_id::text,direction,counterparty_key,accepted_unchanged,accepted_since_correction,corrected,last_corrected_at,level,
 offered_at IS NOT NULL,coalesce(rule_id::text,''),coalesce(decided_account_id::text,''),coalesce(declined_account_id::text,'')
 FROM public.agent_kinds WHERE tenant_id=$1 AND book_id=$2
 AND ($3::text[] IS NULL OR account_id::text||','||direction||','||counterparty_key=ANY($3))
 ORDER BY offered_at IS NULL,level DESC,accepted_unchanged DESC,counterparty_key LIMIT 500`, s.TenantID, s.BookID, keys)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []AgentKind{}
	for rows.Next() {
		var k AgentKind
		if err := rows.Scan(&k.AccountID, &k.Direction, &k.Counterparty, &k.AcceptedUnchanged, &k.AcceptedSinceCorrection, &k.Corrected, &k.LastCorrectedAt,
			&k.Level, &k.Offered, &k.RuleID, &k.DecidedAccountID, &k.DeclinedAccountID); err != nil {
			return nil, dbError(err)
		}
		k.KindKey = k.AccountID + "," + k.Direction + "," + k.Counterparty
		out = append(out, k)
	}
	return out, dbError(rows.Err())
}

// AgentKinds reads the Book's trust per kind for the owner.
func (a TenantAdmin) AgentKinds(ctx context.Context, book string) ([]AgentKind, error) {
	var out []AgentKind
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readAgentKinds(ctx, tx, s)
		return err
	})
	return out, err
}

// AcceptKindOffer makes a recording rule from the latest item of a kind the
// owner offered, within the range of the amounts accepted so far.
func (a TenantAdmin) AcceptKindOffer(ctx context.Context, book, actor, account, direction, counterparty string) (RecordingRule, error) {
	var item, lo, hi string
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		return dbError(tx.QueryRow(ctx, `SELECT (array_agg(x.id::text ORDER BY x.occurred_on DESC,x.id))[1],min(abs(x.amount_minor))::text,max(abs(x.amount_minor))::text
 FROM public.agent_kind_events e JOIN public.bank_transactions x ON x.tenant_id=e.tenant_id AND x.book_id=e.book_id AND x.id=e.transaction_id
 LEFT JOIN public.bank_accounts b ON b.tenant_id=x.tenant_id AND b.book_id=x.book_id AND b.id=x.bank_account_id
 WHERE e.tenant_id=$1 AND e.book_id=$2 AND e.outcome='accepted' AND coalesce(b.account_id,x.account_id)=$3::uuid
 AND (CASE WHEN x.amount_minor>0 THEN 'in' ELSE 'out' END)=$4 AND left(public.rule_key(coalesce(nullif(x.counterparty,''),x.description)),200)=$5
 AND EXISTS(SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id=x.tenant_id AND l.book_id=x.book_id AND l.transaction_id=x.id AND l.revoked_at IS NULL)
 AND EXISTS(SELECT 1 FROM public.agent_kinds k WHERE k.tenant_id=e.tenant_id AND k.book_id=e.book_id AND k.account_id=$3::uuid AND k.direction=$4 AND k.counterparty_key=$5 AND k.offered_at IS NOT NULL)
 HAVING count(*)>0`, s.TenantID, s.BookID, account, direction, counterparty).Scan(&item, &lo, &hi))
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return RecordingRule{}, &RefusedError{Reason: "Vult hasn't offered to record this kind of activity yet."}
		}
		return RecordingRule{}, err
	}
	rule, err := a.CreateRecordingRule(ctx, book, actor, RuleFromActivity{TransactionID: item, MinMinor: lo, MaxMinor: hi})
	if err != nil {
		return rule, err
	}
	err = a.withRuleOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		var ok bool
		if err := tx.QueryRow(ctx, "SELECT public.agent_kind_rule($1,$2,$3,$4,$5,$6)", s.TenantID, s.BookID, account, direction, counterparty, rule.ID).Scan(&ok); err != nil {
			return dbError(err)
		}
		if !ok {
			return ErrConflict
		}
		return nil
	})
	return rule, err
}

// AgentSummary is what agents did and what waits for the owner since a
// time, counted by the ledger, never taken from an agent's report.
type AgentSummary struct {
	Since    time.Time        `json:"since"`
	Recorded AgentSummaryDone `json:"recorded"`
	Waiting  AgentSummaryWait `json:"waiting"`
}

type AgentSummaryDone struct {
	Total int            `json:"total"`
	By    map[string]int `json:"by_basis"`
	// EntryIDs are the latest entries recorded, at most 50.
	EntryIDs []string `json:"entry_ids"`
}

type AgentSummaryWait struct {
	Proposals int `json:"proposals"`
	// Why each waiting bank proposal must be asked.
	Reasons map[string]int `json:"reasons"`
	// Unrecorded bank activity nobody has prepared yet.
	Unrecorded int `json:"unrecorded"`
	// Questions waiting for the owner, and loose ends due by today.
	Questions int `json:"questions_open"`
	LoopsDue  int `json:"loops_due"`
}

// AgentSummaryQuery reads the summary since a time; without one, the last
// seven days.
type AgentSummaryQuery struct {
	Since string `json:"since,omitempty"`
}

func readAgentSummary(ctx context.Context, tx pgx.Tx, s Scope, q AgentSummaryQuery) (AgentSummary, error) {
	out := AgentSummary{Since: time.Now().UTC().AddDate(0, 0, -7)}
	if q.Since != "" {
		t, err := time.Parse(time.RFC3339, q.Since)
		if err != nil {
			if d, derr := time.Parse(time.DateOnly, q.Since); derr == nil {
				t = d
			} else {
				return out, ErrInvalid
			}
		}
		out.Since = t.UTC()
	}
	out.Recorded.By = map[string]int{}
	out.Recorded.EntryIDs = []string{}
	rows, err := tx.Query(ctx, `SELECT l.basis,l.entry_id::text FROM public.bank_transaction_links l
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.recorded_at>=$3 AND l.revoked_at IS NULL
 AND EXISTS(SELECT 1 FROM public.connections c WHERE c.tenant_id=l.tenant_id AND c.book_id=l.book_id AND c.actor_id=l.actor_id)
 ORDER BY l.recorded_at DESC`, s.TenantID, s.BookID, out.Since)
	if err != nil {
		return out, dbError(err)
	}
	seen := map[string]bool{}
	for rows.Next() {
		var basis, entry string
		if err := rows.Scan(&basis, &entry); err != nil {
			rows.Close()
			return out, dbError(err)
		}
		if seen[entry] {
			continue
		}
		seen[entry] = true
		out.Recorded.Total++
		out.Recorded.By[basisName(basis)]++
		if len(out.Recorded.EntryIDs) < 50 {
			out.Recorded.EntryIDs = append(out.Recorded.EntryIDs, entry)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, dbError(err)
	}
	out.Waiting.Reasons = map[string]int{}
	waiting, err := tx.Query(ctx, `SELECT p.operation,convert_from(p.payload_bytes,'UTF8')::jsonb->'arguments' FROM public.operation_proposals p
 WHERE p.tenant_id=$1 AND p.book_id=$2 AND (p.expires_at IS NULL OR p.expires_at>clock_timestamp())
 AND NOT EXISTS(SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id=p.tenant_id AND d.book_id=p.book_id AND d.proposal_id=p.id)`, s.TenantID, s.BookID)
	if err != nil {
		return out, dbError(err)
	}
	type pending struct {
		operation string
		args      []byte
	}
	var list []pending
	for waiting.Next() {
		var p pending
		if err := waiting.Scan(&p.operation, &p.args); err != nil {
			waiting.Close()
			return out, dbError(err)
		}
		list = append(list, p)
	}
	waiting.Close()
	if err := waiting.Err(); err != nil {
		return out, dbError(err)
	}
	for _, p := range list {
		out.Waiting.Proposals++
		if p.operation != "record_bank_activity" {
			continue
		}
		reason, err := askedBecause(ctx, tx, s, p.args)
		if err != nil {
			return out, err
		}
		out.Waiting.Reasons[reason]++
	}
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.agent_questions WHERE tenant_id=$1 AND book_id=$2 AND status='open'),
 (SELECT count(*) FROM public.agent_loops WHERE tenant_id=$1 AND book_id=$2 AND status='open' AND due_on<=current_date)`, s.TenantID, s.BookID).Scan(&out.Waiting.Questions, &out.Waiting.LoopsDue); err != nil {
		return out, dbError(err)
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT `+bankStatusSQL+` AS status`+bankActivityFrom+` WHERE t.tenant_id=$1 AND t.book_id=$2
 AND NOT EXISTS(SELECT 1 FROM public.operation_proposals p WHERE p.tenant_id=t.tenant_id AND p.book_id=t.book_id
   AND convert_from(p.payload_bytes,'UTF8')::jsonb->'arguments'->>'transaction_id'=t.id::text
   AND NOT EXISTS(SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id=p.tenant_id AND d.book_id=p.book_id AND d.proposal_id=p.id))) v
 WHERE status='unrecorded'`, s.TenantID, s.BookID).Scan(&out.Waiting.Unrecorded)
	return out, dbError(err)
}

// basisName says how an agent recorded an item: by a rule, when confident,
// as a transfer, or as a match to an entry already there.
func basisName(basis string) string {
	switch basis {
	case "judgement":
		return "confident"
	case "existing_entry":
		return "match"
	}
	return basis
}

// askedBecause says why a bank proposal is the owner's to decide, with the
// same checks the ledger makes for an agent recording on its own.
func askedBecause(ctx context.Context, tx pgx.Tx, s Scope, args []byte) (string, error) {
	var q RecordBankActivity
	if json.Unmarshal(args, &q) != nil || !ValidID(q.TransactionID) {
		return "not_confident", nil
	}
	v, err := readBankItem(ctx, tx, s, q.TransactionID)
	if err != nil {
		return "not_confident", nil
	}
	lines := q.Lines
	if q.OtherAccountID != "" {
		lines = itemLines(v, q.OtherAccountID)
	}
	if len(lines) != 2 {
		return "split", nil
	}
	var cap int64
	if err := tx.QueryRow(ctx, "SELECT public.agent_amount_cap($1,$2)", s.TenantID, s.BookID).Scan(&cap); err != nil {
		return "", dbError(err)
	}
	amount := v.amount
	if amount < 0 {
		amount = -amount
	}
	if amount > cap {
		return "over_cap", nil
	}
	for _, l := range lines {
		if l.AccountID == v.account {
			continue
		}
		var class, role string
		var tax, kindDraft bool
		if err := tx.QueryRow(ctx, `SELECT a.class,coalesce(a.role,''),EXISTS(SELECT 1 FROM public.agent_tax_sensitive_accounts x WHERE x.tenant_id=a.tenant_id AND x.book_id=a.book_id AND x.account_id=a.id),
 EXISTS(SELECT 1 FROM public.agent_kinds k JOIN public.bank_transactions t ON t.tenant_id=k.tenant_id AND t.book_id=k.book_id AND t.id=$4
   WHERE k.tenant_id=a.tenant_id AND k.book_id=a.book_id AND k.account_id=$5 AND k.level='draft'
   AND k.direction=CASE WHEN t.amount_minor>0 THEN 'in' ELSE 'out' END AND k.counterparty_key=left(public.rule_key(coalesce(nullif(t.counterparty,''),t.description)),200))
 FROM public.accounts a WHERE a.tenant_id=$1 AND a.book_id=$2 AND a.id=$3`, s.TenantID, s.BookID, l.AccountID, v.id, v.account).Scan(&class, &role, &tax, &kindDraft); err != nil {
			return "not_confident", nil
		}
		switch {
		case class == "equity":
			return "owner_money", nil
		case tax:
			return "tax", nil
		case class == "income" && l.Side == "debit" || class == "expense" && l.Side == "credit":
			return "refund", nil
		case kindDraft:
			return "kind_corrected", nil
		}
	}
	return "not_confident", nil
}

// AgentSummary reads the summary for the owner's screen.
func (a TenantAdmin) AgentSummary(ctx context.Context, book string, q AgentSummaryQuery) (AgentSummary, error) {
	var out AgentSummary
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readAgentSummary(ctx, tx, s, q)
		return err
	})
	return out, err
}

func agentRead(ctx context.Context, tx pgx.Tx, s Scope, method string, input []byte) (json.RawMessage, error) {
	switch method {
	case "get_book_links":
		if strictjson.Decode(input, &struct{}{}) != nil {
			return nil, ErrInvalid
		}
		links, err := readLinkedBooks(ctx, tx, s)
		if err != nil {
			return nil, err
		}
		return raw(map[string]any{"links": links}), nil
	case "list_assignment_rules":
		if strictjson.Decode(input, &struct{}{}) != nil {
			return nil, ErrInvalid
		}
		rules, err := readAssignmentRules(ctx, tx, s)
		if err != nil {
			return nil, err
		}
		return raw(map[string]any{"rules": rules}), nil
	case "get_linked_balances":
		if strictjson.Decode(input, &struct{}{}) != nil {
			return nil, ErrInvalid
		}
		out, err := readLinkedBalances(ctx, tx, s.TenantID, s.BookID)
		if err != nil {
			return nil, err
		}
		return raw(out), nil
	case "get_book_link":
		if strictjson.Decode(input, &struct{}{}) != nil {
			return nil, ErrInvalid
		}
		link, err := readBookLink(ctx, tx, s)
		if err != nil {
			return nil, err
		}
		return raw(link), nil
	case "list_agent_kinds":
		var q struct {
			KindKeys []string `json:"kind_keys,omitempty"`
		}
		if strictjson.Decode(input, &q) != nil || len(q.KindKeys) > 200 {
			return nil, ErrInvalid
		}
		for _, key := range q.KindKeys {
			if len(key) == 0 || len(key) > 1000 {
				return nil, ErrInvalid
			}
		}
		kinds, err := readAgentKinds(ctx, tx, s, q.KindKeys...)
		if err != nil {
			return nil, err
		}
		policy, err := readAgentPolicy(ctx, tx, s)
		if err != nil {
			return nil, err
		}
		var trust string
		err = tx.QueryRow(ctx, "SELECT public.agent_default_trust($1,$2)", s.TenantID, s.BookID).Scan(&trust)
		return raw(struct {
			Kinds        []AgentKind `json:"kinds"`
			DefaultTrust string      `json:"default_trust"`
			Policy       AgentPolicy `json:"policy"`
		}{kinds, trust, policy}), dbError(err)
	case "get_agent_summary":
		var q AgentSummaryQuery
		if strictjson.Decode(input, &q) != nil {
			return nil, ErrInvalid
		}
		out, err := readAgentSummary(ctx, tx, s, q)
		return raw(out), err
	}
	return nil, ErrDenied
}

// RecordAgentSkills keeps the result of the built-in agent's skills test for
// the model it ran on; it's lost when the owner changes the model.
func (s Store) RecordAgentSkills(ctx context.Context, tenant, book, model, level string, score, total int) error {
	if s.Runtime == nil || !ValidID(tenant) || !ValidID(book) || total <= 0 || score < 0 || score > total ||
		level != RecordWhenSure && level != RecordByRules && level != "drafts" {
		return ErrInvalid
	}
	var ok bool
	if err := s.Runtime.QueryRow(ctx, "SELECT public.agent_record_skills($1,$2,$3,$4,$5,$6)", tenant, book, model, level, score, total).Scan(&ok); err != nil {
		return dbError(err)
	}
	if !ok {
		return ErrConflict
	}
	return nil
}
