-- Accounting foundations: a chart of accounts with parents and retirement,
-- payees, a closed vocabulary of account roles, statement reconciliation and
-- audited period close and reopen.
--
-- Every change is additive. Posted money, receipts and earlier history keep
-- their meaning; existing rows upgrade as described beside each part.

-- A shared test for the import and restore paths that may replay history.
-- Only a trusted operator session restoring this exact Book qualifies.
CREATE FUNCTION public.ledger_restoring(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT public.ledger_import_owner() AND coalesce(current_setting('vult.restore_book', true), '') = t::text || '/' || b::text
$$;

-- Account roles are a closed vocabulary, each bound to one account class.
-- Books recorded by earlier releases keep the roles those releases accepted,
-- with the meaning reports gave them: cost_of_goods_sold is the earlier
-- spelling of cost_of_sales; other_income and other_expense refine income and
-- expense. opening_balance marks the equity account that opening balances
-- post against. Stored rows are never rewritten.
CREATE FUNCTION public.ledger_role_fits(role text, class text) RETURNS boolean
    LANGUAGE sql IMMUTABLE SET search_path = pg_catalog AS $$
SELECT role IS NULL OR (role, class) IN (
    ('cash', 'asset'), ('receivable', 'asset'), ('fixed_asset', 'asset'), ('investment', 'asset'),
    ('card', 'liability'), ('loan', 'liability'), ('payable', 'liability'),
    ('opening_balance', 'equity'),
    ('cost_of_sales', 'expense'), ('cost_of_goods_sold', 'expense'),
    ('other_income', 'income'), ('other_expense', 'expense'))
$$;

-- Fail loudly instead of guessing a meaning for a role outside the vocabulary.
DO $$
DECLARE found text;
BEGIN
    SELECT string_agg(DISTINCT quote_literal(role), ', ' ORDER BY quote_literal(role)) INTO found
    FROM (SELECT role FROM public.accounts UNION ALL SELECT role FROM public.account_revisions) stored
    WHERE role IS NOT NULL AND role NOT IN ('cash', 'card', 'loan', 'receivable', 'payable', 'fixed_asset', 'investment', 'cost_of_sales', 'cost_of_goods_sold', 'other_income', 'other_expense', 'opening_balance');
    IF found IS NOT NULL THEN
        RAISE EXCEPTION 'account roles outside the supported vocabulary are stored: %', found
            USING ERRCODE = '23514',
            HINT = 'Supported roles are cash, card, loan, receivable, payable, fixed_asset, investment, cost_of_sales and opening_balance. Change each account role before upgrading.';
    END IF;
    SELECT string_agg(DISTINCT quote_literal(role) || ' on ' || class, ', ') INTO found
    FROM public.accounts a WHERE NOT public.ledger_role_fits(a.role, a.class);
    IF found IS NOT NULL THEN
        RAISE EXCEPTION 'account roles are stored on accounts of the wrong class: %', found
            USING ERRCODE = '23514',
            HINT = 'cash, receivable, fixed_asset and investment need an asset account; card, loan and payable need a liability account; cost_of_sales and other_expense need an expense account; other_income needs an income account.';
    END IF;
END $$;

-- Whether a visible row was written by this transaction, including its
-- savepoints. Only this transaction's own uncommitted rows are visible while
-- still in progress; every other visible row was committed.
CREATE FUNCTION public.ledger_written_here(written xid) RETURNS boolean
    LANGUAGE sql VOLATILE SET search_path = pg_catalog AS $$
-- Visible transaction IDs lie within 2^31 of this transaction's own ID.
SELECT coalesce(pg_xact_status((CASE WHEN candidate > own + 2147483648 THEN candidate - 4294967296
        WHEN candidate < own - 2147483648 THEN candidate + 4294967296 ELSE candidate END)::text::xid8) = 'in progress', false)
FROM (SELECT own, own - mod(own, 4294967296) + written::text::numeric AS candidate
    FROM (SELECT pg_current_xact_id()::text::numeric AS own) self) current
$$;

-- A. Chart of accounts. Parent and status are presentation and control: they
-- never move a balance. Every existing account becomes active with no parent.
ALTER TABLE public.accounts
    ADD COLUMN parent_account_id uuid,
    -- A heading groups accounts and takes no postings of its own.
    ADD COLUMN heading boolean NOT NULL DEFAULT false,
    ADD COLUMN status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired')),
    ADD CONSTRAINT account_parent_fk FOREIGN KEY (tenant_id, book_id, parent_account_id)
        REFERENCES public.accounts(tenant_id, book_id, id),
    ADD CONSTRAINT account_parent_self CHECK (parent_account_id IS DISTINCT FROM id),
    ADD CONSTRAINT account_role_class CHECK (public.ledger_role_fits(role, class));
ALTER TABLE public.account_revisions
    ADD COLUMN parent_account_id uuid,
    ADD COLUMN heading boolean NOT NULL DEFAULT false,
    ADD COLUMN status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired')),
    -- The transfer that emptied an account, recorded with its retirement.
    ADD COLUMN transfer_entry_id uuid,
    ADD CONSTRAINT account_revision_parent_fk FOREIGN KEY (tenant_id, book_id, parent_account_id)
        REFERENCES public.accounts(tenant_id, book_id, id),
    ADD CONSTRAINT account_revision_transfer_fk FOREIGN KEY (tenant_id, book_id, transfer_entry_id)
        REFERENCES public.entries(tenant_id, book_id, id) DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT account_revision_parent_self CHECK (parent_account_id IS DISTINCT FROM account_id),
    ADD CONSTRAINT account_revision_transfer_status CHECK (transfer_entry_id IS NULL OR status = 'retired'),
    ADD CONSTRAINT account_revision_role CHECK (role IS NULL OR role IN ('cash', 'card', 'loan', 'receivable', 'payable', 'fixed_asset', 'investment', 'cost_of_sales', 'cost_of_goods_sold', 'other_income', 'other_expense', 'opening_balance'));
-- A revision that names no status keeps the account's current one.
ALTER TABLE public.account_revisions ALTER COLUMN status DROP DEFAULT, ALTER COLUMN heading DROP DEFAULT;
CREATE INDEX accounts_children ON public.accounts(tenant_id, book_id, parent_account_id) WHERE parent_account_id IS NOT NULL;

-- B. Payees: a stable identity for whom an entry was with. Names change;
-- identity doesn't. Two payees with the same name stay separate.
CREATE TABLE public.payees (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    actor_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED
);
CREATE TABLE public.payee_revisions (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    payee_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    display_name text NOT NULL CHECK (length(btrim(display_name)) BETWEEN 1 AND 200 AND display_name !~ '[[:cntrl:]]'),
    kind text NOT NULL CHECK (kind IN ('person', 'organization')),
    -- An optional public brand identifier, never a private contact.
    brand_id text CHECK (brand_id ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
    status text NOT NULL CHECK (status IN ('active', 'retired')),
    actor_id uuid NOT NULL,
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 4000),
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, payee_id, revision),
    FOREIGN KEY (tenant_id, book_id, payee_id) REFERENCES public.payees(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK (revision = 1 AND status = 'active' OR revision > 1 AND length(btrim(reason)) > 0)
);
-- Who an entry was with is a separate, revisioned decision: correcting it
-- never creates a journal correction. An entry with no row is "Not recorded",
-- which differs from an explicit unknown counterpart.
CREATE TABLE public.entry_payees (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    counterpart text NOT NULL CHECK (counterpart IN ('payee', 'unknown')),
    payee_id uuid,
    actor_id uuid NOT NULL,
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 4000),
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, entry_id, revision),
    FOREIGN KEY (tenant_id, book_id, entry_id) REFERENCES public.entries(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, payee_id) REFERENCES public.payees(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK ((counterpart = 'payee') = (payee_id IS NOT NULL)),
    CHECK (revision = 1 OR length(btrim(reason)) > 0)
);
CREATE INDEX entry_payees_payee ON public.entry_payees(tenant_id, book_id, payee_id) WHERE payee_id IS NOT NULL;

-- D. Statements are evidence the Book is compared against. They never post.
CREATE TABLE public.statements (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    -- The account's role when the statement was recorded; it fixes the sign.
    kind text NOT NULL CHECK (kind IN ('cash', 'card')),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    scale integer NOT NULL CHECK (scale BETWEEN 0 AND 18),
    document_id uuid NOT NULL,
    period_start date NOT NULL CHECK (period_start BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
    period_end date NOT NULL CHECK (period_end BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
    opening_balance_minor bigint NOT NULL,
    closing_balance_minor bigint NOT NULL,
    evidence_state text NOT NULL CHECK (evidence_state IN ('complete', 'incomplete', 'disputed')),
    note text NOT NULL DEFAULT '' CHECK (length(note) <= 4000),
    actor_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    UNIQUE (tenant_id, book_id, receipt_id),
    CHECK (period_start <= period_end),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, document_id) REFERENCES public.documents(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX statements_account ON public.statements(tenant_id, book_id, account_id, period_end);
-- One immutable observed row. Its amount is signed the way the statement's
-- balance moves: a deposit on a cash statement, a charge on a card statement.
CREATE TABLE public.statement_rows (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    statement_id uuid NOT NULL,
    row_number integer NOT NULL CHECK (row_number BETWEEN 1 AND 1000000),
    location jsonb NOT NULL CHECK (public.document_location_valid(location)),
    occurred_on date NOT NULL CHECK (occurred_on BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
    amount_minor bigint NOT NULL,
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 4000),
    external_id text CHECK (length(external_id) BETWEEN 1 AND 256),
    lifecycle text NOT NULL CHECK (lifecycle IN ('settled', 'pending', 'refund', 'chargeback', 'chargeback_reversal', 'cancelled')),
    related_external_id text CHECK (length(related_external_id) BETWEEN 1 AND 256),
    raw_bytes bytea NOT NULL CHECK (octet_length(raw_bytes) BETWEEN 1 AND 65536),
    raw_sha256 text GENERATED ALWAYS AS (encode(sha256(raw_bytes), 'hex')) STORED,
    UNIQUE (tenant_id, book_id, id),
    UNIQUE (tenant_id, book_id, statement_id, row_number),
    FOREIGN KEY (tenant_id, book_id, statement_id) REFERENCES public.statements(tenant_id, book_id, id)
);
-- An explicit decision that one row repeats another, for example across
-- overlapping statements. Equal date and amount alone never decide it.
CREATE TABLE public.statement_duplicates (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    row_id uuid NOT NULL,
    canonical_row_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 4000),
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, row_id),
    FOREIGN KEY (tenant_id, book_id, row_id) REFERENCES public.statement_rows(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, canonical_row_id) REFERENCES public.statement_rows(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK (row_id <> canonical_row_id)
);
CREATE INDEX statement_duplicates_canonical ON public.statement_duplicates(tenant_id, book_id, canonical_row_id);
-- One decision that statement rows and posted lines are the same money.
CREATE TABLE public.statement_matches (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 4000),
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED
);
-- Parts support one-to-one, split and combined matches. Each part is either a
-- statement row or a posted line, with its signed amount in the statement unit.
CREATE TABLE public.statement_match_parts (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    match_id uuid NOT NULL,
    part integer NOT NULL CHECK (part BETWEEN 1 AND 512),
    row_id uuid,
    line_id uuid,
    amount_minor bigint NOT NULL CHECK (amount_minor <> 0),
    PRIMARY KEY (tenant_id, book_id, match_id, part),
    UNIQUE (tenant_id, book_id, match_id, row_id),
    UNIQUE (tenant_id, book_id, match_id, line_id),
    FOREIGN KEY (tenant_id, book_id, match_id) REFERENCES public.statement_matches(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, row_id) REFERENCES public.statement_rows(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, line_id) REFERENCES public.lines(tenant_id, book_id, id),
    CHECK (num_nonnulls(row_id, line_id) = 1)
);
CREATE INDEX statement_match_parts_row ON public.statement_match_parts(tenant_id, book_id, row_id) WHERE row_id IS NOT NULL;
CREATE INDEX statement_match_parts_line ON public.statement_match_parts(tenant_id, book_id, line_id) WHERE line_id IS NOT NULL;
-- Undoing a match keeps its history and releases its allocation.
CREATE TABLE public.statement_match_revocations (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    match_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 4000),
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, match_id),
    FOREIGN KEY (tenant_id, book_id, match_id) REFERENCES public.statement_matches(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED
);
-- The entry posted because of a statement row, for example a missing bank fee.
CREATE TABLE public.statement_adjustments (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    row_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, row_id),
    FOREIGN KEY (tenant_id, book_id, row_id) REFERENCES public.statement_rows(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, entry_id) REFERENCES public.entries(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX statement_adjustments_entry ON public.statement_adjustments(tenant_id, book_id, entry_id);
-- The sign-off, with the exact report it signed.
CREATE TABLE public.statement_reviews (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    statement_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    outcome text NOT NULL CHECK (outcome IN ('reconciled', 'needs_work')),
    report_bytes bytea NOT NULL CHECK (octet_length(report_bytes) BETWEEN 2 AND 16777216),
    report_sha256 text GENERATED ALWAYS AS (encode(sha256(report_bytes), 'hex')) STORED,
    actor_id uuid NOT NULL,
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 4000),
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, statement_id, revision),
    FOREIGN KEY (tenant_id, book_id, statement_id) REFERENCES public.statements(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED
);

-- E. Accounting periods: immutable date ranges (both ends included). A
-- period may contain earlier ones, as a year contains its closed months, but
-- never partly overlaps another. Who closed or reopened one, why, and on what evidence, is a
-- separate append-only history of decisions.
CREATE TABLE public.accounting_periods (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    start_date date NOT NULL CHECK (start_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
    end_date date NOT NULL CHECK (end_date BETWEEN DATE '0001-01-01' AND DATE '9999-12-31'),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    CHECK (start_date <= end_date),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);
CREATE INDEX accounting_periods_dates ON public.accounting_periods(tenant_id, book_id, start_date, end_date);
CREATE TABLE public.accounting_period_events (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    period_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    event text NOT NULL CHECK (event IN ('closed', 'reopened')),
    -- 'decision' is a recorded close or reopen by its actor. 'closed_period'
    -- is a close kept from the original close record, which named no actor
    -- or reason.
    origin text NOT NULL CHECK (origin IN ('decision', 'closed_period')),
    actor_id uuid,
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 4000),
    -- Statement reviews the decision rests on: [{"statement_id":…,"revision":…}].
    statement_reviews jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(statement_reviews) = 'array'
        AND jsonb_array_length(statement_reviews) <= 256),
    -- The exact review report shown before the decision, when there was one.
    report_bytes bytea CHECK (octet_length(report_bytes) BETWEEN 2 AND 16777216),
    report_sha256 text GENERATED ALWAYS AS (encode(sha256(report_bytes), 'hex')) STORED,
    -- The review items still open when a period was closed, counted by the
    -- database: {"unreconciled_statements":n,"entries_without_evidence":n,
    -- "entries_without_payee":n}. Empty for a kept original close.
    open_items jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(open_items) = 'object'),
    receipt_id uuid,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, period_id, revision),
    FOREIGN KEY (tenant_id, book_id, period_id) REFERENCES public.accounting_periods(tenant_id, book_id, id),
    CONSTRAINT accounting_period_event_receipt_fk FOREIGN KEY (tenant_id, book_id, receipt_id)
        REFERENCES public.requests(tenant_id, book_id, id) DEFERRABLE INITIALLY DEFERRED,
    CHECK ((origin = 'decision') = (actor_id IS NOT NULL AND receipt_id IS NOT NULL)),
    CHECK (origin = 'decision' OR (event = 'closed' AND revision = 1 AND reason = ''
        AND report_bytes IS NULL AND statement_reviews = '[]' AND open_items = '{}')),
    CHECK (event = 'closed' OR open_items = '{}'),
    CHECK (revision > 1 OR event = 'closed'),
    CHECK (event <> 'reopened' OR length(btrim(reason)) > 0)
);
-- At most one period event can be in each receipt.
CREATE UNIQUE INDEX accounting_period_event_receipt ON public.accounting_period_events(tenant_id, book_id, receipt_id)
    WHERE receipt_id IS NOT NULL;

-- Every existing close becomes a period with one close kept from the original
-- record: its time is preserved and no actor or reason is invented. The
-- period keeps the original identifier, so references to it stay valid.
INSERT INTO public.accounting_periods(tenant_id, book_id, id, start_date, end_date, recorded_at)
    SELECT tenant_id, book_id, id, start_date, end_date, closed_at FROM public.closed_periods;
INSERT INTO public.accounting_period_events(tenant_id, book_id, period_id, revision, event, origin, recorded_at)
    SELECT tenant_id, book_id, id, 1, 'closed', 'closed_period', closed_at FROM public.closed_periods;
-- Settle the copied rows' checks now, so the tables can take row security.
SET CONSTRAINTS public.accounting_period_event_receipt_fk IMMEDIATE;
SET CONSTRAINTS public.accounting_period_event_receipt_fk DEFERRED;

-- Account revisions carry parent and status. Roles are checked against class.
CREATE OR REPLACE FUNCTION public.ledger_account_revision() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $$
DECLARE
    prior public.accounts%ROWTYPE;
    unit_changed boolean;
    parent public.accounts%ROWTYPE;
    cyclic boolean;
    above integer;
    below integer;
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
        OR (NEW.role IS NOT NULL AND NEW.role !~ '^[a-z][a-z0-9_]{0,63}$') THEN
        RAISE EXCEPTION 'invalid account measurement unit' USING ERRCODE='23514';
    END IF;
    IF NOT public.ledger_role_fits(NEW.role,prior.class) THEN
        RAISE EXCEPTION 'account role does not fit the account class' USING ERRCODE='23514';
    END IF;
    -- A revision that names no status or heading keeps the current one.
    NEW.status=coalesce(NEW.status,CASE WHEN NEW.revision=1 THEN 'active' ELSE prior.status END);
    NEW.heading=coalesce(NEW.heading,CASE WHEN NEW.revision=1 THEN false ELSE prior.heading END);
    IF NEW.revision=1 AND NEW.status<>'active' THEN
        RAISE EXCEPTION 'a new account starts active' USING ERRCODE='23514';
    END IF;
    IF NEW.transfer_entry_id IS NOT NULL AND NOT (NEW.status='retired' AND prior.status='active') THEN
        RAISE EXCEPTION 'a balance transfer is recorded only with a retirement' USING ERRCODE='23514';
    END IF;
    IF NOT public.ledger_import_owner() AND NEW.receipt_id IS NULL AND
        (NEW.parent_account_id IS DISTINCT FROM prior.parent_account_id OR NEW.status IS DISTINCT FROM prior.status
            OR NEW.heading IS DISTINCT FROM prior.heading) THEN
        RAISE EXCEPTION 'account change receipt required' USING ERRCODE='23514';
    END IF;
    -- Replayed history is checked as a whole once the Book is restored.
    IF NEW.parent_account_id IS NOT NULL AND NOT public.ledger_restoring(NEW.tenant_id,NEW.book_id) THEN
        SELECT * INTO parent FROM public.accounts
            WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND id=NEW.parent_account_id;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'parent account must be in the same Book' USING ERRCODE='23503';
        END IF;
        IF parent.class<>prior.class THEN
            RAISE EXCEPTION 'a parent account must have the same class' USING ERRCODE='23514';
        END IF;
        IF parent.status='retired' AND NEW.parent_account_id IS DISTINCT FROM prior.parent_account_id THEN
            RAISE EXCEPTION 'a retired account cannot gain children' USING ERRCODE='23514';
        END IF;
        WITH RECURSIVE up(id,parent_id,depth,path) AS (
            SELECT parent.id,parent.parent_account_id,1,ARRAY[parent.id]
            UNION ALL
            SELECT a.id,a.parent_account_id,u.depth+1,u.path||a.id FROM up u JOIN public.accounts a
                ON a.tenant_id=NEW.tenant_id AND a.book_id=NEW.book_id AND a.id=u.parent_id
            WHERE NOT a.id=ANY(u.path) AND u.depth<16
        ) SELECT coalesce(bool_or(id=NEW.account_id),false),max(depth) INTO cyclic,above FROM up;
        IF cyclic THEN
            RAISE EXCEPTION 'account parents cannot form a cycle' USING ERRCODE='23514';
        END IF;
        WITH RECURSIVE down(id,depth,path) AS (
            SELECT NEW.account_id,0,ARRAY[NEW.account_id]
            UNION ALL
            SELECT a.id,d.depth+1,d.path||a.id FROM down d JOIN public.accounts a
                ON a.tenant_id=NEW.tenant_id AND a.book_id=NEW.book_id AND a.parent_account_id=d.id
            WHERE NOT a.id=ANY(d.path) AND d.depth<16
        ) SELECT max(depth) INTO below FROM down;
        IF above+1+below>4 THEN
            RAISE EXCEPTION 'account hierarchy is limited to four levels' USING ERRCODE='23514';
        END IF;
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
        role_revision=NEW.role_revision,parent_account_id=NEW.parent_account_id,status=NEW.status,heading=NEW.heading
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
            AND ROW(r.code,r.name,r.currency,r.native_scale,r.currency_definition,r.role,r.currency_revision,r.native_origin_revision,r.role_revision,r.parent_account_id,r.status,r.heading)
                IS NOT DISTINCT FROM ROW(a.code,a.name,a.currency,a.native_scale,a.currency_definition,a.role,a.currency_revision,a.native_origin_revision,a.role_revision,a.parent_account_id,a.status,a.heading)))) THEN
        RAISE EXCEPTION 'account revision required' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END
$$;

-- Each revision matches the receipt that recorded it. Creating and revising
-- an account record it alone; a retirement records the account and its
-- balance transfer; a starter chart records every account it creates.
CREATE OR REPLACE FUNCTION public.ledger_account_measurement_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE saved jsonb; result jsonb; requested jsonb; operation text; prior public.account_revisions%ROWTYPE; account_class text;
BEGIN
    IF NEW.receipt_id IS NULL THEN
        IF num_nonnulls(NEW.currency,NEW.role_revision,NEW.native_origin_revision,NEW.transfer_entry_id)>0 THEN
            RAISE EXCEPTION 'account measurement receipt required' USING ERRCODE='23514';
        END IF;
        RETURN NULL;
    END IF;
    SELECT r.operation,convert_from(r.result_bytes,'UTF8')::jsonb,convert_from(r.request_bytes,'UTF8')::jsonb
        INTO STRICT operation,result,requested FROM public.requests r
        WHERE r.tenant_id=NEW.tenant_id AND r.book_id=NEW.book_id AND r.id=NEW.receipt_id AND r.actor_id=NEW.actor_id;
    SELECT class INTO STRICT account_class FROM public.accounts
        WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND id=NEW.account_id;
    IF NEW.revision>1 THEN
        SELECT * INTO STRICT prior FROM public.account_revisions
            WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND account_id=NEW.account_id AND revision=NEW.revision-1;
    END IF;
    saved=result;
    IF operation IN ('retire_account','reactivate_account') THEN
        -- Status is the only change; everything else stays as it was.
        IF NEW.revision=1 OR requested->>'account_id' IS DISTINCT FROM NEW.account_id::text
            OR (requested->>'expected_revision')::bigint IS DISTINCT FROM NEW.revision-1
            OR ROW(NEW.code,NEW.name,NEW.currency,NEW.native_scale,NEW.currency_definition,NEW.role,NEW.currency_revision,
                NEW.native_origin_revision,NEW.role_revision,NEW.parent_account_id,NEW.heading) IS DISTINCT FROM
               ROW(prior.code,prior.name,prior.currency,prior.native_scale,prior.currency_definition,prior.role,prior.currency_revision,
                prior.native_origin_revision,prior.role_revision,prior.parent_account_id,prior.heading)
            OR NEW.status<>(CASE operation WHEN 'retire_account' THEN 'retired' ELSE 'active' END)
            OR (operation='retire_account' AND result->'transfer'->>'id' IS DISTINCT FROM NEW.transfer_entry_id::text)
            OR (operation='reactivate_account' AND NEW.transfer_entry_id IS NOT NULL) THEN
            RAISE EXCEPTION 'account status receipt differs from revision' USING ERRCODE='23514';
        END IF;
        IF operation='retire_account' THEN
            saved=result->'account';
        END IF;
    ELSIF operation='apply_starter_chart' THEN
        -- The template is Go code; the receipt names it and lists each account.
        IF NEW.revision<>1 OR jsonb_typeof(requested->'template') IS DISTINCT FROM 'string'
            OR result->>'template' IS DISTINCT FROM requested->>'template' THEN
            RAISE EXCEPTION 'starter chart receipt differs from revision' USING ERRCODE='23514';
        END IF;
        SELECT value INTO saved FROM jsonb_array_elements(result->'accounts') WHERE value->>'id'=NEW.account_id::text;
    ELSIF operation<>(CASE WHEN NEW.revision=1 THEN 'create_account' ELSE 'revise_account' END)
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
        OR (NEW.revision=1 AND coalesce(requested->>'native_origin'='empty_ledger',false) IS DISTINCT FROM coalesce(NEW.native_origin_revision=1,false))
        OR (NOT requested ? 'parent_account_id' AND NEW.parent_account_id IS DISTINCT FROM prior.parent_account_id)
        OR (requested ? 'parent_account_id' AND nullif(requested->>'parent_account_id','') IS DISTINCT FROM NEW.parent_account_id::text)
        OR (NOT requested ? 'status' AND NEW.status IS DISTINCT FROM coalesce(prior.status,'active'))
        OR (requested ? 'status' AND requested->>'status' IS DISTINCT FROM NEW.status)
        OR (NOT requested ? 'heading' AND NEW.heading IS DISTINCT FROM coalesce(prior.heading,false))
        OR (requested ? 'heading' AND (requested->>'heading')::boolean IS DISTINCT FROM NEW.heading)
        OR NEW.transfer_entry_id IS NOT NULL THEN
        RAISE EXCEPTION 'account measurement receipt differs from revision' USING ERRCODE='23514';
    END IF;
    -- Receipts written before parents and status existed name neither; an
    -- active account omits its status.
    IF saved IS NULL OR saved->>'class' IS DISTINCT FROM account_class
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
        OR saved->>'parent_account_id' IS DISTINCT FROM NEW.parent_account_id::text
        OR coalesce(saved->>'status','active') IS DISTINCT FROM NEW.status
        OR coalesce((saved->>'heading')::boolean,false) IS DISTINCT FROM NEW.heading THEN
        RAISE EXCEPTION 'account measurement receipt differs from revision' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
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
    -- Account parents are replayed in revision order; check the result once.
    IF EXISTS(SELECT 1 FROM public.accounts a JOIN public.accounts p ON p.tenant_id=a.tenant_id AND p.book_id=a.book_id
        AND p.id=a.parent_account_id WHERE a.tenant_id=t AND a.book_id=b AND p.class<>a.class) THEN
        RAISE EXCEPTION 'restored account parents differ in class' USING ERRCODE='23514';
    END IF;
    IF EXISTS(WITH RECURSIVE up(start_id,id,depth,cyclic) AS (
            SELECT a.id,a.parent_account_id,1,false FROM public.accounts a WHERE a.tenant_id=t AND a.book_id=b
            UNION ALL
            SELECT u.start_id,a.parent_account_id,u.depth+1,a.parent_account_id=u.start_id FROM up u JOIN public.accounts a
                ON a.tenant_id=t AND a.book_id=b AND a.id=u.id WHERE NOT u.cyclic AND u.depth<=4
        ) SELECT 1 FROM up WHERE cyclic OR (id IS NOT NULL AND depth>=4)) THEN
        RAISE EXCEPTION 'restored account parents form a cycle or exceed four levels' USING ERRCODE='23514';
    END IF;
    IF (SELECT logo FROM public.book_revisions WHERE tenant_id=t AND book_id=b ORDER BY revision DESC LIMIT 1)
        IS DISTINCT FROM (SELECT jsonb_build_object('id',id,'sha256',sha256,'content_type',content_type,'size',octet_length(data))
            FROM public.book_logo_images WHERE tenant_id=t AND book_id=b) THEN
        RAISE EXCEPTION 'Book logo differs from retained history' USING ERRCODE='23514';
    END IF;
END
$$;

-- Each row owns one accepted settings revision and its retained request outcome.

-- A retired account rejects new lines. Restored history replays them.
CREATE OR REPLACE FUNCTION public.ledger_line_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id,NEW.book_id);
    IF EXISTS(SELECT 1 FROM public.requests r JOIN public.entries e ON e.receipt_id=r.id
        WHERE e.tenant_id=NEW.tenant_id AND e.book_id=NEW.book_id AND e.id=NEW.entry_id
        AND e.xmin::text::bigint<>mod(pg_current_xact_id()::text::numeric,4294967296)::bigint) THEN
        RAISE EXCEPTION 'posted lines are immutable' USING ERRCODE='23514';
    END IF;
    IF NOT public.ledger_restoring(NEW.tenant_id,NEW.book_id) AND EXISTS(SELECT 1 FROM public.accounts a
        WHERE a.tenant_id=NEW.tenant_id AND a.book_id=NEW.book_id AND a.id=NEW.account_id AND a.status='retired') THEN
        RAISE EXCEPTION 'account is retired' USING ERRCODE='23514';
    END IF;
    -- A heading takes no new postings. A reversal of an earlier line can
    -- still correct a mistake posted before the account became a heading.
    IF NEW.reverses_line_id IS NULL AND NOT public.ledger_restoring(NEW.tenant_id,NEW.book_id) AND EXISTS(SELECT 1 FROM public.accounts a
        WHERE a.tenant_id=NEW.tenant_id AND a.book_id=NEW.book_id AND a.id=NEW.account_id AND a.heading) THEN
        RAISE EXCEPTION 'account is a heading and takes no postings' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;

-- Retiring requires a zero balance in every unit once the request commits,
-- reached beforehand or by the transfer recorded with the retirement.
CREATE FUNCTION public.ledger_account_retirement() RETURNS trigger
    LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE was text; book_balance numeric; native_balance numeric;
BEGIN
    IF NEW.status<>'retired' OR public.ledger_restoring(NEW.tenant_id,NEW.book_id) THEN
        RETURN NULL;
    END IF;
    SELECT status INTO was FROM public.account_revisions
        WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND account_id=NEW.account_id AND revision=NEW.revision-1;
    IF was IS DISTINCT FROM 'active' THEN
        RETURN NULL;
    END IF;
    SELECT coalesce(sum(CASE side WHEN 'debit' THEN amount_minor::numeric ELSE -amount_minor::numeric END),0),
        coalesce(sum(CASE side WHEN 'debit' THEN native_amount_minor::numeric ELSE -native_amount_minor::numeric END),0)
        INTO book_balance,native_balance
        FROM public.lines WHERE tenant_id=NEW.tenant_id AND book_id=NEW.book_id AND account_id=NEW.account_id;
    IF book_balance<>0 OR native_balance<>0 THEN
        RAISE EXCEPTION 'account still holds a balance; record where it went'
            USING ERRCODE='23514', DETAIL=format('book currency %s, account currency %s', book_balance, native_balance);
    END IF;
    IF NEW.transfer_entry_id IS NOT NULL AND (NOT EXISTS(SELECT 1 FROM public.lines l
            WHERE l.tenant_id=NEW.tenant_id AND l.book_id=NEW.book_id AND l.entry_id=NEW.transfer_entry_id AND l.account_id=NEW.account_id)
        OR NOT public.ledger_import_owner() AND NOT public.ledger_written_here((SELECT e.xmin FROM public.entries e
            WHERE e.tenant_id=NEW.tenant_id AND e.book_id=NEW.book_id AND e.id=NEW.transfer_entry_id))) THEN
        RAISE EXCEPTION 'the balance transfer must be posted to this account with its retirement' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER account_retirement AFTER INSERT ON public.account_revisions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.ledger_account_retirement();

-- E. A date is closed when a period contains it and that period's latest
-- decision is a close. Both close and posting take the Book lock first.
CREATE FUNCTION public.ledger_period_closed(t uuid, b uuid, d date) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT EXISTS(SELECT 1 FROM public.accounting_periods p
    WHERE p.tenant_id = t AND p.book_id = b AND d BETWEEN p.start_date AND p.end_date
    AND (SELECT e.event FROM public.accounting_period_events e
        WHERE e.tenant_id = p.tenant_id AND e.book_id = p.book_id AND e.period_id = p.id
        ORDER BY e.revision DESC LIMIT 1) = 'closed')
$$;

CREATE OR REPLACE FUNCTION public.ledger_entry_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path=pg_catalog AS $$
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
        RAISE EXCEPTION 'accounting period is closed' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END $$;

-- Shared rules for every new decision record: the Book lock, the
-- authenticated actor and the server's recorded time. An operator import
-- keeps the retained actor and time.
CREATE FUNCTION public.foundation_record_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.recorded_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;

-- Each decision's request receipt is in the same Book, by the same actor,
-- for one of the operations named by the trigger.
CREATE FUNCTION public.foundation_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.receipt_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM public.requests r
        WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id AND r.id = NEW.receipt_id
        AND r.actor_id = NEW.actor_id AND r.operation = ANY(TG_ARGV)) THEN
        RAISE EXCEPTION 'decision receipt differs from the record' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;

-- B. Payees.
CREATE FUNCTION public.payee_revision_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE prior bigint;
BEGIN
    SELECT coalesce(max(revision), 0) INTO prior FROM public.payee_revisions
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND payee_id = NEW.payee_id;
    IF NEW.revision <> prior + 1 THEN
        RAISE EXCEPTION 'stale payee revision' USING ERRCODE = '23514';
    END IF;
    IF NEW.revision = 1 AND NOT public.ledger_import_owner() AND NOT public.ledger_written_here((SELECT p.xmin FROM public.payees p
        WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id AND p.id = NEW.payee_id)) THEN
        RAISE EXCEPTION 'a payee is named when it is created' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE FUNCTION public.payee_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT EXISTS(SELECT 1 FROM public.payee_revisions r WHERE r.tenant_id = NEW.tenant_id
        AND r.book_id = NEW.book_id AND r.payee_id = NEW.id AND r.revision = 1) THEN
        RAISE EXCEPTION 'payee revision required' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE FUNCTION public.entry_payee_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE prior bigint;
BEGIN
    SELECT coalesce(max(revision), 0) INTO prior FROM public.entry_payees
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND entry_id = NEW.entry_id;
    IF NEW.revision <> prior + 1 THEN
        RAISE EXCEPTION 'stale entry payee revision' USING ERRCODE = '23514';
    END IF;
    IF NEW.payee_id IS NOT NULL AND NOT public.ledger_restoring(NEW.tenant_id, NEW.book_id)
        AND (SELECT r.status FROM public.payee_revisions r WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id
            AND r.payee_id = NEW.payee_id ORDER BY r.revision DESC LIMIT 1) IS DISTINCT FROM 'active' THEN
        RAISE EXCEPTION 'payee is retired or unavailable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

-- D. Statements. A line's comparable amount is in the statement unit and
-- signed the way the statement balance moves: a debit is positive on an asset
-- account and a credit is positive on a liability account. A line that has no
-- amount in that unit is not comparable.
CREATE FUNCTION public.statement_line_amount(t uuid, b uuid, lid uuid, unit text, target_scale integer) RETURNS numeric
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
WITH movement AS (
    -- A later account-unit declaration never changes the line's source unit.
    SELECT coalesce(l.native_amount_minor,l.amount_minor)::numeric AS amount,
        coalesce(l.native_scale,k.scale) AS source_scale,
        CASE WHEN a.class = 'asset' AND l.side = 'debit' OR a.class = 'liability' AND l.side = 'credit' THEN 1 ELSE -1 END AS direction
    FROM public.lines l JOIN public.accounts a ON a.tenant_id = l.tenant_id AND a.book_id = l.book_id AND a.id = l.account_id
    JOIN public.books k ON k.tenant_id = l.tenant_id AND k.id = l.book_id
    WHERE l.tenant_id = t AND l.book_id = b AND l.id = lid AND coalesce(l.native_currency,k.currency) = unit
), converted AS (
    SELECT direction, CASE WHEN target_scale >= source_scale THEN amount * power(10::numeric,target_scale-source_scale)
        WHEN mod(amount,power(10::numeric,source_scale-target_scale)) = 0 THEN amount / power(10::numeric,source_scale-target_scale) END AS amount
    FROM movement
)
SELECT trim_scale(amount * direction) FROM converted
$$;

-- Coverage follows a duplicate's canonical observation without allocating the
-- same posted money twice. A review uses its recorded time; current reads use
-- infinity so a later revocation or reversal releases the explanation.
-- Statement coverage and allocation read a whole statement's rows and its
-- account's lines. They check the caller's access to the Book once, then read
-- as their owner, so row security isn't re-evaluated for every row they join.
CREATE FUNCTION public.statement_read_check(t uuid, b uuid) RETURNS void
    LANGUAGE plpgsql STABLE SET search_path = pg_catalog AS $$
BEGIN
    IF NOT (public.ledger_import_owner() OR public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN
        RAISE EXCEPTION 'selected Book required' USING ERRCODE = '42501';
    END IF;
END $$;

CREATE FUNCTION public.statement_row_coverages(t uuid, b uuid, sid uuid, at_time timestamptz)
    RETURNS TABLE(row_id uuid, covered_minor numeric)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
#variable_conflict use_column
BEGIN
    PERFORM public.statement_read_check(t, b);
    RETURN QUERY
WITH observed AS MATERIALIZED (
    SELECT r.id AS row_id, c.id AS canonical_id, c.lifecycle, c.amount_minor FROM public.statement_rows r
    LEFT JOIN public.statement_duplicates d ON d.tenant_id = r.tenant_id AND d.book_id = r.book_id
        AND d.row_id = r.id AND d.recorded_at <= at_time
    JOIN public.statement_rows c ON c.tenant_id = r.tenant_id AND c.book_id = r.book_id
        AND c.id = coalesce(d.canonical_row_id, r.id)
    WHERE r.tenant_id = t AND r.book_id = b AND r.statement_id = sid
), canonical AS MATERIALIZED (
    SELECT DISTINCT o.canonical_id FROM observed o
), relevant_matches AS MATERIALIZED (
    SELECT DISTINCT m.id FROM canonical c JOIN public.statement_match_parts p
        ON p.tenant_id = t AND p.book_id = b AND p.row_id = c.canonical_id
    JOIN public.statement_matches m ON m.tenant_id = p.tenant_id AND m.book_id = p.book_id AND m.id = p.match_id
    WHERE m.recorded_at <= at_time
        AND NOT EXISTS(SELECT 1 FROM public.statement_match_revocations v
            WHERE v.tenant_id = t AND v.book_id = b AND v.match_id = m.id AND v.recorded_at <= at_time)
), entry_ids AS MATERIALIZED (
    SELECT l.entry_id FROM relevant_matches m JOIN public.statement_match_parts p
        ON p.tenant_id = t AND p.book_id = b AND p.match_id = m.id
    JOIN public.lines l ON l.tenant_id = p.tenant_id AND l.book_id = p.book_id AND l.id = p.line_id
    UNION
    SELECT a.entry_id FROM canonical c JOIN public.statement_adjustments a
        ON a.tenant_id = t AND a.book_id = b AND a.row_id = c.canonical_id
    WHERE a.recorded_at <= at_time
), eligible_entries AS MATERIALIZED (
    SELECT e.id FROM entry_ids i JOIN public.entries e ON e.tenant_id = t AND e.book_id = b AND e.id = i.entry_id
    WHERE e.reversal_of IS NULL AND NOT EXISTS(SELECT 1 FROM public.entries v
        WHERE v.tenant_id = t AND v.book_id = b AND v.reversal_of = e.id AND v.recorded_at <= at_time)
), eligible_matches AS MATERIALIZED (
    -- Validate every posted leg once per match, even when many rows share it.
    SELECT m.id FROM relevant_matches m WHERE NOT EXISTS(
        SELECT 1 FROM public.statement_match_parts p JOIN public.lines l
            ON l.tenant_id = p.tenant_id AND l.book_id = p.book_id AND l.id = p.line_id
        LEFT JOIN eligible_entries e ON e.id = l.entry_id
        WHERE p.tenant_id = t AND p.book_id = b AND p.match_id = m.id AND e.id IS NULL)
), matched AS (
    SELECT p.row_id, sum(p.amount_minor::numeric) AS amount FROM eligible_matches m
    JOIN public.statement_match_parts p ON p.tenant_id = t AND p.book_id = b AND p.match_id = m.id
    JOIN canonical c ON c.canonical_id = p.row_id GROUP BY p.row_id
), adjusted AS (
    SELECT a.row_id FROM canonical c JOIN public.statement_adjustments a
        ON a.tenant_id = t AND a.book_id = b AND a.row_id = c.canonical_id
    JOIN eligible_entries e ON e.id = a.entry_id WHERE a.recorded_at <= at_time
)
SELECT o.row_id, CASE WHEN o.lifecycle IN ('pending', 'cancelled') THEN 0
    WHEN a.row_id IS NOT NULL THEN o.amount_minor::numeric ELSE coalesce(m.amount, 0) END
FROM observed o LEFT JOIN adjusted a ON a.row_id = o.canonical_id
    LEFT JOIN matched m ON m.row_id = o.canonical_id;
END
$$;

CREATE FUNCTION public.statement_explained(t uuid, b uuid, sid uuid, at_time timestamptz) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
WITH coverage AS MATERIALIZED (SELECT * FROM public.statement_row_coverages(t, b, sid, at_time))
SELECT s.evidence_state = 'complete'
    AND s.opening_balance_minor::numeric + coalesce(sum(r.amount_minor::numeric), 0) = s.closing_balance_minor::numeric
    AND coalesce(bool_and(c.covered_minor = r.amount_minor), true)
FROM public.statements s LEFT JOIN public.statement_rows r ON r.tenant_id = s.tenant_id AND r.book_id = s.book_id
    AND r.statement_id = s.id AND r.lifecycle NOT IN ('pending', 'cancelled')
LEFT JOIN coverage c ON c.row_id = r.id
WHERE s.tenant_id = t AND s.book_id = b AND s.id = sid
GROUP BY s.id
$$;

CREATE FUNCTION public.statement_current_reconciled(t uuid, b uuid, sid uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce((SELECT r.outcome = 'reconciled' FROM public.statement_reviews r
    WHERE r.tenant_id = t AND r.book_id = b AND r.statement_id = sid ORDER BY r.revision DESC LIMIT 1), false)
    AND coalesce(public.statement_explained(t, b, sid, 'infinity'::timestamptz), false)
$$;

-- Matches and adjustments consume the same posted movements. With a cutoff,
-- only observations dated by that date cover the line. A
-- combined match counts as reviewed only when every contributing statement is
-- currently reconciled; no row-to-line allocation is invented within a match.
CREATE FUNCTION public.statement_line_allocations(t uuid, b uuid, lids uuid[], unit text, target_scale integer, cutoff date, reviewed_statements uuid[])
    RETURNS TABLE(line_id uuid, allocated_minor numeric)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
#variable_conflict use_column
BEGIN
    PERFORM public.statement_read_check(t, b);
    RETURN QUERY
WITH requested AS MATERIALIZED (
    SELECT DISTINCT unnest(lids) AS line_id
), selected_lines AS MATERIALIZED (
    SELECT l.id, l.entry_id, l.account_id FROM public.lines l
    WHERE l.tenant_id = t AND l.book_id = b AND l.id = ANY(lids)
), line_parts AS MATERIALIZED (
    SELECT p.match_id, p.line_id, p.amount_minor FROM selected_lines l JOIN public.statement_match_parts p
        ON p.tenant_id = t AND p.book_id = b AND p.line_id = l.id
    WHERE NOT EXISTS(SELECT 1 FROM public.statement_match_revocations v
        WHERE v.tenant_id = t AND v.book_id = b AND v.match_id = p.match_id)
), relevant_matches AS MATERIALIZED (
    SELECT DISTINCT p.match_id FROM line_parts p
), match_units AS MATERIALIZED (
    -- All observations share this unit, as enforced by the match's final guard.
    SELECT m.match_id, s.scale FROM relevant_matches m
    CROSS JOIN LATERAL(SELECT p.row_id FROM public.statement_match_parts p
        WHERE p.tenant_id = t AND p.book_id = b AND p.match_id = m.match_id AND p.row_id IS NOT NULL
        ORDER BY p.row_id LIMIT 1) first_observation
    CROSS JOIN LATERAL(SELECT r.statement_id FROM public.statement_rows r
        WHERE r.tenant_id = t AND r.book_id = b AND r.id = first_observation.row_id LIMIT 1) observed_statement
    JOIN public.statements s ON s.tenant_id = t AND s.book_id = b AND s.id = observed_statement.statement_id
    WHERE s.currency = unit
), observation_parts AS MATERIALIZED (
    SELECT p.match_id, p.row_id, p.amount_minor
    FROM match_units m JOIN public.statement_match_parts p
        ON p.tenant_id = t AND p.book_id = b AND p.match_id = m.match_id
    WHERE p.row_id IS NOT NULL AND (cutoff IS NOT NULL OR reviewed_statements IS NOT NULL)
), observation_rows AS MATERIALIZED (
    SELECT r.id,r.statement_id,r.occurred_on FROM public.statement_rows r
    WHERE r.tenant_id = t AND r.book_id = b
        AND r.id = ANY(ARRAY(SELECT p.row_id FROM observation_parts p))
), observations AS MATERIALIZED (
    SELECT p.match_id,p.amount_minor,r.id AS row_id,r.statement_id,r.occurred_on
    FROM observation_parts p JOIN observation_rows r ON r.id = p.row_id
), reviewed_canonicals AS MATERIALIZED (
    SELECT DISTINCT d.canonical_row_id FROM public.statement_duplicates d JOIN public.statement_rows r
        ON r.tenant_id = d.tenant_id AND r.book_id = d.book_id AND r.id = d.row_id
    WHERE reviewed_statements IS NOT NULL AND d.tenant_id = t AND d.book_id = b
        AND r.statement_id = ANY(reviewed_statements) AND r.lifecycle NOT IN ('pending', 'cancelled')
), match_facts AS MATERIALIZED (
    SELECT m.match_id, m.scale, coalesce(bool_and(o.occurred_on <= cutoff), true) AS all_seen,
        coalesce(bool_or(o.occurred_on <= cutoff), false) AS any_seen,
        sum(o.amount_minor::numeric) FILTER (WHERE o.occurred_on <= cutoff) AS observed,
        coalesce(bool_and(reviewed_statements IS NULL OR o.statement_id = ANY(reviewed_statements)
            OR d.canonical_row_id IS NOT NULL), true) AS reviewed
    FROM match_units m LEFT JOIN observations o ON o.match_id = m.match_id
    LEFT JOIN reviewed_canonicals d ON d.canonical_row_id = o.row_id GROUP BY m.match_id, m.scale
), mixed_line_counts AS MATERIALIZED (
    SELECT p.match_id, count(*) AS line_count FROM match_facts m JOIN public.statement_match_parts p
        ON p.tenant_id = t AND p.book_id = b AND p.match_id = m.match_id AND p.line_id IS NOT NULL
    WHERE cutoff IS NOT NULL AND m.any_seen AND NOT m.all_seen GROUP BY p.match_id
), covered AS MATERIALIZED (
    SELECT p.line_id, sum((CASE WHEN cutoff IS NULL OR m.all_seen THEN p.amount_minor::numeric
        WHEN NOT m.any_seen THEN 0 WHEN n.line_count = 1 THEN m.observed END)
        * CASE WHEN target_scale >= m.scale THEN power(10::numeric,target_scale-m.scale) ELSE 1 END
        / CASE WHEN target_scale < m.scale THEN power(10::numeric,m.scale-target_scale) ELSE 1 END) AS amount,
        coalesce(bool_or(cutoff IS NOT NULL AND m.any_seen AND NOT m.all_seen AND n.line_count <> 1), false) AS ambiguous
    FROM line_parts p JOIN match_facts m ON m.match_id = p.match_id AND m.reviewed
    LEFT JOIN mixed_line_counts n ON n.match_id = m.match_id GROUP BY p.line_id
), adjustment_rows AS MATERIALIZED (
    SELECT a.entry_id, r.id AS row_id, r.statement_id, s.account_id FROM public.statement_adjustments a
    JOIN public.statement_rows r ON r.tenant_id = a.tenant_id AND r.book_id = a.book_id AND r.id = a.row_id
    JOIN public.statements s ON s.tenant_id = r.tenant_id AND s.book_id = r.book_id AND s.id = r.statement_id
    WHERE a.tenant_id = t AND a.book_id = b AND s.currency = unit
        AND (cutoff IS NULL OR r.occurred_on <= cutoff)
        AND EXISTS(SELECT 1 FROM selected_lines l WHERE l.entry_id = a.entry_id)
        AND (reviewed_statements IS NULL OR s.id = ANY(reviewed_statements)
            OR EXISTS(SELECT 1 FROM reviewed_canonicals d WHERE d.canonical_row_id = r.id))
), adjusted_entries AS MATERIALIZED (
    SELECT DISTINCT a.entry_id FROM adjustment_rows a
), eligible_adjusted_entries AS MATERIALIZED (
    SELECT e.id FROM adjusted_entries a JOIN public.entries e
        ON e.tenant_id = t AND e.book_id = b AND e.id = a.entry_id
    WHERE e.reversal_of IS NULL AND NOT EXISTS(SELECT 1 FROM public.entries v
        WHERE v.tenant_id = t AND v.book_id = b AND v.reversal_of = e.id)
), adjusted AS (
    SELECT l.id AS line_id, sum(public.statement_line_amount(t, b, l.id, unit, target_scale)) AS amount
    FROM selected_lines l JOIN adjustment_rows a ON a.entry_id = l.entry_id AND a.account_id = l.account_id
    JOIN eligible_adjusted_entries e ON e.id = a.entry_id GROUP BY l.id
)
SELECT r.line_id, trim_scale(CASE WHEN coalesce(c.ambiguous, false) THEN NULL
    ELSE coalesce(c.amount, 0) + coalesce(a.amount, 0) END)
FROM requested r LEFT JOIN covered c ON c.line_id = r.line_id LEFT JOIN adjusted a ON a.line_id = r.line_id;
END
$$;

-- Single-line capacity checks share the same allocation rules as bulk reads.
CREATE FUNCTION public.statement_line_allocated(t uuid, b uuid, lid uuid, unit text, target_scale integer, cutoff date, reviewed_statements uuid[]) RETURNS numeric
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT a.allocated_minor FROM public.statement_line_allocations(t, b, ARRAY[lid], unit, target_scale, cutoff, reviewed_statements) a
$$;

CREATE FUNCTION public.statement_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE account public.accounts%ROWTYPE; book public.books%ROWTYPE;
BEGIN
    SELECT * INTO account FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.account_id;
    SELECT * INTO book FROM public.books WHERE tenant_id = NEW.tenant_id AND id = NEW.book_id;
    -- Restored statements keep the role and unit they were recorded with.
    IF NOT public.ledger_restoring(NEW.tenant_id, NEW.book_id) THEN
        IF account.role IS DISTINCT FROM NEW.kind THEN
            RAISE EXCEPTION 'a statement belongs to a cash or card account of the same role' USING ERRCODE = '23514';
        END IF;
        IF NEW.currency IS DISTINCT FROM coalesce(account.currency, book.currency)
            OR NEW.scale IS DISTINCT FROM coalesce(account.native_scale, book.scale) THEN
            RAISE EXCEPTION 'statement amounts are in the account currency' USING ERRCODE = '23514';
        END IF;
    END IF;
    PERFORM public.document_use(NEW.tenant_id, NEW.book_id, NEW.document_id);
    RETURN NEW;
END $$;
CREATE FUNCTION public.statement_row_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF NOT public.ledger_import_owner() AND NOT public.ledger_written_here((SELECT s.xmin FROM public.statements s
        WHERE s.tenant_id = NEW.tenant_id AND s.book_id = NEW.book_id AND s.id = NEW.statement_id)) THEN
        RAISE EXCEPTION 'statement rows are recorded with their statement' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

-- A reversed entry and its reversal cancel out; neither is statement money.
CREATE FUNCTION public.statement_entry_reversed(t uuid, b uuid, eid uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
-- Two lookups, so each uses an index: the entry itself, then its reversal.
SELECT EXISTS(SELECT 1 FROM public.entries e WHERE e.tenant_id = t AND e.book_id = b
        AND e.id = eid AND e.reversal_of IS NOT NULL)
    OR EXISTS(SELECT 1 FROM public.entries e WHERE e.tenant_id = t AND e.book_id = b AND e.reversal_of = eid)
$$;
-- A matched entry is reversed only after its match is revoked.
CREATE FUNCTION public.statement_reversal_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.reversal_of IS NOT NULL AND NOT public.ledger_restoring(NEW.tenant_id, NEW.book_id) AND EXISTS(
        SELECT 1 FROM public.statement_match_parts p JOIN public.lines l
            ON l.tenant_id = p.tenant_id AND l.book_id = p.book_id AND l.id = p.line_id
        WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id AND l.entry_id = NEW.reversal_of
        AND NOT EXISTS(SELECT 1 FROM public.statement_match_revocations v
            WHERE v.tenant_id = p.tenant_id AND v.book_id = p.book_id AND v.match_id = p.match_id)) THEN
        RAISE EXCEPTION 'revoke the statement match before reversing the entry' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER statement_reversal BEFORE INSERT ON public.entries
    FOR EACH ROW EXECUTE FUNCTION public.statement_reversal_guard();

-- One row's current allocation: it is used at most up to its amount and never
-- against its sign; pending, cancelled and duplicate rows are never used; a
-- row is either matched or adjusted. Revoked matches no longer count.
CREATE FUNCTION public.statement_row_check(t uuid, b uuid, rid uuid) RETURNS void
    LANGUAGE plpgsql STABLE SET search_path = pg_catalog AS $$
DECLARE observed public.statement_rows%ROWTYPE; allocated numeric; matched boolean; adjusted boolean; duplicate boolean;
BEGIN
    SELECT * INTO STRICT observed FROM public.statement_rows WHERE tenant_id = t AND book_id = b AND id = rid;
    SELECT coalesce(sum(p.amount_minor::numeric), 0), count(*) > 0 INTO allocated, matched
        FROM public.statement_match_parts p WHERE p.tenant_id = t AND p.book_id = b AND p.row_id = rid
        AND NOT EXISTS(SELECT 1 FROM public.statement_match_revocations v
            WHERE v.tenant_id = t AND v.book_id = b AND v.match_id = p.match_id);
    adjusted := EXISTS(SELECT 1 FROM public.statement_adjustments a WHERE a.tenant_id = t AND a.book_id = b
        AND a.row_id = rid AND NOT public.statement_entry_reversed(t, b, a.entry_id));
    duplicate := EXISTS(SELECT 1 FROM public.statement_duplicates d WHERE d.tenant_id = t AND d.book_id = b AND d.row_id = rid);
    IF (matched OR adjusted) AND (observed.lifecycle IN ('pending', 'cancelled') OR duplicate) THEN
        RAISE EXCEPTION 'pending, cancelled and duplicate statement rows cannot be matched or adjusted' USING ERRCODE = '23514';
    END IF;
    IF matched AND adjusted THEN
        RAISE EXCEPTION 'a statement row is explained by a match or an adjustment, not both' USING ERRCODE = '23514';
    END IF;
    IF allocated * sign(observed.amount_minor) < 0 OR abs(allocated) > abs(observed.amount_minor) THEN
        RAISE EXCEPTION 'statement row is allocated beyond its amount' USING ERRCODE = '23514';
    END IF;
END $$;
CREATE FUNCTION public.statement_line_check(t uuid, b uuid, lid uuid, unit text, target_scale integer) RETURNS void
    LANGUAGE plpgsql STABLE SET search_path = pg_catalog AS $$
DECLARE comparable numeric; allocated numeric;
BEGIN
    comparable := public.statement_line_amount(t, b, lid, unit, target_scale);
    allocated := public.statement_line_allocated(t, b, lid, unit, target_scale, NULL, NULL);
    IF comparable IS NULL OR comparable = 0 OR allocated * sign(comparable) < 0 OR abs(allocated) > abs(comparable) THEN
        RAISE EXCEPTION 'posted line is allocated beyond its amount' USING ERRCODE = '23514';
    END IF;
END $$;

CREATE FUNCTION public.statement_duplicate_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT EXISTS(SELECT 1 FROM public.statement_rows r JOIN public.statements s
            ON s.tenant_id = r.tenant_id AND s.book_id = r.book_id AND s.id = r.statement_id
        JOIN public.statement_rows c ON c.tenant_id = r.tenant_id AND c.book_id = r.book_id AND c.id = NEW.canonical_row_id
        JOIN public.statements cs ON cs.tenant_id = c.tenant_id AND cs.book_id = c.book_id AND cs.id = c.statement_id
        WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id AND r.id = NEW.row_id
        AND s.account_id = cs.account_id AND s.currency = cs.currency AND s.scale = cs.scale AND r.amount_minor = c.amount_minor) THEN
        RAISE EXCEPTION 'a duplicate repeats a row of the same account and amount' USING ERRCODE = '23514';
    END IF;
    IF EXISTS(SELECT 1 FROM public.statement_rows r JOIN public.statement_rows c
        ON c.tenant_id = r.tenant_id AND c.book_id = r.book_id AND c.id = NEW.canonical_row_id
        WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id AND r.id = NEW.row_id
        AND r.statement_id = c.statement_id) THEN
        RAISE EXCEPTION 'a duplicate repeats an observation from another statement' USING ERRCODE = '23514';
    END IF;
    IF EXISTS(SELECT 1 FROM public.statement_duplicates d JOIN public.statement_rows previous
        ON previous.tenant_id = d.tenant_id AND previous.book_id = d.book_id AND previous.id = d.row_id
        JOIN public.statement_rows current_row ON current_row.tenant_id = previous.tenant_id AND current_row.book_id = previous.book_id
        AND current_row.id = NEW.row_id AND current_row.statement_id = previous.statement_id
        WHERE d.tenant_id = NEW.tenant_id AND d.book_id = NEW.book_id AND d.canonical_row_id = NEW.canonical_row_id) THEN
        RAISE EXCEPTION 'a statement observes the same canonical row only once' USING ERRCODE = '23514';
    END IF;
    IF EXISTS(SELECT 1 FROM public.statement_duplicates d WHERE d.tenant_id = NEW.tenant_id AND d.book_id = NEW.book_id
            AND (d.row_id = NEW.canonical_row_id OR d.canonical_row_id = NEW.row_id)) THEN
        RAISE EXCEPTION 'a duplicate points at a row that is not itself a duplicate' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE FUNCTION public.statement_duplicate_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    PERFORM public.statement_row_check(NEW.tenant_id, NEW.book_id, NEW.row_id);
    RETURN NULL;
END $$;

CREATE FUNCTION public.statement_match_part_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF NOT public.ledger_import_owner() AND NOT public.ledger_written_here((SELECT m.xmin FROM public.statement_matches m
        WHERE m.tenant_id = NEW.tenant_id AND m.book_id = NEW.book_id AND m.id = NEW.match_id)) THEN
        RAISE EXCEPTION 'match parts are recorded with their match' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
-- A match is one decision about one account and unit. Its row parts and line
-- parts have equal signed totals, and every part keeps its source's sign.
CREATE FUNCTION public.statement_match_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE unit text; unit_scale integer; units integer; row_total numeric; line_total numeric; row_parts integer; line_parts integer; part record;
BEGIN
    SELECT count(DISTINCT ROW(s.currency,s.scale)), min(s.currency), min(s.scale) INTO units, unit, unit_scale
        FROM public.statement_match_parts p
        JOIN public.statement_rows r ON r.tenant_id = p.tenant_id AND r.book_id = p.book_id AND r.id = p.row_id
        JOIN public.statements s ON s.tenant_id = r.tenant_id AND s.book_id = r.book_id AND s.id = r.statement_id
        WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id AND p.match_id = NEW.id;
    SELECT count(*) FILTER (WHERE row_id IS NOT NULL), count(*) FILTER (WHERE line_id IS NOT NULL),
        coalesce(sum(amount_minor::numeric) FILTER (WHERE row_id IS NOT NULL), 0),
        coalesce(sum(amount_minor::numeric) FILTER (WHERE line_id IS NOT NULL), 0)
        INTO row_parts, line_parts, row_total, line_total
        FROM public.statement_match_parts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND match_id = NEW.id;
    IF row_parts = 0 OR line_parts = 0 OR units <> 1 THEN
        RAISE EXCEPTION 'a match joins statement rows and posted lines in one unit' USING ERRCODE = '23514';
    END IF;
    IF row_total <> line_total THEN
        RAISE EXCEPTION 'matched statement rows and posted lines must have equal totals' USING ERRCODE = '23514';
    END IF;
    FOR part IN SELECT p.row_id, p.line_id, p.amount_minor, r.amount_minor AS row_amount, s.account_id AS row_account,
            l.account_id AS line_account
        FROM public.statement_match_parts p
        LEFT JOIN public.statement_rows r ON r.tenant_id = p.tenant_id AND r.book_id = p.book_id AND r.id = p.row_id
        LEFT JOIN public.statements s ON s.tenant_id = r.tenant_id AND s.book_id = r.book_id AND s.id = r.statement_id
        LEFT JOIN public.lines l ON l.tenant_id = p.tenant_id AND l.book_id = p.book_id AND l.id = p.line_id
        WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id AND p.match_id = NEW.id
        ORDER BY p.row_id IS NULL, p.part LOOP
        IF coalesce(part.row_account, part.line_account) IS DISTINCT FROM NEW.account_id THEN
            RAISE EXCEPTION 'every part of a match is on the matched account' USING ERRCODE = '23514';
        END IF;
        IF part.row_id IS NOT NULL THEN
            IF sign(part.amount_minor) <> sign(part.row_amount) THEN
                RAISE EXCEPTION 'a match part keeps the sign of its statement row' USING ERRCODE = '23514';
            END IF;
            PERFORM public.statement_row_check(NEW.tenant_id, NEW.book_id, part.row_id);
        ELSE
            -- Restored matches were made before any later reversal.
            IF NOT public.ledger_restoring(NEW.tenant_id, NEW.book_id)
                AND public.statement_entry_reversed(NEW.tenant_id, NEW.book_id, (SELECT l.entry_id FROM public.lines l
                WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.id = part.line_id)) THEN
                RAISE EXCEPTION 'lines of a reversed or reversing entry cannot be matched' USING ERRCODE = '23514';
            END IF;
            IF sign(part.amount_minor) IS DISTINCT FROM sign(public.statement_line_amount(NEW.tenant_id, NEW.book_id, part.line_id, unit, unit_scale)) THEN
                RAISE EXCEPTION 'a match part keeps the sign of its posted line' USING ERRCODE = '23514';
            END IF;
            PERFORM public.statement_line_check(NEW.tenant_id, NEW.book_id, part.line_id, unit, unit_scale);
        END IF;
    END LOOP;
    RETURN NULL;
END $$;

-- The adjustment entry moves the statement account by exactly the row amount.
CREATE FUNCTION public.statement_adjustment_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE observed record; moved numeric; lines_on integer; comparable_lines integer; movement record;
BEGIN
    SELECT r.amount_minor, s.account_id, s.currency, s.scale INTO STRICT observed FROM public.statement_rows r
        JOIN public.statements s ON s.tenant_id = r.tenant_id AND s.book_id = r.book_id AND s.id = r.statement_id
        WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id AND r.id = NEW.row_id;
    SELECT count(*), count(public.statement_line_amount(l.tenant_id, l.book_id, l.id, observed.currency, observed.scale)),
        sum(public.statement_line_amount(l.tenant_id, l.book_id, l.id, observed.currency, observed.scale))
        INTO lines_on, comparable_lines, moved
        FROM public.lines l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id
        AND l.entry_id = NEW.entry_id AND l.account_id = observed.account_id;
    IF NOT public.ledger_restoring(NEW.tenant_id, NEW.book_id)
        AND public.statement_entry_reversed(NEW.tenant_id, NEW.book_id, NEW.entry_id) THEN
        RAISE EXCEPTION 'a reversed or reversing entry cannot adjust a statement' USING ERRCODE = '23514';
    END IF;
    IF lines_on = 0 OR comparable_lines <> lines_on OR moved <> observed.amount_minor THEN
        RAISE EXCEPTION 'an adjustment entry moves the statement account by the row amount' USING ERRCODE = '23514';
    END IF;
    PERFORM public.statement_row_check(NEW.tenant_id, NEW.book_id, NEW.row_id);
    FOR movement IN SELECT l.id, public.statement_line_amount(l.tenant_id, l.book_id, l.id, observed.currency, observed.scale) AS amount
        FROM public.lines l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id
        AND l.entry_id = NEW.entry_id AND l.account_id = observed.account_id LOOP
        IF movement.amount <> 0 THEN
            PERFORM public.statement_line_check(NEW.tenant_id, NEW.book_id, movement.id, observed.currency, observed.scale);
        END IF;
    END LOOP;
    RETURN NULL;
END $$;

CREATE FUNCTION public.statement_review_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE prior bigint;
BEGIN
    SELECT coalesce(max(revision), 0) INTO prior FROM public.statement_reviews
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND statement_id = NEW.statement_id;
    IF NEW.revision <> prior + 1 THEN
        RAISE EXCEPTION 'stale statement review' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
-- 'reconciled' is accepted only when, as of the review, the opening balance
-- plus every settled observation equals the closing balance and each is
-- explained, directly or through a duplicate's canonical row.
CREATE FUNCTION public.statement_review_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.outcome <> 'reconciled' THEN
        RETURN NULL;
    END IF;
    IF NOT coalesce(public.statement_explained(NEW.tenant_id, NEW.book_id, NEW.statement_id, NEW.recorded_at), false) THEN
        RAISE EXCEPTION 'statement is not fully explained; record it as needs_work' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;

REVOKE ALL ON FUNCTION public.statement_read_check(uuid,uuid), public.statement_row_coverages(uuid,uuid,uuid,timestamptz),
    public.statement_explained(uuid,uuid,uuid,timestamptz), public.statement_current_reconciled(uuid,uuid,uuid),
    public.statement_line_allocated(uuid,uuid,uuid,text,integer,date,uuid[]),
    public.statement_line_allocations(uuid,uuid,uuid[],text,integer,date,uuid[]) FROM PUBLIC;

-- E. Periods never partly overlap: a new period may contain earlier ones,
-- such as a year after its monthly closes. A date stays closed while any
-- period containing it is closed. A period kept from the original close
-- record has its identifier, dates and time.
CREATE FUNCTION public.accounting_period_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE original public.closed_periods%ROWTYPE;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF EXISTS(SELECT 1 FROM public.accounting_periods p WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id
        AND p.start_date <= NEW.end_date AND p.end_date >= NEW.start_date
        AND NOT (p.start_date >= NEW.start_date AND p.end_date <= NEW.end_date
            AND (p.start_date, p.end_date) IS DISTINCT FROM (NEW.start_date, NEW.end_date))) THEN
        RAISE EXCEPTION 'period overlaps an existing accounting period' USING ERRCODE = '23514';
    END IF;
    SELECT * INTO original FROM public.closed_periods WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.id;
    IF FOUND THEN
        IF ROW(original.start_date, original.end_date) IS DISTINCT FROM ROW(NEW.start_date, NEW.end_date) THEN
            RAISE EXCEPTION 'period differs from its original close' USING ERRCODE = '23514';
        END IF;
        NEW.recorded_at := original.closed_at;
    ELSIF NOT public.ledger_import_owner() THEN
        NEW.recorded_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE FUNCTION public.accounting_period_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT EXISTS(SELECT 1 FROM public.accounting_period_events e WHERE e.tenant_id = NEW.tenant_id
        AND e.book_id = NEW.book_id AND e.period_id = NEW.id AND e.revision = 1) THEN
        RAISE EXCEPTION 'a period is created by its first close' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
-- The owner acting directly: a fresh owner sign-in as the owner identity, in
-- the selected Book, with no agent connection.
CREATE FUNCTION access.owner_decision_allowed(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
    SELECT access.owner_document_allowed(t, b) AND access.owner_write_allowed(t)
$$;
REVOKE ALL ON FUNCTION access.owner_decision_allowed(uuid, uuid) FROM PUBLIC;
-- The owner's own decision: a fresh owner session acting directly, or the
-- owner approving an agent's proposal in this transaction. An operator
-- import also qualifies.
CREATE FUNCTION public.ledger_owner_decision(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT public.ledger_import_owner()
    OR CASE WHEN coalesce(current_setting('vult.owner_posting', true), '') = ''
        THEN has_function_privilege(session_user, 'access.owner_identity(uuid)'::regprocedure, 'EXECUTE')
            AND access.owner_decision_allowed(t, b)
        ELSE access.owner_posting_allowed(t, b) END
$$;
-- An entry the review before a close asks about: it records income or an
-- expense, and is neither a reversal nor reversed. Transfers between the
-- Book's own accounts and opening balances post to neither, so they never
-- need a payee or evidence here.
CREATE FUNCTION public.period_review_entry(t uuid, b uuid, eid uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT EXISTS(SELECT 1 FROM public.lines l JOIN public.accounts a
        ON a.tenant_id = l.tenant_id AND a.book_id = l.book_id AND a.id = l.account_id
        WHERE l.tenant_id = t AND l.book_id = b AND l.entry_id = eid AND a.class IN ('income', 'expense'))
    AND NOT public.statement_entry_reversed(t, b, eid)
$$;
-- What the review before a close lists: cash and card statements in the
-- period that are not reconciled, and income or expense entries in it with
-- no evidence or no payee decision.
CREATE FUNCTION public.period_open_items(t uuid, b uuid, first date, last date) RETURNS jsonb
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT jsonb_build_object(
    'unreconciled_statements', (SELECT count(*) FROM public.statements s
        WHERE s.tenant_id = t AND s.book_id = b AND s.period_start <= last AND s.period_end >= first
        AND NOT public.statement_current_reconciled(t, b, s.id)),
    'entries_without_evidence', (SELECT count(*) FROM public.entries e
        WHERE e.tenant_id = t AND e.book_id = b AND e.accounting_date BETWEEN first AND last
        AND public.period_review_entry(t, b, e.id)
        AND NOT EXISTS(SELECT 1 FROM public.source_citations c WHERE c.tenant_id = e.tenant_id AND c.book_id = e.book_id AND c.entry_id = e.id)
        AND NOT EXISTS(SELECT 1 FROM public.evidence_links l WHERE l.tenant_id = e.tenant_id AND l.book_id = e.book_id
            AND l.entry_id = e.id AND l.action = 'attach' AND NOT EXISTS(SELECT 1 FROM public.evidence_links later
                WHERE later.tenant_id = l.tenant_id AND later.book_id = l.book_id AND later.link_id = l.link_id
                AND later.revision > l.revision))),
    'entries_without_payee', (SELECT count(*) FROM public.entries e
        WHERE e.tenant_id = t AND e.book_id = b AND e.accounting_date BETWEEN first AND last
        AND public.period_review_entry(t, b, e.id)
        AND NOT EXISTS(SELECT 1 FROM public.entry_payees p WHERE p.tenant_id = e.tenant_id AND p.book_id = e.book_id AND p.entry_id = e.id)))
$$;

-- A period proposal is a read model, not a temporary period decision. This
-- validates its state without giving the preparing connection owner authority.
CREATE FUNCTION public.accounting_period_preview(t uuid, b uuid, op text, args jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SET search_path = pg_catalog AS $$
DECLARE period public.accounting_periods%ROWTYPE; prior public.accounting_period_events%ROWTYPE;
    first date; last date; reason text := coalesce(args->>'reason', ''); items jsonb;
BEGIN
    IF NOT public.ledger_allowed(t, b, false) THEN
        RAISE EXCEPTION 'selected Book required' USING ERRCODE = '42501';
    END IF;
    IF length(reason) > 4000 THEN
        RAISE EXCEPTION 'invalid period reason' USING ERRCODE = '23514';
    END IF;
    IF op = 'close_period' THEN
        first := (args->>'from')::date;
        last := (args->>'through')::date;
        IF first IS NULL OR last IS NULL OR first > last
            OR first < DATE '0001-01-01' OR last > DATE '9999-12-31' THEN
            RAISE EXCEPTION 'invalid period dates' USING ERRCODE = '23514';
        END IF;
        SELECT * INTO period FROM public.accounting_periods p
            WHERE p.tenant_id = t AND p.book_id = b AND p.start_date = first AND p.end_date = last;
        IF period.id IS NULL AND EXISTS(SELECT 1 FROM public.accounting_periods p
            WHERE p.tenant_id = t AND p.book_id = b AND p.start_date <= last AND p.end_date >= first
            AND NOT (p.start_date >= first AND p.end_date <= last)) THEN
            RAISE EXCEPTION 'period overlaps an existing accounting period' USING ERRCODE = '23514';
        END IF;
        IF period.id IS NOT NULL THEN
            SELECT * INTO STRICT prior FROM public.accounting_period_events e
                WHERE e.tenant_id = t AND e.book_id = b AND e.period_id = period.id ORDER BY e.revision DESC LIMIT 1;
            IF prior.event = 'closed' THEN
                RAISE EXCEPTION 'this period is already closed' USING ERRCODE = '23514';
            END IF;
        END IF;
        items := public.period_open_items(t, b, first, last);
        IF EXISTS(SELECT 1 FROM jsonb_each_text(items) WHERE value::bigint > 0) AND length(btrim(reason)) = 0 THEN
            RAISE EXCEPTION 'closing with open review items needs a reason' USING ERRCODE = '23514';
        END IF;
        RETURN jsonb_strip_nulls(jsonb_build_object('change', 'close', 'period_id', period.id,
            'from', first::text, 'through', last::text, 'reason', nullif(reason, ''), 'open_items', items));
    ELSIF op = 'reopen_period' THEN
        SELECT * INTO period FROM public.accounting_periods p
            WHERE p.tenant_id = t AND p.book_id = b AND p.id = (args->>'period_id')::uuid;
        IF period.id IS NULL THEN
            RAISE EXCEPTION 'period is unavailable' USING ERRCODE = '23514';
        END IF;
        SELECT * INTO STRICT prior FROM public.accounting_period_events e
            WHERE e.tenant_id = t AND e.book_id = b AND e.period_id = period.id ORDER BY e.revision DESC LIMIT 1;
        IF prior.revision IS DISTINCT FROM (args->>'expected_revision')::bigint THEN
            RAISE EXCEPTION 'stale period revision' USING ERRCODE = '23514';
        END IF;
        IF prior.event <> 'closed' THEN
            RAISE EXCEPTION 'this period is already open' USING ERRCODE = '23514';
        END IF;
        IF length(btrim(reason)) = 0 THEN
            RAISE EXCEPTION 'reopening needs a reason' USING ERRCODE = '23514';
        END IF;
        RETURN jsonb_build_object('change', 'reopen', 'period_id', period.id,
            'from', period.start_date::text, 'through', period.end_date::text, 'reason', reason);
    END IF;
    RAISE EXCEPTION 'invalid period operation' USING ERRCODE = '23514';
END $$;

CREATE FUNCTION public.accounting_period_event_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE prior public.accounting_period_events%ROWTYPE; original public.closed_periods%ROWTYPE; reference jsonb; span record;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    SELECT * INTO prior FROM public.accounting_period_events WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id
        AND period_id = NEW.period_id ORDER BY revision DESC LIMIT 1;
    IF NEW.revision <> coalesce(prior.revision, 0) + 1 OR NEW.event = prior.event THEN
        RAISE EXCEPTION 'period must alternate between closed and reopened' USING ERRCODE = '23514';
    END IF;
    IF NEW.origin = 'closed_period' THEN
        SELECT * INTO original FROM public.closed_periods
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.period_id;
        IF NOT FOUND OR original.closed_at IS DISTINCT FROM NEW.recorded_at THEN
            RAISE EXCEPTION 'only an original close record keeps a close without an actor' USING ERRCODE = '42501';
        END IF;
    ELSIF NOT public.ledger_restoring(NEW.tenant_id, NEW.book_id) THEN
        IF NOT public.ledger_import_owner() THEN
            IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
                RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
            END IF;
            NEW.recorded_at := clock_timestamp();
        END IF;
        -- An agent with write access closes a period with nothing left to
        -- review. Reopening, or closing with open items and a reason, needs
        -- the owner, directly or by approving an agent's proposal.
        IF NEW.event = 'reopened' AND NOT public.ledger_owner_decision(NEW.tenant_id, NEW.book_id) THEN
            RAISE EXCEPTION 'only the owner can reopen a period' USING ERRCODE = '42501';
        END IF;
        IF NEW.event = 'closed' THEN
            SELECT p.start_date, p.end_date INTO span FROM public.accounting_periods p
                WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id AND p.id = NEW.period_id;
            NEW.open_items := public.period_open_items(NEW.tenant_id, NEW.book_id, span.start_date, span.end_date);
            IF EXISTS(SELECT 1 FROM jsonb_each_text(NEW.open_items) WHERE value::bigint > 0) THEN
                IF NOT public.ledger_owner_decision(NEW.tenant_id, NEW.book_id) THEN
                    RAISE EXCEPTION 'only the owner can close a period with open review items' USING ERRCODE = '42501';
                END IF;
                IF length(btrim(NEW.reason)) = 0 THEN
                    RAISE EXCEPTION 'closing with open review items needs a reason' USING ERRCODE = '23514';
                END IF;
            END IF;
        END IF;
    END IF;
    FOR reference IN SELECT value FROM jsonb_array_elements(NEW.statement_reviews) LOOP
        IF jsonb_typeof(reference) <> 'object' OR (SELECT count(*) FROM jsonb_object_keys(reference)) <> 2
            OR NOT EXISTS(SELECT 1 FROM public.statement_reviews v WHERE v.tenant_id = NEW.tenant_id AND v.book_id = NEW.book_id
                AND v.statement_id::text = reference->>'statement_id' AND v.revision::text = reference->>'revision') THEN
            RAISE EXCEPTION 'period decision cites an unavailable statement review' USING ERRCODE = '23514';
        END IF;
    END LOOP;
    RETURN NEW;
END $$;

-- Original close records are replayed only as restored history. New closes
-- record decisions, including their actor, review and any required reason.
CREATE FUNCTION public.closed_period_mirror() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_restoring(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'legacy close records are restored history; record a period decision' USING ERRCODE = '42501';
    END IF;
    IF public.ledger_import_owner() AND coalesce(current_setting('vult.restore_periods', true), '') = 'retained' THEN
        RETURN NULL;
    END IF;
    INSERT INTO public.accounting_periods(tenant_id, book_id, id, start_date, end_date, recorded_at)
        VALUES (NEW.tenant_id, NEW.book_id, NEW.id, NEW.start_date, NEW.end_date, NEW.closed_at);
    INSERT INTO public.accounting_period_events(tenant_id, book_id, period_id, revision, event, origin, recorded_at)
        VALUES (NEW.tenant_id, NEW.book_id, NEW.id, 1, 'closed', 'closed_period', NEW.closed_at);
    RETURN NULL;
END $$;
CREATE TRIGGER closed_period_mirror AFTER INSERT ON public.closed_periods
    FOR EACH ROW EXECUTE FUNCTION public.closed_period_mirror();

CREATE TRIGGER payee_guard BEFORE INSERT ON public.payees FOR EACH ROW EXECUTE FUNCTION public.foundation_record_guard();
CREATE CONSTRAINT TRIGGER payee_final AFTER INSERT ON public.payees DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.payee_final();
CREATE TRIGGER payee_revision_00_record BEFORE INSERT ON public.payee_revisions FOR EACH ROW EXECUTE FUNCTION public.foundation_record_guard();
CREATE TRIGGER payee_revision_guard BEFORE INSERT ON public.payee_revisions FOR EACH ROW EXECUTE FUNCTION public.payee_revision_guard();
CREATE TRIGGER entry_payee_00_record BEFORE INSERT ON public.entry_payees FOR EACH ROW EXECUTE FUNCTION public.foundation_record_guard();
CREATE TRIGGER entry_payee_guard BEFORE INSERT ON public.entry_payees FOR EACH ROW EXECUTE FUNCTION public.entry_payee_guard();
CREATE TRIGGER statement_00_record BEFORE INSERT ON public.statements FOR EACH ROW EXECUTE FUNCTION public.foundation_record_guard();
CREATE TRIGGER statement_guard BEFORE INSERT ON public.statements FOR EACH ROW EXECUTE FUNCTION public.statement_guard();
CREATE TRIGGER statement_row_guard BEFORE INSERT ON public.statement_rows FOR EACH ROW EXECUTE FUNCTION public.statement_row_guard();
CREATE TRIGGER statement_duplicate_00_record BEFORE INSERT ON public.statement_duplicates FOR EACH ROW EXECUTE FUNCTION public.foundation_record_guard();
CREATE TRIGGER statement_duplicate_guard BEFORE INSERT ON public.statement_duplicates FOR EACH ROW EXECUTE FUNCTION public.statement_duplicate_guard();
CREATE CONSTRAINT TRIGGER statement_duplicate_final AFTER INSERT ON public.statement_duplicates DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.statement_duplicate_final();
CREATE TRIGGER statement_match_guard BEFORE INSERT ON public.statement_matches FOR EACH ROW EXECUTE FUNCTION public.foundation_record_guard();
CREATE CONSTRAINT TRIGGER statement_match_final AFTER INSERT ON public.statement_matches DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.statement_match_final();
CREATE TRIGGER statement_match_part_guard BEFORE INSERT ON public.statement_match_parts FOR EACH ROW EXECUTE FUNCTION public.statement_match_part_guard();
CREATE TRIGGER statement_match_revocation_guard BEFORE INSERT ON public.statement_match_revocations FOR EACH ROW EXECUTE FUNCTION public.foundation_record_guard();
CREATE TRIGGER statement_adjustment_guard BEFORE INSERT ON public.statement_adjustments FOR EACH ROW EXECUTE FUNCTION public.foundation_record_guard();
CREATE CONSTRAINT TRIGGER statement_adjustment_final AFTER INSERT ON public.statement_adjustments DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.statement_adjustment_final();
CREATE TRIGGER statement_review_00_record BEFORE INSERT ON public.statement_reviews FOR EACH ROW EXECUTE FUNCTION public.foundation_record_guard();
CREATE TRIGGER statement_review_guard BEFORE INSERT ON public.statement_reviews FOR EACH ROW EXECUTE FUNCTION public.statement_review_guard();
CREATE CONSTRAINT TRIGGER statement_review_final AFTER INSERT ON public.statement_reviews DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.statement_review_final();
CREATE TRIGGER accounting_period_guard BEFORE INSERT ON public.accounting_periods FOR EACH ROW EXECUTE FUNCTION public.accounting_period_guard();
CREATE CONSTRAINT TRIGGER accounting_period_final AFTER INSERT ON public.accounting_periods DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.accounting_period_final();
CREATE TRIGGER accounting_period_event_guard BEFORE INSERT ON public.accounting_period_events FOR EACH ROW EXECUTE FUNCTION public.accounting_period_event_guard();

-- Receipts bind each decision to the operation that recorded it.
CREATE CONSTRAINT TRIGGER payee_receipt AFTER INSERT ON public.payees DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('create_payee');
CREATE CONSTRAINT TRIGGER payee_revision_receipt AFTER INSERT ON public.payee_revisions DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('create_payee', 'revise_payee');
CREATE CONSTRAINT TRIGGER entry_payee_receipt AFTER INSERT ON public.entry_payees DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('set_entry_payee');
CREATE CONSTRAINT TRIGGER statement_receipt AFTER INSERT ON public.statements DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('import_statement');
CREATE CONSTRAINT TRIGGER statement_duplicate_receipt AFTER INSERT ON public.statement_duplicates DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('mark_statement_duplicate');
CREATE CONSTRAINT TRIGGER statement_match_receipt AFTER INSERT ON public.statement_matches DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('match_statement');
CREATE CONSTRAINT TRIGGER statement_match_revocation_receipt AFTER INSERT ON public.statement_match_revocations DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('revoke_statement_match');
CREATE CONSTRAINT TRIGGER statement_adjustment_receipt AFTER INSERT ON public.statement_adjustments DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('post_journal', 'record_statement_adjustment');
CREATE CONSTRAINT TRIGGER statement_review_receipt AFTER INSERT ON public.statement_reviews DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('review_statement');
CREATE CONSTRAINT TRIGGER accounting_period_event_receipt AFTER INSERT ON public.accounting_period_events DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('close_period', 'reopen_period');

-- Row security, immutability and proposal recording for every new table.
-- Connection levels apply as everywhere: a write agent records directly, a
-- proposing agent only through approved proposals, a read agent reads. The
-- period guard keeps reopening and closing with open items for the owner.
DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['payees', 'payee_revisions', 'entry_payees', 'statements', 'statement_rows',
        'statement_duplicates', 'statement_matches', 'statement_match_parts', 'statement_match_revocations',
        'statement_adjustments', 'statement_reviews', 'accounting_periods', 'accounting_period_events'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('CREATE POLICY scoped_read ON public.%I FOR SELECT USING (public.ledger_allowed(tenant_id, book_id, false))', tab);
        EXECUTE format('CREATE POLICY owner_read ON public.%I FOR SELECT USING (access.owner_allowed(tenant_id))', tab);
        EXECUTE format('CREATE POLICY scoped_insert ON public.%I FOR INSERT WITH CHECK (public.ledger_allowed(tenant_id, book_id, true))', tab);
        EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OR DELETE ON public.%I FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable()', tab || '_immutable', tab);
        EXECUTE format('CREATE TRIGGER proposal_record_only AFTER INSERT OR UPDATE ON public.%I FOR EACH ROW EXECUTE FUNCTION public.ledger_proposal_record_only()', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
    END LOOP;
END $$;

CREATE POLICY owner_decision_request ON public.requests FOR INSERT
    WITH CHECK (operation IN ('review_statement', 'close_period', 'reopen_period')
        AND access.owner_decision_allowed(tenant_id, book_id));
CREATE POLICY owner_review ON public.statement_reviews FOR INSERT
    WITH CHECK (access.owner_decision_allowed(tenant_id, book_id));
-- The original close path inserts periods as the owner; the guard keeps its
-- close tied to the original record.
CREATE POLICY owner_period ON public.accounting_periods FOR INSERT
    WITH CHECK (access.owner_write_allowed(tenant_id));
CREATE POLICY owner_period_event ON public.accounting_period_events FOR INSERT
    WITH CHECK (origin = 'closed_period' AND access.owner_write_allowed(tenant_id)
        OR origin = 'decision' AND access.owner_decision_allowed(tenant_id, book_id));

REVOKE ALL ON FUNCTION public.ledger_restoring(uuid, uuid), public.ledger_role_fits(text, text),
    public.ledger_period_closed(uuid, uuid, date), public.statement_line_amount(uuid, uuid, uuid, text, integer),
    public.statement_row_check(uuid, uuid, uuid), public.statement_line_check(uuid, uuid, uuid, text, integer),
    public.ledger_account_retirement(), public.foundation_record_guard(), public.foundation_receipt(),
    public.payee_revision_guard(), public.payee_final(), public.entry_payee_guard(), public.statement_guard(),
    public.statement_row_guard(), public.statement_duplicate_guard(), public.statement_duplicate_final(),
    public.statement_match_part_guard(), public.statement_match_final(), public.statement_adjustment_final(),
    public.statement_review_guard(), public.statement_review_final(), public.accounting_period_guard(),
    public.accounting_period_final(), public.accounting_period_event_guard(), public.closed_period_mirror(),
    public.ledger_owner_decision(uuid, uuid), public.period_open_items(uuid, uuid, date, date), public.accounting_period_preview(uuid, uuid, text, jsonb), public.ledger_written_here(xid),
    public.statement_entry_reversed(uuid, uuid, uuid), public.statement_reversal_guard() FROM PUBLIC;

-- Posting checks the accounting periods instead of the original close records.
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
 IF NOT public.ledger_import_owner() AND public.ledger_period_closed(e.tenant_id,e.book_id,e.accounting_date)
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

-- A prepared entry goes out of date when its date's period is closed.
CREATE OR REPLACE FUNCTION public.operation_dependencies(t uuid, b uuid, op text, args jsonb, excluded uuid)
RETURNS jsonb LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE line_input jsonb; source_input jsonb; accounting_day date; target uuid;
    accounts jsonb; documents jsonb; periods jsonb; reversed boolean;
BEGIN
    IF NOT public.ledger_context(t, b) THEN
        RAISE EXCEPTION 'selected Book required' USING ERRCODE = '42501';
    END IF;
    IF op IN ('create_account', 'revise_account', 'retire_account', 'reactivate_account', 'apply_starter_chart', 'create_payee', 'revise_payee', 'set_entry_payee', 'import_statement', 'mark_statement_duplicate', 'match_statement', 'revoke_statement_match', 'record_statement_adjustment', 'review_statement', 'close_period', 'reopen_period') THEN
        -- These records carry no journal dependencies; their own guards
        -- recheck every rule when the approved work runs. A retirement's
        -- transfer may cite originals, which the proposal retains.
        source_input := CASE WHEN op = 'retire_account' THEN coalesce(args -> 'sources', '[]'::jsonb) ELSE '[]'::jsonb END;
        IF jsonb_typeof(source_input) IS DISTINCT FROM 'array' OR jsonb_array_length(source_input) > 32 THEN
            RAISE EXCEPTION 'invalid operation' USING ERRCODE = '23514';
        END IF;
        SELECT coalesce(jsonb_agg(jsonb_build_object('document_id', d.id::text, 'sha256', d.sha256, 'state', d.state,
                'location', source.value -> 'location', 'active_entries', 0, 'last_citation', 0)
            ORDER BY d.id, (source.value -> 'location')::text), '[]'::jsonb) INTO documents
            FROM jsonb_array_elements(source_input) source JOIN public.documents d
            ON d.tenant_id = t AND d.book_id = b AND d.id = (source.value ->> 'document_id')::uuid;
        IF jsonb_array_length(documents) <> jsonb_array_length(source_input) THEN
            RAISE EXCEPTION 'operation original unavailable' USING ERRCODE = '23514';
        END IF;
        RETURN jsonb_build_object('accounts', '[]'::jsonb, 'documents', documents, 'closed_periods', '[]'::jsonb, 'reversed', false);
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
    -- Only a period whose latest decision is a close blocks the date.
    SELECT coalesce(jsonb_agg(p.id::text ORDER BY p.id), '[]'::jsonb) INTO periods FROM public.accounting_periods p
    WHERE p.tenant_id = t AND p.book_id = b AND accounting_day BETWEEN p.start_date AND p.end_date
    AND (SELECT e.event FROM public.accounting_period_events e WHERE e.tenant_id = p.tenant_id AND e.book_id = p.book_id
        AND e.period_id = p.id ORDER BY e.revision DESC LIMIT 1) = 'closed';
    SELECT EXISTS(SELECT 1 FROM public.entries WHERE tenant_id = t AND book_id = b AND reversal_of = target AND id IS DISTINCT FROM excluded) INTO reversed;
    RETURN jsonb_build_object('accounts', accounts, 'documents', documents, 'closed_periods', periods, 'reversed', reversed);
END $$;

-- A statement is recorded history that keeps its original in use.
CREATE OR REPLACE FUNCTION public.document_used_receipt() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.used_at IS NOT NULL
        AND NOT EXISTS (SELECT 1 FROM public.source_citations WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND document_id = NEW.id)
        AND NOT EXISTS (SELECT 1 FROM public.evidence_links WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND document_id = NEW.id)
        AND NOT EXISTS (SELECT 1 FROM public.statements WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND document_id = NEW.id)
        AND NOT EXISTS (SELECT 1 FROM public.operation_proposals p WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id
            AND convert_from(p.payload_bytes, 'UTF8')::jsonb -> 'dependencies' -> 'documents'
                @> jsonb_build_array(jsonb_build_object('document_id', NEW.id::text))) THEN
        RAISE EXCEPTION 'document use requires recorded history' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;

-- Agents that propose can prepare every accounting foundation change for the
-- owner's approval, as they already do for journal entries.
ALTER TABLE public.operation_proposals DROP CONSTRAINT operation_proposals_operation_check,
    ADD CONSTRAINT operation_proposals_operation_check CHECK (operation IN ('post_journal', 'reverse_journal', 'create_account', 'revise_account', 'retire_account', 'reactivate_account', 'apply_starter_chart', 'create_payee', 'revise_payee', 'set_entry_payee', 'import_statement', 'mark_statement_duplicate', 'match_statement', 'revoke_statement_match', 'record_statement_adjustment', 'review_statement', 'close_period', 'reopen_period'));

-- Approved work other than a journal entry records its rows under the
-- approved receipt in the approving transaction.
CREATE OR REPLACE FUNCTION public.operation_consumption_guard() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE proposal public.operation_proposals%ROWTYPE;
    approval public.operation_decisions%ROWTYPE; receipt public.requests%ROWTYPE; entry public.entries%ROWTYPE;
    current_xact bigint := mod(pg_current_xact_id()::text::numeric, 4294967296)::bigint;
    by_owner boolean; payload jsonb; preview jsonb; result jsonb; journal boolean;
BEGIN
    SELECT * INTO STRICT proposal FROM public.operation_proposals
    WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.proposal_id;
    SELECT * INTO STRICT approval FROM public.operation_decisions
    WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND proposal_id = NEW.proposal_id
        AND revision = NEW.decision_revision;
    SELECT * INTO STRICT receipt FROM public.requests
    WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.receipt_id;
    -- Journal work posts one entry; other approved work records its rows
    -- under the receipt, and their own guards check them.
    journal := proposal.operation IN ('post_journal', 'reverse_journal');
    IF journal THEN
        SELECT * INTO STRICT entry FROM public.entries
        WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND receipt_id = NEW.receipt_id;
    END IF;
    by_owner := approval.executor_actor_id IS NULL;
    payload := convert_from(proposal.payload_bytes, 'UTF8')::jsonb;
    IF approval.decision <> 'approve' OR approval.sha256 <> proposal.sha256
        OR receipt.operation <> proposal.operation OR receipt.request_key <> proposal.operation_key
        OR convert_from(receipt.request_bytes, 'UTF8')::jsonb IS DISTINCT FROM payload -> 'arguments'
        OR (by_owner AND (receipt.actor_id <> proposal.actor_id OR NEW.actor_id <> approval.owner_id))
        OR (NOT by_owner AND (receipt.actor_id <> approval.executor_actor_id OR NEW.actor_id <> approval.executor_actor_id)) THEN
        RAISE EXCEPTION 'operation differs from approval' USING ERRCODE = '23514';
    END IF;
    IF journal AND by_owner AND TG_WHEN = 'BEFORE' THEN
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
        IF NOT EXISTS (SELECT 1 FROM public.requests r LEFT JOIN public.entries e
                ON e.tenant_id = r.tenant_id AND e.book_id = r.book_id AND e.receipt_id = r.id
                WHERE r.id = NEW.receipt_id AND r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id
                AND r.xmin::text::bigint = current_xact
                AND (NOT journal OR e.xmin::text::bigint = current_xact)) THEN
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

-- A starter chart is applied only to a Book that had no accounts: every
-- account in the Book was created by this same request.
CREATE FUNCTION public.ledger_starter_chart_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.operation = 'apply_starter_chart' AND NOT public.ledger_restoring(NEW.tenant_id, NEW.book_id) AND EXISTS(
        SELECT 1 FROM public.accounts a WHERE a.tenant_id = NEW.tenant_id AND a.book_id = NEW.book_id
        AND NOT public.ledger_written_here(a.xmin)) THEN
        RAISE EXCEPTION 'a starter chart applies only to a Book with no accounts' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER starter_chart_guard AFTER INSERT ON public.requests
    FOR EACH ROW EXECUTE FUNCTION public.ledger_starter_chart_guard();
REVOKE ALL ON FUNCTION public.ledger_starter_chart_guard() FROM PUBLIC;

-- A retirement approved by the owner records its balance transfer under a
-- second receipt, keyed after the approved request.
DROP POLICY operation_owner_exact_request ON public.requests;
CREATE POLICY operation_owner_exact_request ON public.requests AS RESTRICTIVE FOR INSERT WITH CHECK (
    coalesce(current_setting('vult.owner_posting', true), '') = '' OR EXISTS (
        SELECT 1 FROM public.operation_proposals p
        WHERE p.tenant_id = requests.tenant_id AND p.book_id = requests.book_id
            AND p.id = nullif(current_setting('vult.owner_posting', true), '')::uuid
            AND p.actor_id = requests.actor_id
            AND (p.operation = requests.operation AND p.operation_key = requests.request_key
                AND convert_from(requests.request_bytes, 'UTF8')::jsonb IS NOT DISTINCT FROM
                    convert_from(p.payload_bytes, 'UTF8')::jsonb -> 'arguments'
                OR p.operation = 'retire_account' AND requests.operation = 'post_journal'
                AND requests.request_key = p.operation_key || '/transfer')));

-- The owner acts in their own Book as an agent with write access would:
-- directly, from a fresh sign-in, with no connection. This covers account
-- work such as retiring an account with its balance transfer.
CREATE OR REPLACE FUNCTION public.ledger_allowed(t uuid, b uuid, writing boolean) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT CASE WHEN coalesce(current_setting('vult.owner_posting', true), '') = '' THEN
    ((NOT has_function_privilege(session_user, 'access.owner_posting_allowed(uuid,uuid)'::regprocedure, 'EXECUTE')
        OR public.ledger_import_owner())
    AND public.ledger_context(t, b) AND EXISTS (SELECT 1 FROM public.connections c
        WHERE c.tenant_id = t
        AND c.book_id = b
        AND c.id::text = coalesce(current_setting('vult.connection_id', true), '')
        AND c.actor_id::text = coalesce(current_setting('vult.actor_id', true), '')
        AND c.revoked_at IS NULL
        AND (NOT writing OR c.access = 'write' OR c.access = 'propose' AND public.ledger_proposing())
        AND (NOT EXISTS (SELECT 1 FROM access.oauth_grants g WHERE g.connection_id = c.id)
            OR access.agent_allowed(c.id, writing))))
    OR (coalesce(current_setting('vult.connection_id', true), '') = ''
        AND has_function_privilege(session_user, 'access.owner_identity(uuid)'::regprocedure, 'EXECUTE')
        AND access.owner_decision_allowed(t, b))
ELSE access.owner_posting_allowed(t, b) END
$$;
-- That owner path reaches only the records the owner's own account changes
-- make: accounts, payees, statement work, periods, and the entries an
-- adjustment or a retirement transfer posts. Every other Book table refuses
-- a direct owner insert; tables the owner writes through their own policies
-- (documents, uploads) keep them.
CREATE FUNCTION public.ledger_owner_direct() RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = ''
    AND coalesce(current_setting('vult.owner_posting', true), '') = ''
    AND NOT public.ledger_import_owner()
    AND has_function_privilege(session_user, 'access.owner_identity(uuid)'::regprocedure, 'EXECUTE')
$$;
DO $$
DECLARE tab text;
BEGIN
    FOR tab IN SELECT DISTINCT p.tablename FROM pg_catalog.pg_policies p
        WHERE p.schemaname = 'public' AND p.cmd IN ('INSERT', 'ALL') AND p.permissive = 'PERMISSIVE'
            AND p.with_check LIKE '%ledger_allowed(%true)%'
            AND p.tablename NOT IN ('requests', 'accounts', 'account_revisions', 'entries', 'lines',
                'payees', 'payee_revisions', 'entry_payees', 'statement_duplicates', 'statement_matches',
                'statement_match_parts', 'statement_match_revocations', 'statement_adjustments',
                'statement_reviews', 'accounting_periods', 'accounting_period_events',
                'documents', 'document_uploads')
        ORDER BY 1 LOOP
        EXECUTE format('CREATE POLICY owner_change_scope ON public.%I AS RESTRICTIVE FOR INSERT WITH CHECK (NOT public.ledger_owner_direct())', tab);
    END LOOP;
END $$;
