-- The owner's bank provider keys, once for the whole Vult: every Book in it
-- connects banks with them. A Book's own keys, saved before, are still read
-- when the Vult has none.
CREATE TABLE public.vult_bank_provider_settings (
    tenant_id uuid NOT NULL REFERENCES public.tenants(id),
    provider text NOT NULL CHECK (provider IN ('plaid')),
    environment text NOT NULL CHECK (environment IN ('sandbox', 'production')),
    client_id text NOT NULL CHECK (length(client_id) BETWEEN 1 AND 128 AND client_id !~ '[[:space:]]'),
    -- The provider secret, sealed by the application to the Vult; the key
    -- version names the key that sealed it.
    secret_sealed bytea NOT NULL CHECK (octet_length(secret_sealed) BETWEEN 34 AND 4096),
    key_version integer NOT NULL CHECK (key_version > 0),
    webhook_url text NOT NULL DEFAULT '' CHECK (webhook_url = '' OR webhook_url ~ '^https://[^[:space:]]{1,2040}$'),
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, provider)
);

-- The owner changes the Vult's keys from a fresh sign-in, never through an
-- agent's connection.
CREATE FUNCTION public.vult_bank_owner_allowed(t uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = '' AND access.owner_write_allowed(t)
$$;

CREATE FUNCTION public.vult_bank_settings_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        IF TG_OP = 'UPDATE' AND (NEW.revision <> OLD.revision + 1 OR NEW.tenant_id <> OLD.tenant_id OR NEW.provider <> OLD.provider)
            OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'provider settings changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER vult_bank_settings_guard BEFORE INSERT OR UPDATE ON public.vult_bank_provider_settings
    FOR EACH ROW EXECUTE FUNCTION public.vult_bank_settings_guard();

ALTER TABLE public.vult_bank_provider_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.vult_bank_provider_settings FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.vult_bank_provider_settings FROM PUBLIC;
-- Read only by the owner and by a sync of one of the Vult's Books; agents
-- never read them, sealed or not.
CREATE POLICY bank_read ON public.vult_bank_provider_settings FOR SELECT
    USING (access.owner_allowed(tenant_id) OR tenant_id::text = coalesce(current_setting('vult.tenant_id', true), '')
        AND coalesce(current_setting('vult.bank_sync', true), '') <> '');
CREATE POLICY bank_owner_insert ON public.vult_bank_provider_settings FOR INSERT WITH CHECK (public.vult_bank_owner_allowed(tenant_id));
CREATE POLICY bank_owner_update ON public.vult_bank_provider_settings FOR UPDATE
    USING (public.vult_bank_owner_allowed(tenant_id)) WITH CHECK (public.vult_bank_owner_allowed(tenant_id));
REVOKE ALL ON FUNCTION public.vult_bank_owner_allowed(uuid), public.vult_bank_settings_guard() FROM PUBLIC;

-- Which group a document is filed in: statements, receipts and invoices,
-- company records, or tax. A document with none is read as a statement
-- when a statement was imported from it, else as a receipt.
CREATE TABLE public.document_groups (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    document_id uuid NOT NULL,
    document_group text NOT NULL CHECK (document_group IN ('statement', 'receipt', 'company', 'tax')),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, document_id),
    FOREIGN KEY (tenant_id, book_id, document_id) REFERENCES public.documents(tenant_id, book_id, id)
);

-- The owner files documents when signed in; a fresh sign-in isn't needed,
-- since filing changes no records. A connection that may write files
-- what it adds.
CREATE FUNCTION public.document_group_writer(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = '' AND public.ledger_context(t, b) AND access.owner_allowed(t)
    OR public.ledger_allowed(t, b, true)
$$;

CREATE FUNCTION public.document_group_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        IF TG_OP = 'UPDATE' AND ROW(NEW.tenant_id, NEW.book_id, NEW.document_id) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.document_id) THEN
            RAISE EXCEPTION 'a document keeps its filing' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER document_group_guard BEFORE INSERT OR UPDATE ON public.document_groups
    FOR EACH ROW EXECUTE FUNCTION public.document_group_guard();

ALTER TABLE public.document_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.document_groups FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.document_groups FROM PUBLIC;
CREATE POLICY group_read ON public.document_groups FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY group_insert ON public.document_groups FOR INSERT WITH CHECK (public.document_group_writer(tenant_id, book_id));
CREATE POLICY group_update ON public.document_groups FOR UPDATE
    USING (public.document_group_writer(tenant_id, book_id)) WITH CHECK (public.document_group_writer(tenant_id, book_id));
REVOKE ALL ON FUNCTION public.document_group_writer(uuid, uuid), public.document_group_guard() FROM PUBLIC;
