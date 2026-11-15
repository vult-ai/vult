-- The built-in agent's settings for a Book: the provider and model the
-- owner chose, and the owner's own API key, sealed by the application with
-- a key that never enters this database. Only the owner changes them, from
-- a fresh sign-in; no agent reads them. The agent acts through its own
-- proposing connection, like any agent.
CREATE TABLE public.agent_settings (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    -- service: the service running this Vult provides the agent and its
    -- model, so the owner only turns it on or off and gives no key.
    provider text NOT NULL CHECK (provider IN ('openai_compatible', 'anthropic', 'service')),
    base_url text NOT NULL CHECK (length(base_url) <= 512),
    model text NOT NULL CHECK (length(model) <= 200),
    key_sealed bytea CHECK (octet_length(key_sealed) BETWEEN 34 AND 4096),
    key_version integer CHECK (key_version > 0),
    enabled boolean NOT NULL DEFAULT false,
    connection_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    CHECK ((key_sealed IS NULL) = (key_version IS NULL)),
    CHECK (provider = 'service' AND base_url = '' AND model = '' AND key_sealed IS NULL
        OR provider <> 'service' AND base_url ~ '^https?://[^[:space:]]+$' AND length(model) >= 1)
);

CREATE FUNCTION public.agent_settings_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent settings are turned off, never deleted' USING ERRCODE = '23514';
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
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id
            AND c.id = NEW.connection_id AND c.access = 'propose') THEN
        RAISE EXCEPTION 'the built-in agent works through a proposing connection' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER agent_settings_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_settings
    FOR EACH ROW EXECUTE FUNCTION public.agent_settings_guard();

-- The engine reads settings for the Books whose agent is on. It reveals
-- the sealed key only to the server, which alone can open it.
CREATE FUNCTION public.agent_settings_for(t uuid, b uuid)
    RETURNS TABLE(provider text, base_url text, model text, key_sealed bytea, key_version integer, enabled boolean, connection_id uuid, actor_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT s.provider, s.base_url, s.model, s.key_sealed, s.key_version, s.enabled, s.connection_id, c.actor_id
    FROM public.agent_settings s JOIN public.connections c ON c.tenant_id = s.tenant_id AND c.book_id = s.book_id AND c.id = s.connection_id
    WHERE s.tenant_id = t AND s.book_id = b
$$;
CREATE FUNCTION public.agent_books()
    RETURNS TABLE(tenant_id uuid, book_id uuid, connection_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT s.tenant_id, s.book_id, s.connection_id FROM public.agent_settings s JOIN public.connections c
    ON c.tenant_id = s.tenant_id AND c.book_id = s.book_id AND c.id = s.connection_id
    WHERE s.enabled AND (s.key_sealed IS NOT NULL OR s.provider = 'service' OR s.base_url ~ '^http://(127\.0\.0\.1|localhost|\[::1\])')
    ORDER BY s.updated_at LIMIT 1000
$$;

ALTER TABLE public.agent_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agent_settings FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.agent_settings FROM PUBLIC;
-- Only the owner reads them; agents never do, sealed or not.
CREATE POLICY owner_read ON public.agent_settings FOR SELECT USING (access.owner_allowed(tenant_id));
CREATE POLICY owner_insert ON public.agent_settings FOR INSERT WITH CHECK (public.rule_owner_allowed(tenant_id, book_id));
CREATE POLICY owner_update ON public.agent_settings FOR UPDATE USING (public.rule_owner_allowed(tenant_id, book_id)) WITH CHECK (public.rule_owner_allowed(tenant_id, book_id));
REVOKE ALL ON FUNCTION public.agent_settings_guard(), public.agent_settings_for(uuid, uuid), public.agent_books() FROM PUBLIC;
