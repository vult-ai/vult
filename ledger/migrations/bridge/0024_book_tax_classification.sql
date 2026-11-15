-- How a Book is taxed: a sole owner (a sole proprietor or a single-member
-- LLC taxed as a disregarded entity), a partnership, an S corporation, a C
-- corporation, or the owner's own personal Book. Repayments between linked
-- Books follow it, never the legal form. A Book with no choice yet takes
-- the default from its profile.
ALTER TABLE public.book_revisions ADD COLUMN taxed_as text CHECK (taxed_as IS NULL OR
    taxed_as IN ('sole_owner', 'partnership', 's_corporation', 'c_corporation', 'personal')
    AND profile IN ('personal', 'business') AND (profile = 'personal') = (taxed_as = 'personal'));

CREATE FUNCTION public.book_taxed_as_default(profile text) RETURNS text
    LANGUAGE sql IMMUTABLE SET search_path = pg_catalog AS $$
SELECT CASE profile WHEN 'personal' THEN 'personal' WHEN 'business' THEN 'sole_owner' END
$$;

-- How Book b is taxed: the owner's choice, or the default from its profile.
CREATE FUNCTION public.book_taxed_as(t uuid, b uuid) RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT coalesce(r.taxed_as, public.book_taxed_as_default(r.profile)) FROM public.book_revisions r
WHERE r.tenant_id = t AND r.book_id = b ORDER BY r.revision DESC LIMIT 1
$$;
REVOKE ALL ON FUNCTION public.book_taxed_as(uuid, uuid) FROM PUBLIC;

-- Book settings accept how the Book is taxed, and say it in the outcome.
CREATE OR REPLACE FUNCTION public.ledger_book_revision() RETURNS trigger
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
        -- A new logo keeps how the Book is taxed.
        NEW.taxed_as := prior.taxed_as;
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
        '{"book_id":"string","idempotency_key":"string","revision":"number","name":"string","founder":"string","profile":"string","start_date":"string","fiscal_year_start_month":"number","fiscal_year_start_day":"number","accounting_timezone":"string","taxed_as":"string"}'::jsonb)
        OR requested->>'book_id' IS DISTINCT FROM NEW.book_id::text
        OR (requested ? 'taxed_as' AND nullif(requested->>'taxed_as','') IS DISTINCT FROM NEW.taxed_as)
        -- Unchanged unless asked, or cleared when the profile changes under it.
        OR (NOT requested ? 'taxed_as' AND NEW.taxed_as IS DISTINCT FROM prior.taxed_as
            AND NOT (NEW.taxed_as IS NULL AND NEW.profile IS DISTINCT FROM prior.profile))
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
        '{"tenant_id":"string","id":"string","name":"string","currency":"string","scale":"number","created_at":"string","metadata_revision":"number","founder":"string","profile":"string","start_date":"string","fiscal_year_start_month":"number","fiscal_year_start_day":"number","accounting_timezone":"string","logo":"object","taxed_as":"string","taxed_as_set":"boolean"}'::jsonb)
        -- Outcomes saved before Books were taxed say nothing of it.
        OR (result ? 'taxed_as_set' AND (result->>'taxed_as' IS DISTINCT FROM coalesce(NEW.taxed_as, public.book_taxed_as_default(NEW.profile))
            OR (result->>'taxed_as_set')::boolean IS DISTINCT FROM (NEW.taxed_as IS NOT NULL)))
        OR (NOT result ? 'taxed_as_set' AND NEW.taxed_as IS NOT NULL)
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
