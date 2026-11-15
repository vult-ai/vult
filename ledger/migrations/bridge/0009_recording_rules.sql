-- Recording within the owner's rules.
--
-- A rule is a decision the owner made once: money moving one way on one of
-- their accounts, from one counterparty, within an amount range, records to
-- one other account. Only the owner creates, turns on, turns off or changes
-- a rule; no agent can, and no proposal can. A connection the owner grants
-- it to may record bank activity directly only by an active rule, as a
-- transfer between the Book's own accounts, or as a match to an entry
-- already in the Book. Everything else it does stays a proposal. The
-- database checks every such entry, and each one names its rule.
--
-- A rule may split an item: up to seven parts, each a fixed amount, a
-- fraction of the item, or a loan's interest, with the rest going to the
-- rule's other account (for a loan, the principal).

-- A counterparty's key: its letters only, lowercased, single-spaced, so
-- "EXAMPLE CAFE #1021 10/14" and "Example Cafe #1187" are one key. The
-- same function makes a rule's key and matches activity against it.
CREATE FUNCTION public.rule_key(raw text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT SET search_path = pg_catalog AS $$
SELECT btrim(regexp_replace(lower(raw), '[^a-z]+', ' ', 'g'))
$$;

CREATE TABLE public.recording_rules (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    -- The owner's asset or liability account the money moves on.
    account_id uuid NOT NULL,
    -- in: the account's balance rises; out: it falls.
    direction text NOT NULL CHECK (direction IN ('in', 'out')),
    -- The counterparty as the bank names it, folded to lower case.
    counterparty_key text NOT NULL CHECK (length(counterparty_key) BETWEEN 1 AND 200 AND counterparty_key = public.rule_key(counterparty_key)),
    -- Where the other side of the entry goes.
    other_account_id uuid NOT NULL,
    min_minor bigint NOT NULL CHECK (min_minor > 0),
    max_minor bigint NOT NULL,
    -- At least this many days between two entries the rule records.
    min_days_between integer NOT NULL DEFAULT 0 CHECK (min_days_between BETWEEN 0 AND 366),
    description text NOT NULL CHECK (length(btrim(description)) BETWEEN 1 AND 400),
    active boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, other_account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    CHECK (max_minor >= min_minor),
    CHECK (account_id <> other_account_id)
);
CREATE UNIQUE INDEX recording_rules_one_active ON public.recording_rules(tenant_id, book_id, account_id, direction, counterparty_key) WHERE active;

-- Connections the owner lets record within the rules.
CREATE TABLE public.rule_recording_grants (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    granted_by uuid NOT NULL,
    granted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (tenant_id, book_id, connection_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

-- The parts a rule splits an item into, in order. A part is never deleted:
-- changing a rule's parts replaces them, and the old ones keep their
-- history.
CREATE TABLE public.recording_rule_parts (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    rule_id uuid NOT NULL,
    position smallint NOT NULL CHECK (position BETWEEN 1 AND 7),
    account_id uuid NOT NULL,
    -- fixed: amount_minor. ratio: the item times numerator / denominator,
    -- rounded half away from zero. interest: the loan's balance on the
    -- payment date times the yearly rate numerator / denominator, for a
    -- month, or for the days since the loan's last entry over 365 or 360.
    kind text NOT NULL CHECK (kind IN ('fixed', 'ratio', 'interest')),
    amount_minor bigint,
    numerator bigint,
    denominator bigint,
    basis text,
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    replaced_at timestamptz,
    FOREIGN KEY (tenant_id, book_id, rule_id) REFERENCES public.recording_rules(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    CHECK (kind = 'fixed' AND amount_minor > 0 AND numerator IS NULL AND denominator IS NULL AND basis IS NULL
        OR kind = 'ratio' AND amount_minor IS NULL AND numerator > 0 AND denominator > numerator AND denominator <= 1000000000 AND basis IS NULL
        OR kind = 'interest' AND amount_minor IS NULL AND numerator > 0 AND denominator > numerator AND denominator <= 1000000000
            AND basis IN ('monthly', 'actual_365', 'actual_360'))
);
CREATE UNIQUE INDEX recording_rule_parts_position ON public.recording_rule_parts(tenant_id, book_id, rule_id, position) WHERE replaced_at IS NULL;
CREATE UNIQUE INDEX recording_rule_parts_account ON public.recording_rule_parts(tenant_id, book_id, rule_id, account_id) WHERE replaced_at IS NULL;

ALTER TABLE public.bank_transaction_links DROP CONSTRAINT bank_transaction_links_basis_check,
    ADD CONSTRAINT bank_transaction_links_basis_check CHECK (basis IN ('judgement', 'existing_entry', 'transfer', 'rule')),
    ADD COLUMN rule_id uuid,
    ADD CONSTRAINT bank_transaction_links_rule_fk FOREIGN KEY (tenant_id, book_id, rule_id) REFERENCES public.recording_rules(tenant_id, book_id, id),
    ADD CONSTRAINT bank_transaction_links_rule_basis CHECK ((basis = 'rule') = (rule_id IS NOT NULL));

-- The owner, acting directly from a fresh sign-in. Never an approval of a
-- proposal, and never an agent.
CREATE FUNCTION public.rule_owner_allowed(t uuid, b uuid) RETURNS boolean
    LANGUAGE plpgsql VOLATILE SET search_path = pg_catalog AS $$
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN true;
    END IF;
    RETURN public.ledger_context(t, b)
        AND coalesce(current_setting('vult.connection_id', true), '') = ''
        AND coalesce(current_setting('vult.owner_posting', true), '') = ''
        AND access.owner_decision_allowed(t, b);
END $$;

-- This transaction is a granted connection recording within the rules. The
-- application marks it; the grant itself is checked here.
CREATE FUNCTION public.ledger_rule_recording() RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.rule_recording', true), '') = 'on' AND EXISTS (
    SELECT 1 FROM public.rule_recording_grants g JOIN public.connections c
        ON c.tenant_id = g.tenant_id AND c.book_id = g.book_id AND c.id = g.connection_id
    WHERE g.connection_id::text = coalesce(current_setting('vult.connection_id', true), '')
        AND g.tenant_id::text = coalesce(current_setting('vult.tenant_id', true), '')
        AND g.book_id::text = coalesce(current_setting('vult.book_id', true), '')
        AND g.revoked_at IS NULL AND c.revoked_at IS NULL AND c.access = 'propose')
$$;

CREATE FUNCTION public.recording_rule_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE a public.accounts%ROWTYPE; o public.accounts%ROWTYPE;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a rule is turned off, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.rule_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner creates or changes a recording rule' USING ERRCODE = '42501';
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.updated_at := clock_timestamp();
        IF TG_OP = 'INSERT' THEN
            NEW.created_at := clock_timestamp();
            NEW.revision := 1;
        ELSE
            IF ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.created_at) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.created_at) THEN
                RAISE EXCEPTION 'a rule keeps its identity' USING ERRCODE = '23514';
            END IF;
            NEW.revision := OLD.revision + 1;
        END IF;
    END IF;
    -- Classified by account types, never by names: the money moves on one
    -- of the owner's asset or liability accounts, and the other side fits
    -- its direction.
    SELECT * INTO STRICT a FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.account_id;
    SELECT * INTO STRICT o FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.other_account_id;
    IF a.class NOT IN ('asset', 'liability') THEN
        RAISE EXCEPTION 'a rule moves money on an asset or liability account' USING ERRCODE = '23514';
    END IF;
    IF NEW.direction = 'out' AND o.class = 'income' OR NEW.direction = 'in' AND o.class = 'expense' THEN
        RAISE EXCEPTION 'money out isn''t income and money in isn''t an expense; record those case by case' USING ERRCODE = '23514';
    END IF;
    IF o.status = 'retired' OR o.heading OR a.status = 'retired' THEN
        RAISE EXCEPTION 'a rule uses accounts that take postings' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER recording_rule_guard BEFORE INSERT OR UPDATE OR DELETE ON public.recording_rules
    FOR EACH ROW EXECUTE FUNCTION public.recording_rule_guard();

-- A part is classified by account types, never by names. Money out
-- splits to spending, debts or other assets; money in to income, debts
-- (such as tax collected) or equity. A part never goes to cash or a card:
-- money between the owner's accounts is a transfer.
CREATE FUNCTION public.recording_rule_part_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE r public.recording_rules%ROWTYPE; a public.accounts%ROWTYPE; o public.accounts%ROWTYPE;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a rule''s parts are replaced, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.rule_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner creates or changes a recording rule' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.replaced_at IS NOT NULL OR NEW.replaced_at IS NULL
            OR ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.rule_id, NEW.position, NEW.account_id, NEW.kind, NEW.amount_minor, NEW.numerator, NEW.denominator, NEW.basis, NEW.actor_id, NEW.created_at)
            IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.rule_id, OLD.position, OLD.account_id, OLD.kind, OLD.amount_minor, OLD.numerator, OLD.denominator, OLD.basis, OLD.actor_id, OLD.created_at) THEN
            RAISE EXCEPTION 'a rule''s part is only ever replaced' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.created_at := clock_timestamp();
        NEW.replaced_at := NULL;
    END IF;
    SELECT * INTO STRICT r FROM public.recording_rules WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.rule_id;
    SELECT * INTO STRICT a FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.account_id;
    SELECT * INTO STRICT o FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = r.other_account_id;
    IF a.status = 'retired' OR a.heading THEN
        RAISE EXCEPTION 'a rule uses accounts that take postings' USING ERRCODE = '23514';
    END IF;
    IF a.id IN (r.account_id, r.other_account_id) THEN
        RAISE EXCEPTION 'a part goes to an account other than the rule''s own two' USING ERRCODE = '23514';
    END IF;
    IF a.role IN ('cash', 'card') OR EXISTS (SELECT 1 FROM public.bank_accounts b
            WHERE b.tenant_id = NEW.tenant_id AND b.book_id = NEW.book_id AND b.account_id = NEW.account_id) THEN
        RAISE EXCEPTION 'a part never goes to cash or a card; money between your own accounts is a transfer' USING ERRCODE = '23514';
    END IF;
    IF r.direction = 'out' AND a.class NOT IN ('expense', 'liability', 'asset')
        OR r.direction = 'in' AND a.class NOT IN ('income', 'liability', 'equity') THEN
        RAISE EXCEPTION 'money out splits to spending, debts or other assets; money in to income, debts or equity' USING ERRCODE = '23514';
    END IF;
    IF NEW.kind = 'interest' AND (a.class <> 'expense' OR r.direction <> 'out' OR o.class <> 'liability' OR o.role IS DISTINCT FROM 'loan') THEN
        RAISE EXCEPTION 'interest is an expense split from a payment whose rest pays down a loan' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER recording_rule_part_guard BEFORE INSERT OR UPDATE OR DELETE ON public.recording_rule_parts
    FOR EACH ROW EXECUTE FUNCTION public.recording_rule_part_guard();

CREATE FUNCTION public.rule_grant_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a grant is revoked, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.rule_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner grants recording within rules' USING ERRCODE = '42501';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id
            AND c.id = NEW.connection_id AND c.access = 'propose' AND c.revoked_at IS NULL) THEN
        RAISE EXCEPTION 'recording within rules is granted to a proposing connection' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER rule_grant_guard BEFORE INSERT OR UPDATE OR DELETE ON public.rule_recording_grants
    FOR EACH ROW EXECUTE FUNCTION public.rule_grant_guard();

-- A link made by a rule matches that rule: active, on its account and in its
-- direction, from its counterparty, within its range and cadence, with an
-- entry of exactly two lines that moves the account by the amount and posts
-- the other side to the rule's account.
CREATE FUNCTION public.rule_link_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE r public.recording_rules%ROWTYPE; t public.bank_transactions%ROWTYPE; lines integer; other_ok boolean; distinct_ok boolean;
BEGIN
    IF NEW.basis <> 'rule' OR NEW.revoked_at IS NOT NULL OR public.ledger_restoring(NEW.tenant_id, NEW.book_id) THEN
        RETURN NULL;
    END IF;
    SELECT * INTO STRICT r FROM public.recording_rules WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.rule_id;
    SELECT * INTO STRICT t FROM public.bank_transactions WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.transaction_id;
    IF NOT r.active OR r.account_id <> NEW.account_id
        OR (t.amount_minor > 0) <> (r.direction = 'in')
        OR abs(t.amount_minor) NOT BETWEEN r.min_minor AND r.max_minor
        OR public.rule_key(coalesce(nullif(t.counterparty, ''), t.description)) <> r.counterparty_key THEN
        RAISE EXCEPTION 'this activity isn''t covered by the rule' USING ERRCODE = '23514';
    END IF;
    IF r.min_days_between > 0 AND EXISTS (SELECT 1 FROM public.bank_transaction_links l JOIN public.bank_transactions x
            ON x.tenant_id = l.tenant_id AND x.book_id = l.book_id AND x.id = l.transaction_id
            WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.rule_id = r.id AND l.id <> NEW.id AND l.revoked_at IS NULL
            AND abs(x.occurred_on - t.occurred_on) < r.min_days_between) THEN
        RAISE EXCEPTION 'the rule recorded this counterparty too recently; this one is a proposal' USING ERRCODE = '23514';
    END IF;
    -- The entry posts once to each of the rule's accounts: its own, the
    -- rest to its other account, and any of its current parts.
    SELECT count(*), count(DISTINCT l.account_id) = count(*), bool_and(l.account_id IN (r.account_id, r.other_account_id)
            OR EXISTS (SELECT 1 FROM public.recording_rule_parts p WHERE p.tenant_id = l.tenant_id AND p.book_id = l.book_id
                AND p.rule_id = r.id AND p.replaced_at IS NULL AND p.account_id = l.account_id))
        INTO lines, distinct_ok, other_ok
        FROM public.lines l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.entry_id = NEW.entry_id;
    IF lines < 2 OR NOT distinct_ok OR NOT other_ok OR NOT EXISTS (SELECT 1 FROM public.lines l WHERE l.tenant_id = NEW.tenant_id
            AND l.book_id = NEW.book_id AND l.entry_id = NEW.entry_id AND l.account_id = r.other_account_id) THEN
        RAISE EXCEPTION 'an entry a rule records posts only to the rule''s accounts' USING ERRCODE = '23514';
    END IF;
    -- Fixed and fractional parts are exactly the rule's.
    IF EXISTS (SELECT 1 FROM public.recording_rule_parts p JOIN public.lines l
            ON l.tenant_id = p.tenant_id AND l.book_id = p.book_id AND l.entry_id = NEW.entry_id AND l.account_id = p.account_id
            WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id AND p.rule_id = r.id AND p.replaced_at IS NULL
            AND (p.kind = 'fixed' AND l.amount_minor <> p.amount_minor
                OR p.kind = 'ratio' AND l.amount_minor <> round(abs(t.amount_minor)::numeric * p.numerator / p.denominator))) THEN
        RAISE EXCEPTION 'a part of the entry differs from the rule' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER rule_link_final AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.rule_link_final();

-- Under a rule grant, an entry is posted only to record bank activity by a
-- rule or as a transfer, and links are made only by a rule, a transfer or a
-- match to an existing entry.
CREATE FUNCTION public.rule_recording_entry_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id
            AND l.entry_id = NEW.id AND l.revoked_at IS NULL AND l.basis IN ('rule', 'transfer')
            AND public.ledger_written_here(l.xmin)) THEN
        RAISE EXCEPTION 'within the rules, an entry records bank activity by a rule or as a transfer' USING ERRCODE = '42501';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER rule_recording_entry AFTER INSERT ON public.entries DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (coalesce(current_setting('vult.rule_recording', true), '') = 'on')
    EXECUTE FUNCTION public.rule_recording_entry_final();
CREATE FUNCTION public.rule_recording_link_check() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF public.ledger_rule_recording() AND NEW.basis NOT IN ('rule', 'transfer', 'existing_entry') THEN
        RAISE EXCEPTION 'within the rules, activity is recorded by a rule, a transfer or a match' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER rule_recording_link BEFORE INSERT ON public.bank_transaction_links
    FOR EACH ROW EXECUTE FUNCTION public.rule_recording_link_check();

-- Write authority: a granted proposing connection may write while
-- recording within the rules; everything else it writes stays a proposal.
CREATE OR REPLACE FUNCTION access.agent_allowed(connection uuid, writing boolean) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT EXISTS (SELECT 1
    FROM access.oauth_credentials c
    JOIN access.oauth_grants g ON g.connection_id = c.connection_id
    JOIN access.identities i ON i.id = g.identity_id
    AND i.epoch = g.owner_epoch
    JOIN access.owners o ON o.tenant_id = g.tenant_id
    AND o.identity_id = i.id
    JOIN public.connections p ON p.id = g.connection_id AND p.tenant_id = g.tenant_id AND p.book_id = g.book_id
    WHERE c.connection_id = connection
    AND c.kind = 'access'
    AND c.token_hash = coalesce(current_setting('vult.agent_token', true), '')
    AND g.resource = coalesce(current_setting('vult.agent_resource', true), '') AND c.generation = g.generation
    AND c.expires_at > clock_timestamp()
    AND c.consumed_at IS NULL
    AND p.revoked_at IS NULL
    AND (NOT writing
    OR p.access = 'write'
    OR p.access = 'propose' AND (public.ledger_proposing() OR public.ledger_rule_recording())))
$$;

CREATE OR REPLACE FUNCTION public.ledger_allowed(t uuid, b uuid, writing boolean) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT CASE WHEN coalesce(current_setting('vult.owner_posting', true), '') = '' THEN
    ((NOT has_function_privilege(session_user, 'access.owner_posting_allowed(uuid,uuid)'::regprocedure, 'EXECUTE')
        OR public.ledger_import_owner())
    AND public.ledger_context(t, b) AND EXISTS (SELECT 1 FROM public.connections c
        WHERE c.tenant_id = t
        AND c.book_id = b
        AND c.id::text = coalesce(current_setting('vult.connection_id', true), '')
        AND c.actor_id::text = coalesce(current_setting('vult.actor_id', true), '')
        AND c.revoked_at IS NULL
        AND (NOT writing OR c.access = 'write' OR c.access = 'propose' AND (public.ledger_proposing() OR public.ledger_rule_recording()))
        AND (NOT EXISTS (SELECT 1 FROM access.oauth_grants g WHERE g.connection_id = c.id)
            OR access.agent_allowed(c.id, writing))))
    OR (coalesce(current_setting('vult.connection_id', true), '') = ''
        AND has_function_privilege(session_user, 'access.owner_identity(uuid)'::regprocedure, 'EXECUTE')
        AND access.owner_decision_allowed(t, b))
ELSE access.owner_posting_allowed(t, b) END
$$;

-- Links recorded by a rule keep a receipt under its request.
DROP TRIGGER bank_link_receipt ON public.bank_transaction_links;
CREATE CONSTRAINT TRIGGER bank_link_receipt AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('link_bank_activity', 'record_bank_activity', 'record_bank_transfer', 'record_by_rule');

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['recording_rules', 'rule_recording_grants', 'recording_rule_parts'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY rule_read ON public.%I FOR SELECT USING (public.ledger_allowed(tenant_id, book_id, false) OR access.owner_allowed(tenant_id))', tab);
        EXECUTE format('CREATE POLICY rule_owner_insert ON public.%I FOR INSERT WITH CHECK (public.rule_owner_allowed(tenant_id, book_id))', tab);
        EXECUTE format('CREATE POLICY rule_owner_update ON public.%I FOR UPDATE USING (public.rule_owner_allowed(tenant_id, book_id)) WITH CHECK (public.rule_owner_allowed(tenant_id, book_id))', tab);
    END LOOP;
END $$;

REVOKE ALL ON FUNCTION public.rule_key(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.rule_owner_allowed(uuid, uuid), public.ledger_rule_recording(), public.recording_rule_guard(), public.recording_rule_part_guard(), public.rule_grant_guard(),
    public.rule_link_final(), public.rule_recording_entry_final(), public.rule_recording_link_check() FROM PUBLIC;
