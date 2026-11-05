CREATE SCHEMA access;
REVOKE ALL ON SCHEMA access FROM PUBLIC;

-- Identity, sign-in, OAuth and owner-request records.

CREATE TABLE access.identities (
    id uuid PRIMARY KEY,
    issuer text NOT NULL CHECK (length(issuer) BETWEEN 1 AND 512),
    subject text NOT NULL CHECK (length(subject) BETWEEN 1 AND 256),
    epoch bigint NOT NULL DEFAULT 1 CHECK (epoch > 0),
    UNIQUE(issuer, subject)
);

CREATE TABLE access.owners (
    tenant_id uuid PRIMARY KEY REFERENCES public.tenants(id),
    identity_id uuid NOT NULL REFERENCES access.identities(id)
);

CREATE TABLE access.sessions (
    token_hash text PRIMARY KEY CHECK (token_hash ~ '^[a-f0-9]{64}$'),
    identity_id uuid NOT NULL REFERENCES access.identities(id),
    epoch bigint NOT NULL,
    origin text NOT NULL CHECK (length(origin) BETWEEN 1 AND 512),
    authenticated_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at AND expires_at <= created_at+interval '2 hours')
);

CREATE TABLE access.builtin_credentials (
    identity_id uuid PRIMARY KEY REFERENCES access.identities(id),
    username text NOT NULL UNIQUE CHECK (length(username) BETWEEN 1 AND 80),
    password_hash text NOT NULL CHECK (length(password_hash) <= 512),
    totp_cipher bytea NOT NULL,
    key_id text NOT NULL,
    last_step bigint NOT NULL DEFAULT -1,
    enrolled boolean NOT NULL DEFAULT false,
    failed_count integer NOT NULL DEFAULT 0 CHECK (failed_count BETWEEN 0 AND 8),
    window_expires_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE access.recovery_codes (
    identity_id uuid NOT NULL REFERENCES access.identities(id),
    code_hash text NOT NULL CHECK (code_hash ~ '^[a-f0-9]{64}$'),
    PRIMARY KEY(identity_id, code_hash)
);

CREATE TABLE access.login_flows (
    cookie_hash text PRIMARY KEY CHECK (cookie_hash ~ '^[a-f0-9]{64}$'),
    state_hash text NOT NULL,
    nonce text NOT NULL,
    verifier text NOT NULL,
    issuer text NOT NULL,
    origin text NOT NULL,
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '5 minutes'
);

CREATE TABLE access.oauth_clients (
    id text PRIMARY KEY CHECK (length(id) = 43),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    redirects text[] NOT NULL CHECK (cardinality(redirects) BETWEEN 1 AND 8),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE access.oauth_requests (
    request_hash text PRIMARY KEY CHECK (request_hash ~ '^[a-f0-9]{64}$'),
    client_id text NOT NULL REFERENCES access.oauth_clients(id),
    redirect_uri text NOT NULL,
    resource text NOT NULL,
    challenge text NOT NULL CHECK (length(challenge) = 43),
    state text NOT NULL CHECK (length(state) <= 1024),
    requested_access text NOT NULL CHECK (requested_access IN ('read', 'write')),
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '5 minutes',
    code_hash text UNIQUE,
    identity_id uuid REFERENCES access.identities(id),
    owner_epoch bigint,
    tenant_id uuid,
    book_id uuid,
    approved_access text CHECK (approved_access IN ('read', 'write')),
    selected_connection uuid,
    consumed_at timestamptz,
    activated_connection uuid,
    activated_generation bigint,
    FOREIGN KEY(tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

CREATE TABLE access.oauth_grants (
    connection_id uuid PRIMARY KEY REFERENCES public.connections(id),
    identity_id uuid NOT NULL REFERENCES access.identities(id),
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    client_id text NOT NULL REFERENCES access.oauth_clients(id),
    resource text NOT NULL,
    owner_epoch bigint NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    FOREIGN KEY(tenant_id, book_id, connection_id) REFERENCES public.connections(tenant_id, book_id, id),
    UNIQUE(identity_id, tenant_id, book_id, client_id, resource)
);

CREATE TABLE access.oauth_credentials (
    token_hash text PRIMARY KEY CHECK (token_hash ~ '^[a-f0-9]{64}$'),
    connection_id uuid NOT NULL REFERENCES access.oauth_grants(connection_id),
    generation bigint NOT NULL,
    kind text NOT NULL CHECK (kind IN ('access', 'refresh')),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz
);

CREATE TABLE access.owner_requests (
    identity_id uuid NOT NULL REFERENCES access.identities(id),
    tenant_id uuid NOT NULL REFERENCES public.tenants(id),
    request_key text NOT NULL CHECK (request_key ~ '^[A-Za-z0-9._~-]{1,128}$'),
    operation text NOT NULL CHECK (length(operation) BETWEEN 1 AND 64),
    request_bytes bytea NOT NULL CHECK (octet_length(request_bytes) BETWEEN 2 AND 8192),
    result_bytes bytea NOT NULL CHECK (octet_length(result_bytes) BETWEEN 2 AND 1048576),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(identity_id, tenant_id, request_key)
);

CREATE INDEX sessions_identity ON access.sessions(identity_id);

CREATE INDEX oauth_credentials_connection ON access.oauth_credentials(connection_id);

CREATE INDEX oauth_requests_client ON access.oauth_requests(client_id);

CREATE INDEX oauth_grants_client ON access.oauth_grants(client_id);

-- Authorization and identity operations.

CREATE FUNCTION access.owner_allowed(t uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT t::text = coalesce(current_setting('vult.tenant_id', true), '') AND EXISTS (
    SELECT 1 FROM access.sessions s JOIN access.identities i ON i.id = s.identity_id
    JOIN access.owners o ON o.identity_id = i.id
    WHERE s.token_hash = current_setting('vult.owner_session', true) AND s.epoch = i.epoch
    AND s.origin = current_setting('vult.owner_origin', true) AND s.revoked_at IS NULL
    AND s.expires_at > clock_timestamp() AND o.tenant_id = t)
$$;

CREATE FUNCTION access.require_owner(t uuid, fresh boolean) RETURNS uuid
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
DECLARE
    who uuid;
BEGIN
    SELECT i.id INTO who FROM access.sessions s JOIN access.identities i ON i.id = s.identity_id
    JOIN access.owners o ON o.identity_id = i.id
    WHERE s.token_hash = current_setting('vult.owner_session', true) AND s.epoch = i.epoch
    AND s.origin = current_setting('vult.owner_origin', true) AND s.revoked_at IS NULL
    AND s.expires_at > clock_timestamp() AND o.tenant_id = t
    AND t::text = current_setting('vult.tenant_id', true)
    AND (NOT fresh OR s.authenticated_at > clock_timestamp()-interval '5 minutes')
    FOR SHARE OF s, i, o;
    IF who IS NULL THEN RAISE EXCEPTION 'owner authorization required' USING ERRCODE = '42501';
    END IF;
    RETURN who;
END
$$;

CREATE FUNCTION access.issue_session(iss text, sub text, realm text, token text, proof_at timestamptz)
 RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
DECLARE
    who uuid;
    generation bigint;
    created timestamptz := clock_timestamp();
BEGIN
    IF proof_at < clock_timestamp()-interval '5 minutes' OR proof_at > clock_timestamp()+interval '30 seconds'
    THEN
        RAISE EXCEPTION 'fresh identity required' USING ERRCODE = '42501';
    END IF;
    SELECT i.id, i.epoch INTO who, generation FROM access.identities i
    WHERE i.issuer = iss AND i.subject = sub AND EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = i.id)
    FOR UPDATE OF i;
    IF who IS NULL THEN RAISE EXCEPTION 'owner authorization required' USING ERRCODE = '42501';
    END IF;
    DELETE FROM access.sessions WHERE identity_id = who AND expires_at <= clock_timestamp();
    IF (SELECT count(*)
    FROM access.sessions
    WHERE identity_id = who
    AND epoch = generation
    AND revoked_at IS NULL
    AND expires_at > clock_timestamp()) >= 32
    THEN
        RAISE EXCEPTION 'session limit reached' USING ERRCODE = '42501';
    END IF;
    INSERT INTO access.sessions(token_hash, identity_id, epoch, origin, authenticated_at, created_at, expires_at)
    VALUES(token, who, generation, realm, proof_at, created, created+interval '2 hours');
    RETURN who;
END
$$;

CREATE FUNCTION access.session_identity(token text, realm text)
 RETURNS TABLE(id uuid, issuer text, subject text, authenticated_at timestamptz)
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT i.id, i.issuer, i.subject, s.authenticated_at FROM access.sessions s
    JOIN access.identities i ON i.id = s.identity_id
    WHERE s.token_hash = token AND s.origin = realm AND s.epoch = i.epoch
    AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp()
$$;

CREATE FUNCTION access.session_tenants(token text, realm text) RETURNS SETOF uuid
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT o.tenant_id FROM access.session_identity(token, realm) s
    JOIN access.owners o ON o.identity_id = s.id ORDER BY o.tenant_id
$$;

CREATE FUNCTION access.end_session(token text, realm text) RETURNS void
    LANGUAGE sql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
UPDATE access.sessions SET revoked_at = coalesce(revoked_at, clock_timestamp())
    WHERE token_hash = token AND origin = realm
$$;

CREATE FUNCTION access.builtin_ready(sub text, key text) RETURNS boolean
    LANGUAGE sql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT EXISTS (SELECT 1 FROM access.builtin_credentials c JOIN access.identities i ON i.id = c.identity_id
    WHERE i.issuer = 'urn:vult:builtin' AND i.subject = sub AND c.username = sub AND c.key_id = key)
$$;

CREATE FUNCTION access.reserve_builtin(sub text)
 RETURNS TABLE(id uuid, username text, password_hash text, totp_cipher bytea, key_id text, epoch bigint)
    LANGUAGE sql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
UPDATE access.builtin_credentials c SET
    failed_count = CASE WHEN c.window_expires_at <= clock_timestamp() THEN 1 ELSE c.failed_count+1 END, window_expires_at = CASE WHEN c.window_expires_at <= clock_timestamp() THEN clock_timestamp()+interval '5 minutes' ELSE c.window_expires_at END
    FROM access.identities i WHERE i.id = c.identity_id AND i.issuer = 'urn:vult:builtin' AND i.subject = sub
    AND (c.failed_count < 8 OR c.window_expires_at <= clock_timestamp())
    RETURNING i.id, c.username, c.password_hash, c.totp_cipher, c.key_id, i.epoch
$$;

CREATE FUNCTION access.finish_builtin(who uuid, generation bigint, prior_hash text, next_hash text, step bigint, recovery text)
 RETURNS TABLE(issuer text, subject text)
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
DECLARE
    last bigint;
    ready boolean;
    actual text;
    changed integer;
BEGIN
    PERFORM 1 FROM access.identities i WHERE i.id = who AND i.epoch = generation FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'sign-in unavailable' USING ERRCODE = '42501';
    END IF;
    SELECT c.last_step, c.enrolled, c.password_hash INTO last, ready, actual
    FROM access.builtin_credentials c WHERE c.identity_id = who FOR UPDATE;
    IF actual IS DISTINCT
    FROM prior_hash THEN RAISE EXCEPTION 'sign-in unavailable' USING ERRCODE = '42501';
    END IF;
    IF step >= 0 THEN
        IF step <= last THEN RAISE EXCEPTION 'sign-in unavailable' USING ERRCODE = '42501';
        END IF;
    ELSE
        IF NOT ready THEN RAISE EXCEPTION 'sign-in unavailable' USING ERRCODE = '42501';
        END IF;
        DELETE FROM access.recovery_codes c WHERE c.identity_id = who AND c.code_hash = recovery;
        GET DIAGNOSTICS changed = ROW_COUNT;
        IF changed <> 1 THEN RAISE EXCEPTION 'sign-in unavailable' USING ERRCODE = '42501';
        END IF;
        step = last;
    END IF;
    UPDATE access.builtin_credentials SET last_step = step, enrolled = true, password_hash = next_hash, failed_count = 0, window_expires_at = clock_timestamp()
    WHERE identity_id = who;
    RETURN QUERY SELECT i.issuer, i.subject FROM access.identities i WHERE i.id = who;
END
$$;

CREATE FUNCTION access.start_login(cookie text, state text, n text, v text, iss text, realm text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(769305016);
    DELETE
    FROM access.login_flows
    WHERE cookie_hash IN (SELECT cookie_hash
    FROM access.login_flows
    WHERE expires_at <= clock_timestamp() LIMIT 100);
    IF (SELECT count(*) FROM access.login_flows WHERE expires_at > clock_timestamp()) >= 1024
    THEN
        RAISE EXCEPTION 'sign-in unavailable' USING ERRCODE = '42501';
    END IF;
    INSERT INTO access.login_flows(cookie_hash, state_hash, nonce, verifier, issuer, origin)
    VALUES(cookie, state, n, v, iss, realm);
END
$$;

CREATE FUNCTION access.consume_login(cookie text, state text, iss text, realm text)
 RETURNS TABLE(nonce text, verifier text)
    LANGUAGE sql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
DELETE FROM access.login_flows f WHERE f.cookie_hash = cookie AND f.state_hash = state
    AND f.issuer = iss AND f.origin = realm AND f.expires_at > clock_timestamp() RETURNING f.nonce, f.verifier
$$;

CREATE FUNCTION access.register_client(client text, label text, uris text[]) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION 'registration unavailable' USING ERRCODE = '25001';
    END IF;
    PERFORM pg_advisory_xact_lock(769305017);
    -- Retain expired requests briefly so consumed-code replay still revokes grants.
    DELETE FROM access.oauth_requests
    WHERE request_hash IN (
        SELECT request_hash FROM access.oauth_requests
        WHERE expires_at <= clock_timestamp()-interval '15 minutes'
        ORDER BY expires_at, request_hash LIMIT 100
    );
    -- A registration without a grant or retained request can be abandoned.
    DELETE FROM access.oauth_clients
    WHERE id IN (
        SELECT c.id FROM access.oauth_clients c
        WHERE c.created_at <= clock_timestamp()-interval '24 hours'
        AND NOT EXISTS (SELECT 1 FROM access.oauth_grants g WHERE g.client_id = c.id)
        AND NOT EXISTS (SELECT 1 FROM access.oauth_requests r WHERE r.client_id = c.id)
        ORDER BY c.created_at, c.id LIMIT 128
    );
    IF (SELECT count(*) FROM access.oauth_clients) >= 4096 OR
    (SELECT count(*) FROM access.oauth_clients WHERE created_at > clock_timestamp()-interval '1 hour') >= 128
    THEN
        -- Commit bounded cleanup even when it has not reclaimed a client yet.
        RETURN false;
    END IF;
    INSERT INTO access.oauth_clients(id, name, redirects) VALUES(client, label, uris);
    RETURN true;
END
$$;

CREATE FUNCTION access.start_oauth(request text, client text, redirect text, target text, proof text, client_state text, permission text, realm text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '25001';
    END IF;
    -- Registration cleanup and request creation share a lock before reading clients.
    PERFORM pg_advisory_xact_lock(769305017);
    IF target <> realm||'/mcp'
    OR NOT EXISTS (SELECT 1
    FROM access.oauth_clients
    WHERE id = client
    AND redirect = ANY(redirects))
    THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    DELETE
    FROM access.oauth_requests
    WHERE request_hash IN (SELECT request_hash
    FROM access.oauth_requests
    WHERE expires_at <= clock_timestamp()-interval '15 minutes' LIMIT 100);
    IF (SELECT count(*) FROM access.oauth_requests WHERE expires_at > clock_timestamp()) >= 1024
    THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    INSERT INTO access.oauth_requests(request_hash, client_id, redirect_uri, resource, challenge, state, requested_access)
    VALUES(request, client, redirect, target, proof, client_state, permission);
END
$$;

CREATE FUNCTION access.pending_oauth(request text, realm text)
 RETURNS TABLE(client_name text, redirect_uri text, requested_access text)
    LANGUAGE sql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT c.name, r.redirect_uri, r.requested_access
    FROM access.oauth_requests r
    JOIN access.oauth_clients c ON c.id = r.client_id
    WHERE r.request_hash = request
    AND r.resource = realm||'/mcp'
    AND r.expires_at > clock_timestamp()
    AND r.code_hash IS NULL
    AND r.consumed_at IS NULL
$$;

CREATE FUNCTION access.consent_oauth(session text, realm text, request text, t uuid, b uuid, permission text, existing uuid, code text, deny boolean)
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
        IF permission NOT IN ('read', 'write') OR permission = 'write' AND r.requested_access <> 'write'
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

-- Remote reconnects may change delegated permission, but never actor or Book.
CREATE OR REPLACE FUNCTION public.ledger_connection_guard() RETURNS trigger
    LANGUAGE plpgsql
    SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE'
    OR (to_jsonb(NEW)-ARRAY['revoked_at', 'name', 'access']) IS DISTINCT
    FROM(to_jsonb(OLD)-ARRAY['revoked_at', 'name', 'access'])
    THEN
        RAISE EXCEPTION 'connection scope is immutable' USING ERRCODE = '23514';
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

CREATE FUNCTION access.exchange_oauth(grant_kind text, secret text, client text, target text, redirect text, proof text, new_access text, new_refresh text, realm text)
 RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
DECLARE
    who uuid;
    epoch bigint;
    r access.oauth_requests%ROWTYPE;
    g access.oauth_grants%ROWTYPE;
    c access.oauth_credentials%ROWTYPE;
    connection uuid;permission text;label text;
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '25001';
    END IF;
    IF target <> realm||'/mcp' THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    IF grant_kind = 'authorization_code' THEN
        SELECT * INTO r
        FROM access.oauth_requests
        WHERE code_hash = secret
        AND client_id = client
        AND resource = target
        AND redirect_uri = redirect
        AND challenge = proof
        AND expires_at > clock_timestamp();
        IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        SELECT i.id, i.epoch INTO who, epoch
        FROM access.identities i
        WHERE i.id = r.identity_id
        AND i.epoch = r.owner_epoch FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        PERFORM 1 FROM access.owners WHERE tenant_id = r.tenant_id AND identity_id = who FOR SHARE;
        IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        SELECT * INTO r
        FROM access.oauth_requests
        WHERE code_hash = secret
        AND expires_at > clock_timestamp() FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        IF r.consumed_at IS NOT NULL THEN
            UPDATE public.connections p SET revoked_at = clock_timestamp() FROM access.oauth_grants a
            WHERE p.id = r.activated_connection
            AND p.revoked_at IS NULL
            AND a.connection_id = p.id
            AND a.generation = r.activated_generation;
            RETURN NULL;
        END IF;
        IF r.selected_connection IS NOT NULL THEN
            SELECT * INTO g
            FROM access.oauth_grants
            WHERE connection_id = r.selected_connection
            AND identity_id = who
            AND tenant_id = r.tenant_id
            AND book_id = r.book_id
            AND resource = target FOR UPDATE;
        ELSE
            SELECT * INTO g
            FROM access.oauth_grants
            WHERE identity_id = who
            AND tenant_id = r.tenant_id
            AND book_id = r.book_id
            AND client_id = client
            AND resource = target FOR UPDATE;
        END IF;
        permission = r.approved_access;
        SELECT name INTO label FROM access.oauth_clients WHERE id = client;
        IF g.connection_id IS NULL THEN
            connection = gen_random_uuid();
            INSERT INTO public.connections(tenant_id, book_id, id, actor_id, name, access)
            VALUES(r.tenant_id, r.book_id, connection, gen_random_uuid(), label, permission);
            INSERT INTO access.oauth_grants(connection_id, identity_id, tenant_id, book_id, client_id, resource, owner_epoch, generation)
            VALUES(connection, who, r.tenant_id, r.book_id, client, target, epoch, 1) RETURNING * INTO g;
        ELSE
            UPDATE access.oauth_grants SET client_id = client, owner_epoch = epoch, generation = generation+1
            WHERE connection_id = g.connection_id
            RETURNING * INTO g;
            UPDATE public.connections SET revoked_at = NULL, name = label, access = permission WHERE id = g.connection_id;
        END IF;
        UPDATE access.oauth_requests SET consumed_at = clock_timestamp(), activated_connection = g.connection_id, activated_generation = g.generation
        WHERE request_hash = r.request_hash;
    ELSE
        IF grant_kind <> 'refresh_token' THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        SELECT a.identity_id INTO who
        FROM access.oauth_credentials x
        JOIN access.oauth_grants a ON a.connection_id = x.connection_id
        WHERE x.token_hash = secret
        AND x.kind = 'refresh'
        AND x.expires_at > clock_timestamp()
        AND a.client_id = client
        AND a.resource = target
        AND x.generation = a.generation;
        IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        SELECT i.epoch INTO epoch FROM access.identities i WHERE i.id = who FOR SHARE;
        SELECT a.* INTO g
        FROM access.oauth_grants a
        JOIN access.owners o ON o.identity_id = a.identity_id
        AND o.tenant_id = a.tenant_id
        WHERE a.identity_id = who
        AND a.client_id = client
        AND a.resource = target
        AND a.owner_epoch = epoch
        AND a.connection_id = (SELECT connection_id
        FROM access.oauth_credentials
        WHERE token_hash = secret) FOR UPDATE OF a;
        IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        PERFORM 1 FROM access.owners WHERE tenant_id = g.tenant_id AND identity_id = who FOR SHARE;
        SELECT * INTO c
        FROM access.oauth_credentials
        WHERE token_hash = secret
        AND generation = g.generation
        AND expires_at > clock_timestamp() FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        IF c.consumed_at IS NOT NULL THEN
            UPDATE public.connections SET revoked_at = clock_timestamp()
            WHERE id = g.connection_id
            AND revoked_at IS NULL;
            RETURN NULL;
        END IF;
        SELECT access INTO permission
        FROM public.connections
        WHERE id = g.connection_id
        AND revoked_at IS NULL FOR UPDATE;
        IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        UPDATE access.oauth_credentials SET consumed_at = clock_timestamp() WHERE token_hash = secret;
    END IF;
    DELETE
    FROM access.oauth_credentials
    WHERE token_hash IN (SELECT token_hash
    FROM access.oauth_credentials
    WHERE connection_id = g.connection_id
    AND expires_at < clock_timestamp() LIMIT 100);
    INSERT INTO access.oauth_credentials(token_hash, connection_id, generation, kind, expires_at)
    VALUES(new_access, g.connection_id, g.generation, 'access', clock_timestamp()+interval '15 minutes'), (new_refresh, g.connection_id, g.generation, 'refresh', clock_timestamp()+interval '90 days');
    RETURN permission;
END
$$;

CREATE FUNCTION access.agent_allowed(connection uuid, writing boolean) RETURNS boolean
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
    OR p.access = 'write'))
$$;

CREATE FUNCTION access.resolve_agent(secret text, target text)
 RETURNS TABLE(tenant_id uuid, book_id uuid, connection_id uuid, actor_id uuid, permission text)
    LANGUAGE sql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT p.tenant_id, p.book_id, p.id, p.actor_id, p.access
    FROM access.oauth_credentials c
    JOIN access.oauth_grants g ON g.connection_id = c.connection_id
    JOIN access.identities i ON i.id = g.identity_id
    AND i.epoch = g.owner_epoch
    JOIN access.owners o ON o.tenant_id = g.tenant_id
    AND o.identity_id = i.id
    JOIN public.connections p ON p.id = g.connection_id AND p.tenant_id = g.tenant_id AND p.book_id = g.book_id
    WHERE c.token_hash = secret
    AND c.kind = 'access'
    AND c.expires_at > clock_timestamp()
    AND c.consumed_at IS NULL
    AND c.generation = g.generation AND g.resource = target AND p.revoked_at IS NULL
$$;

CREATE FUNCTION access.delegation_allowed(connection uuid, writing boolean) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT NOT EXISTS (SELECT 1
    FROM access.oauth_grants g
    WHERE g.connection_id = connection)
    OR access.agent_allowed(connection, writing)
$$;

CREATE FUNCTION access.require_agent(secret text, target text, t uuid, b uuid, connection uuid, actor uuid, permission text) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
DECLARE
    who uuid;
    current_generation bigint;
BEGIN
    IF current_setting('transaction_isolation') <> 'read committed' THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '25001';
    END IF;
    SELECT g.identity_id INTO who
    FROM access.oauth_credentials c
    JOIN access.oauth_grants g ON g.connection_id = c.connection_id
    WHERE c.token_hash = secret AND c.kind = 'access' AND g.connection_id = connection AND g.resource = target;
    IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    PERFORM 1 FROM access.identities WHERE id = who FOR SHARE;
    PERFORM 1 FROM access.owners WHERE tenant_id = t AND identity_id = who FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    SELECT g.generation INTO current_generation
    FROM access.oauth_grants g
    JOIN access.identities i ON i.id = g.identity_id
    AND i.epoch = g.owner_epoch
    WHERE g.connection_id = connection
    AND g.identity_id = who
    AND g.tenant_id = t
    AND g.book_id = b
    AND g.resource = target FOR SHARE OF g;
    IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    PERFORM 1
    FROM access.oauth_credentials c
    WHERE c.token_hash = secret
    AND c.connection_id = connection
    AND c.generation = current_generation
    AND c.kind = 'access'
    AND c.expires_at > clock_timestamp() AND c.consumed_at IS NULL FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    PERFORM 1
    FROM public.connections p
    WHERE p.id = connection
    AND p.tenant_id = t
    AND p.book_id = b
    AND p.actor_id = actor
    AND p.access = permission
    AND p.revoked_at IS NULL FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    PERFORM set_config('vult.agent_token', secret, true), set_config('vult.agent_resource', target, true);
END
$$;

CREATE OR REPLACE FUNCTION public.ledger_allowed(t uuid, b uuid, writing boolean) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT public.ledger_context(t, b) AND EXISTS (SELECT 1 FROM public.connections c
    WHERE c.tenant_id = t
    AND c.book_id = b
    AND c.id::text = coalesce(current_setting('vult.connection_id', true), '')
    AND c.actor_id::text = coalesce(current_setting('vult.actor_id', true), '')
    AND c.revoked_at IS NULL
    AND (NOT writing
    OR c.access = 'write')
    AND (NOT EXISTS (SELECT 1
    FROM access.oauth_grants g
    WHERE g.connection_id = c.id)
    OR access.agent_allowed(c.id, writing)))
$$;

CREATE FUNCTION access.consent_books(session text, realm text)
 RETURNS TABLE(tenant_id uuid, book_id uuid, name text)
    LANGUAGE sql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT b.tenant_id, b.id, b.name
    FROM access.sessions s
    JOIN access.identities i ON i.id = s.identity_id
    AND i.epoch = s.epoch
    JOIN access.owners o ON o.identity_id = i.id JOIN public.books b ON b.tenant_id = o.tenant_id
    WHERE s.token_hash = session
    AND s.origin = realm
    AND s.expires_at > clock_timestamp()
    AND s.revoked_at IS NULL
    ORDER BY b.created_at, b.id LIMIT 101
$$;

CREATE FUNCTION access.consent_connections(session text, realm text)
 RETURNS TABLE(connection_id uuid, name text)
    LANGUAGE sql SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT c.id, c.name
    FROM access.sessions s
    JOIN access.identities i ON i.id = s.identity_id
    AND i.epoch = s.epoch
    JOIN access.owners o ON o.identity_id = i.id
    JOIN access.oauth_grants g ON g.identity_id = i.id
    AND g.tenant_id = o.tenant_id
    AND g.resource = realm||'/mcp'
    JOIN public.connections c ON c.id = g.connection_id
    WHERE s.token_hash = session
    AND s.origin = realm
    AND s.expires_at > clock_timestamp()
    AND s.revoked_at IS NULL
    ORDER BY c.created_at, c.id LIMIT 101
$$;

CREATE FUNCTION access.owner_identity(t uuid) RETURNS uuid
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT i.id FROM access.sessions s JOIN access.identities i ON i.id = s.identity_id AND i.epoch = s.epoch
    JOIN access.owners o ON o.identity_id = i.id AND o.tenant_id = t
    WHERE s.token_hash = coalesce(current_setting('vult.owner_session', true), '')
    AND s.origin = coalesce(current_setting('vult.owner_origin', true), '')
    AND t::text = coalesce(current_setting('vult.tenant_id', true), '')
    AND s.expires_at > clock_timestamp()
    AND s.revoked_at IS NULL
$$;

CREATE FUNCTION access.owner_write_allowed(t uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT current_setting('transaction_isolation') = 'read committed' AND access.owner_allowed(t) AND
    EXISTS (SELECT 1
    FROM access.sessions s
    WHERE s.token_hash = coalesce(current_setting('vult.owner_session', true), '')
    AND s.origin = coalesce(current_setting('vult.owner_origin', true), '')
    AND s.authenticated_at >= clock_timestamp()-interval '5 minutes')
$$;

-- Row-level access and immutable owner outcomes.

ALTER TABLE access.owner_requests ENABLE ROW LEVEL SECURITY;

ALTER TABLE access.owner_requests FORCE ROW LEVEL SECURITY;

CREATE POLICY owner_read ON public.books FOR SELECT USING (access.owner_allowed(tenant_id));

CREATE POLICY owner_create ON public.books FOR INSERT WITH CHECK (access.owner_write_allowed(tenant_id));

CREATE POLICY owner_update ON public.books FOR UPDATE USING (access.owner_allowed(tenant_id)) WITH CHECK (access.owner_write_allowed(tenant_id));

CREATE POLICY owner_read ON public.connections FOR SELECT USING (access.owner_allowed(tenant_id));

CREATE POLICY owner_create ON public.connections FOR INSERT WITH CHECK (access.owner_write_allowed(tenant_id));

CREATE POLICY owner_update ON public.connections FOR UPDATE USING (access.owner_allowed(tenant_id)) WITH CHECK (access.owner_write_allowed(tenant_id));

CREATE POLICY owner_close ON public.closed_periods FOR INSERT WITH CHECK (access.owner_write_allowed(tenant_id));

DO $$
DECLARE
    tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['accounts', 'account_revisions', 'requests', 'entries', 'lines', 'closed_periods'] LOOP
        EXECUTE format('CREATE POLICY owner_read ON public.%I FOR SELECT USING(access.owner_allowed(tenant_id))', tab);
    END LOOP;
END
$$;

DROP POLICY IF EXISTS connection_read ON public.connections;

DROP POLICY IF EXISTS connection_lock ON public.connections;

CREATE POLICY connection_read ON public.connections FOR SELECT USING (
 public.ledger_context(tenant_id, book_id) AND id::text = coalesce(current_setting('vult.connection_id', true), '')
 AND actor_id::text = coalesce(current_setting('vult.actor_id', true), '') AND revoked_at IS NULL
 AND access.delegation_allowed(id, false));

CREATE POLICY connection_lock ON public.connections FOR UPDATE USING (
 public.ledger_context(tenant_id, book_id) AND id::text = coalesce(current_setting('vult.connection_id', true), '')
 AND actor_id::text = coalesce(current_setting('vult.actor_id', true), '') AND revoked_at IS NULL) WITH CHECK (false);

CREATE POLICY owner_request_read ON access.owner_requests FOR SELECT USING (identity_id = access.owner_identity(tenant_id));

CREATE POLICY owner_request_insert ON access.owner_requests FOR INSERT WITH CHECK (identity_id = access.owner_identity(tenant_id) AND access.owner_write_allowed(tenant_id));

CREATE TRIGGER owner_request_immutable BEFORE UPDATE OR DELETE ON access.owner_requests FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();

REVOKE ALL ON ALL TABLES IN SCHEMA access FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA access FROM PUBLIC;
