-- Refusals say which Book and what to do; an archived Book stays archived
-- through a cancelled deletion; two Books of one Vult never share a name.

-- A change dated in a closed period says which Book, which period, and
-- what to do.
CREATE OR REPLACE FUNCTION public.ledger_entry_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE closed record;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id,NEW.book_id);
    IF NOT public.ledger_import_owner() OR NEW.sequence<=0 THEN
        SELECT write_epoch INTO STRICT NEW.sequence FROM public.books WHERE tenant_id=NEW.tenant_id AND id=NEW.book_id;
    END IF;
    IF NOT public.ledger_import_owner() AND (NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id',true)) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE='42501';
    END IF;
    IF NOT public.ledger_import_owner() THEN
        NEW.recorded_at=clock_timestamp();
    END IF;
    IF NOT public.ledger_import_owner() AND public.ledger_period_closed(NEW.tenant_id,NEW.book_id,NEW.accounting_date) THEN
        SELECT b.name, p.start_date, p.end_date INTO closed FROM public.accounting_periods p JOIN public.books b ON b.tenant_id = p.tenant_id AND b.id = p.book_id
            WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id AND NEW.accounting_date BETWEEN p.start_date AND p.end_date
            ORDER BY p.end_date DESC LIMIT 1;
        RAISE EXCEPTION 'accounting period is closed in % from % to %, and % falls in it. Reopen that period in %, or date this after %',
            closed.name, closed.start_date, closed.end_date, NEW.accounting_date, closed.name, closed.end_date USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;

-- Deleting an archived Book keeps it archived: cancelled, it stays out of
-- menus as before; erased, its archived row goes with it.
CREATE OR REPLACE FUNCTION public.book_deletion_start(t uuid, b uuid, typed text, who text, path text, address text, agent text)
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

-- Two Books of one Vult can't share a name, even created at the same moment
-- from two tabs: each new Book waits for the other and then sees its name.
-- A restore or an import keeps the names it brings.
CREATE FUNCTION public.book_name_free() RETURNS trigger
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN NEW;
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended('vult.book_names.' || NEW.tenant_id::text, 0));
    IF EXISTS (SELECT 1 FROM public.books k WHERE k.tenant_id = NEW.tenant_id AND k.id <> NEW.id
            AND lower(btrim(public.book_current_name(k.tenant_id, k.id))) = lower(btrim(NEW.name))) THEN
        RAISE EXCEPTION 'This Vult already has a Book named %. Choose another name.', btrim(NEW.name) USING ERRCODE = 'VB409';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER book_name_free BEFORE INSERT ON public.books FOR EACH ROW EXECUTE FUNCTION public.book_name_free();
REVOKE ALL ON FUNCTION public.book_name_free() FROM PUBLIC;
