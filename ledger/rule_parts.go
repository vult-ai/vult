package ledger

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// RulePart is one part a rule splits an item into. The rest of the item
// goes to the rule's other account; for a loan payment, that is the
// principal.
type RulePart struct {
	AccountID string `json:"account_id"`
	// Kind is fixed (AmountMinor), ratio (the item times Numerator /
	// Denominator) or interest (the balance of the rule's loan account on
	// the payment date times the yearly rate Numerator / Denominator).
	Kind        string `json:"kind"`
	AmountMinor string `json:"amount_minor,omitempty"`
	Numerator   int64  `json:"numerator,omitempty"`
	Denominator int64  `json:"denominator,omitempty"`
	// Basis is how interest counts time: monthly, or actual_365 or
	// actual_360 for the days since the loan's last entry.
	Basis string `json:"basis,omitempty"`
}

const maxRuleParts = 7

func validRuleParts(parts []RulePart) error {
	if len(parts) > maxRuleParts {
		return &RefusedError{Reason: fmt.Sprintf("A rule splits an item into at most %d parts and the rest.", maxRuleParts)}
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if !ValidID(p.AccountID) || seen[p.AccountID] {
			return ErrInvalid
		}
		seen[p.AccountID] = true
		switch p.Kind {
		case "fixed":
			if n, ok := minorInt(p.AmountMinor); !ok || n <= 0 || p.Numerator != 0 || p.Denominator != 0 || p.Basis != "" {
				return ErrInvalid
			}
		case "ratio", "interest":
			if p.AmountMinor != "" || p.Numerator <= 0 || p.Denominator <= p.Numerator || p.Denominator > 1_000_000_000 {
				return ErrInvalid
			}
			if p.Kind == "ratio" && p.Basis != "" || p.Kind == "interest" && p.Basis != "monthly" && p.Basis != "actual_365" && p.Basis != "actual_360" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return nil
}

// SetRuleParts replaces a rule's parts. With none, the rule records the
// whole item to its other account again.
func (a TenantAdmin) SetRuleParts(ctx context.Context, book, actor, rule string, parts []RulePart) (RecordingRule, error) {
	var out RecordingRule
	if !ValidID(rule) {
		return out, ErrInvalid
	}
	if err := validRuleParts(parts); err != nil {
		return out, err
	}
	err := a.withRuleOwner(ctx, book, actor, func(tx pgx.Tx, s Scope) error {
		// The parts must leave something for the rest on every item the
		// rule covers: fixed parts below its smallest amount, fractions
		// below the whole.
		var min int64
		var scale int
		if err := tx.QueryRow(ctx, `SELECT r.min_minor,coalesce(a.native_scale,b.scale) FROM public.recording_rules r JOIN public.accounts a ON a.tenant_id=r.tenant_id AND a.book_id=r.book_id AND a.id=r.account_id
 JOIN public.books b ON b.tenant_id=r.tenant_id AND b.id=r.book_id WHERE r.tenant_id=$1 AND r.book_id=$2 AND r.id=$3`, s.TenantID, s.BookID, rule).Scan(&min, &scale); err != nil {
			return dbError(err)
		}
		fixed, share := int64(0), new(big.Rat)
		for _, p := range parts {
			switch p.Kind {
			case "fixed":
				n, _ := minorInt(p.AmountMinor)
				fixed += n
			case "ratio":
				share.Add(share, big.NewRat(p.Numerator, p.Denominator))
			}
		}
		if fixed >= min {
			return &RefusedError{Reason: "The fixed parts come to " + formatMinor(strconv.FormatInt(fixed, 10), scale) + ", which leaves nothing for the rest of an item of " + formatMinor(strconv.FormatInt(min, 10), scale) + ", the smallest this rule covers. Make the parts smaller, or the rule's range larger."}
		}
		if share.Cmp(big.NewRat(1, 1)) >= 0 {
			return &RefusedError{Reason: "The percentages come to the whole item or more, which leaves nothing for the rest."}
		}
		// Touching the rule moves its revision, so readers see the change.
		tag, err := tx.Exec(ctx, "UPDATE public.recording_rules SET actor_id=$4 WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, rule, actor)
		if err != nil {
			return bankError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, "UPDATE public.recording_rule_parts SET replaced_at=clock_timestamp() WHERE tenant_id=$1 AND book_id=$2 AND rule_id=$3 AND replaced_at IS NULL",
			s.TenantID, s.BookID, rule); err != nil {
			return bankError(err)
		}
		for i, p := range parts {
			var amount any
			if p.Kind == "fixed" {
				amount = p.AmountMinor
			}
			var numerator, denominator, basis any
			if p.Kind != "fixed" {
				numerator, denominator = p.Numerator, p.Denominator
			}
			if p.Kind == "interest" {
				basis = p.Basis
			}
			if _, err := tx.Exec(ctx, `INSERT INTO public.recording_rule_parts(tenant_id,book_id,id,rule_id,position,account_id,kind,amount_minor,numerator,denominator,basis,actor_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::bigint,$9,$10,$11,$12)`, s.TenantID, s.BookID, ID(), rule, i+1, p.AccountID, p.Kind, amount, numerator, denominator, basis, actor); err != nil {
				return bankError(err)
			}
		}
		out, err = readRule(ctx, tx, s, rule)
		return err
	})
	return out, err
}

func readRule(ctx context.Context, tx pgx.Tx, s Scope, id string) (RecordingRule, error) {
	r, err := scanRule(tx.QueryRow(ctx, "SELECT "+ruleColumns+" FROM public.recording_rules r WHERE r.tenant_id=$1 AND r.book_id=$2 AND r.id=$3", s.TenantID, s.BookID, id))
	if err != nil {
		return r, dbError(err)
	}
	parts, err := readRuleParts(ctx, tx, s, []string{id})
	r.Parts = parts[id]
	return r, err
}

func readRuleParts(ctx context.Context, tx pgx.Tx, s Scope, rules []string) (map[string][]RulePart, error) {
	out := map[string][]RulePart{}
	if len(rules) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT rule_id::text,account_id::text,kind,coalesce(amount_minor::text,''),coalesce(numerator,0),coalesce(denominator,0),coalesce(basis,'')
 FROM public.recording_rule_parts WHERE tenant_id=$1 AND book_id=$2 AND rule_id::text=ANY($3) AND replaced_at IS NULL ORDER BY rule_id,position`, s.TenantID, s.BookID, rules)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var rule string
		var p RulePart
		if err := rows.Scan(&rule, &p.AccountID, &p.Kind, &p.AmountMinor, &p.Numerator, &p.Denominator, &p.Basis); err != nil {
			return nil, dbError(err)
		}
		out[rule] = append(out[rule], p)
	}
	return out, dbError(rows.Err())
}

// splitItem works out each part of an item a split rule records, with the
// arithmetic in words. The rest goes to the rule's other account and must
// be more than nothing.
func splitItem(ctx context.Context, tx pgx.Tx, s Scope, r RecordingRule, v bankItem, amount int64) ([]PostLine, string, error) {
	opposite := "debit"
	side := "credit"
	if v.debitPositive() > 0 {
		side, opposite = "debit", "credit"
	}
	value := strconv.FormatInt(amount, 10)
	lines := []PostLine{{AccountID: v.account, Side: side, AmountMinor: value}}
	money := func(n int64) string { return formatMinor(strconv.FormatInt(n, 10), v.scale) }
	var words []string
	rest := amount
	for _, p := range r.Parts {
		var part int64
		var why string
		name := accountName(ctx, tx, s, p.AccountID)
		switch p.Kind {
		case "fixed":
			part, _ = minorInt(p.AmountMinor)
			why = name + " " + money(part) + " (a fixed amount)"
		case "ratio":
			part = roundedShare(big.NewInt(amount), p.Numerator, p.Denominator, 1)
			why = name + " " + money(part) + " (" + fraction(p.Numerator, p.Denominator) + " of " + money(amount) + ")"
		case "interest":
			balance, last, err := loanBalance(ctx, tx, s, r.OtherAccountID, v.date)
			if err != nil {
				return nil, "", err
			}
			rate := percent(p.Numerator, p.Denominator) + " a year"
			switch p.Basis {
			case "monthly":
				part = roundedShare(big.NewInt(balance), p.Numerator, p.Denominator, 12)
				why = name + " " + money(part) + " (" + rate + " on " + money(balance) + " for a month)"
			default:
				if last == "" {
					return nil, "", &RefusedError{Reason: "The loan has no earlier entry to count the days of interest from; record this payment as a proposal."}
				}
				days := daysBetween(last, v.date)
				year := int64(365)
				if p.Basis == "actual_360" {
					year = 360
				}
				part = roundedShare(new(big.Int).Mul(big.NewInt(balance), big.NewInt(days)), p.Numerator, p.Denominator, year)
				why = fmt.Sprintf("%s %s (%s on %s for %d days of %d)", name, money(part), rate, money(balance), days, year)
			}
		}
		rest -= part
		if part > 0 {
			lines = append(lines, PostLine{AccountID: p.AccountID, Side: opposite, AmountMinor: strconv.FormatInt(part, 10), Memo: clip(why, 1000)})
		}
		words = append(words, why)
	}
	if rest <= 0 {
		return nil, "", &RefusedError{Reason: "The rule's parts come to " + money(amount-rest) + ", the whole item of " + money(amount) + " or more; record it as a proposal."}
	}
	restWhy := accountName(ctx, tx, s, r.OtherAccountID) + " " + money(rest) + " (the rest)"
	lines = append(lines, PostLine{AccountID: r.OtherAccountID, Side: opposite, AmountMinor: strconv.FormatInt(rest, 10), Memo: restWhy})
	return lines, strings.Join(append(words, restWhy), ", "), nil
}

// loanBalance is what the loan account owes on the payment date, from the
// Book's entries, and the date of its last entry before that day.
func loanBalance(ctx context.Context, tx pgx.Tx, s Scope, account, date string) (int64, string, error) {
	var balance int64
	var last string
	err := tx.QueryRow(ctx, `SELECT coalesce(sum(CASE l.side WHEN 'credit' THEN l.amount_minor ELSE -l.amount_minor END),0),
 coalesce(max(e.accounting_date) FILTER (WHERE e.accounting_date < $4::date)::text,'')
 FROM public.lines l JOIN public.entries e ON e.tenant_id=l.tenant_id AND e.book_id=l.book_id AND e.id=l.entry_id
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND l.account_id=$3 AND e.accounting_date <= $4::date`, s.TenantID, s.BookID, account, date).Scan(&balance, &last)
	if err != nil {
		return 0, "", dbError(err)
	}
	if balance <= 0 {
		return 0, "", &RefusedError{Reason: "The loan account owes nothing in the Book on this date, so there's no interest to work out; record this payment as a proposal."}
	}
	return balance, last, nil
}

// roundedShare is n * numerator / (denominator * per), rounded half away
// from zero, as the database rounds it.
func roundedShare(n *big.Int, numerator, denominator, per int64) int64 {
	top := new(big.Int).Mul(n, big.NewInt(numerator))
	bottom := new(big.Int).Mul(big.NewInt(denominator), big.NewInt(per))
	q, m := new(big.Int).QuoRem(top, bottom, new(big.Int))
	if new(big.Int).Mul(m, big.NewInt(2)).Cmp(bottom) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64()
}

func daysBetween(from, to string) int64 {
	a, _ := time.Parse(time.DateOnly, from)
	b, _ := time.Parse(time.DateOnly, to)
	return int64(b.Sub(a).Hours() / 24)
}

// percent writes a rate as a percentage when it is a whole number of
// hundredths of a percent, and as a fraction otherwise.
func percent(numerator, denominator int64) string {
	scaled := new(big.Int).Mul(big.NewInt(numerator), big.NewInt(1_000_000))
	q, m := new(big.Int).QuoRem(scaled, big.NewInt(denominator), new(big.Int))
	if m.Sign() != 0 {
		return fmt.Sprintf("%d/%d", numerator, denominator)
	}
	text := strings.TrimRight(strings.TrimRight(formatMinor(q.String(), 4), "0"), ".")
	return text + "%"
}

func fraction(numerator, denominator int64) string { return percent(numerator, denominator) }

func accountName(ctx context.Context, tx pgx.Tx, s Scope, id string) string {
	var name string
	if tx.QueryRow(ctx, "SELECT name FROM public.accounts WHERE tenant_id=$1 AND book_id=$2 AND id=$3", s.TenantID, s.BookID, id).Scan(&name) != nil {
		return "an account"
	}
	return name
}
