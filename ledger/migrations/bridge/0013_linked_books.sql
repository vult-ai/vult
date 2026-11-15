-- Linked Books replaces the single link between a business Book and a
-- personal Book: any two Books of the same owner pay costs for each other.
-- The links and pairs recorded before are carried over as they were: the
-- personal Book pays for the business Book, by the link's mode, with the
-- same accounts.

-- Keep the earlier rows while their tables are replaced.
ALTER TABLE public.book_links NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.proposal_pairs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.entry_pairs NO FORCE ROW LEVEL SECURITY;
CREATE TEMP TABLE earlier_links AS SELECT * FROM public.book_links;
CREATE TEMP TABLE earlier_proposal_pairs AS SELECT * FROM public.proposal_pairs;
CREATE TEMP TABLE earlier_entry_pairs AS SELECT * FROM public.entry_pairs;
DROP TABLE public.entry_pairs, public.proposal_pairs, public.book_links;
DROP FUNCTION public.book_link_guard(), public.book_link_for(uuid, uuid), public.pair_proposal(uuid, uuid, uuid),
    public.proposal_pair_of(uuid, uuid, uuid), public.entry_pair_of(uuid, uuid, uuid), public.book_link_owner(uuid);

-- Linked Books: two Books of the same owner pay costs for each other. A cost
-- one Book pays for the other is recorded in both as one pair, decided and
-- undone as one; the money owed between them, or put in, is kept on the
-- accounts each direction's terms name. Each Book stays balanced on its own;
-- classification is by account type and role, never by name.

-- A link between two Books of one tenant (book_a < book_b).
CREATE TABLE public.book_links (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    book_a uuid NOT NULL,
    book_b uuid NOT NULL,
    active boolean NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, book_a, book_b),
    CHECK (book_a < book_b),
    FOREIGN KEY (tenant_id, book_a) REFERENCES public.books(tenant_id, id),
    FOREIGN KEY (tenant_id, book_b) REFERENCES public.books(tenant_id, id)
);

-- How a cost the payer Book pays for the beneficiary Book is recorded: the
-- treatment, and the account on each side.
CREATE TABLE public.book_link_terms (
    tenant_id uuid NOT NULL,
    link_id uuid NOT NULL,
    payer_book_id uuid NOT NULL,
    beneficiary_book_id uuid NOT NULL,
    treatment text NOT NULL CHECK (treatment IN ('contribution', 'owed', 'owner_equity')),
    payer_account_id uuid NOT NULL,
    beneficiary_account_id uuid NOT NULL,
    active boolean NOT NULL,
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, link_id, payer_book_id),
    UNIQUE (tenant_id, payer_book_id, beneficiary_book_id),
    CHECK (payer_book_id <> beneficiary_book_id),
    FOREIGN KEY (tenant_id, link_id) REFERENCES public.book_links(tenant_id, id),
    FOREIGN KEY (tenant_id, payer_book_id, payer_account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, beneficiary_book_id, beneficiary_account_id) REFERENCES public.accounts(tenant_id, book_id, id)
);

-- Two waiting proposals, one in each linked Book, decided as one.
CREATE TABLE public.proposal_pairs (
    tenant_id uuid NOT NULL,
    pair_key text NOT NULL CHECK (pair_key ~ '^[A-Za-z0-9_-]{1,64}$'),
    kind text NOT NULL DEFAULT 'cost' CHECK (kind IN ('cost', 'settlement')),
    payer_book_id uuid NOT NULL,
    payer_proposal_id uuid NOT NULL,
    beneficiary_book_id uuid NOT NULL,
    beneficiary_proposal_id uuid NOT NULL,
    transaction_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, beneficiary_book_id, beneficiary_proposal_id),
    UNIQUE (tenant_id, payer_book_id, payer_proposal_id),
    FOREIGN KEY (tenant_id, payer_book_id, payer_proposal_id) REFERENCES public.operation_proposals(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, beneficiary_book_id, beneficiary_proposal_id) REFERENCES public.operation_proposals(tenant_id, book_id, id)
);

-- The two entries a decided pair posted.
CREATE TABLE public.entry_pairs (
    tenant_id uuid NOT NULL,
    pair_key text NOT NULL,
    kind text NOT NULL DEFAULT 'cost' CHECK (kind IN ('cost', 'settlement')),
    payer_book_id uuid NOT NULL,
    payer_entry_id uuid NOT NULL,
    beneficiary_book_id uuid NOT NULL,
    beneficiary_entry_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, beneficiary_book_id, beneficiary_entry_id),
    UNIQUE (tenant_id, payer_book_id, payer_entry_id)
);

-- The owner may change a link: a recent owner sign-in acting directly, or an
-- operator import.
CREATE FUNCTION public.book_link_owner(t uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SET search_path = pg_catalog AS $$
SELECT public.ledger_import_owner()
    OR coalesce(current_setting('vult.connection_id', true), '') = ''
        AND coalesce(current_setting('vult.owner_posting', true), '') = ''
        AND access.owner_write_allowed(t)
        AND coalesce(access.owner_identity(t)::text = current_setting('vult.actor_id', true), false)
$$;

-- A Book's profile is its latest settings revision.
CREATE FUNCTION public.book_profile(t uuid, b uuid) RETURNS text
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT r.profile FROM public.book_revisions r WHERE r.tenant_id = t AND r.book_id = b ORDER BY r.revision DESC LIMIT 1
$$;

CREATE FUNCTION public.book_link_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END) THEN
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
CREATE TRIGGER book_link_guard BEFORE INSERT OR UPDATE OR DELETE ON public.book_links
    FOR EACH ROW EXECUTE FUNCTION public.book_link_guard();

-- Each direction's treatment and accounts, checked by class and role:
--   personal -> business: contribution (asset role investment / equity), or owed;
--   business -> business: owed (asset role receivable / liability role payable);
--   business -> personal: owed, or owner_equity (equity / asset role investment).
-- An account carries one direction of one link only, so each balance is one link's.
CREATE FUNCTION public.book_link_terms_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE lk public.book_links%ROWTYPE; pa public.accounts%ROWTYPE; ba public.accounts%ROWTYPE; pp text; bp text;
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END) THEN
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
CREATE TRIGGER book_link_terms_guard BEFORE INSERT OR UPDATE OR DELETE ON public.book_link_terms
    FOR EACH ROW EXECUTE FUNCTION public.book_link_terms_guard();

-- Book b's links, from b's side, for the owner or any connection of b.
CREATE FUNCTION public.book_links_for(t uuid, b uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE coalesce((
    SELECT jsonb_agg(jsonb_build_object('link_id', l.id, 'active', l.active, 'revision', l.revision,
        'other', (SELECT jsonb_build_object('id', k.id, 'name', k.name, 'profile', public.book_profile(t, k.id)) FROM public.books k
            WHERE k.tenant_id = t AND k.id = CASE WHEN l.book_a = b THEN l.book_b ELSE l.book_a END),
        'pays_for_them', (SELECT jsonb_build_object('treatment', x.treatment, 'my_account_id', x.payer_account_id, 'their_account_id', x.beneficiary_account_id, 'active', x.active)
            FROM public.book_link_terms x WHERE x.tenant_id = t AND x.link_id = l.id AND x.payer_book_id = b),
        'they_pay_for_me', (SELECT jsonb_build_object('treatment', x.treatment, 'my_account_id', x.beneficiary_account_id, 'their_account_id', x.payer_account_id, 'active', x.active)
            FROM public.book_link_terms x WHERE x.tenant_id = t AND x.link_id = l.id AND x.beneficiary_book_id = b)) ORDER BY l.created_at, l.id)
    FROM public.book_links l WHERE l.tenant_id = t AND (l.book_a = b OR l.book_b = b)), '[]'::jsonb) END
$$;

-- The active terms for a cost paid by Book p for Book q, if any.
CREATE FUNCTION public.active_link_terms(t uuid, p uuid, q uuid) RETURNS public.book_link_terms
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT x.* FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id
WHERE x.tenant_id = t AND x.payer_book_id = p AND x.beneficiary_book_id = q AND x.active AND l.active
$$;

-- Joins the beneficiary's half of a pair, just prepared in Book b, to its
-- waiting payer half, after checking both against the terms and the bank
-- item. A payer half only checks its own side and waits. Refusals say why.
CREATE FUNCTION public.pair_proposal(t uuid, b uuid, p uuid) RETURNS void
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

-- The pair a proposal belongs to, for reads in either Book: the partner's
-- Book, proposal and side, and whether its link is still on. A payer half
-- not yet joined names the Book its account's link pays for.
CREATE FUNCTION public.proposal_pair_of(t uuid, b uuid, p uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE coalesce((
    SELECT jsonb_build_object('state', 'paired', 'pair_key', x.pair_key, 'transaction_id', x.transaction_id, 'kind', x.kind,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'proposal_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_proposal_id ELSE x.payer_proposal_id END,
        'side', CASE WHEN x.payer_book_id = b THEN 'beneficiary' ELSE 'payer' END,
        -- A settlement's debtor is the owed direction's beneficiary.
        'link_active', CASE WHEN x.kind = 'settlement' THEN (public.active_link_terms(t, x.beneficiary_book_id, x.payer_book_id)).link_id
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
    LIMIT 1)) END
$$;

-- The cost pairs a settlement repays. A settlement is live until its
-- debtor's entry is reversed; while it is, the costs it names are repaid.
CREATE TABLE public.settlement_repays (
    tenant_id uuid NOT NULL,
    settlement_book_id uuid NOT NULL,
    settlement_entry_id uuid NOT NULL,
    cost_book_id uuid NOT NULL,
    cost_entry_id uuid NOT NULL,
    PRIMARY KEY (tenant_id, cost_book_id, cost_entry_id, settlement_entry_id),
    FOREIGN KEY (tenant_id, settlement_book_id, settlement_entry_id) REFERENCES public.entry_pairs(tenant_id, payer_book_id, payer_entry_id),
    FOREIGN KEY (tenant_id, cost_book_id, cost_entry_id) REFERENCES public.entry_pairs(tenant_id, beneficiary_book_id, beneficiary_entry_id)
);

-- Whether entry e of Book b has been reversed.
CREATE FUNCTION public.entry_reversed(t uuid, b uuid, e uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT EXISTS (SELECT 1 FROM public.entries r WHERE r.tenant_id = t AND r.book_id = b AND r.reversal_of = e)
$$;

-- The live settlement repaying a cost pair, named by the cost's entry in
-- the Book it was paid for; other_than leaves one settlement out.
CREATE FUNCTION public.cost_repaid_by(t uuid, b uuid, e uuid, other_than uuid) RETURNS text
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT p.pair_key FROM public.settlement_repays r
    JOIN public.entry_pairs p ON p.tenant_id = r.tenant_id AND p.payer_book_id = r.settlement_book_id AND p.payer_entry_id = r.settlement_entry_id
WHERE r.tenant_id = t AND r.cost_book_id = b AND r.cost_entry_id = e AND r.settlement_entry_id IS DISTINCT FROM other_than
    AND NOT public.entry_reversed(t, r.settlement_book_id, r.settlement_entry_id)
LIMIT 1
$$;

-- An amount in a Book's own units, for refusals.
CREATE FUNCTION public.book_amount(t uuid, b uuid, minor bigint) RETURNS text
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT round(minor::numeric / power(10::numeric, k.scale), k.scale::int)::text || ' ' || k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = b
$$;

-- Checks a settlement: the debtor's bank item paying out and the
-- creditor's taking in the same amount, at most five days apart, between
-- the two accounts an owed direction names, for no more than is owed. The
-- costs it names (repays) must be open cost pairs from the creditor to the
-- debtor adding up to exactly the amount; a business repaying its owner's
-- personal Book names them always. leaving_out is the settlement's own
-- entry once posted. Returns the cost pairs' entries in the debtor's Book.
CREATE FUNCTION public.settlement_check(t uuid, debtor uuid, debtor_account uuid, debtor_item uuid, creditor uuid, creditor_account uuid, creditor_item uuid,
        repays jsonb, leaving_out uuid) RETURNS uuid[]
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.bank_transactions%ROWTYPE; c public.bank_transactions%ROWTYPE; dclass text; cclass text; owed bigint; total bigint := 0;
    k text; entry uuid; amount bigint; costs uuid[] := '{}';
BEGIN
    SELECT * INTO d FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = debtor AND x.id = debtor_item;
    SELECT * INTO c FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = creditor AND x.id = creditor_item;
    IF d.id IS NULL OR c.id IS NULL THEN
        RAISE EXCEPTION 'A settlement is two bank items, one in each linked Book.' USING ERRCODE = 'VP020';
    END IF;
    dclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = debtor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = debtor AND y.id = d.bank_account_id), d.account_id));
    cclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = creditor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = creditor AND y.id = c.bank_account_id), c.account_id));
    IF NOT (dclass = 'asset' AND d.amount_minor < 0 OR dclass = 'liability' AND d.amount_minor > 0)
        OR NOT (cclass = 'asset' AND c.amount_minor > 0 OR cclass = 'liability' AND c.amount_minor < 0) THEN
        RAISE EXCEPTION 'A settlement is money paid out of the Book that owes and into the Book that is owed.' USING ERRCODE = 'VP020';
    END IF;
    amount := abs(d.amount_minor);
    IF abs(c.amount_minor) <> amount OR c.currency IS DISTINCT FROM d.currency OR abs(c.occurred_on - d.occurred_on) > 5 THEN
        RAISE EXCEPTION 'A settlement''s two bank items are the same amount, at most five days apart.' USING ERRCODE = 'VP020';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed' AND x.payer_book_id = creditor AND x.payer_account_id = creditor_account
                AND x.beneficiary_book_id = debtor AND x.beneficiary_account_id = debtor_account) THEN
        RAISE EXCEPTION 'A settlement pays what one Book owes the other: to the accounts an owed direction of an active link names.' USING ERRCODE = 'VP020';
    END IF;
    SELECT coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO owed
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.account_id = debtor_account AND x.entry_id IS DISTINCT FROM leaving_out;
    IF amount > owed THEN
        RAISE EXCEPTION 'This pays more than is owed (%, of which % is owed).', public.book_amount(t, debtor, amount), public.book_amount(t, debtor, greatest(owed, 0)) USING ERRCODE = 'VP020';
    END IF;
    IF jsonb_typeof(coalesce(repays, '[]'::jsonb)) <> 'array' THEN
        RAISE EXCEPTION 'repays is a list of pair keys.' USING ERRCODE = 'VP020';
    END IF;
    FOR k IN SELECT jsonb_array_elements_text(coalesce(repays, '[]'::jsonb)) LOOP
        SELECT p.beneficiary_entry_id INTO entry FROM public.entry_pairs p
            WHERE p.tenant_id = t AND p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor AND p.pair_key = k
                AND NOT public.entry_reversed(t, debtor, p.beneficiary_entry_id);
        IF entry IS NULL THEN
            RAISE EXCEPTION 'repays names %, which isn''t a recorded cost % paid for this Book.', k, (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = creditor) USING ERRCODE = 'VP020';
        END IF;
        IF entry = ANY (costs) THEN
            RAISE EXCEPTION 'repays names % twice.', k USING ERRCODE = 'VP020';
        END IF;
        IF public.cost_repaid_by(t, debtor, entry, leaving_out) IS NOT NULL THEN
            RAISE EXCEPTION 'The cost % is already repaid.', k USING ERRCODE = 'VP020';
        END IF;
        costs := costs || entry;
        total := total + (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = entry AND x.side = 'credit');
    END LOOP;
    IF cardinality(costs) = 0 AND public.book_profile(t, creditor) = 'personal' AND public.book_profile(t, debtor) = 'business' THEN
        RAISE EXCEPTION 'A business repaying its owner names the costs it repays (repays), adding up to the payment.' USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) > 0 AND total <> amount THEN
        RAISE EXCEPTION 'The costs named add up to %, not the payment''s %.', public.book_amount(t, debtor, total), public.book_amount(t, debtor, amount) USING ERRCODE = 'VP020';
    END IF;
    RETURN costs;
END $$;

-- Records a settlement the owner decided, or recorded at once: both
-- entries each record their own bank item to its term account, and the
-- costs it repays are kept.
CREATE FUNCTION public.settle_pair(t uuid, key text, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid, repays jsonb) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE ditem uuid; dbank uuid; dacc uuid; citem uuid; cbank uuid; cacc uuid; costs uuid[]; e uuid;
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    SELECT l.transaction_id, l.account_id INTO ditem, dbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = debtor AND l.entry_id = debtor_entry AND l.revoked_at IS NULL;
    SELECT l.transaction_id, l.account_id INTO citem, cbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = creditor AND l.entry_id = creditor_entry AND l.revoked_at IS NULL;
    SELECT min(x.account_id::text)::uuid INTO dacc FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry AND x.account_id <> dbank;
    SELECT min(x.account_id::text)::uuid INTO cacc FROM public.lines x WHERE x.tenant_id = t AND x.book_id = creditor AND x.entry_id = creditor_entry AND x.account_id <> cbank;
    IF ditem IS NULL OR citem IS NULL
        OR (SELECT count(*) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry) <> 2
        OR (SELECT count(*) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = creditor AND x.entry_id = creditor_entry) <> 2
        OR EXISTS (SELECT 1 FROM public.entries x JOIN public.bank_transactions i ON i.tenant_id = x.tenant_id AND i.book_id = x.book_id
                AND i.id = CASE WHEN x.book_id = debtor THEN ditem ELSE citem END
            WHERE x.tenant_id = t AND (x.book_id = debtor AND x.id = debtor_entry OR x.book_id = creditor AND x.id = creditor_entry)
                AND (x.accounting_date <> i.occurred_on OR x.reversal_of IS NOT NULL)) THEN
        RAISE EXCEPTION 'Each entry of a settlement records its own bank item, on its date, to one account.' USING ERRCODE = 'VP020';
    END IF;
    costs := public.settlement_check(t, debtor, dacc, ditem, creditor, cacc, citem, repays, debtor_entry);
    PERFORM set_config('vult.settling', key, true);
    INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id)
        VALUES (t, key, 'settlement', debtor, debtor_entry, creditor, creditor_entry);
    PERFORM set_config('vult.settling', '', true);
    FOREACH e IN ARRAY costs LOOP
        INSERT INTO public.settlement_repays(tenant_id, settlement_book_id, settlement_entry_id, cost_book_id, cost_entry_id) VALUES (t, debtor, debtor_entry, debtor, e);
    END LOOP;
END $$;

-- Joins the halves of a settlement prepared as proposals: the first waits,
-- the second, in the other Book, joins it once both are checked.
CREATE FUNCTION public.pair_settlement(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; args jsonb; oargs jsonb; key text;
    debtor uuid; creditor uuid; dp uuid; cp uuid; dargs jsonb; cargs jsonb;
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
    PERFORM public.settlement_check(t, debtor, (dargs ->> 'other_account_id')::uuid, (dargs ->> 'transaction_id')::uuid,
        creditor, (cargs ->> 'other_account_id')::uuid, (cargs ->> 'transaction_id')::uuid, args -> 'repays', NULL);
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, kind, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id)
        VALUES (t, key, 'settlement', debtor, dp, creditor, cp, (dargs ->> 'transaction_id')::uuid);
END $$;

-- Who owes whom: for each link with an owed direction, the net between the
-- two Books from the term accounts on both sides, whether both sides agree
-- (and the entries only one side has), and the cost pairs along it. Each
-- Book's other liabilities follow, cards and loans included. The owner
-- reads every link (b NULL); a connection of Book b reads b's links and b's
-- own liabilities.
CREATE FUNCTION public.linked_balances(t uuid, b uuid) RETURNS jsonb
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
                'pair_key', p.pair_key, 'payer_book_id', p.payer_book_id, 'beneficiary_book_id', p.beneficiary_book_id,
                'date', e.accounting_date, 'description', e.description,
                'amount_minor', (SELECT sum(z.amount_minor) FROM public.lines z WHERE z.tenant_id = t AND z.book_id = p.beneficiary_book_id AND z.entry_id = p.beneficiary_entry_id AND z.side = 'credit')::text,
                'repaid', public.cost_repaid_by(t, p.beneficiary_book_id, p.beneficiary_entry_id, NULL) IS NOT NULL,
                'has_receipt', EXISTS (SELECT 1 FROM public.evidence_links v WHERE v.tenant_id = t
                        AND (v.book_id = p.beneficiary_book_id AND v.entry_id = p.beneficiary_entry_id OR v.book_id = p.payer_book_id AND v.entry_id = p.payer_entry_id)
                        AND v.action = 'attach' AND v.role IN ('receipt', 'invoice')
                        AND NOT EXISTS (SELECT 1 FROM public.evidence_links w WHERE w.tenant_id = v.tenant_id AND w.book_id = v.book_id AND w.link_id = v.link_id AND w.revision > v.revision))
                    OR EXISTS (SELECT 1 FROM public.source_citations c WHERE c.tenant_id = t
                        AND (c.book_id = p.beneficiary_book_id AND c.entry_id = p.beneficiary_entry_id OR c.book_id = p.payer_book_id AND c.entry_id = p.payer_entry_id)))
                ORDER BY e.accounting_date, p.pair_key)
            FROM public.entry_pairs p JOIN dirs d ON d.link_id = n.link_id AND d.creditor = p.payer_book_id AND d.debtor = p.beneficiary_book_id
                JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
            WHERE p.tenant_id = t AND p.kind = 'cost' AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)), '[]'::jsonb))
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

-- The owner answers and closes notes agents wrote: only a new note, or a
-- profile change, must carry the writer's own actor.
CREATE OR REPLACE FUNCTION public.agent_note_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE owner boolean; open_loops integer;
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'notes are closed or replaced, never deleted' USING ERRCODE = '23514';
    END IF;
    -- Closing a loop because its item was recorded or its question settled
    -- happens with whoever did that; it changes nothing else.
    IF TG_TABLE_NAME = 'agent_loops' AND TG_OP = 'UPDATE' AND coalesce(current_setting('vult.loop_autoclose', true), '') = 'on' THEN
        IF OLD.status = 'open' AND NEW.status = 'done' AND NEW.closed_reason IN ('The item was recorded.', 'The question was answered.', 'The question was withdrawn.')
            AND (to_jsonb(NEW) - ARRAY['status', 'closed_at', 'closed_reason']) = (to_jsonb(OLD) - ARRAY['status', 'closed_at', 'closed_reason']) THEN
            RETURN NEW;
        END IF;
    END IF;
    owner := public.agent_note_owner(NEW.tenant_id, NEW.book_id);
    IF TG_TABLE_NAME = 'agent_seen' THEN
        -- Any signed-in visit counts; no fresh sign-in is needed to look.
        IF NOT (public.ledger_context(NEW.tenant_id, NEW.book_id) AND coalesce(current_setting('vult.connection_id', true), '') = ''
                AND access.owner_allowed(NEW.tenant_id)) OR NEW.person::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'a person records only when they last looked' USING ERRCODE = '42501';
        END IF;
        NEW.seen_at := clock_timestamp();
        RETURN NEW;
    END IF;
    IF NOT public.agent_note_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the Book''s agents and its owner keep these notes' USING ERRCODE = '42501';
    END IF;
    -- A note's actor is whoever wrote it; the owner answers, withdraws or
    -- closes notes agents wrote, and a profile names its last editor.
    IF (TG_OP = 'INSERT' OR TG_TABLE_NAME = 'agent_profile') AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF TG_TABLE_NAME = 'agent_profile' THEN
        IF TG_OP = 'UPDATE' AND NEW.revision <> OLD.revision + 1 OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'the profile changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' THEN
        NEW.connection_id := nullif(current_setting('vult.connection_id', true), '')::uuid;
        IF TG_TABLE_NAME = 'agent_questions' THEN
            IF NEW.status <> 'open' AND NOT owner OR NEW.answer IS NOT NULL AND NOT owner THEN
                RAISE EXCEPTION 'only the owner answers a question' USING ERRCODE = '42501';
            END IF;
            NEW.asked_at := clock_timestamp();
        ELSE
            IF NEW.status <> 'open' THEN
                RAISE EXCEPTION 'a loop opens open' USING ERRCODE = '23514';
            END IF;
            SELECT count(*) INTO open_loops FROM public.agent_loops WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND status = 'open';
            IF open_loops >= 50 THEN
                RAISE EXCEPTION 'this Book has 50 open loops; close some first' USING ERRCODE = '23514';
            END IF;
            NEW.created_at := clock_timestamp();
        END IF;
        RETURN NEW;
    END IF;
    -- Updates: a question is answered by the owner, withdrawn by its asker
    -- or the owner, or superseded by the owner; a loop is closed once.
    IF TG_TABLE_NAME = 'agent_questions' THEN
        IF (to_jsonb(NEW) - ARRAY['answer', 'status', 'answered_at', 'answered_by', 'reason']) IS DISTINCT FROM
            (to_jsonb(OLD) - ARRAY['answer', 'status', 'answered_at', 'answered_by', 'reason'])
            OR NOT (OLD.status = 'open' AND NEW.status = 'answered' AND owner
                OR OLD.status = 'open' AND NEW.status = 'withdrawn' AND (owner OR OLD.connection_id::text = current_setting('vult.connection_id', true))
                OR OLD.status = 'answered' AND NEW.status = 'superseded' AND owner) THEN
            RAISE EXCEPTION 'a question is answered or replaced by the owner, or withdrawn by whoever asked it' USING ERRCODE = '42501';
        END IF;
        IF NEW.status = 'answered' THEN
            NEW.answered_at := clock_timestamp();
            NEW.answered_by := current_setting('vult.actor_id', true)::uuid;
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status <> 'open' OR NEW.status = 'open'
        OR (to_jsonb(NEW) - ARRAY['status', 'closed_at', 'closed_reason']) IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['status', 'closed_at', 'closed_reason']) THEN
        RAISE EXCEPTION 'a loop is closed once, with a reason' USING ERRCODE = '23514';
    END IF;
    NEW.closed_at := clock_timestamp();
    RETURN NEW;
END $$;

-- Questions about which Book a charge belongs to, and which costs a
-- payment repays, offer the owner choices; the answer is one of them.
ALTER TABLE public.agent_questions DROP CONSTRAINT agent_questions_subject_kind_check;
ALTER TABLE public.agent_questions ADD CONSTRAINT agent_questions_subject_kind_check
    CHECK (subject_kind IN ('bank_item', 'kind', 'goal', 'preference', 'account', 'other', 'entity', 'settlement'));
ALTER TABLE public.agent_questions ADD COLUMN choices jsonb CHECK (choices IS NULL OR jsonb_typeof(choices) = 'array'
    AND jsonb_array_length(choices) BETWEEN 2 AND 8 AND octet_length(choices::text) <= 4096);

CREATE FUNCTION public.agent_question_choices() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.subject_kind IN ('entity', 'settlement') AND NEW.choices IS NULL THEN
        RAISE EXCEPTION 'A question about an entity or a settlement offers choices.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.choices IS NOT NULL AND EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.choices) c
            WHERE jsonb_typeof(c) <> 'object' OR jsonb_typeof(c -> 'value') <> 'string' OR length(c ->> 'value') NOT BETWEEN 1 AND 200
                OR jsonb_typeof(c -> 'label') <> 'string' OR length(c ->> 'label') NOT BETWEEN 1 AND 200
                OR (c - 'value' - 'label') <> '{}'::jsonb)
        OR NEW.choices IS NOT NULL AND (SELECT count(DISTINCT c ->> 'value') FROM jsonb_array_elements(NEW.choices) c) <> jsonb_array_length(NEW.choices) THEN
        RAISE EXCEPTION 'Choices are {value, label} pairs with different values.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.choices IS NOT NULL AND NEW.answer IS NOT NULL AND (jsonb_typeof(NEW.answer) <> 'object' OR (NEW.answer - 'value' - 'make_rule') <> '{}'::jsonb
            OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.choices) c WHERE c ->> 'value' = NEW.answer ->> 'value')
            OR NEW.answer ? 'make_rule' AND jsonb_typeof(NEW.answer -> 'make_rule') <> 'boolean') THEN
        RAISE EXCEPTION 'The answer is one of the question''s choices.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER agent_question_choices BEFORE INSERT OR UPDATE ON public.agent_questions
    FOR EACH ROW EXECUTE FUNCTION public.agent_question_choices();

-- Where the owner said charges go: one bank item, or every charge from a
-- counterparty (on one bank account, or any). The target is a linked Book
-- or personal (this Book itself).
CREATE TABLE public.assignment_rules (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    transaction_id uuid,
    bank_account_id uuid,
    counterparty_key text CHECK (counterparty_key IS NULL OR length(counterparty_key) BETWEEN 1 AND 200),
    target text NOT NULL CHECK (target = 'personal' OR target ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    source text NOT NULL CHECK (source IN ('owner', 'answer', 'reassigned')),
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    CHECK ((transaction_id IS NULL) <> (counterparty_key IS NULL)),
    CHECK (transaction_id IS NULL OR bank_account_id IS NULL),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);
CREATE UNIQUE INDEX assignment_rules_item ON public.assignment_rules(tenant_id, book_id, transaction_id) WHERE transaction_id IS NOT NULL;
CREATE UNIQUE INDEX assignment_rules_party ON public.assignment_rules(tenant_id, book_id, coalesce(bank_account_id, '00000000-0000-0000-0000-000000000000'::uuid), counterparty_key)
    WHERE counterparty_key IS NOT NULL;

CREATE FUNCTION public.assignment_rule_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END) THEN
        RAISE EXCEPTION 'only the owner makes assignment rules' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'an assignment rule is replaced, not changed' USING ERRCODE = '23514';
    END IF;
    IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF NEW.target <> 'personal' AND NOT EXISTS (SELECT 1 FROM public.book_links l WHERE l.tenant_id = NEW.tenant_id
            AND (l.book_a = NEW.book_id AND l.book_b = NEW.target::uuid OR l.book_b = NEW.book_id AND l.book_a = NEW.target::uuid)) THEN
        RAISE EXCEPTION 'Charges go to this Book or to a Book linked to it.' USING ERRCODE = 'VP020';
    END IF;
    -- The counterparty is kept as the key Vult forms from it, as kind_key gives it.
    IF NEW.counterparty_key IS NOT NULL THEN
        NEW.counterparty_key := left(public.rule_key(NEW.counterparty_key), 200);
        IF NEW.counterparty_key = '' THEN
            RAISE EXCEPTION 'A rule for a counterparty names one.' USING ERRCODE = 'VP020';
        END IF;
    END IF;
    NEW.created_at := clock_timestamp();
    RETURN NEW;
END $$;
CREATE TRIGGER assignment_rule_guard BEFORE INSERT OR UPDATE OR DELETE ON public.assignment_rules
    FOR EACH ROW EXECUTE FUNCTION public.assignment_rule_guard();

-- A rejection may say which Book the cost belongs to instead.
ALTER TABLE public.operation_decisions ADD COLUMN reassigned_to text
    CHECK (reassigned_to IS NULL OR decision = 'reject' AND (reassigned_to = 'personal'
        OR reassigned_to ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'));

-- A cost pair is checked again when its two entries are recorded together,
-- however they were made: the owner's approval of two proposals, or the
-- owner recording both at once. The payer's entry records its bank item to
-- the term account; the beneficiary's credits its term account with the
-- whole amount and debits only expenses or assets that aren't money.
CREATE FUNCTION public.entry_pair_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE terms public.book_link_terms%ROWTYPE; item public.bank_transactions%ROWTYPE; bank_account uuid; tid uuid;
    paid text; amount bigint; n int; bad int;
BEGIN
    IF NOT (access.owner_allowed(NEW.tenant_id) AND access.owner_write_allowed(NEW.tenant_id)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    IF NEW.kind = 'settlement' THEN
        -- Settlements are checked and recorded by settle_pair only.
        IF current_setting('vult.settling', true) IS DISTINCT FROM NEW.pair_key THEN
            RAISE EXCEPTION 'a settlement is recorded with settle_pair' USING ERRCODE = '42501';
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
CREATE TRIGGER entry_pair_guard BEFORE INSERT ON public.entry_pairs
    FOR EACH ROW EXECUTE FUNCTION public.entry_pair_guard();

-- For a bank item paid out of Book b, the linked Books b pays for and the
-- account each used before for the same counterparty: in its own bank
-- activity, or in an earlier pair paid from b. Read with the caller's own
-- rights, so a connection sees only Books it can read.
CREATE FUNCTION public.suggested_beneficiaries(t uuid, b uuid, i uuid) RETURNS jsonb
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
WITH item AS (
    SELECT x.*, left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200) AS party,
        (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = b
            AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = b AND y.id = x.bank_account_id), x.account_id)) AS paid
    FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = b AND x.id = i
), used AS (
    -- The beneficiary's own items from the same counterparty, recorded with two lines.
    SELECT o.book_id, o2.account_id, x.occurred_on AS seen
    FROM item, public.book_link_terms k JOIN public.book_links lk ON lk.tenant_id = k.tenant_id AND lk.id = k.link_id AND lk.active
        JOIN public.bank_transactions x ON x.tenant_id = k.tenant_id AND x.book_id = k.beneficiary_book_id
        JOIN public.bank_transaction_links o ON o.tenant_id = x.tenant_id AND o.book_id = x.book_id AND o.transaction_id = x.id AND o.revoked_at IS NULL
        JOIN public.lines o2 ON o2.tenant_id = o.tenant_id AND o2.book_id = o.book_id AND o2.entry_id = o.entry_id AND o2.account_id <> o.account_id
    WHERE k.tenant_id = t AND k.payer_book_id = b AND k.active
        AND left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200) = item.party
        AND (SELECT count(*) FROM public.lines z WHERE z.tenant_id = o.tenant_id AND z.book_id = o.book_id AND z.entry_id = o.entry_id) = 2
    UNION ALL
    -- Earlier pairs paid from this Book for the same counterparty.
    SELECT p.beneficiary_book_id, d.account_id, x.occurred_on
    FROM item, public.entry_pairs p
        JOIN public.bank_transaction_links o ON o.tenant_id = p.tenant_id AND o.book_id = p.payer_book_id AND o.entry_id = p.payer_entry_id AND o.revoked_at IS NULL
        JOIN public.bank_transactions x ON x.tenant_id = o.tenant_id AND x.book_id = o.book_id AND x.id = o.transaction_id
        JOIN public.lines d ON d.tenant_id = p.tenant_id AND d.book_id = p.beneficiary_book_id AND d.entry_id = p.beneficiary_entry_id AND d.side = 'debit'
    WHERE p.tenant_id = t AND p.payer_book_id = b AND p.kind = 'cost'
        AND (public.active_link_terms(t, b, p.beneficiary_book_id)).link_id IS NOT NULL
        AND left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200) = item.party
        AND NOT EXISTS (SELECT 1 FROM public.entries r WHERE r.tenant_id = p.tenant_id AND r.book_id = p.beneficiary_book_id AND r.reversal_of = p.beneficiary_entry_id)
), best AS (
    SELECT DISTINCT ON (u.book_id) u.book_id, u.account_id, u.seen FROM used u ORDER BY u.book_id, u.seen DESC, u.account_id
)
SELECT coalesce(jsonb_agg(jsonb_build_object('book_id', s.book_id, 'account_id', s.account_id,
        'why', k.name || ' recorded ' || coalesce(nullif((SELECT x.counterparty FROM item x), ''), (SELECT x.description FROM item x)) || ' to ' || a.name || ' on ' || s.seen)
        ORDER BY s.seen DESC, s.book_id), '[]'::jsonb)
FROM best s JOIN public.books k ON k.tenant_id = t AND k.id = s.book_id
    JOIN public.accounts a ON a.tenant_id = t AND a.book_id = s.book_id AND a.id = s.account_id
WHERE (SELECT paid = 'liability' AND amount_minor > 0 OR paid = 'asset' AND amount_minor < 0 FROM item)
    AND (a.class = 'expense' OR a.class = 'asset' AND coalesce(a.role, '') NOT IN ('cash', 'card'))
$$;

-- The other entry of a decided pair, for the owner or a connection of
-- Book b: its Book, entry and side.
CREATE FUNCTION public.entry_pair_of(t uuid, b uuid, e uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE (
    SELECT jsonb_build_object('pair_key', x.pair_key, 'kind', x.kind,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'entry_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_entry_id ELSE x.payer_entry_id END,
        'side', CASE WHEN x.payer_book_id = b THEN 'beneficiary' ELSE 'payer' END,
        'repaid_by', CASE WHEN x.kind = 'cost' THEN public.cost_repaid_by(t, x.beneficiary_book_id, x.beneficiary_entry_id, NULL) END)
    FROM public.entry_pairs x WHERE x.tenant_id = t
        AND (x.payer_book_id = b AND x.payer_entry_id = e OR x.beneficiary_book_id = b AND x.beneficiary_entry_id = e)) END
$$;

-- Undoing half of a pair reverses its partner too. That partner's reversal
-- follows the owner's decision on the other half; it isn't a correction of
-- the agent's kind of activity.
CREATE OR REPLACE FUNCTION public.agent_kind_undone() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE l record;
BEGIN
    IF coalesce(current_setting('vult.pair_partner', true), '') = 'on' THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = NEW.actor_id) THEN
        RETURN NULL;
    END IF;
    IF TG_TABLE_NAME = 'entries' THEN
        FOR l IN SELECT DISTINCT x.transaction_id FROM public.bank_transaction_links x
                WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.book_id AND x.entry_id = NEW.reversal_of
                AND EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = x.actor_id) LOOP
            PERFORM public.agent_kind_note(NEW.tenant_id, NEW.book_id, l.transaction_id, 'undone',
                public.agent_other_account(NEW.tenant_id, NEW.book_id, l.transaction_id, '{}'::jsonb, NEW.reversal_of));
        END LOOP;
    END IF;
    RETURN NULL;
END $$;

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['book_links', 'book_link_terms', 'proposal_pairs', 'entry_pairs', 'settlement_repays', 'assignment_rules'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
    END LOOP;
END $$;
CREATE POLICY link_read ON public.book_links FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_a, false) OR public.ledger_allowed(tenant_id, book_b, false));
CREATE POLICY link_read ON public.book_link_terms FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, payer_book_id, false) OR public.ledger_allowed(tenant_id, beneficiary_book_id, false));
CREATE POLICY pair_read ON public.proposal_pairs FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, payer_book_id, false) OR public.ledger_allowed(tenant_id, beneficiary_book_id, false));
CREATE POLICY rule_read ON public.assignment_rules FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY owner_write ON public.assignment_rules FOR INSERT WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_delete ON public.assignment_rules FOR DELETE USING (public.book_link_owner(tenant_id));
CREATE POLICY repays_read ON public.settlement_repays FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, settlement_book_id, false));
CREATE POLICY pair_read ON public.entry_pairs FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, payer_book_id, false) OR public.ledger_allowed(tenant_id, beneficiary_book_id, false));
CREATE POLICY owner_write ON public.book_links FOR INSERT WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_update ON public.book_links FOR UPDATE USING (public.book_link_owner(tenant_id)) WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_delete ON public.book_links FOR DELETE USING (public.book_link_owner(tenant_id));
CREATE POLICY owner_write ON public.book_link_terms FOR INSERT WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_update ON public.book_link_terms FOR UPDATE USING (public.book_link_owner(tenant_id)) WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_delete ON public.book_link_terms FOR DELETE USING (public.book_link_owner(tenant_id));
-- The owner's decision, or the owner recording both at once, posts both
-- entries of a pair and records them here.
CREATE POLICY owner_pairs ON public.entry_pairs FOR INSERT WITH CHECK (access.owner_allowed(tenant_id) AND access.owner_write_allowed(tenant_id));

REVOKE ALL ON FUNCTION public.book_link_owner(uuid), public.book_profile(uuid, uuid), public.book_link_guard(), public.book_link_terms_guard(),
    public.book_links_for(uuid, uuid), public.active_link_terms(uuid, uuid, uuid), public.pair_proposal(uuid, uuid, uuid),
    public.proposal_pair_of(uuid, uuid, uuid), public.entry_pair_of(uuid, uuid, uuid), public.entry_pair_guard(),
    public.suggested_beneficiaries(uuid, uuid, uuid), public.entry_reversed(uuid, uuid, uuid), public.cost_repaid_by(uuid, uuid, uuid, uuid),
    public.book_amount(uuid, uuid, bigint), public.settlement_check(uuid, uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid),
    public.settle_pair(uuid, text, uuid, uuid, uuid, uuid, jsonb), public.pair_settlement(uuid, uuid, uuid), public.linked_balances(uuid, uuid),
    public.agent_question_choices(), public.assignment_rule_guard() FROM PUBLIC;


-- Carry the earlier links and pairs over, as the owner recorded them. The
-- checks on new rows were met when they were first made.
ALTER TABLE public.book_links DISABLE TRIGGER USER;
ALTER TABLE public.book_link_terms DISABLE TRIGGER USER;
ALTER TABLE public.entry_pairs DISABLE TRIGGER USER;
ALTER TABLE public.book_links NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.book_link_terms NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.proposal_pairs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.entry_pairs NO FORCE ROW LEVEL SECURITY;
CREATE TEMP TABLE carried_links AS SELECT gen_random_uuid() AS id, x.* FROM earlier_links x;
INSERT INTO public.book_links(tenant_id, id, book_a, book_b, active, revision, actor_id, created_at, updated_at)
    SELECT tenant_id, id, least(business_book_id, personal_book_id), greatest(business_book_id, personal_book_id), active, 1, actor_id, created_at, updated_at
    FROM carried_links;
INSERT INTO public.book_link_terms(tenant_id, link_id, payer_book_id, beneficiary_book_id, treatment, payer_account_id, beneficiary_account_id, active, actor_id, updated_at)
    SELECT tenant_id, id, personal_book_id, business_book_id, mode, personal_account_id, business_account_id, true, actor_id, updated_at
    FROM carried_links;
INSERT INTO public.proposal_pairs(tenant_id, pair_key, kind, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id, created_at)
    SELECT tenant_id, pair_key, 'cost', personal_book_id, personal_proposal_id, business_book_id, business_proposal_id, transaction_id, created_at
    FROM earlier_proposal_pairs;
INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id, created_at)
    SELECT tenant_id, pair_key, 'cost', personal_book_id, personal_entry_id, business_book_id, business_entry_id, created_at
    FROM earlier_entry_pairs;
ALTER TABLE public.book_links ENABLE TRIGGER USER;
ALTER TABLE public.book_link_terms ENABLE TRIGGER USER;
ALTER TABLE public.entry_pairs ENABLE TRIGGER USER;
ALTER TABLE public.book_links FORCE ROW LEVEL SECURITY;
ALTER TABLE public.book_link_terms FORCE ROW LEVEL SECURITY;
ALTER TABLE public.proposal_pairs FORCE ROW LEVEL SECURITY;
ALTER TABLE public.entry_pairs FORCE ROW LEVEL SECURITY;
DROP TABLE earlier_links, earlier_proposal_pairs, earlier_entry_pairs, carried_links;
