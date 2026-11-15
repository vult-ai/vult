-- Archiving and deleting a Book.
--
-- Archiving only hides a Book from menus; every record stays and
-- unarchiving puts it back. Deleting is permanent: only the Vult's owner
-- asks for it, with a passkey assertion made for that one Book at that
-- moment and the Book's exact name. The Book is then locked (nothing in it
-- changes, agents lose access, its bank logins are disconnected) and erased
-- after a waiting period the owner can cancel. Erasure removes every row of
-- the Book; the deletion record below is what remains.

-- Books the owner archived. Removing the row unarchives the Book.
CREATE TABLE public.archived_books (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    archived_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

-- A Book waiting to be erased. Held: the owner must confirm again, no
-- sooner than confirm_after, before the waiting period starts. Waiting: it
-- is erased from erase_after. Erasing: erasure has started and resumes
-- until it finishes. Only one Book of a Vult waits at a time.
CREATE TABLE public.book_deletions (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    state text NOT NULL CHECK (state IN ('held', 'waiting', 'erasing')),
    requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    confirm_after timestamptz,
    erase_after timestamptz,
    actor text NOT NULL CHECK (length(actor) BETWEEN 1 AND 80),
    via text NOT NULL CHECK (via IN ('browser', 'operator')),
    PRIMARY KEY (tenant_id, book_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    CHECK ((state = 'held') = (erase_after IS NULL)),
    CHECK (state <> 'held' OR confirm_after IS NOT NULL)
);
CREATE UNIQUE INDEX book_deletions_one_per_vult ON public.book_deletions(tenant_id);

-- What happened to a Book's archiving and deletion: who, which Book, when
-- and from where, never its records or amounts. It is never changed or
-- removed, and it stays after the Book is erased. The Book is named by
-- book_ref, not book_id, so nothing that removes a Book's rows touches it.
CREATE TABLE access.book_deletion_events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES public.tenants(id),
    book_ref uuid NOT NULL,
    book_name text NOT NULL CHECK (length(book_name) <= 240),
    event text NOT NULL CHECK (event IN ('archived', 'unarchived', 'requested', 'held', 'confirmed', 'cancelled',
        'banks_disconnected', 'erasure_started', 'erased', 'refused')),
    actor text NOT NULL CHECK (length(actor) BETWEEN 1 AND 80),
    via text NOT NULL CHECK (via IN ('browser', 'operator', 'vult')),
    client_address text NOT NULL DEFAULT '' CHECK (length(client_address) <= 64),
    client text NOT NULL DEFAULT '' CHECK (length(client) <= 200),
    detail text NOT NULL DEFAULT '' CHECK (length(detail) <= 400),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX book_deletion_events_recent ON access.book_deletion_events(tenant_id, event, recorded_at);
CREATE TRIGGER book_deletion_event_immutable BEFORE UPDATE OR DELETE ON access.book_deletion_events
    FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE FUNCTION public.book_deletion_events_kept() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN RAISE EXCEPTION 'the deletion record is kept' USING ERRCODE = '23514'; END $$;
CREATE TRIGGER book_deletion_events_kept BEFORE TRUNCATE ON access.book_deletion_events
    FOR EACH STATEMENT EXECUTE FUNCTION public.book_deletion_events_kept();

-- A passkey assertion asked for one action on one Book, by one signed-in
-- browser session. It is used once: taken to verify the assertion, then
-- spent by the action it was made for.
CREATE TABLE access.book_deletion_proofs (
    token_hash text PRIMARY KEY CHECK (token_hash ~ '^[a-f0-9]{64}$'),
    identity_id uuid NOT NULL REFERENCES access.identities(id),
    epoch bigint NOT NULL,
    session_hash text NOT NULL CHECK (session_hash ~ '^[a-f0-9]{64}$'),
    tenant_id uuid NOT NULL,
    book_ref uuid NOT NULL,
    purpose text NOT NULL CHECK (purpose IN ('delete', 'confirm')),
    ceremony jsonb NOT NULL CHECK (jsonb_typeof(ceremony) = 'object' AND octet_length(ceremony::text) <= 32768),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp() + interval '5 minutes',
    taken_at timestamptz,
    verified_at timestamptz,
    used_at timestamptz
);
CREATE INDEX book_deletion_proofs_identity ON access.book_deletion_proofs(identity_id, created_at);

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['public.archived_books', 'public.book_deletions', 'access.book_deletion_events', 'access.book_deletion_proofs'] LOOP
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON %s FROM PUBLIC', tab);
    END LOOP;
END $$;
-- The owner reads which Books are archived or being deleted. Every change
-- goes through the functions below.
CREATE POLICY owner_read ON public.archived_books FOR SELECT USING (access.owner_allowed(tenant_id));
CREATE POLICY owner_read ON public.book_deletions FOR SELECT USING (access.owner_allowed(tenant_id));

-- Whether a Book is being deleted.
CREATE FUNCTION public.book_locked(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = t AND d.book_id = b)
$$;

-- A locked Book changes no more. Its agent connections may still be
-- revoked and its bank logins disconnected and released; nothing else is
-- written. Erasure itself runs with triggers set aside.
CREATE FUNCTION public.book_lock_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE row_data jsonb; col text; v text; t text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.book_deletions) THEN
        IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN row_data := to_jsonb(OLD); ELSE row_data := to_jsonb(NEW); END IF;
    t := row_data->>'tenant_id';
    FOREACH col IN ARRAY TG_ARGV LOOP
        v := row_data->>col;
        IF v IS NOT NULL AND EXISTS (SELECT 1 FROM public.book_deletions d
            WHERE d.book_id::text = v AND (t IS NULL OR d.tenant_id::text = t)) THEN
            IF TG_OP = 'UPDATE' AND TG_TABLE_SCHEMA = 'public' AND TG_TABLE_NAME = 'bank_connections' AND row_data->>'status' = 'disconnected' THEN
                RETURN NEW;
            END IF;
            IF TG_OP = 'UPDATE' AND (TG_TABLE_SCHEMA, TG_TABLE_NAME) IN (('public', 'connections'), ('access', 'oauth_grants'), ('access', 'oauth_requests')) THEN
                RETURN NEW;
            END IF;
            RAISE EXCEPTION 'This Book is being deleted, so nothing in it can change.' USING ERRCODE = 'VB423';
        END IF;
    END LOOP;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION public.book_lock_guard(), public.book_locked(uuid, uuid), public.book_deletion_events_kept() FROM PUBLIC;

-- Every table that holds a Book's rows gets the lock. A table added later
-- must add it too; a test checks that none is missing.
DO $$
DECLARE r record;
BEGIN
    FOR r IN SELECT n.nspname, c.relname, array_agg(a.attname::text ORDER BY a.attname) AS cols
        FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
        WHERE c.relkind = 'r' AND n.nspname IN ('public', 'access') AND a.atttypid = 'uuid'::regtype
            AND (a.attname IN ('book_id', 'book_a', 'book_b') OR a.attname LIKE '%\_book\_id')
            AND (n.nspname, c.relname) NOT IN (('public', 'book_deletions'), ('access', 'book_view_preferences'))
        GROUP BY n.nspname, c.relname
    LOOP
        EXECUTE format('CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON %I.%I FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard(%s)',
            r.nspname, r.relname, (SELECT string_agg(quote_literal(x), ',') FROM unnest(r.cols) x));
    END LOOP;
END $$;
CREATE TRIGGER book_lock_guard BEFORE UPDATE ON public.books FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('id');

-- Agents lose access to a Book being deleted: the same check as before,
-- refused for a locked Book.
CREATE OR REPLACE FUNCTION public.ledger_allowed(t uuid, b uuid, writing boolean) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT NOT public.book_locked(t, b) AND CASE WHEN coalesce(current_setting('vult.owner_posting', true), '') = '' THEN
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

-- The built-in agent never works in a locked Book.
CREATE OR REPLACE FUNCTION public.agent_books()
    RETURNS TABLE(tenant_id uuid, book_id uuid, connection_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT s.tenant_id, s.book_id, s.connection_id FROM public.agent_settings s JOIN public.connections c
    ON c.tenant_id = s.tenant_id AND c.book_id = s.book_id AND c.id = s.connection_id
    WHERE s.enabled AND (s.key_sealed IS NOT NULL OR s.provider = 'service' OR s.base_url ~ '^http://(127\.0\.0\.1|localhost|\[::1\])')
        AND NOT EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = s.tenant_id AND d.book_id = s.book_id)
    ORDER BY s.updated_at LIMIT 1000
$$;

-- An agent asking for access is offered only Books that are neither
-- archived nor being deleted.
CREATE OR REPLACE FUNCTION access.consent_books(session text, realm text)
 RETURNS TABLE(tenant_id uuid, book_id uuid, name text)
    LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
SELECT b.tenant_id,b.id,coalesce(m.name,b.name)
    FROM access.sessions s
    JOIN access.identities i ON i.id=s.identity_id AND i.epoch=s.epoch
    JOIN access.owners o ON o.identity_id=i.id
    JOIN public.books b ON b.tenant_id=o.tenant_id
    LEFT JOIN LATERAL(SELECT r.name FROM public.book_revisions r
        WHERE r.tenant_id=b.tenant_id AND r.book_id=b.id ORDER BY r.revision DESC LIMIT 1) m ON true
    WHERE s.token_hash=session AND s.origin=realm
        AND s.expires_at>clock_timestamp() AND s.revoked_at IS NULL
        AND NOT EXISTS (SELECT 1 FROM public.archived_books x WHERE x.tenant_id=b.tenant_id AND x.book_id=b.id)
        AND NOT EXISTS (SELECT 1 FROM public.book_deletions x WHERE x.tenant_id=b.tenant_id AND x.book_id=b.id)
    ORDER BY b.created_at,b.id LIMIT 101
$$;

-- The Book's current name, as the owner sees it.
CREATE FUNCTION public.book_current_name(t uuid, b uuid) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT coalesce((SELECT r.name FROM public.book_revisions r WHERE r.tenant_id = t AND r.book_id = b ORDER BY r.revision DESC LIMIT 1),
    (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = b))
$$;

CREATE FUNCTION public.book_deletion_event(t uuid, b uuid, name text, what text, who text, path text, address text, agent text, note text) RETURNS void
    LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
INSERT INTO access.book_deletion_events(tenant_id, book_ref, book_name, event, actor, via, client_address, client, detail)
VALUES (t, b, left(coalesce(name, ''), 240), what, left(who, 80), path, left(coalesce(address, ''), 64), left(coalesce(agent, ''), 200), left(coalesce(note, ''), 400))
$$;

-- Refuses a request beyond its limit in the window, per Vult.
CREATE FUNCTION public.book_deletion_limit(t uuid, events text[], window_length interval, most integer, message text) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF (SELECT count(*) FROM access.book_deletion_events e WHERE e.tenant_id = t AND e.event = ANY(events)
        AND e.recorded_at > clock_timestamp() - window_length) >= most THEN
        RAISE EXCEPTION '%', message USING ERRCODE = 'VB429';
    END IF;
END $$;

-- The owner, signed in, and not an agent's connection.
CREATE FUNCTION public.book_deletion_owner(t uuid) RETURNS uuid
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF coalesce(current_setting('vult.connection_id', true), '') <> '' OR coalesce(current_setting('vult.owner_posting', true), '') <> '' THEN
        RAISE EXCEPTION 'owner authorization required' USING ERRCODE = '42501';
    END IF;
    RETURN access.require_owner(t, false);
END $$;

-- Archives or unarchives a Book. Any sign-in of the owner may: nothing is
-- removed and it is undone at once.
CREATE FUNCTION public.archive_book(t uuid, b uuid, archiving boolean, address text, agent text) RETURNS boolean
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; name text;
BEGIN
    who := public.book_deletion_owner(t);
    PERFORM 1 FROM public.books WHERE tenant_id = t AND id = b FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'Book unavailable' USING ERRCODE = '42501'; END IF;
    IF public.book_locked(t, b) THEN
        RAISE EXCEPTION 'This Book is being deleted. Cancel the deletion first.' USING ERRCODE = 'VB409';
    END IF;
    PERFORM public.book_deletion_limit(t, ARRAY['archived', 'unarchived'], interval '1 hour', 60, 'Too many changes in the last hour. Try again later.');
    name := public.book_current_name(t, b);
    IF archiving THEN
        INSERT INTO public.archived_books(tenant_id, book_id, actor_id) VALUES (t, b, who) ON CONFLICT DO NOTHING;
    ELSE
        DELETE FROM public.archived_books WHERE tenant_id = t AND book_id = b;
    END IF;
    IF FOUND THEN
        PERFORM public.book_deletion_event(t, b, name, CASE WHEN archiving THEN 'archived' ELSE 'unarchived' END, who::text, 'browser', address, agent, '');
    END IF;
    RETURN archiving;
END $$;

-- What deleting the Book removes, for the owner to read before confirming.
CREATE FUNCTION public.book_deletion_summary(t uuid, b uuid)
    RETURNS TABLE(name text, entries bigint, documents bigint, bank_connections bigint, accounts bigint, linked_books text[])
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NOT access.owner_allowed(t) OR NOT EXISTS (SELECT 1 FROM public.books WHERE tenant_id = t AND id = b) THEN
        RAISE EXCEPTION 'owner authorization required' USING ERRCODE = '42501';
    END IF;
    RETURN QUERY SELECT public.book_current_name(t, b),
        (SELECT count(*) FROM public.entries e WHERE e.tenant_id = t AND e.book_id = b),
        (SELECT count(*) FROM public.documents d WHERE d.tenant_id = t AND d.book_id = b AND d.state = 'ready'),
        (SELECT count(*) FROM public.bank_connections c WHERE c.tenant_id = t AND c.book_id = b AND c.disconnected_at IS NULL),
        (SELECT count(*) FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = b),
        coalesce((SELECT array_agg(public.book_current_name(t, CASE WHEN l.book_a = b THEN l.book_b ELSE l.book_a END) ORDER BY l.created_at)
            FROM public.book_links l WHERE l.tenant_id = t AND l.active AND b IN (l.book_a, l.book_b)), ARRAY[]::text[]);
END $$;

-- Starts a deletion. The caller has verified a passkey assertion made for
-- this Book; proof names it, and is spent here. The Vult's last Book, or a
-- second Book within a day of another, is held until the owner confirms
-- again a day later.
CREATE FUNCTION public.book_deletion_start(t uuid, b uuid, typed text, who text, path text, address text, agent text)
    RETURNS TABLE(state text, confirm_after timestamptz, erase_after timestamptz, banks integer)
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE name text; held boolean; reasons text := ''; disconnected integer; linked text;
BEGIN
    PERFORM 1 FROM public.tenants WHERE id = t FOR UPDATE;
    PERFORM 1 FROM public.books WHERE tenant_id = t AND id = b FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'Book unavailable' USING ERRCODE = '42501'; END IF;
    name := public.book_current_name(t, b);
    IF typed IS DISTINCT FROM name THEN
        RAISE EXCEPTION 'Type the Book''s name exactly as it is shown.' USING ERRCODE = 'VB422';
    END IF;
    IF EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = t) THEN
        RAISE EXCEPTION 'Another Book is already being deleted. Cancel that deletion, or wait until it is done.' USING ERRCODE = 'VB409';
    END IF;
    SELECT string_agg(public.book_current_name(t, CASE WHEN l.book_a = b THEN l.book_b ELSE l.book_a END), ', ') INTO linked
        FROM public.book_links l WHERE l.tenant_id = t AND l.active AND b IN (l.book_a, l.book_b);
    IF linked IS NOT NULL THEN
        RAISE EXCEPTION 'This Book is linked to %. Turn the link off first.', linked USING ERRCODE = 'VB409';
    END IF;
    held := path = 'browser' AND (
        NOT EXISTS (SELECT 1 FROM public.books k WHERE k.tenant_id = t AND k.id <> b)
        OR EXISTS (SELECT 1 FROM access.book_deletion_events e WHERE e.tenant_id = t AND e.book_ref <> b
            AND e.event IN ('requested', 'erased') AND e.recorded_at > clock_timestamp() - interval '1 day'));
    -- Disconnect the Book's bank logins while it can still change. The
    -- server then asks each provider to end its access.
    PERFORM set_config('vult.book_id', b::text, true);
    IF path = 'browser' THEN
        PERFORM set_config('vult.actor_id', who, true);
    END IF;
    UPDATE public.bank_connections c SET status = 'disconnected', status_reason = 'Disconnected because the Book is being deleted.',
        disconnected_at = clock_timestamp(), sync_requested_at = NULL
        WHERE c.tenant_id = t AND c.book_id = b AND c.disconnected_at IS NULL;
    GET DIAGNOSTICS disconnected = ROW_COUNT;
    DELETE FROM public.archived_books x WHERE x.tenant_id = t AND x.book_id = b;
    INSERT INTO public.book_deletions(tenant_id, book_id, state, confirm_after, erase_after, actor, via)
    VALUES (t, b, CASE WHEN held THEN 'held' ELSE 'waiting' END,
        CASE WHEN held THEN clock_timestamp() + interval '1 day' END,
        CASE WHEN held THEN NULL ELSE clock_timestamp() + interval '7 days' END, who, path);
    PERFORM public.book_deletion_event(t, b, name, 'requested', who, path, address, agent, '');
    IF held THEN
        PERFORM public.book_deletion_event(t, b, name, 'held', who, path, address, agent,
            CASE WHEN NOT EXISTS (SELECT 1 FROM public.books k WHERE k.tenant_id = t AND k.id <> b)
                THEN 'This is the only Book in this Vult.' ELSE 'Another Book was deleted in the last day.' END);
    END IF;
    IF disconnected > 0 THEN
        PERFORM public.book_deletion_event(t, b, name, 'banks_disconnected', who, path, address, agent, disconnected::text || ' bank connection(s)');
    END IF;
    RETURN QUERY SELECT d.state, d.confirm_after, d.erase_after, disconnected FROM public.book_deletions d WHERE d.tenant_id = t AND d.book_id = b;
END $$;

-- Spends a verified passkey proof made for this Book, purpose and session.
CREATE FUNCTION public.book_deletion_spend_proof(t uuid, b uuid, proof text, purpose text, who uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    UPDATE access.book_deletion_proofs p SET used_at = clock_timestamp()
        WHERE p.token_hash = proof AND p.identity_id = who AND p.tenant_id = t AND p.book_ref = b AND p.purpose = book_deletion_spend_proof.purpose
        AND p.session_hash = coalesce(current_setting('vult.owner_session', true), '')
        AND p.verified_at IS NOT NULL AND p.verified_at > clock_timestamp() - interval '2 minutes' AND p.used_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Confirm with your passkey to continue.' USING ERRCODE = 'VB401';
    END IF;
END $$;

-- The owner asks to delete a Book, in the browser.
CREATE FUNCTION public.request_book_deletion(t uuid, b uuid, proof text, typed text, address text, agent text)
    RETURNS TABLE(state text, confirm_after timestamptz, erase_after timestamptz, banks integer)
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid;
BEGIN
    who := public.book_deletion_owner(t);
    PERFORM public.book_deletion_spend_proof(t, b, proof, 'delete', who);
    PERFORM public.book_deletion_limit(t, ARRAY['requested'], interval '1 day', 3, 'Too many deletions were started today. Try again tomorrow.');
    RETURN QUERY SELECT * FROM public.book_deletion_start(t, b, typed, who::text, 'browser', address, agent);
END $$;

-- The owner confirms a held deletion, a day or more after asking.
CREATE FUNCTION public.confirm_book_deletion(t uuid, b uuid, proof text, address text, agent text)
    RETURNS TABLE(state text, confirm_after timestamptz, erase_after timestamptz, banks integer)
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; d public.book_deletions%ROWTYPE;
BEGIN
    who := public.book_deletion_owner(t);
    PERFORM public.book_deletion_spend_proof(t, b, proof, 'confirm', who);
    PERFORM public.book_deletion_limit(t, ARRAY['confirmed'], interval '1 day', 3, 'Too many confirmations today. Try again tomorrow.');
    SELECT * INTO d FROM public.book_deletions x WHERE x.tenant_id = t AND x.book_id = b FOR UPDATE;
    IF d.state IS DISTINCT FROM 'held' THEN
        RAISE EXCEPTION 'This deletion is not waiting for a confirmation.' USING ERRCODE = 'VB409';
    END IF;
    IF d.confirm_after > clock_timestamp() THEN
        RAISE EXCEPTION 'You can confirm this deletion from %.', to_char(d.confirm_after AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI "UTC"') USING ERRCODE = 'VB425';
    END IF;
    UPDATE public.book_deletions x SET state = 'waiting', erase_after = clock_timestamp() + interval '7 days'
        WHERE x.tenant_id = t AND x.book_id = b;
    PERFORM public.book_deletion_event(t, b, public.book_current_name(t, b), 'confirmed', who::text, 'browser', address, agent, '');
    RETURN QUERY SELECT x.state, x.confirm_after, x.erase_after, 0 FROM public.book_deletions x WHERE x.tenant_id = t AND x.book_id = b;
END $$;

-- The owner cancels a deletion before erasure starts. Any sign-in of the
-- owner may: cancelling keeps everything. Bank logins stay disconnected.
CREATE FUNCTION public.cancel_book_deletion(t uuid, b uuid, address text, agent text) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; d public.book_deletions%ROWTYPE;
BEGIN
    who := public.book_deletion_owner(t);
    PERFORM public.book_deletion_limit(t, ARRAY['cancelled'], interval '1 day', 20, 'Too many cancellations today. Try again tomorrow.');
    SELECT * INTO d FROM public.book_deletions x WHERE x.tenant_id = t AND x.book_id = b FOR UPDATE;
    IF d.state IS NULL THEN
        RAISE EXCEPTION 'This Book is not being deleted.' USING ERRCODE = 'VB409';
    END IF;
    IF d.state = 'erasing' THEN
        RAISE EXCEPTION 'Erasure has started and can''t be stopped.' USING ERRCODE = 'VB409';
    END IF;
    DELETE FROM public.book_deletions x WHERE x.tenant_id = t AND x.book_id = b;
    PERFORM public.book_deletion_event(t, b, public.book_current_name(t, b), 'cancelled', who::text, 'browser', address, agent, '');
END $$;

-- The machine's operator deletes a Book, from an administrator command. It
-- skips the passkey and the hold; immediately starts erasure now.
CREATE FUNCTION public.operator_book_deletion(t uuid, b uuid, typed text, immediately boolean)
    RETURNS TABLE(state text, confirm_after timestamptz, erase_after timestamptz, banks integer)
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_import_owner() THEN
        RAISE EXCEPTION 'operator authorization required' USING ERRCODE = '42501';
    END IF;
    PERFORM * FROM public.book_deletion_start(t, b, typed, 'operator', 'operator', '', '');
    IF immediately THEN
        UPDATE public.book_deletions x SET erase_after = clock_timestamp() WHERE x.tenant_id = t AND x.book_id = b;
    END IF;
    RETURN QUERY SELECT x.state, x.confirm_after, x.erase_after, 0 FROM public.book_deletions x WHERE x.tenant_id = t AND x.book_id = b;
END $$;

CREATE FUNCTION public.operator_cancel_book_deletion(t uuid, b uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.book_deletions%ROWTYPE;
BEGIN
    IF NOT public.ledger_import_owner() THEN
        RAISE EXCEPTION 'operator authorization required' USING ERRCODE = '42501';
    END IF;
    SELECT * INTO d FROM public.book_deletions x WHERE x.tenant_id = t AND x.book_id = b FOR UPDATE;
    IF d.state IS NULL OR d.state = 'erasing' THEN
        RAISE EXCEPTION 'This Book has no deletion to cancel.' USING ERRCODE = 'VB409';
    END IF;
    DELETE FROM public.book_deletions x WHERE x.tenant_id = t AND x.book_id = b;
    PERFORM public.book_deletion_event(t, b, public.book_current_name(t, b), 'cancelled', 'operator', 'operator', '', '', '');
END $$;

-- The owner's Books that are archived or being deleted.
CREATE FUNCTION public.book_lifecycle(t uuid)
    RETURNS TABLE(book_id uuid, archived_at timestamptz, deletion_state text, requested_at timestamptz,
        confirm_after timestamptz, erase_after timestamptz, banks_waiting bigint)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NOT access.owner_allowed(t) THEN
        RAISE EXCEPTION 'owner authorization required' USING ERRCODE = '42501';
    END IF;
    RETURN QUERY SELECT k.id, a.archived_at, d.state, d.requested_at, d.confirm_after, d.erase_after,
        (SELECT count(*) FROM public.bank_connections c WHERE c.tenant_id = t AND c.book_id = k.id AND d.state IS NOT NULL
            AND c.access_ended_at IS NULL AND (c.token_sealed IS NOT NULL OR c.access = 'operator'))
        FROM public.books k
        LEFT JOIN public.archived_books a ON a.tenant_id = k.tenant_id AND a.book_id = k.id
        LEFT JOIN public.book_deletions d ON d.tenant_id = k.tenant_id AND d.book_id = k.id
        WHERE k.tenant_id = t AND (a.book_id IS NOT NULL OR d.book_id IS NOT NULL);
END $$;

-- What the owner's Vult recorded about deletions, newest first. It names
-- Books, never their records.
CREATE FUNCTION public.book_deletion_history(t uuid, max_rows integer)
    RETURNS TABLE(book_ref uuid, book_name text, event text, actor text, via text, client_address text, client text, detail text, recorded_at timestamptz)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NOT access.owner_allowed(t) AND NOT public.ledger_import_owner() THEN
        RAISE EXCEPTION 'owner authorization required' USING ERRCODE = '42501';
    END IF;
    RETURN QUERY SELECT e.book_ref, e.book_name, e.event, e.actor, e.via, e.client_address, e.client, e.detail, e.recorded_at
        FROM access.book_deletion_events e WHERE e.tenant_id = t ORDER BY e.sequence DESC LIMIT least(greatest(max_rows, 1), 500);
END $$;

-- Books whose erasure is due: the waiting period is over and every bank
-- login's provider access has ended, or erasure started and must finish.
CREATE FUNCTION public.book_erasures_due(max_rows integer)
    RETURNS TABLE(tenant_id uuid, book_id uuid, state text)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT d.tenant_id, d.book_id, d.state FROM public.book_deletions d
    WHERE d.state = 'erasing' OR d.state = 'waiting' AND d.erase_after <= clock_timestamp()
        AND NOT EXISTS (SELECT 1 FROM public.bank_connections c WHERE c.tenant_id = d.tenant_id AND c.book_id = d.book_id
            AND c.access_ended_at IS NULL AND (c.token_sealed IS NOT NULL OR c.access = 'operator'))
    ORDER BY d.erase_after NULLS FIRST LIMIT least(greatest(max_rows, 1), 20)
$$;

-- Starts or resumes a due erasure, and lists the stored files to remove.
-- Before the waiting period ends it refuses, whoever asks.
CREATE FUNCTION public.begin_book_erasure(t uuid, b uuid)
    RETURNS TABLE(book_name text, storage_ids text[], requested_at timestamptz)
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.book_deletions%ROWTYPE; name text;
BEGIN
    SELECT * INTO d FROM public.book_deletions x WHERE x.tenant_id = t AND x.book_id = b FOR UPDATE;
    IF d.state IS NULL OR NOT EXISTS (SELECT 1 FROM public.book_erasures_due(20) x WHERE x.tenant_id = t AND x.book_id = b) THEN
        RAISE EXCEPTION 'This Book is not due for erasure.' USING ERRCODE = 'VB409';
    END IF;
    name := public.book_current_name(t, b);
    IF d.state = 'waiting' THEN
        UPDATE public.book_deletions x SET state = 'erasing' WHERE x.tenant_id = t AND x.book_id = b;
        PERFORM public.book_deletion_event(t, b, name, 'erasure_started', 'vult', 'vult', '', '', '');
    END IF;
    RETURN QUERY SELECT name, coalesce((SELECT array_agg(DISTINCT x.storage_id ORDER BY x.storage_id) FROM (
            SELECT o.storage_id FROM public.documents o WHERE o.tenant_id = t AND o.book_id = b AND o.storage_id IS NOT NULL AND o.storage_id <> ''
            UNION SELECT u.storage_id FROM public.document_uploads u WHERE u.tenant_id = t AND u.book_id = b AND u.storage_id IS NOT NULL AND u.storage_id <> '') x),
        ARRAY[]::text[]), d.requested_at;
END $$;

-- Every column that names a Book, in the tables erasure clears.
CREATE FUNCTION public.book_erasure_columns()
    RETURNS TABLE(schema_name text, table_name text, column_name text)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT n.nspname::text, c.relname::text, a.attname::text
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
    WHERE c.relkind = 'r' AND n.nspname IN ('public', 'access') AND a.atttypid = 'uuid'::regtype
        AND (a.attname IN ('book_id', 'book_a', 'book_b') OR a.attname LIKE '%\_book\_id')
        AND (n.nspname, c.relname) <> ('public', 'book_deletions')
    ORDER BY 1, 2, 3
$$;

-- Removes every row of the Book in one transaction, checks that nothing
-- remains and nothing points at a removed row, and records the erasure.
-- The ledger's guards keep records from ever being removed; they step aside
-- for this transaction only, which needs the function's owner to be the
-- database's superuser.
CREATE FUNCTION public.finish_book_erasure(t uuid, b uuid) RETURNS bigint
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.book_deletions%ROWTYPE; x record; n bigint; total bigint := 0; name text; left_over bigint; orphan boolean;
BEGIN
    SELECT * INTO d FROM public.book_deletions k WHERE k.tenant_id = t AND k.book_id = b FOR UPDATE;
    IF d.state IS DISTINCT FROM 'erasing' THEN
        RAISE EXCEPTION 'This Book''s erasure has not started.' USING ERRCODE = 'VB409';
    END IF;
    name := public.book_current_name(t, b);
    PERFORM set_config('session_replication_role', 'replica', true);
    -- Agent credentials and owner requests name the Book only indirectly.
    DELETE FROM access.oauth_credentials c WHERE c.connection_id IN (SELECT g.connection_id FROM access.oauth_grants g WHERE g.book_id = b);
    GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
    DELETE FROM access.owner_requests r WHERE r.tenant_id = t
        AND (position(convert_to(b::text, 'UTF8') IN r.request_bytes) > 0 OR position(convert_to(b::text, 'UTF8') IN r.result_bytes) > 0);
    GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
    DELETE FROM access.book_deletion_proofs p WHERE p.tenant_id = t AND p.book_ref = b;
    GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
    FOR x IN SELECT * FROM public.book_erasure_columns() LOOP
        EXECUTE format('DELETE FROM %I.%I WHERE %I = $1', x.schema_name, x.table_name, x.column_name) USING b;
        GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
    END LOOP;
    DELETE FROM public.book_deletions k WHERE k.tenant_id = t AND k.book_id = b;
    DELETE FROM public.books k WHERE k.tenant_id = t AND k.id = b;
    GET DIAGNOSTICS n = ROW_COUNT; total := total + n;
    PERFORM set_config('session_replication_role', 'origin', true);
    -- Nothing of the Book remains.
    FOR x IN SELECT * FROM public.book_erasure_columns() LOOP
        EXECUTE format('SELECT count(*) FROM %I.%I WHERE %I = $1', x.schema_name, x.table_name, x.column_name) USING b INTO left_over;
        IF left_over > 0 THEN
            RAISE EXCEPTION 'erasure left rows in %.%', x.schema_name, x.table_name USING ERRCODE = 'VB500';
        END IF;
    END LOOP;
    -- Nothing points at a removed row.
    FOR x IN SELECT format('%I.%I', cn.nspname, c.relname) AS child, format('%I.%I', pn.nspname, p.relname) AS parent,
            (SELECT array_agg(quote_ident(a.attname) ORDER BY k.n) FROM unnest(f.conkey) WITH ORDINALITY k(att, n) JOIN pg_attribute a ON a.attrelid = f.conrelid AND a.attnum = k.att) AS cols,
            (SELECT array_agg(quote_ident(a.attname) ORDER BY k.n) FROM unnest(f.confkey) WITH ORDINALITY k(att, n) JOIN pg_attribute a ON a.attrelid = f.confrelid AND a.attnum = k.att) AS refs
        FROM pg_constraint f JOIN pg_class c ON c.oid = f.conrelid JOIN pg_namespace cn ON cn.oid = c.relnamespace
        JOIN pg_class p ON p.oid = f.confrelid JOIN pg_namespace pn ON pn.oid = p.relnamespace
        WHERE f.contype = 'f' AND cn.nspname IN ('public', 'access')
    LOOP
        EXECUTE format('SELECT EXISTS (SELECT 1 FROM %s c WHERE %s AND NOT EXISTS (SELECT 1 FROM %s p WHERE %s))', x.child,
            (SELECT string_agg('c.' || col || ' IS NOT NULL', ' AND ') FROM unnest(x.cols) col),
            x.parent,
            (SELECT string_agg('p.' || x.refs[i] || ' = c.' || x.cols[i], ' AND ') FROM generate_subscripts(x.cols, 1) i)) INTO orphan;
        IF orphan THEN
            RAISE EXCEPTION 'erasure would leave % pointing at a removed row', x.child USING ERRCODE = 'VB500';
        END IF;
    END LOOP;
    PERFORM public.book_deletion_event(t, b, name, 'erased', 'vult', 'vult', '', '', total::text || ' rows removed');
    RETURN total;
END $$;

REVOKE ALL ON FUNCTION public.book_current_name(uuid, uuid), public.book_deletion_event(uuid, uuid, text, text, text, text, text, text, text),
    public.book_deletion_limit(uuid, text[], interval, integer, text), public.book_deletion_owner(uuid),
    public.archive_book(uuid, uuid, boolean, text, text), public.book_deletion_summary(uuid, uuid),
    public.book_deletion_start(uuid, uuid, text, text, text, text, text), public.book_deletion_spend_proof(uuid, uuid, text, text, uuid),
    public.request_book_deletion(uuid, uuid, text, text, text, text), public.confirm_book_deletion(uuid, uuid, text, text, text),
    public.cancel_book_deletion(uuid, uuid, text, text), public.operator_book_deletion(uuid, uuid, text, boolean),
    public.operator_cancel_book_deletion(uuid, uuid), public.book_lifecycle(uuid), public.book_deletion_history(uuid, integer),
    public.book_erasures_due(integer), public.begin_book_erasure(uuid, uuid), public.book_erasure_columns(),
    public.finish_book_erasure(uuid, uuid) FROM PUBLIC;

-- Passkey proofs for one action on one Book. The browser session asks;
-- the owner's passkey answers with user verification.
CREATE FUNCTION access.begin_book_proof(sub text, realm text, generation bigint, token text, session text,
    t uuid, b uuid, purpose text, data jsonb) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid;
BEGIN
    SELECT i.id INTO who FROM access.identities i
        JOIN access.owners o ON o.identity_id = i.id AND o.tenant_id = t
        JOIN access.sessions s ON s.identity_id = i.id AND s.epoch = i.epoch AND s.token_hash = session AND s.origin = realm
            AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp()
        WHERE i.issuer = 'urn:vult:builtin' AND i.subject = sub AND i.epoch = generation FOR UPDATE OF i;
    IF who IS NULL OR purpose NOT IN ('delete', 'confirm')
        OR NOT EXISTS (SELECT 1 FROM public.books k WHERE k.tenant_id = t AND k.id = b)
        OR NOT EXISTS (SELECT 1 FROM access.passkey_profiles p WHERE p.identity_id = who AND p.origin = realm AND p.state = 'ready') THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    DELETE FROM access.book_deletion_proofs p WHERE p.identity_id = who AND p.created_at < clock_timestamp() - interval '1 day';
    IF (SELECT count(*) FROM access.book_deletion_proofs p WHERE p.identity_id = who AND p.created_at > clock_timestamp() - interval '1 hour') >= 10 THEN
        RAISE EXCEPTION 'Too many passkey requests in the last hour. Try again later.' USING ERRCODE = 'VB429';
    END IF;
    INSERT INTO access.book_deletion_proofs(token_hash, identity_id, epoch, session_hash, tenant_id, book_ref, purpose, ceremony)
    VALUES (token, who, generation, session, t, b, purpose, data);
END $$;

CREATE FUNCTION access.take_book_proof(token text, realm text, session text)
    RETURNS TABLE(identity_id uuid, epoch bigint, ceremony jsonb, credentials jsonb)
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE p access.book_deletion_proofs%ROWTYPE;
BEGIN
    SELECT * INTO p FROM access.book_deletion_proofs x
        WHERE x.token_hash = token AND x.session_hash = session AND x.taken_at IS NULL AND x.expires_at > clock_timestamp()
        AND EXISTS (SELECT 1 FROM access.identities i WHERE i.id = x.identity_id AND i.epoch = x.epoch)
        AND EXISTS (SELECT 1 FROM access.passkey_profiles f WHERE f.identity_id = x.identity_id AND f.origin = realm)
        FOR UPDATE;
    IF p.token_hash IS NULL THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    UPDATE access.book_deletion_proofs x SET taken_at = clock_timestamp() WHERE x.token_hash = token;
    RETURN QUERY SELECT p.identity_id, p.epoch, p.ceremony,
        coalesce((SELECT jsonb_agg(c.credential ORDER BY c.credential_id)
        FROM access.passkey_credentials c WHERE c.identity_id = p.identity_id AND c.revoked_at IS NULL), '[]'::jsonb);
END $$;

-- Records a verified assertion: the credential's new counter, and the
-- session's sign-in as fresh, since the owner just used the passkey.
CREATE FUNCTION access.finish_book_proof(token text, realm text, session text, key_id text, prior jsonb, updated jsonb, key_hash text)
    RETURNS uuid
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE p access.book_deletion_proofs%ROWTYPE;
BEGIN
    SELECT * INTO p FROM access.book_deletion_proofs x
        WHERE x.token_hash = token AND x.session_hash = session AND x.taken_at IS NOT NULL AND x.verified_at IS NULL
        AND x.expires_at > clock_timestamp() FOR UPDATE;
    IF p.token_hash IS NULL OR prior IS NULL THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    UPDATE access.passkey_credentials c SET credential = updated
        WHERE c.identity_id = p.identity_id AND c.credential_id = key_id AND c.credential = prior AND c.revoked_at IS NULL;
    IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501'; END IF;
    UPDATE access.book_deletion_proofs x SET verified_at = clock_timestamp() WHERE x.token_hash = token;
    UPDATE access.sessions s SET authenticated_at = clock_timestamp()
        WHERE s.token_hash = session AND s.identity_id = p.identity_id AND s.origin = realm AND s.revoked_at IS NULL;
    INSERT INTO access.passkey_events(identity_id, kind, credential_hash) VALUES (p.identity_id, 'authenticated', key_hash);
    RETURN p.identity_id;
END $$;
REVOKE ALL ON FUNCTION access.begin_book_proof(text, text, bigint, text, text, uuid, uuid, text, jsonb),
    access.take_book_proof(text, text, text), access.finish_book_proof(text, text, text, text, jsonb, jsonb, text) FROM PUBLIC;
