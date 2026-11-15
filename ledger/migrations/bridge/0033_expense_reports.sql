-- One stored record of which payment repaid which cost, and expense reports
-- built on it.
--
-- Until now a settlement kept the part of each cost it repaid, but a payment
-- recorded as a plain entry on what one Book owes another was spread over
-- the oldest costs each time it was read, so an older cost added later
-- moved it. From here every reduction of what is owed stores its own
-- allocation when it is posted, and reading never re-attributes. What
-- anyone saw before this migration is frozen as it was.

-- Allocations: a reduction of what a Book owes (a settlement, a plain
-- payment, a report payment or a correction), the cost or claim it repaid,
-- and how much. Removing one (unmatching) keeps its history.
CREATE TABLE public.allocations (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    debtor_book_id uuid NOT NULL,
    creditor_book_id uuid NOT NULL,
    -- The entry in the paying Book that reduced what it owes. Empty only for
    -- an amount frozen from before allocations were stored that no entry
    -- accounted for.
    source_book_id uuid,
    source_entry_id uuid,
    obligation_book_id uuid NOT NULL,
    obligation_entry_id uuid NOT NULL,
    claim_id uuid,
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    fx_difference_minor bigint NOT NULL DEFAULT 0,
    source text NOT NULL CHECK (source IN ('settlement', 'posting', 'report_payment', 'correction')),
    provenance text NOT NULL DEFAULT 'recorded' CHECK (provenance IN ('recorded', 'migrated_settlement', 'frozen_at_migration')),
    unapproved boolean NOT NULL DEFAULT false,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    actor_id uuid,
    removed_at timestamptz,
    removed_by uuid,
    removal_reason text CHECK (removal_reason IS NULL OR length(removal_reason) BETWEEN 1 AND 500),
    PRIMARY KEY (tenant_id, id),
    CHECK ((source_book_id IS NULL) = (source_entry_id IS NULL)),
    CHECK (source_entry_id IS NOT NULL OR provenance = 'frozen_at_migration'),
    CHECK (removed_at IS NULL AND removed_by IS NULL AND removal_reason IS NULL OR removed_at IS NOT NULL AND removed_by IS NOT NULL AND removal_reason IS NOT NULL),
    -- The obligation is a pair's beneficiary entry, or a cash claim's
    -- business entry before the person's side is recorded: checked by
    -- obligation_known.
    FOREIGN KEY (tenant_id, source_book_id, source_entry_id) REFERENCES public.entries(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, obligation_book_id, obligation_entry_id) REFERENCES public.entries(tenant_id, book_id, id)
);
CREATE INDEX allocations_obligation ON public.allocations(tenant_id, obligation_book_id, obligation_entry_id) WHERE removed_at IS NULL;
CREATE INDEX allocations_source ON public.allocations(tenant_id, source_book_id, source_entry_id);
CREATE UNIQUE INDEX allocations_once ON public.allocations(tenant_id, source_book_id, source_entry_id, obligation_book_id, obligation_entry_id, coalesce(claim_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE removed_at IS NULL AND source_entry_id IS NOT NULL;
ALTER TABLE public.allocations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.allocations FORCE ROW LEVEL SECURITY;
CREATE POLICY allocations_read ON public.allocations FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, debtor_book_id, false));
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.allocations
    FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('creditor_book_id', 'debtor_book_id', 'obligation_book_id', 'source_book_id');
REVOKE ALL ON public.allocations FROM PUBLIC;

-- Allocations are only ever added and removed, never rewritten.
CREATE FUNCTION public.allocations_append_only() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF current_setting('vult.erasing', true) = 'on' THEN RETURN OLD; END IF;
        RAISE EXCEPTION 'An allocation is removed, never deleted.' USING ERRCODE = '42501';
    END IF;
    IF OLD.removed_at IS NOT NULL OR NEW.removed_at IS NULL
        OR (to_jsonb(NEW) - 'removed_at' - 'removed_by' - 'removal_reason') <> (to_jsonb(OLD) - 'removed_at' - 'removed_by' - 'removal_reason') THEN
        RAISE EXCEPTION 'An allocation only changes by being removed.' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER allocations_append_only BEFORE UPDATE OR DELETE ON public.allocations
    FOR EACH ROW EXECUTE FUNCTION public.allocations_append_only();

-- Whether an allocation counts: not removed, and its payment not undone.
CREATE FUNCTION public.allocation_live(t uuid, a public.allocations) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT a.removed_at IS NULL AND (a.source_entry_id IS NULL OR NOT public.entry_reversed(t, a.source_book_id, a.source_entry_id))
$$;

-- Settlements' rows move over with the same amounts. A row with no amount
-- repaid the whole cost.
INSERT INTO public.allocations(tenant_id, id, debtor_book_id, creditor_book_id, source_book_id, source_entry_id,
        obligation_book_id, obligation_entry_id, amount_minor, source, provenance)
SELECT r.tenant_id, gen_random_uuid(), r.settlement_book_id, p.beneficiary_book_id, r.settlement_book_id, r.settlement_entry_id,
    r.cost_book_id, r.cost_entry_id, coalesce(r.amount_minor, public.cost_amount(r.tenant_id, r.cost_book_id, r.cost_entry_id)), 'settlement', 'migrated_settlement'
FROM public.settlement_repays r
JOIN public.entry_pairs p ON p.tenant_id = r.tenant_id AND p.payer_book_id = r.settlement_book_id AND p.payer_entry_id = r.settlement_entry_id
WHERE coalesce(r.amount_minor, public.cost_amount(r.tenant_id, r.cost_book_id, r.cost_entry_id)) > 0;

DO $$
BEGIN
    IF (SELECT count(*) FROM public.settlement_repays r WHERE coalesce(r.amount_minor, public.cost_amount(r.tenant_id, r.cost_book_id, r.cost_entry_id)) > 0)
        <> (SELECT count(*) FROM public.allocations) THEN
        RAISE EXCEPTION 'a settlement could not be carried over; nothing was changed';
    END IF;
END $$;

-- How much of a cost live allocations repaid; other_than leaves one
-- payment out.
CREATE OR REPLACE FUNCTION public.cost_repaid_amount(t uuid, b uuid, e uuid, other_than uuid) RETURNS bigint
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(sum(a.amount_minor), 0)::bigint FROM public.allocations a
WHERE a.tenant_id = t AND a.obligation_book_id = b AND a.obligation_entry_id = e
    AND (other_than IS NULL OR a.source_entry_id IS DISTINCT FROM other_than) AND public.allocation_live(t, a)
$$;

-- The payment that finished repaying a cost, or NULL while any of it is open.
CREATE OR REPLACE FUNCTION public.cost_repaid_by(t uuid, b uuid, e uuid, other_than uuid) RETURNS text
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT CASE WHEN public.cost_repaid_amount(t, b, e, other_than) >= public.cost_amount(t, b, e) THEN (
    SELECT coalesce(p.pair_key, a.source_entry_id::text) FROM public.allocations a
        LEFT JOIN public.entry_pairs p ON p.tenant_id = a.tenant_id AND p.kind = 'settlement' AND p.payer_book_id = a.source_book_id AND p.payer_entry_id = a.source_entry_id
        LEFT JOIN public.entries s ON s.tenant_id = a.tenant_id AND s.book_id = a.source_book_id AND s.id = a.source_entry_id
    WHERE a.tenant_id = t AND a.obligation_book_id = b AND a.obligation_entry_id = e AND (other_than IS NULL OR a.source_entry_id IS DISTINCT FROM other_than)
        AND public.allocation_live(t, a)
    ORDER BY s.accounting_date DESC NULLS LAST, s.sequence DESC NULLS LAST, a.recorded_at DESC LIMIT 1) END
$$;

-- The costs a debtor owes a creditor, oldest first, as the open costs read
-- did before allocations were stored: what is still owed on the debtor's
-- account reaches back from the newest. Kept only to freeze that view.
CREATE FUNCTION public.legacy_open_costs(t uuid, creditor uuid, debtor uuid, debtor_account uuid)
    RETURNS TABLE (book_id uuid, entry_id uuid, accounting_date date, sequence bigint, pair_key text, amount bigint, repaid bigint, open bigint)
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
WITH owed AS (
    SELECT greatest(coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0), 0)::bigint AS amount
    FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.account_id = debtor_account
), costs AS (
    SELECT p.pair_key, p.beneficiary_book_id AS book_id, p.beneficiary_entry_id AS entry_id, e.accounting_date, e.sequence::bigint AS sequence,
        public.cost_amount(t, p.beneficiary_book_id, p.beneficiary_entry_id) AS amount
    FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
    WHERE p.tenant_id = t
        AND (p.kind IN ('cost', 'carried') AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
            OR p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor)
        AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)
), repaid AS (
    SELECT c.*, least(c.amount, public.cost_repaid_amount(t, c.book_id, c.entry_id, NULL)) AS repaid FROM costs c
), ranked AS (
    SELECT r.*, sum(r.amount - r.repaid) OVER (ORDER BY r.accounting_date DESC, r.sequence DESC, r.pair_key DESC ROWS UNBOUNDED PRECEDING) AS newer
    FROM repaid r
)
SELECT r.book_id, r.entry_id, r.accounting_date, r.sequence, r.pair_key, r.amount, r.repaid,
    least(r.amount - r.repaid, greatest((SELECT o.amount FROM owed o) - (r.newer - (r.amount - r.repaid)), 0))::bigint AS open
FROM ranked r
$$;

-- The entries of a debtor's Book that reduce what it owes on its account
-- and aren't settlements or undone: their net debit to the account.
CREATE FUNCTION public.reducing_entries(t uuid, debtor uuid, debtor_account uuid)
    RETURNS TABLE (entry_id uuid, accounting_date date, sequence bigint, amount bigint)
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT e.id, e.accounting_date, e.sequence::bigint, sum(CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END)::bigint
FROM public.lines x JOIN public.entries e ON e.tenant_id = x.tenant_id AND e.book_id = x.book_id AND e.id = x.entry_id
WHERE x.tenant_id = t AND x.book_id = debtor AND x.account_id = debtor_account
    AND e.reversal_of IS NULL AND NOT public.entry_reversed(t, debtor, e.id)
    AND NOT EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = t AND p.kind = 'settlement' AND p.payer_book_id = debtor AND p.payer_entry_id = e.id)
GROUP BY e.id, e.accounting_date, e.sequence
HAVING sum(CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END) > 0
$$;

-- Freezes, for one owed direction, what the open costs read attributed at
-- read time to payments that weren't settlements: the same amounts, oldest
-- cost first, given to those payments in date order, so every cost's open
-- amount stays as it was. What no payment accounts for is kept with none.
CREATE FUNCTION public.freeze_allocations(t uuid, creditor uuid, debtor uuid, debtor_account uuid) RETURNS integer
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE c record; e record; need bigint; left_in_entry bigint := 0; entry uuid; part bigint; n integer := 0;
    entries_cursor refcursor;
BEGIN
    OPEN entries_cursor FOR SELECT r.entry_id, r.amount - coalesce((SELECT sum(a.amount_minor) FROM public.allocations a
            WHERE a.tenant_id = t AND a.source_book_id = debtor AND a.source_entry_id = r.entry_id AND public.allocation_live(t, a)), 0) AS free
        FROM public.reducing_entries(t, debtor, debtor_account) r ORDER BY r.accounting_date, r.sequence;
    FOR c IN SELECT o.* FROM public.legacy_open_costs(t, creditor, debtor, debtor_account) o
        WHERE o.amount - o.repaid - o.open > 0 ORDER BY o.accounting_date, o.sequence, o.pair_key LOOP
        need := c.amount - c.repaid - c.open;
        WHILE need > 0 LOOP
            IF left_in_entry = 0 THEN
                FETCH entries_cursor INTO e;
                IF NOT FOUND THEN
                    entry := NULL;
                    left_in_entry := need;
                ELSE
                    entry := e.entry_id;
                    left_in_entry := greatest(e.free, 0);
                    CONTINUE WHEN left_in_entry = 0;
                END IF;
            END IF;
            part := least(need, left_in_entry);
            INSERT INTO public.allocations(tenant_id, id, debtor_book_id, creditor_book_id, source_book_id, source_entry_id,
                    obligation_book_id, obligation_entry_id, amount_minor, source, provenance)
                VALUES (t, gen_random_uuid(), debtor, creditor, CASE WHEN entry IS NOT NULL THEN debtor END, entry,
                    c.book_id, c.entry_id, part, CASE WHEN entry IS NULL THEN 'correction' ELSE 'posting' END, 'frozen_at_migration');
            need := need - part;
            left_in_entry := left_in_entry - part;
            n := n + 1;
        END LOOP;
    END LOOP;
    CLOSE entries_cursor;
    RETURN n;
END $$;

-- Every owed direction a Book can have: who pays, who owes, and the account
-- in the owing Book.
CREATE FUNCTION public.owed_directions(t uuid)
    RETURNS TABLE (creditor uuid, debtor uuid, debtor_account uuid)
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT x.payer_book_id, x.beneficiary_book_id, x.beneficiary_account_id FROM public.book_link_terms x
WHERE x.tenant_id = t AND x.treatment = 'owed'
UNION
-- A Book restored without the other keeps its costs' pairs but not the
-- link: the direction is the pair's, along the account its cost entry
-- credits, as the read always took it.
SELECT p.payer_book_id, p.beneficiary_book_id, z.account_id FROM public.entry_pairs p
    JOIN public.lines z ON z.tenant_id = p.tenant_id AND z.book_id = p.beneficiary_book_id AND z.entry_id = p.beneficiary_entry_id AND z.side = 'credit'
WHERE p.tenant_id = t AND p.kind IN ('cost', 'carried')
    AND NOT EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t AND x.treatment = 'owed' AND x.payer_book_id = p.payer_book_id
        AND x.beneficiary_book_id = p.beneficiary_book_id)
$$;

-- Freezes every owed direction of a Vult, and checks nothing a person sees
-- moved: each cost's open amount is the same as the read before.
CREATE FUNCTION public.freeze_vult_allocations(t uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d record; before jsonb := '[]'; bad text;
BEGIN
    FOR d IN SELECT * FROM public.owed_directions(t) LOOP
        before := before || coalesce((SELECT jsonb_agg(jsonb_build_object('book_id', o.book_id, 'entry_id', o.entry_id, 'open', o.open))
            FROM public.legacy_open_costs(t, d.creditor, d.debtor, d.debtor_account) o), '[]');
        PERFORM public.freeze_allocations(t, d.creditor, d.debtor, d.debtor_account);
    END LOOP;
    SELECT string_agg(f.entry_id::text || ' (' || f.open || ' open before, ' || (public.cost_amount(t, f.book_id, f.entry_id)
            - least(public.cost_amount(t, f.book_id, f.entry_id), public.cost_repaid_amount(t, f.book_id, f.entry_id, NULL))) || ' after)', ', ') INTO bad
        FROM jsonb_to_recordset(before) AS f(book_id uuid, entry_id uuid, open bigint)
    WHERE f.open <> public.cost_amount(t, f.book_id, f.entry_id) - least(public.cost_amount(t, f.book_id, f.entry_id), public.cost_repaid_amount(t, f.book_id, f.entry_id, NULL));
    IF bad IS NOT NULL THEN
        RAISE EXCEPTION 'freezing what was repaid would change what is open for %; nothing was changed', bad;
    END IF;
END $$;
REVOKE ALL ON FUNCTION public.legacy_open_costs(uuid, uuid, uuid, uuid), public.reducing_entries(uuid, uuid, uuid),
    public.freeze_allocations(uuid, uuid, uuid, uuid), public.owed_directions(uuid), public.freeze_vult_allocations(uuid),
    public.allocation_live(uuid, public.allocations) FROM PUBLIC;

DO $$
DECLARE t uuid;
BEGIN
    FOR t IN SELECT id FROM public.tenants LOOP
        PERFORM public.freeze_vult_allocations(t);
    END LOOP;
END $$;

-- The open costs, now simply what each cost amounts to less what live
-- allocations repaid. leaving_out leaves one payment out.
CREATE OR REPLACE FUNCTION public.open_costs(t uuid, creditor uuid, debtor uuid, debtor_account uuid, leaving_out uuid)
    RETURNS TABLE (pair_key text, kind text, payer_book_id uuid, payer_entry_id uuid, beneficiary_book_id uuid, beneficiary_entry_id uuid,
        book_id uuid, entry_id uuid, accounting_date date, sequence bigint, description text, amount bigint, repaid bigint, open bigint)
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
WITH costs AS (
    SELECT p.pair_key, p.kind, p.payer_book_id, p.payer_entry_id, p.beneficiary_book_id, p.beneficiary_entry_id,
        p.beneficiary_book_id AS book_id, p.beneficiary_entry_id AS entry_id, e.accounting_date, e.sequence::bigint AS sequence, e.description,
        public.cost_amount(t, p.beneficiary_book_id, p.beneficiary_entry_id) AS amount
    FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
    WHERE p.tenant_id = t
        AND (p.kind IN ('cost', 'carried', 'claim') AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
            OR p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor)
        AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)
)
SELECT c.pair_key, c.kind, c.payer_book_id, c.payer_entry_id, c.beneficiary_book_id, c.beneficiary_entry_id, c.book_id, c.entry_id,
    c.accounting_date, c.sequence, c.description, c.amount, least(c.amount, r.repaid)::bigint, (c.amount - least(c.amount, r.repaid))::bigint
FROM costs c CROSS JOIN LATERAL (SELECT public.cost_repaid_amount(t, c.book_id, c.entry_id, leaving_out) AS repaid) r
ORDER BY c.accounting_date, c.sequence, c.pair_key
$$;

-- What a payment reduced but no cost was open for: paid ahead, shown as
-- unapplied.
CREATE FUNCTION public.unapplied_amount(t uuid, creditor uuid, debtor uuid, debtor_account uuid) RETURNS bigint
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(sum(greatest(r.amount - coalesce((SELECT sum(a.amount_minor) FROM public.allocations a
    WHERE a.tenant_id = t AND a.source_book_id = debtor AND a.source_entry_id = r.entry_id AND public.allocation_live(t, a)), 0), 0)), 0)::bigint
FROM public.reducing_entries(t, debtor, debtor_account) r
$$;
REVOKE ALL ON FUNCTION public.unapplied_amount(uuid, uuid, uuid, uuid) FROM PUBLIC;

-- Stores, for an entry that reduces what one Book owes another and isn't a
-- settlement, the oldest open costs it repays, when it's posted. An entry
-- that already has allocations (a settlement or report payment in the same
-- transaction) is left as it is.
CREATE FUNCTION public.allocate_posting(t uuid, b uuid, e uuid) RETURNS integer
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d record; r record; total bigint; part bigint; n integer := 0;
BEGIN
    IF current_setting('vult.allocating', true) = 'on'
        OR EXISTS (SELECT 1 FROM public.allocations a WHERE a.tenant_id = t AND a.source_book_id = b AND a.source_entry_id = e)
        OR EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = t AND p.kind = 'settlement' AND p.payer_book_id = b AND p.payer_entry_id = e)
        OR EXISTS (SELECT 1 FROM public.entries x WHERE x.tenant_id = t AND x.book_id = b AND x.id = e AND x.reversal_of IS NOT NULL) THEN
        RETURN 0;
    END IF;
    FOR d IN SELECT o.* FROM public.owed_directions(t) o WHERE o.debtor = b LOOP
        SELECT coalesce(sum(CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO total
            FROM public.lines x WHERE x.tenant_id = t AND x.book_id = b AND x.entry_id = e AND x.account_id = d.debtor_account;
        CONTINUE WHEN total <= 0;
        FOR r IN SELECT o.* FROM public.open_costs(t, d.creditor, d.debtor, d.debtor_account, e) o WHERE o.open > 0
            ORDER BY o.accounting_date, o.sequence, o.pair_key LOOP
            EXIT WHEN total <= 0;
            part := least(r.open, total);
            INSERT INTO public.allocations(tenant_id, id, debtor_book_id, creditor_book_id, source_book_id, source_entry_id,
                    obligation_book_id, obligation_entry_id, amount_minor, source)
                VALUES (t, gen_random_uuid(), b, d.creditor, b, e, r.book_id, r.entry_id, part, 'posting');
            total := total - part;
            n := n + 1;
        END LOOP;
    END LOOP;
    RETURN n;
END $$;

CREATE FUNCTION public.allocate_posting_line() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.side = 'debit' AND EXISTS (SELECT 1 FROM public.book_link_terms x
            WHERE x.tenant_id = NEW.tenant_id AND x.treatment = 'owed' AND x.beneficiary_book_id = NEW.book_id AND x.beneficiary_account_id = NEW.account_id) THEN
        PERFORM public.allocate_posting(NEW.tenant_id, NEW.book_id, NEW.entry_id);
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER allocate_posting AFTER INSERT ON public.lines DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.allocate_posting_line();
REVOKE ALL ON FUNCTION public.allocate_posting(uuid, uuid, uuid), public.allocate_posting_line() FROM PUBLIC;


-- Settlements keep what they repaid in allocations, like every other
-- payment.
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
    -- An entry first recorded as a plain payment, then settled, becomes the
    -- settlement: what it repaid as a plain payment is removed.
    UPDATE public.allocations a SET removed_at = clock_timestamp(), removed_by = '00000000-0000-0000-0000-000000000000'::uuid,
            removal_reason = 'Recorded as a settlement.'
        WHERE a.tenant_id = t AND a.source_book_id = debtor AND a.source_entry_id = debtor_entry AND a.source = 'posting' AND a.removed_at IS NULL;
    FOR item IN SELECT jsonb_array_elements(checked -> 'costs') LOOP
        INSERT INTO public.allocations(tenant_id, id, debtor_book_id, creditor_book_id, source_book_id, source_entry_id,
                obligation_book_id, obligation_entry_id, amount_minor, source)
            VALUES (t, gen_random_uuid(), debtor, creditor, debtor, debtor_entry, (item ->> 'book')::uuid, (item ->> 'entry')::uuid,
                (item ->> 'amount_minor')::bigint, 'settlement');
    END LOOP;
END $$;

DROP TABLE public.settlement_repays;

-- Expense reports. A business pays back a person for costs they paid: a
-- report under the link between the business's Book and the person's,
-- with its claims, frozen revisions, the approval bound to one revision,
-- its events, money paid over the claim to be resolved, and settlement
-- statements. Only the owner writes them; agents propose.
CREATE FUNCTION public.expense_writer(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = '' AND public.ledger_context(t, b) AND access.owner_allowed(t)
$$;
REVOKE ALL ON FUNCTION public.expense_writer(uuid, uuid) FROM PUBLIC;

-- The business's record of a person it pays back, set by its owner or
-- approver. Each change is kept.
CREATE TABLE public.people (
    tenant_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    id uuid NOT NULL,
    revision integer NOT NULL CHECK (revision > 0),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120 AND name = btrim(name) AND name !~ '[[:cntrl:]]'),
    capacity text NOT NULL CHECK (capacity IN ('employee', 'shareholder_employee', 'partner', 'sole_owner', 'not_established')),
    -- The day this capacity applies from; NULL is from the start.
    effective_from date,
    personal_book_id uuid,
    identity_id uuid,
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, business_book_id, id),
    FOREIGN KEY (tenant_id, business_book_id) REFERENCES public.books(tenant_id, id),
    CHECK (personal_book_id IS DISTINCT FROM business_book_id)
);
CREATE TABLE public.person_revisions (
    tenant_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    person_id uuid NOT NULL,
    revision integer NOT NULL CHECK (revision > 0),
    name text NOT NULL,
    capacity text NOT NULL,
    effective_from date,
    personal_book_id uuid,
    identity_id uuid,
    actor_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, person_id, revision),
    FOREIGN KEY (tenant_id, person_id) REFERENCES public.people(tenant_id, id) DEFERRABLE INITIALLY DEFERRED
);

-- A business's expense policy, as its owner adopted it: the words
-- adopted, kept verbatim, and the rules Vult applies from them. Each
-- adoption is a new version in force from its day; none is ever changed.
-- Before one is adopted, the owner approves their own reports, flagged,
-- under the profile's checks.
CREATE TABLE public.report_policies (
    tenant_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    version integer NOT NULL CHECK (version > 0),
    effective_from date NOT NULL,
    adopted_by uuid NOT NULL,
    adopted_identity_id uuid,
    adopted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    adopted_text text NOT NULL CHECK (length(btrim(adopted_text)) BETWEEN 1 AND 20000),
    -- Who it covers: everyone, or the people named.
    covers_all boolean NOT NULL DEFAULT true,
    covered_person_ids uuid[] NOT NULL DEFAULT '{}',
    methods text[] NOT NULL DEFAULT '{actual_expense,cash}' CHECK (cardinality(methods) > 0 AND methods <@ ARRAY['actual_expense', 'cash']),
    approval_kind text NOT NULL CHECK (approval_kind IN ('independent', 'owner_self')),
    approver_person_ids uuid[] NOT NULL DEFAULT '{}',
    fallback_person_id uuid,
    receipt_threshold_minor bigint NOT NULL DEFAULT 7500 CHECK (receipt_threshold_minor >= 0),
    lodging_always boolean NOT NULL DEFAULT true,
    exception_acceptor_person_ids uuid[] NOT NULL DEFAULT '{}',
    substantiation_days integer NOT NULL DEFAULT 60 CHECK (substantiation_days BETWEEN 1 AND 3650),
    excess_days integer NOT NULL DEFAULT 120 CHECK (excess_days BETWEEN 1 AND 3650),
    reviewer_person_id uuid,
    reimbursement_currency text NOT NULL CHECK (reimbursement_currency ~ '^[A-Z]{3}$'),
    conversion_method text NOT NULL DEFAULT 'same_currency' CHECK (conversion_method IN ('same_currency', 'card_statement_rate', 'published_rate_on_date')),
    rule_profile text NOT NULL DEFAULT 'us_actual_expense_v1' CHECK (rule_profile IN ('us_actual_expense_v1', 'not_assessed')),
    retention_years integer NOT NULL DEFAULT 7 CHECK (retention_years BETWEEN 1 AND 30),
    attestation_text text NOT NULL CHECK (length(btrim(attestation_text)) BETWEEN 1 AND 2000),
    attestation_version integer NOT NULL DEFAULT 1 CHECK (attestation_version > 0),
    PRIMARY KEY (tenant_id, business_book_id, version),
    FOREIGN KEY (tenant_id, business_book_id) REFERENCES public.books(tenant_id, id),
    FOREIGN KEY (tenant_id, fallback_person_id) REFERENCES public.people(tenant_id, id),
    FOREIGN KEY (tenant_id, reviewer_person_id) REFERENCES public.people(tenant_id, id),
    CHECK (approval_kind = 'owner_self' OR cardinality(approver_person_ids) > 0),
    CHECK (covers_all OR cardinality(covered_person_ids) > 0)
);

-- The version in force on a day: the latest adopted that applies by then.
CREATE FUNCTION public.policy_version_on(t uuid, b uuid, d date) RETURNS integer
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT p.version FROM public.report_policies p WHERE p.tenant_id = t AND p.business_book_id = b AND p.effective_from <= d
ORDER BY p.effective_from DESC, p.version DESC LIMIT 1
$$;
REVOKE ALL ON FUNCTION public.policy_version_on(uuid, uuid, date) FROM PUBLIC;

-- A report: a permanent internal id, and ER- with twelve random Crockford
-- base32 characters, immutable and meaningless, generated or given for one
-- already sent.
CREATE TABLE public.expense_reports (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    link_id uuid NOT NULL,
    person_id uuid NOT NULL,
    reference text NOT NULL CHECK (reference ~ '^ER-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$'),
    reference_source text NOT NULL CHECK (reference_source IN ('generated', 'imported')),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    workflow text NOT NULL DEFAULT 'draft' CHECK (workflow IN ('draft', 'submitted', 'returned', 'approved', 'withdrawn', 'superseded')),
    revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
    amends_report_id uuid,
    amend_reason text CHECK (amend_reason IS NULL OR length(btrim(amend_reason)) BETWEEN 1 AND 1000),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by uuid NOT NULL,
    prepared_by_agent boolean NOT NULL DEFAULT false,
    -- A report restored from a backup with money still to pay is matched
    -- or settled only once its owner has reviewed its cash and settlement
    -- history and said so: its reference alone proves nothing.
    reconciled_after_restore boolean NOT NULL DEFAULT true,
    PRIMARY KEY (tenant_id, id),
    -- A reference is the issuing business's; new ones are also checked
    -- against every report this Vult knows.
    UNIQUE (tenant_id, business_book_id, reference),
    FOREIGN KEY (tenant_id, business_book_id) REFERENCES public.books(tenant_id, id),
    -- The link names where the report was made; a business's own copy
    -- restores without the person's Book, so it isn't required to exist.
    FOREIGN KEY (tenant_id, person_id) REFERENCES public.people(tenant_id, id)
);

-- A claim keeps its identity across revisions: what the person asks the
-- business for, for one cost they paid (already recorded as owed, a cash
-- cost recorded owed only when accepted, or an opening balance with its
-- provenance kept). Accounting coding is only proposed here.
CREATE TABLE public.claims (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    person_id uuid NOT NULL,
    kind text NOT NULL CHECK (kind IN ('cost', 'cash', 'opening')),
    obligation_book_id uuid,
    obligation_entry_id uuid,
    incurred_on date NOT NULL,
    paid_on date,
    evidence_received_on date,
    vendor text NOT NULL CHECK (length(btrim(vendor)) BETWEEN 1 AND 200 AND vendor !~ '[[:cntrl:]]'),
    original_amount_minor bigint NOT NULL CHECK (original_amount_minor > 0),
    original_currency text NOT NULL CHECK (original_currency ~ '^[A-Z]{3}$'),
    business_portion_minor bigint NOT NULL CHECK (business_portion_minor > 0),
    requested_minor bigint NOT NULL CHECK (requested_minor > 0),
    prior_reimbursed_minor bigint NOT NULL DEFAULT 0 CHECK (prior_reimbursed_minor >= 0),
    rate numeric NOT NULL DEFAULT 1 CHECK (rate > 0),
    rate_source text NOT NULL DEFAULT 'same currency',
    rate_date date,
    rounding text NOT NULL DEFAULT 'half_even',
    purpose text NOT NULL DEFAULT '' CHECK (length(purpose) <= 2000 AND purpose !~ '[[:cntrl:]]'),
    lodging boolean NOT NULL DEFAULT false,
    proposed_account_id uuid,
    missing_explanation text CHECK (missing_explanation IS NULL OR length(btrim(missing_explanation)) BETWEEN 1 AND 1000),
    provenance text CHECK (provenance IS NULL OR length(btrim(provenance)) BETWEEN 1 AND 1000),
    recognized_at timestamptz,
    recognized_by uuid,
    -- The expense policy version in force on the day of the cost, and the
    -- rule profile it applies; the profile's assessment is kept at
    -- submission. Anything the profile doesn't support reads "not
    -- assessed".
    expense_policy_version integer,
    rule_profile text NOT NULL DEFAULT 'us_actual_expense_v1' CHECK (rule_profile IN ('us_actual_expense_v1', 'not_assessed')),
    assessment jsonb CHECK (assessment IS NULL OR jsonb_typeof(assessment) = 'object'),
    -- The supplier's invoice facts as given, kept exactly: number,
    -- supplier, named customer, tax ids, issue date, tax breakdown, place
    -- and travel. Never the report's reference.
    -- Text, so it's kept byte for byte; it must read as a JSON object.
    supplier_invoice text CHECK (supplier_invoice IS NULL OR length(supplier_invoice) <= 16000 AND json_typeof(supplier_invoice::json) = 'object'),
    revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by uuid NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, person_id) REFERENCES public.people(tenant_id, id),
    FOREIGN KEY (tenant_id, obligation_book_id, obligation_entry_id) REFERENCES public.entries(tenant_id, book_id, id),
    CHECK (business_portion_minor <= original_amount_minor),
    CHECK (requested_minor <= business_portion_minor),
    CHECK ((obligation_book_id IS NULL) = (obligation_entry_id IS NULL)),
    CHECK (kind = 'cash' OR obligation_entry_id IS NOT NULL),
    CHECK (kind <> 'opening' OR provenance IS NOT NULL),
    CHECK ((recognized_at IS NULL) = (recognized_by IS NULL))
);
-- A cost recorded as owed is claimed by one live claim at a time.
CREATE UNIQUE INDEX claims_one_per_cost ON public.claims(tenant_id, obligation_book_id, obligation_entry_id) WHERE obligation_entry_id IS NOT NULL;
ALTER TABLE public.allocations ADD FOREIGN KEY (tenant_id, claim_id) REFERENCES public.claims(tenant_id, id);

-- A claim keeps the expense policy in force on the day of its cost.
CREATE FUNCTION public.claim_policy_version() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    NEW.expense_policy_version := public.policy_version_on(NEW.tenant_id, NEW.business_book_id, NEW.incurred_on);
    NEW.rule_profile := coalesce((SELECT p.rule_profile FROM public.report_policies p WHERE p.tenant_id = NEW.tenant_id
        AND p.business_book_id = NEW.business_book_id AND p.version = NEW.expense_policy_version), 'us_actual_expense_v1');
    RETURN NEW;
END $$;
CREATE TRIGGER claim_policy_version BEFORE INSERT OR UPDATE OF incurred_on ON public.claims FOR EACH ROW EXECUTE FUNCTION public.claim_policy_version();

-- A policy names only the business's own people.
CREATE FUNCTION public.policy_people() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM unnest(NEW.covered_person_ids || NEW.approver_person_ids || NEW.exception_acceptor_person_ids) x(id)
            WHERE NOT EXISTS (SELECT 1 FROM public.people p WHERE p.tenant_id = NEW.tenant_id AND p.business_book_id = NEW.business_book_id AND p.id = x.id)) THEN
        RAISE EXCEPTION 'A policy names only people this business pays back.' USING ERRCODE = 'VE422';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER policy_people BEFORE INSERT ON public.report_policies FOR EACH ROW EXECUTE FUNCTION public.policy_people();

CREATE TABLE public.claim_evidence (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    claim_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    document_id uuid NOT NULL,
    sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    -- What the document is: only these are copied into the business's Book.
    kind text NOT NULL DEFAULT 'receipt' CHECK (kind IN ('receipt', 'invoice', 'payment_proof_extract', 'calculation')),
    -- A redacted or focused copy names what it came from and how, so it
    -- never passes for the original.
    redacted_from_document_id uuid,
    redaction_provenance text CHECK (redaction_provenance IS NULL OR length(btrim(redaction_provenance)) BETWEEN 1 AND 1000),
    retention_until date NOT NULL,
    supplement boolean NOT NULL DEFAULT false,
    added_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    added_by uuid NOT NULL,
    removed_at timestamptz,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, claim_id, document_id),
    CHECK ((redacted_from_document_id IS NULL) = (redaction_provenance IS NULL)),
    FOREIGN KEY (tenant_id, claim_id) REFERENCES public.claims(tenant_id, id),
    FOREIGN KEY (tenant_id, business_book_id, document_id) REFERENCES public.documents(tenant_id, book_id, id)
);

-- A report's revisions. The draft revision's lines change; a submitted
-- revision is frozen: its snapshot, hash and attestation never change.
CREATE TABLE public.report_revisions (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL UNIQUE,
    report_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    revision integer NOT NULL CHECK (revision > 0),
    snapshot jsonb,
    snapshot_sha256 text CHECK (snapshot_sha256 IS NULL OR snapshot_sha256 ~ '^[a-f0-9]{64}$'),
    submitted_at timestamptz,
    submitted_by uuid,
    attestation text,
    pdf_document_id uuid,
    pdf_sha256 text CHECK (pdf_sha256 IS NULL OR pdf_sha256 ~ '^[a-f0-9]{64}$'),
    reason text CHECK (reason IS NULL OR length(btrim(reason)) BETWEEN 1 AND 1000),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, report_id, revision),
    FOREIGN KEY (tenant_id, report_id) REFERENCES public.expense_reports(tenant_id, id),
    FOREIGN KEY (tenant_id, business_book_id, pdf_document_id) REFERENCES public.documents(tenant_id, book_id, id),
    CHECK ((submitted_at IS NULL) = (snapshot IS NULL) AND (snapshot IS NULL) = (snapshot_sha256 IS NULL)),
    CHECK ((pdf_document_id IS NULL) = (pdf_sha256 IS NULL))
);

CREATE TABLE public.report_lines (
    tenant_id uuid NOT NULL,
    report_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    revision integer NOT NULL,
    claim_id uuid NOT NULL,
    position integer NOT NULL CHECK (position BETWEEN 1 AND 500),
    portion_minor bigint NOT NULL CHECK (portion_minor > 0),
    approved_minor bigint CHECK (approved_minor IS NULL OR approved_minor >= 0),
    PRIMARY KEY (tenant_id, report_id, revision, claim_id),
    UNIQUE (tenant_id, report_id, revision, position),
    FOREIGN KEY (tenant_id, report_id, revision) REFERENCES public.report_revisions(tenant_id, report_id, revision),
    FOREIGN KEY (tenant_id, claim_id) REFERENCES public.claims(tenant_id, id)
);

CREATE TABLE public.report_approvals (
    tenant_id uuid NOT NULL,
    report_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    revision integer NOT NULL,
    snapshot_sha256 text NOT NULL CHECK (snapshot_sha256 ~ '^[a-f0-9]{64}$'),
    approver_identity_id uuid NOT NULL,
    approver_person_id uuid,
    authority text NOT NULL CHECK (authority IN ('independent', 'self')),
    -- The policy version in force when it was given; none before one is
    -- adopted.
    approval_policy_version integer,
    approved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    proof_hash text NOT NULL CHECK (proof_hash ~ '^[a-f0-9]{64}$'),
    PRIMARY KEY (tenant_id, report_id, revision),
    FOREIGN KEY (tenant_id, report_id, revision) REFERENCES public.report_revisions(tenant_id, report_id, revision),
    FOREIGN KEY (tenant_id, business_book_id, approval_policy_version) REFERENCES public.report_policies(tenant_id, business_book_id, version)
);

CREATE TABLE public.report_events (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    report_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    revision integer NOT NULL,
    kind text NOT NULL CHECK (kind IN ('created', 'submitted', 'accepted', 'approved', 'returned', 'withdrawn', 'superseded', 'amended',
        'paid', 'paid_before_approval', 'payment_removed', 'recoverable_opened', 'recoverable_resolved', 'statement_issued', 'evidence_added',
        'residual_opened', 'residual_classified', 'reconciled')),
    at timestamptz NOT NULL DEFAULT clock_timestamp(),
    actor_id uuid NOT NULL,
    identity_id uuid,
    note text CHECK (note IS NULL OR length(note) <= 2000),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, report_id) REFERENCES public.expense_reports(tenant_id, id)
);
CREATE INDEX report_events_report ON public.report_events(tenant_id, report_id, at);

-- Money owed back to the business: an overpayment its owner classified as
-- an error, or a correction that reduced a claim already paid. How it's
-- recovered and how it's taxed are tracked apart; Vult never computes
-- payroll nor decides a treatment from dates, and never turns it into a
-- draw, a distribution or a loan.
CREATE TABLE public.recoverables (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    report_id uuid NOT NULL,
    person_id uuid NOT NULL,
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    origin text NOT NULL CHECK (origin IN ('overpayment', 'reduction')),
    origin_entry_id uuid,
    cost_date date NOT NULL,
    opened_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    recovery_status text NOT NULL DEFAULT 'outstanding' CHECK (recovery_status IN ('outstanding', 'returned', 'applied', 'waived', 'resolved')),
    tax_status text NOT NULL DEFAULT 'not_determined' CHECK (tax_status IN ('not_determined', 'review_required', 'treatment_determined', 'reported', 'corrected')),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, report_id) REFERENCES public.expense_reports(tenant_id, id),
    FOREIGN KEY (tenant_id, person_id) REFERENCES public.people(tenant_id, id)
);
CREATE TABLE public.recoverable_events (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    recoverable_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    -- Which status changed, and to what.
    track text NOT NULL CHECK (track IN ('recovery', 'tax')),
    status text NOT NULL,
    entry_id uuid,
    target_report_id uuid,
    note text CHECK (note IS NULL OR length(btrim(note)) BETWEEN 1 AND 2000),
    recorded_by uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, recoverable_id) REFERENCES public.recoverables(tenant_id, id),
    CHECK (track = 'recovery' AND status IN ('outstanding', 'returned', 'applied', 'waived', 'resolved')
        OR track = 'tax' AND status IN ('not_determined', 'review_required', 'treatment_determined', 'reported', 'corrected')),
    CHECK (NOT (track = 'recovery' AND status = 'returned') OR entry_id IS NOT NULL),
    CHECK (NOT (track = 'recovery' AND status = 'applied') OR target_report_id IS NOT NULL),
    CHECK (NOT (track = 'recovery' AND status IN ('waived', 'resolved') OR track = 'tax' AND status IN ('treatment_determined', 'reported', 'corrected')) OR note IS NOT NULL)
);

-- A review of what a recoverable means for the person: who is
-- responsible, on what amount, when, and what came of it. Each is kept.
CREATE TABLE public.recoverable_reviews (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    recoverable_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    responsible_person_id uuid,
    basis_amount_minor bigint CHECK (basis_amount_minor IS NULL OR basis_amount_minor >= 0),
    timing text CHECK (timing IS NULL OR length(btrim(timing)) BETWEEN 1 AND 500),
    review_date date NOT NULL,
    outcome text CHECK (outcome IS NULL OR length(btrim(outcome)) BETWEEN 1 AND 2000),
    outcome_reference text CHECK (outcome_reference IS NULL OR length(btrim(outcome_reference)) BETWEEN 1 AND 200),
    recorded_by uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, recoverable_id) REFERENCES public.recoverables(tenant_id, id),
    FOREIGN KEY (tenant_id, responsible_person_id) REFERENCES public.people(tenant_id, id)
);

-- What a payment to a person left over after it was matched: awaiting the
-- owner's classification. A report is never enlarged to fit it; only an
-- error opens a recoverable.
CREATE TABLE public.payment_residuals (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    report_id uuid NOT NULL,
    person_id uuid NOT NULL,
    source_entry_id uuid NOT NULL,
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    classification text CHECK (classification IS NULL OR classification IN ('another_report', 'fee', 'exchange_difference', 'advance', 'error', 'unknown')),
    note text CHECK (note IS NULL OR length(btrim(note)) BETWEEN 1 AND 2000),
    target_report_id uuid,
    recoverable_id uuid,
    classified_by uuid,
    classified_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, business_book_id, source_entry_id, report_id),
    FOREIGN KEY (tenant_id, report_id) REFERENCES public.expense_reports(tenant_id, id),
    FOREIGN KEY (tenant_id, person_id) REFERENCES public.people(tenant_id, id),
    FOREIGN KEY (tenant_id, recoverable_id) REFERENCES public.recoverables(tenant_id, id),
    CHECK ((classification IS NULL) = (classified_at IS NULL) AND (classified_at IS NULL) = (classified_by IS NULL)),
    CHECK ((classification = 'error') = (recoverable_id IS NOT NULL)),
    CHECK (classification IS DISTINCT FROM 'another_report' OR target_report_id IS NOT NULL)
);

-- A residual is classified once; until then its amount follows the
-- payment's matching.
CREATE FUNCTION public.residual_classified_once() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF current_setting('vult.erasing', true) = 'on' THEN
        IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' OR OLD.classification IS NOT NULL THEN
        RAISE EXCEPTION 'This leftover is already classified.' USING ERRCODE = 'VE409';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER residual_classified_once BEFORE UPDATE OR DELETE ON public.payment_residuals FOR EACH ROW EXECUTE FUNCTION public.residual_classified_once();

CREATE TABLE public.settlement_statements (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    report_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    issued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    allocations_sha256 text NOT NULL CHECK (allocations_sha256 ~ '^[a-f0-9]{64}$'),
    document_id uuid NOT NULL,
    pdf_sha256 text NOT NULL CHECK (pdf_sha256 ~ '^[a-f0-9]{64}$'),
    issued_by uuid NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, report_id) REFERENCES public.expense_reports(tenant_id, id),
    FOREIGN KEY (tenant_id, business_book_id, document_id) REFERENCES public.documents(tenant_id, book_id, id)
);

-- Notes on a report, by who may read them. A personal note is kept in the
-- person's own Book and never reaches the business's reads, documents or
-- archive; shared and company notes are the business's.
CREATE TABLE public.report_notes (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    book_id uuid NOT NULL,
    report_id uuid NOT NULL,
    audience text NOT NULL CHECK (audience IN ('personal', 'shared', 'company')),
    text text NOT NULL CHECK (length(btrim(text)) BETWEEN 1 AND 4000),
    author_id uuid NOT NULL,
    at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);
CREATE INDEX report_notes_report ON public.report_notes(tenant_id, report_id, at);
ALTER TABLE public.report_notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.report_notes FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.report_notes FROM PUBLIC;
CREATE POLICY note_read ON public.report_notes FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY note_insert ON public.report_notes FOR INSERT WITH CHECK (public.expense_writer(tenant_id, book_id));
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.report_notes FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('book_id');

-- A deleted draft's reference stays used, so it's never issued again.
CREATE TABLE public.retired_report_references (
    tenant_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    reference text NOT NULL CHECK (reference ~ '^ER-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$'),
    report_id uuid NOT NULL,
    retired_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, business_book_id, reference),
    FOREIGN KEY (tenant_id, business_book_id) REFERENCES public.books(tenant_id, id)
);

-- A claim is on at most one live report at once; claims are locked while
-- this is checked, so two submissions racing for one claim can't both win.
CREATE FUNCTION public.claim_once() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE other text;
BEGIN
    PERFORM 1 FROM public.claims c WHERE c.tenant_id = NEW.tenant_id AND c.id = NEW.claim_id FOR UPDATE;
    SELECT r.reference INTO other FROM public.report_lines l
        JOIN public.expense_reports r ON r.tenant_id = l.tenant_id AND r.id = l.report_id AND r.revision = l.revision
        WHERE l.tenant_id = NEW.tenant_id AND l.claim_id = NEW.claim_id AND l.report_id <> NEW.report_id
            AND r.workflow NOT IN ('withdrawn', 'superseded')
            AND NOT EXISTS (SELECT 1 FROM public.expense_reports n WHERE n.tenant_id = r.tenant_id AND n.amends_report_id = r.id AND n.id = NEW.report_id)
        LIMIT 1;
    IF other IS NOT NULL THEN
        RAISE EXCEPTION 'This cost is already on %.', other USING ERRCODE = 'VE409';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER claim_once BEFORE INSERT OR UPDATE ON public.report_lines FOR EACH ROW EXECUTE FUNCTION public.claim_once();

-- A submitted revision's lines and snapshot never change; its document is
-- set once.
CREATE FUNCTION public.revision_frozen() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE frozen boolean;
BEGIN
    IF current_setting('vult.erasing', true) = 'on' THEN
        IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
        RETURN NEW;
    END IF;
    IF TG_TABLE_NAME = 'report_revisions' THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION 'A report revision is kept.' USING ERRCODE = '42501';
        END IF;
        IF OLD.submitted_at IS NOT NULL AND ((to_jsonb(NEW) - 'pdf_document_id' - 'pdf_sha256') <> (to_jsonb(OLD) - 'pdf_document_id' - 'pdf_sha256')
                OR OLD.pdf_document_id IS NOT NULL AND (NEW.pdf_document_id IS DISTINCT FROM OLD.pdf_document_id OR NEW.pdf_sha256 IS DISTINCT FROM OLD.pdf_sha256)) THEN
            RAISE EXCEPTION 'A submitted revision never changes; correct the report instead.' USING ERRCODE = 'VE409';
        END IF;
        RETURN NEW;
    END IF;
    SELECT v.submitted_at IS NOT NULL INTO frozen FROM public.report_revisions v
        WHERE v.tenant_id = coalesce(NEW.tenant_id, OLD.tenant_id) AND v.report_id = coalesce(NEW.report_id, OLD.report_id)
            AND v.revision = coalesce(NEW.revision, OLD.revision);
    IF frozen AND NOT (TG_OP = 'UPDATE' AND (to_jsonb(NEW) - 'approved_minor') = (to_jsonb(OLD) - 'approved_minor') AND OLD.approved_minor IS NULL) THEN
        RAISE EXCEPTION 'A submitted revision never changes; correct the report instead.' USING ERRCODE = 'VE409';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER revision_frozen BEFORE UPDATE OR DELETE ON public.report_revisions FOR EACH ROW EXECUTE FUNCTION public.revision_frozen();
CREATE TRIGGER revision_frozen BEFORE INSERT OR UPDATE OR DELETE ON public.report_lines FOR EACH ROW EXECUTE FUNCTION public.revision_frozen();

-- Approvals, events and resolutions are only ever added.
CREATE FUNCTION public.expense_append_only() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF current_setting('vult.erasing', true) = 'on' THEN RETURN OLD; END IF;
    RAISE EXCEPTION 'This record is kept as it was written.' USING ERRCODE = '42501';
END $$;
CREATE TRIGGER expense_append_only BEFORE UPDATE OR DELETE ON public.report_approvals FOR EACH ROW EXECUTE FUNCTION public.expense_append_only();
CREATE TRIGGER expense_append_only BEFORE UPDATE OR DELETE ON public.report_events FOR EACH ROW EXECUTE FUNCTION public.expense_append_only();
CREATE TRIGGER expense_append_only BEFORE UPDATE OR DELETE ON public.recoverable_events FOR EACH ROW EXECUTE FUNCTION public.expense_append_only();
CREATE TRIGGER expense_append_only BEFORE UPDATE OR DELETE ON public.recoverable_reviews FOR EACH ROW EXECUTE FUNCTION public.expense_append_only();
CREATE TRIGGER expense_append_only BEFORE UPDATE OR DELETE ON public.person_revisions FOR EACH ROW EXECUTE FUNCTION public.expense_append_only();
CREATE TRIGGER expense_append_only BEFORE UPDATE OR DELETE ON public.report_policies FOR EACH ROW EXECUTE FUNCTION public.expense_append_only();
CREATE TRIGGER expense_append_only BEFORE UPDATE OR DELETE ON public.settlement_statements FOR EACH ROW EXECUTE FUNCTION public.expense_append_only();
CREATE TRIGGER expense_append_only BEFORE UPDATE OR DELETE ON public.retired_report_references FOR EACH ROW EXECUTE FUNCTION public.expense_append_only();

-- An approval is for the exact revision submitted.
CREATE FUNCTION public.approval_matches() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.report_revisions v WHERE v.tenant_id = NEW.tenant_id AND v.report_id = NEW.report_id
            AND v.revision = NEW.revision AND v.snapshot_sha256 = NEW.snapshot_sha256)
        OR NOT EXISTS (SELECT 1 FROM public.expense_reports r WHERE r.tenant_id = NEW.tenant_id AND r.id = NEW.report_id AND r.revision = NEW.revision) THEN
        RAISE EXCEPTION 'This report changed since you opened it. Review it again.' USING ERRCODE = 'VE412';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER approval_matches BEFORE INSERT ON public.report_approvals FOR EACH ROW EXECUTE FUNCTION public.approval_matches();

-- A person's record keeps every change.
CREATE FUNCTION public.person_revision() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND NEW.revision <> OLD.revision + 1 OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
        RAISE EXCEPTION 'A person''s record changed since you opened it.' USING ERRCODE = 'VE409';
    END IF;
    INSERT INTO public.person_revisions(tenant_id, business_book_id, person_id, revision, name, capacity, effective_from, personal_book_id, identity_id, actor_id)
        VALUES (NEW.tenant_id, NEW.business_book_id, NEW.id, NEW.revision, NEW.name, NEW.capacity, NEW.effective_from, NEW.personal_book_id, NEW.identity_id, NEW.actor_id);
    RETURN NEW;
END $$;
CREATE TRIGGER person_revision AFTER INSERT OR UPDATE ON public.people FOR EACH ROW EXECUTE FUNCTION public.person_revision();

-- A person's capacity on a day: the latest recorded change in effect by
-- then. Before any applies, the relationship isn't established.
CREATE FUNCTION public.person_capacity_on(t uuid, person uuid, d date) RETURNS text
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce((SELECT r.capacity FROM public.person_revisions r WHERE r.tenant_id = t AND r.person_id = person
    AND (r.effective_from IS NULL OR r.effective_from <= d) ORDER BY r.effective_from DESC NULLS LAST, r.revision DESC LIMIT 1), 'not_established')
$$;
REVOKE ALL ON FUNCTION public.person_capacity_on(uuid, uuid, date) FROM PUBLIC;

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['people', 'person_revisions', 'report_policies', 'expense_reports', 'claims', 'claim_evidence', 'report_revisions',
            'report_lines', 'report_approvals', 'report_events', 'recoverables', 'recoverable_events', 'settlement_statements', 'retired_report_references',
            'recoverable_reviews', 'payment_residuals'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY expense_read ON public.%I FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, business_book_id, false))', tab);
        EXECUTE format('CREATE POLICY expense_insert ON public.%I FOR INSERT WITH CHECK (public.expense_writer(tenant_id, business_book_id))', tab);
        EXECUTE format('CREATE POLICY expense_update ON public.%I FOR UPDATE USING (public.expense_writer(tenant_id, business_book_id)) WITH CHECK (public.expense_writer(tenant_id, business_book_id))', tab);
        EXECUTE format('CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.%I FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard(%L)', tab, 'business_book_id');
    END LOOP;
END $$;
CREATE TRIGGER book_lock_guard_person BEFORE INSERT OR UPDATE OR DELETE ON public.people FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('personal_book_id');
REVOKE ALL ON FUNCTION public.claim_once(), public.revision_frozen(), public.expense_append_only(), public.approval_matches(), public.person_revision(), public.claim_policy_version(), public.policy_people(), public.residual_classified_once() FROM PUBLIC;

-- Passkey proofs for reports: bound to one report, revision and snapshot,
-- and spent once.
ALTER TABLE access.book_deletion_proofs DROP CONSTRAINT book_deletion_proofs_purpose_check;
ALTER TABLE access.book_deletion_proofs ADD CONSTRAINT book_deletion_proofs_purpose_check
    CHECK (purpose IN ('delete', 'confirm', 'submit', 'approve', 'submit_approve'));
ALTER TABLE access.book_deletion_proofs ADD COLUMN bound_to text CHECK (bound_to IS NULL OR length(bound_to) <= 200);

CREATE FUNCTION access.begin_report_proof(sub text, realm text, generation bigint, token text, session text,
    t uuid, b uuid, purpose text, bound text, data jsonb) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid;
BEGIN
    SELECT i.id INTO who FROM access.identities i
        JOIN access.owners o ON o.identity_id = i.id AND o.tenant_id = t
        JOIN access.sessions s ON s.identity_id = i.id AND s.epoch = i.epoch AND s.token_hash = session AND s.origin = realm
            AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp()
        WHERE i.issuer = 'urn:vult:builtin' AND i.subject = sub AND i.epoch = generation FOR UPDATE OF i;
    IF who IS NULL OR purpose NOT IN ('submit', 'approve', 'submit_approve') OR bound IS NULL
        OR NOT EXISTS (SELECT 1 FROM public.books k WHERE k.tenant_id = t AND k.id = b)
        OR NOT EXISTS (SELECT 1 FROM access.passkey_profiles p WHERE p.identity_id = who AND p.origin = realm AND p.state = 'ready') THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    DELETE FROM access.book_deletion_proofs p WHERE p.identity_id = who AND p.created_at < clock_timestamp() - interval '1 day';
    IF (SELECT count(*) FROM access.book_deletion_proofs p WHERE p.identity_id = who AND p.created_at > clock_timestamp() - interval '1 hour') >= 30 THEN
        RAISE EXCEPTION 'Too many passkey requests in the last hour. Try again later.' USING ERRCODE = 'VB429';
    END IF;
    INSERT INTO access.book_deletion_proofs(token_hash, identity_id, epoch, session_hash, tenant_id, book_ref, purpose, bound_to, ceremony)
    VALUES (token, who, generation, session, t, b, purpose, bound, data);
END $$;

-- Spends a verified report proof for exactly what it was made for, and
-- returns who gave it.
CREATE FUNCTION public.report_spend_proof(t uuid, b uuid, proof text, purpose text, bound text) RETURNS uuid
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid;
BEGIN
    UPDATE access.book_deletion_proofs p SET used_at = clock_timestamp()
        WHERE p.token_hash = proof AND p.tenant_id = t AND p.book_ref = b AND p.purpose = report_spend_proof.purpose AND p.bound_to = bound
        AND p.session_hash = coalesce(current_setting('vult.owner_session', true), '')
        AND p.verified_at IS NOT NULL AND p.verified_at > clock_timestamp() - interval '2 minutes' AND p.used_at IS NULL
        RETURNING p.identity_id INTO who;
    IF who IS NULL THEN
        RAISE EXCEPTION 'Confirm with your passkey to continue.' USING ERRCODE = 'VB401';
    END IF;
    RETURN who;
END $$;
REVOKE ALL ON FUNCTION access.begin_report_proof(text, text, bigint, text, text, uuid, uuid, text, text, jsonb), public.report_spend_proof(uuid, uuid, text, text, text) FROM PUBLIC;

-- A draft's lines and claims not yet on a submitted revision can be removed.
CREATE POLICY expense_delete ON public.report_lines FOR DELETE USING (public.expense_writer(tenant_id, business_book_id));
CREATE POLICY expense_delete ON public.claims FOR DELETE USING (public.expense_writer(tenant_id, business_book_id));

-- Applies a payment from a business to a person to that report's claims:
-- whatever the payment repaid before (as a plain payment or settlement) is
-- removed, and each claim's cost gets the amount given. The amounts must
-- fit what is open of each cost and what the payment reduced.
CREATE FUNCTION public.report_settle(t uuid, b uuid, source_entry uuid, actor uuid, items jsonb, unapproved boolean) RETURNS integer
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d record; item jsonb; claim public.claims%ROWTYPE; open_amount bigint; reduced bigint; total bigint := 0; part bigint; n integer := 0;
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner applies a payment' USING ERRCODE = '42501';
    END IF;
    IF jsonb_typeof(items) <> 'array' OR jsonb_array_length(items) NOT BETWEEN 1 AND 500 THEN
        RAISE EXCEPTION 'Choose what this payment pays.' USING ERRCODE = 'VE422';
    END IF;
    PERFORM 1 FROM public.entries e WHERE e.tenant_id = t AND e.book_id = b AND e.id = source_entry AND e.reversal_of IS NULL
        AND NOT public.entry_reversed(t, b, e.id) FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'That payment isn''t recorded in this Book, or it was undone.' USING ERRCODE = 'VE422';
    END IF;
    UPDATE public.allocations a SET removed_at = clock_timestamp(), removed_by = actor, removal_reason = 'Applied to an expense report.'
        WHERE a.tenant_id = t AND a.source_book_id = b AND a.source_entry_id = source_entry AND a.removed_at IS NULL AND a.source <> 'report_payment';
    FOR item IN SELECT jsonb_array_elements(items) LOOP
        SELECT * INTO claim FROM public.claims c WHERE c.tenant_id = t AND c.business_book_id = b AND c.id = (item ->> 'claim_id')::uuid FOR UPDATE;
        part := (item ->> 'amount_minor')::bigint;
        IF claim.id IS NULL OR claim.obligation_entry_id IS NULL OR part IS NULL OR part <= 0 THEN
            RAISE EXCEPTION 'A claim this pays isn''t one recorded as owed.' USING ERRCODE = 'VE422';
        END IF;
        SELECT d2.* INTO d FROM public.owed_directions(t) d2 WHERE d2.debtor = b
            AND (EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = t AND p.beneficiary_book_id = claim.obligation_book_id
                AND p.beneficiary_entry_id = claim.obligation_entry_id AND (p.payer_book_id = d2.creditor OR p.beneficiary_book_id = d2.creditor))
            -- A cash claim the business accepted is owed along its link too.
            OR EXISTS (SELECT 1 FROM public.claim_obligations o WHERE o.tenant_id = t AND o.business_book_id = claim.obligation_book_id
                AND o.business_entry_id = claim.obligation_entry_id AND o.person_book_id = d2.creditor))
            LIMIT 1;
        -- A cash claim is owed what its obligation reads now, after any
        -- correction; a cost, what its entry recorded.
        open_amount := coalesce((SELECT o.amount_minor FROM public.claim_obligations o WHERE o.tenant_id = t AND o.claim_id = claim.id),
                public.cost_amount(t, claim.obligation_book_id, claim.obligation_entry_id));
        open_amount := open_amount - least(open_amount, public.cost_repaid_amount(t, claim.obligation_book_id, claim.obligation_entry_id, NULL));
        IF d.creditor IS NULL OR part > open_amount THEN
            RAISE EXCEPTION 'This pays more of a cost than is left to pay (%).', public.book_amount(t, b, greatest(open_amount, 0)) USING ERRCODE = 'VE422';
        END IF;
        INSERT INTO public.allocations(tenant_id, id, debtor_book_id, creditor_book_id, source_book_id, source_entry_id,
                obligation_book_id, obligation_entry_id, claim_id, amount_minor, source, unapproved, actor_id)
            VALUES (t, gen_random_uuid(), b, d.creditor, b, source_entry, claim.obligation_book_id, claim.obligation_entry_id,
                claim.id, part, 'report_payment', unapproved, actor);
        total := total + part;
        n := n + 1;
    END LOOP;
    SELECT coalesce(sum(CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO reduced
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = b AND x.entry_id = source_entry
            AND x.account_id IN (SELECT o.debtor_account FROM public.owed_directions(t) o WHERE o.debtor = b);
    IF (SELECT coalesce(sum(a.amount_minor), 0) FROM public.allocations a WHERE a.tenant_id = t AND a.source_book_id = b
            AND a.source_entry_id = source_entry AND a.removed_at IS NULL) > reduced THEN
        RAISE EXCEPTION 'This applies more than the payment paid (%).', public.book_amount(t, b, greatest(reduced, 0)) USING ERRCODE = 'VE422';
    END IF;
    RETURN n;
END $$;

-- Unmatches: the allocation is removed and kept in its history. The
-- payment stays, and what it no longer repays is unapplied, never spread
-- over other costs again.
CREATE FUNCTION public.remove_allocation(t uuid, allocation uuid, actor uuid, reason text) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner unmatches a payment' USING ERRCODE = '42501';
    END IF;
    UPDATE public.allocations a SET removed_at = clock_timestamp(), removed_by = actor, removal_reason = reason
        WHERE a.tenant_id = t AND a.id = allocation AND a.removed_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'That payment is no longer matched.' USING ERRCODE = 'VE409';
    END IF;
END $$;
REVOKE ALL ON FUNCTION public.report_settle(uuid, uuid, uuid, uuid, jsonb, boolean), public.remove_allocation(uuid, uuid, uuid, text) FROM PUBLIC;

-- A cash cost a person paid becomes owed when the business accepts the
-- claim: a claim pair, recorded only by accept_claim, repaid like any cost.
ALTER TABLE public.entry_pairs DROP CONSTRAINT entry_pairs_kind_check;
ALTER TABLE public.entry_pairs ADD CONSTRAINT entry_pairs_kind_check CHECK (kind IN ('cost', 'settlement', 'opening', 'carried', 'claim'));
ALTER TABLE public.claims ADD COLUMN paid_from_account_id uuid;
ALTER TABLE public.claims ADD CONSTRAINT claims_paid_from CHECK (kind <> 'cash' OR paid_from_account_id IS NOT NULL);

CREATE OR REPLACE FUNCTION public.entry_pair_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE terms public.book_link_terms%ROWTYPE; item public.bank_transactions%ROWTYPE; bank_account uuid; tid uuid;
    paid text; amount bigint; n int; bad int;
BEGIN
    IF NOT (access.owner_allowed(NEW.tenant_id) AND access.owner_write_allowed(NEW.tenant_id)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    IF NEW.kind IN ('settlement', 'opening', 'carried', 'claim') THEN
        -- Settlements, opening balances and carried costs are checked and
        -- recorded by settle_pair, open_link_balance and carry_cost only.
        IF current_setting('vult.settling', true) IS DISTINCT FROM NEW.pair_key THEN
            RAISE EXCEPTION 'a settlement or an opening balance is recorded with its own function' USING ERRCODE = '42501';
        END IF;
        RETURN NEW;
    END IF;
    terms := public.active_link_terms(NEW.tenant_id, NEW.payer_book_id, NEW.beneficiary_book_id);
    IF terms.link_id IS NULL THEN
        RAISE EXCEPTION 'This link is turned off; the pair can only be rejected.' USING ERRCODE = 'VP020';
    END IF;
    SELECT count(*), min(l.account_id::text)::uuid, min(l.transaction_id::text)::uuid INTO n, bank_account, tid
        FROM public.bank_transaction_links l
        WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.payer_book_id AND l.entry_id = NEW.payer_entry_id AND l.revoked_at IS NULL;
    SELECT * INTO item FROM public.bank_transactions t WHERE t.tenant_id = NEW.tenant_id AND t.book_id = NEW.payer_book_id AND t.id = tid;
    IF n <> 1 OR item.id IS NULL THEN
        RAISE EXCEPTION 'A cost pair''s first entry records one bank item in the Book that paid.' USING ERRCODE = 'VP020';
    END IF;
    paid := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = NEW.tenant_id AND a.book_id = NEW.payer_book_id AND a.id = bank_account);
    IF NOT (paid = 'liability' AND item.amount_minor > 0 OR paid = 'asset' AND item.amount_minor < 0) THEN
        RAISE EXCEPTION 'A cost pair records money paid out, such as a charge on a card.' USING ERRCODE = 'VP020';
    END IF;
    amount := abs(item.amount_minor);
    IF (SELECT k.currency FROM public.books k WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.beneficiary_book_id) IS DISTINCT FROM item.currency THEN
        RAISE EXCEPTION 'The bank item is in another currency than the Book it was paid for.' USING ERRCODE = 'VP020';
    END IF;
    -- The payer's entry: the bank account against the term account, whole.
    SELECT count(*) FILTER (WHERE NOT (x.account_id = bank_account OR x.account_id = terms.payer_account_id AND x.side = 'debit' AND x.amount_minor = amount)),
           count(*) INTO bad, n
        FROM public.lines x WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.payer_book_id AND x.entry_id = NEW.payer_entry_id;
    IF bad <> 0 OR n <> 2 THEN
        RAISE EXCEPTION 'The first entry records the bank item to the account this link names in the Book that paid.' USING ERRCODE = 'VP020';
    END IF;
    -- The beneficiary's entry: one credit to its term account, whole, and
    -- debits to expenses or assets that aren't money, for the same amount.
    SELECT count(*) FILTER (WHERE x.side = 'credit' AND NOT (x.account_id = terms.beneficiary_account_id AND x.amount_minor = amount))
         + count(*) FILTER (WHERE x.side = 'debit' AND NOT (a.class = 'expense' OR a.class = 'asset' AND coalesce(a.role, '') NOT IN ('cash', 'card')
               AND NOT EXISTS (SELECT 1 FROM public.bank_accounts y WHERE y.tenant_id = a.tenant_id AND y.book_id = a.book_id AND y.account_id = a.id))),
           count(*) FILTER (WHERE x.side = 'credit')
        INTO bad, n
        FROM public.lines x JOIN public.accounts a ON a.tenant_id = x.tenant_id AND a.book_id = x.book_id AND a.id = x.account_id
        WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.beneficiary_book_id AND x.entry_id = NEW.beneficiary_entry_id;
    IF bad <> 0 OR n <> 1 THEN
        RAISE EXCEPTION 'The second entry credits the account this link names (%) with the whole amount, and debits only expenses, or assets that aren''t cash, a card or a bank account.', terms.beneficiary_account_id USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.entries e WHERE e.tenant_id = NEW.tenant_id
            AND (e.book_id = NEW.payer_book_id AND e.id = NEW.payer_entry_id OR e.book_id = NEW.beneficiary_book_id AND e.id = NEW.beneficiary_entry_id)
            AND (e.accounting_date <> item.occurred_on OR e.reversal_of IS NOT NULL))
        OR (SELECT count(*) FROM public.entries e WHERE e.tenant_id = NEW.tenant_id
            AND (e.book_id = NEW.payer_book_id AND e.id = NEW.payer_entry_id OR e.book_id = NEW.beneficiary_book_id AND e.id = NEW.beneficiary_entry_id)) <> 2 THEN
        RAISE EXCEPTION 'Both entries of a pair are dated the bank item''s date (%).', item.occurred_on USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION public.entry_pair_of(t uuid, b uuid, e uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE (
    SELECT jsonb_build_object('pair_key', x.pair_key, 'kind', x.kind,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'entry_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_entry_id ELSE x.payer_entry_id END,
        'side', CASE WHEN x.payer_book_id = b THEN 'beneficiary' ELSE 'payer' END,
        'repaid_by', CASE WHEN x.kind IN ('cost', 'opening', 'carried', 'claim') THEN public.cost_repaid_by(t, x.beneficiary_book_id, x.beneficiary_entry_id, NULL) END,
        'reconstructed', EXISTS (SELECT 1 FROM public.allocations a WHERE a.tenant_id = t AND a.obligation_book_id = x.beneficiary_book_id
            AND a.obligation_entry_id = x.beneficiary_entry_id AND a.provenance = 'frozen_at_migration' AND public.allocation_live(t, a)))
    FROM public.entry_pairs x WHERE x.tenant_id = t
        AND (x.payer_book_id = b AND x.payer_entry_id = e OR x.beneficiary_book_id = b AND x.beneficiary_entry_id = e)) END
$$;

-- What a business owes a person for a cash claim it accepted. Approval
-- records the business's side only: its entry, the cost against what it
-- owes the person. The person's side is theirs to record in their own Book
-- (matching a charge already there, or a new entry for real cash), so it
-- waits; a person with no Book, or whose relationship isn't established,
-- has none to record. Nothing is counted twice.
CREATE TABLE public.claim_obligations (
    tenant_id uuid NOT NULL,
    claim_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    business_entry_id uuid NOT NULL,
    -- What the business owes for it now: the accepted amount, moved by
    -- each approved correction.
    amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
    person_book_id uuid,
    person_status text NOT NULL CHECK (person_status IN ('pending', 'recorded', 'not_applicable')),
    person_entry_id uuid,
    person_method text CHECK (person_method IS NULL OR person_method IN ('reclassified', 'matched_charge', 'new_entry')),
    matched_bank_item_id uuid,
    person_recorded_at timestamptz,
    person_recorded_by uuid,
    accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    accepted_by uuid NOT NULL,
    PRIMARY KEY (tenant_id, claim_id),
    UNIQUE (tenant_id, business_book_id, business_entry_id),
    FOREIGN KEY (tenant_id, claim_id) REFERENCES public.claims(tenant_id, id),
    FOREIGN KEY (tenant_id, business_book_id, business_entry_id) REFERENCES public.entries(tenant_id, book_id, id),
    CHECK ((person_status = 'recorded') = (person_entry_id IS NOT NULL)),
    CHECK ((person_entry_id IS NULL) = (person_method IS NULL) AND (person_entry_id IS NULL) = (person_recorded_at IS NULL)),
    CHECK (person_status <> 'pending' OR person_book_id IS NOT NULL)
);
-- What each person's Book still has to record, read on every visit.
CREATE INDEX claim_obligations_pending ON public.claim_obligations(tenant_id, person_book_id) WHERE person_status = 'pending';
ALTER TABLE public.claim_obligations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.claim_obligations FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.claim_obligations FROM PUBLIC;
CREATE POLICY obligation_read ON public.claim_obligations FOR SELECT USING (access.owner_allowed(tenant_id)
    OR public.ledger_allowed(tenant_id, business_book_id, false) OR person_book_id IS NOT NULL AND public.ledger_allowed(tenant_id, person_book_id, false));
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.claim_obligations FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('business_book_id', 'person_book_id');

-- An allocation repays a pair's cost or a cash claim's obligation.
CREATE FUNCTION public.obligation_known() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = NEW.tenant_id AND p.beneficiary_book_id = NEW.obligation_book_id AND p.beneficiary_entry_id = NEW.obligation_entry_id)
        AND NOT EXISTS (SELECT 1 FROM public.claim_obligations o WHERE o.tenant_id = NEW.tenant_id AND o.business_book_id = NEW.obligation_book_id AND o.business_entry_id = NEW.obligation_entry_id) THEN
        RAISE EXCEPTION 'An allocation repays a recorded cost.' USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END $$;
CREATE CONSTRAINT TRIGGER obligation_known AFTER INSERT OR UPDATE OF obligation_book_id, obligation_entry_id ON public.allocations
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.obligation_known();
CREATE CONSTRAINT TRIGGER obligation_known AFTER INSERT OR UPDATE OF obligation_book_id, obligation_entry_id ON public.claims
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.obligation_entry_id IS NOT NULL) EXECUTE FUNCTION public.obligation_known();

-- Accepts a cash claim on the business's side: its entry records the cost
-- against what it owes the person, on the day it was paid, for the claim's
-- amount. The person's side waits for them, in their own Book.
CREATE FUNCTION public.accept_claim(t uuid, claim uuid, business_entry uuid, actor uuid) RETURNS text
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE c public.claims%ROWTYPE; p public.people%ROWTYPE; terms public.book_link_terms%ROWTYPE; bad int; status text;
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner accepts a claim' USING ERRCODE = '42501';
    END IF;
    SELECT * INTO c FROM public.claims x WHERE x.tenant_id = t AND x.id = claim FOR UPDATE;
    SELECT * INTO p FROM public.people x WHERE x.tenant_id = t AND x.id = c.person_id;
    IF c.id IS NULL OR c.kind <> 'cash' OR c.recognized_at IS NOT NULL THEN
        RAISE EXCEPTION 'Only a cash claim not yet accepted is accepted this way.' USING ERRCODE = 'VE422';
    END IF;
    SELECT count(*) FILTER (WHERE NOT coalesce(x.side = 'credit' AND x.amount_minor = c.requested_minor
            AND EXISTS (SELECT 1 FROM public.book_link_terms k WHERE k.tenant_id = t AND k.beneficiary_book_id = c.business_book_id
                AND k.beneficiary_account_id = x.account_id AND k.treatment = 'owed')
            OR x.side = 'debit' AND x.amount_minor = c.requested_minor, false)) + abs(count(*) - 2) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = c.business_book_id AND x.entry_id = business_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The business''s entry records the claim against what it owes the person.' USING ERRCODE = 'VE422';
    END IF;
    status := CASE WHEN p.personal_book_id IS NULL OR public.person_capacity_on(t, p.id, c.incurred_on) = 'not_established' THEN 'not_applicable' ELSE 'pending' END;
    INSERT INTO public.claim_obligations(tenant_id, claim_id, business_book_id, business_entry_id, amount_minor, person_book_id, person_status, accepted_by)
        VALUES (t, claim, c.business_book_id, business_entry, c.requested_minor, p.personal_book_id, status, actor);
    UPDATE public.claims x SET obligation_book_id = c.business_book_id, obligation_entry_id = business_entry,
            recognized_at = clock_timestamp(), recognized_by = actor
        WHERE x.tenant_id = t AND x.id = claim;
    RETURN status;
END $$;
REVOKE ALL ON FUNCTION public.accept_claim(uuid, uuid, uuid, uuid) FROM PUBLIC;

-- Records the person's side of an accepted cash claim, in their Book:
-- an entry that debits what the business owes them for the claim's
-- amount, either reclassifying a charge already recorded, recording a fed
-- charge, or a new entry for cash. Only the owner of that Book does it.
CREATE FUNCTION public.record_person_side(t uuid, claim uuid, person_entry uuid, method text, item uuid, actor uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE o public.claim_obligations%ROWTYPE; c public.claims%ROWTYPE; terms public.book_link_terms%ROWTYPE; debit bigint;
BEGIN
    SELECT * INTO o FROM public.claim_obligations x WHERE x.tenant_id = t AND x.claim_id = claim FOR UPDATE;
    IF o.claim_id IS NULL OR o.person_status <> 'pending' THEN
        RAISE EXCEPTION 'This side is already recorded, or has none to record.' USING ERRCODE = 'VE409';
    END IF;
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t) AND public.ledger_context(t, o.person_book_id)) THEN
        RAISE EXCEPTION 'only the owner of the person''s Book records their side' USING ERRCODE = '42501';
    END IF;
    SELECT * INTO c FROM public.claims x WHERE x.tenant_id = t AND x.id = claim;
    terms := public.active_link_terms(t, o.person_book_id, o.business_book_id);
    SELECT coalesce(sum(CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO debit
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = o.person_book_id AND x.entry_id = person_entry AND x.account_id = terms.payer_account_id;
    IF terms.link_id IS NULL OR debit <> o.amount_minor THEN
        RAISE EXCEPTION 'The person''s entry records what the business owes them, for the claim''s amount.' USING ERRCODE = 'VE422';
    END IF;
    UPDATE public.claim_obligations x SET person_status = 'recorded', person_entry_id = person_entry, person_method = method, matched_bank_item_id = item,
            person_recorded_at = clock_timestamp(), person_recorded_by = actor
        WHERE x.tenant_id = t AND x.claim_id = claim;
END $$;
REVOKE ALL ON FUNCTION public.record_person_side(uuid, uuid, uuid, text, uuid, uuid) FROM PUBLIC;

-- An approved correction of an accepted cash claim: never an edit of
-- the entry first recorded, but a correcting entry for the difference,
-- dated the day it was approved, between the revision it corrects and the
-- one that corrects it. The obligation moves to the new amount. Where the
-- person already recorded their side, they record the same difference in
-- their Book; otherwise their waiting item simply reads the new amount.
CREATE TABLE public.claim_obligation_adjustments (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    claim_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    report_id uuid NOT NULL,
    from_revision integer NOT NULL,
    to_revision integer NOT NULL CHECK (to_revision > from_revision),
    delta_minor bigint NOT NULL CHECK (delta_minor <> 0),
    business_entry_id uuid NOT NULL,
    person_book_id uuid,
    person_status text NOT NULL CHECK (person_status IN ('pending', 'recorded', 'not_applicable', 'folded')),
    person_entry_id uuid,
    person_recorded_at timestamptz,
    person_recorded_by uuid,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_by uuid NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, claim_id) REFERENCES public.claim_obligations(tenant_id, claim_id),
    FOREIGN KEY (tenant_id, business_book_id, business_entry_id) REFERENCES public.entries(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, report_id) REFERENCES public.expense_reports(tenant_id, id),
    CHECK ((person_status = 'recorded') = (person_entry_id IS NOT NULL)),
    CHECK ((person_entry_id IS NULL) = (person_recorded_at IS NULL)),
    CHECK (person_status <> 'pending' OR person_book_id IS NOT NULL)
);
CREATE INDEX claim_obligation_adjustments_pending ON public.claim_obligation_adjustments(tenant_id, person_book_id) WHERE person_status = 'pending';
ALTER TABLE public.claim_obligation_adjustments ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.claim_obligation_adjustments FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.claim_obligation_adjustments FROM PUBLIC;
CREATE POLICY adjustment_read ON public.claim_obligation_adjustments FOR SELECT USING (access.owner_allowed(tenant_id)
    OR public.ledger_allowed(tenant_id, business_book_id, false) OR person_book_id IS NOT NULL AND public.ledger_allowed(tenant_id, person_book_id, false));
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.claim_obligation_adjustments FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('business_book_id', 'person_book_id');

-- Records an approved correction of an accepted cash claim: the business's
-- correcting entry moves what it owes the person by delta, and the
-- obligation with it.
CREATE FUNCTION public.adjust_claim_obligation(t uuid, claim uuid, report uuid, from_revision integer, to_revision integer, delta bigint,
    business_entry uuid, actor uuid) RETURNS text
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE o public.claim_obligations%ROWTYPE; moved bigint; status text;
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner corrects a claim' USING ERRCODE = '42501';
    END IF;
    SELECT * INTO o FROM public.claim_obligations x WHERE x.tenant_id = t AND x.claim_id = claim FOR UPDATE;
    IF o.claim_id IS NULL OR delta = 0 OR o.amount_minor + delta < 0 THEN
        RAISE EXCEPTION 'Only an accepted cash claim is corrected this way.' USING ERRCODE = 'VE422';
    END IF;
    -- The entry moves what the business owes the person, and nothing else
    -- of it.
    SELECT coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO moved
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = o.business_book_id AND x.entry_id = business_entry
            AND x.account_id IN (SELECT k.beneficiary_account_id FROM public.book_link_terms k WHERE k.tenant_id = t
                AND k.beneficiary_book_id = o.business_book_id AND k.treatment = 'owed');
    IF moved <> delta THEN
        RAISE EXCEPTION 'The correcting entry moves what the business owes the person by the difference.' USING ERRCODE = 'VE422';
    END IF;
    status := CASE o.person_status WHEN 'recorded' THEN 'pending' WHEN 'pending' THEN 'folded' ELSE 'not_applicable' END;
    INSERT INTO public.claim_obligation_adjustments(tenant_id, id, claim_id, business_book_id, report_id, from_revision, to_revision, delta_minor,
            business_entry_id, person_book_id, person_status, created_by)
        VALUES (t, gen_random_uuid(), claim, o.business_book_id, report, from_revision, to_revision, delta, business_entry, o.person_book_id, status, actor);
    UPDATE public.claim_obligations x SET amount_minor = x.amount_minor + delta WHERE x.tenant_id = t AND x.claim_id = claim;
    RETURN status;
END $$;
REVOKE ALL ON FUNCTION public.adjust_claim_obligation(uuid, uuid, uuid, integer, integer, bigint, uuid, uuid) FROM PUBLIC;

-- Records the person's side of a correction in their Book: an entry that
-- moves what the business owes them by the same difference.
CREATE FUNCTION public.record_person_adjustment(t uuid, adjustment uuid, person_entry uuid, actor uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE a public.claim_obligation_adjustments%ROWTYPE; terms public.book_link_terms%ROWTYPE; debit bigint;
BEGIN
    SELECT * INTO a FROM public.claim_obligation_adjustments x WHERE x.tenant_id = t AND x.id = adjustment FOR UPDATE;
    IF a.id IS NULL OR a.person_status <> 'pending' THEN
        RAISE EXCEPTION 'This correction is already recorded, or has none to record.' USING ERRCODE = 'VE409';
    END IF;
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t) AND public.ledger_context(t, a.person_book_id)) THEN
        RAISE EXCEPTION 'only the owner of the person''s Book records their side' USING ERRCODE = '42501';
    END IF;
    terms := public.active_link_terms(t, a.person_book_id, a.business_book_id);
    SELECT coalesce(sum(CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO debit
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = a.person_book_id AND x.entry_id = person_entry AND x.account_id = terms.payer_account_id;
    IF terms.link_id IS NULL OR debit <> a.delta_minor THEN
        RAISE EXCEPTION 'The person''s entry moves what the business owes them by the difference.' USING ERRCODE = 'VE422';
    END IF;
    UPDATE public.claim_obligation_adjustments x SET person_status = 'recorded', person_entry_id = person_entry, person_recorded_at = clock_timestamp(), person_recorded_by = actor
        WHERE x.tenant_id = t AND x.id = adjustment;
END $$;
REVOKE ALL ON FUNCTION public.record_person_adjustment(uuid, uuid, uuid, uuid) FROM PUBLIC;

-- Open costs include the cash claims a business accepted.
CREATE OR REPLACE FUNCTION public.open_costs(t uuid, creditor uuid, debtor uuid, debtor_account uuid, leaving_out uuid)
    RETURNS TABLE (pair_key text, kind text, payer_book_id uuid, payer_entry_id uuid, beneficiary_book_id uuid, beneficiary_entry_id uuid,
        book_id uuid, entry_id uuid, accounting_date date, sequence bigint, description text, amount bigint, repaid bigint, open bigint)
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
WITH costs AS (
    SELECT p.pair_key, p.kind, p.payer_book_id, p.payer_entry_id, p.beneficiary_book_id, p.beneficiary_entry_id,
        p.beneficiary_book_id AS book_id, p.beneficiary_entry_id AS entry_id, e.accounting_date, e.sequence::bigint AS sequence, e.description,
        public.cost_amount(t, p.beneficiary_book_id, p.beneficiary_entry_id) AS amount
    FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
    WHERE p.tenant_id = t
        AND (p.kind IN ('cost', 'carried', 'claim') AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
            OR p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor)
        AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)
    UNION ALL
    -- A cash claim the business accepted, whether or not the person's side
    -- is recorded yet.
    SELECT 'claim:' || o.claim_id::text, 'claim', o.person_book_id, o.person_entry_id, o.business_book_id, o.business_entry_id,
        o.business_book_id, o.business_entry_id, e.accounting_date, e.sequence::bigint, e.description, o.amount_minor
    FROM public.claim_obligations o JOIN public.entries e ON e.tenant_id = t AND e.book_id = o.business_book_id AND e.id = o.business_entry_id
    WHERE o.tenant_id = t AND o.person_book_id = creditor AND o.business_book_id = debtor
        AND NOT public.entry_reversed(t, o.business_book_id, o.business_entry_id)
)
SELECT c.pair_key, c.kind, c.payer_book_id, c.payer_entry_id, c.beneficiary_book_id, c.beneficiary_entry_id, c.book_id, c.entry_id,
    c.accounting_date, c.sequence, c.description, c.amount, least(c.amount, r.repaid)::bigint, (c.amount - least(c.amount, r.repaid))::bigint
FROM costs c CROSS JOIN LATERAL (SELECT public.cost_repaid_amount(t, c.book_id, c.entry_id, leaving_out) AS repaid) r
ORDER BY c.accounting_date, c.sequence, c.pair_key
$$;

-- A claim's cost moved to another account by a reclassifying entry, never
-- by rewriting the recorded one.
ALTER TABLE public.claims ADD COLUMN reclassified_entry_id uuid;

-- A draft never submitted is the owner's to throw away: its lines, its
-- one revision and its events go, and claims no other report holds and
-- with no receipt go with it. Receipts stay with their claims, and the
-- reference stays used. A report that was ever submitted, or that money is
-- matched to, is kept.
CREATE FUNCTION public.delete_draft_report(t uuid, b uuid, report uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE r public.expense_reports%ROWTYPE; held uuid[];
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner deletes a draft' USING ERRCODE = '42501';
    END IF;
    SELECT * INTO r FROM public.expense_reports x WHERE x.tenant_id = t AND x.business_book_id = b AND x.id = report FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'That report isn''t here.' USING ERRCODE = 'VE422';
    END IF;
    IF r.workflow <> 'draft' OR r.revision <> 1
        OR EXISTS (SELECT 1 FROM public.report_revisions v WHERE v.tenant_id = t AND v.report_id = report AND v.submitted_at IS NOT NULL)
        OR EXISTS (SELECT 1 FROM public.report_approvals a WHERE a.tenant_id = t AND a.report_id = report)
        OR EXISTS (SELECT 1 FROM public.settlement_statements s WHERE s.tenant_id = t AND s.report_id = report) THEN
        RAISE EXCEPTION 'Only a draft that was never submitted is deleted; withdraw this one instead.' USING ERRCODE = 'VE422';
    END IF;
    SELECT array_agg(l.claim_id) INTO held FROM public.report_lines l WHERE l.tenant_id = t AND l.report_id = report;
    IF EXISTS (SELECT 1 FROM public.payment_residuals x WHERE x.tenant_id = t AND x.report_id = report) THEN
        RAISE EXCEPTION 'Money is matched to this report, so it isn''t deleted.' USING ERRCODE = 'VE422';
    END IF;
    IF EXISTS (SELECT 1 FROM public.allocations a WHERE a.tenant_id = t AND a.claim_id = ANY(coalesce(held, '{}')) AND a.removed_at IS NULL) THEN
        RAISE EXCEPTION 'Money is matched to this report, so it isn''t deleted.' USING ERRCODE = 'VE422';
    END IF;
    PERFORM set_config('vult.erasing', 'on', true);
    DELETE FROM public.report_lines l WHERE l.tenant_id = t AND l.report_id = report;
    DELETE FROM public.report_events e WHERE e.tenant_id = t AND e.report_id = report;
    DELETE FROM public.report_notes n WHERE n.tenant_id = t AND n.report_id = report;
    DELETE FROM public.report_revisions v WHERE v.tenant_id = t AND v.report_id = report;
    DELETE FROM public.expense_reports x WHERE x.tenant_id = t AND x.id = report;
    PERFORM set_config('vult.erasing', '', true);
    INSERT INTO public.retired_report_references(tenant_id, business_book_id, reference, report_id) VALUES (t, b, r.reference, report);
    DELETE FROM public.claims c WHERE c.tenant_id = t AND c.id = ANY(coalesce(held, '{}')) AND c.kind = 'cash' AND c.recognized_at IS NULL
        AND NOT EXISTS (SELECT 1 FROM public.report_lines l WHERE l.tenant_id = c.tenant_id AND l.claim_id = c.id)
        AND NOT EXISTS (SELECT 1 FROM public.claim_evidence v WHERE v.tenant_id = c.tenant_id AND v.claim_id = c.id);
END $$;
REVOKE ALL ON FUNCTION public.delete_draft_report(uuid, uuid, uuid) FROM PUBLIC;

-- A business Book keeps expense reports when a personal Book's costs are
-- owed back to it through an active link. Only someone who may read the
-- Book learns it.
CREATE FUNCTION public.keeps_expense_reports(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) AND public.book_profile(t, b) = 'business'
    AND EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id
        WHERE x.tenant_id = t AND x.beneficiary_book_id = b AND x.treatment = 'owed' AND x.active AND l.active
            AND public.book_profile(t, x.payer_book_id) = 'personal')
$$;
REVOKE ALL ON FUNCTION public.keeps_expense_reports(uuid, uuid) FROM PUBLIC;

-- Who owes whom counts a cash claim's entries as paired.
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
    -- A cash claim's sides are paired too; until the person records theirs,
    -- the two Books differ by it, as they should.
    UNION ALL SELECT o.business_book_id, o.business_entry_id FROM public.claim_obligations o WHERE o.tenant_id = t
    UNION ALL SELECT o.person_book_id, o.person_entry_id FROM public.claim_obligations o WHERE o.tenant_id = t AND o.person_entry_id IS NOT NULL
    UNION ALL SELECT a.business_book_id, a.business_entry_id FROM public.claim_obligation_adjustments a WHERE a.tenant_id = t
    UNION ALL SELECT a.person_book_id, a.person_entry_id FROM public.claim_obligation_adjustments a WHERE a.tenant_id = t AND a.person_entry_id IS NOT NULL
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
