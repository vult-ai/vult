-- The built-in agent's model and key, once for the whole Vult: every Book
-- runs its agent with them, as Books share the Vult's bank provider keys.
-- Each Book keeps its own on/off switch, its connection, its limits and
-- its skills result. A Book row whose provider is 'vult' uses these.
CREATE TABLE public.vult_agent_settings (
    tenant_id uuid PRIMARY KEY REFERENCES public.tenants(id),
    provider text NOT NULL CHECK (provider IN ('openai_compatible', 'anthropic')),
    base_url text NOT NULL CHECK (length(base_url) <= 512 AND base_url ~ '^https?://[^[:space:]]+$'),
    model text NOT NULL CHECK (length(model) BETWEEN 1 AND 200),
    -- The owner's API key, sealed by the application with a key that never
    -- enters this database. None for a model on this computer.
    key_sealed bytea CHECK (octet_length(key_sealed) BETWEEN 34 AND 4096),
    key_version integer CHECK (key_version > 0),
    -- A key carried forward from one Book's settings is still sealed for
    -- that Book; the server seals it for the Vult the next time it opens it.
    sealed_for uuid,
    -- Whether the provider accepts the key, as the agent last found:
    -- unchecked until it's used, working, or rejected with why (refused,
    -- expired or out of credit). A new key starts unchecked.
    key_state text NOT NULL DEFAULT 'unchecked' CHECK (key_state IN ('unchecked', 'working', 'rejected')),
    key_problem text NOT NULL DEFAULT '' CHECK (key_problem IN ('', 'refused', 'expired', 'out_of_credit')),
    key_checked_at timestamptz,
    -- When the owner says the key expires, for a reminder a week before.
    key_expires_on date,
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((key_sealed IS NULL) = (key_version IS NULL)),
    CHECK (sealed_for IS NULL OR key_sealed IS NOT NULL),
    CHECK ((key_state = 'rejected') = (key_problem <> ''))
);

-- The owner changes the Vult's model and key from a fresh sign-in, never
-- through an agent's connection.
CREATE FUNCTION public.vult_agent_owner_allowed(t uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = ''
    AND coalesce(current_setting('vult.owner_posting', true), '') = ''
    AND access.owner_write_allowed(t)
$$;

CREATE FUNCTION public.vult_agent_settings_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'the Vult''s agent settings are changed, never deleted' USING ERRCODE = '23514';
    END IF;
    -- The server re-seals a carried-forward key for the Vult: only the key
    -- changes, and only from a Book's seal to the Vult's.
    IF TG_OP = 'UPDATE' AND coalesce(current_setting('vult.agent_reseal', true), '') = 'on'
        AND OLD.sealed_for IS NOT NULL AND NEW.sealed_for IS NULL AND NEW.key_sealed IS NOT NULL
        AND (to_jsonb(NEW) - ARRAY['key_sealed', 'key_version', 'sealed_for']) = (to_jsonb(OLD) - ARRAY['key_sealed', 'key_version', 'sealed_for']) THEN
        RETURN NEW;
    END IF;
    -- The agent records whether the provider accepted the key: only those
    -- columns change.
    IF TG_OP = 'UPDATE' AND coalesce(current_setting('vult.agent_key_state', true), '') = 'on'
        AND (to_jsonb(NEW) - ARRAY['key_state', 'key_problem', 'key_checked_at']) = (to_jsonb(OLD) - ARRAY['key_state', 'key_problem', 'key_checked_at']) THEN
        RETURN NEW;
    END IF;
    -- A new key, or a new address, hasn't been tried yet.
    IF TG_OP = 'UPDATE' AND (NEW.key_sealed IS DISTINCT FROM OLD.key_sealed AND NEW.sealed_for IS NOT DISTINCT FROM OLD.sealed_for
        OR NEW.base_url IS DISTINCT FROM OLD.base_url) OR TG_OP = 'INSERT' AND NOT public.ledger_import_owner() THEN
        NEW.key_state := 'unchecked'; NEW.key_problem := ''; NEW.key_checked_at := NULL;
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NOT public.vult_agent_owner_allowed(NEW.tenant_id) THEN
            RAISE EXCEPTION 'only the owner changes the Vult''s agent model and key' USING ERRCODE = '42501';
        END IF;
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        IF TG_OP = 'UPDATE' AND (NEW.revision <> OLD.revision + 1 OR NEW.tenant_id <> OLD.tenant_id) OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'the Vult''s agent settings changed concurrently' USING ERRCODE = '23514';
        END IF;
        IF NEW.sealed_for IS NOT NULL AND (TG_OP = 'INSERT' OR NEW.key_sealed IS DISTINCT FROM OLD.key_sealed) THEN
            RAISE EXCEPTION 'a new key is sealed for the Vult' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER vult_agent_settings_guard BEFORE INSERT OR UPDATE OR DELETE ON public.vult_agent_settings
    FOR EACH ROW EXECUTE FUNCTION public.vult_agent_settings_guard();

-- A different model hasn't taken the skills test in any Book.
CREATE FUNCTION public.vult_agent_model_changed() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'INSERT' OR ROW(NEW.provider, NEW.base_url, NEW.model) IS DISTINCT FROM ROW(OLD.provider, OLD.base_url, OLD.model) THEN
        PERFORM set_config('vult.agent_skills', 'on', true);
        UPDATE public.agent_settings s SET skills_level = NULL, skills_score = NULL, skills_total = NULL, skills_model = NULL,
            skills_tested_at = NULL, default_trust = 'draft'
            WHERE s.tenant_id = NEW.tenant_id AND s.provider = 'vult' AND s.skills_model IS DISTINCT FROM NEW.model
                AND (s.skills_level IS NOT NULL OR s.default_trust <> 'draft')
                AND NOT EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = s.tenant_id AND d.book_id = s.book_id);
        PERFORM set_config('vult.agent_skills', '', true);
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER vult_agent_model_changed AFTER INSERT OR UPDATE ON public.vult_agent_settings
    FOR EACH ROW EXECUTE FUNCTION public.vult_agent_model_changed();

ALTER TABLE public.vult_agent_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.vult_agent_settings FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.vult_agent_settings FROM PUBLIC;
-- Read only by the owner; agents never read it, sealed or not. The engine
-- reads it through agent_secret_for.
CREATE POLICY owner_read ON public.vult_agent_settings FOR SELECT USING (access.owner_allowed(tenant_id));
CREATE POLICY owner_insert ON public.vult_agent_settings FOR INSERT WITH CHECK (public.vult_agent_owner_allowed(tenant_id));
CREATE POLICY owner_update ON public.vult_agent_settings FOR UPDATE
    USING (public.vult_agent_owner_allowed(tenant_id)) WITH CHECK (public.vult_agent_owner_allowed(tenant_id));

-- A Book's row may now say 'vult': use the Vult's model and key.
DO $$
DECLARE c record;
BEGIN
    FOR c IN SELECT conname FROM pg_constraint WHERE conrelid = 'public.agent_settings'::regclass AND contype = 'c'
        AND pg_get_constraintdef(oid) LIKE '%provider%' LOOP
        EXECUTE format('ALTER TABLE public.agent_settings DROP CONSTRAINT %I', c.conname);
    END LOOP;
END $$;
ALTER TABLE public.agent_settings
    ADD CONSTRAINT agent_settings_provider CHECK (provider IN ('openai_compatible', 'anthropic', 'service', 'vult')),
    ADD CONSTRAINT agent_settings_model CHECK (
        provider IN ('service', 'vult') AND base_url = '' AND model = '' AND key_sealed IS NULL
        OR provider IN ('openai_compatible', 'anthropic') AND base_url ~ '^https?://[^[:space:]]+$' AND length(model) >= 1);

-- Carry each Vult's most recent key forward, with its Book's model, and
-- point every Book at it. The other Books' copies of a key are dropped; a
-- Book being deleted is left as it is. Skills results stay only where the
-- Book had tested the same model.
INSERT INTO public.vult_agent_settings(tenant_id, provider, base_url, model, key_sealed, key_version, sealed_for, revision, actor_id)
SELECT DISTINCT ON (s.tenant_id) s.tenant_id, s.provider, s.base_url, s.model, s.key_sealed, s.key_version,
    CASE WHEN s.key_sealed IS NOT NULL THEN s.book_id END, 1, s.actor_id
    FROM public.agent_settings s WHERE s.provider IN ('openai_compatible', 'anthropic')
    ORDER BY s.tenant_id, s.key_sealed IS NOT NULL DESC, s.updated_at DESC, s.book_id;
UPDATE public.agent_settings s SET provider = 'vult', base_url = '', model = '', key_sealed = NULL, key_version = NULL,
    skills_level = CASE WHEN s.model = v.model THEN s.skills_level END,
    skills_score = CASE WHEN s.model = v.model THEN s.skills_score END,
    skills_total = CASE WHEN s.model = v.model THEN s.skills_total END,
    skills_model = CASE WHEN s.model = v.model THEN s.skills_model END,
    skills_tested_at = CASE WHEN s.model = v.model THEN s.skills_tested_at END,
    default_trust = CASE WHEN s.model = v.model THEN s.default_trust ELSE 'draft' END
    FROM public.vult_agent_settings v
    WHERE v.tenant_id = s.tenant_id AND s.provider IN ('openai_compatible', 'anthropic')
        AND NOT EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = s.tenant_id AND d.book_id = s.book_id);

-- What the engine reads for a Book: the Book's switch and connection, with
-- the Vult's model and still-sealed key when the Book uses them.
CREATE FUNCTION public.agent_secret_for(t uuid, b uuid)
    RETURNS TABLE(provider text, base_url text, model text, key_sealed bytea, key_version integer, enabled boolean,
        connection_id uuid, actor_id uuid, vult_key boolean, sealed_for uuid, key_state text, key_checked_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT coalesce(v.provider, s.provider), coalesce(v.base_url, s.base_url), coalesce(v.model, s.model),
    CASE WHEN s.provider = 'vult' THEN v.key_sealed ELSE s.key_sealed END,
    CASE WHEN s.provider = 'vult' THEN v.key_version ELSE s.key_version END,
    s.enabled AND (s.provider <> 'vult' OR v.tenant_id IS NOT NULL), s.connection_id, c.actor_id, s.provider = 'vult', v.sealed_for,
    coalesce(v.key_state, ''), v.key_checked_at
    FROM public.agent_settings s JOIN public.connections c ON c.tenant_id = s.tenant_id AND c.book_id = s.book_id AND c.id = s.connection_id
    LEFT JOIN public.vult_agent_settings v ON s.provider = 'vult' AND v.tenant_id = s.tenant_id
    WHERE s.tenant_id = t AND s.book_id = b
$$;
DROP FUNCTION public.agent_settings_for(uuid, uuid);

-- The Vult's model and still-sealed key alone, for testing the connection
-- from a Book whose agent isn't set up yet.
CREATE FUNCTION public.vult_agent_secret(t uuid)
    RETURNS TABLE(provider text, base_url text, model text, key_sealed bytea, key_version integer, sealed_for uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT v.provider, v.base_url, v.model, v.key_sealed, v.key_version, v.sealed_for FROM public.vult_agent_settings v WHERE v.tenant_id = t
$$;

-- The agent records whether the provider accepted the key it used. A key
-- replaced meanwhile is left alone: the record is about the key it names.
CREATE FUNCTION public.vult_agent_key_checked(t uuid, used bytea, state text, problem text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE updated integer;
BEGIN
    IF state NOT IN ('working', 'rejected') OR (state = 'rejected') <> (problem IN ('refused', 'expired', 'out_of_credit')) THEN
        RAISE EXCEPTION 'unknown key state' USING ERRCODE = '22023';
    END IF;
    PERFORM set_config('vult.agent_key_state', 'on', true);
    UPDATE public.vult_agent_settings v SET key_state = state, key_problem = CASE WHEN state = 'rejected' THEN problem ELSE '' END, key_checked_at = clock_timestamp()
        WHERE v.tenant_id = t AND v.key_sealed = used
            AND (v.key_state, v.key_problem) IS DISTINCT FROM (state, CASE WHEN state = 'rejected' THEN problem ELSE '' END)
            OR v.tenant_id = t AND v.key_sealed = used AND state = 'rejected' AND v.key_checked_at < clock_timestamp() - interval '1 hour';
    GET DIAGNOSTICS updated = ROW_COUNT;
    PERFORM set_config('vult.agent_key_state', '', true);
    RETURN updated = 1;
END $$;

-- The owner removes the Vult's key: it is cleared, and every Book's agent
-- is turned off until a new key is saved. The owner, from a fresh sign-in,
-- never through an agent; the caller checks the Book revision it was shown.
CREATE FUNCTION public.vult_agent_remove_key(t uuid, who uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE turned_off integer := 0; selected text; b uuid;
BEGIN
    IF NOT public.vult_agent_owner_allowed(t) OR who::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'only the owner removes the Vult''s agent key' USING ERRCODE = '42501';
    END IF;
    UPDATE public.vult_agent_settings v SET key_sealed = NULL, key_version = NULL, sealed_for = NULL, key_state = 'unchecked',
        key_problem = '', key_checked_at = NULL, key_expires_on = NULL, revision = revision + 1, actor_id = who
        WHERE v.tenant_id = t AND v.key_sealed IS NOT NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'This Vult keeps no AI key.' USING ERRCODE = '23514';
    END IF;
    -- Each Book's own guard checks the change in that Book.
    selected := current_setting('vult.book_id', true);
    FOR b IN SELECT s.book_id FROM public.agent_settings s
        WHERE s.tenant_id = t AND s.provider = 'vult' AND s.enabled
            AND NOT EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = s.tenant_id AND d.book_id = s.book_id)
        ORDER BY s.book_id FOR UPDATE LOOP
        PERFORM set_config('vult.book_id', b::text, true);
        UPDATE public.agent_settings s SET enabled = false, revision = revision + 1, actor_id = who WHERE s.tenant_id = t AND s.book_id = b;
        turned_off := turned_off + 1;
    END LOOP;
    PERFORM set_config('vult.book_id', coalesce(selected, ''), true);
    RETURN turned_off;
END $$;

-- The server seals a carried-forward key for the Vult. It changes the key
-- only when it is still the one it opened.
CREATE FUNCTION public.vult_agent_reseal(t uuid, opened bytea, sealed bytea, version integer) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE updated integer;
BEGIN
    PERFORM set_config('vult.agent_reseal', 'on', true);
    UPDATE public.vult_agent_settings v SET key_sealed = sealed, key_version = version, sealed_for = NULL
        WHERE v.tenant_id = t AND v.sealed_for IS NOT NULL AND v.key_sealed = opened;
    GET DIAGNOSTICS updated = ROW_COUNT;
    PERFORM set_config('vult.agent_reseal', '', true);
    RETURN updated = 1;
END $$;

CREATE OR REPLACE FUNCTION public.agent_books()
    RETURNS TABLE(tenant_id uuid, book_id uuid, connection_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT s.tenant_id, s.book_id, s.connection_id FROM public.agent_settings s JOIN public.connections c
    ON c.tenant_id = s.tenant_id AND c.book_id = s.book_id AND c.id = s.connection_id
    LEFT JOIN public.vult_agent_settings v ON s.provider = 'vult' AND v.tenant_id = s.tenant_id
    WHERE s.enabled AND (s.provider = 'service'
        OR s.provider = 'vult' AND (v.key_sealed IS NOT NULL OR v.base_url ~ '^http://(127\.0\.0\.1|localhost|\[::1\])')
        OR s.provider NOT IN ('service', 'vult') AND (s.key_sealed IS NOT NULL OR s.base_url ~ '^http://(127\.0\.0\.1|localhost|\[::1\])'))
        AND NOT EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = s.tenant_id AND d.book_id = s.book_id)
    ORDER BY s.updated_at LIMIT 1000
$$;

-- The skills test result belongs to the model the Book's agent runs on.
CREATE OR REPLACE FUNCTION public.agent_record_skills(t uuid, b uuid, tested_model text, tested_level text, tested_score integer, tested_total integer) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE updated integer;
BEGIN
    PERFORM set_config('vult.agent_skills', 'on', true);
    UPDATE public.agent_settings s SET skills_level = tested_level, skills_score = tested_score, skills_total = tested_total, skills_model = left(tested_model, 200),
        skills_tested_at = clock_timestamp(), default_trust = CASE WHEN tested_level = 'confident' THEN 'record' ELSE 'draft' END
        WHERE s.tenant_id = t AND s.book_id = b AND (s.model = tested_model
            OR s.provider = 'vult' AND EXISTS (SELECT 1 FROM public.vult_agent_settings v WHERE v.tenant_id = t AND v.model = tested_model));
    GET DIAGNOSTICS updated = ROW_COUNT;
    PERFORM set_config('vult.agent_skills', '', true);
    RETURN updated = 1;
END $$;

REVOKE ALL ON FUNCTION public.vult_agent_owner_allowed(uuid), public.vult_agent_settings_guard(), public.vult_agent_model_changed(),
    public.agent_secret_for(uuid, uuid), public.vult_agent_secret(uuid), public.vult_agent_key_checked(uuid, bytea, text, text), public.vult_agent_remove_key(uuid, uuid), public.vult_agent_reseal(uuid, bytea, bytea, integer), public.agent_books(),
    public.agent_record_skills(uuid, uuid, text, text, integer, integer) FROM PUBLIC;
