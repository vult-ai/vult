-- A repayment between linked Books may repay part of a cost. Unless the
-- owner chooses, the payment goes to the oldest open costs first, and the
-- last one it reaches is repaid in part and stays open for the rest. Each
-- cost a settlement repays keeps the amount it repaid; earlier rows, with
-- none, repaid the whole cost.
ALTER TABLE public.settlement_repays ADD COLUMN amount_minor bigint CHECK (amount_minor IS NULL OR amount_minor > 0);

-- What a cost (or an opening balance) amounts to, by its entry in the Book
-- it was paid for.
CREATE FUNCTION public.cost_amount(t uuid, b uuid, e uuid) RETURNS bigint
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(sum(z.amount_minor), 0)::bigint FROM public.lines z WHERE z.tenant_id = t AND z.book_id = b AND z.entry_id = e AND z.side = 'credit'
$$;

-- How much of a cost live settlements repaid; other_than leaves one out.
CREATE FUNCTION public.cost_repaid_amount(t uuid, b uuid, e uuid, other_than uuid) RETURNS bigint
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(sum(coalesce(r.amount_minor, public.cost_amount(t, b, e))), 0)::bigint FROM public.settlement_repays r
WHERE r.tenant_id = t AND r.cost_book_id = b AND r.cost_entry_id = e AND r.settlement_entry_id IS DISTINCT FROM other_than
    AND NOT public.entry_reversed(t, r.settlement_book_id, r.settlement_entry_id)
$$;

-- The settlement that finished repaying a cost, or NULL while any of it is open.
CREATE OR REPLACE FUNCTION public.cost_repaid_by(t uuid, b uuid, e uuid, other_than uuid) RETURNS text
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT CASE WHEN public.cost_repaid_amount(t, b, e, other_than) >= public.cost_amount(t, b, e) THEN (
    SELECT p.pair_key FROM public.settlement_repays r
        JOIN public.entry_pairs p ON p.tenant_id = r.tenant_id AND p.payer_book_id = r.settlement_book_id AND p.payer_entry_id = r.settlement_entry_id
        JOIN public.entries s ON s.tenant_id = r.tenant_id AND s.book_id = r.settlement_book_id AND s.id = r.settlement_entry_id
    WHERE r.tenant_id = t AND r.cost_book_id = b AND r.cost_entry_id = e AND r.settlement_entry_id IS DISTINCT FROM other_than
        AND NOT public.entry_reversed(t, r.settlement_book_id, r.settlement_entry_id)
    ORDER BY s.accounting_date DESC, s.sequence DESC LIMIT 1) END
$$;

-- The costs a debtor owes a creditor, oldest first, with what is open of
-- each: what settlements repaid is taken off each cost, and what is still
-- owed on the debtor's account reaches back from the newest; a payment that
-- named nothing repays the oldest. leaving_out leaves one settlement out.
CREATE FUNCTION public.open_costs(t uuid, creditor uuid, debtor uuid, debtor_account uuid, leaving_out uuid)
    RETURNS TABLE (pair_key text, kind text, payer_book_id uuid, payer_entry_id uuid, beneficiary_book_id uuid, beneficiary_entry_id uuid,
        book_id uuid, entry_id uuid, accounting_date date, sequence bigint, description text, amount bigint, repaid bigint, open bigint)
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
WITH owed AS (
    SELECT greatest(coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0), 0)::bigint AS amount
    FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.account_id = debtor_account AND x.entry_id IS DISTINCT FROM leaving_out
), costs AS (
    SELECT p.pair_key, p.kind, p.payer_book_id, p.payer_entry_id, p.beneficiary_book_id, p.beneficiary_entry_id,
        p.beneficiary_book_id AS book_id, p.beneficiary_entry_id AS entry_id, e.accounting_date, e.sequence::bigint AS sequence, e.description,
        public.cost_amount(t, p.beneficiary_book_id, p.beneficiary_entry_id) AS amount
    FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
    WHERE p.tenant_id = t
        AND (p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
            OR p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor)
        AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)
), repaid AS (
    SELECT c.*, least(c.amount, public.cost_repaid_amount(t, c.book_id, c.entry_id, leaving_out)) AS repaid FROM costs c
), ranked AS (
    SELECT r.*, sum(r.amount - r.repaid) OVER (ORDER BY r.accounting_date DESC, r.sequence DESC, r.pair_key DESC ROWS UNBOUNDED PRECEDING) AS newer
    FROM repaid r
)
SELECT r.pair_key, r.kind, r.payer_book_id, r.payer_entry_id, r.beneficiary_book_id, r.beneficiary_entry_id, r.book_id, r.entry_id,
    r.accounting_date, r.sequence, r.description, r.amount, r.repaid,
    least(r.amount - r.repaid, greatest((SELECT o.amount FROM owed o) - (r.newer - (r.amount - r.repaid)), 0))::bigint AS open
FROM ranked r ORDER BY r.accounting_date, r.sequence, r.pair_key
$$;
REVOKE ALL ON FUNCTION public.cost_amount(uuid, uuid, uuid), public.cost_repaid_amount(uuid, uuid, uuid, uuid),
    public.open_costs(uuid, uuid, uuid, uuid, uuid) FROM PUBLIC;

-- Checks a settlement, as before; a cost may be named with the part of it
-- this repays, and a refusal says what to do in the owner's words.
CREATE OR REPLACE FUNCTION public.settlement_check(t uuid, debtor uuid, debtor_account uuid, debtor_item uuid, creditor uuid, creditor_account uuid, creditor_item uuid,
        repays jsonb, leaving_out uuid) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.bank_transactions%ROWTYPE; c public.bank_transactions%ROWTYPE; dclass text; cclass text; owed bigint; total bigint := 0;
    k text; entry uuid; entry_book uuid; cost bigint; amount bigint; excess bigint; repaying bigint; costs uuid[] := '{}'; items jsonb := '[]'; rev public.book_link_terms%ROWTYPE;
    required boolean; debtor_name text; creditor_name text; r record; elem jsonb; part bigint; open_amount bigint; key_text text; chosen text[] := '{}';
BEGIN
    SELECT * INTO d FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = debtor AND x.id = debtor_item;
    SELECT * INTO c FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = creditor AND x.id = creditor_item;
    IF d.id IS NULL OR c.id IS NULL THEN
        RAISE EXCEPTION 'A settlement is two bank items, one in each linked Book.' USING ERRCODE = 'VP020';
    END IF;
    debtor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = debtor);
    creditor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = creditor);
    dclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = debtor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = debtor AND y.id = d.bank_account_id), d.account_id));
    cclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = creditor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = creditor AND y.id = c.bank_account_id), c.account_id));
    IF NOT (dclass = 'asset' AND d.amount_minor < 0 OR dclass = 'liability' AND d.amount_minor > 0)
        OR NOT (cclass = 'asset' AND c.amount_minor > 0 OR cclass = 'liability' AND c.amount_minor < 0) THEN
        RAISE EXCEPTION 'A settlement is money paid out of the Book that owes and into the Book that is owed; pick the payment in %, and its arrival in %.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    amount := abs(d.amount_minor);
    IF abs(c.amount_minor) <> amount OR c.currency IS DISTINCT FROM d.currency OR abs(c.occurred_on - d.occurred_on) > 5 THEN
        RAISE EXCEPTION 'A settlement''s two bank items are the same amount, at most five days apart; these are % and %, on % and %.',
            public.book_amount(t, debtor, amount), public.book_amount(t, creditor, abs(c.amount_minor)), d.occurred_on, c.occurred_on USING ERRCODE = 'VP020';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed' AND x.payer_book_id = creditor AND x.payer_account_id = creditor_account
                AND x.beneficiary_book_id = debtor AND x.beneficiary_account_id = debtor_account) THEN
        RAISE EXCEPTION 'Nothing is owed this way: % pays % only for what an active owed direction names. Set "% pays for %" to owed back in Links.',
            debtor_name, creditor_name, creditor_name, debtor_name USING ERRCODE = 'VP020';
    END IF;
    rev := public.active_link_terms(t, debtor, creditor);
    SELECT coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO owed
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.account_id = debtor_account AND x.entry_id IS DISTINCT FROM leaving_out;
    excess := greatest(amount - greatest(owed, 0), 0);
    repaying := amount - excess;
    IF excess > 0 AND rev.link_id IS NULL THEN
        RAISE EXCEPTION 'This pays % more than % owes (%). Set "% pays for %" in Links, and the extra is recorded there.',
            public.book_amount(t, debtor, excess), debtor_name, public.book_amount(t, debtor, greatest(owed, 0)), debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    IF jsonb_typeof(coalesce(repays, '[]'::jsonb)) <> 'array' OR jsonb_array_length(coalesce(repays, '[]'::jsonb)) > 50 THEN
        RAISE EXCEPTION 'Choose the costs this repays from the open items.' USING ERRCODE = 'VP020';
    END IF;
    -- Whether the payer's books expect the costs named (an accountable plan);
    -- reported, not required: unnamed, the oldest are repaid first.
    required := public.book_profile(t, creditor) = 'personal' AND public.book_profile(t, debtor) = 'business' AND rev.treatment IS DISTINCT FROM 'owner_equity';
    FOR elem IN SELECT jsonb_array_elements(coalesce(repays, '[]'::jsonb)) LOOP
        -- A cost by its key repays what is open of it; with an amount, that part.
        IF jsonb_typeof(elem) = 'string' THEN
            key_text := elem #>> '{}';
            part := NULL;
        ELSIF jsonb_typeof(elem) = 'object' AND jsonb_typeof(elem -> 'pair_key') = 'string' AND (elem - 'pair_key' - 'amount_minor') = '{}'::jsonb
                AND (elem -> 'amount_minor' IS NULL OR jsonb_typeof(elem -> 'amount_minor') = 'string' AND elem ->> 'amount_minor' ~ '^[1-9][0-9]{0,17}$') THEN
            key_text := elem ->> 'pair_key';
            part := (elem ->> 'amount_minor')::bigint;
        ELSE
            RAISE EXCEPTION 'Choose the costs this repays from the open items.' USING ERRCODE = 'VP020';
        END IF;
        IF key_text = ANY (chosen) THEN
            RAISE EXCEPTION 'A cost is chosen twice; choose each cost once.' USING ERRCODE = 'VP020';
        END IF;
        chosen := chosen || key_text;
        SELECT o.book_id, o.entry_id, o.open INTO entry_book, entry, open_amount
            FROM public.open_costs(t, creditor, debtor, debtor_account, leaving_out) o WHERE o.pair_key = key_text;
        IF entry IS NULL THEN
            RAISE EXCEPTION 'One of the costs chosen isn''t a cost % paid for % or an opening balance % owes; choose from the open items.', creditor_name, debtor_name, debtor_name USING ERRCODE = 'VP020';
        END IF;
        IF open_amount <= 0 THEN
            RAISE EXCEPTION 'One of the costs chosen is already repaid; choose an open cost instead.' USING ERRCODE = 'VP020';
        END IF;
        part := coalesce(part, open_amount);
        IF part > open_amount THEN
            RAISE EXCEPTION 'This repays % of a cost that has % left to repay; repay at most what is left.',
                public.book_amount(t, debtor, part), public.book_amount(t, debtor, open_amount) USING ERRCODE = 'VP020';
        END IF;
        costs := costs || entry;
        items := items || jsonb_build_array(jsonb_build_object('book', entry_book, 'entry', entry, 'pair_key', key_text, 'amount_minor', part));
        total := total + part;
    END LOOP;
    IF cardinality(costs) > 0 AND total <> repaying THEN
        RAISE EXCEPTION 'The costs chosen add up to %, but this payment repays %; choose costs and amounts that add up to it exactly.',
            public.book_amount(t, debtor, total), public.book_amount(t, debtor, repaying) USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) = 0 THEN
        -- Unnamed: the oldest open costs first; the last one reached is
        -- repaid in part and stays open for the rest.
        FOR r IN SELECT o.* FROM public.open_costs(t, creditor, debtor, debtor_account, leaving_out) o WHERE o.open > 0
            ORDER BY o.accounting_date, o.sequence, o.pair_key LOOP
            EXIT WHEN total >= repaying;
            part := least(r.open, repaying - total);
            costs := costs || r.entry_id;
            items := items || jsonb_build_array(jsonb_build_object('book', r.book_id, 'entry', r.entry_id, 'pair_key', r.pair_key, 'amount_minor', part));
            total := total + part;
        END LOOP;
    END IF;
    RETURN jsonb_build_object('costs', items, 'excess', excess, 'repaying', repaying, 'required', required, 'named', jsonb_array_length(coalesce(repays, '[]'::jsonb)) > 0);
END $$;


-- Records a settlement, keeping the part of each cost it repays.
CREATE OR REPLACE FUNCTION public.settle_pair(t uuid, key text, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid, repays jsonb) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE ditem uuid; dbank uuid; citem uuid; cbank uuid; owed public.book_link_terms%ROWTYPE; rev public.book_link_terms%ROWTYPE;
    checked jsonb; item jsonb; excess bigint; repaying bigint; amount bigint; bad int;
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    SELECT l.transaction_id, l.account_id INTO ditem, dbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = debtor AND l.entry_id = debtor_entry AND l.revoked_at IS NULL;
    SELECT l.transaction_id, l.account_id INTO citem, cbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = creditor AND l.entry_id = creditor_entry AND l.revoked_at IS NULL;
    owed := public.active_link_terms(t, creditor, debtor);
    rev := public.active_link_terms(t, debtor, creditor);
    IF ditem IS NULL OR citem IS NULL OR owed.treatment IS DISTINCT FROM 'owed' THEN
        RAISE EXCEPTION 'Each entry of a settlement records its own bank item, between Books an owed direction links.' USING ERRCODE = 'VP020';
    END IF;
    checked := public.settlement_check(t, debtor, owed.beneficiary_account_id, ditem, creditor, owed.payer_account_id, citem, repays, debtor_entry);
    excess := (checked ->> 'excess')::bigint;
    repaying := (checked ->> 'repaying')::bigint;
    amount := excess + repaying;
    -- The debtor's entry: the bank account for the whole amount, the payable
    -- for what was owed, the reverse direction's account for the excess.
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = dbank AND x.amount_minor = amount
            OR x.account_id = owed.beneficiary_account_id AND x.side = 'debit' AND x.amount_minor = repaying
            OR x.account_id = rev.payer_account_id AND x.side = 'debit' AND x.amount_minor = excess, false))
        + abs(count(*) - (1 + (repaying > 0)::int + (excess > 0)::int)) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The paying Book''s entry records % to what it owed and % as the extra; record it that way.',
            public.book_amount(t, debtor, repaying), public.book_amount(t, debtor, excess) USING ERRCODE = 'VP020';
    END IF;
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = cbank AND x.amount_minor = amount
            OR x.account_id = owed.payer_account_id AND x.side = 'credit' AND x.amount_minor = repaying
            OR x.account_id = rev.beneficiary_account_id AND x.side = 'credit' AND x.amount_minor = excess, false))
        + abs(count(*) - (1 + (repaying > 0)::int + (excess > 0)::int)) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = creditor AND x.entry_id = creditor_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The paid Book''s entry records % to what it was owed and % as the extra; record it that way.',
            public.book_amount(t, creditor, repaying), public.book_amount(t, creditor, excess) USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.entries x JOIN public.bank_transactions i ON i.tenant_id = x.tenant_id AND i.book_id = x.book_id
                AND i.id = CASE WHEN x.book_id = debtor THEN ditem ELSE citem END
            WHERE x.tenant_id = t AND (x.book_id = debtor AND x.id = debtor_entry OR x.book_id = creditor AND x.id = creditor_entry)
                AND (x.accounting_date <> i.occurred_on OR x.reversal_of IS NOT NULL)) THEN
        RAISE EXCEPTION 'Each entry of a settlement is dated its own bank item''s date.' USING ERRCODE = 'VP020';
    END IF;
    PERFORM set_config('vult.settling', key, true);
    INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id)
        VALUES (t, key, 'settlement', debtor, debtor_entry, creditor, creditor_entry);
    PERFORM set_config('vult.settling', '', true);
    FOR item IN SELECT jsonb_array_elements(checked -> 'costs') LOOP
        INSERT INTO public.settlement_repays(tenant_id, settlement_book_id, settlement_entry_id, cost_book_id, cost_entry_id, amount_minor)
            VALUES (t, debtor, debtor_entry, (item ->> 'book')::uuid, (item ->> 'entry')::uuid, (item ->> 'amount_minor')::bigint);
    END LOOP;
END $$;

-- Who owes whom shows what is still open of each cost.
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
), dirs AS (
    -- Owed directions: the payer is owed (receivable), the beneficiary owes (payable).
    SELECT k.link_id, k.payer_book_id AS creditor, k.payer_account_id AS creditor_account, k.beneficiary_book_id AS debtor, k.beneficiary_account_id AS debtor_account,
        coalesce((SELECT sum(y.amount) FROM balance y WHERE y.book_id = k.payer_book_id AND y.account_id = k.payer_account_id), 0) AS creditor_says,
        coalesce((SELECT -sum(y.amount) FROM balance y WHERE y.book_id = k.beneficiary_book_id AND y.account_id = k.beneficiary_account_id), 0) AS debtor_says
    FROM public.book_link_terms k JOIN links l ON l.tenant_id = t AND l.id = k.link_id WHERE k.tenant_id = t AND k.treatment = 'owed'
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
                'balance_minor', coalesce((SELECT -sum(y.amount) FROM balance y WHERE y.book_id = a.book_id AND y.account_id = a.id), 0)::text) ORDER BY a.code, a.id)
            FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = k.id AND a.class = 'liability'
                AND NOT EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t
                    AND (x.payer_book_id = k.id AND x.payer_account_id = a.id OR x.beneficiary_book_id = k.id AND x.beneficiary_account_id = a.id))), '[]'::jsonb))
        ORDER BY k.name, k.id)
        FROM public.books k, allowed WHERE allowed.ok AND k.tenant_id = t AND (b IS NULL AND EXISTS (SELECT 1 FROM links l WHERE k.id IN (l.book_a, l.book_b)) OR k.id = b)), '[]'::jsonb))
$$;

-- What a repayment would do, before it is recorded: which costs it repays
-- and how much of each, what each has left after it, and any extra. Only
-- the owner reads it.
CREATE FUNCTION public.settlement_preview(t uuid, debtor_item uuid, creditor_item uuid, repays jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE debtor uuid; creditor uuid; owed public.book_link_terms%ROWTYPE; checked jsonb;
BEGIN
    IF NOT access.owner_allowed(t) THEN
        RAISE EXCEPTION 'only the owner previews a repayment' USING ERRCODE = '42501';
    END IF;
    debtor := (SELECT x.book_id FROM public.bank_transactions x WHERE x.tenant_id = t AND x.id = debtor_item);
    creditor := (SELECT x.book_id FROM public.bank_transactions x WHERE x.tenant_id = t AND x.id = creditor_item);
    owed := public.active_link_terms(t, creditor, debtor);
    IF debtor IS NULL OR creditor IS NULL OR debtor = creditor OR owed.treatment IS DISTINCT FROM 'owed' THEN
        RAISE EXCEPTION 'Nothing is owed this way between these Books: no active owed direction links them, or the link is turned off.' USING ERRCODE = 'VP020';
    END IF;
    checked := public.settlement_check(t, debtor, owed.beneficiary_account_id, debtor_item, creditor, owed.payer_account_id, creditor_item, repays, NULL);
    RETURN jsonb_build_object(
        'repaying_minor', (checked ->> 'repaying'),
        'excess_minor', (checked ->> 'excess'),
        'excess_recorded_as', CASE WHEN (checked ->> 'excess')::bigint > 0 THEN
            CASE WHEN (public.active_link_terms(t, debtor, creditor)).treatment = 'owner_equity' THEN 'owner_draw' ELSE 'owed_back' END END,
        'names_expected', checked -> 'required',
        'costs', coalesce((SELECT jsonb_agg(jsonb_build_object('pair_key', c ->> 'pair_key', 'amount_minor', c ->> 'amount_minor',
                'open_minor', o.open::text, 'left_after_minor', (o.open - (c ->> 'amount_minor')::bigint)::text) ORDER BY o.accounting_date, o.sequence, o.pair_key)
            FROM jsonb_array_elements(checked -> 'costs') c
                JOIN public.open_costs(t, creditor, debtor, owed.beneficiary_account_id, NULL) o ON o.pair_key = c ->> 'pair_key'), '[]'::jsonb));
END $$;
REVOKE ALL ON FUNCTION public.settlement_preview(uuid, uuid, uuid, jsonb) FROM PUBLIC;
