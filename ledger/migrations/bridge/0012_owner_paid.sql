-- Business costs the owner pays personally: a business Book and a personal
-- Book of the same owner are linked, and a business cost paid on a personal
-- card is recorded in both as one pair, decided once. Each Book stays
-- balanced on its own; classification is by account type and role, never by
-- name.

-- The owner's link between a business Book and a personal Book, with how
-- owner-paid costs are recorded and the one account on each side for them.
CREATE TABLE public.book_links (
    tenant_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    personal_book_id uuid NOT NULL,
    mode text NOT NULL CHECK (mode IN ('contribution', 'owed')),
    business_account_id uuid NOT NULL,
    personal_account_id uuid NOT NULL,
    active boolean NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, business_book_id, personal_book_id),
    UNIQUE (tenant_id, business_book_id),
    UNIQUE (tenant_id, personal_book_id),
    CHECK (business_book_id <> personal_book_id),
    FOREIGN KEY (tenant_id, business_book_id) REFERENCES public.books(tenant_id, id),
    FOREIGN KEY (tenant_id, personal_book_id) REFERENCES public.books(tenant_id, id),
    FOREIGN KEY (tenant_id, business_book_id, business_account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, personal_book_id, personal_account_id) REFERENCES public.accounts(tenant_id, book_id, id)
);

-- Two waiting proposals, one in each linked Book, decided as one.
CREATE TABLE public.proposal_pairs (
    tenant_id uuid NOT NULL,
    pair_key text NOT NULL CHECK (pair_key ~ '^[A-Za-z0-9_-]{1,64}$'),
    business_book_id uuid NOT NULL,
    business_proposal_id uuid NOT NULL,
    personal_book_id uuid NOT NULL,
    personal_proposal_id uuid NOT NULL,
    transaction_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, business_book_id, business_proposal_id),
    UNIQUE (tenant_id, personal_book_id, personal_proposal_id),
    FOREIGN KEY (tenant_id, business_book_id, business_proposal_id) REFERENCES public.operation_proposals(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, personal_book_id, personal_proposal_id) REFERENCES public.operation_proposals(tenant_id, book_id, id)
);

-- The two entries a decided pair posted.
CREATE TABLE public.entry_pairs (
    tenant_id uuid NOT NULL,
    pair_key text NOT NULL,
    business_book_id uuid NOT NULL,
    business_entry_id uuid NOT NULL,
    personal_book_id uuid NOT NULL,
    personal_entry_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, business_book_id, business_entry_id),
    UNIQUE (tenant_id, personal_book_id, personal_entry_id)
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

CREATE FUNCTION public.book_link_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE ba public.accounts%ROWTYPE; pa public.accounts%ROWTYPE;
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END) THEN
        RAISE EXCEPTION 'only the owner links Books' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF EXISTS (SELECT 1 FROM public.proposal_pairs p WHERE p.tenant_id = OLD.tenant_id AND p.business_book_id = OLD.business_book_id AND p.personal_book_id = OLD.personal_book_id)
            OR EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = OLD.tenant_id AND p.business_book_id = OLD.business_book_id AND p.personal_book_id = OLD.personal_book_id) THEN
            RAISE EXCEPTION 'These Books have pairs recorded between them; turn the link off instead.' USING ERRCODE = 'VP020';
        END IF;
        RETURN OLD;
    END IF;
    IF NOT public.ledger_import_owner() AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF ROW(NEW.tenant_id, NEW.business_book_id, NEW.personal_book_id, NEW.created_at) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.business_book_id, OLD.personal_book_id, OLD.created_at) THEN
            RAISE EXCEPTION 'a link keeps its Books' USING ERRCODE = '23514';
        END IF;
        IF NEW.revision <> OLD.revision + 1 THEN
            RAISE EXCEPTION 'the link changed concurrently' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.revision <> 1 THEN
        RAISE EXCEPTION 'the link changed concurrently' USING ERRCODE = '23514';
    END IF;
    NEW.updated_at := clock_timestamp();
    -- A Book's profile is its latest settings revision.
    IF (SELECT r.profile FROM public.book_revisions r WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.business_book_id ORDER BY r.revision DESC LIMIT 1) IS DISTINCT FROM 'business'
        OR (SELECT r.profile FROM public.book_revisions r WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.personal_book_id ORDER BY r.revision DESC LIMIT 1) IS DISTINCT FROM 'personal' THEN
        RAISE EXCEPTION 'A link joins a business Book and a personal Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT * INTO ba FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.business_book_id AND id = NEW.business_account_id;
    SELECT * INTO pa FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.personal_book_id AND id = NEW.personal_account_id;
    IF ba.status = 'retired' OR ba.heading OR pa.status = 'retired' OR pa.heading THEN
        RAISE EXCEPTION 'A link uses accounts that take postings.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.mode = 'contribution' AND (ba.class <> 'equity' OR pa.class <> 'asset' OR pa.role IS DISTINCT FROM 'investment') THEN
        RAISE EXCEPTION 'Money you put in is recorded to an equity account in the business Book and an investment asset in the personal Book.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.mode = 'owed' AND (ba.class <> 'liability' OR ba.role IS DISTINCT FROM 'payable' OR pa.class <> 'asset' OR pa.role IS DISTINCT FROM 'receivable') THEN
        RAISE EXCEPTION 'Money owed back to you is recorded to a payable in the business Book and a receivable in the personal Book.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER book_link_guard BEFORE INSERT OR UPDATE OR DELETE ON public.book_links
    FOR EACH ROW EXECUTE FUNCTION public.book_link_guard();

-- The link that names Book b, read by the owner or by any connection of b.
CREATE FUNCTION public.book_link_for(t uuid, b uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL
    ELSE (SELECT jsonb_build_object('linked', true,
        'linked_book_id', CASE WHEN l.business_book_id = b THEN l.personal_book_id ELSE l.business_book_id END,
        'linked_book_profile', CASE WHEN l.business_book_id = b THEN 'personal' ELSE 'business' END,
        'linked_book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN l.business_book_id = b THEN l.personal_book_id ELSE l.business_book_id END),
        'business_book_id', l.business_book_id, 'personal_book_id', l.personal_book_id,
        'mode', l.mode, 'business_account_id', l.business_account_id, 'personal_account_id', l.personal_account_id,
        'active', l.active, 'revision', l.revision)
        FROM public.book_links l WHERE l.tenant_id = t AND (l.business_book_id = b OR l.personal_book_id = b)) END
$$;

-- Joins the business half of a pair, just prepared in Book b, to its waiting
-- personal half, after checking both against the link and the bank item. A
-- personal half only checks its own side and waits. Refusals say why.
CREATE FUNCTION public.pair_proposal(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; lk public.book_links%ROWTYPE;
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
    IF me.operation = 'record_bank_activity' THEN
        SELECT * INTO lk FROM public.book_links WHERE tenant_id = t AND personal_book_id = b AND active;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'The pair can''t be prepared: this Book isn''t linked to a business Book.' USING ERRCODE = 'VP020';
        END IF;
        IF args ->> 'other_account_id' IS DISTINCT FROM lk.personal_account_id::text THEN
            RAISE EXCEPTION 'The pair can''t be prepared: the personal side records the card charge to the link''s account (other_account_id %).', lk.personal_account_id USING ERRCODE = 'VP020';
        END IF;
        RETURN;
    END IF;
    IF me.operation <> 'post_journal' THEN
        RAISE EXCEPTION 'The pair can''t be prepared: a pair is record_bank_activity in the personal Book and post_journal in the business Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT * INTO lk FROM public.book_links WHERE tenant_id = t AND business_book_id = b AND active;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: this Book isn''t linked to a personal Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id = lk.personal_book_id AND q.operation = 'record_bank_activity'
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: prepare the personal half first; no waiting personal half has pair_key %.', key USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = t AND x.personal_book_id = other.book_id AND x.personal_proposal_id = other.id) THEN
        RAISE EXCEPTION 'The pair can''t be prepared: its personal half is already paired.' USING ERRCODE = 'VP020';
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF oargs ->> 'transaction_id' IS DISTINCT FROM args ->> 'paired_transaction_id' THEN
        RAISE EXCEPTION 'The pair can''t be prepared: paired_transaction_id isn''t the card charge its personal half records.' USING ERRCODE = 'VP020';
    END IF;
    IF oargs ->> 'other_account_id' IS DISTINCT FROM lk.personal_account_id::text THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the personal half doesn''t record to the link''s account.' USING ERRCODE = 'VP020';
    END IF;
    SELECT * INTO item FROM public.bank_transactions WHERE tenant_id = t AND book_id = lk.personal_book_id AND id = (oargs ->> 'transaction_id')::uuid;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the card charge isn''t in the personal Book.' USING ERRCODE = 'VP020';
    END IF;
    -- Amounts follow the bank account's own balance: a card charge raises a
    -- liability (positive), money paid from an account lowers an asset
    -- (negative). A pair records money the owner paid out.
    paid := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = lk.personal_book_id
        AND a.id = coalesce((SELECT x.account_id FROM public.bank_accounts x WHERE x.tenant_id = t AND x.book_id = lk.personal_book_id AND x.id = item.bank_account_id), item.account_id));
    IF NOT (paid = 'liability' AND item.amount_minor > 0 OR paid = 'asset' AND item.amount_minor < 0) THEN
        RAISE EXCEPTION 'The pair can''t be prepared: a pair records money paid out, such as a charge on the card.' USING ERRCODE = 'VP020';
    END IF;
    amount := abs(item.amount_minor);
    IF (SELECT k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = b) IS DISTINCT FROM item.currency THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the charge is in another currency than the business Book.' USING ERRCODE = 'VP020';
    END IF;
    IF args ->> 'accounting_date' IS DISTINCT FROM item.occurred_on::text THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the business entry''s date isn''t the charge''s date (%).', item.occurred_on USING ERRCODE = 'VP020';
    END IF;
    FOR line IN SELECT * FROM jsonb_array_elements(coalesce(args -> 'lines', '[]'::jsonb)) LOOP
        SELECT * INTO acc FROM public.accounts WHERE tenant_id = t AND book_id = b AND id = (line ->> 'account_id')::uuid;
        IF line ->> 'side' = 'credit' THEN
            credits := credits + 1;
            IF acc.id IS DISTINCT FROM lk.business_account_id OR (line ->> 'amount_minor')::bigint <> amount THEN
                RAISE EXCEPTION 'The pair can''t be prepared: the business entry credits the link''s account (%) with the whole charge.', lk.business_account_id USING ERRCODE = 'VP020';
            END IF;
        ELSE
            IF NOT (acc.class = 'expense' OR acc.class = 'asset' AND coalesce(acc.role, '') NOT IN ('cash', 'card')
                    AND NOT EXISTS (SELECT 1 FROM public.bank_accounts x WHERE x.tenant_id = t AND x.book_id = b AND x.account_id = acc.id)) THEN
                RAISE EXCEPTION 'The pair can''t be prepared: the business entry debits only expenses, or assets that aren''t cash, a card or a bank account.' USING ERRCODE = 'VP020';
            END IF;
            debits := debits + (line ->> 'amount_minor')::bigint;
        END IF;
    END LOOP;
    IF credits <> 1 OR debits <> amount THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the business entry is for the charge''s amount, with one credit to the link''s account.' USING ERRCODE = 'VP020';
    END IF;
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, business_book_id, business_proposal_id, personal_book_id, personal_proposal_id, transaction_id)
        VALUES (t, key, b, p, other.book_id, other.id, item.id);
END $$;

-- The pair a proposal belongs to, for reads in either Book: the partner's
-- Book, proposal and side.
CREATE FUNCTION public.proposal_pair_of(t uuid, b uuid, p uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE (
    SELECT jsonb_build_object('pair_key', x.pair_key, 'transaction_id', x.transaction_id,
        'book_id', CASE WHEN x.business_book_id = b THEN x.personal_book_id ELSE x.business_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.business_book_id = b THEN x.personal_book_id ELSE x.business_book_id END),
        'proposal_id', CASE WHEN x.business_book_id = b THEN x.personal_proposal_id ELSE x.business_proposal_id END,
        'side', CASE WHEN x.business_book_id = b THEN 'personal' ELSE 'business' END)
    FROM public.proposal_pairs x WHERE x.tenant_id = t
        AND (x.business_book_id = b AND x.business_proposal_id = p OR x.personal_book_id = b AND x.personal_proposal_id = p)) END
$$;

-- The other entry of a decided pair, for the owner or a connection of
-- Book b: its Book, entry and side.
CREATE FUNCTION public.entry_pair_of(t uuid, b uuid, e uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE (
    SELECT jsonb_build_object('pair_key', x.pair_key,
        'book_id', CASE WHEN x.business_book_id = b THEN x.personal_book_id ELSE x.business_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.business_book_id = b THEN x.personal_book_id ELSE x.business_book_id END),
        'entry_id', CASE WHEN x.business_book_id = b THEN x.personal_entry_id ELSE x.business_entry_id END,
        'side', CASE WHEN x.business_book_id = b THEN 'personal' ELSE 'business' END)
    FROM public.entry_pairs x WHERE x.tenant_id = t
        AND (x.business_book_id = b AND x.business_entry_id = e OR x.personal_book_id = b AND x.personal_entry_id = e)) END
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
    FOREACH tab IN ARRAY ARRAY['book_links', 'proposal_pairs', 'entry_pairs'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY pair_read ON public.%I FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, business_book_id, false) OR public.ledger_allowed(tenant_id, personal_book_id, false))', tab);
    END LOOP;
END $$;
CREATE POLICY owner_write ON public.book_links FOR INSERT WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_update ON public.book_links FOR UPDATE USING (public.book_link_owner(tenant_id)) WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_delete ON public.book_links FOR DELETE USING (public.book_link_owner(tenant_id));
-- The owner's decision posts both entries of a pair and records them here.
CREATE POLICY owner_pairs ON public.entry_pairs FOR INSERT WITH CHECK (access.owner_allowed(tenant_id) AND access.owner_write_allowed(tenant_id));

REVOKE ALL ON FUNCTION public.book_link_owner(uuid), public.book_link_guard(), public.book_link_for(uuid, uuid), public.pair_proposal(uuid, uuid, uuid),
    public.proposal_pair_of(uuid, uuid, uuid), public.entry_pair_of(uuid, uuid, uuid) FROM PUBLIC;
