package ledger

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type AccountPositionRead struct {
	ID              string  `json:"id"`
	Code            string  `json:"code"`
	GLCode          string  `json:"gl_code,omitempty"`
	Name            string  `json:"name"`
	Kind            string  `json:"kind"`
	Role            string  `json:"role,omitempty"`
	Usage           string  `json:"usage"`
	Revision        int64   `json:"revision"`
	Currency        string  `json:"currency,omitempty"`
	Scale           *int    `json:"scale,omitempty"`
	NativeStatus    string  `json:"native_status"`
	NativeMinor     *string `json:"native_minor,omitempty"`
	BalanceMinor    string  `json:"balance_minor"`
	PeriodMinor     string  `json:"period_change_minor"`
	PostingCount    int64   `json:"posting_count"`
	ThroughSequence int64   `json:"through_sequence"`
}

type positionReadScope struct {
	From, Through, Cutoff string
	CashOnly              bool
	AccountIDs            []string
	Limit                 int
}

// readAccountPositions uses the caller's owner scope and captured knowledge
// cutoff. A missing native revision never becomes a complete zero balance.
func readAccountPositions(ctx context.Context, tx pgx.Tx, scope Scope, q positionReadScope) ([]AccountPositionRead, error) {
	rows, err := tx.Query(ctx, `WITH selected AS MATERIALIZED (
 SELECT id,code,name,class,role,revision,currency,native_scale,currency_revision,native_origin_revision
 FROM public.accounts WHERE tenant_id=$1 AND book_id=$2
 AND (NOT $6::boolean OR (class='asset' AND role='cash'))
 AND ($7::uuid[] IS NULL OR id=ANY($7::uuid[])) ORDER BY id LIMIT $4
 ), allowed AS MATERIALIZED (
 SELECT * FROM selected WHERE (SELECT count(*) FROM selected)<$4
 ), history AS MATERIALIZED (
 SELECT l.id,l.account_id,l.side,l.amount_minor,l.native_currency,l.native_scale,l.native_amount_minor,
 r.account_id measurement_account_id,r.currency_revision measurement_currency_revision,
 e.sequence,e.accounting_date FROM public.lines l JOIN public.entries e
 ON e.tenant_id=l.tenant_id AND e.book_id=l.book_id AND e.id=l.entry_id
 JOIN allowed a ON a.id=l.account_id
 LEFT JOIN public.account_revisions r ON r.tenant_id=l.tenant_id AND r.book_id=l.book_id
   AND r.account_id=l.account_id AND r.revision=l.native_account_revision
 WHERE l.tenant_id=$1 AND l.book_id=$2 AND e.accounting_date<=$3::date AND e.recorded_at<=$5::timestamptz LIMIT 100001
 ), capacity AS MATERIALIZED (
 SELECT (SELECT count(*) FROM selected)>=$4 OR (SELECT count(*) FROM history)>100000 overflow
 ), positions AS (
 SELECT a.id,a.code,a.name,a.class,a.role,a.revision,a.currency,a.native_scale,a.native_origin_revision,
 coalesce(bool_and(h.measurement_account_id IS NOT NULL AND h.native_currency=a.currency
   AND h.native_scale=a.native_scale AND h.measurement_currency_revision=a.currency_revision
   AND h.native_amount_minor IS NOT NULL) FILTER(WHERE h.id IS NOT NULL),true) measurements_complete,
 coalesce(sum(CASE h.side WHEN 'debit' THEN h.native_amount_minor::numeric ELSE -h.native_amount_minor::numeric END),0) native_minor,
 coalesce(sum(CASE h.side WHEN 'debit' THEN h.amount_minor::numeric ELSE -h.amount_minor::numeric END),0) book_minor,
 coalesce(sum(CASE h.side WHEN 'debit' THEN h.amount_minor::numeric ELSE -h.amount_minor::numeric END)
   FILTER(WHERE $8='' OR h.accounting_date>=nullif($8,'')::date),0) period_minor,
 count(h.id) posting_count,coalesce(max(h.sequence),0) last_sequence
 FROM allowed a LEFT JOIN history h ON h.account_id=a.id
 WHERE NOT (SELECT overflow FROM capacity)
 GROUP BY a.id,a.code,a.name,a.class,a.role,a.revision,a.currency,a.native_scale,a.native_origin_revision
 ) SELECT id::text,code,name,class,coalesce(role,''),revision,coalesce(currency,''),native_scale,
 CASE WHEN currency IS NULL THEN 'not_recorded'
   WHEN native_origin_revision IS NULL OR NOT measurements_complete THEN 'incomplete' ELSE 'complete' END,
 CASE WHEN currency IS NOT NULL AND native_origin_revision IS NOT NULL AND measurements_complete
   THEN native_minor::text END,book_minor::text,period_minor::text,posting_count,last_sequence,false
 FROM positions UNION ALL
 SELECT '','','','','',0::bigint,'',NULL::integer,'',NULL::text,'0','0',0::bigint,0::bigint,true
 FROM capacity WHERE overflow ORDER BY 1`,
		scope.TenantID, scope.BookID, q.Through, q.Limit+1, q.Cutoff, q.CashOnly, q.AccountIDs, q.From)
	if err != nil {
		return nil, dbError(err)
	}
	defer rows.Close()
	out := []AccountPositionRead{}
	for rows.Next() {
		var a AccountPositionRead
		var overflow bool
		if err := rows.Scan(&a.ID, &a.GLCode, &a.Name, &a.Kind, &a.Role, &a.Revision, &a.Currency, &a.Scale, &a.NativeStatus, &a.NativeMinor, &a.BalanceMinor, &a.PeriodMinor, &a.PostingCount, &a.ThroughSequence, &overflow); err != nil {
			return nil, dbError(err)
		}
		if overflow {
			return nil, ErrLimit
		}
		a.Code, a.Usage = a.ID, "general"
		if a.Kind == "asset" && a.Role == "cash" {
			a.Usage = "cash"
		}
		if a.NativeStatus != "complete" {
			a.NativeMinor = nil
		}
		out = append(out, a)
		if len(out) > q.Limit {
			return nil, ErrLimit
		}
	}
	return out, dbError(rows.Err())
}

func cashAccountViews(accounts []AccountPositionRead) []CashAccountRead {
	out := []CashAccountRead{}
	for _, a := range accounts {
		if a.Kind == "asset" && a.Role == "cash" {
			out = append(out, CashAccountRead{ID: a.ID, Code: a.ID, GLCode: a.GLCode, Name: a.Name, Currency: a.Currency, Scale: a.Scale, NativeStatus: a.NativeStatus, NativeMinor: a.NativeMinor, CarryingMinor: a.BalanceMinor, ThroughSequence: a.ThroughSequence})
		}
	}
	return out
}
