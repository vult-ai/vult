-- Settling up between linked Books, as the owner decided after review:
-- the business's direction to its owner decides whether the costs repaid
-- are named, an overpayment is recorded plainly rather than refused, and
-- undoing an entry frees the bank item it recorded.

-- Checks a settlement: the debtor's bank item paying out and the
-- creditor's taking in the same amount, at most five days apart, between
-- the two accounts an owed direction names. Returns the cost pairs it
-- repays (their entries in the debtor's Book) and the excess over what is
-- owed, which the reverse direction's accounts record: a draw where the
-- business's direction to its owner is owner's equity, else owed back.
-- Naming the costs (repays) is required of a business repaying its owner
-- with no owner's-equity direction (an accountable plan), and named costs
-- add up to exactly what the payment repays. Unnamed, a payment repays the
-- oldest open costs it covers whole. leaving_out is the settlement's own
-- entry once posted.
DROP FUNCTION public.settlement_check(uuid, uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid);
CREATE FUNCTION public.settlement_check(t uuid, debtor uuid, debtor_account uuid, debtor_item uuid, creditor uuid, creditor_account uuid, creditor_item uuid,
        repays jsonb, leaving_out uuid) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.bank_transactions%ROWTYPE; c public.bank_transactions%ROWTYPE; dclass text; cclass text; owed bigint; total bigint := 0;
    k text; entry uuid; cost bigint; amount bigint; excess bigint; repaying bigint; costs uuid[] := '{}'; rev public.book_link_terms%ROWTYPE;
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
        SELECT p.beneficiary_entry_id INTO entry FROM public.entry_pairs p
            WHERE p.tenant_id = t AND p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor AND p.pair_key = k
                AND NOT public.entry_reversed(t, debtor, p.beneficiary_entry_id);
        IF entry IS NULL THEN
            RAISE EXCEPTION 'repays names %, which isn''t a recorded cost % paid for %; name one from the open costs.', k, creditor_name, debtor_name USING ERRCODE = 'VP020';
        END IF;
        IF entry = ANY (costs) THEN
            RAISE EXCEPTION 'repays names % twice; name each cost once.', k USING ERRCODE = 'VP020';
        END IF;
        IF public.cost_repaid_by(t, debtor, entry, leaving_out) IS NOT NULL THEN
            RAISE EXCEPTION 'The cost % is already repaid; name an open cost instead.', k USING ERRCODE = 'VP020';
        END IF;
        costs := costs || entry;
        total := total + (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = entry AND x.side = 'credit');
    END LOOP;
    IF cardinality(costs) = 0 AND required THEN
        RAISE EXCEPTION '% repaying % names the costs it repays (repays), adding up to %; pick them from the open costs.',
            debtor_name, creditor_name, public.book_amount(t, debtor, repaying) USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) > 0 AND total <> repaying THEN
        RAISE EXCEPTION 'The costs named add up to %, but this payment repays %; name costs that add up to it exactly.',
            public.book_amount(t, debtor, total), public.book_amount(t, debtor, repaying) USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) = 0 THEN
        -- Unnamed: the oldest open costs the payment covers whole.
        FOR r IN SELECT p.beneficiary_entry_id AS entry,
                (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = p.beneficiary_entry_id AND x.side = 'credit') AS amount
            FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id = t AND e.book_id = debtor AND e.id = p.beneficiary_entry_id
            WHERE p.tenant_id = t AND p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
                AND NOT public.entry_reversed(t, debtor, p.beneficiary_entry_id) AND public.cost_repaid_by(t, debtor, p.beneficiary_entry_id, leaving_out) IS NULL
            ORDER BY e.accounting_date, e.sequence LOOP
            EXIT WHEN total + r.amount > repaying;
            costs := costs || r.entry;
            total := total + r.amount;
        END LOOP;
    END IF;
    RETURN jsonb_build_object('costs', to_jsonb(costs), 'excess', excess, 'repaying', repaying, 'required', required);
END $$;

-- Records a settlement the owner decided, or recorded at once: each entry
-- records its own bank item, on its date, to the owed direction's account
-- for what was owed and, for any excess, to the reverse direction's
-- account. The costs it repays are kept.
CREATE OR REPLACE FUNCTION public.settle_pair(t uuid, key text, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid, repays jsonb) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE ditem uuid; dbank uuid; citem uuid; cbank uuid; owed public.book_link_terms%ROWTYPE; rev public.book_link_terms%ROWTYPE;
    checked jsonb; e uuid; excess bigint; repaying bigint; amount bigint; bad int;
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
    FOR e IN SELECT (jsonb_array_elements_text(checked -> 'costs'))::uuid LOOP
        INSERT INTO public.settlement_repays(tenant_id, settlement_book_id, settlement_entry_id, cost_book_id, cost_entry_id) VALUES (t, debtor, debtor_entry, debtor, e);
    END LOOP;
END $$;

-- Joins the halves of a settlement prepared as proposals: the first waits,
-- the second, in the other Book, joins it once both are checked.
CREATE OR REPLACE FUNCTION public.pair_settlement(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; args jsonb; oargs jsonb; key text;
    debtor uuid; creditor uuid; dp uuid; cp uuid; dargs jsonb; cargs jsonb; checked jsonb;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed'
                AND (x.payer_book_id = b AND x.payer_account_id::text = args ->> 'other_account_id' OR x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id')) THEN
        RAISE EXCEPTION 'The settlement can''t be prepared: other_account_id is no account an owed direction of an active link names in this Book.' USING ERRCODE = 'VP020';
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

-- Undoing an entry frees the bank item it recorded: the undo revokes the
-- item's link with its own receipt, so the item can be recorded again, or
-- for another Book, without a separate unlink.
CREATE OR REPLACE FUNCTION public.bank_unlink_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.revoke_receipt_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM public.requests r
        WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id AND r.id = NEW.revoke_receipt_id
        AND r.actor_id = NEW.revoked_by AND (r.operation = 'unlink_bank_activity'
            OR r.operation = 'reverse_journal' AND EXISTS (SELECT 1 FROM public.entries e WHERE e.tenant_id = NEW.tenant_id
                AND e.book_id = NEW.book_id AND e.reversal_of = NEW.entry_id AND e.receipt_id = NEW.revoke_receipt_id))) THEN
        RAISE EXCEPTION 'decision receipt differs from the record' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;

-- The undo already counted against the agent's kind of activity; the link
-- it revokes doesn't count again.
CREATE OR REPLACE FUNCTION public.agent_kind_unlinked() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF coalesce(current_setting('vult.undo_unlink', true), '') = 'on' THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = OLD.actor_id)
        AND NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = NEW.revoked_by) THEN
        PERFORM public.agent_kind_note(NEW.tenant_id, NEW.book_id, NEW.transaction_id, 'undone',
            public.agent_other_account(NEW.tenant_id, NEW.book_id, NEW.transaction_id, '{}'::jsonb, NEW.entry_id));
    END IF;
    RETURN NULL;
END $$;

REVOKE ALL ON FUNCTION public.settlement_check(uuid, uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid) FROM PUBLIC;


-- An opening balance between linked Books: what one Book owed the other
-- before their first bank line, entered once. It is two mirrored entries,
-- decided and undone as one: the Book that owes credits the owed
-- direction's payable, the Book owed debits its receivable, and each posts
-- the other side to its own opening-balance equity account. Pairs record
-- the Book that owes as the payer.
ALTER TABLE public.proposal_pairs DROP CONSTRAINT proposal_pairs_kind_check;
ALTER TABLE public.proposal_pairs ADD CONSTRAINT proposal_pairs_kind_check CHECK (kind IN ('cost', 'settlement', 'opening'));
ALTER TABLE public.entry_pairs DROP CONSTRAINT entry_pairs_kind_check;
ALTER TABLE public.entry_pairs ADD CONSTRAINT entry_pairs_kind_check CHECK (kind IN ('cost', 'settlement', 'opening'));

CREATE OR REPLACE FUNCTION public.entry_pair_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE terms public.book_link_terms%ROWTYPE; item public.bank_transactions%ROWTYPE; bank_account uuid; tid uuid;
    paid text; amount bigint; n int; bad int;
BEGIN
    IF NOT (access.owner_allowed(NEW.tenant_id) AND access.owner_write_allowed(NEW.tenant_id)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    IF NEW.kind IN ('settlement', 'opening') THEN
        -- Settlements and opening balances are checked and recorded by
        -- settle_pair and open_link_balance only.
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

-- A Book's opening-balance equity account, by role.
CREATE FUNCTION public.opening_equity_account(t uuid, b uuid) RETURNS uuid
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT a.id FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = b AND a.class = 'equity' AND a.role = 'opening_balance'
    AND a.status IS DISTINCT FROM 'retired' AND NOT a.heading ORDER BY a.code, a.id LIMIT 1
$$;

-- Checks an opening balance's two entries against the owed direction and
-- each Book's opening-balance account, and that the direction has no other
-- live opening balance. leaving_out names this pair's debtor entry, once
-- posted.
CREATE FUNCTION public.opening_check(t uuid, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid, leaving_out uuid) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE owed public.book_link_terms%ROWTYPE; dequity uuid; cequity uuid; amount bigint; bad int; debtor_name text; creditor_name text;
BEGIN
    debtor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = debtor);
    creditor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = creditor);
    owed := public.active_link_terms(t, creditor, debtor);
    IF owed.treatment IS DISTINCT FROM 'owed' THEN
        RAISE EXCEPTION 'Nothing is owed this way: set "% pays for %" to owed back in Links, then enter the opening balance.', creditor_name, debtor_name USING ERRCODE = 'VP020';
    END IF;
    dequity := public.opening_equity_account(t, debtor);
    cequity := public.opening_equity_account(t, creditor);
    IF dequity IS NULL OR cequity IS NULL THEN
        RAISE EXCEPTION 'Add an opening balances account (equity, role opening balance) in %, then enter the opening balance.',
            CASE WHEN dequity IS NULL THEN debtor_name ELSE creditor_name END USING ERRCODE = 'VP020';
    END IF;
    amount := (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry AND x.side = 'credit');
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = owed.beneficiary_account_id AND x.side = 'credit' AND x.amount_minor = amount
            OR x.account_id = dequity AND x.side = 'debit' AND x.amount_minor = amount, false)) + abs(count(*) - 2) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry;
    IF bad <> 0 OR amount IS NULL THEN
        RAISE EXCEPTION 'An opening balance in % credits what it owes % and debits its opening balances account, for the same amount.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = owed.payer_account_id AND x.side = 'debit' AND x.amount_minor = amount
            OR x.account_id = cequity AND x.side = 'credit' AND x.amount_minor = amount, false)) + abs(count(*) - 2) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = creditor AND x.entry_id = creditor_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'An opening balance in % debits what % owes it and credits its opening balances account, for the same amount (%).',
            creditor_name, debtor_name, public.book_amount(t, debtor, amount) USING ERRCODE = 'VP020';
    END IF;
    IF (SELECT count(DISTINCT e.accounting_date) FROM public.entries e WHERE e.tenant_id = t
            AND (e.book_id = debtor AND e.id = debtor_entry OR e.book_id = creditor AND e.id = creditor_entry) AND e.reversal_of IS NULL) <> 1
        OR (SELECT count(*) FROM public.entries e WHERE e.tenant_id = t
            AND (e.book_id = debtor AND e.id = debtor_entry OR e.book_id = creditor AND e.id = creditor_entry) AND e.reversal_of IS NULL) <> 2 THEN
        RAISE EXCEPTION 'Both entries of an opening balance are dated the same day, the day it was owed as of.' USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = t AND p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor
            AND p.payer_entry_id IS DISTINCT FROM leaving_out AND NOT public.entry_reversed(t, debtor, p.payer_entry_id)) THEN
        RAISE EXCEPTION 'What % owed % at the start is already entered; undo it to enter it differently.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
END $$;

-- Records an opening balance the owner decided, or entered at once.
CREATE FUNCTION public.open_link_balance(t uuid, key text, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    PERFORM public.opening_check(t, debtor, debtor_entry, creditor, creditor_entry, debtor_entry);
    PERFORM set_config('vult.settling', key, true);
    INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id)
        VALUES (t, key, 'opening', debtor, debtor_entry, creditor, creditor_entry);
    PERFORM set_config('vult.settling', '', true);
END $$;

REVOKE ALL ON FUNCTION public.opening_equity_account(uuid, uuid), public.opening_check(uuid, uuid, uuid, uuid, uuid, uuid),
    public.open_link_balance(uuid, text, uuid, uuid, uuid, uuid) FROM PUBLIC;


-- Opening balances prepared as proposals: each Book's half is a journal
-- with opening and other_book_id; the first waits, the second joins it once
-- both mirror each other by the owed direction and opening-balance
-- accounts. Opening balances have no bank item.
ALTER TABLE public.proposal_pairs ALTER COLUMN transaction_id DROP NOT NULL;

CREATE FUNCTION public.pair_opening(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; args jsonb; oargs jsonb; key text; them uuid;
    debtor uuid; creditor uuid; dargs jsonb; cargs jsonb; dp uuid; cp uuid; owed public.book_link_terms%ROWTYPE; dequity uuid; cequity uuid;
    amount bigint; bad int; debtor_name text; creditor_name text;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    them := (args ->> 'other_book_id')::uuid;
    -- This half owes when it credits the payable of the other Book's owed direction.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(args -> 'lines') l
            WHERE l ->> 'account_id' = (public.active_link_terms(t, them, b)).beneficiary_account_id::text AND l ->> 'side' = 'credit') THEN
        debtor := b; creditor := them;
    ELSIF EXISTS (SELECT 1 FROM jsonb_array_elements(args -> 'lines') l
            WHERE l ->> 'account_id' = (public.active_link_terms(t, b, them)).payer_account_id::text AND l ->> 'side' = 'debit') THEN
        debtor := them; creditor := b;
    ELSE
        RAISE EXCEPTION 'The opening balance can''t be prepared: it credits what this Book owes the other, or debits what the other owes it, by an owed direction of an active link.' USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id = them AND q.operation = 'post_journal'
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' -> 'opening') = 'true'::jsonb
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'other_book_id' = b::text
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
            AND NOT EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = q.tenant_id
                AND (x.payer_book_id = q.book_id AND x.payer_proposal_id = q.id OR x.beneficiary_book_id = q.book_id AND x.beneficiary_proposal_id = q.id))
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF debtor = b THEN dargs := args; dp := p; cargs := oargs; cp := other.id; ELSE dargs := oargs; dp := other.id; cargs := args; cp := p; END IF;
    debtor_name := (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = debtor);
    creditor_name := (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = creditor);
    owed := public.active_link_terms(t, creditor, debtor);
    dequity := public.opening_equity_account(t, debtor);
    cequity := public.opening_equity_account(t, creditor);
    IF owed.treatment IS DISTINCT FROM 'owed' OR dequity IS NULL OR cequity IS NULL THEN
        RAISE EXCEPTION 'The opening balance can''t be prepared: % needs an owed direction to %, and both Books an opening balances account.', creditor_name, debtor_name USING ERRCODE = 'VP020';
    END IF;
    amount := (SELECT sum((l ->> 'amount_minor')::bigint) FROM jsonb_array_elements(dargs -> 'lines') l WHERE l ->> 'side' = 'credit');
    SELECT count(*) FILTER (WHERE NOT coalesce(l ->> 'account_id' = owed.beneficiary_account_id::text AND l ->> 'side' = 'credit' AND (l ->> 'amount_minor')::bigint = amount
            OR l ->> 'account_id' = dequity::text AND l ->> 'side' = 'debit' AND (l ->> 'amount_minor')::bigint = amount, false)) + abs(count(*) - 2)
        INTO bad FROM jsonb_array_elements(dargs -> 'lines') l;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The opening balance can''t be prepared: in % it credits what it owes % and debits its opening balances account, for the same amount.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    SELECT count(*) FILTER (WHERE NOT coalesce(l ->> 'account_id' = owed.payer_account_id::text AND l ->> 'side' = 'debit' AND (l ->> 'amount_minor')::bigint = amount
            OR l ->> 'account_id' = cequity::text AND l ->> 'side' = 'credit' AND (l ->> 'amount_minor')::bigint = amount, false)) + abs(count(*) - 2)
        INTO bad FROM jsonb_array_elements(cargs -> 'lines') l;
    IF bad <> 0 OR dargs ->> 'accounting_date' IS DISTINCT FROM cargs ->> 'accounting_date' THEN
        RAISE EXCEPTION 'The opening balance can''t be prepared: in % it mirrors %, on the same date, for the same amount.', creditor_name, debtor_name USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.entry_pairs x WHERE x.tenant_id = t AND x.kind = 'opening' AND x.payer_book_id = debtor AND x.beneficiary_book_id = creditor
            AND NOT public.entry_reversed(t, debtor, x.payer_entry_id)) THEN
        RAISE EXCEPTION 'The opening balance can''t be prepared: what % owed % at the start is already entered.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, kind, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id)
        VALUES (t, key, 'opening', debtor, dp, creditor, cp, NULL);
END $$;
REVOKE ALL ON FUNCTION public.pair_opening(uuid, uuid, uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.pair_proposal(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; terms public.book_link_terms%ROWTYPE;
    item public.bank_transactions%ROWTYPE; args jsonb; oargs jsonb; key text; line jsonb; acc public.accounts%ROWTYPE;
    amount bigint; debits bigint := 0; credits int := 0; paid text;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    IF NOT FOUND OR NOT public.ledger_allowed(t, b, false)
        OR me.connection_id::text IS DISTINCT FROM current_setting('vult.connection_id', true) THEN
        RAISE EXCEPTION 'only the preparing connection pairs its proposal' USING ERRCODE = '42501';
    END IF;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    IF key IS NULL THEN
        RETURN;
    END IF;
    IF me.operation = 'post_journal' AND (args -> 'opening') = 'true'::jsonb THEN
        PERFORM public.pair_opening(t, b, p);
        RETURN;
    END IF;
    IF me.operation = 'record_bank_activity' AND (args -> 'settlement') = 'true'::jsonb THEN
        PERFORM public.pair_settlement(t, b, p);
        RETURN;
    END IF;
    IF me.operation = 'record_bank_activity' THEN
        IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id
                WHERE x.tenant_id = t AND x.payer_book_id = b AND x.payer_account_id::text = args ->> 'other_account_id' AND x.active AND l.active) THEN
            RAISE EXCEPTION 'The pair can''t be prepared: other_account_id is no linked Book''s account in this Book, or that link is turned off.' USING ERRCODE = 'VP020';
        END IF;
        RETURN;
    END IF;
    IF me.operation <> 'post_journal' THEN
        RAISE EXCEPTION 'The pair can''t be prepared: a pair is record_bank_activity in the Book that paid and post_journal in the Book it paid for.' USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id <> b AND q.operation = 'record_bank_activity'
            AND (args ->> 'payer_book_id' IS NULL OR q.book_id::text = args ->> 'payer_book_id')
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' -> 'settlement') IS DISTINCT FROM 'true'::jsonb
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: prepare the half in the Book that paid first; no waiting half has pair_key %.', key USING ERRCODE = 'VP020';
    END IF;
    terms := public.active_link_terms(t, other.book_id, b);
    IF terms.link_id IS NULL THEN
        RAISE EXCEPTION 'The pair can''t be prepared: these Books aren''t linked for this direction, or the link is turned off.' USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = t AND x.payer_book_id = other.book_id AND x.payer_proposal_id = other.id) THEN
        RAISE EXCEPTION 'The pair can''t be prepared: its other half is already paired.' USING ERRCODE = 'VP020';
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF oargs ->> 'transaction_id' IS DISTINCT FROM args ->> 'paired_transaction_id' THEN
        RAISE EXCEPTION 'The pair can''t be prepared: paired_transaction_id isn''t the bank item its other half records.' USING ERRCODE = 'VP020';
    END IF;
    IF oargs ->> 'other_account_id' IS DISTINCT FROM terms.payer_account_id::text THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the other half doesn''t record to the account this link names in that Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT * INTO item FROM public.bank_transactions WHERE tenant_id = t AND book_id = other.book_id AND id = (oargs ->> 'transaction_id')::uuid;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the bank item isn''t in the Book that paid.' USING ERRCODE = 'VP020';
    END IF;
    -- Amounts follow the bank account's own balance: a card charge raises a
    -- liability (positive), money paid from an account lowers an asset
    -- (negative). A cost pair records money paid out.
    paid := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = other.book_id
        AND a.id = coalesce((SELECT x.account_id FROM public.bank_accounts x WHERE x.tenant_id = t AND x.book_id = other.book_id AND x.id = item.bank_account_id), item.account_id));
    IF NOT (paid = 'liability' AND item.amount_minor > 0 OR paid = 'asset' AND item.amount_minor < 0) THEN
        RAISE EXCEPTION 'The pair can''t be prepared: a cost pair records money paid out, such as a charge on a card.' USING ERRCODE = 'VP020';
    END IF;
    amount := abs(item.amount_minor);
    IF (SELECT k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = b) IS DISTINCT FROM item.currency THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the bank item is in another currency than this Book.' USING ERRCODE = 'VP020';
    END IF;
    IF args ->> 'accounting_date' IS DISTINCT FROM item.occurred_on::text THEN
        RAISE EXCEPTION 'The pair can''t be prepared: this entry''s date isn''t the bank item''s date (%).', item.occurred_on USING ERRCODE = 'VP020';
    END IF;
    FOR line IN SELECT * FROM jsonb_array_elements(coalesce(args -> 'lines', '[]'::jsonb)) LOOP
        SELECT * INTO acc FROM public.accounts WHERE tenant_id = t AND book_id = b AND id = (line ->> 'account_id')::uuid;
        IF line ->> 'side' = 'credit' THEN
            credits := credits + 1;
            IF acc.id IS DISTINCT FROM terms.beneficiary_account_id OR (line ->> 'amount_minor')::bigint <> amount THEN
                RAISE EXCEPTION 'The pair can''t be prepared: this entry credits the account the link names here (%) with the whole amount.', terms.beneficiary_account_id USING ERRCODE = 'VP020';
            END IF;
        ELSE
            IF NOT (acc.class = 'expense' OR acc.class = 'asset' AND coalesce(acc.role, '') NOT IN ('cash', 'card')
                    AND NOT EXISTS (SELECT 1 FROM public.bank_accounts x WHERE x.tenant_id = t AND x.book_id = b AND x.account_id = acc.id)) THEN
                RAISE EXCEPTION 'The pair can''t be prepared: this entry debits only expenses, or assets that aren''t cash, a card or a bank account.' USING ERRCODE = 'VP020';
            END IF;
            debits := debits + (line ->> 'amount_minor')::bigint;
        END IF;
    END LOOP;
    IF credits <> 1 OR debits <> amount THEN
        RAISE EXCEPTION 'The pair can''t be prepared: this entry is for the bank item''s amount, with one credit to the link''s account.' USING ERRCODE = 'VP020';
    END IF;
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id)
        VALUES (t, key, other.book_id, other.id, b, p, item.id);
END $$;

CREATE OR REPLACE FUNCTION public.proposal_pair_of(t uuid, b uuid, p uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE coalesce((
    SELECT jsonb_build_object('state', 'paired', 'pair_key', x.pair_key, 'transaction_id', x.transaction_id, 'kind', x.kind,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'proposal_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_proposal_id ELSE x.payer_proposal_id END,
        'side', CASE WHEN x.payer_book_id = b THEN 'beneficiary' ELSE 'payer' END,
        -- A settlement's debtor is the owed direction's beneficiary.
        'link_active', CASE WHEN x.kind IN ('settlement', 'opening') THEN (public.active_link_terms(t, x.beneficiary_book_id, x.payer_book_id)).link_id
            ELSE (public.active_link_terms(t, x.payer_book_id, x.beneficiary_book_id)).link_id END IS NOT NULL)
    FROM public.proposal_pairs x WHERE x.tenant_id = t
        AND (x.payer_book_id = b AND x.payer_proposal_id = p OR x.beneficiary_book_id = b AND x.beneficiary_proposal_id = p)), (
    SELECT jsonb_build_object('state', 'waiting_for_pair', 'pair_key', args ->> 'pair_key', 'side', 'beneficiary',
        'book_id', x.beneficiary_book_id,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = x.beneficiary_book_id),
        'link_active', (public.active_link_terms(t, b, x.beneficiary_book_id)).link_id IS NOT NULL)
    FROM public.operation_proposals q
        CROSS JOIN LATERAL (SELECT convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' AS args) a
        JOIN public.book_link_terms x ON x.tenant_id = q.tenant_id AND x.payer_book_id = q.book_id AND x.payer_account_id::text = a.args ->> 'other_account_id'
    WHERE q.tenant_id = t AND q.book_id = b AND q.id = p AND q.operation = 'record_bank_activity' AND a.args ->> 'pair_key' IS NOT NULL
        AND (a.args -> 'settlement') IS DISTINCT FROM 'true'::jsonb
    LIMIT 1), (
    -- A settlement half waiting for the other Book's: the side is the
    -- partner's, the Book that pays (owes) or the Book paid.
    SELECT jsonb_build_object('state', 'waiting_for_pair', 'pair_key', a.args ->> 'pair_key', 'kind', 'settlement',
        'side', CASE WHEN x.payer_book_id = b THEN 'payer' ELSE 'beneficiary' END,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'link_active', (public.active_link_terms(t, x.payer_book_id, x.beneficiary_book_id)).link_id IS NOT NULL)
    FROM public.operation_proposals q
        CROSS JOIN LATERAL (SELECT convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' AS args) a
        JOIN public.book_link_terms x ON x.tenant_id = q.tenant_id AND x.treatment = 'owed'
            AND (x.payer_book_id = q.book_id AND x.payer_account_id::text = a.args ->> 'other_account_id'
                OR x.beneficiary_book_id = q.book_id AND x.beneficiary_account_id::text = a.args ->> 'other_account_id')
    WHERE q.tenant_id = t AND q.book_id = b AND q.id = p AND q.operation = 'record_bank_activity' AND a.args ->> 'pair_key' IS NOT NULL
        AND (a.args -> 'settlement') = 'true'::jsonb
    LIMIT 1), (
    -- An opening balance's half waiting for the other Book's.
    SELECT jsonb_build_object('state', 'waiting_for_pair', 'pair_key', a.args ->> 'pair_key', 'kind', 'opening',
        'side', CASE WHEN EXISTS (SELECT 1 FROM jsonb_array_elements(a.args -> 'lines') l
            WHERE l ->> 'account_id' = (public.active_link_terms(t, (a.args ->> 'other_book_id')::uuid, b)).beneficiary_account_id::text) THEN 'beneficiary' ELSE 'payer' END,
        'book_id', a.args ->> 'other_book_id',
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id::text = a.args ->> 'other_book_id'),
        'link_active', EXISTS (SELECT 1 FROM public.book_links l WHERE l.tenant_id = t AND l.active
            AND (l.book_a = b AND l.book_b::text = a.args ->> 'other_book_id' OR l.book_b = b AND l.book_a::text = a.args ->> 'other_book_id')))
    FROM public.operation_proposals q
        CROSS JOIN LATERAL (SELECT convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' AS args) a
    WHERE q.tenant_id = t AND q.book_id = b AND q.id = p AND q.operation = 'post_journal' AND a.args ->> 'pair_key' IS NOT NULL
        AND (a.args -> 'opening') = 'true'::jsonb
    LIMIT 1)) END
$$;
