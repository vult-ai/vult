package ledger

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// KindHistory is how a Book has already recorded one kind of bank activity
// (an account, a direction and a counterparty, named by its kind key): for
// each other account, how many items, their smallest and largest amounts,
// and the latest date.
type KindHistory struct {
	KindKey  string     `json:"kind_key"`
	Accounts []KindPast `json:"accounts"`
}

type KindPast struct {
	AccountID string `json:"account_id"`
	Count     int    `json:"count"`
	MinMinor  string `json:"min_minor"`
	MaxMinor  string `json:"max_minor"`
	LastOn    string `json:"last_on"`
}

// MaxKindHistoryKeys bounds one read of kind history.
const MaxKindHistoryKeys = 200

// KindHistory reads how the Book recorded earlier bank items of each kind
// named: items linked to an entry that isn't reversed and that has one other
// line, so the other account is plain. Kinds with no such items are left out.
func (a TenantAdmin) KindHistory(ctx context.Context, book string, keys []string) ([]KindHistory, error) {
	if len(keys) > MaxKindHistoryKeys {
		return nil, ErrInvalid
	}
	for _, k := range keys {
		if len(k) == 0 || len(k) > 1000 {
			return nil, ErrInvalid
		}
	}
	out := []KindHistory{}
	if len(keys) == 0 {
		return out, nil
	}
	err := a.withBookRead(ctx, book, func(tx pgx.Tx, s Scope, _ Book) error {
		var err error
		out, err = readKindHistory(ctx, tx, s, keys)
		return err
	})
	return out, err
}

func readKindHistory(ctx context.Context, tx pgx.Tx, s Scope, keys []string) ([]KindHistory, error) {
	rows, err := tx.Query(ctx, `SELECT k.kind_key,o.account_id::text,count(*),min(abs(k.amount_minor))::text,max(abs(k.amount_minor))::text,max(k.occurred_on)::text
 FROM (SELECT t.amount_minor,t.occurred_on,l.entry_id,coalesce(a.account_id,t.account_id) AS account_id,
   coalesce(a.account_id,t.account_id)::text||','||CASE WHEN t.amount_minor>0 THEN 'in' ELSE 'out' END||','||left(public.rule_key(coalesce(nullif(t.counterparty,''),t.description)),200) AS kind_key
  FROM public.bank_transactions t
  LEFT JOIN public.bank_accounts a ON a.tenant_id=t.tenant_id AND a.book_id=t.book_id AND a.id=t.bank_account_id
  JOIN public.bank_transaction_links l ON l.tenant_id=t.tenant_id AND l.book_id=t.book_id AND l.transaction_id=t.id AND l.revoked_at IS NULL
  WHERE t.tenant_id=$1 AND t.book_id=$2 AND t.removed_at IS NULL AND NOT public.statement_entry_reversed(l.tenant_id,l.book_id,l.entry_id)) k
 JOIN LATERAL (SELECT x.account_id FROM public.lines x WHERE x.tenant_id=$1 AND x.book_id=$2 AND x.entry_id=k.entry_id AND x.account_id<>k.account_id
   AND (SELECT count(*) FROM public.lines y WHERE y.tenant_id=$1 AND y.book_id=$2 AND y.entry_id=k.entry_id)=2) o ON true
 WHERE k.kind_key=ANY($3)
 GROUP BY 1,2 ORDER BY 1,3 DESC,2`, s.TenantID, s.BookID, keys)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []KindHistory{}
	for rows.Next() {
		var key string
		var p KindPast
		if err := rows.Scan(&key, &p.AccountID, &p.Count, &p.MinMinor, &p.MaxMinor, &p.LastOn); err != nil {
			return nil, dbError(err)
		}
		if len(out) == 0 || out[len(out)-1].KindKey != key {
			out = append(out, KindHistory{KindKey: key})
		}
		out[len(out)-1].Accounts = append(out[len(out)-1].Accounts, p)
	}
	return out, dbError(rows.Err())
}
