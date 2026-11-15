-- Sum account balances once; keep individual postings for pair mismatch checks.
CREATE OR REPLACE FUNCTION public.linked_balances(t uuid, b uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
WITH allowed AS (
    SELECT CASE WHEN b IS NULL THEN access.owner_allowed(t) ELSE access.owner_allowed(t) OR public.ledger_allowed(t, b, false) END AS ok
), links AS (
    SELECT l.* FROM public.book_links l, allowed WHERE allowed.ok AND l.tenant_id = t AND (b IS NULL OR b IN (l.book_a, l.book_b))
), balance AS (
    -- Each line's effect, debit positive, on the accounts terms name.
    SELECT x.book_id, x.account_id, x.entry_id, CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END AS amount
    FROM public.lines x WHERE x.tenant_id = t
), account_balances AS MATERIALIZED (
    SELECT book_id, account_id, sum(amount) AS amount FROM balance GROUP BY book_id, account_id
), dirs AS (
    -- Owed directions: the payer is owed (receivable), the beneficiary owes (payable).
    SELECT k.link_id, k.payer_book_id AS creditor, k.payer_account_id AS creditor_account, k.beneficiary_book_id AS debtor, k.beneficiary_account_id AS debtor_account,
        coalesce(creditor.amount, 0) AS creditor_says,
        coalesce(-debtor.amount, 0) AS debtor_says
    FROM public.book_link_terms k JOIN links l ON l.tenant_id = t AND l.id = k.link_id
        LEFT JOIN account_balances creditor ON creditor.book_id = k.payer_book_id AND creditor.account_id = k.payer_account_id
        LEFT JOIN account_balances debtor ON debtor.book_id = k.beneficiary_book_id AND debtor.account_id = k.beneficiary_account_id
    WHERE k.tenant_id = t AND k.treatment = 'owed'
), paired AS (
    SELECT p.payer_book_id AS book_id, p.payer_entry_id AS entry_id FROM public.entry_pairs p WHERE p.tenant_id = t
    UNION ALL SELECT p.beneficiary_book_id, p.beneficiary_entry_id FROM public.entry_pairs p WHERE p.tenant_id = t
), net AS (
    SELECT l.id AS link_id, l.book_a, l.book_b,
        coalesce(sum(CASE WHEN d.creditor = l.book_a THEN d.debtor_says ELSE -d.debtor_says END), 0) AS owed_to_a,
        bool_and(d.creditor_says = d.debtor_says) AS matches
    FROM links l JOIN dirs d ON d.link_id = l.id GROUP BY l.id, l.book_a, l.book_b
), costs AS (
    -- Costs and opening balances along each owed direction, with what is
    -- still open of each.
    SELECT o.*, d.link_id, d.creditor FROM dirs d, public.open_costs(t, d.creditor, d.debtor, d.debtor_account, NULL) o
), ranked AS (
    SELECT c.* FROM costs c
)
SELECT jsonb_build_object(
    'links', coalesce((SELECT jsonb_agg(jsonb_build_object(
        'link_id', n.link_id,
        'debtor_book_id', CASE WHEN n.owed_to_a >= 0 THEN n.book_b ELSE n.book_a END,
        'creditor_book_id', CASE WHEN n.owed_to_a >= 0 THEN n.book_a ELSE n.book_b END,
        'amount_minor', abs(n.owed_to_a)::text,
        'currency', (SELECT k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = n.book_a),
        'matches', n.matches,
        'mismatched_entry_ids', coalesce((SELECT jsonb_agg(DISTINCT y.entry_id) FROM dirs d JOIN balance y
                ON (y.book_id = d.creditor AND y.account_id = d.creditor_account OR y.book_id = d.debtor AND y.account_id = d.debtor_account)
            WHERE d.link_id = n.link_id
                AND NOT EXISTS (SELECT 1 FROM paired q WHERE q.book_id = y.book_id AND q.entry_id = y.entry_id)
                AND NOT EXISTS (SELECT 1 FROM public.entries e JOIN paired q ON q.book_id = e.book_id AND q.entry_id = e.reversal_of
                    WHERE e.tenant_id = t AND e.book_id = y.book_id AND e.id = y.entry_id)), '[]'::jsonb),
        'open_pairs', coalesce((SELECT jsonb_agg(jsonb_build_object(
                'pair_key', r.pair_key, 'kind', r.kind, 'payer_book_id', r.payer_book_id, 'beneficiary_book_id', r.beneficiary_book_id,
                'date', r.accounting_date, 'description', r.description,
                'amount_minor', r.amount::text,
                'open_minor', r.open::text,
                'repaid', r.open = 0,
                'partly_repaid', r.open > 0 AND r.open < r.amount,
                'has_receipt', EXISTS (SELECT 1 FROM public.evidence_links v WHERE v.tenant_id = t
                        AND (v.book_id = r.beneficiary_book_id AND v.entry_id = r.beneficiary_entry_id OR v.book_id = r.payer_book_id AND v.entry_id = r.payer_entry_id)
                        AND v.action = 'attach' AND v.role IN ('receipt', 'invoice')
                        AND NOT EXISTS (SELECT 1 FROM public.evidence_links w WHERE w.tenant_id = v.tenant_id AND w.book_id = v.book_id AND w.link_id = v.link_id AND w.revision > v.revision))
                    OR EXISTS (SELECT 1 FROM public.source_citations c WHERE c.tenant_id = t
                        AND (c.book_id = r.beneficiary_book_id AND c.entry_id = r.beneficiary_entry_id OR c.book_id = r.payer_book_id AND c.entry_id = r.payer_entry_id)))
                ORDER BY r.accounting_date, r.pair_key)
            FROM ranked r WHERE r.link_id = n.link_id), '[]'::jsonb))
        ORDER BY n.link_id) FROM net n), '[]'::jsonb),
    'books', coalesce((SELECT jsonb_agg(jsonb_build_object('book_id', k.id, 'name', k.name,
        'liabilities', coalesce((SELECT jsonb_agg(jsonb_build_object('account_id', a.id, 'name', a.name, 'role', coalesce(a.role, ''),
                'balance_minor', coalesce(-y.amount, 0)::text) ORDER BY a.code, a.id)
            FROM public.accounts a LEFT JOIN account_balances y ON y.book_id = a.book_id AND y.account_id = a.id
            WHERE a.tenant_id = t AND a.book_id = k.id AND a.class = 'liability'
                AND NOT EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t
                    AND (x.payer_book_id = k.id AND x.payer_account_id = a.id OR x.beneficiary_book_id = k.id AND x.beneficiary_account_id = a.id))), '[]'::jsonb))
        ORDER BY k.name, k.id)
        FROM public.books k, allowed WHERE allowed.ok AND k.tenant_id = t AND (b IS NULL AND EXISTS (SELECT 1 FROM links l WHERE k.id IN (l.book_a, l.book_b)) OR k.id = b)), '[]'::jsonb))
$$;
