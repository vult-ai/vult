-- Vult Open v0.4
-- Recorded Book, browser access and owner review.

-- Account-native movements and contextual measurements are separate facts.
ALTER TABLE public.accounts
    ADD COLUMN currency text,
    ADD COLUMN native_scale integer,
    ADD COLUMN currency_definition text,
    ADD COLUMN role text,
    ADD COLUMN currency_revision bigint,
    ADD COLUMN native_origin_revision bigint,
    ADD COLUMN role_revision bigint;
ALTER TABLE public.account_revisions
    ADD COLUMN currency text,
    ADD COLUMN native_scale integer,
    ADD COLUMN currency_definition text,
    ADD COLUMN role text,
    ADD COLUMN currency_revision bigint,
    ADD COLUMN native_origin_revision bigint,
    ADD COLUMN role_revision bigint,
    ADD COLUMN receipt_id uuid,
    DROP CONSTRAINT account_revisions_code_check,
    ADD CONSTRAINT account_revisions_code_check
        CHECK(code='' OR (length(code)<=32 AND code ~ '^[A-Za-z0-9][A-Za-z0-9._/-]*$')),
    ADD CONSTRAINT account_revision_receipt_fk FOREIGN KEY(tenant_id,book_id,receipt_id)
        REFERENCES public.requests(tenant_id,book_id,id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.entries ADD COLUMN measurement_policy jsonb;
ALTER TABLE public.lines
    DROP CONSTRAINT lines_amount_minor_check,
    ADD CONSTRAINT lines_amount_minor_check CHECK(amount_minor>=0),
    ADD COLUMN native_currency text,
    ADD COLUMN native_scale integer,
    ADD COLUMN native_amount_minor bigint CHECK(native_amount_minor>=0),
    ADD COLUMN native_account_revision bigint,
    ADD COLUMN contexts jsonb CHECK(jsonb_typeof(contexts)='array' AND jsonb_array_length(contexts) BETWEEN 1 AND 16),
    ADD COLUMN valuation jsonb CHECK(jsonb_typeof(valuation)='object'),
    ADD CONSTRAINT line_native_presence CHECK(num_nonnulls(native_currency,native_scale,native_amount_minor,native_account_revision) IN(0,4)),
    ADD CONSTRAINT line_native_revision_fk FOREIGN KEY(tenant_id,book_id,account_id,native_account_revision)
        REFERENCES public.account_revisions(tenant_id,book_id,account_id,revision);

-- This catalog describes the accepted unit definition, not an exchange-rate source.
CREATE FUNCTION public.ledger_native_scale(code text, definition text) RETURNS integer
    LANGUAGE sql IMMUTABLE SET search_path=pg_catalog AS $$
    SELECT units.scale FROM (VALUES
        ('AED',2),
        ('AFN',2),
        ('ALL',2),
        ('AMD',2),
        ('AOA',2),
        ('ARS',2),
        ('AUD',2),
        ('AWG',2),
        ('AZN',2),
        ('BAM',2),
        ('BBD',2),
        ('BDT',2),
        ('BHD',3),
        ('BIF',0),
        ('BMD',2),
        ('BND',2),
        ('BOB',2),
        ('BRL',2),
        ('BSD',2),
        ('BTN',2),
        ('BWP',2),
        ('BYN',2),
        ('BZD',2),
        ('CAD',2),
        ('CDF',2),
        ('CHF',2),
        ('CLP',0),
        ('CNY',2),
        ('COP',2),
        ('CRC',2),
        ('CUP',2),
        ('CVE',2),
        ('CZK',2),
        ('DJF',0),
        ('DKK',2),
        ('DOP',2),
        ('DZD',2),
        ('EGP',2),
        ('ERN',2),
        ('ETB',2),
        ('EUR',2),
        ('FJD',2),
        ('FKP',2),
        ('GBP',2),
        ('GEL',2),
        ('GHS',2),
        ('GIP',2),
        ('GMD',2),
        ('GNF',0),
        ('GTQ',2),
        ('GYD',2),
        ('HKD',2),
        ('HNL',2),
        ('HTG',2),
        ('HUF',2),
        ('IDR',2),
        ('ILS',2),
        ('INR',2),
        ('IQD',3),
        ('IRR',2),
        ('ISK',0),
        ('JMD',2),
        ('JOD',3),
        ('JPY',0),
        ('KES',2),
        ('KGS',2),
        ('KHR',2),
        ('KMF',0),
        ('KPW',2),
        ('KRW',0),
        ('KWD',3),
        ('KYD',2),
        ('KZT',2),
        ('LAK',2),
        ('LBP',2),
        ('LKR',2),
        ('LRD',2),
        ('LSL',2),
        ('LYD',3),
        ('MAD',2),
        ('MDL',2),
        ('MGA',2),
        ('MKD',2),
        ('MMK',2),
        ('MNT',2),
        ('MOP',2),
        ('MRU',2),
        ('MUR',2),
        ('MVR',2),
        ('MWK',2),
        ('MXN',2),
        ('MYR',2),
        ('MZN',2),
        ('NAD',2),
        ('NGN',2),
        ('NIO',2),
        ('NOK',2),
        ('NPR',2),
        ('NZD',2),
        ('OMR',3),
        ('PAB',2),
        ('PEN',2),
        ('PGK',2),
        ('PHP',2),
        ('PKR',2),
        ('PLN',2),
        ('PYG',0),
        ('QAR',2),
        ('RON',2),
        ('RSD',2),
        ('RUB',2),
        ('RWF',0),
        ('SAR',2),
        ('SBD',2),
        ('SCR',2),
        ('SDG',2),
        ('SEK',2),
        ('SGD',2),
        ('SHP',2),
        ('SLE',2),
        ('SOS',2),
        ('SRD',2),
        ('SSP',2),
        ('STN',2),
        ('SVC',2),
        ('SYP',2),
        ('SZL',2),
        ('THB',2),
        ('TJS',2),
        ('TMT',2),
        ('TND',3),
        ('TOP',2),
        ('TRY',2),
        ('TTD',2),
        ('TWD',2),
        ('TZS',2),
        ('UAH',2),
        ('UGX',0),
        ('USD',2),
        ('UYU',2),
        ('UZS',2),
        ('VED',2),
        ('VES',2),
        ('VND',0),
        ('VUV',0),
        ('WST',2),
        ('XAF',0),
        ('XCD',2),
        ('XCG',2),
        ('XOF',0),
        ('XPF',0),
        ('YER',2),
        ('ZAR',2),
        ('ZMW',2),
        ('ZWG',2)
    ) AS units(code,scale) WHERE units.code=$1 AND $2='iso4217-2026-09-17'
$$;
REVOKE ALL ON FUNCTION public.ledger_native_scale(text,text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.ledger_account_revision() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE
    prior public.accounts%ROWTYPE;
    unit_changed boolean;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id,NEW.book_id);
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id',true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE='42501';
        END IF;
        NEW.recorded_at=clock_timestamp();
    END IF;
    SELECT * INTO STRICT prior FROM public.accounts
        WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND id=NEW.account_id FOR UPDATE;
    IF NEW.revision<>prior.revision+1 THEN
        RAISE EXCEPTION 'stale account revision' USING ERRCODE='23514';
    END IF;
    IF num_nonnulls(NEW.currency,NEW.native_scale,NEW.currency_definition,NEW.currency_revision) NOT IN(0,4)
        OR (NEW.currency IS NOT NULL AND (public.ledger_native_scale(NEW.currency,NEW.currency_definition)
            IS DISTINCT FROM NEW.native_scale))
        OR (NEW.role IS NOT NULL AND NEW.role !~ '^[a-z][a-z0-9_]{0,63}$')
        OR (NEW.role='receivable' AND prior.class<>'asset') THEN
        RAISE EXCEPTION 'invalid account measurement unit' USING ERRCODE='23514';
    END IF;
    unit_changed=ROW(NEW.currency,NEW.native_scale,NEW.currency_definition)
        IS DISTINCT FROM ROW(prior.currency,prior.native_scale,prior.currency_definition);
    IF (unit_changed AND NEW.currency_revision IS DISTINCT FROM NEW.revision)
        OR (NOT unit_changed AND NEW.currency_revision IS DISTINCT FROM prior.currency_revision)
        OR (NEW.native_origin_revision IS NOT NULL AND NOT
            (NEW.revision=1 AND NEW.currency IS NOT NULL AND NEW.native_origin_revision=1)
            AND NOT (NOT unit_changed AND NEW.native_origin_revision=prior.native_origin_revision))
        OR (NOT unit_changed AND NEW.native_origin_revision IS DISTINCT FROM prior.native_origin_revision
            AND NEW.revision<>1)
        OR (NEW.role IS DISTINCT FROM prior.role AND NEW.role_revision IS DISTINCT FROM NEW.revision)
        OR (NEW.role IS NOT DISTINCT FROM prior.role AND NEW.role_revision IS DISTINCT FROM prior.role_revision) THEN
        RAISE EXCEPTION 'account measurement revision is inconsistent' USING ERRCODE='23514';
    END IF;
    IF NOT public.ledger_import_owner() AND
        (NEW.currency IS NOT NULL OR NEW.role_revision IS NOT NULL) AND NEW.receipt_id IS NULL THEN
        RAISE EXCEPTION 'account measurement acceptance required' USING ERRCODE='23514';
    END IF;
    UPDATE public.accounts SET code=NEW.code,name=NEW.name,revision=NEW.revision,
        currency=NEW.currency,native_scale=NEW.native_scale,currency_definition=NEW.currency_definition,
        role=NEW.role,currency_revision=NEW.currency_revision,native_origin_revision=NEW.native_origin_revision,
        role_revision=NEW.role_revision
        WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND id=NEW.account_id;
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION public.ledger_account_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM public.accounts a WHERE a.tenant_id=NEW.tenant_id AND a.book_id=NEW.book_id AND a.id=NEW.id
        AND (a.revision<1 OR NOT EXISTS(SELECT 1 FROM public.account_revisions r
            WHERE r.tenant_id=a.tenant_id AND r.book_id=a.book_id AND r.account_id=a.id AND r.revision=a.revision
            AND ROW(r.code,r.name,r.currency,r.native_scale,r.currency_definition,r.role,r.currency_revision,r.native_origin_revision,r.role_revision)
                IS NOT DISTINCT FROM ROW(a.code,a.name,a.currency,a.native_scale,a.currency_definition,a.role,a.currency_revision,a.native_origin_revision,a.role_revision)))) THEN
        RAISE EXCEPTION 'account revision required' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END
$$;

CREATE FUNCTION public.ledger_account_measurement_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE saved jsonb; requested jsonb; operation text; prior public.account_revisions%ROWTYPE; account_class text;
BEGIN
    IF NEW.receipt_id IS NULL THEN
        IF num_nonnulls(NEW.currency,NEW.role_revision,NEW.native_origin_revision)>0 THEN
            RAISE EXCEPTION 'account measurement receipt required' USING ERRCODE='23514';
        END IF;
        RETURN NULL;
    END IF;
    SELECT r.operation,convert_from(r.result_bytes,'UTF8')::jsonb,convert_from(r.request_bytes,'UTF8')::jsonb
        INTO STRICT operation,saved,requested FROM public.requests r
        WHERE r.tenant_id=NEW.tenant_id AND r.book_id=NEW.book_id AND r.id=NEW.receipt_id AND r.actor_id=NEW.actor_id;
    SELECT class INTO STRICT account_class FROM public.accounts
        WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND id=NEW.account_id;
    IF NEW.revision>1 THEN
        SELECT * INTO STRICT prior FROM public.account_revisions
            WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND account_id=NEW.account_id AND revision=NEW.revision-1;
    END IF;
    IF operation<>(CASE WHEN NEW.revision=1 THEN 'create_account' ELSE 'revise_account' END)
        OR saved->>'class' IS DISTINCT FROM account_class
        OR saved->>'id' IS DISTINCT FROM NEW.account_id::text
        OR (saved->>'revision')::bigint IS DISTINCT FROM NEW.revision
        OR saved->>'code' IS DISTINCT FROM NEW.code OR saved->>'name' IS DISTINCT FROM NEW.name
        OR saved->>'currency' IS DISTINCT FROM NEW.currency
        OR (saved->>'scale')::integer IS DISTINCT FROM NEW.native_scale
        OR saved->>'currency_definition' IS DISTINCT FROM NEW.currency_definition
        OR saved->>'role' IS DISTINCT FROM NEW.role
        OR (saved->>'currency_revision')::bigint IS DISTINCT FROM NEW.currency_revision
        OR (saved->>'native_origin_revision')::bigint IS DISTINCT FROM NEW.native_origin_revision
        OR (saved->>'role_revision')::bigint IS DISTINCT FROM NEW.role_revision
        OR requested->>'code' IS DISTINCT FROM NEW.code OR requested->>'name' IS DISTINCT FROM NEW.name
        OR ((requested ? 'currency') IS DISTINCT FROM (requested ? 'scale'))
        OR (NOT requested ? 'currency' AND ROW(NEW.currency,NEW.native_scale,NEW.currency_definition)
            IS DISTINCT FROM ROW(prior.currency,prior.native_scale,prior.currency_definition))
        OR (NOT requested ? 'role' AND NEW.role IS DISTINCT FROM prior.role)
        OR (NEW.revision>1 AND (requested->>'account_id' IS DISTINCT FROM NEW.account_id::text
            OR (requested->>'expected_revision')::bigint IS DISTINCT FROM NEW.revision-1))
        OR (NEW.revision=1 AND requested->>'class' IS DISTINCT FROM account_class)
        OR (requested ? 'currency' AND requested->>'currency' IS DISTINCT FROM NEW.currency)
        OR (requested ? 'scale' AND (requested->>'scale')::integer IS DISTINCT FROM NEW.native_scale)
        OR (requested ? 'role' AND nullif(requested->>'role','') IS DISTINCT FROM NEW.role)
        OR (NEW.revision=1 AND coalesce(requested->>'native_origin'='empty_ledger',false) IS DISTINCT FROM coalesce(NEW.native_origin_revision=1,false)) THEN
        RAISE EXCEPTION 'account measurement receipt differs from revision' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END
$$;
CREATE CONSTRAINT TRIGGER account_measurement_receipt AFTER INSERT ON public.account_revisions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.ledger_account_measurement_receipt();
REVOKE ALL ON FUNCTION public.ledger_account_measurement_receipt() FROM PUBLIC;

-- A missing observation is never aggregated as a known zero movement.
CREATE FUNCTION public.ledger_native_position(t uuid,b uuid,aid uuid,through_date text,before_sequence bigint,unit_revision bigint DEFAULT 0)
    RETURNS TABLE(status text,currency text,native_scale integer,native_minor numeric,book_minor numeric,
        last_sequence bigint,role text,class text,currency_revision bigint,role_revision bigint)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $$
BEGIN
    IF (public.ledger_context(t,b) AND (public.ledger_import_owner() OR public.ledger_allowed(t,b,false))) IS NOT TRUE THEN
        RAISE EXCEPTION 'selected Book access required' USING ERRCODE='42501';
    END IF;
    RETURN QUERY WITH selected AS (
        SELECT a.id,a.class,a.role,a.role_revision,r.currency,r.native_scale,r.currency_revision,r.native_origin_revision
        FROM public.accounts a JOIN public.account_revisions r ON r.tenant_id=a.tenant_id AND r.book_id=a.book_id
          AND r.account_id=a.id AND r.revision=CASE WHEN unit_revision=0 THEN a.revision ELSE unit_revision END
        WHERE a.tenant_id=t AND a.book_id=b AND a.id=aid
    ) SELECT CASE WHEN a.currency IS NULL THEN 'not_recorded'
        WHEN a.native_origin_revision IS NULL OR coalesce(bool_and(
            l.native_currency=a.currency AND l.native_scale=a.native_scale AND r.currency_revision=a.currency_revision
            AND l.native_amount_minor IS NOT NULL) FILTER(WHERE l.id IS NOT NULL),true) IS NOT TRUE THEN 'incomplete'
        ELSE 'complete' END,
        a.currency,a.native_scale,
        CASE WHEN a.native_origin_revision IS NOT NULL AND coalesce(bool_and(
            l.native_currency=a.currency AND l.native_scale=a.native_scale AND r.currency_revision=a.currency_revision
            AND l.native_amount_minor IS NOT NULL) FILTER(WHERE l.id IS NOT NULL),true) IS TRUE
            THEN coalesce(sum(CASE l.side WHEN 'debit' THEN l.native_amount_minor::numeric ELSE -l.native_amount_minor::numeric END),0) END,
        coalesce(sum(CASE l.side WHEN 'debit' THEN l.amount_minor::numeric ELSE -l.amount_minor::numeric END),0),
        coalesce(max(e.sequence),0),a.role,a.class,a.currency_revision,a.role_revision
    FROM selected a
    LEFT JOIN (public.lines l JOIN public.entries e ON e.tenant_id=l.tenant_id AND e.book_id=l.book_id AND e.id=l.entry_id
        AND (before_sequence=0 OR e.sequence<before_sequence)
        AND (through_date='' OR e.accounting_date<=nullif(through_date,'')::date))
        ON l.tenant_id=t AND l.book_id=b AND l.account_id=a.id
    LEFT JOIN public.account_revisions r ON r.tenant_id=l.tenant_id AND r.book_id=l.book_id
        AND r.account_id=l.account_id AND r.revision=l.native_account_revision
    GROUP BY a.id,a.currency,a.native_scale,a.native_origin_revision,a.currency_revision,a.role,a.class,a.role_revision;
END
$$;
REVOKE ALL ON FUNCTION public.ledger_native_position(uuid,uuid,uuid,text,bigint,bigint) FROM PUBLIC;

CREATE FUNCTION public.ledger_round_half_even(n numeric,d numeric) RETURNS numeric
    LANGUAGE plpgsql IMMUTABLE SET search_path=pg_catalog AS $$
DECLARE q numeric; remainder numeric;
BEGIN
    IF n<0 OR d<=0 THEN RAISE EXCEPTION 'invalid rounding inputs' USING ERRCODE='23514'; END IF;
    q=div(n,d); remainder=mod(n,d);
    IF remainder*2>d OR (remainder*2=d AND mod(q,2)=1) THEN q=q+1; END IF;
    RETURN q;
END
$$;
REVOKE ALL ON FUNCTION public.ledger_round_half_even(numeric,numeric) FROM PUBLIC;

CREATE FUNCTION public.ledger_measurement_shape(v jsonb,required_keys text[],types jsonb) RETURNS boolean
    LANGUAGE sql IMMUTABLE SET search_path=pg_catalog AS $$
    SELECT coalesce(jsonb_typeof(v)='object' AND v ?& required_keys AND NOT EXISTS(
        SELECT 1 FROM jsonb_each(v) item WHERE NOT types ? item.key OR jsonb_typeof(item.value) IS DISTINCT FROM types->>item.key),false)
$$;
REVOKE ALL ON FUNCTION public.ledger_measurement_shape(jsonb,text[],jsonb) FROM PUBLIC;

CREATE FUNCTION public.ledger_money_valid(v jsonb,bc text,bs integer,denomination boolean,signed_amount boolean)
    RETURNS boolean LANGUAGE plpgsql IMMUTABLE SET search_path=pg_catalog AS $$
DECLARE value numeric;
BEGIN
    IF NOT public.ledger_measurement_shape(v,ARRAY['currency','scale','amount_minor','unit_definition'],
        '{"currency":"string","scale":"number","amount_minor":"string","unit_definition":"string"}'::jsonb)
        OR coalesce(v->>'scale','') !~ '^[0-4]$'
        OR v->>'amount_minor' IS NULL OR v->>'amount_minor' !~ (CASE WHEN signed_amount THEN '^(0|-?[1-9][0-9]{0,18})$' ELSE '^(0|[1-9][0-9]{0,18})$' END)
        OR EXISTS(SELECT 1 FROM jsonb_object_keys(v) k WHERE k NOT IN('currency','scale','amount_minor','unit_definition')) THEN
        RETURN false;
    END IF;
    value=(v->>'amount_minor')::numeric;
    IF value NOT BETWEEN -9223372036854775808::numeric AND 9223372036854775807::numeric THEN RETURN false; END IF;
    IF denomination THEN
        RETURN coalesce(v->>'unit_definition'='iso4217-2026-09-17'
            AND public.ledger_native_scale(v->>'currency',v->>'unit_definition')=(v->>'scale')::integer,false);
    END IF;
    RETURN coalesce(v->>'unit_definition'='book' AND v->>'currency'=bc AND (v->>'scale')::integer=bs,false);
END
$$;
REVOKE ALL ON FUNCTION public.ledger_money_valid(jsonb,text,integer,boolean,boolean) FROM PUBLIC;

CREATE FUNCTION public.ledger_measurements_final() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
DECLARE
    e public.entries%ROWTYPE; b public.books%ROWTYPE; l public.lines%ROWTYPE;
    a public.accounts%ROWTYPE; revision public.account_revisions%ROWTYPE; original public.lines%ROWTYPE;
    saved jsonb; requested jsonb; input jsonb; quote jsonb; value jsonb; c jsonb;
    group_value jsonb; part jsonb; member integer; index_value integer; native_count integer;
    n numeric; d numeric; rate_digits text; rate_places integer; expected numeric;
    native_total numeric; book_total numeric; reference_unit text; seen integer[];
    weights numeric; floor_total numeric; residual numeric; rank_value integer;
    state record; normalized jsonb; supplied jsonb; policy jsonb;
BEGIN
    e=NEW;
    IF (public.ledger_context(e.tenant_id,e.book_id) AND
        (public.ledger_import_owner() OR public.ledger_allowed(e.tenant_id,e.book_id,true))) IS NOT TRUE THEN
        RAISE EXCEPTION 'selected Book write access required' USING ERRCODE='42501';
    END IF;
    SELECT * INTO STRICT b FROM public.books WHERE tenant_id=e.tenant_id AND id=e.book_id;
    SELECT convert_from(result_bytes,'UTF8')::jsonb,convert_from(request_bytes,'UTF8')::jsonb
        INTO STRICT saved,requested FROM public.requests WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND id=e.receipt_id;
    IF e.measurement_policy IS NULL AND NOT EXISTS(SELECT 1 FROM public.lines
        WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id
        AND num_nonnulls(native_currency,contexts,valuation)>0) THEN
        IF requested ? 'measurement_policy' OR EXISTS(SELECT 1 FROM jsonb_array_elements(requested->'lines') item
            WHERE item ?| ARRAY['native','contexts','valuation']) THEN
            RAISE EXCEPTION 'accepted measurements were omitted' USING ERRCODE='23514';
        END IF;
        IF (public.ledger_import_owner() AND current_setting('vult.restore_book',true)=e.tenant_id::text||'/'||e.book_id::text)
            OR e.reversal_of IS NOT NULL THEN
            RETURN NULL;
        END IF;
        RAISE EXCEPTION 'new postings require a valuation basis' USING ERRCODE='23514';
    END IF;
    IF saved->'measurement_policy' IS DISTINCT FROM e.measurement_policy THEN
        RAISE EXCEPTION 'measurement policy differs from receipt' USING ERRCODE='23514';
    END IF;
    IF e.reversal_of IS NOT NULL THEN
        IF e.measurement_policy IS DISTINCT FROM (SELECT measurement_policy FROM public.entries
            WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND id=e.reversal_of) THEN
            RAISE EXCEPTION 'reversal must retain measurement policy' USING ERRCODE='23514';
        END IF;
        FOR l IN SELECT * FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id LOOP
            SELECT * INTO STRICT original FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND id=l.reverses_line_id;
            IF ROW(l.native_currency,l.native_scale,l.native_amount_minor,l.native_account_revision,l.contexts,l.valuation)
                IS DISTINCT FROM ROW(original.native_currency,original.native_scale,original.native_amount_minor,original.native_account_revision,original.contexts,original.valuation) THEN
                RAISE EXCEPTION 'reversal must mirror original measurements' USING ERRCODE='23514';
            END IF;
        END LOOP;
        RETURN NULL;
    END IF;
    FOR l IN SELECT * FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id LOOP
        input=requested->'lines'->(l.position-1);
        IF input IS NULL THEN RAISE EXCEPTION 'posting input required' USING ERRCODE='23514'; END IF;
        IF l.native_currency IS NOT NULL THEN
            SELECT r.* INTO STRICT revision FROM public.account_revisions r WHERE r.tenant_id=l.tenant_id AND r.book_id=l.book_id
                AND r.account_id=l.account_id AND r.revision=l.native_account_revision;
            SELECT * INTO STRICT a FROM public.accounts WHERE tenant_id=l.tenant_id AND book_id=l.book_id AND id=l.account_id;
            IF l.native_currency IS DISTINCT FROM revision.currency OR l.native_scale IS DISTINCT FROM revision.native_scale
                OR revision.recorded_at>e.recorded_at
                OR (NOT public.ledger_import_owner() AND revision.currency_revision IS DISTINCT FROM a.currency_revision)
                OR input->'native'->>'currency' IS DISTINCT FROM l.native_currency
                OR (input->'native'->>'scale')::integer IS DISTINCT FROM l.native_scale
                OR input->'native'->>'amount_minor' IS DISTINCT FROM l.native_amount_minor::text
                OR (input->'native' ? 'account_revision' AND (input->'native'->>'account_revision')::bigint IS DISTINCT FROM l.native_account_revision)
                OR (input->'native' ? 'unit_definition' AND input->'native'->>'unit_definition' IS DISTINCT FROM revision.currency_definition) THEN
                RAISE EXCEPTION 'native measurement differs from accepted account unit' USING ERRCODE='23514';
            END IF;
        ELSIF input ? 'native' THEN
            RAISE EXCEPTION 'native movement was omitted' USING ERRCODE='23514';
        END IF;
        IF l.contexts IS NOT NULL THEN
            normalized='[]'::jsonb;
            FOR value IN SELECT item FROM jsonb_array_elements(l.contexts) item LOOP
                IF NOT public.ledger_measurement_shape(value,ARRAY['role','amount'],'{"role":"string","amount":"object","source":"string"}'::jsonb)
                    OR coalesce(value->>'role','') !~ '^[a-z][a-z0-9_]{0,63}$'
                    OR NOT public.ledger_money_valid(value->'amount',b.currency,b.scale,true,true)
                    OR length(coalesce(value->>'source',''))>512 THEN
                    RAISE EXCEPTION 'invalid contextual denomination' USING ERRCODE='23514';
                END IF;
            END LOOP;
            FOR value IN SELECT item FROM jsonb_array_elements(input->'contexts') item LOOP
                normalized=normalized||jsonb_build_array(jsonb_set(value,'{amount,unit_definition}',
                    coalesce(value->'amount'->'unit_definition','"iso4217-2026-09-17"'::jsonb)));
            END LOOP;
            IF normalized IS DISTINCT FROM l.contexts THEN
                RAISE EXCEPTION 'context differs from accepted input' USING ERRCODE='23514';
            END IF;
        ELSIF input ? 'contexts' THEN
            RAISE EXCEPTION 'context was omitted' USING ERRCODE='23514';
        END IF;
        value=l.valuation;
        IF value IS NULL THEN
            IF num_nonnulls(l.native_currency,l.contexts,e.measurement_policy)>0 OR input ? 'valuation'
                OR NOT (public.ledger_import_owner() AND current_setting('vult.restore_book',true)=e.tenant_id::text||'/'||e.book_id::text) THEN
                RAISE EXCEPTION 'valuation basis required' USING ERRCODE='23514';
            END IF;
            CONTINUE;
        END IF;
        IF NOT public.ledger_measurement_shape(value,ARRAY['basis','policy'],
            '{"basis":"string","policy":"string","input":"object","rate":"object","source":"string","allocation_group":"number","carrying":"object"}'::jsonb)
            OR length(coalesce(value->>'source',''))>512 THEN
            RAISE EXCEPTION 'invalid valuation source' USING ERRCODE='23514';
        END IF;
        supplied=input->'valuation';
        IF supplied IS NULL THEN
            supplied=jsonb_build_object('basis','manual','policy','supplied-book-value-v1','input',
                jsonb_build_object('currency',b.currency,'scale',b.scale,'amount_minor',l.amount_minor::text,'unit_definition','book'));
        ELSE
            IF supplied ? 'input' THEN
                supplied=jsonb_set(supplied,'{input,unit_definition}',coalesce(supplied->'input'->'unit_definition',
                    to_jsonb(CASE WHEN supplied->>'basis'='applied_quote' THEN 'iso4217-2026-09-17' ELSE 'book' END)));
            END IF;
            IF supplied ? 'carrying' THEN
                FOREACH rate_digits IN ARRAY ARRAY['before_native','consumed_native','before_book','consumed_book'] LOOP
                    supplied=jsonb_set(supplied,ARRAY['carrying',rate_digits,'unit_definition'],
                        coalesce(supplied->'carrying'->rate_digits->'unit_definition',
                            to_jsonb(CASE WHEN rate_digits IN('before_native','consumed_native') THEN 'iso4217-2026-09-17' ELSE 'book' END)));
                END LOOP;
            END IF;
        END IF;
        IF supplied IS DISTINCT FROM value THEN
            RAISE EXCEPTION 'valuation differs from accepted input' USING ERRCODE='23514';
        END IF;
        IF value->>'basis' IN('manual','actual_source') THEN
            IF value->>'policy' IS DISTINCT FROM (CASE WHEN value->>'basis'='manual' THEN 'supplied-book-value-v1' ELSE 'actual-source-book-value-v1' END)
                OR NOT public.ledger_money_valid(value->'input',b.currency,b.scale,false,false)
                OR value->'input'->>'amount_minor' IS DISTINCT FROM l.amount_minor::text
                OR value ?| ARRAY['rate','carrying','allocation_group'] THEN
                RAISE EXCEPTION 'invalid recorded Book valuation' USING ERRCODE='23514';
            END IF;
        ELSIF value->>'basis'='applied_quote' THEN
            quote=value->'rate'; input=value->'input';
            IF NOT public.ledger_measurement_shape(quote,ARRAY['base','quote','rate','requested_date','observation_date','provenance'],
                '{"base":"string","quote":"string","rate":"string","requested_date":"string","observation_date":"string","provenance":"string"}'::jsonb)
                OR value->>'policy' IS DISTINCT FROM 'quoted-rate-half-even-v1'
                OR NOT public.ledger_money_valid(input,b.currency,b.scale,true,false)
                OR quote->>'base' IS DISTINCT FROM input->>'currency' OR quote->>'quote' IS DISTINCT FROM b.currency
                OR coalesce(quote->>'rate','') !~ '^(0|[1-9][0-9]{0,63})(\.[0-9]{1,64})?$'
                OR (quote->>'rate')::numeric<=0 OR length(coalesce(quote->>'provenance','')) NOT BETWEEN 1 AND 512
                OR coalesce(quote->>'requested_date','') !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
                OR coalesce(quote->>'observation_date','') !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
                OR (quote->>'observation_date')::date>(quote->>'requested_date')::date
                OR value ?| ARRAY['carrying','allocation_group'] THEN
                RAISE EXCEPTION 'invalid applied quote' USING ERRCODE='23514';
            END IF;
            rate_digits=replace(quote->>'rate','.','');
            rate_places=length(split_part(quote->>'rate','.',2));
            n=(input->>'amount_minor')::numeric*rate_digits::numeric*power(10::numeric,b.scale);
            d=power(10::numeric,rate_places+(input->>'scale')::integer);
            IF public.ledger_round_half_even(n,d)<>l.amount_minor THEN
                RAISE EXCEPTION 'quote does not reproduce Book amount' USING ERRCODE='23514';
            END IF;
        ELSIF value->>'basis'='historical_carrying' THEN
            c=value->'carrying';
            IF NOT public.ledger_measurement_shape(c,ARRAY['policy','account_id','account_revision','role_revision','before_sequence','before_native','before_book','consumed_native','consumed_book'],
                '{"policy":"string","account_id":"string","account_revision":"number","role_revision":"number","before_sequence":"number","before_native":"object","before_book":"object","consumed_native":"object","consumed_book":"object"}'::jsonb)
                OR value->>'policy' IS DISTINCT FROM 'moving-average-cash-v1' OR c->>'policy' IS DISTINCT FROM 'moving-average-cash-v1'
                OR l.native_currency IS NULL OR value ?| ARRAY['input','rate','allocation_group']
                OR (SELECT count(*) FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id AND account_id=l.account_id)<>1
                OR c->>'account_id' IS DISTINCT FROM l.account_id::text THEN
                RAISE EXCEPTION 'invalid carrying allocation scope' USING ERRCODE='23514';
            END IF;
            SELECT * INTO STRICT state FROM public.ledger_native_position(e.tenant_id,e.book_id,l.account_id,'',e.sequence,l.native_account_revision);
            SELECT r.* INTO STRICT revision FROM public.account_revisions r WHERE r.tenant_id=l.tenant_id AND r.book_id=l.book_id
                AND r.account_id=l.account_id AND r.revision=(c->>'role_revision')::bigint;
            IF state.status<>'complete' OR state.class<>'asset' OR revision.role IS DISTINCT FROM 'cash'
                OR revision.role_revision IS DISTINCT FROM (c->>'role_revision')::bigint OR revision.recorded_at>e.recorded_at
                OR (NOT public.ledger_import_owner() AND (revision.role IS DISTINCT FROM state.role OR revision.role_revision IS DISTINCT FROM state.role_revision))
                OR (c->>'account_revision')::bigint IS DISTINCT FROM l.native_account_revision
                OR (c->>'before_sequence')::bigint IS DISTINCT FROM state.last_sequence
                OR NOT public.ledger_money_valid(c->'before_native',b.currency,b.scale,true,true)
                OR NOT public.ledger_money_valid(c->'consumed_native',b.currency,b.scale,true,false)
                OR NOT public.ledger_money_valid(c->'before_book',b.currency,b.scale,false,true)
                OR NOT public.ledger_money_valid(c->'consumed_book',b.currency,b.scale,false,false)
                OR c->'before_native'->>'currency' IS DISTINCT FROM l.native_currency
                OR (c->'before_native'->>'scale')::integer IS DISTINCT FROM l.native_scale
                OR c->'consumed_native'->>'currency' IS DISTINCT FROM l.native_currency
                OR (c->'consumed_native'->>'scale')::integer IS DISTINCT FROM l.native_scale
                OR (c->'before_native'->>'amount_minor')::numeric IS DISTINCT FROM state.native_minor
                OR (c->'before_book'->>'amount_minor')::numeric IS DISTINCT FROM state.book_minor
                OR c->'consumed_native'->>'amount_minor' IS DISTINCT FROM l.native_amount_minor::text
                OR c->'consumed_book'->>'amount_minor' IS DISTINCT FROM l.amount_minor::text THEN
                RAISE EXCEPTION 'carrying allocation inputs differ from accepted state' USING ERRCODE='23514';
            END IF;
            n=abs(state.native_minor); d=abs(state.book_minor);
            IF n=0 OR l.native_amount_minor<=0 OR l.native_amount_minor>n
                OR (state.book_minor<>0 AND sign(state.native_minor)<>sign(state.book_minor))
                OR (state.native_minor>0 AND l.side<>'credit') OR (state.native_minor<0 AND l.side<>'debit') THEN
                RAISE EXCEPTION 'invalid carrying consumption' USING ERRCODE='23514';
            END IF;
            expected=CASE WHEN l.native_amount_minor=n THEN d ELSE public.ledger_round_half_even(d*l.native_amount_minor,n) END;
            IF expected<>l.amount_minor THEN RAISE EXCEPTION 'carrying amount differs from allocation' USING ERRCODE='23514'; END IF;
        ELSIF value->>'basis'='authoritative_allocation' THEN
            IF value->>'policy' IS DISTINCT FROM 'largest-remainder-v1' OR jsonb_typeof(value->'allocation_group') IS DISTINCT FROM 'number'
                OR (value->>'allocation_group')::integer NOT BETWEEN 1 AND coalesce(jsonb_array_length(e.measurement_policy->'allocations'),0)
                OR value ?| ARRAY['input','rate','carrying'] THEN
                RAISE EXCEPTION 'invalid authoritative allocation' USING ERRCODE='23514';
            END IF;
        ELSE
            RAISE EXCEPTION 'unsupported valuation basis' USING ERRCODE='23514';
        END IF;
    END LOOP;
    policy=e.measurement_policy;
    IF policy IS NOT NULL THEN
        IF NOT public.ledger_measurement_shape(policy,ARRAY['version'],'{"version":"number","native_transfers":"array","allocations":"array"}'::jsonb)
            OR policy->>'version' IS DISTINCT FROM '1'
            OR coalesce(jsonb_array_length(policy->'native_transfers'),0)>128
            OR coalesce(jsonb_array_length(policy->'allocations'),0)>128 THEN
            RAISE EXCEPTION 'invalid measurement policy' USING ERRCODE='23514';
        END IF;
        normalized=requested->'measurement_policy';
        IF normalized ? 'allocations' THEN
            group_value='[]'::jsonb;
            FOR value IN SELECT item FROM jsonb_array_elements(normalized->'allocations') item LOOP
                group_value=group_value||jsonb_build_array(jsonb_set(value,'{total,unit_definition}',
                    coalesce(value->'total'->'unit_definition','"book"'::jsonb)));
            END LOOP;
            normalized=jsonb_set(normalized,'{allocations}',group_value);
        END IF;
        IF normalized IS DISTINCT FROM policy THEN
            RAISE EXCEPTION 'measurement policy differs from accepted input' USING ERRCODE='23514';
        END IF;
        FOR group_value IN SELECT item FROM jsonb_array_elements(policy->'native_transfers') item LOOP
            native_total=0; book_total=0; reference_unit=NULL; seen='{}'; native_count=0;
            IF NOT public.ledger_measurement_shape(group_value,ARRAY['lines'],'{"lines":"array"}'::jsonb)
                OR jsonb_array_length(group_value->'lines') NOT BETWEEN 2 AND 256 THEN
                RAISE EXCEPTION 'invalid native transfer' USING ERRCODE='23514';
            END IF;
            FOR part IN SELECT item FROM jsonb_array_elements(group_value->'lines') item LOOP
                member=part::text::integer;
                SELECT * INTO STRICT l FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id AND position=member;
                IF l.native_currency IS NULL OR member=ANY(seen) OR
                    (reference_unit IS NOT NULL AND reference_unit<>l.native_currency||'/'||l.native_scale::text) THEN
                    RAISE EXCEPTION 'invalid native transfer members' USING ERRCODE='23514';
                END IF;
                seen=array_append(seen,member); reference_unit=l.native_currency||'/'||l.native_scale::text;
                native_total=native_total+CASE l.side WHEN 'debit' THEN l.native_amount_minor::numeric ELSE -l.native_amount_minor::numeric END;
                book_total=book_total+CASE l.side WHEN 'debit' THEN l.amount_minor::numeric ELSE -l.amount_minor::numeric END;
                IF l.native_amount_minor>0 THEN native_count=native_count+1; END IF;
            END LOOP;
            IF native_count=0 OR native_total<>0 OR book_total<>0 THEN
                RAISE EXCEPTION 'fee-free transfer must preserve native and Book totals' USING ERRCODE='23514';
            END IF;
        END LOOP;
        seen='{}'; index_value=0;
        FOR group_value IN SELECT item FROM jsonb_array_elements(policy->'allocations') item LOOP
            index_value=index_value+1;
            IF NOT public.ledger_measurement_shape(group_value,ARRAY['total','method','parts'],'{"total":"object","method":"string","parts":"array"}'::jsonb)
                OR group_value->>'method' IS DISTINCT FROM 'largest-remainder-v1'
                OR NOT public.ledger_money_valid(group_value->'total',b.currency,b.scale,false,false)
                OR jsonb_array_length(group_value->'parts') NOT BETWEEN 1 AND 256 THEN
                RAISE EXCEPTION 'invalid allocation total' USING ERRCODE='23514';
            END IF;
            weights=0; reference_unit=NULL;
            FOR part IN SELECT item FROM jsonb_array_elements(group_value->'parts') item LOOP
                IF NOT public.ledger_measurement_shape(part,ARRAY['line','weight'],'{"line":"number","weight":"string"}'::jsonb)
                    OR coalesce(part->>'weight','') !~ '^[1-9][0-9]{0,18}$' OR (part->>'weight')::numeric>9223372036854775807::numeric
                    OR (part->>'line')::integer=ANY(seen) THEN
                    RAISE EXCEPTION 'invalid allocation member' USING ERRCODE='23514';
                END IF;
                member=(part->>'line')::integer; seen=array_append(seen,member);
                SELECT * INTO STRICT l FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id AND position=member;
                IF (reference_unit IS NOT NULL AND reference_unit<>l.side) OR
                    (l.valuation->>'allocation_group')::integer IS DISTINCT FROM index_value THEN
                    RAISE EXCEPTION 'allocation must have one side and matching members' USING ERRCODE='23514';
                END IF;
                reference_unit=l.side; weights=weights+(part->>'weight')::numeric;
            END LOOP;
            n=(group_value->'total'->>'amount_minor')::numeric;
            SELECT sum(div(n*(item->>'weight')::numeric,weights)) INTO floor_total FROM jsonb_array_elements(group_value->'parts') item;
            residual=n-floor_total; rank_value=0;
            FOR part IN SELECT item FROM jsonb_array_elements(group_value->'parts') item
                ORDER BY mod(n*(item->>'weight')::numeric,weights) DESC,(item->>'line')::integer LOOP
                rank_value=rank_value+1;
                expected=div(n*(part->>'weight')::numeric,weights)+CASE WHEN rank_value<=residual THEN 1 ELSE 0 END;
                IF NOT EXISTS(SELECT 1 FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id
                    AND position=(part->>'line')::integer AND amount_minor=expected) THEN
                    RAISE EXCEPTION 'allocation does not preserve authoritative total' USING ERRCODE='23514';
                END IF;
            END LOOP;
        END LOOP;
        IF EXISTS(SELECT 1 FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id
            AND valuation ? 'allocation_group' AND NOT position=ANY(seen)) THEN
            RAISE EXCEPTION 'allocation line is not assigned' USING ERRCODE='23514';
        END IF;
    ELSIF EXISTS(SELECT 1 FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id
        AND valuation ? 'allocation_group') THEN
        RAISE EXCEPTION 'allocation policy required' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END
$$;
CREATE CONSTRAINT TRIGGER measurements_final AFTER INSERT ON public.entries
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.ledger_measurements_final();
REVOKE ALL ON FUNCTION public.ledger_measurements_final() FROM PUBLIC;
CREATE OR REPLACE FUNCTION public.ledger_line_receipt() RETURNS trigger
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
        OR saved->>'reverses_line_id' IS DISTINCT FROM NEW.reverses_line_id::text
        OR saved->'contexts' IS DISTINCT FROM NEW.contexts
        OR saved->'valuation' IS DISTINCT FROM NEW.valuation
        OR (CASE WHEN NEW.native_currency IS NULL THEN saved ? 'native' ELSE
            saved->'native' IS DISTINCT FROM jsonb_build_object('currency',NEW.native_currency,'scale',NEW.native_scale,
                'amount_minor',NEW.native_amount_minor::text,'account_revision',NEW.native_account_revision,
                'unit_definition',(SELECT currency_definition FROM public.account_revisions
                  WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND account_id=NEW.account_id AND revision=NEW.native_account_revision)) END) THEN
        RAISE EXCEPTION 'journal receipt differs from the recorded line' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END
$$;

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
 IF count_lines<2 OR count_lines>256 OR debits<>credits OR (debits=0 AND NOT EXISTS(SELECT 1 FROM public.lines WHERE tenant_id=e.tenant_id AND book_id=e.book_id AND entry_id=e.id AND native_amount_minor>0))
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

CREATE FUNCTION public.ledger_check_native_history(t uuid,b uuid,eid uuid) RETURNS void
    LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM (
        SELECT sum(change) OVER(PARTITION BY account_id ORDER BY sequence) AS position,
            sum(unknown_count) OVER(PARTITION BY account_id ORDER BY sequence) AS unknown_count
        FROM (
            SELECT l.account_id,e.sequence,
                sum(CASE l.side WHEN 'debit' THEN l.native_amount_minor::numeric ELSE -l.native_amount_minor::numeric END) AS change,
                count(*) FILTER(WHERE l.native_currency IS DISTINCT FROM origin.currency OR l.native_scale IS DISTINCT FROM origin.native_scale
                    OR r.currency_revision IS DISTINCT FROM origin.currency_revision OR l.native_amount_minor IS NULL) AS unknown_count
            FROM public.lines l JOIN public.entries e ON e.tenant_id=l.tenant_id AND e.book_id=l.book_id AND e.id=l.entry_id
            JOIN public.account_revisions origin ON origin.tenant_id=l.tenant_id AND origin.book_id=l.book_id
                AND origin.account_id=l.account_id AND origin.revision=1 AND origin.native_origin_revision=1
            LEFT JOIN public.account_revisions r ON r.tenant_id=l.tenant_id AND r.book_id=l.book_id
                AND r.account_id=l.account_id AND r.revision=l.native_account_revision
            WHERE l.tenant_id=t AND l.book_id=b
                AND (eid IS NULL OR l.account_id IN(SELECT account_id FROM public.lines WHERE tenant_id=t AND book_id=b AND entry_id=eid))
            GROUP BY l.account_id,e.sequence
        ) changes
    ) positions WHERE unknown_count=0 AND position NOT BETWEEN -9223372036854775808::numeric AND 9223372036854775807::numeric) THEN
        RAISE EXCEPTION 'native balance is outside the supported integer range' USING ERRCODE='22003';
    END IF;
END
$$;
REVOKE ALL ON FUNCTION public.ledger_check_native_history(uuid,uuid,uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.ledger_validate_balances(t uuid,b uuid,eid uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
BEGIN
    IF (public.ledger_context(t,b) AND (public.ledger_import_owner() OR public.ledger_allowed(t,b,true))) IS NOT TRUE THEN
        RAISE EXCEPTION 'selected Book write access required' USING ERRCODE='42501';
    END IF;
    IF public.ledger_import_owner() AND current_setting('vult.restore_book',true)=t::text||'/'||b::text THEN RETURN; END IF;
    PERFORM public.ledger_check_balance_history(t,b,eid);
    PERFORM public.ledger_check_native_history(t,b,eid);
END
$$;

CREATE OR REPLACE FUNCTION public.ledger_validate_restored_book(t uuid,b uuid) RETURNS void
    LANGUAGE plpgsql SET search_path=pg_catalog,pg_temp AS $$
BEGIN
    IF (public.ledger_import_owner() AND public.ledger_context(t,b)) IS NOT TRUE THEN
        RAISE EXCEPTION 'selected Book operator access required' USING ERRCODE='42501';
    END IF;
    PERFORM public.ledger_check_balance_history(t,b,NULL);
    PERFORM public.ledger_check_native_history(t,b,NULL);
    IF (SELECT logo FROM public.book_revisions WHERE tenant_id=t AND book_id=b ORDER BY revision DESC LIMIT 1)
        IS DISTINCT FROM (SELECT jsonb_build_object('id',id,'sha256',sha256,'content_type',content_type,'size',octet_length(data))
            FROM public.book_logo_images WHERE tenant_id=t AND book_id=b) THEN
        RAISE EXCEPTION 'Book logo differs from retained history' USING ERRCODE='23514';
    END IF;
END
$$;

-- Each row owns one accepted settings revision and its retained request outcome.
-- The Book's original header, financial unit and posted records remain unchanged.
CREATE OR REPLACE FUNCTION public.ledger_import_owner() RETURNS boolean
    LANGUAGE sql STABLE SET search_path=pg_catalog,pg_temp AS $$
SELECT coalesce((SELECT rolsuper FROM pg_catalog.pg_roles WHERE rolname=session_user),false)
$$;
ALTER FUNCTION public.ledger_connection_guard() SET search_path=pg_catalog,pg_temp;

CREATE TABLE public.book_revisions (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    revision bigint NOT NULL CHECK(revision>0),
    actor_id uuid NOT NULL,
    request_key text NOT NULL CHECK(octet_length(request_key) BETWEEN 1 AND 128),
    prior_name text NOT NULL CHECK(length(prior_name) BETWEEN 1 AND 120),
    name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 120),
    founder text CHECK(length(founder)<=120),
    profile text CHECK(profile IN('personal','business')),
    start_date date CHECK(start_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
    fiscal_year_start_month integer,
    fiscal_year_start_day integer,
    accounting_timezone text CHECK(length(accounting_timezone) BETWEEN 1 AND 100 AND accounting_timezone<>'Local'),
    request_bytes bytea NOT NULL CHECK(octet_length(request_bytes) BETWEEN 2 AND 16384),
    result_bytes bytea NOT NULL CHECK(octet_length(result_bytes) BETWEEN 2 AND 32768),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    logo jsonb,
    logo_actor text CHECK(logo_actor IN('owner','agent')),
    PRIMARY KEY(tenant_id,book_id,revision),
    UNIQUE(tenant_id,actor_id,request_key),
    FOREIGN KEY(tenant_id,book_id) REFERENCES public.books(tenant_id,id),
    CHECK(num_nonnulls(fiscal_year_start_month,fiscal_year_start_day) IN(0,2)),
    CHECK(fiscal_year_start_month BETWEEN 1 AND 12 AND fiscal_year_start_day BETWEEN 1 AND 31)
);

CREATE FUNCTION public.ledger_book_revision() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
DECLARE
    original public.books%ROWTYPE;
    prior public.book_revisions%ROWTYPE;
    requested jsonb;
    result jsonb;
    expected_name text;
    who uuid;
BEGIN
    IF public.ledger_context(NEW.tenant_id,NEW.book_id) IS NOT TRUE THEN
        RAISE EXCEPTION 'selected Book required' USING ERRCODE='42501';
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.logo_actor='agent' THEN
            IF public.ledger_allowed(NEW.tenant_id,NEW.book_id,true) IS NOT TRUE
                OR NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id',true) THEN
                RAISE EXCEPTION 'authenticated Book writer required' USING ERRCODE='42501';
            END IF;
        ELSE
            who=access.require_owner(NEW.tenant_id,NEW.logo_actor IS NULL);
            IF who IS DISTINCT FROM NEW.actor_id THEN
                RAISE EXCEPTION 'authenticated owner required' USING ERRCODE='42501';
            END IF;
        END IF;
        NEW.recorded_at=clock_timestamp();
    END IF;
    IF NEW.logo_actor IS NULL THEN
        PERFORM public.ledger_lock(NEW.tenant_id,NEW.book_id);
    END IF;
    SELECT * INTO STRICT original FROM public.books WHERE tenant_id=NEW.tenant_id AND id=NEW.book_id;
    SELECT * INTO prior FROM public.book_revisions WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id ORDER BY revision DESC LIMIT 1;
    expected_name=CASE WHEN prior.revision IS NULL THEN original.name ELSE prior.name END;
    IF NEW.revision IS DISTINCT FROM coalesce(prior.revision,0)+1 OR NEW.prior_name IS DISTINCT FROM expected_name THEN
        RAISE EXCEPTION 'stale Book settings revision' USING ERRCODE='23514';
    END IF;
    requested=convert_from(NEW.request_bytes,'UTF8')::jsonb;
    result=convert_from(NEW.result_bytes,'UTF8')::jsonb;
    IF NEW.logo_actor IS NOT NULL THEN
        IF ROW(NEW.name,NEW.founder,NEW.profile,NEW.start_date,NEW.fiscal_year_start_month,
                NEW.fiscal_year_start_day,NEW.accounting_timezone)
            IS DISTINCT FROM ROW(expected_name,prior.founder,prior.profile,prior.start_date,prior.fiscal_year_start_month,
                prior.fiscal_year_start_day,prior.accounting_timezone)
            OR NEW.logo IS NOT DISTINCT FROM prior.logo
            OR (NEW.logo IS NOT NULL AND (public.ledger_measurement_shape(NEW.logo,ARRAY['id','sha256','content_type','size'],
                '{"id":"string","sha256":"string","content_type":"string","size":"number"}'::jsonb) IS NOT TRUE
                OR (NEW.logo->>'id')::uuid::text IS DISTINCT FROM NEW.logo->>'id'
                OR NEW.logo->>'sha256' !~ '^[0-9a-f]{64}$'
                OR NEW.logo->>'content_type' NOT IN('image/jpeg','image/png')
                OR (NEW.logo->>'size')::integer NOT BETWEEN 1 AND 147456))
            OR requested IS DISTINCT FROM jsonb_build_object('logo',NEW.logo)
            OR result IS DISTINCT FROM jsonb_build_object('schema_version',1,'logo',NEW.logo,'metadata_revision',NEW.revision) THEN
            RAISE EXCEPTION 'invalid Book logo revision' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.logo IS DISTINCT FROM prior.logo THEN
        RAISE EXCEPTION 'Book settings cannot change the logo' USING ERRCODE='23514';
    END IF;
    IF NOT public.ledger_measurement_shape(requested,ARRAY['book_id','idempotency_key','revision','name','founder'],
        '{"book_id":"string","idempotency_key":"string","revision":"number","name":"string","founder":"string","profile":"string","start_date":"string","fiscal_year_start_month":"number","fiscal_year_start_day":"number","accounting_timezone":"string"}'::jsonb)
        OR requested->>'book_id' IS DISTINCT FROM NEW.book_id::text
        OR requested->>'idempotency_key' IS DISTINCT FROM NEW.request_key
        OR (requested->>'revision')::bigint IS DISTINCT FROM NEW.revision-1
        OR requested->>'name' IS DISTINCT FROM NEW.name
        OR nullif(requested->>'founder','') IS DISTINCT FROM NEW.founder
        OR (requested ? 'profile' AND nullif(requested->>'profile','') IS DISTINCT FROM NEW.profile)
        OR (NOT requested ? 'profile' AND NEW.profile IS DISTINCT FROM prior.profile)
        OR (requested ? 'start_date' AND nullif(requested->>'start_date','') IS DISTINCT FROM NEW.start_date::text)
        OR (NOT requested ? 'start_date' AND NEW.start_date IS DISTINCT FROM prior.start_date)
        OR (requested ? 'accounting_timezone' AND nullif(requested->>'accounting_timezone','') IS DISTINCT FROM NEW.accounting_timezone)
        OR (NOT requested ? 'accounting_timezone' AND NEW.accounting_timezone IS DISTINCT FROM prior.accounting_timezone)
        OR ((requested ? 'fiscal_year_start_month') IS DISTINCT FROM (requested ? 'fiscal_year_start_day'))
        OR (requested ? 'fiscal_year_start_month' AND
            ROW(nullif((requested->>'fiscal_year_start_month')::integer,0),nullif((requested->>'fiscal_year_start_day')::integer,0))
            IS DISTINCT FROM ROW(NEW.fiscal_year_start_month,NEW.fiscal_year_start_day))
        OR (NOT requested ? 'fiscal_year_start_month' AND ROW(NEW.fiscal_year_start_month,NEW.fiscal_year_start_day)
            IS DISTINCT FROM ROW(prior.fiscal_year_start_month,prior.fiscal_year_start_day)) THEN
        RAISE EXCEPTION 'Book settings differ from accepted request' USING ERRCODE='23514';
    END IF;
    IF NEW.fiscal_year_start_month IS NOT NULL THEN
        PERFORM make_date(2001,NEW.fiscal_year_start_month,NEW.fiscal_year_start_day);
    END IF;
    IF NEW.accounting_timezone IS NOT NULL AND (NEW.accounting_timezone='Local' OR NOT EXISTS(
            SELECT 1 FROM pg_catalog.pg_timezone_names WHERE name=NEW.accounting_timezone)) THEN
        RAISE EXCEPTION 'unsupported accounting timezone' USING ERRCODE='23514';
    END IF;
    IF NOT public.ledger_measurement_shape(result,ARRAY['tenant_id','id','name','currency','scale','created_at','metadata_revision'],
        '{"tenant_id":"string","id":"string","name":"string","currency":"string","scale":"number","created_at":"string","metadata_revision":"number","founder":"string","profile":"string","start_date":"string","fiscal_year_start_month":"number","fiscal_year_start_day":"number","accounting_timezone":"string","logo":"object"}'::jsonb)
        OR result->>'tenant_id' IS DISTINCT FROM NEW.tenant_id::text
        OR result->>'id' IS DISTINCT FROM NEW.book_id::text
        OR result->>'name' IS DISTINCT FROM NEW.name
        OR result->>'currency' IS DISTINCT FROM original.currency
        OR (result->>'scale')::integer IS DISTINCT FROM original.scale
        OR (result->>'created_at')::timestamptz IS DISTINCT FROM original.created_at
        OR (result->>'metadata_revision')::bigint IS DISTINCT FROM NEW.revision
        OR result->'logo' IS DISTINCT FROM NEW.logo
        OR result->>'founder' IS DISTINCT FROM NEW.founder
        OR result->>'profile' IS DISTINCT FROM NEW.profile
        OR result->>'start_date' IS DISTINCT FROM NEW.start_date::text
        OR (result->>'fiscal_year_start_month')::integer IS DISTINCT FROM NEW.fiscal_year_start_month
        OR (result->>'fiscal_year_start_day')::integer IS DISTINCT FROM NEW.fiscal_year_start_day
        OR result->>'accounting_timezone' IS DISTINCT FROM NEW.accounting_timezone THEN
        RAISE EXCEPTION 'Book settings differ from retained outcome' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END
$$;
CREATE TRIGGER book_revision_apply BEFORE INSERT ON public.book_revisions FOR EACH ROW EXECUTE FUNCTION public.ledger_book_revision();
CREATE TRIGGER book_revision_immutable BEFORE UPDATE OR DELETE ON public.book_revisions FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
REVOKE ALL ON FUNCTION public.ledger_book_revision() FROM PUBLIC;
ALTER TABLE public.book_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.book_revisions FORCE ROW LEVEL SECURITY;
CREATE POLICY book_revision_read ON public.book_revisions FOR SELECT USING(
    access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id,book_id,false));
CREATE POLICY book_revision_insert ON public.book_revisions FOR INSERT WITH CHECK(
    logo_actor IS NULL AND public.ledger_context(tenant_id,book_id) AND access.owner_write_allowed(tenant_id));
REVOKE ALL ON public.book_revisions FROM PUBLIC;

CREATE INDEX book_logo_history ON public.book_revisions(tenant_id,book_id,revision DESC) WHERE logo_actor IS NOT NULL;

-- One current image per Book. The revision log keeps small references and
-- provenance, so replacing an image does not retain the old bytes.
CREATE TABLE public.book_logo_images (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid NOT NULL,
    sha256 text NOT NULL,
    content_type text NOT NULL,
    data bytea NOT NULL CHECK(octet_length(data) BETWEEN 1 AND 147456),
    PRIMARY KEY(tenant_id,book_id),
    FOREIGN KEY(tenant_id,book_id) REFERENCES public.books(tenant_id,id),
    CHECK(sha256=encode(pg_catalog.sha256(data),'hex')),
    CHECK((content_type='image/jpeg' AND substr(data,1,3)=decode('ffd8ff','hex'))
        OR (content_type='image/png' AND substr(data,1,8)=decode('89504e470d0a1a0a','hex')))
);
ALTER TABLE public.book_logo_images ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.book_logo_images FORCE ROW LEVEL SECURITY;
CREATE POLICY book_logo_owner_read ON public.book_logo_images FOR SELECT USING(access.owner_allowed(tenant_id));
REVOKE ALL ON public.book_logo_images FROM PUBLIC;

CREATE FUNCTION public.ledger_set_book_logo(t uuid,b uuid,key text,media text,digest text,image bytea) RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
DECLARE
    actor uuid;
    actor_kind text;
    original_name text;
    prior public.book_revisions%ROWTYPE;
    next_logo jsonb;
    result jsonb;
    next_revision bigint;
BEGIN
    IF current_setting('transaction_isolation')<>'read committed' OR public.ledger_context(t,b) IS NOT TRUE THEN
        RAISE EXCEPTION 'selected writable Book required' USING ERRCODE='42501';
    END IF;
    IF nullif(current_setting('vult.connection_id',true),'') IS NOT NULL THEN
        IF public.ledger_allowed(t,b,true) IS NOT TRUE THEN
            RAISE EXCEPTION 'live Book writer required' USING ERRCODE='42501';
        END IF;
        actor_kind='agent';
        actor=current_setting('vult.actor_id',true)::uuid;
    ELSE
        actor_kind='owner';
        actor=access.require_owner(t,false);
    END IF;
    SELECT name INTO STRICT original_name FROM public.books WHERE tenant_id=t AND id=b FOR UPDATE;
    SELECT * INTO prior FROM public.book_revisions WHERE tenant_id=t AND book_id=b ORDER BY revision DESC LIMIT 1;
    next_revision=coalesce(prior.revision,0);
    IF image IS NULL AND prior.logo IS NULL OR image IS NOT NULL AND
        ROW(digest,media,octet_length(image)) IS NOT DISTINCT FROM
        ROW(prior.logo->>'sha256',prior.logo->>'content_type',(prior.logo->>'size')::integer) THEN
        RETURN jsonb_build_object('schema_version',1,'logo',prior.logo,'metadata_revision',next_revision);
    END IF;
    IF image IS NOT NULL THEN
        next_logo=jsonb_build_object('id',gen_random_uuid(),'sha256',digest,'content_type',media,'size',octet_length(image));
    END IF;
    next_revision=next_revision+1;
    result=jsonb_build_object('schema_version',1,'logo',next_logo,'metadata_revision',next_revision);
    INSERT INTO public.book_revisions(tenant_id,book_id,revision,actor_id,request_key,prior_name,name,founder,profile,
        start_date,fiscal_year_start_month,fiscal_year_start_day,accounting_timezone,request_bytes,result_bytes,logo,logo_actor)
    VALUES(t,b,next_revision,actor,key,coalesce(prior.name,original_name),coalesce(prior.name,original_name),
        prior.founder,prior.profile,prior.start_date,prior.fiscal_year_start_month,prior.fiscal_year_start_day,prior.accounting_timezone,
        convert_to(jsonb_build_object('logo',next_logo)::text,'UTF8'),convert_to(result::text,'UTF8'),next_logo,actor_kind);
    IF next_logo IS NULL THEN
        DELETE FROM public.book_logo_images WHERE tenant_id=t AND book_id=b;
    ELSE
        INSERT INTO public.book_logo_images(tenant_id,book_id,id,sha256,content_type,data)
        VALUES(t,b,(next_logo->>'id')::uuid,digest,media,image)
        ON CONFLICT(tenant_id,book_id) DO UPDATE SET
            id=excluded.id,sha256=excluded.sha256,content_type=excluded.content_type,data=excluded.data;
    END IF;
    RETURN result;
END
$$;
REVOKE ALL ON FUNCTION public.ledger_set_book_logo(uuid,uuid,text,text,text,bytea) FROM PUBLIC;

-- Consent uses the same accepted display name as the journal and owner tools.
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
    ORDER BY b.created_at,b.id LIMIT 101
$$;

CREATE TABLE access.passkey_profiles (
    identity_id uuid PRIMARY KEY REFERENCES access.identities(id),
    origin text NOT NULL CHECK (length(origin) BETWEEN 1 AND 512),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'ready')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count BETWEEN 0 AND 32),
    attempt_until timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE access.passkey_credentials (
    identity_id uuid NOT NULL REFERENCES access.passkey_profiles(identity_id),
    credential_id text NOT NULL CHECK (length(credential_id) BETWEEN 1 AND 1366),
    credential jsonb NOT NULL CHECK (jsonb_typeof(credential) = 'object' AND octet_length(credential::text) <= 32768),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (identity_id, credential_id),
    UNIQUE (credential_id)
);

CREATE TABLE access.passkey_setups (
    token_hash text PRIMARY KEY CHECK (token_hash ~ '^[a-f0-9]{64}$'),
    identity_id uuid NOT NULL REFERENCES access.passkey_profiles(identity_id),
    epoch bigint NOT NULL,
    browser_hash text CHECK (browser_hash ~ '^[a-f0-9]{64}$'),
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp() + interval '5 minutes',
    consumed_at timestamptz
);

CREATE TABLE access.passkey_challenges (
    token_hash text PRIMARY KEY CHECK (token_hash ~ '^[a-f0-9]{64}$'),
    identity_id uuid NOT NULL REFERENCES access.passkey_profiles(identity_id),
    epoch bigint NOT NULL,
    browser_hash text NOT NULL CHECK (browser_hash ~ '^[a-f0-9]{64}$'),
    intent text NOT NULL CHECK (intent IN ('registration', 'login')),
    setup_hash text REFERENCES access.passkey_setups(token_hash),
    ceremony jsonb NOT NULL CHECK (jsonb_typeof(ceremony) = 'object' AND octet_length(ceremony::text) <= 32768),
    expires_at timestamptz NOT NULL DEFAULT clock_timestamp() + interval '5 minutes',
    consumed_at timestamptz,
    finished_at timestamptz,
    CHECK ((intent = 'registration') = (setup_hash IS NOT NULL))
);
CREATE INDEX passkey_challenge_owner ON access.passkey_challenges(identity_id, expires_at);
CREATE INDEX passkey_setup_owner ON access.passkey_setups(identity_id, expires_at);

CREATE TABLE access.passkey_events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    identity_id uuid NOT NULL REFERENCES access.passkey_profiles(identity_id),
    kind text NOT NULL CHECK (kind IN ('setup', 'registered', 'recovery', 'authenticated')),
    credential_hash text CHECK (credential_hash ~ '^[a-f0-9]{64}$'),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['passkey_profiles', 'passkey_credentials', 'passkey_setups', 'passkey_challenges', 'passkey_events'] LOOP
        EXECUTE format('ALTER TABLE access.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE access.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON access.%I FROM PUBLIC', tab);
    END LOOP;
END $$;

CREATE FUNCTION access.passkey_profile(sub text, realm text)
RETURNS TABLE(identity_id uuid, epoch bigint, credentials jsonb)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT i.id, i.epoch, coalesce((SELECT jsonb_agg(c.credential ORDER BY c.credential_id)
        FROM access.passkey_credentials c WHERE c.identity_id = i.id AND c.revoked_at IS NULL), '[]'::jsonb)
    FROM access.identities i JOIN access.passkey_profiles p ON p.identity_id = i.id
    WHERE i.issuer = 'urn:vult:builtin' AND i.subject = sub AND p.origin = realm
    AND EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = i.id);
$$;

CREATE FUNCTION access.begin_passkey(sub text, realm text, generation bigint, token text, browser text,
    purpose text, setup text, data jsonb) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; profile access.passkey_profiles%ROWTYPE; setup_record access.passkey_setups%ROWTYPE;
BEGIN
    SELECT i.id INTO who FROM access.identities i
    WHERE i.issuer = 'urn:vult:builtin' AND i.subject = sub AND i.epoch = generation
    AND EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = i.id) FOR UPDATE OF i;
    SELECT * INTO profile FROM access.passkey_profiles WHERE identity_id = who AND origin = realm FOR UPDATE;
    IF who IS NULL OR profile.identity_id IS NULL OR purpose NOT IN ('registration', 'login')
        OR (profile.attempt_until > clock_timestamp() AND profile.attempt_count >= 32) THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    IF purpose = 'registration' THEN
        SELECT * INTO setup_record FROM access.passkey_setups
        WHERE token_hash = setup AND identity_id = who AND epoch = generation
            AND consumed_at IS NULL AND expires_at > clock_timestamp() FOR UPDATE;
        IF setup_record.token_hash IS NULL OR profile.state <> 'pending'
            OR (setup_record.browser_hash IS NOT NULL AND setup_record.browser_hash <> browser)
            OR EXISTS (SELECT 1 FROM access.passkey_credentials WHERE identity_id = who AND revoked_at IS NULL) THEN
            RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
    ELSIF setup IS NOT NULL OR profile.state <> 'ready'
        OR NOT EXISTS (SELECT 1 FROM access.passkey_credentials WHERE identity_id = who AND revoked_at IS NULL) THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    DELETE FROM access.passkey_challenges WHERE token_hash IN (
        SELECT token_hash FROM access.passkey_challenges WHERE identity_id = who
        AND expires_at < clock_timestamp() - interval '5 minutes' ORDER BY expires_at LIMIT 32);
    IF (SELECT count(*) FROM access.passkey_challenges WHERE identity_id = who
        AND expires_at > clock_timestamp() AND consumed_at IS NULL) >= 32 THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    UPDATE access.passkey_profiles SET
        attempt_count = CASE WHEN attempt_until <= clock_timestamp() THEN 1 ELSE attempt_count + 1 END,
        attempt_until = CASE WHEN attempt_until <= clock_timestamp() THEN clock_timestamp() + interval '5 minutes' ELSE attempt_until END
    WHERE identity_id = who;
    INSERT INTO access.passkey_challenges(token_hash, identity_id, epoch, browser_hash, intent, setup_hash, ceremony)
    VALUES (token, who, generation, browser, purpose, setup, data);
END $$;

CREATE FUNCTION access.take_passkey(token text, realm text, browser text, purpose text)
RETURNS TABLE(identity_id uuid, epoch bigint, ceremony jsonb, credentials jsonb)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; generation bigint; challenge access.passkey_challenges%ROWTYPE;
BEGIN
    SELECT c.identity_id INTO who FROM access.passkey_challenges c WHERE c.token_hash = token;
    SELECT i.epoch INTO generation FROM access.identities i WHERE i.id = who FOR UPDATE;
    SELECT * INTO challenge FROM access.passkey_challenges c
    WHERE c.token_hash = token AND c.identity_id = who AND c.epoch = generation
        AND c.browser_hash = browser AND c.intent = purpose AND c.consumed_at IS NULL
        AND c.expires_at > clock_timestamp()
        AND EXISTS (SELECT 1 FROM access.passkey_profiles p WHERE p.identity_id = who AND p.origin = realm)
    FOR UPDATE;
    IF challenge.token_hash IS NULL OR NOT EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = who) THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    UPDATE access.passkey_challenges c SET consumed_at = clock_timestamp() WHERE c.token_hash = token;
    RETURN QUERY SELECT who, generation, challenge.ceremony,
        coalesce((SELECT jsonb_agg(c.credential ORDER BY c.credential_id)
        FROM access.passkey_credentials c WHERE c.identity_id = who AND c.revoked_at IS NULL), '[]'::jsonb);
END $$;

CREATE FUNCTION access.finish_passkey(token text, realm text, browser text, purpose text,
    key_id text, prior jsonb, updated jsonb, session_token text, key_hash text) RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; generation bigint; challenge access.passkey_challenges%ROWTYPE;
    owner_record access.identities%ROWTYPE; setup_record access.passkey_setups%ROWTYPE;
BEGIN
    SELECT c.identity_id INTO who FROM access.passkey_challenges c WHERE c.token_hash = token;
    SELECT * INTO owner_record FROM access.identities i WHERE i.id = who FOR UPDATE;
    generation := owner_record.epoch;
    SELECT * INTO challenge FROM access.passkey_challenges c
    WHERE c.token_hash = token AND c.identity_id = who AND c.epoch = generation
        AND c.browser_hash = browser AND c.intent = purpose AND c.consumed_at IS NOT NULL
        AND c.finished_at IS NULL AND c.expires_at > clock_timestamp()
        AND EXISTS (SELECT 1 FROM access.passkey_profiles p WHERE p.identity_id = who AND p.origin = realm)
    FOR UPDATE;
    IF challenge.token_hash IS NULL OR NOT EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = who) THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    IF purpose = 'registration' THEN
        SELECT * INTO setup_record FROM access.passkey_setups s
        WHERE s.token_hash = challenge.setup_hash AND s.identity_id = who AND s.epoch = generation
            AND s.consumed_at IS NULL AND s.expires_at > clock_timestamp() FOR UPDATE;
        IF setup_record.token_hash IS NULL OR prior IS NOT NULL
            OR (setup_record.browser_hash IS NOT NULL AND setup_record.browser_hash <> browser)
            OR EXISTS (SELECT 1 FROM access.passkey_credentials WHERE identity_id = who AND revoked_at IS NULL) THEN
            RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        INSERT INTO access.passkey_credentials(identity_id, credential_id, credential) VALUES (who, key_id, updated)
        ON CONFLICT (identity_id, credential_id) DO UPDATE
        SET credential = excluded.credential, revoked_at = NULL, created_at = clock_timestamp()
        WHERE access.passkey_credentials.revoked_at IS NOT NULL;
        IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501'; END IF;
        UPDATE access.passkey_setups SET consumed_at = clock_timestamp() WHERE token_hash = setup_record.token_hash;
        UPDATE access.passkey_profiles SET state = 'ready' WHERE identity_id = who;
    ELSIF purpose = 'login' AND prior IS NOT NULL THEN
        UPDATE access.passkey_credentials SET credential = updated
        WHERE identity_id = who AND credential_id = key_id AND credential = prior AND revoked_at IS NULL;
        IF NOT FOUND THEN RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501'; END IF;
    ELSE
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    UPDATE access.passkey_challenges SET finished_at = clock_timestamp() WHERE token_hash = token;
    PERFORM access.issue_session(owner_record.issuer, owner_record.subject, realm, session_token, clock_timestamp());
    INSERT INTO access.passkey_events(identity_id, kind, credential_hash)
    VALUES (who, CASE WHEN purpose = 'registration' THEN 'registered' ELSE 'authenticated' END, key_hash);
    RETURN who;
END $$;

CREATE FUNCTION access.recover_passkey(sub text, realm text, identity uuid, recovery text,
    setup text, browser text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; generation bigint; profile access.passkey_profiles%ROWTYPE;
BEGIN
    SELECT i.id, i.epoch INTO who, generation FROM access.identities i
    WHERE i.id = identity AND i.issuer = 'urn:vult:builtin' AND i.subject = sub
        AND EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = i.id) FOR UPDATE OF i;
    SELECT * INTO profile FROM access.passkey_profiles WHERE identity_id = who AND origin = realm FOR UPDATE;
    IF who IS NULL OR profile.identity_id IS NULL OR recovery IS NULL OR setup IS NULL OR browser IS NULL
        OR recovery !~ '^[a-f0-9]{64}$'
        OR setup !~ '^[a-f0-9]{64}$' OR browser !~ '^[a-f0-9]{64}$'
        OR (profile.attempt_until > clock_timestamp() AND profile.attempt_count >= 32) THEN
        RETURN false;
    END IF;
    -- Invalid codes consume the durable budget too; the caller commits this
    -- statement before starting a replacement credential ceremony.
    UPDATE access.passkey_profiles SET
        attempt_count = CASE WHEN attempt_until <= clock_timestamp() THEN 1 ELSE attempt_count + 1 END,
        attempt_until = CASE WHEN attempt_until <= clock_timestamp() THEN clock_timestamp() + interval '5 minutes' ELSE attempt_until END
    WHERE identity_id = who;
    DELETE FROM access.recovery_codes WHERE identity_id = who AND code_hash = recovery;
    IF NOT FOUND THEN RETURN false; END IF;
    UPDATE access.identities SET epoch = epoch + 1 WHERE id = who RETURNING epoch INTO generation;
    UPDATE access.passkey_credentials SET revoked_at = clock_timestamp() WHERE identity_id = who AND revoked_at IS NULL;
    UPDATE access.passkey_setups SET consumed_at = clock_timestamp() WHERE identity_id = who AND consumed_at IS NULL;
    UPDATE access.passkey_profiles SET state = 'pending', attempt_count = 0, attempt_until = clock_timestamp()
    WHERE identity_id = who;
    INSERT INTO access.passkey_setups(token_hash, identity_id, epoch, browser_hash) VALUES (setup, who, generation, browser);
    INSERT INTO access.passkey_events(identity_id, kind) VALUES (who, 'recovery');
    RETURN true;
END $$;

REVOKE ALL ON FUNCTION access.passkey_profile(text,text),
    access.begin_passkey(text,text,bigint,text,text,text,text,jsonb),
    access.take_passkey(text,text,text,text),
    access.finish_passkey(text,text,text,text,text,jsonb,jsonb,text,text),
    access.recover_passkey(text,text,uuid,text,text,text) FROM PUBLIC;

CREATE TABLE public.operation_proposals (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid NOT NULL,
    sequence bigint NOT NULL CHECK (sequence > 0),
    actor_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    request_key text NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
    operation text NOT NULL CHECK (operation IN ('post_journal', 'reverse_journal')),
    operation_key text NOT NULL CHECK (length(operation_key) BETWEEN 1 AND 128),
    payload_bytes bytea NOT NULL CHECK (octet_length(payload_bytes) BETWEEN 2 AND 262144),
    sha256 text GENERATED ALWAYS AS (encode(sha256(
        uuid_send(tenant_id) || uuid_send(book_id) || uuid_send(id) ||
        uuid_send(actor_id) || uuid_send(connection_id) || payload_bytes), 'hex')) STORED,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz,
    PRIMARY KEY (tenant_id, book_id, id),
    UNIQUE (tenant_id, book_id, sequence),
    UNIQUE (tenant_id, book_id, actor_id, request_key),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    CONSTRAINT operation_proposal_expiry CHECK (expires_at IS NULL OR expires_at > created_at),
    CHECK (jsonb_typeof(convert_from(payload_bytes, 'UTF8')::jsonb) = 'object'),
    CHECK ((convert_from(payload_bytes, 'UTF8')::jsonb ->> 'version') IS NOT DISTINCT FROM '1'),
    CONSTRAINT operation_proposals_check1 CHECK ((convert_from(payload_bytes, 'UTF8')::jsonb ->> 'operation') IS NOT DISTINCT FROM operation),
    CONSTRAINT operation_proposals_check2 CHECK ((convert_from(payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'request_key') IS NOT DISTINCT FROM operation_key)
);

CREATE TABLE public.operation_decisions (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    proposal_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    owner_id uuid NOT NULL,
    owner_epoch bigint NOT NULL CHECK (owner_epoch >= 0),
    request_key text NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
    sha256 text NOT NULL CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    decision text NOT NULL CHECK (decision IN ('approve', 'reject')),
    executor_actor_id uuid,
    executor_connection_id uuid,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 4000),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz,
    PRIMARY KEY (tenant_id, book_id, proposal_id, revision),
    UNIQUE (tenant_id, book_id, owner_id, request_key),
    FOREIGN KEY (tenant_id, book_id, proposal_id) REFERENCES public.operation_proposals(tenant_id, book_id, id),
    CONSTRAINT operation_decision_expiry CHECK (expires_at IS NULL OR expires_at > recorded_at),
    CONSTRAINT operation_decision_executor CHECK ((executor_actor_id IS NULL) = (executor_connection_id IS NULL)
        AND (decision = 'approve' OR executor_actor_id IS NULL))
);

CREATE TABLE public.operation_consumptions (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    proposal_id uuid NOT NULL,
    decision_revision bigint NOT NULL,
    receipt_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, proposal_id),
    UNIQUE (tenant_id, book_id, receipt_id),
    FOREIGN KEY (tenant_id, book_id, proposal_id, decision_revision) REFERENCES public.operation_decisions(tenant_id, book_id, proposal_id, revision),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
);

CREATE FUNCTION access.operation_owner_epoch(t uuid) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; generation bigint;
BEGIN
    who := access.require_owner(t, true);
    SELECT epoch INTO STRICT generation FROM access.identities WHERE id = who;
    RETURN generation;
END $$;

CREATE FUNCTION access.operation_owner_allowed(t uuid, b uuid, who uuid, generation bigint) RETURNS boolean
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT public.ledger_context(t, b) AND (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) AND EXISTS (
        SELECT 1 FROM access.identities i JOIN access.owners o ON o.identity_id = i.id
        WHERE i.id = who AND i.epoch = generation AND o.tenant_id = t)
$$;

CREATE FUNCTION access.operation_executor_active(t uuid, b uuid, connection uuid, actor uuid) RETURNS boolean
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT public.ledger_context(t, b) AND (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) AND EXISTS (
        SELECT 1 FROM public.connections p WHERE p.tenant_id = t AND p.book_id = b
            AND p.id = connection AND p.actor_id = actor AND p.access = 'write' AND p.revoked_at IS NULL
            AND (NOT EXISTS (SELECT 1 FROM access.oauth_grants g WHERE g.connection_id = p.id)
                OR EXISTS (SELECT 1 FROM access.oauth_grants g
                    JOIN access.identities i ON i.id = g.identity_id AND i.epoch = g.owner_epoch
                    JOIN access.owners o ON o.identity_id = i.id AND o.tenant_id = t
                    WHERE g.connection_id = p.id)))
$$;

CREATE FUNCTION access.hold_operation_owner(t uuid, b uuid, who uuid, generation bigint) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_context(t, b) OR NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN RETURN false; END IF;
    PERFORM 1 FROM access.identities WHERE id = who AND epoch = generation FOR SHARE;
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM access.owners WHERE identity_id = who AND tenant_id = t FOR SHARE;
    RETURN FOUND;
END $$;

CREATE FUNCTION public.operation_dependencies(t uuid, b uuid, op text, args jsonb, excluded uuid)
RETURNS jsonb LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE line_input jsonb; source_input jsonb; accounting_day date; target uuid;
    accounts jsonb; documents jsonb; periods jsonb; reversed boolean;
BEGIN
    IF NOT public.ledger_context(t, b) THEN
        RAISE EXCEPTION 'selected Book required' USING ERRCODE = '42501';
    END IF;
    accounting_day := (args ->> 'accounting_date')::date;
    IF op = 'post_journal' THEN
        line_input := args -> 'lines';
        source_input := coalesce(args -> 'sources', '[]'::jsonb);
    ELSIF op = 'reverse_journal' THEN
        target := (args ->> 'entry_id')::uuid;
        SELECT jsonb_agg(jsonb_build_object('account_id', account_id) ORDER BY position) INTO line_input
        FROM public.lines WHERE tenant_id = t AND book_id = b AND entry_id = target;
        source_input := '[]'::jsonb;
    ELSE
        RAISE EXCEPTION 'unsupported operation' USING ERRCODE = '23514';
    END IF;
    IF jsonb_typeof(line_input) IS DISTINCT FROM 'array' OR jsonb_array_length(line_input) NOT BETWEEN 2 AND 256
        OR jsonb_typeof(source_input) IS DISTINCT FROM 'array' OR jsonb_array_length(source_input) > 32
        OR accounting_day IS NULL THEN
        RAISE EXCEPTION 'invalid operation' USING ERRCODE = '23514';
    END IF;
    SELECT coalesce(jsonb_agg(to_jsonb(position) ORDER BY account_id), '[]'::jsonb) INTO accounts FROM (
        SELECT a.id::text AS account_id, a.class, coalesce(max(e.sequence), 0) AS last_sequence,
            coalesce(sum(CASE WHEN l.side = 'debit' THEN l.amount_minor::numeric ELSE -l.amount_minor::numeric END), 0)::text AS balance_minor,
            coalesce(a.currency_revision, 0) AS currency_revision, coalesce(a.role_revision, 0) AS role_revision,
            coalesce(a.native_origin_revision, 0) AS origin_revision
        FROM public.accounts a LEFT JOIN public.lines l ON l.tenant_id = a.tenant_id AND l.book_id = a.book_id AND l.account_id = a.id
            AND l.entry_id IS DISTINCT FROM excluded
        LEFT JOIN public.entries e ON e.tenant_id = l.tenant_id AND e.book_id = l.book_id AND e.id = l.entry_id
        WHERE a.tenant_id = t AND a.book_id = b AND a.id IN (
            SELECT (value ->> 'account_id')::uuid FROM jsonb_array_elements(line_input))
        GROUP BY a.id
    ) position;
    IF jsonb_array_length(accounts) <> (SELECT count(DISTINCT value ->> 'account_id') FROM jsonb_array_elements(line_input)) THEN
        RAISE EXCEPTION 'operation account unavailable' USING ERRCODE = '23514';
    END IF;
    SELECT coalesce(jsonb_agg(to_jsonb(original) ORDER BY document_id, location::text), '[]'::jsonb) INTO documents FROM (
        SELECT d.id::text AS document_id, d.sha256, d.state, source.value -> 'location' AS location,
            usage.active_entries, usage.last_citation
        FROM jsonb_array_elements(source_input) source JOIN public.documents d
            ON d.tenant_id = t AND d.book_id = b AND d.id = (source.value ->> 'document_id')::uuid
        CROSS JOIN LATERAL (
            SELECT count(DISTINCT c.entry_id) AS active_entries, coalesce(max(c.sequence), 0) AS last_citation
            FROM public.source_citations c
            WHERE c.tenant_id = t AND c.book_id = b AND c.document_id = d.id AND c.location = source.value -> 'location'
            AND c.entry_id IS DISTINCT FROM excluded
            AND NOT EXISTS (SELECT 1 FROM public.source_citations newer WHERE newer.tenant_id = t AND newer.book_id = b AND newer.source_id = c.source_id AND newer.revision > c.revision)
            AND NOT EXISTS (SELECT 1 FROM public.entries reversal WHERE reversal.tenant_id = t AND reversal.book_id = b AND reversal.reversal_of = c.entry_id AND reversal.id IS DISTINCT FROM excluded)
        ) usage
    ) original;
    IF jsonb_array_length(documents) <> jsonb_array_length(source_input) THEN
        RAISE EXCEPTION 'operation original unavailable' USING ERRCODE = '23514';
    END IF;
    SELECT coalesce(jsonb_agg(id::text ORDER BY id), '[]'::jsonb) INTO periods FROM public.closed_periods
    WHERE tenant_id = t AND book_id = b AND accounting_day BETWEEN start_date AND end_date;
    SELECT EXISTS(SELECT 1 FROM public.entries WHERE tenant_id = t AND book_id = b AND reversal_of = target AND id IS DISTINCT FROM excluded) INTO reversed;
    RETURN jsonb_build_object('accounts', accounts, 'documents', documents, 'closed_periods', periods, 'reversed', reversed);
END $$;

CREATE FUNCTION public.operation_proposal_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_import_owner() THEN
        PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
        IF NOT public.ledger_allowed(NEW.tenant_id, NEW.book_id, true)
            OR coalesce(current_setting('vult.owner_posting', true), '') <> ''
            OR NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true)
            OR NEW.connection_id::text IS DISTINCT FROM current_setting('vult.connection_id', true) THEN
            RAISE EXCEPTION 'operation authority required' USING ERRCODE = '42501';
        END IF;
        NEW.created_at := clock_timestamp();
        NEW.expires_at := NULL;
        SELECT write_epoch INTO STRICT NEW.sequence FROM public.books
        WHERE tenant_id = NEW.tenant_id AND id = NEW.book_id;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER operation_proposal_insert BEFORE INSERT ON public.operation_proposals
    FOR EACH ROW EXECUTE FUNCTION public.operation_proposal_guard();

CREATE FUNCTION public.operation_proposal_retain() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE original jsonb; sources jsonb;
BEGIN
    IF NOT public.ledger_import_owner() THEN
        sources := coalesce(convert_from(NEW.payload_bytes, 'UTF8')::jsonb -> 'arguments' -> 'sources', '[]'::jsonb);
        IF jsonb_typeof(sources) IS DISTINCT FROM 'array' OR jsonb_array_length(sources) > 32 THEN
            RAISE EXCEPTION 'invalid operation originals' USING ERRCODE = '23514';
        END IF;
        FOR original IN SELECT value FROM jsonb_array_elements(sources) LOOP
            PERFORM public.document_use(NEW.tenant_id, NEW.book_id, (original ->> 'document_id')::uuid);
        END LOOP;
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER operation_proposal_00_retain AFTER INSERT ON public.operation_proposals
    FOR EACH ROW EXECUTE FUNCTION public.operation_proposal_retain();

CREATE FUNCTION public.operation_proposal_final() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE payload jsonb; original jsonb; sources jsonb; retained jsonb; source_keys jsonb; retained_keys jsonb;
BEGIN
    payload := convert_from(NEW.payload_bytes, 'UTF8')::jsonb;
    sources := coalesce(payload -> 'arguments' -> 'sources', '[]'::jsonb);
    retained := payload -> 'dependencies' -> 'documents';
    IF jsonb_typeof(sources) IS DISTINCT FROM 'array' OR jsonb_typeof(retained) IS DISTINCT FROM 'array'
        OR jsonb_array_length(retained) <> jsonb_array_length(sources) THEN
        RAISE EXCEPTION 'operation originals differ' USING ERRCODE = '23514';
    END IF;
    SELECT coalesce(jsonb_agg(jsonb_build_object('document_id', value -> 'document_id', 'location', value -> 'location')
        ORDER BY value ->> 'document_id', (value -> 'location')::text), '[]'::jsonb) INTO source_keys FROM jsonb_array_elements(sources);
    SELECT coalesce(jsonb_agg(jsonb_build_object('document_id', value -> 'document_id', 'location', value -> 'location')
        ORDER BY value ->> 'document_id', (value -> 'location')::text), '[]'::jsonb) INTO retained_keys FROM jsonb_array_elements(retained);
    IF source_keys IS DISTINCT FROM retained_keys OR jsonb_array_length(sources) <>
        (SELECT count(DISTINCT value) FROM jsonb_array_elements(source_keys)) THEN
        RAISE EXCEPTION 'operation originals differ' USING ERRCODE = '23514';
    END IF;
    FOR original IN SELECT value FROM jsonb_array_elements(retained) LOOP
        IF NOT EXISTS (SELECT 1 FROM public.documents WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id
            AND id = (original ->> 'document_id')::uuid AND sha256 = original ->> 'sha256' AND state = 'ready' AND used_at IS NOT NULL)
            OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(sources) s
                WHERE s.value ->> 'document_id' = original ->> 'document_id' AND s.value -> 'location' = original -> 'location') THEN
            RAISE EXCEPTION 'operation original unavailable' USING ERRCODE = '23514';
        END IF;
    END LOOP;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER operation_proposal_final AFTER INSERT ON public.operation_proposals
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.operation_proposal_final();

CREATE FUNCTION public.operation_decision_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE proposal public.operation_proposals%ROWTYPE; who uuid; prior bigint;
BEGIN
    IF NOT public.ledger_import_owner() THEN
        who := access.require_owner(NEW.tenant_id, true);
        IF NEW.executor_actor_id IS NOT NULL OR NEW.executor_connection_id IS NOT NULL THEN
            RAISE EXCEPTION 'approval posts directly' USING ERRCODE = '23514';
        END IF;
        PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
        IF NEW.owner_id IS DISTINCT FROM who THEN
            RAISE EXCEPTION 'owner decision required' USING ERRCODE = '42501';
        END IF;
        NEW.recorded_at := clock_timestamp();
        NEW.expires_at := NULL;
        NEW.owner_epoch := access.operation_owner_epoch(NEW.tenant_id);
    ELSIF NEW.executor_actor_id IS NOT NULL THEN
        -- A restored approval made for an agent to execute keeps its window.
        NEW.expires_at := coalesce(NEW.expires_at, NEW.recorded_at + interval '5 minutes');
    END IF;
    SELECT * INTO STRICT proposal FROM public.operation_proposals
    WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.proposal_id;
    SELECT coalesce(max(revision), 0) INTO prior FROM public.operation_decisions
    WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND proposal_id = NEW.proposal_id;
    IF NEW.revision <> prior + 1 OR NEW.sha256 <> proposal.sha256
        OR NEW.recorded_at < proposal.created_at
        OR (proposal.expires_at IS NOT NULL AND NEW.recorded_at >= proposal.expires_at)
        OR EXISTS (SELECT 1 FROM public.operation_consumptions
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND proposal_id = NEW.proposal_id) THEN
        RAISE EXCEPTION 'proposal unavailable' USING ERRCODE = '23514';
    END IF;
    IF proposal.expires_at IS NOT NULL AND NEW.expires_at IS NOT NULL THEN
        NEW.expires_at := least(NEW.expires_at, proposal.expires_at);
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER operation_decision_insert BEFORE INSERT ON public.operation_decisions
    FOR EACH ROW EXECUTE FUNCTION public.operation_decision_guard();

-- A prepared entry stays approvable when other entries change an account's
-- balance. It still goes out of date when anything it rests on changes: an
-- account's currency, role or origin, the use of its cited sources, a closed
-- period, or a reversal. The approved entry is posted exactly as prepared.
CREATE FUNCTION public.operation_dependencies_settled(deps jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE SET search_path = pg_catalog AS $$
    SELECT CASE WHEN deps IS NULL OR jsonb_typeof(deps -> 'accounts') IS DISTINCT FROM 'array' THEN deps
        ELSE jsonb_set(deps, '{accounts}', coalesce((
            SELECT jsonb_agg(account - 'last_sequence' - 'balance_minor' ORDER BY ordinal)
            FROM jsonb_array_elements(deps -> 'accounts') WITH ORDINALITY AS listed(account, ordinal)), '[]'::jsonb))
    END
$$;

CREATE FUNCTION public.operation_consumption_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE proposal public.operation_proposals%ROWTYPE;
    approval public.operation_decisions%ROWTYPE; receipt public.requests%ROWTYPE; entry public.entries%ROWTYPE;
    current_xact bigint := mod(pg_current_xact_id()::text::numeric, 4294967296)::bigint;
    by_owner boolean; payload jsonb; preview jsonb; result jsonb;
BEGIN
    SELECT * INTO STRICT proposal FROM public.operation_proposals
    WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.proposal_id;
    SELECT * INTO STRICT approval FROM public.operation_decisions
    WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND proposal_id = NEW.proposal_id
        AND revision = NEW.decision_revision;
    SELECT * INTO STRICT receipt FROM public.requests
    WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.receipt_id;
    SELECT * INTO STRICT entry FROM public.entries
    WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND receipt_id = NEW.receipt_id;
    by_owner := approval.executor_actor_id IS NULL;
    payload := convert_from(proposal.payload_bytes, 'UTF8')::jsonb;
    IF approval.decision <> 'approve' OR approval.sha256 <> proposal.sha256
        OR receipt.operation <> proposal.operation OR receipt.request_key <> proposal.operation_key
        OR convert_from(receipt.request_bytes, 'UTF8')::jsonb IS DISTINCT FROM payload -> 'arguments'
        OR (by_owner AND (receipt.actor_id <> proposal.actor_id OR NEW.actor_id <> approval.owner_id))
        OR (NOT by_owner AND (receipt.actor_id <> approval.executor_actor_id OR NEW.actor_id <> approval.executor_actor_id)) THEN
        RAISE EXCEPTION 'operation differs from approval' USING ERRCODE = '23514';
    END IF;
    IF by_owner AND TG_WHEN = 'BEFORE' THEN
        -- Journal constraints bind the receipt's lines to the stored lines.
        -- Compare those same lines, excluding generated IDs, to the review.
        preview := payload -> 'preview';
        result := convert_from(receipt.result_bytes, 'UTF8')::jsonb;
        IF ROW(entry.economic_date::text, entry.accounting_date::text, entry.description,
                entry.reversal_of::text, entry.measurement_policy) IS DISTINCT FROM
            ROW(preview ->> 'economic_date', preview ->> 'accounting_date', preview ->> 'description',
                preview ->> 'reversal_of', preview -> 'measurement_policy')
            OR preview -> 'sources' IS DISTINCT FROM coalesce(payload -> 'arguments' -> 'sources', '[]'::jsonb)
            OR (SELECT jsonb_agg(line - ARRAY['id', 'reverses_line_id'] ORDER BY position)
                FROM jsonb_array_elements(result -> 'lines') WITH ORDINALITY AS lines(line, position))
                IS DISTINCT FROM preview -> 'lines' THEN
            RAISE EXCEPTION 'posted entry differs from review' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF approval.revision <> (SELECT max(revision) FROM public.operation_decisions
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND proposal_id = NEW.proposal_id) THEN
        RAISE EXCEPTION 'consumed approval must be the final decision' USING ERRCODE = '23514';
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NOT EXISTS (SELECT 1 FROM public.requests r JOIN public.entries e
                ON e.tenant_id = r.tenant_id AND e.book_id = r.book_id AND e.receipt_id = r.id
                WHERE r.id = NEW.receipt_id AND r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id
                AND r.xmin::text::bigint = current_xact AND e.xmin::text::bigint = current_xact) THEN
            RAISE EXCEPTION 'operation must commit with approval consumption' USING ERRCODE = '23514';
        END IF;
        IF NOT access.hold_operation_owner(NEW.tenant_id, NEW.book_id, approval.owner_id, approval.owner_epoch)
            OR (proposal.expires_at IS NOT NULL AND proposal.expires_at <= clock_timestamp())
            OR (approval.expires_at IS NOT NULL AND approval.expires_at <= clock_timestamp()) THEN
            RAISE EXCEPTION 'operation approval unavailable' USING ERRCODE = '42501';
        END IF;
        IF by_owner THEN
            IF NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = NEW.tenant_id
                    AND d.book_id = NEW.book_id AND d.proposal_id = NEW.proposal_id AND d.revision = approval.revision
                    AND d.xmin::text::bigint = current_xact)
                OR access.require_owner(NEW.tenant_id, true) IS DISTINCT FROM approval.owner_id
                OR NOT access.owner_posting_allowed(NEW.tenant_id, NEW.book_id) THEN
                RAISE EXCEPTION 'operation approval unavailable' USING ERRCODE = '42501';
            END IF;
        ELSIF NOT public.ledger_allowed(NEW.tenant_id, NEW.book_id, true)
            OR NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true)
            OR approval.executor_connection_id::text IS DISTINCT FROM current_setting('vult.connection_id', true) THEN
            RAISE EXCEPTION 'operation approval unavailable' USING ERRCODE = '42501';
        END IF;
        IF TG_WHEN = 'AFTER' AND public.operation_dependencies_settled(public.operation_dependencies(NEW.tenant_id, NEW.book_id, proposal.operation,
                payload -> 'arguments', entry.id)) IS DISTINCT FROM public.operation_dependencies_settled(payload -> 'dependencies') THEN
            RAISE EXCEPTION 'proposal dependencies changed' USING ERRCODE = 'VP001';
        END IF;
        NEW.recorded_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER operation_consumption_insert BEFORE INSERT ON public.operation_consumptions
    FOR EACH ROW EXECUTE FUNCTION public.operation_consumption_guard();
CREATE CONSTRAINT TRIGGER operation_consumption_final AFTER INSERT ON public.operation_consumptions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.operation_consumption_guard();

CREATE FUNCTION public.operation_decision_final() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_import_owner() AND
        (access.require_owner(NEW.tenant_id, true) IS DISTINCT FROM NEW.owner_id
            OR (NEW.expires_at IS NOT NULL AND NEW.expires_at <= clock_timestamp())) THEN
        RAISE EXCEPTION 'owner decision expired' USING ERRCODE = '42501';
    END IF;
    -- Direct approvals and their postings are one retained fact, including
    -- during a restore. Legacy approvals can have no consumed entry.
    IF NEW.decision = 'approve' AND NEW.executor_actor_id IS NULL AND NOT EXISTS (
        SELECT 1 FROM public.operation_consumptions c
        WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id AND c.proposal_id = NEW.proposal_id
            AND c.decision_revision = NEW.revision AND c.actor_id = NEW.owner_id) THEN
        RAISE EXCEPTION 'approval must post the proposal' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER operation_decision_final AFTER INSERT ON public.operation_decisions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.operation_decision_final();

CREATE OR REPLACE FUNCTION public.document_used_receipt() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.used_at IS NOT NULL
        AND NOT EXISTS (SELECT 1 FROM public.source_citations WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND document_id = NEW.id)
        AND NOT EXISTS (SELECT 1 FROM public.evidence_links WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND document_id = NEW.id)
        AND NOT EXISTS (SELECT 1 FROM public.operation_proposals p WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id
            AND convert_from(p.payload_bytes, 'UTF8')::jsonb -> 'dependencies' -> 'documents'
                @> jsonb_build_array(jsonb_build_object('document_id', NEW.id::text))) THEN
        RAISE EXCEPTION 'document use requires recorded history' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['operation_proposals', 'operation_decisions', 'operation_consumptions'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('CREATE TRIGGER operation_immutable BEFORE UPDATE OR DELETE ON public.%I FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable()', tab);
        EXECUTE format('CREATE POLICY operation_owner_read ON public.%I FOR SELECT USING(access.owner_allowed(tenant_id))', tab);
    END LOOP;
END $$;
CREATE POLICY operation_actor_read ON public.operation_proposals FOR SELECT USING (
    public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY operation_actor_insert ON public.operation_proposals FOR INSERT WITH CHECK (
    public.ledger_allowed(tenant_id, book_id, true) AND actor_id::text = current_setting('vult.actor_id', true));
CREATE POLICY operation_decision_read ON public.operation_decisions FOR SELECT USING (
    EXISTS (SELECT 1 FROM public.operation_proposals p WHERE p.tenant_id = operation_decisions.tenant_id
        AND p.book_id = operation_decisions.book_id AND p.id = operation_decisions.proposal_id));
CREATE POLICY operation_owner_decide ON public.operation_decisions FOR INSERT WITH CHECK (
    access.owner_write_allowed(tenant_id) AND owner_id = access.owner_identity(tenant_id));
CREATE POLICY operation_consumption_read ON public.operation_consumptions FOR SELECT USING (
    public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY operation_actor_consume ON public.operation_consumptions FOR INSERT WITH CHECK (
    public.ledger_allowed(tenant_id, book_id, true) AND actor_id::text = current_setting('vult.actor_id', true));

REVOKE ALL ON public.operation_proposals, public.operation_decisions, public.operation_consumptions FROM PUBLIC;
REVOKE ALL ON FUNCTION public.operation_proposal_guard(), public.operation_proposal_retain(), public.operation_proposal_final(), public.operation_decision_guard(), public.operation_consumption_guard(), public.operation_decision_final(), public.operation_dependencies(uuid, uuid, text, jsonb, uuid), public.operation_dependencies_settled(jsonb) FROM PUBLIC;
REVOKE ALL ON FUNCTION access.operation_owner_epoch(uuid), access.operation_owner_allowed(uuid, uuid, uuid, bigint), access.operation_executor_active(uuid, uuid, uuid, uuid), access.hold_operation_owner(uuid, uuid, uuid, bigint) FROM PUBLIC;

-- Book search. Entries and lines are immutable, so their searchable words and
-- totals are derived once, by trigger, on every write path: posting,
-- reversal, import and restore. Account names can change, so they are matched
-- live through lines_account rather than indexed.
--
-- One function folds and splits text, for the stored words and for queries
-- alike, so the two can never disagree. No extensions are required.

-- Folded text: accents removed, lower case, every run of characters that are
-- not letters or digits replaced by one space, padded with a space each side.
CREATE FUNCTION public.search_text(t text) RETURNS text
 LANGUAGE sql IMMUTABLE PARALLEL SAFE SET search_path=pg_catalog,pg_temp AS $$
 SELECT ' '||btrim(regexp_replace(
  lower(regexp_replace(normalize(coalesce(t,''),NFKD),'[̀-ͯ]','','g')),
  '[^[:alnum:]]+',' ','g'))||' '
$$;

-- The distinct words of a text, at most 64 characters each.
CREATE FUNCTION public.search_tokens(t text) RETURNS text[]
 LANGUAGE sql IMMUTABLE PARALLEL SAFE SET search_path=pg_catalog,pg_temp AS $$
 SELECT coalesce(array_agg(DISTINCT w),'{}') FROM unnest(string_to_array(btrim(public.search_text(t)),' ')) w
 WHERE w<>'' AND length(w)<=64
$$;

CREATE TABLE public.search_words (
 tenant_id uuid NOT NULL, book_id uuid NOT NULL, entry_id uuid NOT NULL,
 word text COLLATE "C" NOT NULL CHECK (length(word) BETWEEN 1 AND 64)
);

-- The sum of an entry's debit lines in the Book currency: its amount (amount
-- role book_debit_total). An entry whose debits are all zero has no row.
CREATE TABLE public.search_totals (
 tenant_id uuid NOT NULL, book_id uuid NOT NULL, entry_id uuid NOT NULL,
 debit_minor numeric NOT NULL CHECK (debit_minor > 0)
);

-- Bulk load the retained history before building search indexes and checking references.
INSERT INTO public.search_words(tenant_id, book_id, entry_id, word)
SELECT e.tenant_id, e.book_id, e.id, w FROM public.entries e, unnest(public.search_tokens(e.description)) w
UNION
SELECT l.tenant_id, l.book_id, l.entry_id, w FROM public.lines l, unnest(public.search_tokens(l.memo)) w;
INSERT INTO public.search_totals(tenant_id, book_id, entry_id, debit_minor)
SELECT tenant_id, book_id, entry_id, sum(amount_minor::numeric) FROM public.lines
WHERE side = 'debit' AND amount_minor > 0 GROUP BY tenant_id, book_id, entry_id;
ALTER TABLE public.search_words
 ADD PRIMARY KEY (tenant_id, book_id, word, entry_id),
 ADD FOREIGN KEY (tenant_id, book_id, entry_id) REFERENCES public.entries(tenant_id, book_id, id) DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE public.search_totals
 ADD PRIMARY KEY (tenant_id, book_id, entry_id),
 ADD FOREIGN KEY (tenant_id, book_id, entry_id) REFERENCES public.entries(tenant_id, book_id, id) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX search_totals_amount ON public.search_totals(tenant_id, book_id, debit_minor);
CREATE INDEX lines_amount ON public.lines(tenant_id, book_id, amount_minor);
CREATE INDEX entries_accounting_date ON public.entries(tenant_id, book_id, accounting_date, sequence);
CREATE INDEX entries_recorded_at ON public.entries(tenant_id, book_id, recorded_at);

-- Index writes run as the tables' owner: the runtime role can read the index
-- but never write it.
CREATE FUNCTION public.search_index_entry() RETURNS trigger
 LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
BEGIN
 INSERT INTO public.search_words(tenant_id, book_id, entry_id, word)
 SELECT NEW.tenant_id, NEW.book_id, NEW.id, w FROM unnest(public.search_tokens(NEW.description)) w
 ON CONFLICT DO NOTHING;
 RETURN NULL;
END $$;
CREATE TRIGGER search_index AFTER INSERT ON public.entries
 FOR EACH ROW EXECUTE FUNCTION public.search_index_entry();

CREATE FUNCTION public.search_index_line() RETURNS trigger
 LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,pg_temp AS $$
BEGIN
 INSERT INTO public.search_words(tenant_id, book_id, entry_id, word)
 SELECT NEW.tenant_id, NEW.book_id, NEW.entry_id, w FROM unnest(public.search_tokens(NEW.memo)) w
 ON CONFLICT DO NOTHING;
 IF NEW.side = 'debit' AND NEW.amount_minor > 0 THEN
  INSERT INTO public.search_totals(tenant_id, book_id, entry_id, debit_minor)
  VALUES (NEW.tenant_id, NEW.book_id, NEW.entry_id, NEW.amount_minor)
  ON CONFLICT (tenant_id, book_id, entry_id)
  DO UPDATE SET debit_minor = public.search_totals.debit_minor + EXCLUDED.debit_minor;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER search_index AFTER INSERT ON public.lines
 FOR EACH ROW EXECUTE FUNCTION public.search_index_line();

CREATE TRIGGER search_words_immutable BEFORE UPDATE OR DELETE ON public.search_words
 FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE TRIGGER search_totals_immutable BEFORE DELETE ON public.search_totals
 FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();

-- Reads follow the ledger's scope: a live connection to the Book, or the owner.
-- Row security is enabled but not forced, so the owning role's triggers write.
DO $$
DECLARE tab text;
BEGIN
 FOREACH tab IN ARRAY ARRAY['search_words', 'search_totals'] LOOP
  EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
  EXECUTE format('CREATE POLICY scoped_read ON public.%I FOR SELECT USING (public.ledger_allowed(tenant_id,book_id,false))', tab);
  EXECUTE format('CREATE POLICY owner_read ON public.%I FOR SELECT USING (access.owner_allowed(tenant_id))', tab);
  EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
 END LOOP;
END
$$;
REVOKE ALL ON FUNCTION public.search_index_entry(), public.search_index_line() FROM PUBLIC;

CREATE TABLE access.user_settings (
    identity_id uuid PRIMARY KEY REFERENCES access.identities(id),
    revision bigint NOT NULL CHECK (revision >= 0),
    display_name text NOT NULL CHECK (length(display_name) <= 80),
    preferences jsonb NOT NULL CHECK (jsonb_typeof(preferences) = 'object'),
    appearance text NOT NULL CHECK (appearance IN ('system','light','dark')),
    avatar_id uuid UNIQUE,
    avatar_sha256 text,
    avatar_bytes bytea,
    CHECK ((avatar_id IS NULL AND avatar_sha256 IS NULL AND avatar_bytes IS NULL) OR
        (avatar_id IS NOT NULL AND avatar_sha256 IS NOT NULL AND avatar_bytes IS NOT NULL AND
        avatar_sha256 ~ '^[0-9a-f]{64}$' AND octet_length(avatar_bytes) BETWEEN 1 AND 147456))
);

CREATE INDEX owners_identity ON access.owners(identity_id,tenant_id);
CREATE INDEX active_connections_by_tenant ON public.connections(tenant_id,created_at,id) WHERE revoked_at IS NULL;

-- Personal data belongs to the verified identity, independent of Book selection.
-- Hold an active owner membership as well as the session and identity epoch.
CREATE FUNCTION access.require_account_owner()
RETURNS TABLE(id uuid, issuer text, subject text)
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog AS $$
    SELECT i.id,i.issuer,i.subject FROM access.sessions s
    JOIN access.identities i ON i.id=s.identity_id AND i.epoch=s.epoch
    JOIN access.owners o ON o.identity_id=i.id
    WHERE s.token_hash=current_setting('vult.owner_session',true)
    AND s.origin=current_setting('vult.owner_origin',true)
    AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()
    ORDER BY o.tenant_id LIMIT 1 FOR SHARE OF s,i,o
$$;

REVOKE ALL ON access.user_settings FROM PUBLIC;
REVOKE ALL ON FUNCTION access.require_account_owner() FROM PUBLIC;
ALTER TABLE access.user_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE access.user_settings FORCE ROW LEVEL SECURITY;

CREATE POLICY user_settings_owner ON access.user_settings
    USING (identity_id = (SELECT id FROM access.require_account_owner()))
    WITH CHECK (identity_id = (SELECT id FROM access.require_account_owner()));

-- An archive contains the selected Book's tenant row. The browser has no grant
-- to enumerate installation tenants directly.
CREATE FUNCTION access.book_archive_tenant(t uuid) RETURNS SETOF public.tenants
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog AS $$
    SELECT * FROM public.tenants WHERE id=t AND access.owner_allowed(t)
$$;
REVOKE ALL ON FUNCTION access.book_archive_tenant(uuid) FROM PUBLIC;

CREATE FUNCTION public.book_slug_valid(slug text) RETURNS boolean
LANGUAGE sql IMMUTABLE SET search_path = pg_catalog AS $$
    SELECT length(slug) BETWEEN 2 AND 63 AND slug COLLATE "C" ~ '^[a-z0-9]+(-[a-z0-9]+)*$'
        AND slug COLLATE "C" !~ '^[abcdefghjklmnpqrstuvwxyz23456789]{4}$'
        AND slug NOT IN ('dashboard','accounts','activity','transactions','sources','documents','statements',
            'settings','books','approvals','api','assets','auth','oauth','login','logout','register','healthz','readyz',
            'admin','owner','identity','mcp','tests','public','help','docs')
$$;

CREATE FUNCTION public.book_aliases_valid(aliases text[]) RETURNS boolean
LANGUAGE sql IMMUTABLE SET search_path = pg_catalog AS $$
    SELECT cardinality(aliases) <= 256 AND cardinality(aliases) = count(DISTINCT slug)
        AND coalesce(bool_and(public.book_slug_valid(slug)),true)
    FROM unnest(aliases) slug
$$;

CREATE TABLE public.book_addresses (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    code text NOT NULL UNIQUE CHECK(code COLLATE "C" ~ '^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{4}$' AND code NOT IN ('AUTH','HELP')),
    slug text NOT NULL DEFAULT '',
    revision bigint NOT NULL DEFAULT 0 CHECK(revision BETWEEN 0 AND 9007199254740991),
    aliases text[] NOT NULL DEFAULT '{}',
    PRIMARY KEY(tenant_id,book_id),
    FOREIGN KEY(tenant_id,book_id) REFERENCES public.books(tenant_id,id),
    CHECK(public.book_aliases_valid(aliases)),
    CHECK(slug='' OR slug=ANY(aliases))
);
CREATE INDEX book_address_aliases ON public.book_addresses USING gin(aliases);

CREATE FUNCTION public.allocate_book_address(t uuid,b uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE
    alphabet constant text := 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789';
    entropy bytea;
    candidate text;
BEGIN
    FOR attempt IN 1..128 LOOP
        entropy := uuid_send(gen_random_uuid());
        candidate := substr(alphabet, get_byte(entropy,0)%32+1, 1) ||
            substr(alphabet, get_byte(entropy,1)%32+1, 1) ||
            substr(alphabet, get_byte(entropy,2)%32+1, 1) ||
            substr(alphabet, get_byte(entropy,3)%32+1, 1);
        IF candidate IN ('AUTH','HELP') THEN CONTINUE; END IF;
        INSERT INTO public.book_addresses(tenant_id,book_id,code) VALUES(t,b,candidate)
            ON CONFLICT(code) DO NOTHING;
        IF FOUND THEN RETURN; END IF;
    END LOOP;
    RAISE EXCEPTION 'Book address allocation unavailable';
END
$$;

CREATE FUNCTION public.book_address_created() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF current_setting('vult.restore_book_details',true) = 'yes' AND public.ledger_import_owner() THEN
        RETURN NULL;
    END IF;
    PERFORM public.allocate_book_address(NEW.tenant_id, NEW.id);
    RETURN NULL;
END
$$;

SELECT public.allocate_book_address(tenant_id,id) FROM public.books ORDER BY id;

CREATE TRIGGER book_address_created AFTER INSERT ON public.books
FOR EACH ROW EXECUTE FUNCTION public.book_address_created();

-- A browser catalog can span multiple tenants held by the same owner.
-- A different owner's aliases never participate in this availability check.
CREATE FUNCTION access.book_conflicting_aliases(t uuid,b uuid,candidates text[]) RETURNS text[]
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT ARRAY(
        SELECT DISTINCT candidate.slug FROM public.book_addresses a
        JOIN access.owners o ON o.tenant_id=a.tenant_id
        CROSS JOIN LATERAL unnest(a.aliases) AS candidate(slug)
        WHERE o.identity_id=access.owner_identity(t) AND a.book_id<>b
        AND a.aliases && candidates AND candidate.slug=ANY(candidates))
$$;

CREATE TABLE public.book_identity_revisions (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
    id uuid NOT NULL UNIQUE,
    prior_kind text CHECK(prior_kind IN ('person','household','legal_entity','trust','estate','fund','informal_activity','other')),
    new_kind text CHECK(new_kind IN ('person','household','legal_entity','trust','estate','fund','informal_activity','other')),
    prior_identity jsonb NOT NULL CHECK(jsonb_typeof(prior_identity)='object' AND octet_length(prior_identity::text)<=32768),
    new_identity jsonb NOT NULL CHECK(jsonb_typeof(new_identity)='object' AND octet_length(new_identity::text)<=32768),
    actor_id uuid NOT NULL,
    reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 1000),
    effective_date date CHECK(effective_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(tenant_id,book_id,revision),
    FOREIGN KEY(tenant_id,book_id) REFERENCES public.books(tenant_id,id)
);

CREATE TRIGGER book_identity_immutable BEFORE UPDATE OR DELETE ON public.book_identity_revisions
FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();

ALTER TABLE public.book_addresses ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.book_addresses FORCE ROW LEVEL SECURITY;
ALTER TABLE public.book_identity_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.book_identity_revisions FORCE ROW LEVEL SECURITY;

CREATE POLICY book_address_read ON public.book_addresses FOR SELECT USING (access.owner_allowed(tenant_id));
CREATE POLICY book_address_write ON public.book_addresses FOR UPDATE USING (access.owner_allowed(tenant_id))
    WITH CHECK (public.ledger_context(tenant_id,book_id) AND access.owner_write_allowed(tenant_id));
CREATE POLICY book_identity_read ON public.book_identity_revisions FOR SELECT USING (access.owner_allowed(tenant_id));
CREATE POLICY book_identity_write ON public.book_identity_revisions FOR INSERT WITH CHECK (
    public.ledger_context(tenant_id,book_id) AND access.owner_write_allowed(tenant_id)
    AND actor_id=access.owner_identity(tenant_id));

REVOKE ALL ON public.book_addresses,public.book_identity_revisions FROM PUBLIC;
REVOKE ALL ON FUNCTION public.allocate_book_address(uuid,uuid),public.book_address_created(),access.book_conflicting_aliases(uuid,uuid,text[]) FROM PUBLIC;

-- Resolve a browser-owned resource by its indexed identifier. The restricted
-- authentication role receives only the tenant of a matching owned row.
CREATE FUNCTION access.owner_resource_tenant(token text, realm text, resource_kind text, resource_id uuid)
RETURNS TABLE(tenant_id uuid, is_owner boolean)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT (
        SELECT o.tenant_id FROM access.owners o
        WHERE o.identity_id=i.id AND o.tenant_id=CASE resource_kind
            WHEN 'book' THEN (SELECT b.tenant_id FROM public.books b WHERE b.id=resource_id)
            WHEN 'connection' THEN (SELECT c.tenant_id FROM public.connections c WHERE c.id=resource_id)
        END
    ), EXISTS(SELECT 1 FROM access.owners o WHERE o.identity_id=i.id)
    FROM access.session_identity(token, realm) i
$$;

REVOKE ALL ON FUNCTION access.owner_resource_tenant(text,text,text,uuid) FROM PUBLIC;

CREATE INDEX closed_periods_by_book ON public.closed_periods(tenant_id,book_id,id);

CREATE TABLE access.book_view_preferences (
    identity_id uuid NOT NULL REFERENCES access.identities(id),
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    period text NOT NULL CHECK (period IN (
        'today', 'yesterday', 'last-7-days', 'last-30-days', 'last-90-days',
        'week', 'last-week', 'month', 'last-month', 'quarter', 'last-quarter',
        'year', 'last-year', 'year-to-date', 'last-12-months', 'all', 'custom'
    )),
    range_from date NOT NULL CHECK (range_from >= DATE '0001-01-01'),
    range_to date NOT NULL CHECK (range_to <= DATE '9999-12-31'),
    PRIMARY KEY (identity_id, book_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    CHECK (range_from <= range_to)
);

REVOKE ALL ON access.book_view_preferences FROM PUBLIC;
ALTER TABLE access.book_view_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE access.book_view_preferences FORCE ROW LEVEL SECURITY;

CREATE POLICY book_view_preference_owner ON access.book_view_preferences
    USING (identity_id = access.owner_identity(tenant_id));

-- Read the preferences for the visible catalog in one call. The left join
-- also returns the verified owner when they have no saved selections.
CREATE FUNCTION access.owner_book_views(token text, realm text, visible_books uuid[])
RETURNS TABLE(identity_id uuid, book_id uuid, period text, range_from date, range_to date)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT i.id, v.book_id, v.period, v.range_from, v.range_to
    FROM access.session_identity(token, realm) i
    LEFT JOIN LATERAL (
        SELECT p.book_id, p.period, p.range_from, p.range_to
        FROM access.book_view_preferences p
        JOIN access.owners o USING (tenant_id, identity_id)
        WHERE p.identity_id=i.id AND p.book_id=ANY(visible_books)
    ) v ON true
    WHERE EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id=i.id)
$$;

REVOKE ALL ON FUNCTION access.owner_book_views(text,text,uuid[]) FROM PUBLIC;

-- Prepared operations wait for the owner without a time limit, and the owner's
-- approval posts the exact prepared entry in the same transaction.
--
-- Restored proposals keep any recorded expiry. New proposals have none.
-- Correctness rests on the dependency check that runs
-- when the owner approves. An approval no longer names an executor connection;
-- the owner's fresh sign-in authorizes one posting of that exact proposal. The
-- entry's receipt keeps the preparing agent as its author, so the agent's
-- financial request key still resolves to the posted entry, and the
-- consumption records the approving owner as the one who posted it.

-- True only while the owner posts the exact proposal named in this transaction:
-- a fresh owner sign-in, the preparing agent's actor and connection selected,
-- and that owner's approval of the proposal recorded in this same transaction.
CREATE FUNCTION access.owner_posting_allowed(t uuid, b uuid) RETURNS boolean
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT has_function_privilege(session_user, 'access.owner_posting_allowed(uuid,uuid)'::regprocedure, 'EXECUTE')
    AND public.ledger_context(t, b) AND access.owner_write_allowed(t) AND EXISTS (
        SELECT 1 FROM public.operation_proposals p
        JOIN LATERAL (SELECT d.decision, d.executor_actor_id, d.owner_id, d.xmin::text::bigint AS created_in
            FROM public.operation_decisions d
            WHERE d.tenant_id = p.tenant_id AND d.book_id = p.book_id AND d.proposal_id = p.id
            ORDER BY d.revision DESC LIMIT 1) d ON true
        WHERE p.tenant_id = t AND p.book_id = b
            AND p.id = nullif(current_setting('vult.owner_posting', true), '')::uuid
            AND p.actor_id::text = coalesce(current_setting('vult.actor_id', true), '')
            AND p.connection_id::text = coalesce(current_setting('vult.connection_id', true), '')
            AND d.decision = 'approve' AND d.executor_actor_id IS NULL
            AND d.owner_id = access.owner_identity(t)
            AND d.created_in = mod(pg_current_xact_id()::text::numeric, 4294967296)::bigint
            AND NOT EXISTS (SELECT 1 FROM public.operation_consumptions c
                WHERE c.tenant_id = p.tenant_id AND c.book_id = p.book_id AND c.proposal_id = p.id
                AND c.xmin::text::bigint <> mod(pg_current_xact_id()::text::numeric, 4294967296)::bigint))
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
        AND (NOT writing OR c.access = 'write')
        AND (NOT EXISTS (SELECT 1 FROM access.oauth_grants g WHERE g.connection_id = c.id)
            OR access.agent_allowed(c.id, writing)))
ELSE access.owner_posting_allowed(t, b) END
$$;

-- The ordinary scoped policy checks the live approval. This additional policy
-- binds every owner-written receipt to its approved operation and key, even if
-- the caller forces deferred checks early. The receipt key and entry reference
-- are unique, so the owner cannot create a second effect under the same approval.
CREATE POLICY operation_owner_exact_request ON public.requests AS RESTRICTIVE FOR INSERT WITH CHECK (
    coalesce(current_setting('vult.owner_posting', true), '') = '' OR EXISTS (
        SELECT 1 FROM public.operation_proposals p
        WHERE p.tenant_id = requests.tenant_id AND p.book_id = requests.book_id
            AND p.id = nullif(current_setting('vult.owner_posting', true), '')::uuid
            AND p.actor_id = requests.actor_id AND p.operation = requests.operation
            AND p.operation_key = requests.request_key
            AND convert_from(requests.request_bytes, 'UTF8')::jsonb IS NOT DISTINCT FROM
                convert_from(p.payload_bytes, 'UTF8')::jsonb -> 'arguments'));

-- Evaluated for every consumption insert, by agents too, so it answers false
-- without further checks unless an owner posting is in progress.
CREATE FUNCTION access.owner_posting_consumer(t uuid, b uuid, who uuid) RETURNS boolean
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT CASE WHEN coalesce(current_setting('vult.owner_posting', true), '') = '' THEN false
        ELSE who = access.owner_identity(t) AND access.owner_posting_allowed(t, b) END
$$;

CREATE POLICY operation_owner_consume ON public.operation_consumptions FOR INSERT WITH CHECK (
    access.owner_posting_consumer(tenant_id, book_id, actor_id));

REVOKE ALL ON FUNCTION access.owner_posting_allowed(uuid, uuid), access.owner_posting_consumer(uuid, uuid, uuid) FROM PUBLIC;

-- Owner document retention and upload authority.
-- A ready original retained by an owner has no expiry. Pending uploads
-- always keep a finite storage lease. Earlier document rows need no rewrite.
ALTER TABLE public.documents
    ALTER COLUMN expires_at DROP NOT NULL,
    ADD CONSTRAINT document_expiry_required CHECK (expires_at IS NOT NULL OR state = 'ready'),
    DROP CONSTRAINT documents_original_name_check,
    ADD CONSTRAINT documents_original_name_check CHECK (
        (octet_length(original_name) BETWEEN 1 AND 255 OR char_length(original_name) BETWEEN 1 AND 180)
        AND original_name !~ '[[:cntrl:]/\\]');
ALTER TABLE public.document_uploads ALTER COLUMN connection_id DROP NOT NULL;

DROP INDEX public.documents_expiry;
CREATE INDEX documents_expiry ON public.documents(tenant_id, book_id, expires_at, id)
    WHERE used_at IS NULL AND state <> 'expired' AND expires_at IS NOT NULL;

-- The owner lease grants only original intake, completion and the Book's
-- write lock. The actor is the authenticated identity, with no connection.
CREATE FUNCTION access.owner_document_allowed(t uuid, b uuid) RETURNS boolean
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT has_function_privilege(session_user, 'access.owner_identity(uuid)'::regprocedure, 'EXECUTE')
    AND current_setting('transaction_isolation') = 'read committed'
    AND public.ledger_context(t, b)
    AND coalesce(current_setting('vult.connection_id', true), '') = ''
    AND coalesce(current_setting('vult.owner_posting', true), '') = ''
    AND coalesce(access.owner_identity(t)::text = current_setting('vult.actor_id', true), false)
$$;
REVOKE ALL ON FUNCTION access.owner_document_allowed(uuid,uuid) FROM PUBLIC;

CREATE POLICY owner_document_lock ON public.books FOR UPDATE
    USING (access.owner_document_allowed(tenant_id,id))
    WITH CHECK (access.owner_document_allowed(tenant_id,id));
CREATE POLICY owner_document_insert ON public.documents FOR INSERT
    WITH CHECK (access.owner_document_allowed(tenant_id,book_id));
CREATE POLICY owner_document_update ON public.documents FOR UPDATE
    USING (access.owner_document_allowed(tenant_id,book_id))
    WITH CHECK (access.owner_document_allowed(tenant_id,book_id));
CREATE POLICY owner_document_upload_insert ON public.document_uploads FOR INSERT
    WITH CHECK (connection_id IS NULL AND access.owner_document_allowed(tenant_id,book_id));
CREATE POLICY owner_document_request ON public.requests FOR INSERT
    WITH CHECK (operation IN ('add_document', 'complete_document_upload')
        AND access.owner_document_allowed(tenant_id,book_id));

-- Approval can link an original through the existing source trigger. Its
-- scoped posting authority cannot create a new original or upload lease.
CREATE POLICY document_insert_outside_posting ON public.documents AS RESTRICTIVE FOR INSERT
    WITH CHECK (coalesce(current_setting('vult.owner_posting', true), '') = '');
CREATE POLICY document_upload_insert_outside_posting ON public.document_uploads AS RESTRICTIVE FOR INSERT
    WITH CHECK (coalesce(current_setting('vult.owner_posting', true), '') = '');

CREATE OR REPLACE FUNCTION public.document_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'document identity is retained' USING ERRCODE = '23514';
    END IF;
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF TG_OP = 'INSERT' THEN
        IF NOT public.ledger_import_owner() OR access.owner_document_allowed(NEW.tenant_id, NEW.book_id) THEN
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
    IF coalesce(current_setting('vult.owner_posting', true), '') <> ''
        AND ROW(NEW.state, NEW.storage_id, NEW.key_id, NEW.ready_at, NEW.expires_at)
        IS DISTINCT FROM ROW(OLD.state, OLD.storage_id, OLD.key_id, OLD.ready_at, OLD.expires_at) THEN
        RAISE EXCEPTION 'approval cannot change original storage' USING ERRCODE = '23514';
    END IF;
    IF OLD.state = 'pending' AND NEW.state = 'ready' THEN
        IF OLD.expires_at <= clock_timestamp() OR NEW.storage_id <> OLD.storage_id OR NEW.key_id <> OLD.key_id THEN
            RAISE EXCEPTION 'upload expired' USING ERRCODE = '23514';
        END IF;
        NEW.ready_at := clock_timestamp();
        IF NEW.expires_at IS NOT NULL OR NOT access.owner_document_allowed(NEW.tenant_id, NEW.book_id) THEN
            NEW.expires_at := NEW.ready_at + interval '30 days';
        END IF;
    ELSIF OLD.state = 'ready' AND NEW.state = 'ready' AND OLD.expires_at IS NOT NULL
        AND NEW.expires_at IS NULL AND access.owner_document_allowed(NEW.tenant_id, NEW.book_id) THEN
        IF (OLD.used_at IS NULL AND OLD.expires_at <= clock_timestamp())
            OR NEW.storage_id <> OLD.storage_id OR NEW.key_id <> OLD.key_id
            OR NEW.ready_at IS DISTINCT FROM OLD.ready_at THEN
            RAISE EXCEPTION 'live original required' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.state = 'expired' AND OLD.state IN ('pending', 'ready') THEN
        IF OLD.used_at IS NOT NULL OR OLD.expires_at IS NULL OR OLD.expires_at > clock_timestamp()
            OR NEW.storage_id <> OLD.storage_id OR NEW.key_id <> OLD.key_id
            OR NEW.ready_at IS DISTINCT FROM OLD.ready_at OR NEW.expires_at IS DISTINCT FROM OLD.expires_at THEN
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

CREATE OR REPLACE FUNCTION public.document_upload_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE
    deadline timestamptz;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF public.ledger_import_owner() AND NOT access.owner_document_allowed(NEW.tenant_id, NEW.book_id) THEN
        RETURN NEW;
    END IF;
    IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated uploader required' USING ERRCODE = '42501';
    END IF;
    NEW.created_at := clock_timestamp();
    IF NEW.connection_id IS NULL THEN
        IF NOT access.owner_document_allowed(NEW.tenant_id, NEW.book_id) THEN
            RAISE EXCEPTION 'owner upload required' USING ERRCODE = '42501';
        END IF;
        SELECT CASE WHEN state = 'pending' THEN expires_at END INTO STRICT deadline FROM public.documents
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.document_id
            AND storage_id = NEW.storage_id AND state IN ('pending', 'ready');
    ELSE
        IF NEW.connection_id::text IS DISTINCT FROM current_setting('vult.connection_id', true) THEN
            RAISE EXCEPTION 'authenticated uploader required' USING ERRCODE = '42501';
        END IF;
        SELECT expires_at INTO STRICT deadline FROM public.documents
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.document_id
            AND storage_id = NEW.storage_id AND state = 'pending';
    END IF;
    NEW.expires_at := least(deadline, NEW.created_at + interval '15 minutes');
    RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION public.document_completion_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.state <> 'ready' THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM public.document_uploads u
        JOIN public.requests r ON r.tenant_id = u.tenant_id AND r.book_id = u.book_id
            AND r.actor_id = u.actor_id AND r.request_key = 'upload:' || u.id::text
        CROSS JOIN LATERAL (SELECT convert_from(r.request_bytes, 'UTF8')::jsonb AS value) request
        CROSS JOIN LATERAL (SELECT convert_from(r.result_bytes, 'UTF8')::jsonb AS value) result
        WHERE u.tenant_id = NEW.tenant_id AND u.book_id = NEW.book_id AND u.document_id = NEW.id
        AND u.storage_id = NEW.storage_id AND r.operation = 'complete_document_upload'
        AND request.value->>'upload_id' = u.id::text
        AND (NEW.expires_at IS NOT NULL OR u.connection_id IS NULL AND NOT result.value ? 'expires_at')
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

CREATE OR REPLACE FUNCTION public.document_use(t uuid, b uuid, d uuid) RETURNS void
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.documents WHERE tenant_id = t AND book_id = b
        AND id = d AND state = 'ready' AND used_at IS NOT NULL) THEN
        RETURN;
    END IF;
    UPDATE public.documents SET used_at = clock_timestamp()
        WHERE tenant_id = t AND book_id = b AND id = d AND state = 'ready'
        AND used_at IS NULL AND (expires_at IS NULL OR expires_at > clock_timestamp());
    IF NOT FOUND THEN
        RAISE EXCEPTION 'verified original required' USING ERRCODE = '23514';
    END IF;
END
$$;

-- A bounded report population is selected under one live owner lease.
-- The explicit tenant and Book predicates apply even for the offline operator.
CREATE FUNCTION access.report_postings(t uuid, b uuid, through_date date, cutoff timestamptz,
    window_from date, window_to date, comparison_from date, comparison_to date)
RETURNS TABLE(line_id uuid, entry_id uuid, account_id uuid, accounting_date date, side text,
    amount_minor bigint, native_amount_minor bigint, entry_sequence bigint, line_position integer)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE operator_session boolean := public.ledger_import_owner();
BEGIN
    IF public.ledger_context(t, b) IS NOT TRUE THEN
        RAISE EXCEPTION 'selected Book required' USING ERRCODE = '42501';
    END IF;
    IF NOT operator_session THEN
        PERFORM access.require_owner(t, false);
    END IF;
    RETURN QUERY
        SELECT l.id, e.id, l.account_id, e.accounting_date, l.side, l.amount_minor,
            l.native_amount_minor, e.sequence, l.position
        FROM public.entries e JOIN public.lines l
            ON l.tenant_id = e.tenant_id AND l.book_id = e.book_id AND l.entry_id = e.id
        WHERE e.tenant_id = t AND e.book_id = b AND e.accounting_date <= through_date
            AND e.recorded_at <= cutoff
            AND (window_from IS NULL OR e.accounting_date BETWEEN window_from AND window_to
                OR e.accounting_date BETWEEN comparison_from AND comparison_to)
        LIMIT 100001;
    -- Recheck expiry before returning the materialized rows to the caller.
    IF NOT operator_session THEN
        PERFORM access.require_owner(t, false);
    END IF;
END
$$;
REVOKE ALL ON FUNCTION access.report_postings(uuid, uuid, date, timestamptz, date, date, date, date) FROM PUBLIC;

-- An authenticator app as the local owner's alternative or addition to a
-- passkey. The secret is sealed by the browser service; the database only
-- stores ciphertext. A secret belongs to one identity epoch, so a reset or a
-- recovery that advances the epoch retires it without a separate step.

CREATE TABLE access.local_authenticators (
    identity_id uuid PRIMARY KEY REFERENCES access.passkey_profiles(identity_id),
    key_id text NOT NULL CHECK (key_id ~ '^[a-f0-9]{64}$'),
    secret_cipher bytea CHECK (octet_length(secret_cipher) BETWEEN 1 AND 256),
    secret_epoch bigint,
    last_step bigint NOT NULL DEFAULT -1,
    enrolled_at timestamptz,
    pending_cipher bytea CHECK (octet_length(pending_cipher) BETWEEN 1 AND 256),
    pending_epoch bigint,
    pending_browser text CHECK (pending_browser ~ '^[a-f0-9]{64}$'),
    pending_setup text REFERENCES access.passkey_setups(token_hash),
    pending_session text CHECK (pending_session ~ '^[a-f0-9]{64}$'),
    pending_expires_at timestamptz,
    failed_count integer NOT NULL DEFAULT 0 CHECK (failed_count BETWEEN 0 AND 8),
    window_expires_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((secret_cipher IS NULL) = (secret_epoch IS NULL)),
    CHECK ((pending_cipher IS NULL) = (pending_epoch IS NULL)),
    CHECK (pending_cipher IS NULL OR (pending_browser IS NOT NULL AND pending_expires_at IS NOT NULL
        AND (pending_setup IS NULL) <> (pending_session IS NULL)))
);
ALTER TABLE access.local_authenticators ENABLE ROW LEVEL SECURITY;
ALTER TABLE access.local_authenticators FORCE ROW LEVEL SECURITY;
REVOKE ALL ON access.local_authenticators FROM PUBLIC;

-- local_authenticator_ready reports whether the owner can sign in with an
-- authenticator sealed by this key.
CREATE FUNCTION access.local_authenticator_ready(sub text, realm text, key text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT EXISTS (SELECT 1 FROM access.identities i
        JOIN access.passkey_profiles p ON p.identity_id = i.id
        JOIN access.local_authenticators a ON a.identity_id = i.id
        WHERE i.issuer = 'urn:vult:builtin' AND i.subject = sub AND p.origin = realm AND p.state = 'ready'
            AND a.key_id = key AND a.secret_epoch = i.epoch
            AND EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = i.id));
$$;

-- begin_local_authenticator stores a new secret for confirmation. It needs the
-- same unused setup authority as a first passkey, or a session signed in within
-- the last five minutes when the owner adds an authenticator to a passkey.
CREATE FUNCTION access.begin_local_authenticator(sub text, realm text, setup text, session text,
    browser text, sealed bytea, key text) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; generation bigint; profile access.passkey_profiles%ROWTYPE;
    current access.local_authenticators%ROWTYPE;
BEGIN
    SELECT i.id, i.epoch INTO who, generation FROM access.identities i
    WHERE i.issuer = 'urn:vult:builtin' AND i.subject = sub
        AND EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = i.id) FOR UPDATE OF i;
    SELECT * INTO profile FROM access.passkey_profiles WHERE identity_id = who AND origin = realm FOR UPDATE;
    IF who IS NULL OR profile.identity_id IS NULL OR browser !~ '^[a-f0-9]{64}$' OR key !~ '^[a-f0-9]{64}$'
        OR sealed IS NULL OR (setup IS NULL) = (session IS NULL) THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    IF setup IS NOT NULL THEN
        IF profile.state <> 'pending'
            OR EXISTS (SELECT 1 FROM access.passkey_credentials WHERE identity_id = who AND revoked_at IS NULL)
            OR NOT EXISTS (SELECT 1 FROM access.passkey_setups s WHERE s.token_hash = setup AND s.identity_id = who
                AND s.epoch = generation AND s.consumed_at IS NULL AND s.expires_at > clock_timestamp()
                AND (s.browser_hash IS NULL OR s.browser_hash = browser)) THEN
            RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
    ELSIF profile.state <> 'ready' OR NOT EXISTS (SELECT 1 FROM access.sessions s WHERE s.token_hash = session
        AND s.identity_id = who AND s.epoch = generation AND s.origin = realm AND s.revoked_at IS NULL
        AND s.expires_at > clock_timestamp() AND s.authenticated_at > clock_timestamp() - interval '5 minutes') THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    SELECT * INTO current FROM access.local_authenticators WHERE identity_id = who FOR UPDATE;
    IF current.identity_id IS NOT NULL AND current.failed_count >= 8 AND current.window_expires_at > clock_timestamp() THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    INSERT INTO access.local_authenticators(identity_id, key_id, pending_cipher, pending_epoch, pending_browser,
        pending_setup, pending_session, pending_expires_at)
    VALUES (who, key, sealed, generation, browser, setup, session, clock_timestamp() + interval '10 minutes')
    ON CONFLICT (identity_id) DO UPDATE SET pending_cipher = excluded.pending_cipher,
        pending_epoch = excluded.pending_epoch, pending_browser = excluded.pending_browser,
        pending_setup = excluded.pending_setup, pending_session = excluded.pending_session,
        pending_expires_at = excluded.pending_expires_at;
END $$;

-- reserve_local_authenticator spends one attempt from the owner's budget of
-- eight per five minutes before any code is checked, and returns the sealed
-- secret to check against. The caller commits this before checking.
CREATE FUNCTION access.reserve_local_authenticator(sub text, realm text, purpose text, browser text)
RETURNS TABLE(allowed boolean, identity_id uuid, epoch bigint, sealed bytea, key_id text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; generation bigint; current access.local_authenticators%ROWTYPE;
BEGIN
    SELECT i.id, i.epoch INTO who, generation FROM access.identities i
    JOIN access.passkey_profiles p ON p.identity_id = i.id
    WHERE i.issuer = 'urn:vult:builtin' AND i.subject = sub AND p.origin = realm
        AND EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = i.id);
    SELECT * INTO current FROM access.local_authenticators a WHERE a.identity_id = who FOR UPDATE;
    IF who IS NULL OR current.identity_id IS NULL OR purpose NOT IN ('enroll', 'login') THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    IF current.failed_count >= 8 AND current.window_expires_at > clock_timestamp() THEN
        RETURN QUERY SELECT false, NULL::uuid, NULL::bigint, NULL::bytea, NULL::text;
        RETURN;
    END IF;
    IF purpose = 'enroll' AND (current.pending_cipher IS NULL OR current.pending_epoch <> generation
        OR current.pending_browser IS DISTINCT FROM browser OR current.pending_expires_at <= clock_timestamp())
        OR purpose = 'login' AND (current.secret_cipher IS NULL OR current.secret_epoch <> generation) THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    UPDATE access.local_authenticators a SET
        failed_count = CASE WHEN a.window_expires_at <= clock_timestamp() THEN 1 ELSE a.failed_count + 1 END,
        window_expires_at = CASE WHEN a.window_expires_at <= clock_timestamp() THEN clock_timestamp() + interval '5 minutes' ELSE a.window_expires_at END
    WHERE a.identity_id = who;
    RETURN QUERY SELECT true, who, generation,
        CASE WHEN purpose = 'enroll' THEN current.pending_cipher ELSE current.secret_cipher END, current.key_id;
END $$;

-- finish_local_authenticator accepts a verified code's time step once, then
-- issues a session whose proof time is now.
CREATE FUNCTION access.finish_local_authenticator(who uuid, realm text, purpose text, generation bigint,
    browser text, sealed bytea, step bigint, session_token text) RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE owner_record access.identities%ROWTYPE; current access.local_authenticators%ROWTYPE;
BEGIN
    SELECT * INTO owner_record FROM access.identities i WHERE i.id = who AND i.epoch = generation
        AND i.issuer = 'urn:vult:builtin' FOR UPDATE;
    SELECT * INTO current FROM access.local_authenticators a WHERE a.identity_id = who FOR UPDATE;
    IF owner_record.id IS NULL OR current.identity_id IS NULL OR step < 0
        OR NOT EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = who)
        OR NOT EXISTS (SELECT 1 FROM access.passkey_profiles p WHERE p.identity_id = who AND p.origin = realm) THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    IF purpose = 'enroll' THEN
        IF current.pending_cipher IS DISTINCT FROM sealed OR current.pending_epoch <> generation
            OR current.pending_browser IS DISTINCT FROM browser OR current.pending_expires_at <= clock_timestamp() THEN
            RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        IF current.pending_setup IS NOT NULL THEN
            UPDATE access.passkey_setups s SET consumed_at = clock_timestamp()
            WHERE s.token_hash = current.pending_setup AND s.identity_id = who AND s.epoch = generation
                AND s.consumed_at IS NULL AND s.expires_at > clock_timestamp();
            IF NOT FOUND OR EXISTS (SELECT 1 FROM access.passkey_credentials c WHERE c.identity_id = who AND c.revoked_at IS NULL)
                OR NOT EXISTS (SELECT 1 FROM access.passkey_profiles p WHERE p.identity_id = who AND p.state = 'pending') THEN
                RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
            END IF;
            UPDATE access.passkey_profiles SET state = 'ready' WHERE identity_id = who;
        ELSIF NOT EXISTS (SELECT 1 FROM access.sessions s WHERE s.token_hash = current.pending_session
            AND s.identity_id = who AND s.epoch = generation AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp()) THEN
            RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        UPDATE access.local_authenticators SET secret_cipher = sealed, secret_epoch = generation, last_step = step,
            enrolled_at = clock_timestamp(), pending_cipher = NULL, pending_epoch = NULL, pending_browser = NULL,
            pending_setup = NULL, pending_session = NULL, pending_expires_at = NULL,
            failed_count = 0, window_expires_at = clock_timestamp()
        WHERE identity_id = who;
    ELSIF purpose = 'login' THEN
        IF current.secret_cipher IS DISTINCT FROM sealed OR current.secret_epoch <> generation OR step <= current.last_step
            OR NOT EXISTS (SELECT 1 FROM access.passkey_profiles p WHERE p.identity_id = who AND p.state = 'ready') THEN
            RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
        END IF;
        UPDATE access.local_authenticators SET last_step = step, failed_count = 0, window_expires_at = clock_timestamp()
        WHERE identity_id = who;
    ELSE
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    PERFORM access.issue_session(owner_record.issuer, owner_record.subject, realm, session_token, clock_timestamp());
    RETURN who;
END $$;

REVOKE ALL ON FUNCTION access.local_authenticator_ready(text,text,text),
    access.begin_local_authenticator(text,text,text,text,text,bytea,text),
    access.reserve_local_authenticator(text,text,text,text),
    access.finish_local_authenticator(uuid,text,text,bigint,text,bytea,bigint,text) FROM PUBLIC;
