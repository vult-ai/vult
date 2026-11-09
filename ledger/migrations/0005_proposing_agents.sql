-- Agents that propose: a third access level between read and write.
--
-- A proposing agent reads the Book and prepares entries for the owner's
-- approval. It never posts, reverses or changes anything itself; an approved
-- entry is posted by the owner's decision. Owners may also change a live
-- connection's access level without reconnecting the agent.

ALTER TABLE public.connections DROP CONSTRAINT connections_access_check,
    ADD CONSTRAINT connections_access_check CHECK (access IN ('read', 'propose', 'write'));
ALTER TABLE access.oauth_requests DROP CONSTRAINT oauth_requests_approved_access_check,
    ADD CONSTRAINT oauth_requests_approved_access_check CHECK (approved_access IN ('read', 'propose', 'write'));

-- A proposal is prepared in two transactions the agent runtime marks with
-- vult.proposing: a preview that runs the operation and is always rolled back,
-- then one that records only the proposal. A proposing connection may write
-- only inside them, and the recording transaction can't change the Book.
CREATE FUNCTION public.ledger_proposing() RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.proposing', true), '') IN ('preview', 'record')
$$;

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
    OR p.access = 'propose' AND public.ledger_proposing()))
$$;

CREATE OR REPLACE FUNCTION public.ledger_allowed(t uuid, b uuid, writing boolean) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT CASE WHEN coalesce(current_setting('vult.owner_posting', true), '') = '' THEN
    -- Only the agent runtime can select a connection as its write authority.
    -- A privileged database administrator still has the normal import path.
    (NOT has_function_privilege(session_user, 'access.owner_posting_allowed(uuid,uuid)'::regprocedure, 'EXECUTE')
        OR public.ledger_import_owner())
    AND public.ledger_context(t, b) AND EXISTS (SELECT 1 FROM public.connections c
        WHERE c.tenant_id = t
        AND c.book_id = b
        AND c.id::text = coalesce(current_setting('vult.connection_id', true), '')
        AND c.actor_id::text = coalesce(current_setting('vult.actor_id', true), '')
        AND c.revoked_at IS NULL
        AND (NOT writing OR c.access = 'write' OR c.access = 'propose' AND public.ledger_proposing())
        AND (NOT EXISTS (SELECT 1 FROM access.oauth_grants g WHERE g.connection_id = c.id)
            OR access.agent_allowed(c.id, writing)))
ELSE access.owner_posting_allowed(t, b) END
$$;

-- Recording a proposal never changes the Book itself.
CREATE FUNCTION public.ledger_proposal_record_only() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF coalesce(current_setting('vult.proposing', true), '') = 'record' THEN
        RAISE EXCEPTION 'a proposal never records changes' USING ERRCODE = '42501';
    END IF;
    RETURN NULL;
END $$;
DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['entries', 'lines', 'accounts', 'account_revisions', 'book_revisions', 'evidence_links', 'source_citations', 'requests'] LOOP
        EXECUTE format('CREATE TRIGGER proposal_record_only AFTER INSERT OR UPDATE ON public.%I FOR EACH ROW EXECUTE FUNCTION public.ledger_proposal_record_only()', tab);
    END LOOP;
END $$;

-- The owner may change a live connection's access level; nothing else about
-- a connection changes, and agents can't change their own access.
CREATE OR REPLACE FUNCTION public.ledger_connection_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF TG_OP = 'DELETE'
    OR (to_jsonb(NEW)-ARRAY['revoked_at', 'name', 'access']) IS DISTINCT
    FROM(to_jsonb(OLD)-ARRAY['revoked_at', 'name', 'access'])
    THEN
        RAISE EXCEPTION 'connection scope is immutable' USING ERRCODE = '23514';
    END IF;
    IF (to_jsonb(NEW)-'access') IS NOT DISTINCT FROM (to_jsonb(OLD)-'access')
    AND OLD.revoked_at IS NULL AND NEW.access IS DISTINCT FROM OLD.access
    AND (access.owner_write_allowed(NEW.tenant_id) OR public.ledger_import_owner()) THEN
        RETURN NEW;
    END IF;
    IF (to_jsonb(NEW)-'revoked_at') IS DISTINCT
    FROM(to_jsonb(OLD)-'revoked_at')
    OR OLD.revoked_at IS NOT NULL
    OR NEW.revoked_at IS NULL THEN
        IF current_user <> (SELECT rolname
        FROM pg_roles
        WHERE oid = (SELECT relowner
        FROM pg_class
        WHERE oid = 'access.oauth_grants'::regclass))
        OR NOT EXISTS (SELECT 1 FROM access.oauth_grants WHERE connection_id = OLD.id)
        THEN
            RAISE EXCEPTION 'connection scope is immutable' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION access.consent_oauth(session text, realm text, request text, t uuid, b uuid, permission text, existing uuid, code text, deny boolean)
 RETURNS TABLE(redirect_uri text, state text)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
DECLARE
    who uuid;
    generation bigint;
    r access.oauth_requests%ROWTYPE;
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '25001';
    END IF;
    PERFORM set_config('vult.owner_session', session, true), set_config('vult.owner_origin', realm, true), set_config('vult.tenant_id', t::text, true);
    who = access.require_owner(t, true);
    SELECT epoch INTO generation FROM access.identities WHERE id = who;
    SELECT * INTO r FROM access.oauth_requests WHERE request_hash = request AND resource = realm||'/mcp'
    AND expires_at > clock_timestamp() AND code_hash IS NULL AND consumed_at IS NULL FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    IF deny THEN UPDATE access.oauth_requests SET consumed_at = clock_timestamp() WHERE request_hash = request;
    ELSE
        -- An app that asked to write may be given less: proposing or reading.
        IF permission NOT IN ('read', 'propose', 'write') OR permission <> 'read' AND r.requested_access <> 'write'
        OR NOT EXISTS (SELECT 1 FROM public.books WHERE tenant_id = t AND id = b)
        OR existing IS NOT NULL
        AND NOT EXISTS (SELECT 1
        FROM access.oauth_grants
        WHERE connection_id = existing
        AND identity_id = who
        AND tenant_id = t
        AND book_id = b
        AND resource = realm||'/mcp')
        THEN
            RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        UPDATE access.oauth_requests SET code_hash = code, identity_id = who, owner_epoch = generation, tenant_id = t, book_id = b, approved_access = permission, selected_connection = existing, expires_at = least(expires_at, clock_timestamp()+interval '2 minutes')
        WHERE request_hash = request;
    END IF;
    RETURN QUERY SELECT r.redirect_uri, r.state;
END
$$;

REVOKE ALL ON FUNCTION public.ledger_proposal_record_only() FROM PUBLIC;
