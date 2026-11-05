CREATE TABLE public.documents (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    sequence bigint NOT NULL CHECK (sequence > 0),
    sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 1 AND 33554432),
    media_type text NOT NULL CHECK (media_type IN (
        'application/pdf', 'image/png', 'image/jpeg', 'image/webp', 'text/csv'
    )),
    original_name text NOT NULL CHECK (
        octet_length(original_name) BETWEEN 1 AND 255
        AND original_name !~ '[[:cntrl:]/\\]'
    ),
    storage_id text NOT NULL UNIQUE CHECK (storage_id ~ '^[a-f0-9]{64}$'),
    key_id text NOT NULL CHECK (key_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'ready', 'expired')),
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ready_at timestamptz,
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    UNIQUE (tenant_id, book_id, id),
    UNIQUE (tenant_id, book_id, sequence),
    UNIQUE (tenant_id, book_id, sha256),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    CHECK (used_at IS NULL OR state = 'ready'),
    CHECK (state <> 'ready' OR ready_at IS NOT NULL)
);

CREATE INDEX documents_expiry ON public.documents(tenant_id, book_id, expires_at, id)
    WHERE used_at IS NULL AND state <> 'expired';

CREATE TABLE public.document_uploads (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    document_id uuid NOT NULL,
    storage_id text NOT NULL CHECK (storage_id ~ '^[a-f0-9]{64}$'),
    actor_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    UNIQUE (tenant_id, book_id, id),
    UNIQUE (tenant_id, book_id, receipt_id),
    FOREIGN KEY (tenant_id, book_id, document_id) REFERENCES public.documents(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK (expires_at > created_at)
);

CREATE INDEX document_uploads_original ON public.document_uploads(tenant_id, book_id, document_id, storage_id);

CREATE FUNCTION public.document_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'document identity is retained' USING ERRCODE = '23514';
    END IF;
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF TG_OP = 'INSERT' THEN
        IF NOT public.ledger_import_owner() THEN
            IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true)
                OR NEW.state <> 'pending' OR NEW.ready_at IS NOT NULL OR NEW.used_at IS NOT NULL THEN
                RAISE EXCEPTION 'invalid document intake' USING ERRCODE = '23514';
            END IF;
            NEW.created_at := clock_timestamp();
            NEW.expires_at := NEW.created_at + interval '15 minutes';
            SELECT write_epoch INTO STRICT NEW.sequence FROM public.books
                WHERE tenant_id = NEW.tenant_id AND id = NEW.book_id;
        END IF;
        RETURN NEW;
    END IF;
    IF ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.sequence, NEW.sha256, NEW.size_bytes,
        NEW.media_type, NEW.original_name, NEW.actor_id, NEW.created_at)
        IS DISTINCT FROM
        ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.sequence, OLD.sha256, OLD.size_bytes,
        OLD.media_type, OLD.original_name, OLD.actor_id, OLD.created_at) THEN
        RAISE EXCEPTION 'document identity is immutable' USING ERRCODE = '23514';
    END IF;
    IF NEW.used_at IS DISTINCT FROM OLD.used_at THEN
        IF OLD.used_at IS NOT NULL OR NEW.used_at IS NULL OR pg_trigger_depth() < 2 THEN
            RAISE EXCEPTION 'document use is permanent' USING ERRCODE = '23514';
        END IF;
        NEW.used_at := clock_timestamp();
    END IF;
    IF OLD.state = 'pending' AND NEW.state = 'ready' THEN
        IF OLD.expires_at <= clock_timestamp() OR NEW.storage_id <> OLD.storage_id OR NEW.key_id <> OLD.key_id THEN
            RAISE EXCEPTION 'upload expired' USING ERRCODE = '23514';
        END IF;
        NEW.ready_at := clock_timestamp();
        NEW.expires_at := NEW.ready_at + interval '30 days';
    ELSIF NEW.state = 'expired' AND OLD.state IN ('pending', 'ready') THEN
        IF OLD.used_at IS NOT NULL OR OLD.expires_at > clock_timestamp()
            OR NEW.storage_id <> OLD.storage_id OR NEW.key_id <> OLD.key_id
            OR NEW.ready_at IS DISTINCT FROM OLD.ready_at OR NEW.expires_at <> OLD.expires_at THEN
            RAISE EXCEPTION 'document cannot expire' USING ERRCODE = '23514';
        END IF;
    ELSIF OLD.state = 'expired' AND NEW.state = 'pending' THEN
        IF OLD.used_at IS NOT NULL OR NEW.storage_id = OLD.storage_id THEN
            RAISE EXCEPTION 'new upload required' USING ERRCODE = '23514';
        END IF;
        NEW.ready_at := NULL;
        NEW.expires_at := clock_timestamp() + interval '15 minutes';
    ELSIF ROW(NEW.state, NEW.storage_id, NEW.key_id, NEW.ready_at, NEW.expires_at)
        IS DISTINCT FROM ROW(OLD.state, OLD.storage_id, OLD.key_id, OLD.ready_at, OLD.expires_at) THEN
        RAISE EXCEPTION 'invalid document transition' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER document_guard BEFORE INSERT OR UPDATE OR DELETE ON public.documents
    FOR EACH ROW EXECUTE FUNCTION public.document_guard();

CREATE FUNCTION public.document_upload_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    deadline timestamptz;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF public.ledger_import_owner() THEN
        RETURN NEW;
    END IF;
    SELECT expires_at INTO STRICT deadline FROM public.documents
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id
        AND id = NEW.document_id AND storage_id = NEW.storage_id AND state = 'pending';
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true)
            OR NEW.connection_id::text IS DISTINCT FROM current_setting('vult.connection_id', true) THEN
            RAISE EXCEPTION 'authenticated uploader required' USING ERRCODE = '42501';
        END IF;
        NEW.created_at := clock_timestamp();
        NEW.expires_at := least(deadline, NEW.created_at + interval '15 minutes');
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER document_upload_guard BEFORE INSERT ON public.document_uploads
    FOR EACH ROW EXECUTE FUNCTION public.document_upload_guard();
CREATE TRIGGER document_upload_immutable BEFORE UPDATE OR DELETE ON public.document_uploads
    FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();

CREATE FUNCTION public.document_upload_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    receipt public.requests%ROWTYPE;
    request jsonb;
    result jsonb;
    original public.documents%ROWTYPE;
BEGIN
    SELECT * INTO STRICT receipt FROM public.requests
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.receipt_id;
    SELECT * INTO STRICT original FROM public.documents
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.document_id;
    request := convert_from(receipt.request_bytes, 'UTF8')::jsonb;
    result := convert_from(receipt.result_bytes, 'UTF8')::jsonb;
    IF receipt.operation <> 'add_document' OR receipt.actor_id <> NEW.actor_id
        OR request->>'sha256' IS DISTINCT FROM original.sha256
        OR request->>'size' IS DISTINCT FROM original.size_bytes::text
        OR result->>'upload_id' IS DISTINCT FROM NEW.id::text
        OR result->'document'->>'id' IS DISTINCT FROM NEW.document_id::text
        OR (result->>'upload_expires_at')::timestamptz IS DISTINCT FROM NEW.expires_at THEN
        RAISE EXCEPTION 'upload receipt differs from intake' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER document_upload_receipt AFTER INSERT ON public.document_uploads
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.document_upload_receipt();

CREATE FUNCTION public.document_completion_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.state <> 'ready' THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM public.requests r
        CROSS JOIN LATERAL (SELECT convert_from(r.request_bytes, 'UTF8')::jsonb AS value) request
        CROSS JOIN LATERAL (SELECT convert_from(r.result_bytes, 'UTF8')::jsonb AS value) result
        JOIN public.document_uploads u ON u.id::text = request.value->>'upload_id'
        WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id
        AND r.operation = 'complete_document_upload' AND r.request_key = 'upload:'||u.id::text
        AND u.tenant_id = NEW.tenant_id AND u.book_id = NEW.book_id AND u.document_id = NEW.id
        AND u.storage_id = NEW.storage_id AND u.actor_id = r.actor_id
        AND result.value->>'id' = NEW.id::text AND result.value->>'state' = 'ready'
        AND result.value->>'sha256' = NEW.sha256 AND result.value->>'size' = NEW.size_bytes::text
        AND result.value->>'sequence' = NEW.sequence::text
        AND (result.value->>'ready_at')::timestamptz = NEW.ready_at
    ) THEN
        RAISE EXCEPTION 'completed upload receipt required' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER document_completion_receipt AFTER INSERT OR UPDATE ON public.documents
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.document_completion_receipt();

DO $$
DECLARE
    tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['documents', 'document_uploads'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('CREATE POLICY scoped_read ON public.%I FOR SELECT USING (public.ledger_allowed(tenant_id,book_id,false))', tab);
        EXECUTE format('CREATE POLICY scoped_insert ON public.%I FOR INSERT WITH CHECK (public.ledger_allowed(tenant_id,book_id,true))', tab);
        EXECUTE format('CREATE POLICY owner_read ON public.%I FOR SELECT USING (access.owner_allowed(tenant_id))', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
    END LOOP;
END
$$;

CREATE POLICY scoped_update ON public.documents FOR UPDATE
    USING (public.ledger_allowed(tenant_id, book_id, true))
    WITH CHECK (public.ledger_allowed(tenant_id, book_id, true));

REVOKE ALL ON FUNCTION public.document_guard(), public.document_upload_guard(),
    public.document_upload_receipt(), public.document_completion_receipt() FROM PUBLIC;

CREATE FUNCTION public.document_location_valid(value jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE SET search_path = pg_catalog AS $$
    SELECT CASE WHEN jsonb_typeof(value) <> 'object' THEN false ELSE
        value - 'page' - 'row' - 'line' = '{}'::jsonb
        AND NOT EXISTS (
            SELECT 1 FROM jsonb_each(value) f
            WHERE jsonb_typeof(f.value) <> 'number' OR f.value::text !~ '^[1-9][0-9]{0,8}$'
        ) END
$$;

CREATE FUNCTION public.document_use(t uuid, b uuid, d uuid) RETURNS void
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.documents WHERE tenant_id = t AND book_id = b
        AND id = d AND state = 'ready' AND used_at IS NOT NULL) THEN
        RETURN;
    END IF;
    UPDATE public.documents SET used_at = clock_timestamp()
        WHERE tenant_id = t AND book_id = b AND id = d AND state = 'ready'
        AND used_at IS NULL AND expires_at > clock_timestamp();
    IF NOT FOUND THEN
        RAISE EXCEPTION 'verified original required' USING ERRCODE = '23514';
    END IF;
END
$$;

CREATE TABLE public.source_citations (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    source_id uuid NOT NULL,
    revision integer NOT NULL CHECK (revision > 0),
    sequence bigint NOT NULL CHECK (sequence > 0),
    entry_id uuid NOT NULL,
    position integer NOT NULL CHECK (position BETWEEN 1 AND 32),
    document_id uuid NOT NULL,
    location jsonb NOT NULL CHECK (public.document_location_valid(location)),
    actor_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 4000),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    UNIQUE (tenant_id, book_id, source_id, revision),
    UNIQUE (tenant_id, book_id, sequence),
    UNIQUE (tenant_id, book_id, receipt_id, position, revision),
    FOREIGN KEY (tenant_id, book_id, source_id) REFERENCES public.source_citations(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (tenant_id, book_id, entry_id) REFERENCES public.entries(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, document_id) REFERENCES public.documents(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK ((revision = 1 AND source_id = id AND reason = '')
        OR (revision > 1 AND source_id <> id AND length(reason) > 0))
);

CREATE UNIQUE INDEX source_citations_position ON public.source_citations(tenant_id, book_id, entry_id, position)
    WHERE revision = 1;
CREATE INDEX source_citations_document ON public.source_citations(tenant_id, book_id, document_id);
CREATE INDEX source_citations_entry ON public.source_citations(tenant_id, book_id, entry_id, sequence);

CREATE FUNCTION public.source_citation_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    entry public.entries%ROWTYPE;
    prior public.source_citations%ROWTYPE;
    transaction_id bigint;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    SELECT * INTO STRICT entry FROM public.entries
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.entry_id;
    IF NEW.revision = 1 THEN
        SELECT xmin::text::bigint INTO STRICT transaction_id FROM public.entries
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.entry_id;
        IF entry.receipt_id <> NEW.receipt_id OR entry.actor_id <> NEW.actor_id
            OR NOT public.ledger_import_owner()
                AND transaction_id <> mod(pg_current_xact_id()::text::numeric, 4294967296)::bigint THEN
            RAISE EXCEPTION 'sources belong to the original posting' USING ERRCODE = '23514';
        END IF;
    ELSE
        SELECT * INTO STRICT prior FROM public.source_citations
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND source_id = NEW.source_id
            ORDER BY revision DESC LIMIT 1;
        IF NEW.revision <> prior.revision + 1 OR NEW.entry_id <> prior.entry_id
            OR NEW.position <> prior.position THEN
            RAISE EXCEPTION 'stale citation revision' USING ERRCODE = '23514';
        END IF;
    END IF;
    PERFORM public.document_use(NEW.tenant_id, NEW.book_id, NEW.document_id);
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.recorded_at := clock_timestamp();
        SELECT write_epoch INTO STRICT NEW.sequence FROM public.books
            WHERE tenant_id = NEW.tenant_id AND id = NEW.book_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER source_citation_guard BEFORE INSERT ON public.source_citations
    FOR EACH ROW EXECUTE FUNCTION public.source_citation_guard();
CREATE TRIGGER source_citation_immutable BEFORE UPDATE OR DELETE ON public.source_citations
    FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();

CREATE FUNCTION public.source_citation_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    receipt public.requests%ROWTYPE;
    request jsonb;
    result jsonb;
BEGIN
    IF NEW.revision = 1 THEN
        RETURN NULL;
    END IF;
    SELECT * INTO STRICT receipt FROM public.requests
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.receipt_id;
    request := convert_from(receipt.request_bytes, 'UTF8')::jsonb;
    result := convert_from(receipt.result_bytes, 'UTF8')::jsonb;
    IF receipt.operation <> 'correct_source_citation' OR receipt.actor_id <> NEW.actor_id
        OR request->>'source_id' IS DISTINCT FROM NEW.source_id::text
        OR request->>'expected_revision' IS DISTINCT FROM (NEW.revision - 1)::text
        OR request->>'document_id' IS DISTINCT FROM NEW.document_id::text
        OR request->'location' IS DISTINCT FROM NEW.location OR request->>'reason' IS DISTINCT FROM NEW.reason
        OR result->>'id' IS DISTINCT FROM NEW.id::text OR result->>'source_id' IS DISTINCT FROM NEW.source_id::text
        OR result->>'revision' IS DISTINCT FROM NEW.revision::text
        OR result->>'sequence' IS DISTINCT FROM NEW.sequence::text
        OR result->>'entry_id' IS DISTINCT FROM NEW.entry_id::text
        OR result->>'position' IS DISTINCT FROM NEW.position::text
        OR result->>'receipt_id' IS DISTINCT FROM NEW.receipt_id::text
        OR result->>'actor_id' IS DISTINCT FROM NEW.actor_id::text
        OR result->>'document_id' IS DISTINCT FROM NEW.document_id::text
        OR result->'location' IS DISTINCT FROM NEW.location OR result->>'reason' IS DISTINCT FROM NEW.reason
        OR (result->>'recorded_at')::timestamptz IS DISTINCT FROM NEW.recorded_at THEN
        RAISE EXCEPTION 'citation receipt differs from revision' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER source_citation_receipt AFTER INSERT ON public.source_citations
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.source_citation_receipt();

CREATE FUNCTION public.source_posting_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    receipt public.requests%ROWTYPE;
    sources jsonb;
BEGIN
    SELECT * INTO STRICT receipt FROM public.requests
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.receipt_id;
    sources := coalesce(convert_from(receipt.request_bytes, 'UTF8')::jsonb->'sources', '[]'::jsonb);
    IF jsonb_typeof(sources) <> 'array' OR jsonb_array_length(sources) > 32 OR
        jsonb_array_length(sources) <> (SELECT count(*) FROM public.source_citations c
            WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id AND c.entry_id = NEW.id AND c.revision = 1)
        OR EXISTS (SELECT 1 FROM public.source_citations c
            WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id AND c.entry_id = NEW.id AND c.revision = 1
            AND sources->(c.position - 1) IS DISTINCT FROM jsonb_build_object('document_id', c.document_id::text, 'location', c.location)) THEN
        RAISE EXCEPTION 'posted sources differ from request' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER source_posting_receipt AFTER INSERT ON public.entries
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.source_posting_receipt();

ALTER TABLE public.source_citations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.source_citations FORCE ROW LEVEL SECURITY;
CREATE POLICY scoped_read ON public.source_citations FOR SELECT USING (public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY scoped_insert ON public.source_citations FOR INSERT WITH CHECK (public.ledger_allowed(tenant_id, book_id, true));
CREATE POLICY owner_read ON public.source_citations FOR SELECT USING (access.owner_allowed(tenant_id));
REVOKE ALL ON public.source_citations FROM PUBLIC;
REVOKE ALL ON FUNCTION public.document_use(uuid, uuid, uuid), public.source_citation_guard(),
    public.source_citation_receipt(), public.source_posting_receipt() FROM PUBLIC;

CREATE TABLE public.evidence_links (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    link_id uuid NOT NULL,
    revision integer NOT NULL CHECK (revision IN (1, 2)),
    sequence bigint NOT NULL CHECK (sequence > 0),
    entry_id uuid NOT NULL,
    document_id uuid NOT NULL,
    role text NOT NULL CHECK (role IN ('receipt', 'invoice', 'contract', 'statement', 'other')),
    action text NOT NULL CHECK (action IN ('attach', 'detach')),
    actor_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    reason text NOT NULL CHECK (length(reason) BETWEEN 1 AND 4000),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    UNIQUE (tenant_id, book_id, link_id, revision),
    UNIQUE (tenant_id, book_id, sequence),
    UNIQUE (tenant_id, book_id, receipt_id),
    FOREIGN KEY (tenant_id, book_id, link_id) REFERENCES public.evidence_links(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (tenant_id, book_id, entry_id) REFERENCES public.entries(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, document_id) REFERENCES public.documents(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK ((revision = 1 AND link_id = id AND action = 'attach')
        OR (revision = 2 AND link_id <> id AND action = 'detach'))
);

CREATE INDEX evidence_links_entry ON public.evidence_links(tenant_id, book_id, entry_id, sequence);
CREATE INDEX evidence_links_document ON public.evidence_links(tenant_id, book_id, document_id);

CREATE FUNCTION public.evidence_link_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    prior public.evidence_links%ROWTYPE;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF NEW.action = 'attach' THEN
        IF EXISTS (SELECT 1 FROM public.evidence_links e
            WHERE e.tenant_id = NEW.tenant_id AND e.book_id = NEW.book_id AND e.entry_id = NEW.entry_id
            AND e.document_id = NEW.document_id AND e.role = NEW.role AND e.revision = 1
            AND NOT EXISTS (SELECT 1 FROM public.evidence_links d
                WHERE d.tenant_id = e.tenant_id AND d.book_id = e.book_id AND d.link_id = e.link_id AND d.revision = 2)) THEN
            RAISE EXCEPTION 'evidence is already attached' USING ERRCODE = '23514';
        END IF;
    ELSE
        SELECT * INTO STRICT prior FROM public.evidence_links
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND link_id = NEW.link_id
            ORDER BY revision DESC LIMIT 1;
        IF prior.revision <> 1 OR ROW(NEW.entry_id, NEW.document_id, NEW.role)
            IS DISTINCT FROM ROW(prior.entry_id, prior.document_id, prior.role) THEN
            RAISE EXCEPTION 'evidence link changed' USING ERRCODE = '23514';
        END IF;
    END IF;
    PERFORM public.document_use(NEW.tenant_id, NEW.book_id, NEW.document_id);
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.recorded_at := clock_timestamp();
        SELECT write_epoch INTO STRICT NEW.sequence FROM public.books
            WHERE tenant_id = NEW.tenant_id AND id = NEW.book_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER evidence_link_guard BEFORE INSERT ON public.evidence_links
    FOR EACH ROW EXECUTE FUNCTION public.evidence_link_guard();
CREATE TRIGGER evidence_link_immutable BEFORE UPDATE OR DELETE ON public.evidence_links
    FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();

CREATE FUNCTION public.evidence_link_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    receipt public.requests%ROWTYPE;
    request jsonb;
    result jsonb;
BEGIN
    SELECT * INTO STRICT receipt FROM public.requests
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.receipt_id;
    request := convert_from(receipt.request_bytes, 'UTF8')::jsonb;
    result := convert_from(receipt.result_bytes, 'UTF8')::jsonb;
    IF receipt.operation <> NEW.action||'_evidence' OR receipt.actor_id <> NEW.actor_id
        OR request->>'reason' IS DISTINCT FROM NEW.reason
        OR NEW.action = 'attach' AND (request->>'entry_id' IS DISTINCT FROM NEW.entry_id::text
            OR request->>'document_id' IS DISTINCT FROM NEW.document_id::text OR request->>'role' IS DISTINCT FROM NEW.role)
        OR NEW.action = 'detach' AND request->>'link_id' IS DISTINCT FROM NEW.link_id::text
        OR result->>'id' IS DISTINCT FROM NEW.id::text OR result->>'link_id' IS DISTINCT FROM NEW.link_id::text
        OR result->>'revision' IS DISTINCT FROM NEW.revision::text OR result->>'sequence' IS DISTINCT FROM NEW.sequence::text
        OR result->>'entry_id' IS DISTINCT FROM NEW.entry_id::text OR result->>'document_id' IS DISTINCT FROM NEW.document_id::text
        OR result->>'role' IS DISTINCT FROM NEW.role OR result->>'action' IS DISTINCT FROM NEW.action
        OR result->>'actor_id' IS DISTINCT FROM NEW.actor_id::text OR result->>'receipt_id' IS DISTINCT FROM NEW.receipt_id::text
        OR result->>'reason' IS DISTINCT FROM NEW.reason
        OR (result->>'recorded_at')::timestamptz IS DISTINCT FROM NEW.recorded_at THEN
        RAISE EXCEPTION 'evidence receipt differs from link history' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER evidence_link_receipt AFTER INSERT ON public.evidence_links
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.evidence_link_receipt();

ALTER TABLE public.evidence_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.evidence_links FORCE ROW LEVEL SECURITY;
CREATE POLICY scoped_read ON public.evidence_links FOR SELECT USING (public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY scoped_insert ON public.evidence_links FOR INSERT WITH CHECK (public.ledger_allowed(tenant_id, book_id, true));
CREATE POLICY owner_read ON public.evidence_links FOR SELECT USING (access.owner_allowed(tenant_id));
REVOKE ALL ON public.evidence_links FROM PUBLIC;
REVOKE ALL ON FUNCTION public.evidence_link_guard(), public.evidence_link_receipt() FROM PUBLIC;

CREATE FUNCTION public.document_used_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.used_at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM public.source_citations
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND document_id = NEW.id)
        AND NOT EXISTS (SELECT 1 FROM public.evidence_links
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND document_id = NEW.id) THEN
        RAISE EXCEPTION 'document use requires recorded history' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER document_used_receipt AFTER INSERT OR UPDATE ON public.documents
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.document_used_receipt();
REVOKE ALL ON FUNCTION public.document_used_receipt() FROM PUBLIC;

-- Every inserted line must still match its immutable receipt, including lines
-- inserted after an entry's deferred checks have been forced to run early.
CREATE FUNCTION public.ledger_line_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    saved jsonb;
BEGIN
    SELECT (convert_from(r.result_bytes, 'UTF8')::jsonb)->'lines'->(NEW.position - 1)
        INTO saved
        FROM public.entries e JOIN public.requests r
            ON r.tenant_id = e.tenant_id AND r.book_id = e.book_id AND r.id = e.receipt_id
        WHERE e.tenant_id = NEW.tenant_id AND e.book_id = NEW.book_id AND e.id = NEW.entry_id;
    IF NOT FOUND
        OR saved->>'id' IS DISTINCT FROM NEW.id::text
        OR saved->>'account_id' IS DISTINCT FROM NEW.account_id::text
        OR saved->>'side' IS DISTINCT FROM NEW.side
        OR saved->>'amount_minor' IS DISTINCT FROM NEW.amount_minor::text
        OR coalesce(saved->>'memo', '') IS DISTINCT FROM NEW.memo
        OR saved->>'reverses_line_id' IS DISTINCT FROM NEW.reverses_line_id::text THEN
        RAISE EXCEPTION 'journal receipt differs from the recorded line' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END
$$;

CREATE CONSTRAINT TRIGGER line_receipt AFTER INSERT ON public.lines
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.ledger_line_receipt();
REVOKE ALL ON FUNCTION public.ledger_line_receipt() FROM PUBLIC;

CREATE FUNCTION public.ledger_check_balance_history(t uuid, b uuid, eid uuid) RETURNS void
    LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM (
        SELECT sum(change) OVER (PARTITION BY account_id ORDER BY sequence) AS balance FROM (
            SELECT l.account_id, j.sequence,
                sum(CASE l.side WHEN 'debit' THEN l.amount_minor::numeric ELSE -l.amount_minor::numeric END) AS change
            FROM public.lines l JOIN public.entries j
                ON j.tenant_id = l.tenant_id AND j.book_id = l.book_id AND j.id = l.entry_id
            WHERE l.tenant_id = t AND l.book_id = b
                AND (eid IS NULL OR l.account_id IN (SELECT account_id FROM public.lines
                    WHERE tenant_id = t AND book_id = b AND entry_id = eid))
            GROUP BY l.account_id, j.sequence
        ) changes
    ) positions WHERE balance NOT BETWEEN -9223372036854775808::numeric AND 9223372036854775807::numeric) THEN
        RAISE EXCEPTION 'account balance is outside the supported integer range' USING ERRCODE = '22003';
    END IF;
END
$$;
REVOKE ALL ON FUNCTION public.ledger_check_balance_history(uuid, uuid, uuid) FROM PUBLIC;

-- Authorization is checked once before scanning the selected account history.
-- Keep all historical prefixes: a final in-range total can hide an overflow.
CREATE FUNCTION public.ledger_validate_balances(t uuid, b uuid, eid uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF (public.ledger_context(t, b)
        AND (public.ledger_import_owner() OR public.ledger_allowed(t, b, true))) IS NOT TRUE THEN
        RAISE EXCEPTION 'selected Book write access required' USING ERRCODE = '42501';
    END IF;
    -- The trusted importer checks every prefix of the completed Book after
    -- flushing the other constraints. A runtime session cannot opt out.
    IF public.ledger_import_owner()
        AND current_setting('vult.restore_book', true) = t::text || '/' || b::text THEN
        RETURN;
    END IF;
    PERFORM public.ledger_check_balance_history(t, b, eid);
END
$$;
REVOKE ALL ON FUNCTION public.ledger_validate_balances(uuid, uuid, uuid) FROM PUBLIC;

CREATE FUNCTION public.ledger_validate_restored_book(t uuid, b uuid) RETURNS void
    LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF (public.ledger_import_owner() AND public.ledger_context(t, b)) IS NOT TRUE THEN
        RAISE EXCEPTION 'selected Book operator access required' USING ERRCODE = '42501';
    END IF;
    PERFORM public.ledger_check_balance_history(t, b, NULL);
END
$$;
REVOKE ALL ON FUNCTION public.ledger_validate_restored_book(uuid, uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.ledger_validate_entry() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog,pg_temp AS $$
DECLARE e public.entries%ROWTYPE; r public.requests%ROWTYPE; result jsonb;
 eid uuid; count_lines integer; debits numeric;credits numeric;
BEGIN
 IF TG_TABLE_NAME='entries' THEN eid=NEW.id;ELSE eid=NEW.entry_id;END IF;
 SELECT * INTO STRICT e FROM public.entries WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND id=eid;
 PERFORM public.ledger_lock(e.tenant_id,e.book_id);
 SELECT * INTO STRICT r FROM public.requests WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND id=e.receipt_id;
 result=convert_from(r.result_bytes,'UTF8')::jsonb;
 IF r.actor_id<>e.actor_id OR r.operation<>(CASE WHEN e.reversal_of IS NULL THEN 'post_journal' ELSE 'reverse_journal' END)
 OR result->>'id' IS DISTINCT FROM e.id::text OR result->>'receipt_id' IS DISTINCT FROM r.id::text
 OR result->>'actor_id' IS DISTINCT FROM e.actor_id::text
 OR (result->>'sequence')::bigint IS DISTINCT FROM e.sequence
 OR result->>'economic_date' IS DISTINCT FROM e.economic_date::text
 OR result->>'accounting_date' IS DISTINCT FROM e.accounting_date::text
 OR result->>'description' IS DISTINCT FROM e.description
 OR (result->>'recorded_at')::timestamptz IS DISTINCT FROM e.recorded_at
 OR result->>'reversal_of' IS DISTINCT FROM e.reversal_of::text
 OR jsonb_typeof(result->'lines') IS DISTINCT FROM 'array'
 THEN RAISE EXCEPTION 'journal receipt differs from the recorded entry' USING ERRCODE='23514';END IF;
 SELECT count(*),coalesce(sum(amount_minor::numeric) FILTER(WHERE side='debit'),0),
 coalesce(sum(amount_minor::numeric) FILTER(WHERE side='credit'),0)
 INTO count_lines,debits,credits FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id;
 IF count_lines<2 OR count_lines>256 OR debits<>credits OR debits=0
 OR (SELECT min(position)<>1 OR max(position)<>count(*) FROM public.lines
     WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id)
 THEN RAISE EXCEPTION 'journal must contain balanced debit and credit lines' USING ERRCODE='23514';END IF;
 IF jsonb_array_length(result->'lines')<>count_lines OR EXISTS(
  SELECT 1 FROM public.lines l JOIN LATERAL(SELECT result->'lines'->(l.position-1) AS item) saved ON true
  WHERE l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id AND
  (item->>'id' IS DISTINCT FROM l.id::text OR item->>'account_id' IS DISTINCT FROM l.account_id::text
   OR item->>'side' IS DISTINCT FROM l.side OR item->>'amount_minor' IS DISTINCT FROM l.amount_minor::text
   OR coalesce(item->>'memo','')<>l.memo OR item->>'reverses_line_id' IS DISTINCT FROM l.reverses_line_id::text))
 THEN RAISE EXCEPTION 'journal receipt differs from the recorded lines' USING ERRCODE='23514';END IF;
 IF NOT public.ledger_import_owner() AND EXISTS(SELECT 1 FROM public.closed_periods p WHERE p.tenant_id=e.tenant_id AND p.book_id=e.book_id
 AND e.accounting_date BETWEEN p.start_date AND p.end_date)
 THEN RAISE EXCEPTION 'accounting period is closed' USING ERRCODE='23514';END IF;
 IF e.reversal_of IS NULL THEN
  IF EXISTS(SELECT 1 FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id AND reverses_line_id IS NOT NULL)
  THEN RAISE EXCEPTION 'reversal parent required' USING ERRCODE='23514';END IF;
 ELSE
  IF NOT EXISTS(SELECT 1 FROM public.entries original WHERE original.tenant_id=e.tenant_id AND original.book_id=e.book_id
   AND original.id=e.reversal_of AND original.economic_date=e.economic_date AND original.sequence<e.sequence)
  OR count_lines<>(SELECT count(*) FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.reversal_of)
  OR EXISTS(SELECT 1 FROM public.lines l LEFT JOIN public.lines original ON original.tenant_id=l.tenant_id
   AND original.book_id=l.book_id AND original.id=l.reverses_line_id
   WHERE l.tenant_id=e.tenant_id AND l.book_id=e.book_id AND l.entry_id=e.id AND
   (original.id IS NULL OR original.entry_id<>e.reversal_of OR original.account_id<>l.account_id
    OR original.amount_minor<>l.amount_minor OR original.side=l.side OR original.position<>l.position OR original.memo<>l.memo))
  THEN RAISE EXCEPTION 'reversal must mirror every original line' USING ERRCODE='23514';END IF;
 END IF;
 PERFORM public.ledger_validate_balances(e.tenant_id,e.book_id,e.id);
 RETURN NULL;
END $$;
