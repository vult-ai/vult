-- Recording or proposing a bank item no longer reads every item in the Book.
--
-- Two lookups read all of a Book's bank activity each time they ran, so
-- recording and proposing slowed as a Book grew. Both now go by index, and
-- check exactly what they checked before.
--
-- 1. The repeat check.
--
-- Each new link checks that its item isn't the same money as one already
-- recorded: an item it may repeat, or one that may repeat it. The second
-- half had no index, so every recorded item read all of the Book's bank
-- activity, and recording slowed as a Book grew. The check is the same;
-- it now finds both halves by index.

CREATE INDEX bank_transactions_repeat_of ON public.bank_transactions(tenant_id, book_id, possible_repeat_of)
    WHERE possible_repeat_of IS NOT NULL;

CREATE OR REPLACE FUNCTION public.bank_link_once() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE other record;
BEGIN
    IF NEW.revoked_at IS NOT NULL OR public.ledger_restoring(NEW.tenant_id, NEW.book_id) THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM public.bank_transaction_duplicates d WHERE d.tenant_id = NEW.tenant_id AND d.book_id = NEW.book_id AND d.transaction_id = NEW.transaction_id) THEN
        RAISE EXCEPTION 'this item repeats one already here; record that one, and this one needs nothing' USING ERRCODE = '23514';
    END IF;
    SELECT y.occurred_on INTO other FROM (
        -- The item this one may repeat.
        SELECT o.id, o.occurred_on FROM public.bank_transactions x JOIN public.bank_transactions o
            ON o.tenant_id = x.tenant_id AND o.book_id = x.book_id AND o.id = x.possible_repeat_of
            WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.book_id AND x.id = NEW.transaction_id
        UNION ALL
        -- Items that may repeat this one.
        SELECT r.id, r.occurred_on FROM public.bank_transactions r
            WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id AND r.possible_repeat_of = NEW.transaction_id
    ) y
        WHERE y.id <> NEW.transaction_id
        AND EXISTS (SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id
            AND l.transaction_id = y.id AND l.revoked_at IS NULL)
        LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'this may be the same money as an item already recorded on %; if it is, mark it as a repeat', other.occurred_on USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;

-- 2. What a bank operation rests on. Its items were found by comparing
-- every item's id as text; they are now found by id.

CREATE OR REPLACE FUNCTION public.operation_dependencies(t uuid, b uuid, op text, args jsonb, excluded uuid)
RETURNS jsonb LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE lines jsonb; activity_day text; linked boolean;
BEGIN
    IF op IN ('record_bank_activity', 'record_bank_transfer') THEN
        IF NOT public.ledger_context(t, b) THEN
            RAISE EXCEPTION 'selected Book required' USING ERRCODE = '42501';
        END IF;
        -- The entry it posts rests on what any posting rests on: its
        -- accounts, its originals and its date's period. A transfer's lines
        -- are the two linked Book accounts; dates default to the activity's.
        -- A statement line has no bank account; its Book account is its own.
        SELECT jsonb_agg(jsonb_build_object('account_id', coalesce(a.account_id, t2.account_id)::text) ORDER BY t2.id), max(t2.occurred_on)::text,
                count(coalesce(a.account_id, t2.account_id)) = count(*)
            INTO lines, activity_day, linked
            FROM public.bank_transactions t2 LEFT JOIN public.bank_accounts a
            ON a.tenant_id = t2.tenant_id AND a.book_id = t2.book_id AND a.id = t2.bank_account_id
            WHERE t2.tenant_id = t AND t2.book_id = b AND t2.id = ANY (ARRAY(
                SELECT (value #>> '{}')::uuid FROM jsonb_array_elements(CASE WHEN op = 'record_bank_transfer'
                    THEN args -> 'transaction_ids' ELSE jsonb_build_array(args -> 'transaction_id') END)
                WHERE value #>> '{}' ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'));
        IF activity_day IS NULL OR NOT linked THEN
            -- Nothing to rest on yet; the operation itself refuses with a reason.
            RETURN jsonb_build_object('accounts', '[]'::jsonb, 'documents', '[]'::jsonb, 'closed_periods', '[]'::jsonb, 'reversed', false);
        END IF;
        IF op = 'record_bank_activity' THEN
            -- With other_account_id, Vult writes the two lines: the item's
            -- own account and that one.
            lines := coalesce(args -> 'lines', lines || jsonb_build_array(jsonb_build_object('account_id', args ->> 'other_account_id')));
        END IF;
        RETURN public.operation_dependencies_journal(t, b, 'post_journal', jsonb_build_object(
            'lines', lines, 'sources', coalesce(args -> 'sources', '[]'::jsonb),
            'accounting_date', coalesce(args ->> 'accounting_date', activity_day)), excluded);
    ELSIF op IN ('link_bank_activity', 'unlink_bank_activity', 'link_bank_account', 'disconnect_bank', 'pause_bank_connection',
            'resume_bank_connection', 'mark_bank_duplicate') THEN
        IF NOT public.ledger_context(t, b) THEN
            RAISE EXCEPTION 'selected Book required' USING ERRCODE = '42501';
        END IF;
        -- The link guards recheck every rule when it runs.
        RETURN jsonb_build_object('accounts', '[]'::jsonb, 'documents', '[]'::jsonb, 'closed_periods', '[]'::jsonb, 'reversed', false);
    END IF;
    RETURN public.operation_dependencies_journal(t, b, op, args, excluded);
END $$;
