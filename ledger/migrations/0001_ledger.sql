CREATE TABLE public.tenants (
 id uuid PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE public.books (
 tenant_id uuid NOT NULL REFERENCES public.tenants(id), id uuid PRIMARY KEY,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 120),
 currency text NOT NULL CHECK(currency ~ '^[A-Z]{3}$'),
 scale smallint NOT NULL CHECK(scale BETWEEN 0 AND 4),
 write_epoch bigint NOT NULL DEFAULT 0 CHECK(write_epoch>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(tenant_id,id)
);
CREATE TABLE public.connections (
 tenant_id uuid NOT NULL,book_id uuid NOT NULL,id uuid PRIMARY KEY,actor_id uuid NOT NULL UNIQUE,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 80),
 access text NOT NULL CHECK(access IN('read','write')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),revoked_at timestamptz,
 UNIQUE(tenant_id,book_id,id),
 FOREIGN KEY(tenant_id,book_id) REFERENCES public.books(tenant_id,id)
);
CREATE TABLE public.accounts (
 tenant_id uuid NOT NULL,book_id uuid NOT NULL,id uuid PRIMARY KEY,
 class text NOT NULL CHECK(class IN('asset','liability','equity','income','expense')),
 code text NOT NULL DEFAULT '',name text NOT NULL DEFAULT '',revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(tenant_id,book_id,id),
 FOREIGN KEY(tenant_id,book_id) REFERENCES public.books(tenant_id,id),
 CHECK(code='' OR (length(code)<=32 AND code ~ '^[A-Za-z0-9][A-Za-z0-9._/-]*$')),
 CHECK(length(name)<=120)
);
CREATE UNIQUE INDEX accounts_current_code ON public.accounts(tenant_id,book_id,code) WHERE code<>'';
CREATE TABLE public.account_revisions (
 tenant_id uuid NOT NULL,book_id uuid NOT NULL,account_id uuid NOT NULL,
 revision bigint NOT NULL CHECK(revision>0),code text NOT NULL,name text NOT NULL,
 actor_id uuid NOT NULL,recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,book_id,account_id,revision),
 FOREIGN KEY(tenant_id,book_id,account_id) REFERENCES public.accounts(tenant_id,book_id,id),
 CHECK(length(code) BETWEEN 1 AND 32 AND code ~ '^[A-Za-z0-9][A-Za-z0-9._/-]*$'),
 CHECK(length(name) BETWEEN 1 AND 120)
);
CREATE TABLE public.requests (
 tenant_id uuid NOT NULL,book_id uuid NOT NULL,id uuid PRIMARY KEY,actor_id uuid NOT NULL,
 request_key text NOT NULL CHECK(length(request_key) BETWEEN 1 AND 128),
 operation text NOT NULL CHECK(length(operation) BETWEEN 1 AND 64),
 request_bytes bytea NOT NULL CHECK(octet_length(request_bytes) BETWEEN 2 AND 1048576),
 request_sha256 text GENERATED ALWAYS AS(encode(sha256(request_bytes),'hex')) STORED,
 result_bytes bytea NOT NULL CHECK(octet_length(result_bytes) BETWEEN 2 AND 1048576),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(tenant_id,book_id,id),UNIQUE(tenant_id,book_id,actor_id,request_key),
 FOREIGN KEY(tenant_id,book_id) REFERENCES public.books(tenant_id,id)
);
CREATE TABLE public.entries (
 tenant_id uuid NOT NULL,book_id uuid NOT NULL,id uuid PRIMARY KEY,
 sequence bigint NOT NULL CHECK(sequence>0),receipt_id uuid NOT NULL,actor_id uuid NOT NULL,
 economic_date date NOT NULL CHECK(economic_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
 accounting_date date NOT NULL CHECK(accounting_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
 description text NOT NULL CHECK(length(description) BETWEEN 1 AND 4000),
 reversal_of uuid,recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(tenant_id,book_id,id),UNIQUE(tenant_id,book_id,sequence),UNIQUE(tenant_id,book_id,reversal_of),
 UNIQUE(tenant_id,book_id,receipt_id),
 FOREIGN KEY(tenant_id,book_id) REFERENCES public.books(tenant_id,id),
 FOREIGN KEY(tenant_id,book_id,receipt_id) REFERENCES public.requests(tenant_id,book_id,id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(tenant_id,book_id,reversal_of) REFERENCES public.entries(tenant_id,book_id,id) DEFERRABLE INITIALLY DEFERRED,
 CHECK(reversal_of IS DISTINCT FROM id)
);
CREATE TABLE public.lines (
 tenant_id uuid NOT NULL,book_id uuid NOT NULL,id uuid PRIMARY KEY,entry_id uuid NOT NULL,
 position integer NOT NULL CHECK(position BETWEEN 1 AND 256),account_id uuid NOT NULL,
 side text NOT NULL CHECK(side IN('debit','credit')),amount_minor bigint NOT NULL CHECK(amount_minor>0),
 memo text NOT NULL DEFAULT '' CHECK(length(memo)<=1000),reverses_line_id uuid,
 UNIQUE(tenant_id,book_id,id),UNIQUE(tenant_id,book_id,entry_id,position),
 UNIQUE(tenant_id,book_id,reverses_line_id),
 FOREIGN KEY(tenant_id,book_id,entry_id) REFERENCES public.entries(tenant_id,book_id,id) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(tenant_id,book_id,account_id) REFERENCES public.accounts(tenant_id,book_id,id),
 FOREIGN KEY(tenant_id,book_id,reverses_line_id) REFERENCES public.lines(tenant_id,book_id,id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX lines_account ON public.lines(tenant_id,book_id,account_id,entry_id);
CREATE TABLE public.closed_periods (
 tenant_id uuid NOT NULL,book_id uuid NOT NULL,id uuid PRIMARY KEY,
 start_date date NOT NULL CHECK(start_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
 end_date date NOT NULL CHECK(end_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
 closed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(start_date<=end_date),
 FOREIGN KEY(tenant_id,book_id) REFERENCES public.books(tenant_id,id)
);

CREATE FUNCTION public.ledger_context(t uuid,b uuid) RETURNS boolean
 LANGUAGE sql STABLE SET search_path=pg_catalog AS $$
 SELECT t::text=coalesce(current_setting('vult.tenant_id',true),'')
 AND b::text=coalesce(current_setting('vult.book_id',true),'')
$$;
CREATE FUNCTION public.ledger_import_owner() RETURNS boolean
 LANGUAGE sql STABLE SET search_path=pg_catalog AS $$
 SELECT coalesce((SELECT rolsuper FROM pg_roles WHERE rolname=session_user),false)
$$;
CREATE FUNCTION public.ledger_allowed(t uuid,b uuid,writing boolean) RETURNS boolean
 LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$
 SELECT public.ledger_context(t,b) AND EXISTS(SELECT 1 FROM public.connections c
 WHERE c.tenant_id=t AND c.book_id=b AND c.id::text=coalesce(current_setting('vult.connection_id',true),'')
 AND c.actor_id::text=coalesce(current_setting('vult.actor_id',true),'')
 AND c.revoked_at IS NULL AND (NOT writing OR c.access='write'))
$$;
CREATE FUNCTION public.ledger_lock(t uuid,b uuid) RETURNS void
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF current_setting('transaction_isolation')<>'read committed'
 THEN RAISE EXCEPTION 'ledger writes require READ COMMITTED' USING ERRCODE='25001';END IF;
 IF NOT coalesce(public.ledger_context(t,b),false)
 THEN RAISE EXCEPTION 'selected Book required' USING ERRCODE='42501';END IF;
 UPDATE public.books SET write_epoch=write_epoch+1 WHERE tenant_id=t AND id=b;
 IF NOT FOUND THEN RAISE EXCEPTION 'Book unavailable' USING ERRCODE='42501';END IF;
END $$;
CREATE FUNCTION public.ledger_immutable() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN RAISE EXCEPTION 'record is immutable' USING ERRCODE='23514';END $$;
CREATE FUNCTION public.ledger_book_guard() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' OR (to_jsonb(NEW)-'write_epoch') IS DISTINCT FROM(to_jsonb(OLD)-'write_epoch')
 OR NEW.write_epoch<OLD.write_epoch
 OR (NOT public.ledger_import_owner() AND NEW.write_epoch<>OLD.write_epoch+1)
 THEN RAISE EXCEPTION 'Book identity is immutable' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER book_guard BEFORE UPDATE OR DELETE ON public.books FOR EACH ROW EXECUTE FUNCTION public.ledger_book_guard();

CREATE FUNCTION public.ledger_account_guard() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'account identity is permanent' USING ERRCODE='23514';END IF;
 IF TG_OP='UPDATE' AND (pg_trigger_depth()<2 OR
 ROW(NEW.tenant_id,NEW.book_id,NEW.id,NEW.class,NEW.created_at) IS DISTINCT FROM
 ROW(OLD.tenant_id,OLD.book_id,OLD.id,OLD.class,OLD.created_at))
 THEN RAISE EXCEPTION 'use an account revision' USING ERRCODE='23514';END IF;
 PERFORM public.ledger_lock(NEW.tenant_id,NEW.book_id);
 RETURN NEW;
END $$;
CREATE TRIGGER account_guard BEFORE INSERT OR UPDATE OR DELETE ON public.accounts FOR EACH ROW EXECUTE FUNCTION public.ledger_account_guard();
CREATE FUNCTION public.ledger_account_revision() RETURNS trigger
 LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE prior bigint;
BEGIN
 PERFORM public.ledger_lock(NEW.tenant_id,NEW.book_id);
 IF NOT public.ledger_import_owner() THEN
  IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id',true)
  THEN RAISE EXCEPTION 'authenticated actor required' USING ERRCODE='42501';END IF;
  NEW.recorded_at=clock_timestamp();
 END IF;
 SELECT revision INTO STRICT prior FROM public.accounts
 WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND id=NEW.account_id FOR UPDATE;
 IF NEW.revision<>prior+1 THEN RAISE EXCEPTION 'stale account revision' USING ERRCODE='23514';END IF;
 UPDATE public.accounts SET code=NEW.code,name=NEW.name,revision=NEW.revision
 WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND id=NEW.account_id;
 RETURN NEW;
END $$;
CREATE TRIGGER account_revision_apply BEFORE INSERT ON public.account_revisions FOR EACH ROW EXECUTE FUNCTION public.ledger_account_revision();
CREATE TRIGGER account_revision_immutable BEFORE UPDATE OR DELETE ON public.account_revisions FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE FUNCTION public.ledger_account_final() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM public.accounts a WHERE a.tenant_id=NEW.tenant_id AND a.book_id=NEW.book_id AND a.id=NEW.id
 AND(a.revision<1 OR NOT EXISTS(SELECT 1 FROM public.account_revisions r WHERE r.tenant_id=a.tenant_id AND r.book_id=a.book_id
 AND r.account_id=a.id AND r.revision=a.revision AND r.code=a.code AND r.name=a.name)))
 THEN RAISE EXCEPTION 'account revision required' USING ERRCODE='23514';END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER account_final AFTER INSERT OR UPDATE ON public.accounts DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW EXECUTE FUNCTION public.ledger_account_final();

CREATE FUNCTION public.ledger_entry_guard() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 PERFORM public.ledger_lock(NEW.tenant_id,NEW.book_id);
 IF NOT public.ledger_import_owner() OR NEW.sequence<=0 THEN
  SELECT write_epoch INTO STRICT NEW.sequence FROM public.books WHERE tenant_id=NEW.tenant_id AND id=NEW.book_id;
 END IF;
 IF NOT public.ledger_import_owner() AND (NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id',true))
 THEN RAISE EXCEPTION 'authenticated actor required' USING ERRCODE='42501';END IF;
 IF NOT public.ledger_import_owner() THEN NEW.recorded_at=clock_timestamp();END IF;
 IF NOT public.ledger_import_owner() AND EXISTS(SELECT 1 FROM public.closed_periods p WHERE p.tenant_id=NEW.tenant_id AND p.book_id=NEW.book_id
 AND NEW.accounting_date BETWEEN p.start_date AND p.end_date)
 THEN RAISE EXCEPTION 'accounting period is closed' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER entry_guard BEFORE INSERT ON public.entries FOR EACH ROW EXECUTE FUNCTION public.ledger_entry_guard();
CREATE TRIGGER entry_immutable BEFORE UPDATE OR DELETE ON public.entries FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE FUNCTION public.ledger_line_guard() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 PERFORM public.ledger_lock(NEW.tenant_id,NEW.book_id);
 IF EXISTS(SELECT 1 FROM public.requests r JOIN public.entries e ON e.receipt_id=r.id
 WHERE e.tenant_id=NEW.tenant_id AND e.book_id=NEW.book_id AND e.id=NEW.entry_id
 AND e.xmin::text::bigint<>mod(pg_current_xact_id()::text::numeric,4294967296)::bigint)
 THEN RAISE EXCEPTION 'posted lines are immutable' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER line_guard BEFORE INSERT ON public.lines FOR EACH ROW EXECUTE FUNCTION public.ledger_line_guard();
CREATE TRIGGER line_immutable BEFORE UPDATE OR DELETE ON public.lines FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE TRIGGER request_immutable BEFORE UPDATE OR DELETE ON public.requests FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE FUNCTION public.ledger_request_guard() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 PERFORM public.ledger_lock(NEW.tenant_id,NEW.book_id);
 IF NOT public.ledger_import_owner() THEN
  IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id',true)
  THEN RAISE EXCEPTION 'authenticated actor required' USING ERRCODE='42501';END IF;
  NEW.recorded_at=clock_timestamp();
 END IF;
 IF jsonb_typeof(convert_from(NEW.request_bytes,'UTF8')::jsonb)<>'object'
 OR jsonb_typeof(convert_from(NEW.result_bytes,'UTF8')::jsonb)<>'object'
 OR (convert_from(NEW.request_bytes,'UTF8')::jsonb->>'request_key') IS DISTINCT FROM NEW.request_key
 THEN RAISE EXCEPTION 'request receipt must contain the original key and result' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER request_guard BEFORE INSERT ON public.requests FOR EACH ROW EXECUTE FUNCTION public.ledger_request_guard();

CREATE FUNCTION public.ledger_validate_entry() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
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
 IF EXISTS(SELECT 1 FROM(
  SELECT sum(change) OVER(PARTITION BY account_id ORDER BY sequence) AS balance FROM(
   SELECT l.account_id,j.sequence,sum(CASE l.side WHEN 'debit' THEN l.amount_minor::numeric ELSE -l.amount_minor::numeric END) AS change
   FROM public.lines l JOIN public.entries j ON j.tenant_id=l.tenant_id AND j.book_id=l.book_id AND j.id=l.entry_id
   WHERE l.tenant_id=e.tenant_id AND l.book_id=e.book_id
   AND l.account_id IN(SELECT account_id FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id)
   GROUP BY l.account_id,j.sequence
  ) changes
 ) positions WHERE balance NOT BETWEEN -9223372036854775808::numeric AND 9223372036854775807::numeric)
 THEN RAISE EXCEPTION 'account balance is outside the supported integer range' USING ERRCODE='22003';END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER entry_final AFTER INSERT ON public.entries DEFERRABLE INITIALLY DEFERRED
 FOR EACH ROW EXECUTE FUNCTION public.ledger_validate_entry();

CREATE FUNCTION public.ledger_close_period() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 PERFORM public.ledger_lock(NEW.tenant_id,NEW.book_id);
 IF EXISTS(SELECT 1 FROM public.closed_periods p WHERE p.tenant_id=NEW.tenant_id AND p.book_id=NEW.book_id
 AND p.start_date<=NEW.end_date AND p.end_date>=NEW.start_date)
 THEN RAISE EXCEPTION 'period overlaps an existing closed period' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER period_guard BEFORE INSERT ON public.closed_periods FOR EACH ROW EXECUTE FUNCTION public.ledger_close_period();
CREATE TRIGGER period_immutable BEFORE UPDATE OR DELETE ON public.closed_periods FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE FUNCTION public.ledger_connection_guard() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' OR (to_jsonb(NEW)-'revoked_at') IS DISTINCT FROM(to_jsonb(OLD)-'revoked_at')
 OR OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL
 THEN RAISE EXCEPTION 'connection scope is immutable' USING ERRCODE='23514';END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER connection_guard BEFORE UPDATE OR DELETE ON public.connections FOR EACH ROW EXECUTE FUNCTION public.ledger_connection_guard();

DO $$DECLARE tab text;BEGIN
 FOREACH tab IN ARRAY ARRAY['books','accounts','account_revisions','requests','entries','lines','closed_periods'] LOOP
  EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY',tab);
  EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY',tab);
  IF tab='books' THEN
   EXECUTE 'CREATE POLICY book_read ON public.books FOR SELECT USING(public.ledger_allowed(tenant_id,id,false))';
   EXECUTE 'CREATE POLICY book_write ON public.books FOR UPDATE USING(public.ledger_allowed(tenant_id,id,false)) WITH CHECK(public.ledger_allowed(tenant_id,id,true))';
  ELSE
   EXECUTE format('CREATE POLICY scoped_read ON public.%I FOR SELECT USING(public.ledger_allowed(tenant_id,book_id,false))',tab);
   EXECUTE format('CREATE POLICY scoped_insert ON public.%I FOR INSERT WITH CHECK(public.ledger_allowed(tenant_id,book_id,true))',tab);
  END IF;
 END LOOP;
END $$;
ALTER TABLE public.connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.connections FORCE ROW LEVEL SECURITY;
CREATE POLICY connection_read ON public.connections FOR SELECT USING(
 public.ledger_context(tenant_id,book_id) AND id::text=coalesce(current_setting('vult.connection_id',true),'')
 AND actor_id::text=coalesce(current_setting('vult.actor_id',true),'') AND revoked_at IS NULL);
CREATE POLICY connection_lock ON public.connections FOR UPDATE USING(
 public.ledger_context(tenant_id,book_id) AND id::text=coalesce(current_setting('vult.connection_id',true),'')
 AND actor_id::text=coalesce(current_setting('vult.actor_id',true),'') AND revoked_at IS NULL) WITH CHECK(false);
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC;
