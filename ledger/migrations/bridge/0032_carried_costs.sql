-- A project turned into a company carries its costs there: for each cost,
-- the Book that paid moves it from its expense to what the company owes
-- it, and the company records it as a start-up cost it owes back. The two
-- entries are a carried pair, repaid like any cost, oldest first.
ALTER TABLE public.entry_pairs DROP CONSTRAINT entry_pairs_kind_check;
ALTER TABLE public.entry_pairs ADD CONSTRAINT entry_pairs_kind_check CHECK (kind IN ('cost', 'settlement', 'opening', 'carried'));

CREATE OR REPLACE FUNCTION public.entry_pair_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE terms public.book_link_terms%ROWTYPE; item public.bank_transactions%ROWTYPE; bank_account uuid; tid uuid;
    paid text; amount bigint; n int; bad int;
BEGIN
    IF NOT (access.owner_allowed(NEW.tenant_id) AND access.owner_write_allowed(NEW.tenant_id)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    IF NEW.kind IN ('settlement', 'opening', 'carried') THEN
        -- Settlements, opening balances and carried costs are checked and
        -- recorded by settle_pair, open_link_balance and carry_cost only.
        IF current_setting('vult.settling', true) IS DISTINCT FROM NEW.pair_key THEN
            RAISE EXCEPTION 'a settlement or an opening balance is recorded with its own function' USING ERRCODE = '42501';
        END IF;
        RETURN NEW;
    END IF;
    terms := public.active_link_terms(NEW.tenant_id, NEW.payer_book_id, NEW.beneficiary_book_id);
    IF terms.link_id IS NULL THEN
        RAISE EXCEPTION 'This link is turned off; the pair can only be rejected.' USING ERRCODE = 'VP020';
    END IF;
    SELECT count(*), min(l.account_id::text)::uuid, min(l.transaction_id::text)::uuid INTO n, bank_account, tid
        FROM public.bank_transaction_links l
        WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.payer_book_id AND l.entry_id = NEW.payer_entry_id AND l.revoked_at IS NULL;
    SELECT * INTO item FROM public.bank_transactions t WHERE t.tenant_id = NEW.tenant_id AND t.book_id = NEW.payer_book_id AND t.id = tid;
    IF n <> 1 OR item.id IS NULL THEN
        RAISE EXCEPTION 'A cost pair''s first entry records one bank item in the Book that paid.' USING ERRCODE = 'VP020';
    END IF;
    paid := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = NEW.tenant_id AND a.book_id = NEW.payer_book_id AND a.id = bank_account);
    IF NOT (paid = 'liability' AND item.amount_minor > 0 OR paid = 'asset' AND item.amount_minor < 0) THEN
        RAISE EXCEPTION 'A cost pair records money paid out, such as a charge on a card.' USING ERRCODE = 'VP020';
    END IF;
    amount := abs(item.amount_minor);
    IF (SELECT k.currency FROM public.books k WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.beneficiary_book_id) IS DISTINCT FROM item.currency THEN
        RAISE EXCEPTION 'The bank item is in another currency than the Book it was paid for.' USING ERRCODE = 'VP020';
    END IF;
    -- The payer's entry: the bank account against the term account, whole.
    SELECT count(*) FILTER (WHERE NOT (x.account_id = bank_account OR x.account_id = terms.payer_account_id AND x.side = 'debit' AND x.amount_minor = amount)),
           count(*) INTO bad, n
        FROM public.lines x WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.payer_book_id AND x.entry_id = NEW.payer_entry_id;
    IF bad <> 0 OR n <> 2 THEN
        RAISE EXCEPTION 'The first entry records the bank item to the account this link names in the Book that paid.' USING ERRCODE = 'VP020';
    END IF;
    -- The beneficiary's entry: one credit to its term account, whole, and
    -- debits to expenses or assets that aren't money, for the same amount.
    SELECT count(*) FILTER (WHERE x.side = 'credit' AND NOT (x.account_id = terms.beneficiary_account_id AND x.amount_minor = amount))
         + count(*) FILTER (WHERE x.side = 'debit' AND NOT (a.class = 'expense' OR a.class = 'asset' AND coalesce(a.role, '') NOT IN ('cash', 'card')
               AND NOT EXISTS (SELECT 1 FROM public.bank_accounts y WHERE y.tenant_id = a.tenant_id AND y.book_id = a.book_id AND y.account_id = a.id))),
           count(*) FILTER (WHERE x.side = 'credit')
        INTO bad, n
        FROM public.lines x JOIN public.accounts a ON a.tenant_id = x.tenant_id AND a.book_id = x.book_id AND a.id = x.account_id
        WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.beneficiary_book_id AND x.entry_id = NEW.beneficiary_entry_id;
    IF bad <> 0 OR n <> 1 THEN
        RAISE EXCEPTION 'The second entry credits the account this link names (%) with the whole amount, and debits only expenses, or assets that aren''t cash, a card or a bank account.', terms.beneficiary_account_id USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.entries e WHERE e.tenant_id = NEW.tenant_id
            AND (e.book_id = NEW.payer_book_id AND e.id = NEW.payer_entry_id OR e.book_id = NEW.beneficiary_book_id AND e.id = NEW.beneficiary_entry_id)
            AND (e.accounting_date <> item.occurred_on OR e.reversal_of IS NOT NULL))
        OR (SELECT count(*) FROM public.entries e WHERE e.tenant_id = NEW.tenant_id
            AND (e.book_id = NEW.payer_book_id AND e.id = NEW.payer_entry_id OR e.book_id = NEW.beneficiary_book_id AND e.id = NEW.beneficiary_entry_id)) <> 2 THEN
        RAISE EXCEPTION 'Both entries of a pair are dated the bank item''s date (%).', item.occurred_on USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;

-- Carried costs are open costs the company owes, like costs paid for it.
CREATE OR REPLACE FUNCTION public.open_costs(t uuid, creditor uuid, debtor uuid, debtor_account uuid, leaving_out uuid)
    RETURNS TABLE (pair_key text, kind text, payer_book_id uuid, payer_entry_id uuid, beneficiary_book_id uuid, beneficiary_entry_id uuid,
        book_id uuid, entry_id uuid, accounting_date date, sequence bigint, description text, amount bigint, repaid bigint, open bigint)
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
WITH owed AS (
    SELECT greatest(coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0), 0)::bigint AS amount
    FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.account_id = debtor_account AND x.entry_id IS DISTINCT FROM leaving_out
), costs AS (
    SELECT p.pair_key, p.kind, p.payer_book_id, p.payer_entry_id, p.beneficiary_book_id, p.beneficiary_entry_id,
        p.beneficiary_book_id AS book_id, p.beneficiary_entry_id AS entry_id, e.accounting_date, e.sequence::bigint AS sequence, e.description,
        public.cost_amount(t, p.beneficiary_book_id, p.beneficiary_entry_id) AS amount
    FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
    WHERE p.tenant_id = t
        AND (p.kind IN ('cost', 'carried') AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
            OR p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor)
        AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)
), repaid AS (
    SELECT c.*, least(c.amount, public.cost_repaid_amount(t, c.book_id, c.entry_id, leaving_out)) AS repaid FROM costs c
), ranked AS (
    SELECT r.*, sum(r.amount - r.repaid) OVER (ORDER BY r.accounting_date DESC, r.sequence DESC, r.pair_key DESC ROWS UNBOUNDED PRECEDING) AS newer
    FROM repaid r
)
SELECT r.pair_key, r.kind, r.payer_book_id, r.payer_entry_id, r.beneficiary_book_id, r.beneficiary_entry_id, r.book_id, r.entry_id,
    r.accounting_date, r.sequence, r.description, r.amount, r.repaid,
    least(r.amount - r.repaid, greatest((SELECT o.amount FROM owed o) - (r.newer - (r.amount - r.repaid)), 0))::bigint AS open
FROM ranked r ORDER BY r.accounting_date, r.sequence, r.pair_key
$$;

CREATE OR REPLACE FUNCTION public.entry_pair_of(t uuid, b uuid, e uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE (
    SELECT jsonb_build_object('pair_key', x.pair_key, 'kind', x.kind,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'entry_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_entry_id ELSE x.payer_entry_id END,
        'side', CASE WHEN x.payer_book_id = b THEN 'beneficiary' ELSE 'payer' END,
        'repaid_by', CASE WHEN x.kind IN ('cost', 'opening', 'carried') THEN public.cost_repaid_by(t, x.beneficiary_book_id, x.beneficiary_entry_id, NULL) END)
    FROM public.entry_pairs x WHERE x.tenant_id = t
        AND (x.payer_book_id = b AND x.payer_entry_id = e OR x.beneficiary_book_id = b AND x.beneficiary_entry_id = e)) END
$$;

-- Records a carried pair once both entries are posted: the payer's moves
-- the cost from expenses to the link's receivable, the company's records
-- it to expenses against the link's payable, for the same amount and on
-- the same day.
CREATE FUNCTION public.carry_cost(t uuid, key text, payer uuid, payer_entry uuid, company uuid, company_entry uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE terms public.book_link_terms%ROWTYPE; amount bigint; bad int; n int;
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner carries a cost' USING ERRCODE = '42501';
    END IF;
    terms := public.active_link_terms(t, payer, company);
    IF terms.link_id IS NULL OR terms.treatment IS DISTINCT FROM 'owed' THEN
        RAISE EXCEPTION 'A carried cost is owed back: set "% pays for %" to owed back in Links.', public.book_current_name(t, payer), public.book_current_name(t, company) USING ERRCODE = 'VP020';
    END IF;
    amount := (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = payer AND x.entry_id = payer_entry AND x.side = 'debit');
    -- The payer's entry: the receivable debited whole, expenses credited.
    SELECT count(*) FILTER (WHERE NOT (x.side = 'debit' AND x.account_id = terms.payer_account_id AND x.amount_minor = amount
            OR x.side = 'credit' AND a.class = 'expense')), count(*) FILTER (WHERE x.side = 'debit') INTO bad, n
        FROM public.lines x JOIN public.accounts a ON a.tenant_id = x.tenant_id AND a.book_id = x.book_id AND a.id = x.account_id
        WHERE x.tenant_id = t AND x.book_id = payer AND x.entry_id = payer_entry;
    IF amount IS NULL OR bad <> 0 OR n <> 1 THEN
        RAISE EXCEPTION 'The first entry moves the cost from expenses to what the company owes.' USING ERRCODE = 'VP020';
    END IF;
    -- The company's entry: the payable credited whole, expenses debited.
    SELECT count(*) FILTER (WHERE NOT (x.side = 'credit' AND x.account_id = terms.beneficiary_account_id AND x.amount_minor = amount
            OR x.side = 'debit' AND a.class = 'expense')), count(*) FILTER (WHERE x.side = 'credit') INTO bad, n
        FROM public.lines x JOIN public.accounts a ON a.tenant_id = x.tenant_id AND a.book_id = x.book_id AND a.id = x.account_id
        WHERE x.tenant_id = t AND x.book_id = company AND x.entry_id = company_entry;
    IF bad <> 0 OR n <> 1 THEN
        RAISE EXCEPTION 'The second entry records the cost to expenses against what the company owes.' USING ERRCODE = 'VP020';
    END IF;
    IF (SELECT count(DISTINCT e.accounting_date) FROM public.entries e WHERE e.tenant_id = t
            AND (e.book_id = payer AND e.id = payer_entry OR e.book_id = company AND e.id = company_entry) AND e.reversal_of IS NULL) <> 1 THEN
        RAISE EXCEPTION 'Both entries of a carried cost are dated the formation date.' USING ERRCODE = 'VP020';
    END IF;
    PERFORM set_config('vult.settling', key, true);
    INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id)
        VALUES (t, key, 'carried', payer, payer_entry, company, company_entry);
    PERFORM set_config('vult.settling', '', true);
END $$;
REVOKE ALL ON FUNCTION public.carry_cost(uuid, text, uuid, uuid, uuid, uuid) FROM PUBLIC;

-- A project turned into a company, once per request: the company Book.
CREATE TABLE public.project_promotions (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    project_id uuid NOT NULL,
    request_key text NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
    company_book_id uuid NOT NULL,
    formation_date date NOT NULL,
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, request_key),
    FOREIGN KEY (tenant_id, book_id, project_id) REFERENCES public.projects(tenant_id, book_id, id)
);

-- Each cost carried into a company, once: the entry it was paid by, and
-- the carried pair.
CREATE TABLE public.project_carried (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    project_id uuid NOT NULL,
    request_key text NOT NULL,
    pair_key text NOT NULL,
    actor_id uuid NOT NULL,
    PRIMARY KEY (tenant_id, book_id, entry_id),
    FOREIGN KEY (tenant_id, book_id, request_key) REFERENCES public.project_promotions(tenant_id, book_id, request_key)
);
CREATE TRIGGER project_guard BEFORE INSERT OR UPDATE ON public.project_promotions FOR EACH ROW EXECUTE FUNCTION public.project_guard();
CREATE TRIGGER project_guard BEFORE INSERT OR UPDATE ON public.project_carried FOR EACH ROW EXECUTE FUNCTION public.project_guard();
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.project_promotions FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('book_id');
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.project_carried FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('book_id');
DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['public.project_promotions', 'public.project_carried'] LOOP
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON %s FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY project_read ON %s FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_id, false))', tab);
        EXECUTE format('CREATE POLICY project_insert ON %s FOR INSERT WITH CHECK (public.project_writer(tenant_id, book_id))', tab);
        EXECUTE format('CREATE POLICY project_delete ON %s FOR DELETE USING (public.project_writer(tenant_id, book_id))', tab);
    END LOOP;
END $$;
