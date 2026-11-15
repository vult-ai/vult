-- Follow-ups to settling up between linked Books: an agent's overpaying
-- half is refused whichever comes first, unnamed payments repay the oldest
-- first, an opening balance is repaid like a cost, and turning a link off
-- needs only a valid sign-in.

-- Each half checks, from its own Book, that it pays no more than is owed.
CREATE OR REPLACE FUNCTION public.pair_settlement(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; args jsonb; oargs jsonb; key text;
    debtor uuid; creditor uuid; dp uuid; cp uuid; dargs jsonb; cargs jsonb; checked jsonb; paid bigint; owing bigint;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed'
                AND (x.payer_book_id = b AND x.payer_account_id::text = args ->> 'other_account_id' OR x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id')) THEN
        RAISE EXCEPTION 'The settlement can''t be prepared: other_account_id is no account an owed direction of an active link names in this Book.' USING ERRCODE = 'VP020';
    END IF;
    -- Each half checks, from its own Book, that it pays no more than is owed,
    -- so an overpayment is refused on whichever half comes first.
    SELECT abs(x.amount_minor) INTO paid FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = b AND x.id::text = args ->> 'transaction_id';
    SELECT coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO owing
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = b AND x.account_id::text = args ->> 'other_account_id';
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t AND x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id') THEN
        owing := -owing;
    END IF;
    IF paid > greatest(owing, 0) THEN
        RAISE EXCEPTION 'This pays % more than is owed. The owner records it in Vult, which records the extra plainly; don''t prepare it as a settlement.',
            public.book_amount(t, b, paid - greatest(owing, 0)) USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id <> b AND q.operation = 'record_bank_activity'
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' -> 'settlement') = 'true'::jsonb
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
            AND NOT EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = q.tenant_id
                AND (x.payer_book_id = q.book_id AND x.payer_proposal_id = q.id OR x.beneficiary_book_id = q.book_id AND x.beneficiary_proposal_id = q.id))
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF coalesce(args -> 'repays', '[]'::jsonb) <> coalesce(oargs -> 'repays', '[]'::jsonb) THEN
        RAISE EXCEPTION 'The settlement can''t be prepared: both halves name the same costs in repays.' USING ERRCODE = 'VP020';
    END IF;
    -- The Book whose account is the owed direction's payable owes.
    IF EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t AND x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id') THEN
        debtor := b; dp := p; dargs := args; creditor := other.book_id; cp := other.id; cargs := oargs;
    ELSE
        debtor := other.book_id; dp := other.id; dargs := oargs; creditor := b; cp := p; cargs := args;
    END IF;
    checked := public.settlement_check(t, debtor, (dargs ->> 'other_account_id')::uuid, (dargs ->> 'transaction_id')::uuid,
        creditor, (cargs ->> 'other_account_id')::uuid, (cargs ->> 'transaction_id')::uuid, args -> 'repays', NULL);
    IF (checked ->> 'excess')::bigint > 0 THEN
        RAISE EXCEPTION 'This pays % more than is owed. The owner records it in Vult, which records the extra plainly; don''t prepare it as a settlement.',
            public.book_amount(t, debtor, (checked ->> 'excess')::bigint) USING ERRCODE = 'VP020';
    END IF;
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, kind, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id)
        VALUES (t, key, 'settlement', debtor, dp, creditor, cp, (dargs ->> 'transaction_id')::uuid);
END $$;

-- Who owes whom counts every payment toward the costs, and the opening
-- balance, it covers: named
-- costs are repaid by their settlement, and the rest are repaid oldest
-- first by whatever paid down the balance, so nothing reads open once
-- nothing is owed.
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
    -- Cost pairs along each owed direction, with what is still owed there.
    SELECT p.*, d.link_id, d.creditor, e.accounting_date, e.description, e.sequence,
        (SELECT sum(z.amount_minor) FROM public.lines z WHERE z.tenant_id = t AND z.book_id = p.beneficiary_book_id AND z.entry_id = p.beneficiary_entry_id AND z.side = 'credit')::bigint AS amount,
        public.cost_repaid_by(t, p.beneficiary_book_id, p.beneficiary_entry_id, NULL) IS NOT NULL AS named,
        greatest(d.debtor_says, 0) AS owed
    FROM public.entry_pairs p JOIN dirs d ON (p.kind = 'cost' AND d.creditor = p.payer_book_id AND d.debtor = p.beneficiary_book_id
            OR p.kind = 'opening' AND d.creditor = p.beneficiary_book_id AND d.debtor = p.payer_book_id)
        JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
    WHERE p.tenant_id = t AND p.kind IN ('cost', 'opening') AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)
), ranked AS (
    -- Payments that named no costs repay the oldest first: a cost is open
    -- only while what is still owed reaches back to it, newest first.
    SELECT c.*, sum(CASE WHEN c.named THEN 0 ELSE c.amount END)
        OVER (PARTITION BY c.link_id, c.creditor ORDER BY c.accounting_date DESC, c.sequence DESC, c.pair_key DESC ROWS UNBOUNDED PRECEDING) AS newer
    FROM costs c
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
                'repaid', r.named OR r.newer - r.amount >= r.owed,
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


-- Turning a link or a direction off only narrows what can happen, so any
-- valid sign-in of the owner may do it, as disconnecting an agent; turning
-- one on, or changing its terms, needs a recent sign-in.
CREATE FUNCTION public.link_owner_narrowing(t uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = ''
    AND coalesce(current_setting('vult.owner_posting', true), '') = ''
    AND access.owner_allowed(t)
    AND coalesce(access.owner_identity(t)::text = current_setting('vult.actor_id', true), false)
$$;
REVOKE ALL ON FUNCTION public.link_owner_narrowing(uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.book_link_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END)
        AND NOT (TG_OP = 'UPDATE' AND public.link_owner_narrowing(NEW.tenant_id) AND NOT NEW.active
            AND ROW(NEW.tenant_id, NEW.id, NEW.book_a, NEW.book_b) IS NOT DISTINCT FROM ROW(OLD.tenant_id, OLD.id, OLD.book_a, OLD.book_b)) THEN
        RAISE EXCEPTION 'only the owner links Books' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND (p.payer_book_id, p.beneficiary_book_id) IN ((OLD.book_a, OLD.book_b), (OLD.book_b, OLD.book_a)))
            OR EXISTS (SELECT 1 FROM public.proposal_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND (p.payer_book_id, p.beneficiary_book_id) IN ((OLD.book_a, OLD.book_b), (OLD.book_b, OLD.book_a))) THEN
            RAISE EXCEPTION 'These Books have pairs between them; turn the link off instead.' USING ERRCODE = 'VP020';
        END IF;
        RETURN OLD;
    END IF;
    IF NOT public.ledger_import_owner() AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF ROW(NEW.tenant_id, NEW.id, NEW.book_a, NEW.book_b, NEW.created_at) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.id, OLD.book_a, OLD.book_b, OLD.created_at) THEN
            RAISE EXCEPTION 'a link keeps its Books' USING ERRCODE = '23514';
        END IF;
        IF NEW.revision <> OLD.revision + 1 THEN
            RAISE EXCEPTION 'the link changed concurrently' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.revision <> 1 THEN
        RAISE EXCEPTION 'the link changed concurrently' USING ERRCODE = '23514';
    END IF;
    NEW.updated_at := clock_timestamp();
    IF (SELECT k.currency FROM public.books k WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.book_a)
        IS DISTINCT FROM (SELECT k.currency FROM public.books k WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.book_b) THEN
        RAISE EXCEPTION 'Linked Books keep the same currency.' USING ERRCODE = 'VP020';
    END IF;
    IF public.book_profile(NEW.tenant_id, NEW.book_a) IS NULL OR public.book_profile(NEW.tenant_id, NEW.book_b) IS NULL THEN
        RAISE EXCEPTION 'Choose Personal or Business for both Books before linking them.' USING ERRCODE = 'VP020';
    END IF;
    IF public.book_profile(NEW.tenant_id, NEW.book_a) = 'personal' AND public.book_profile(NEW.tenant_id, NEW.book_b) = 'personal' THEN
        RAISE EXCEPTION 'Two personal Books aren''t linked; link a personal Book to a business.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION public.book_link_terms_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE lk public.book_links%ROWTYPE; pa public.accounts%ROWTYPE; ba public.accounts%ROWTYPE; pp text; bp text;
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END)
        AND NOT (TG_OP = 'UPDATE' AND public.link_owner_narrowing(NEW.tenant_id) AND NOT (NEW.active AND NOT OLD.active)
            AND ROW(NEW.tenant_id, NEW.link_id, NEW.payer_book_id, NEW.beneficiary_book_id, NEW.treatment, NEW.payer_account_id, NEW.beneficiary_account_id)
                IS NOT DISTINCT FROM ROW(OLD.tenant_id, OLD.link_id, OLD.payer_book_id, OLD.beneficiary_book_id, OLD.treatment, OLD.payer_account_id, OLD.beneficiary_account_id)) THEN
        RAISE EXCEPTION 'only the owner links Books' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN
        -- Terms go only with their link, and only while no pair names either direction.
        IF EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND p.payer_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id)
                AND p.beneficiary_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id))
            OR EXISTS (SELECT 1 FROM public.proposal_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND p.payer_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id)
                AND p.beneficiary_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id)) THEN
            RAISE EXCEPTION 'These Books have pairs between them; turn the link off instead.' USING ERRCODE = 'VP020';
        END IF;
        RETURN OLD;
    END IF;
    IF NOT public.ledger_import_owner() AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    NEW.updated_at := clock_timestamp();
    SELECT * INTO lk FROM public.book_links WHERE tenant_id = NEW.tenant_id AND id = NEW.link_id;
    IF NOT ((NEW.payer_book_id, NEW.beneficiary_book_id) IN ((lk.book_a, lk.book_b), (lk.book_b, lk.book_a))) THEN
        RAISE EXCEPTION 'a direction is between the link''s two Books' USING ERRCODE = '23514';
    END IF;
    pp := public.book_profile(NEW.tenant_id, NEW.payer_book_id);
    bp := public.book_profile(NEW.tenant_id, NEW.beneficiary_book_id);
    SELECT * INTO pa FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.payer_book_id AND id = NEW.payer_account_id;
    SELECT * INTO ba FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.beneficiary_book_id AND id = NEW.beneficiary_account_id;
    IF pa.status = 'retired' OR pa.heading OR ba.status = 'retired' OR ba.heading THEN
        RAISE EXCEPTION 'A link uses accounts that take postings.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'contribution' AND NOT (pp = 'personal' AND bp = 'business') THEN
        RAISE EXCEPTION 'Money put in is only from a personal Book to a business.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'owner_equity' AND NOT (pp = 'business' AND bp = 'personal') THEN
        RAISE EXCEPTION 'A draw or distribution is only from a business to a personal Book.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'contribution' AND (pa.class <> 'asset' OR pa.role IS DISTINCT FROM 'investment' OR ba.class <> 'equity') THEN
        RAISE EXCEPTION 'Money put in is recorded to an investment asset in the personal Book and an equity account in the business.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'owner_equity' AND (pa.class <> 'equity' OR ba.class <> 'asset' OR ba.role IS DISTINCT FROM 'investment') THEN
        RAISE EXCEPTION 'A draw or distribution is recorded to an equity account in the business and an investment asset in the personal Book.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'owed' AND (pa.class <> 'asset' OR pa.role IS DISTINCT FROM 'receivable' OR ba.class <> 'liability' OR ba.role IS DISTINCT FROM 'payable') THEN
        RAISE EXCEPTION 'Money owed back is recorded to a receivable in the Book that paid and a payable in the Book it paid for.' USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = NEW.tenant_id AND NOT (x.link_id = NEW.link_id AND x.payer_book_id = NEW.payer_book_id)
            AND (x.payer_book_id = NEW.payer_book_id AND x.payer_account_id = NEW.payer_account_id
                OR x.beneficiary_book_id = NEW.payer_book_id AND x.beneficiary_account_id = NEW.payer_account_id
                OR x.payer_book_id = NEW.beneficiary_book_id AND x.payer_account_id = NEW.beneficiary_account_id
                OR x.beneficiary_book_id = NEW.beneficiary_book_id AND x.beneficiary_account_id = NEW.beneficiary_account_id)) THEN
        RAISE EXCEPTION 'Each account carries one direction of one link, so its balance is that link''s; choose or add another account.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;

CREATE POLICY owner_narrow ON public.book_links FOR UPDATE USING (public.link_owner_narrowing(tenant_id)) WITH CHECK (public.link_owner_narrowing(tenant_id));
CREATE POLICY owner_narrow ON public.book_link_terms FOR UPDATE USING (public.link_owner_narrowing(tenant_id)) WITH CHECK (public.link_owner_narrowing(tenant_id));


-- An opening balance is repaid like a cost: a settlement may name it in
-- repays, and an unnamed payment repays it, oldest first, with the costs.
DROP FUNCTION public.settlement_check(uuid, uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid);
CREATE FUNCTION public.settlement_check(t uuid, debtor uuid, debtor_account uuid, debtor_item uuid, creditor uuid, creditor_account uuid, creditor_item uuid,
        repays jsonb, leaving_out uuid) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.bank_transactions%ROWTYPE; c public.bank_transactions%ROWTYPE; dclass text; cclass text; owed bigint; total bigint := 0;
    k text; entry uuid; entry_book uuid; cost bigint; amount bigint; excess bigint; repaying bigint; costs uuid[] := '{}'; items jsonb := '[]'; rev public.book_link_terms%ROWTYPE;
    required boolean; debtor_name text; creditor_name text; r record;
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
    IF jsonb_typeof(coalesce(repays, '[]'::jsonb)) <> 'array' THEN
        RAISE EXCEPTION 'repays is a list of pair keys.' USING ERRCODE = 'VP020';
    END IF;
    required := public.book_profile(t, creditor) = 'personal' AND public.book_profile(t, debtor) = 'business' AND rev.treatment IS DISTINCT FROM 'owner_equity';
    FOR k IN SELECT jsonb_array_elements_text(coalesce(repays, '[]'::jsonb)) LOOP
        entry := NULL;
        -- A cost the creditor paid for the debtor, or the debtor's opening
        -- balance owed to the creditor; each is kept by its entry in the
        -- pair's beneficiary Book.
        SELECT p.beneficiary_book_id, p.beneficiary_entry_id INTO entry_book, entry FROM public.entry_pairs p
            WHERE p.tenant_id = t AND p.pair_key = k
                AND (p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
                    OR p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor)
                AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id);
        IF entry IS NULL THEN
            RAISE EXCEPTION 'repays names %, which isn''t a cost % paid for % or an opening balance % owes; name one from the open items.', k, creditor_name, debtor_name, debtor_name USING ERRCODE = 'VP020';
        END IF;
        IF entry = ANY (costs) THEN
            RAISE EXCEPTION 'repays names % twice; name each cost once.', k USING ERRCODE = 'VP020';
        END IF;
        IF public.cost_repaid_by(t, entry_book, entry, leaving_out) IS NOT NULL THEN
            RAISE EXCEPTION 'The cost % is already repaid; name an open cost instead.', k USING ERRCODE = 'VP020';
        END IF;
        costs := costs || entry;
        items := items || jsonb_build_array(jsonb_build_object('book', entry_book, 'entry', entry));
        total := total + (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = entry_book AND x.entry_id = entry AND x.side = 'credit');
    END LOOP;
    IF cardinality(costs) = 0 AND required THEN
        RAISE EXCEPTION '% repaying % names what it repays (repays: costs, or the opening balance), adding up to %; pick them from the open items.',
            debtor_name, creditor_name, public.book_amount(t, debtor, repaying) USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) > 0 AND total <> repaying THEN
        RAISE EXCEPTION 'The costs named add up to %, but this payment repays %; name costs that add up to it exactly.',
            public.book_amount(t, debtor, total), public.book_amount(t, debtor, repaying) USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) = 0 THEN
        -- Unnamed: the oldest open costs the payment covers whole.
        FOR r IN SELECT p.beneficiary_book_id AS book, p.beneficiary_entry_id AS entry,
                (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = p.beneficiary_book_id AND x.entry_id = p.beneficiary_entry_id AND x.side = 'credit') AS amount
            FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
            WHERE p.tenant_id = t
                AND (p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
                    OR p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor)
                AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)
                AND public.cost_repaid_by(t, p.beneficiary_book_id, p.beneficiary_entry_id, leaving_out) IS NULL
            ORDER BY e.accounting_date, e.sequence LOOP
            EXIT WHEN total + r.amount > repaying;
            costs := costs || r.entry;
            items := items || jsonb_build_array(jsonb_build_object('book', r.book, 'entry', r.entry));
            total := total + r.amount;
        END LOOP;
    END IF;
    RETURN jsonb_build_object('costs', items, 'excess', excess, 'repaying', repaying, 'required', required);
END $$;

-- Records a settlement the owner decided, or recorded at once: each entry
-- records its own bank item, on its date, to the owed direction's account
-- for what was owed and, for any excess, to the reverse direction's
-- account. The costs it repays are kept.
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
        INSERT INTO public.settlement_repays(tenant_id, settlement_book_id, settlement_entry_id, cost_book_id, cost_entry_id)
            VALUES (t, debtor, debtor_entry, (item ->> 'book')::uuid, (item ->> 'entry')::uuid);
    END LOOP;
END $$;

REVOKE ALL ON FUNCTION public.settlement_check(uuid, uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid) FROM PUBLIC;

-- An opening balance a settlement repays is undone only after it.
CREATE OR REPLACE FUNCTION public.entry_pair_of(t uuid, b uuid, e uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE (
    SELECT jsonb_build_object('pair_key', x.pair_key, 'kind', x.kind,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'entry_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_entry_id ELSE x.payer_entry_id END,
        'side', CASE WHEN x.payer_book_id = b THEN 'beneficiary' ELSE 'payer' END,
        'repaid_by', CASE WHEN x.kind IN ('cost', 'opening') THEN public.cost_repaid_by(t, x.beneficiary_book_id, x.beneficiary_entry_id, NULL) END)
    FROM public.entry_pairs x WHERE x.tenant_id = t
        AND (x.payer_book_id = b AND x.payer_entry_id = e OR x.beneficiary_book_id = b AND x.beneficiary_entry_id = e)) END
$$;
