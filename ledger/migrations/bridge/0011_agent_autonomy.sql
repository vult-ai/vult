-- Earned autonomy: an agent the owner allows may record what it's sure
-- about, within limits the ledger checks, and asks about the rest. Trust is
-- earned per kind of activity and held here, the same for any agent. The
-- owner decides; approval of proposals stays the owner's alone.

-- How a proposing connection may record directly: only by the owner's rules,
-- or also when confident, within the Book's limits. Without a grant it only
-- drafts proposals.
ALTER TABLE public.rule_recording_grants
    ADD COLUMN recording_mode text NOT NULL DEFAULT 'rules' CHECK (recording_mode IN ('rules', 'confident'));

CREATE FUNCTION public.ledger_confident_recording() RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT public.ledger_rule_recording() AND EXISTS (
    SELECT 1 FROM public.rule_recording_grants g
    WHERE g.connection_id::text = coalesce(current_setting('vult.connection_id', true), '')
        AND g.tenant_id::text = coalesce(current_setting('vult.tenant_id', true), '')
        AND g.book_id::text = coalesce(current_setting('vult.book_id', true), '')
        AND g.revoked_at IS NULL AND g.recording_mode = 'confident')
$$;

-- The Book's limits for recording when confident. Without a row, the cap is
-- 500 in the Book's currency.
CREATE TABLE public.agent_policy (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    amount_cap_minor bigint NOT NULL CHECK (amount_cap_minor > 0),
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

CREATE FUNCTION public.agent_amount_cap(t uuid, b uuid) RETURNS bigint
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT coalesce((SELECT p.amount_cap_minor FROM public.agent_policy p WHERE p.tenant_id = t AND p.book_id = b),
    (SELECT 500 * (10::numeric ^ k.scale)::bigint FROM public.books k WHERE k.tenant_id = t AND k.id = b))
$$;

-- Accounts an agent reaches only by the owner's rules, such as taxes owed.
-- The owner marks them; a starter chart marks its own as it creates them.
CREATE TABLE public.agent_tax_sensitive_accounts (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    account_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    marked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, account_id),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id)
);

CREATE FUNCTION public.agent_limits_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE r record;
BEGIN
    IF TG_OP = 'DELETE' THEN
        r := OLD;
    ELSE
        r := NEW;
    END IF;
    IF public.ledger_import_owner() THEN
        RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    END IF;
    -- Only a tax-sensitive row has an account; the policy row has none, so
    -- its account is read only inside this branch.
    IF TG_TABLE_NAME = 'agent_tax_sensitive_accounts' AND TG_OP = 'INSERT' THEN
        IF public.ledger_context(NEW.tenant_id, NEW.book_id)
            AND EXISTS (SELECT 1 FROM public.accounts a WHERE a.tenant_id = NEW.tenant_id AND a.book_id = NEW.book_id
                AND a.id = (to_jsonb(NEW) ->> 'account_id')::uuid AND public.ledger_written_here(a.xmin)) THEN
            -- A starter chart marks the accounts it creates in this transaction.
            RETURN NEW;
        END IF;
    END IF;
    IF NOT public.agent_owner_change_allowed(r.tenant_id, r.book_id, TG_TABLE_NAME, TG_OP,
            CASE WHEN TG_OP <> 'INSERT' THEN to_jsonb(OLD) END, CASE WHEN TG_OP <> 'DELETE' THEN to_jsonb(NEW) END) THEN
        RAISE EXCEPTION 'only the owner sets an agent''s limits' USING ERRCODE = '42501';
    END IF;
    IF TG_OP <> 'DELETE' AND r.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF TG_TABLE_NAME = 'agent_policy' THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION 'an agent policy is changed, never deleted' USING ERRCODE = '23514';
        END IF;
        IF TG_OP = 'UPDATE' AND NEW.revision <> OLD.revision + 1 OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'agent policy changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE TRIGGER agent_policy_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_policy
    FOR EACH ROW EXECUTE FUNCTION public.agent_limits_guard();
CREATE TRIGGER agent_tax_sensitive_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_tax_sensitive_accounts
    FOR EACH ROW EXECUTE FUNCTION public.agent_limits_guard();

-- Trust per kind of activity: the Book account money moves on, its
-- direction (the balance's, as for rules) and the counterparty's key. The
-- ledger alone writes it, from the owner's decisions.
CREATE TABLE public.agent_kinds (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    account_id uuid NOT NULL,
    direction text NOT NULL CHECK (direction IN ('in', 'out')),
    counterparty_key text NOT NULL CHECK (length(counterparty_key) <= 200),
    accepted_unchanged integer NOT NULL DEFAULT 0 CHECK (accepted_unchanged >= 0),
    accepted_since_correction integer NOT NULL DEFAULT 0 CHECK (accepted_since_correction >= 0),
    corrected integer NOT NULL DEFAULT 0 CHECK (corrected >= 0),
    last_corrected_at timestamptz,
    -- record: may be recorded when confident; draft: proposals only.
    level text NOT NULL DEFAULT 'record' CHECK (level IN ('record', 'draft')),
    -- The owner's decision for the kind: the other account of the last
    -- approved entry, and the one they last turned down.
    decided_account_id uuid,
    declined_account_id uuid,
    offered_at timestamptz,
    rule_id uuid,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, account_id, direction, counterparty_key),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id)
);
-- Each bank item counts once per outcome, however it was decided.
CREATE TABLE public.agent_kind_events (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    transaction_id uuid NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('accepted', 'corrected')),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, transaction_id, outcome),
    FOREIGN KEY (tenant_id, book_id, transaction_id) REFERENCES public.bank_transactions(tenant_id, book_id, id)
);

-- The Book's starting trust for kinds with no history: the built-in agent's
-- skills result once the owner consented; 'record' when there's no built-in
-- agent. Trust only ever demotes a kind from here.
CREATE FUNCTION public.agent_default_trust(t uuid, b uuid) RETURNS text
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    RETURN coalesce((SELECT s.default_trust FROM public.agent_settings s WHERE s.tenant_id = t AND s.book_id = b), 'record');
END $$;

-- Notes an outcome for a bank item's kind. Accepted work counts towards an
-- offer to record the kind by a rule and keeps the account the owner chose;
-- a correction sends the kind back to drafts, keeps the account they turned
-- down, and an undo forgets the decision it undid.
CREATE FUNCTION public.agent_kind_note(t uuid, b uuid, item uuid, outcome text, other uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE k record; inserted integer;
BEGIN
    SELECT coalesce(a.account_id, x.account_id) AS account_id, CASE WHEN x.amount_minor > 0 THEN 'in' ELSE 'out' END AS direction,
            left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200) AS counterparty_key
        INTO k FROM public.bank_transactions x LEFT JOIN public.bank_accounts a ON a.tenant_id = x.tenant_id AND a.book_id = x.book_id AND a.id = x.bank_account_id
        WHERE x.tenant_id = t AND x.book_id = b AND x.id = item;
    IF k.account_id IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO public.agent_kind_events(tenant_id, book_id, transaction_id, outcome)
        VALUES (t, b, item, CASE WHEN outcome = 'accepted' THEN 'accepted' ELSE 'corrected' END) ON CONFLICT DO NOTHING;
    GET DIAGNOSTICS inserted = ROW_COUNT;
    IF inserted = 0 THEN
        RETURN;
    END IF;
    INSERT INTO public.agent_kinds(tenant_id, book_id, account_id, direction, counterparty_key, level)
        VALUES (t, b, k.account_id, k.direction, k.counterparty_key, public.agent_default_trust(t, b)) ON CONFLICT DO NOTHING;
    IF outcome = 'accepted' THEN
        UPDATE public.agent_kinds SET accepted_unchanged = accepted_unchanged + 1, accepted_since_correction = accepted_since_correction + 1,
            offered_at = CASE WHEN offered_at IS NULL AND rule_id IS NULL AND level = 'draft' AND accepted_since_correction + 1 >= 15 THEN clock_timestamp() ELSE offered_at END,
            decided_account_id = coalesce(other, decided_account_id),
            declined_account_id = CASE WHEN other IS NOT NULL THEN NULL ELSE declined_account_id END,
            updated_at = clock_timestamp()
            WHERE tenant_id = t AND book_id = b AND account_id = k.account_id AND direction = k.direction AND counterparty_key = k.counterparty_key;
    ELSE
        UPDATE public.agent_kinds SET corrected = corrected + 1, accepted_since_correction = 0, last_corrected_at = clock_timestamp(),
            level = 'draft', offered_at = NULL, updated_at = clock_timestamp(),
            declined_account_id = CASE WHEN outcome = 'rejected' THEN coalesce(other, declined_account_id) ELSE declined_account_id END,
            decided_account_id = CASE WHEN outcome = 'undone' AND decided_account_id = other THEN NULL ELSE decided_account_id END
            WHERE tenant_id = t AND book_id = b AND account_id = k.account_id AND direction = k.direction AND counterparty_key = k.counterparty_key;
    END IF;
END $$;

-- The other account of a bank item's entry: the one that isn't the item's
-- own Book account.
CREATE FUNCTION public.agent_other_account(t uuid, b uuid, item uuid, args jsonb, entry uuid) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT coalesce(
    (args ->> 'other_account_id')::uuid,
    (SELECT (l ->> 'account_id')::uuid FROM jsonb_array_elements(coalesce(args -> 'lines', '[]'::jsonb)) l
        WHERE (l ->> 'account_id')::uuid IS DISTINCT FROM (SELECT coalesce(a.account_id, x.account_id) FROM public.bank_transactions x
            LEFT JOIN public.bank_accounts a ON a.tenant_id = x.tenant_id AND a.book_id = x.book_id AND a.id = x.bank_account_id
            WHERE x.tenant_id = t AND x.book_id = b AND x.id = item)
        AND jsonb_array_length(args -> 'lines') = 2 LIMIT 1),
    (SELECT l.account_id FROM public.lines l WHERE l.tenant_id = t AND l.book_id = b AND l.entry_id = entry
        AND (SELECT count(*) FROM public.lines c WHERE c.tenant_id = t AND c.book_id = b AND c.entry_id = entry) = 2
        AND l.account_id IS DISTINCT FROM (SELECT coalesce(a.account_id, x.account_id) FROM public.bank_transactions x
            LEFT JOIN public.bank_accounts a ON a.tenant_id = x.tenant_id AND a.book_id = x.book_id AND a.id = x.bank_account_id
            WHERE x.tenant_id = t AND x.book_id = b AND x.id = item) LIMIT 1))
$$;

-- The owner's decision on a proposal to record bank activity: approval is
-- always of the exact work, so it counts as accepted unchanged.
CREATE FUNCTION public.agent_kind_decision() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE p public.operation_proposals%ROWTYPE; args jsonb;
BEGIN
    SELECT * INTO p FROM public.operation_proposals WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.proposal_id;
    IF p.operation = 'record_bank_activity' THEN
        args := convert_from(p.payload_bytes, 'UTF8')::jsonb -> 'arguments';
        -- Rejecting a copy of activity already recorded another way is
        -- tidying up, not a correction of the agent.
        IF NEW.decision = 'reject' AND EXISTS (SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id = NEW.tenant_id
                AND l.book_id = NEW.book_id AND l.transaction_id = (args ->> 'transaction_id')::uuid AND l.revoked_at IS NULL) THEN
            RETURN NULL;
        END IF;
        PERFORM public.agent_kind_note(NEW.tenant_id, NEW.book_id, (args ->> 'transaction_id')::uuid,
            CASE WHEN NEW.decision = 'approve' THEN 'accepted' ELSE 'rejected' END,
            public.agent_other_account(NEW.tenant_id, NEW.book_id, (args ->> 'transaction_id')::uuid, args, NULL));
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER agent_kind_decision AFTER INSERT ON public.operation_decisions
    FOR EACH ROW EXECUTE FUNCTION public.agent_kind_decision();

-- The owner undoing an agent's recording: reversing its entry or unlinking
-- it from the bank item is a correction of that kind.
CREATE FUNCTION public.agent_kind_undone() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE l record;
BEGIN
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
CREATE TRIGGER agent_kind_reversed AFTER INSERT ON public.entries
    FOR EACH ROW WHEN (NEW.reversal_of IS NOT NULL) EXECUTE FUNCTION public.agent_kind_undone();

CREATE FUNCTION public.agent_kind_unlinked() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = OLD.actor_id)
        AND NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = NEW.revoked_by) THEN
        PERFORM public.agent_kind_note(NEW.tenant_id, NEW.book_id, NEW.transaction_id, 'undone',
            public.agent_other_account(NEW.tenant_id, NEW.book_id, NEW.transaction_id, '{}'::jsonb, NEW.entry_id));
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER agent_kind_unlinked AFTER UPDATE ON public.bank_transaction_links
    FOR EACH ROW WHEN (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL) EXECUTE FUNCTION public.agent_kind_unlinked();

-- The owner accepts an offer to record a kind by a rule they just made from
-- it; the kind is trusted again.
CREATE FUNCTION public.agent_kind_rule(t uuid, b uuid, account uuid, dir text, party text, rule uuid) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE updated integer;
BEGIN
    IF NOT public.rule_owner_allowed(t, b) THEN
        RAISE EXCEPTION 'only the owner accepts an offer' USING ERRCODE = '42501';
    END IF;
    UPDATE public.agent_kinds SET rule_id = rule, level = 'record', offered_at = NULL, updated_at = clock_timestamp()
        WHERE tenant_id = t AND book_id = b AND account_id = account AND direction = dir AND counterparty_key = party
        AND EXISTS (SELECT 1 FROM public.recording_rules r WHERE r.tenant_id = t AND r.book_id = b AND r.id = rule
            AND r.account_id = account AND r.direction = dir AND r.counterparty_key = party);
    GET DIAGNOSTICS updated = ROW_COUNT;
    RETURN updated = 1;
END $$;

-- A rule made from an offer of a kind waits while the owner has corrected
-- that kind since; recording by it is refused until trust is earned again.
CREATE FUNCTION public.rule_kind_trusted() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.basis = 'rule' AND NEW.revoked_at IS NULL AND EXISTS (SELECT 1 FROM public.agent_kinds k
            WHERE k.tenant_id = NEW.tenant_id AND k.book_id = NEW.book_id AND k.rule_id = NEW.rule_id AND k.level = 'draft') THEN
        RAISE EXCEPTION 'the owner corrected this kind of activity since making the rule; it''s a proposal until trust is earned again' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER rule_kind_trusted AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.rule_kind_trusted();

-- Recording when confident: what the link and its entry must be. Checked
-- at commit, by account types only.
CREATE FUNCTION public.confident_link_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE x public.bank_transactions%ROWTYPE; o record; lines integer; kind_level text;
BEGIN
    IF NEW.basis NOT IN ('judgement', 'transfer') OR NEW.revoked_at IS NOT NULL OR NOT public.ledger_confident_recording() THEN
        RETURN NULL;
    END IF;
    SELECT * INTO STRICT x FROM public.bank_transactions WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.transaction_id;
    SELECT coalesce((SELECT k.level FROM public.agent_kinds k WHERE k.tenant_id = NEW.tenant_id AND k.book_id = NEW.book_id
        AND k.account_id = NEW.account_id AND k.direction = CASE WHEN x.amount_minor > 0 THEN 'in' ELSE 'out' END
        AND k.counterparty_key = left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200)),
        public.agent_default_trust(NEW.tenant_id, NEW.book_id)) INTO kind_level;
    -- A transfer the ledger matched still keeps to the limit and to trust.
    IF NEW.basis = 'transfer' THEN
        IF abs(x.amount_minor) > public.agent_amount_cap(NEW.tenant_id, NEW.book_id) THEN
            RAISE EXCEPTION 'this transfer is over the Book''s limit for an agent recording on its own; it''s a proposal' USING ERRCODE = '23514';
        END IF;
        IF x.amount_minor < 0 AND EXISTS (SELECT 1 FROM public.agent_kinds k WHERE k.tenant_id = NEW.tenant_id AND k.book_id = NEW.book_id
                AND k.account_id = NEW.account_id AND k.direction = 'out' AND k.level = 'draft'
                AND k.counterparty_key = left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200)) THEN
            RAISE EXCEPTION 'this kind of activity waits for the owner until the agent earns trust for it' USING ERRCODE = '23514';
        END IF;
        RETURN NULL;
    END IF;
    IF length(btrim(coalesce(NEW.reason, ''))) = 0 THEN
        RAISE EXCEPTION 'an agent recording on its own gives its reason' USING ERRCODE = '23514';
    END IF;
    SELECT count(*) INTO lines FROM public.lines l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.entry_id = NEW.entry_id;
    SELECT a.id, a.class, a.role, l.side INTO o FROM public.lines l JOIN public.accounts a ON a.tenant_id = l.tenant_id AND a.book_id = l.book_id AND a.id = l.account_id
        WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.entry_id = NEW.entry_id AND l.account_id <> NEW.account_id LIMIT 1;
    IF lines <> 2 OR o.id IS NULL THEN
        RAISE EXCEPTION 'an agent recording on its own records a whole item to one account; a split is a proposal' USING ERRCODE = '23514';
    END IF;
    IF abs(x.amount_minor) > public.agent_amount_cap(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'this is over the Book''s limit for an agent recording on its own; it''s a proposal' USING ERRCODE = '23514';
    END IF;
    IF o.class = 'equity' THEN
        RAISE EXCEPTION 'money between the owner and the Book is asked, or recorded by a rule' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (SELECT 1 FROM public.agent_tax_sensitive_accounts s WHERE s.tenant_id = NEW.tenant_id AND s.book_id = NEW.book_id AND s.account_id = o.id) THEN
        RAISE EXCEPTION 'this account is reached only by the owner''s rules; it''s a proposal' USING ERRCODE = '23514';
    END IF;
    IF o.role IN ('cash', 'card', 'loan') OR EXISTS (SELECT 1 FROM public.bank_accounts b WHERE b.tenant_id = NEW.tenant_id AND b.book_id = NEW.book_id AND b.account_id = o.id) THEN
        RAISE EXCEPTION 'money between your own accounts, or on a loan, is a transfer or a rule, not a judgement' USING ERRCODE = '23514';
    END IF;
    IF o.class = 'income' AND o.side = 'debit' OR o.class = 'expense' AND o.side = 'credit' THEN
        RAISE EXCEPTION 'this reduces income or an expense, as a refund does; refunds are asked' USING ERRCODE = '23514';
    END IF;
    IF kind_level = 'draft' THEN
        RAISE EXCEPTION 'this kind of activity waits for the owner until the agent earns trust for it (a correction, or a skills test it didn''t pass); it''s a proposal' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER confident_link_final AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.confident_link_final();

-- Under a recording grant, entries and links come from rules, transfers and
-- matches; a confident grant adds the agent's own judgement, checked above.
CREATE OR REPLACE FUNCTION public.rule_recording_entry_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id
            AND l.entry_id = NEW.id AND l.revoked_at IS NULL
            AND (l.basis IN ('rule', 'transfer') OR l.basis = 'judgement' AND public.ledger_confident_recording())
            AND public.ledger_written_here(l.xmin)) THEN
        RAISE EXCEPTION 'within the rules, an entry records bank activity by a rule or as a transfer' USING ERRCODE = '42501';
    END IF;
    RETURN NULL;
END $$;
CREATE OR REPLACE FUNCTION public.rule_recording_link_check() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF public.ledger_rule_recording() AND NEW.basis NOT IN ('rule', 'transfer', 'existing_entry')
        AND NOT (NEW.basis = 'judgement' AND public.ledger_confident_recording()) THEN
        RAISE EXCEPTION 'within the rules, activity is recorded by a rule, a transfer or a match' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END $$;

-- The built-in agent's skills test: the result the engine measured for the
-- model, which sets where it starts. Changing the model clears it.
ALTER TABLE public.agent_settings
    ADD COLUMN skills_level text CHECK (skills_level IN ('confident', 'rules', 'drafts')),
    ADD COLUMN skills_score integer CHECK (skills_score >= 0),
    ADD COLUMN skills_total integer CHECK (skills_total > 0),
    ADD COLUMN skills_model text CHECK (length(skills_model) <= 200),
    ADD COLUMN skills_tested_at timestamptz,
    -- Where kinds with no history start: 'record' once the model passed the
    -- skills test, 'draft' otherwise.
    ADD COLUMN default_trust text NOT NULL DEFAULT 'draft' CHECK (default_trust IN ('record', 'draft')),
    ADD CONSTRAINT agent_settings_skills CHECK ((skills_level IS NULL) = (skills_tested_at IS NULL) AND (skills_score IS NULL OR skills_score <= skills_total));
CREATE FUNCTION public.agent_record_skills(t uuid, b uuid, tested_model text, tested_level text, tested_score integer, tested_total integer) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE updated integer;
BEGIN
    -- Bypasses the owner-only guard for these columns alone.
    PERFORM set_config('vult.agent_skills', 'on', true);
    UPDATE public.agent_settings s SET skills_level = tested_level, skills_score = tested_score, skills_total = tested_total, skills_model = left(tested_model, 200),
        skills_tested_at = clock_timestamp(), default_trust = CASE WHEN tested_level = 'confident' THEN 'record' ELSE 'draft' END
        WHERE s.tenant_id = t AND s.book_id = b AND s.model = tested_model;
    GET DIAGNOSTICS updated = ROW_COUNT;
    PERFORM set_config('vult.agent_skills', '', true);
    RETURN updated = 1;
END $$;
CREATE OR REPLACE FUNCTION public.agent_settings_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent settings are turned off, never deleted' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' AND coalesce(current_setting('vult.agent_skills', true), '') = 'on'
        AND (to_jsonb(NEW) - ARRAY['skills_level', 'skills_score', 'skills_total', 'skills_model', 'skills_tested_at', 'default_trust'])
            = (to_jsonb(OLD) - ARRAY['skills_level', 'skills_score', 'skills_total', 'skills_model', 'skills_tested_at', 'default_trust']) THEN
        RETURN NEW;
    END IF;
    IF NOT public.rule_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner changes the built-in agent' USING ERRCODE = '42501';
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        IF TG_OP = 'UPDATE' AND NEW.revision <> OLD.revision + 1 OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'agent settings changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
        IF TG_OP = 'INSERT' THEN
            NEW.default_trust := 'draft';
        ELSIF NEW.default_trust IS DISTINCT FROM OLD.default_trust THEN
            RAISE EXCEPTION 'the skills test sets where trust starts' USING ERRCODE = '42501';
        END IF;
        -- A different model hasn't taken the test.
        IF TG_OP = 'UPDATE' AND ROW(NEW.provider, NEW.base_url, NEW.model) IS DISTINCT FROM ROW(OLD.provider, OLD.base_url, OLD.model) THEN
            NEW.skills_level := NULL; NEW.skills_score := NULL; NEW.skills_total := NULL; NEW.skills_model := NULL; NEW.skills_tested_at := NULL;
            NEW.default_trust := 'draft';
        END IF;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id
            AND c.id = NEW.connection_id AND c.access = 'propose') THEN
        RAISE EXCEPTION 'the built-in agent works through a proposing connection' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['agent_policy', 'agent_tax_sensitive_accounts', 'agent_kinds', 'agent_kind_events'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY agent_read ON public.%I FOR SELECT USING (public.ledger_allowed(tenant_id, book_id, false) OR access.owner_allowed(tenant_id))', tab);
    END LOOP;
    FOREACH tab IN ARRAY ARRAY['agent_policy', 'agent_tax_sensitive_accounts'] LOOP
        EXECUTE format('CREATE POLICY owner_insert ON public.%I FOR INSERT WITH CHECK (public.rule_owner_allowed(tenant_id, book_id) OR public.ledger_allowed(tenant_id, book_id, true))', tab);
        EXECUTE format('CREATE POLICY owner_update ON public.%I FOR UPDATE USING (public.rule_owner_allowed(tenant_id, book_id)) WITH CHECK (public.rule_owner_allowed(tenant_id, book_id))', tab);
        EXECUTE format('CREATE POLICY owner_delete ON public.%I FOR DELETE USING (public.rule_owner_allowed(tenant_id, book_id))', tab);
    END LOOP;
END $$;

REVOKE ALL ON FUNCTION public.ledger_confident_recording(), public.agent_amount_cap(uuid, uuid), public.agent_kind_note(uuid, uuid, uuid, text, uuid), public.agent_default_trust(uuid, uuid), public.agent_other_account(uuid, uuid, uuid, jsonb, uuid),
    public.agent_kind_decision(), public.agent_kind_undone(), public.agent_kind_unlinked(), public.confident_link_final(),
    public.agent_limits_guard(), public.rule_kind_trusted(), public.agent_kind_rule(uuid, uuid, uuid, text, text, uuid), public.agent_record_skills(uuid, uuid, text, text, integer, integer) FROM PUBLIC;

-- Continuity: what an agent asked and was told, what it said it would do,
-- and what it remembers about the person. Every row is one the person can
-- see and change; money facts are never stored here, only read fresh.

-- An agent connection of the Book (proposing or writing), or the owner.
CREATE FUNCTION public.agent_note_allowed(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT public.ledger_context(t, b) AND (EXISTS (SELECT 1 FROM public.connections c
        WHERE c.tenant_id = t AND c.book_id = b AND c.id::text = coalesce(current_setting('vult.connection_id', true), '')
        AND c.actor_id::text = coalesce(current_setting('vult.actor_id', true), '')
        AND c.revoked_at IS NULL AND c.access IN ('propose', 'write'))
    OR coalesce(current_setting('vult.connection_id', true), '') = '' AND access.owner_decision_allowed(t, b))
$$;
CREATE FUNCTION public.agent_note_owner(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT public.ledger_context(t, b) AND coalesce(current_setting('vult.connection_id', true), '') = '' AND access.owner_decision_allowed(t, b)
$$;

-- A question is asked once per subject. An answer settles it; only the
-- owner changing the answer reopens it, as a new question.
CREATE TABLE public.agent_questions (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    connection_id uuid,
    actor_id uuid NOT NULL,
    subject_kind text NOT NULL CHECK (subject_kind IN ('bank_item', 'kind', 'goal', 'preference', 'account', 'other')),
    subject_key text NOT NULL CHECK (length(subject_key) BETWEEN 1 AND 200),
    question text NOT NULL CHECK (length(btrim(question)) BETWEEN 1 AND 1000),
    answer jsonb CHECK (answer IS NULL OR octet_length(answer::text) <= 4096),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'answered', 'withdrawn', 'superseded')),
    asked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    answered_at timestamptz,
    answered_by uuid,
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 1000),
    UNIQUE (tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    CHECK ((status = 'answered') <= (answer IS NOT NULL AND answered_at IS NOT NULL))
);
CREATE UNIQUE INDEX agent_questions_once ON public.agent_questions(tenant_id, book_id, subject_kind, subject_key) WHERE status IN ('open', 'answered');

-- Loose ends: what an agent said it would do, or was asked to remind.
CREATE TABLE public.agent_loops (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    connection_id uuid,
    actor_id uuid NOT NULL,
    request_key text NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
    kind text NOT NULL CHECK (kind IN ('promise', 'follow_up', 'reminder')),
    subject_kind text CHECK (subject_kind IN ('bank_item', 'question', 'account', 'other')),
    subject_key text CHECK (length(subject_key) BETWEEN 1 AND 200),
    text text NOT NULL CHECK (length(btrim(text)) BETWEEN 1 AND 1000),
    due_on date,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done', 'dropped')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    closed_at timestamptz,
    closed_reason text NOT NULL DEFAULT '' CHECK (length(closed_reason) <= 1000),
    UNIQUE (tenant_id, book_id, actor_id, request_key),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    CHECK ((subject_kind IS NULL) = (subject_key IS NULL)),
    CHECK ((status = 'open') = (closed_at IS NULL))
);

-- What the agent remembers about the person: tone and preferences, never
-- money and never standing instructions that change the books (those are
-- rules). Small by design.
CREATE TABLE public.agent_profile (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    goal text NOT NULL DEFAULT '' CHECK (length(goal) <= 280),
    preferences jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(preferences) = 'object' AND octet_length(preferences::text) <= 2048),
    instructions text NOT NULL DEFAULT '' CHECK (length(instructions) <= 1024),
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 1000),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

-- When each person last looked, for "since you were last here".
CREATE TABLE public.agent_seen (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    person uuid NOT NULL,
    seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, person),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

CREATE FUNCTION public.agent_note_guard() RETURNS trigger
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
    IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
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
CREATE TRIGGER agent_note_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_questions FOR EACH ROW EXECUTE FUNCTION public.agent_note_guard();
CREATE TRIGGER agent_note_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_loops FOR EACH ROW EXECUTE FUNCTION public.agent_note_guard();
CREATE TRIGGER agent_note_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_profile FOR EACH ROW EXECUTE FUNCTION public.agent_note_guard();
CREATE TRIGGER agent_note_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_seen FOR EACH ROW EXECUTE FUNCTION public.agent_note_guard();

-- A loop about a bank item closes when the item is recorded; one about a
-- question, when it's answered or withdrawn.
CREATE FUNCTION public.agent_loops_close() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    PERFORM set_config('vult.loop_autoclose', 'on', true);
    IF TG_TABLE_NAME = 'bank_transaction_links' THEN
        UPDATE public.agent_loops SET status = 'done', closed_at = clock_timestamp(), closed_reason = 'The item was recorded.'
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND status = 'open' AND subject_kind = 'bank_item' AND subject_key = NEW.transaction_id::text;
    ELSIF NEW.status IN ('answered', 'withdrawn') THEN
        UPDATE public.agent_loops SET status = 'done', closed_at = clock_timestamp(),
            closed_reason = CASE WHEN NEW.status = 'answered' THEN 'The question was answered.' ELSE 'The question was withdrawn.' END
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND status = 'open' AND subject_kind = 'question' AND subject_key = NEW.id::text;
    END IF;
    PERFORM set_config('vult.loop_autoclose', '', true);
    RETURN NULL;
END $$;
CREATE TRIGGER agent_loops_close AFTER INSERT ON public.bank_transaction_links FOR EACH ROW EXECUTE FUNCTION public.agent_loops_close();
CREATE TRIGGER agent_loops_close AFTER UPDATE ON public.agent_questions
    FOR EACH ROW WHEN (OLD.status = 'open' AND NEW.status <> 'open') EXECUTE FUNCTION public.agent_loops_close();

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['agent_questions', 'agent_loops', 'agent_profile', 'agent_seen'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY agent_read ON public.%I FOR SELECT USING (public.ledger_allowed(tenant_id, book_id, false) OR access.owner_allowed(tenant_id))', tab);
    END LOOP;
    FOREACH tab IN ARRAY ARRAY['agent_questions', 'agent_loops', 'agent_profile'] LOOP
        EXECUTE format('CREATE POLICY agent_note_insert ON public.%I FOR INSERT WITH CHECK (public.agent_note_allowed(tenant_id, book_id))', tab);
        EXECUTE format('CREATE POLICY agent_note_update ON public.%I FOR UPDATE USING (public.agent_note_allowed(tenant_id, book_id)) WITH CHECK (public.agent_note_allowed(tenant_id, book_id))', tab);
    END LOOP;
END $$;
CREATE POLICY owner_seen_insert ON public.agent_seen FOR INSERT WITH CHECK (access.owner_allowed(tenant_id));
CREATE POLICY owner_seen_update ON public.agent_seen FOR UPDATE USING (access.owner_allowed(tenant_id)) WITH CHECK (access.owner_allowed(tenant_id));
REVOKE ALL ON FUNCTION public.agent_note_allowed(uuid, uuid), public.agent_note_owner(uuid, uuid), public.agent_note_guard(), public.agent_loops_close() FROM PUBLIC;

-- A starter chart also goes into a Book whose only accounts are the ones
-- its bank accounts were linked to, as when a Book starts from the bank.
CREATE OR REPLACE FUNCTION public.ledger_starter_chart_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.operation = 'apply_starter_chart' AND NOT public.ledger_restoring(NEW.tenant_id, NEW.book_id) AND EXISTS(
        SELECT 1 FROM public.accounts a WHERE a.tenant_id = NEW.tenant_id AND a.book_id = NEW.book_id
        AND NOT public.ledger_written_here(a.xmin)
        AND NOT EXISTS (SELECT 1 FROM public.bank_accounts b WHERE b.tenant_id = a.tenant_id AND b.book_id = a.book_id AND b.account_id = a.id)) THEN
        RAISE EXCEPTION 'a starter chart applies only to a Book with no accounts, or only those its bank accounts are linked to' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;

-- Stopping an agent is the easiest thing an owner does: any valid sign-in may
-- disconnect one, without signing in again. The change it allows only ends a
-- connection; giving or changing access still needs a fresh sign-in.
CREATE POLICY owner_disconnect ON public.connections FOR UPDATE
    USING (access.owner_allowed(tenant_id) AND revoked_at IS NULL)
    WITH CHECK (access.owner_allowed(tenant_id) AND revoked_at IS NOT NULL);
CREATE FUNCTION public.connection_disconnect_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    -- Without a fresh sign-in, an owner's change to a connection may only
    -- set when it was disconnected.
    IF NOT access.owner_write_allowed(NEW.tenant_id) AND access.owner_allowed(NEW.tenant_id)
        AND (OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL
            OR (to_jsonb(NEW) - 'revoked_at') IS DISTINCT FROM (to_jsonb(OLD) - 'revoked_at')) THEN
        RAISE EXCEPTION 'owner authorization required' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION public.connection_disconnect_guard() FROM PUBLIC;
CREATE TRIGGER connection_disconnect_guard BEFORE UPDATE ON public.connections
    FOR EACH ROW EXECUTE FUNCTION public.connection_disconnect_guard();

-- Narrowing what an agent may do needs only a valid sign-in, as stopping it
-- does: turning a rule off, recording by rules instead of when sure, drafts
-- only, a lower limit, or keeping an account to the owner's rules. Giving
-- an agent more still needs a recent sign-in. The guards below allow each
-- such change only when it narrows.
CREATE FUNCTION public.rule_owner_narrowing(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SET search_path = pg_catalog AS $$
SELECT NOT public.ledger_import_owner() AND access.owner_document_allowed(t, b)
$$;
REVOKE ALL ON FUNCTION public.rule_owner_narrowing(uuid, uuid) FROM PUBLIC;

CREATE POLICY rule_owner_narrow ON public.recording_rules FOR UPDATE
    USING (public.rule_owner_narrowing(tenant_id, book_id)) WITH CHECK (public.rule_owner_narrowing(tenant_id, book_id));
CREATE POLICY rule_owner_narrow ON public.rule_recording_grants FOR UPDATE
    USING (public.rule_owner_narrowing(tenant_id, book_id)) WITH CHECK (public.rule_owner_narrowing(tenant_id, book_id));
CREATE POLICY owner_narrow_insert ON public.agent_policy FOR INSERT WITH CHECK (public.rule_owner_narrowing(tenant_id, book_id));
CREATE POLICY owner_narrow_update ON public.agent_policy FOR UPDATE
    USING (public.rule_owner_narrowing(tenant_id, book_id)) WITH CHECK (public.rule_owner_narrowing(tenant_id, book_id));
CREATE POLICY owner_narrow_insert ON public.agent_tax_sensitive_accounts FOR INSERT WITH CHECK (public.rule_owner_narrowing(tenant_id, book_id));

-- Whether this change to a rule, a grant or the limits only narrows what
-- agents may do.
CREATE FUNCTION public.agent_change_narrows(tab text, op text, o jsonb, n jsonb) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT CASE
    WHEN tab = 'recording_rules' AND op = 'UPDATE' THEN
        (o ->> 'active')::boolean AND NOT (n ->> 'active')::boolean
        AND (n - ARRAY['active', 'actor_id', 'updated_at', 'revision']) = (o - ARRAY['active', 'actor_id', 'updated_at', 'revision'])
    WHEN tab = 'rule_recording_grants' AND op = 'UPDATE' THEN
        o ->> 'revoked_at' IS NULL AND (
            n ->> 'revoked_at' IS NOT NULL AND (n - 'revoked_at') = (o - 'revoked_at')
            OR n ->> 'revoked_at' IS NULL AND o ->> 'recording_mode' = 'confident' AND n ->> 'recording_mode' = 'rules'
                AND (n - 'recording_mode') = (o - 'recording_mode'))
    WHEN tab = 'agent_policy' AND op = 'UPDATE' THEN
        (n ->> 'amount_cap_minor')::bigint < (o ->> 'amount_cap_minor')::bigint
        AND (n - ARRAY['amount_cap_minor', 'revision', 'actor_id', 'updated_at']) = (o - ARRAY['amount_cap_minor', 'revision', 'actor_id', 'updated_at'])
    WHEN tab = 'agent_policy' AND op = 'INSERT' THEN
        (n ->> 'amount_cap_minor')::bigint < public.agent_amount_cap((n ->> 'tenant_id')::uuid, (n ->> 'book_id')::uuid)
    WHEN tab = 'agent_tax_sensitive_accounts' AND op = 'INSERT' THEN true
    ELSE false
END
$$;
REVOKE ALL ON FUNCTION public.agent_change_narrows(text, text, jsonb, jsonb) FROM PUBLIC;

-- The owner may make this change: from a recent sign-in, or from any valid
-- one when it only narrows what agents may do.
CREATE FUNCTION public.agent_owner_change_allowed(t uuid, b uuid, tab text, op text, o jsonb, n jsonb) RETURNS boolean
    LANGUAGE sql VOLATILE SET search_path = pg_catalog AS $$
SELECT public.rule_owner_allowed(t, b) OR public.rule_owner_narrowing(t, b) AND public.agent_change_narrows(tab, op, o, n)
$$;
REVOKE ALL ON FUNCTION public.agent_owner_change_allowed(uuid, uuid, text, text, jsonb, jsonb) FROM PUBLIC;

-- The rule and grant guards, as before, admitting a narrowing change on any
-- valid sign-in.
CREATE OR REPLACE FUNCTION public.recording_rule_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE a public.accounts%ROWTYPE; o public.accounts%ROWTYPE;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a rule is turned off, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.agent_owner_change_allowed(NEW.tenant_id, NEW.book_id, TG_TABLE_NAME, TG_OP,
            CASE WHEN TG_OP = 'UPDATE' THEN to_jsonb(OLD) END, to_jsonb(NEW)) THEN
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
CREATE OR REPLACE FUNCTION public.rule_grant_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a grant is revoked, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.agent_owner_change_allowed(NEW.tenant_id, NEW.book_id, TG_TABLE_NAME, TG_OP,
            CASE WHEN TG_OP = 'UPDATE' THEN to_jsonb(OLD) END, to_jsonb(NEW)) THEN
        RAISE EXCEPTION 'only the owner grants recording within rules' USING ERRCODE = '42501';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id
            AND c.id = NEW.connection_id AND c.access = 'propose' AND c.revoked_at IS NULL) THEN
        RAISE EXCEPTION 'recording within rules is granted to a proposing connection' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
