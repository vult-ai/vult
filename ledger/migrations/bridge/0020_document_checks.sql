-- Checks on each document, as they happen.
--
-- Vult checks what it read from a statement or receipt against the Book: the account it belongs to, its period beside earlier statements,
-- whether its balances add up, whether it is already in the Book, and which
-- transaction a receipt belongs to. Each check is one row, kept with the
-- Book; a later row for the same document and check replaces the earlier
-- one for the owner. Rows hold short compared values, never document
-- contents.
CREATE TABLE public.document_checks (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    document_id uuid NOT NULL,
    check_name text NOT NULL CHECK (check_name IN ('statement_account', 'statement_holder', 'statement_address', 'statement_period', 'statement_balances',
        'duplicate', 'receipt_details', 'receipt_matched', 'done')),
    -- running: started. ok, note (passed, with a note), review (a person
    -- needs to look) or failed: how it ended.
    result text NOT NULL CHECK (result IN ('running', 'ok', 'note', 'review', 'failed')),
    against text NOT NULL DEFAULT '' CHECK (length(against) <= 200),
    found text NOT NULL DEFAULT '' CHECK (length(found) <= 200),
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 300),
    fix text NOT NULL DEFAULT '' CHECK (length(fix) <= 300),
    -- A button the owner's screen offers with the fix.
    action text NOT NULL DEFAULT '' CHECK (action IN ('', 'confirm_reading')),
    actor_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, sequence),
    FOREIGN KEY (tenant_id, book_id, document_id) REFERENCES public.documents(tenant_id, book_id, id),
    -- A check that didn't simply pass always says why.
    CHECK (result IN ('running', 'ok') OR reason <> '')
);
CREATE INDEX document_checks_document ON public.document_checks(tenant_id, book_id, document_id, sequence);

-- Each Book numbers its checks in order, so the owner's screen reads what's
-- new after the last one it saw. Restoring a Book keeps its numbers.
CREATE FUNCTION public.document_check_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_import_owner() AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    -- A restored row keeps its number and time; a new one takes the next.
    IF NEW.sequence IS NULL THEN
        PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('document_checks/' || NEW.book_id::text, 0));
        SELECT coalesce(max(c.sequence), 0) + 1 INTO NEW.sequence FROM public.document_checks c
            WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id;
        NEW.recorded_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER document_check_guard BEFORE INSERT ON public.document_checks
    FOR EACH ROW EXECUTE FUNCTION public.document_check_guard();
CREATE TRIGGER document_check_immutable BEFORE UPDATE OR DELETE ON public.document_checks
    FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.document_checks
    FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('book_id');

-- The owner's screen hears of each check as it happens.
CREATE TRIGGER book_changed AFTER INSERT ON public.document_checks FOR EACH ROW EXECUTE FUNCTION public.book_changed('documents');

-- Only Vult writes checks and readings: its document checker and its own
-- reading of statement files. An agent's proposed reading is written by
-- Vult once it has verified it against the document; no agent tool writes
-- a check. Anyone who may read the Book reads them.
ALTER TABLE public.document_checks ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.document_checks FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.document_checks FROM PUBLIC;
CREATE POLICY check_read ON public.document_checks FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY check_insert ON public.document_checks FOR INSERT WITH CHECK (public.ledger_context(tenant_id, book_id));
REVOKE ALL ON FUNCTION public.document_check_guard() FROM PUBLIC;

-- What Vult read from each document, and how: from a bank's export, a PDF's
-- text, a local scan, Vult's AI, an agent's proposal Vult verified, or the
-- owner's confirmation. The latest reading of a document is current; a
-- later one fills what an earlier one missed. It holds the fields read and
-- the page each came from, never the document itself.
CREATE TABLE public.document_readings (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    document_id uuid NOT NULL,
    read_by text NOT NULL CHECK (read_by IN ('export', 'pdf', 'ocr', 'ai', 'agent', 'owner')),
    reader text NOT NULL DEFAULT '' CHECK (length(reader) <= 120),
    fields jsonb NOT NULL CHECK (jsonb_typeof(fields) = 'object' AND octet_length(fields::text) <= 1048576),
    missing text[] NOT NULL DEFAULT '{}' CHECK (cardinality(missing) <= 16),
    actor_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, sequence),
    FOREIGN KEY (tenant_id, book_id, document_id) REFERENCES public.documents(tenant_id, book_id, id)
);
CREATE INDEX document_readings_document ON public.document_readings(tenant_id, book_id, document_id, sequence);

CREATE FUNCTION public.document_reading_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_import_owner() AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF NEW.sequence IS NULL THEN
        PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('document_readings/' || NEW.book_id::text, 0));
        SELECT coalesce(max(r.sequence), 0) + 1 INTO NEW.sequence FROM public.document_readings r
            WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id;
        NEW.recorded_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER document_reading_guard BEFORE INSERT ON public.document_readings
    FOR EACH ROW EXECUTE FUNCTION public.document_reading_guard();
CREATE TRIGGER document_reading_immutable BEFORE UPDATE OR DELETE ON public.document_readings
    FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.document_readings
    FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('book_id');
CREATE TRIGGER book_changed AFTER INSERT ON public.document_readings FOR EACH ROW EXECUTE FUNCTION public.book_changed('documents');
ALTER TABLE public.document_readings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.document_readings FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.document_readings FROM PUBLIC;
CREATE POLICY reading_read ON public.document_readings FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY reading_insert ON public.document_readings FOR INSERT WITH CHECK (public.ledger_context(tenant_id, book_id));
REVOKE ALL ON FUNCTION public.document_reading_guard() FROM PUBLIC;
