-- Vult 0.8 in one migration: everything 0.8 adds to the ledger after 0.7.0
-- (bank activity, rules, agents, linked Books, Book archives and erasure,
-- document checks, the Vult's AI key, repayments, how a Book is taxed and
-- projects). A new Vult runs it once. An existing one runs the steps it
-- hasn't yet, which are kept as they were, and then records this one.

-- ------------------------------------------------------------------------
-- 0008 bank_activity
-- ------------------------------------------------------------------------

-- Bank activity: transactions a bank connection reports, kept apart from the
-- ledger until a person or an agent records them.
--
-- Nothing here posts by itself. Synced activity lands in an inbox; an entry
-- records it only when an agent with write access, the owner, or the owner's
-- approval of a proposal posts that entry and links it. A bank transaction is
-- recorded at most once: one live link per transaction, and each link must
-- move the linked Book account by exactly the transaction's amount.
--
-- Provider keys and access tokens are stored only as values the application
-- sealed with a key that never enters this database (see secret columns).

-- A Book's provider settings: the person's own keys for one provider.
CREATE TABLE public.bank_provider_settings (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    provider text NOT NULL CHECK (provider IN ('plaid')),
    environment text NOT NULL CHECK (environment IN ('sandbox', 'production')),
    client_id text NOT NULL CHECK (length(client_id) BETWEEN 1 AND 128 AND client_id !~ '[[:space:]]'),
    -- The provider secret, sealed by the application; the key version names
    -- the key that sealed it so keys can be rotated.
    secret_sealed bytea NOT NULL CHECK (octet_length(secret_sealed) BETWEEN 34 AND 4096),
    key_version integer NOT NULL CHECK (key_version > 0),
    -- Where the provider sends update notifications, when this Vult can be
    -- reached from the internet. Empty means sync by polling and on open.
    webhook_url text NOT NULL DEFAULT '' CHECK (webhook_url = '' OR webhook_url ~ '^https://[^[:space:]]{1,2040}$'),
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, provider),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

-- One linked login at a bank, through a provider.
CREATE TABLE public.bank_connections (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    provider text NOT NULL CHECK (provider IN ('plaid')),
    environment text NOT NULL CHECK (environment IN ('sandbox', 'production')),
    -- The provider's own identifier for this login, used to route its
    -- notifications. Not secret.
    provider_item_id text NOT NULL CHECK (length(provider_item_id) BETWEEN 1 AND 256),
    -- Who holds the access token: the Book, with the owner's own provider
    -- keys, or the server's operator, who provides bank access with its own
    -- provider account and syncs the login itself.
    access text NOT NULL DEFAULT 'book' CHECK (access IN ('book', 'operator')),
    institution_id text NOT NULL DEFAULT '' CHECK (length(institution_id) <= 128),
    institution_name text NOT NULL DEFAULT '' CHECK (length(institution_name) <= 200),
    token_sealed bytea CHECK (octet_length(token_sealed) BETWEEN 34 AND 4096),
    key_version integer CHECK (key_version > 0),
    -- Where the last complete sync stopped; the next sync continues from it.
    sync_cursor text NOT NULL DEFAULT '' CHECK (length(sync_cursor) <= 4096),
    -- paused: the owner or the operator stopped syncing; every record stays.
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'needs_attention', 'paused', 'disconnected')),
    -- Set while a login that restarted on a new provider login is being
    -- reconciled with what the Book already holds.
    reconciling boolean NOT NULL DEFAULT false,
    status_reason text NOT NULL DEFAULT '' CHECK (length(status_reason) <= 400),
    last_synced_at timestamptz,
    last_attempt_at timestamptz,
    -- A notification or an open app asked for a sync that hasn't run yet.
    sync_requested_at timestamptz,
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    disconnected_at timestamptz,
    -- When the provider confirmed the access ended, after a disconnection or
    -- an operator's pause. Until then the server keeps asking it to.
    access_ended_at timestamptz,
    UNIQUE (tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    -- A disconnected login keeps its history but holds no access token.
    CHECK ((status = 'disconnected') = (disconnected_at IS NOT NULL)),
    -- A Book's own login holds its token while live; an operator's never
    -- does, here.
    CHECK ((token_sealed IS NULL) = (key_version IS NULL)),
    -- A disconnected Book login keeps its token only until the provider
    -- confirms the access ended.
    CHECK (access = 'operator' AND token_sealed IS NULL
        OR access = 'book' AND (status = 'disconnected' OR token_sealed IS NOT NULL)
            AND (access_ended_at IS NULL OR token_sealed IS NULL))
);
-- One login can feed several Books, each through its own connection.
CREATE UNIQUE INDEX bank_connections_live_item ON public.bank_connections(provider, provider_item_id, tenant_id, book_id) WHERE disconnected_at IS NULL;

-- One account the provider reports for a login, and the Book account it is
-- linked to. Activity is recorded only for linked accounts.
CREATE TABLE public.bank_accounts (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    connection_id uuid NOT NULL,
    provider_account_id text NOT NULL CHECK (length(provider_account_id) BETWEEN 1 AND 256),
    name text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    mask text NOT NULL DEFAULT '' CHECK (length(mask) <= 8),
    -- The provider's own description of the account. It never decides how
    -- money is recorded; the linked Book account's class does.
    provider_type text NOT NULL DEFAULT '' CHECK (length(provider_type) <= 64),
    provider_subtype text NOT NULL DEFAULT '' CHECK (length(provider_subtype) <= 64),
    -- How activity is signed: cash accounts hold the owner's money (an
    -- asset); card accounts are money owed (a liability).
    kind text NOT NULL CHECK (kind IN ('cash', 'card')),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    scale integer NOT NULL CHECK (scale BETWEEN 0 AND 18),
    account_id uuid,
    linked_by uuid,
    linked_at timestamptz,
    UNIQUE (tenant_id, book_id, id),
    UNIQUE (connection_id, provider_account_id),
    FOREIGN KEY (tenant_id, book_id, connection_id) REFERENCES public.bank_connections(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    CHECK ((account_id IS NULL) = (linked_at IS NULL) AND (account_id IS NULL) = (linked_by IS NULL))
);
-- A Book account is fed by at most one bank account, so the same money can't
-- arrive twice through two logins.
CREATE UNIQUE INDEX bank_accounts_book_account ON public.bank_accounts(tenant_id, book_id, account_id) WHERE account_id IS NOT NULL;

-- One transaction as the provider reports it. Its amount is signed the way
-- the account's balance moves, as on a statement: a deposit is positive on a
-- cash account, a charge is positive on a card account.
CREATE TABLE public.bank_transactions (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    -- Where the item came from: a bank connection's account, or a line of an
    -- imported statement (with its Book account). An item from one source
    -- gains the other when the same transaction arrives from it too.
    source text NOT NULL DEFAULT 'bank' CHECK (source IN ('bank', 'statement')),
    bank_account_id uuid,
    account_id uuid,
    statement_row_id uuid,
    provider_transaction_id text NOT NULL CHECK (length(provider_transaction_id) BETWEEN 1 AND 256),
    occurred_on date NOT NULL CHECK (occurred_on BETWEEN DATE '1900-01-01' AND DATE '9999-12-31'),
    authorized_on date CHECK (authorized_on BETWEEN DATE '1900-01-01' AND DATE '9999-12-31'),
    amount_minor bigint NOT NULL CHECK (amount_minor <> 0),
    currency text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    scale integer NOT NULL CHECK (scale BETWEEN 0 AND 18),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    counterparty text NOT NULL DEFAULT '' CHECK (length(counterparty) <= 200),
    pending boolean NOT NULL,
    -- The pending transaction this posted one replaces, by provider ID.
    pending_provider_id text CHECK (length(pending_provider_id) BETWEEN 1 AND 256),
    -- Set when a reconnected login reported this as new but it may repeat
    -- an item already here; a person or agent decides.
    possible_repeat_of uuid,
    removed_at timestamptz,
    revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
    first_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    UNIQUE (bank_account_id, provider_transaction_id),
    FOREIGN KEY (tenant_id, book_id, bank_account_id) REFERENCES public.bank_accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, statement_row_id) REFERENCES public.statement_rows(tenant_id, book_id, id),
    CHECK (source = 'bank' AND bank_account_id IS NOT NULL
        OR source = 'statement' AND statement_row_id IS NOT NULL AND account_id IS NOT NULL)
);
-- A statement line feeds at most one item.
CREATE UNIQUE INDEX bank_transactions_statement_row ON public.bank_transactions(tenant_id, book_id, statement_row_id) WHERE statement_row_id IS NOT NULL;
CREATE INDEX bank_transactions_inbox ON public.bank_transactions(tenant_id, book_id, occurred_on DESC, id) WHERE removed_at IS NULL;

-- The entry that records a bank transaction. One live link per transaction;
-- a link is revoked, with a reason, rather than deleted.
CREATE TABLE public.bank_transaction_links (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    transaction_id uuid NOT NULL,
    bank_account_id uuid,
    -- The Book account the item moves, from its bank account or statement.
    account_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    receipt_id uuid NOT NULL,
    -- Why this entry records the transaction: a person's or agent's
    -- judgement, or a deterministic match Vult offered and someone accepted.
    basis text NOT NULL DEFAULT 'judgement' CHECK (basis IN ('judgement', 'existing_entry', 'transfer')),
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 4000),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_by uuid,
    revoke_reason text CHECK (length(btrim(revoke_reason)) BETWEEN 1 AND 4000),
    revoke_receipt_id uuid,
    revoked_at timestamptz,
    UNIQUE (tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, transaction_id) REFERENCES public.bank_transactions(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, bank_account_id) REFERENCES public.bank_accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, entry_id) REFERENCES public.entries(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (tenant_id, book_id, revoke_receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK (num_nulls(revoked_by, revoke_reason, revoke_receipt_id, revoked_at) IN (0, 4))
);
CREATE UNIQUE INDEX bank_transaction_links_live ON public.bank_transaction_links(tenant_id, book_id, transaction_id) WHERE revoked_at IS NULL;
-- One entry explains at most one transaction per bank account: a transfer's
-- entry links the two sides, never the same side twice.
CREATE UNIQUE INDEX bank_transaction_links_entry ON public.bank_transaction_links(tenant_id, book_id, entry_id, account_id) WHERE revoked_at IS NULL;

-- A recorded decision that a transaction repeats another of the same bank
-- account, with a reason. A repeat is never recorded.
CREATE TABLE public.bank_transaction_duplicates (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    transaction_id uuid NOT NULL,
    canonical_transaction_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 4000),
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, transaction_id),
    FOREIGN KEY (tenant_id, book_id, transaction_id) REFERENCES public.bank_transactions(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, canonical_transaction_id) REFERENCES public.bank_transactions(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK (transaction_id <> canonical_transaction_id)
);

-- After a login restarts on a new provider login, the provider's new IDs
-- for accounts and transactions Vult already holds. The original IDs stay;
-- the new ones route later changes to the same records.
CREATE TABLE public.bank_account_aliases (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    provider_account_id text NOT NULL CHECK (length(provider_account_id) BETWEEN 1 AND 256),
    bank_account_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (connection_id, provider_account_id),
    FOREIGN KEY (tenant_id, book_id, connection_id) REFERENCES public.bank_connections(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, bank_account_id) REFERENCES public.bank_accounts(tenant_id, book_id, id)
);
CREATE TABLE public.bank_transaction_aliases (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    bank_account_id uuid NOT NULL,
    provider_transaction_id text NOT NULL CHECK (length(provider_transaction_id) BETWEEN 1 AND 256),
    transaction_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (bank_account_id, provider_transaction_id),
    FOREIGN KEY (tenant_id, book_id, bank_account_id) REFERENCES public.bank_accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, transaction_id) REFERENCES public.bank_transactions(tenant_id, book_id, id)
);

-- The bank sync writes only bank tables, and only for the one login it is
-- syncing in this transaction. It is never an agent or the owner, so it can't
-- post or link anything.
CREATE FUNCTION public.bank_sync_allowed(t uuid, b uuid, connection uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT public.ledger_context(t, b)
    AND coalesce(current_setting('vult.connection_id', true), '') = ''
    AND coalesce(current_setting('vult.owner_session', true), '') = ''
    AND connection::text = coalesce(current_setting('vult.bank_sync', true), '')
$$;

-- Who may read bank data: any connection that can read the Book, the owner,
-- or the sync for its own login.
CREATE FUNCTION public.bank_read_allowed(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT public.ledger_allowed(t, b, false) OR access.owner_allowed(t)
    OR public.ledger_context(t, b) AND coalesce(current_setting('vult.bank_sync', true), '') <> ''
$$;

-- Whether a login of this Book was disconnected. A new login's sync, which
-- sees only its own login, asks this to take over a disconnected one's
-- accounts; it reveals nothing else.
CREATE FUNCTION public.bank_login_disconnected(t uuid, b uuid, login uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT public.ledger_context(t, b) AND coalesce((SELECT c.disconnected_at IS NOT NULL FROM public.bank_connections c
    WHERE c.tenant_id = t AND c.book_id = b AND c.id = login), false)
$$;
REVOKE ALL ON FUNCTION public.bank_login_disconnected(uuid, uuid, uuid) FROM PUBLIC;

-- A provider notification names a login, not a Book. This finds the one
-- live login it names, and nothing else.
CREATE FUNCTION public.bank_connection_for_item(p text, item text)
    RETURNS TABLE(tenant_id uuid, book_id uuid, id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT c.tenant_id, c.book_id, c.id FROM public.bank_connections c
    WHERE c.provider = p AND c.provider_item_id = item AND c.disconnected_at IS NULL AND c.access = 'book'
$$;

-- Logins whose sync is due: requested, or not synced within the interval.
-- It reveals no secrets and no activity, only which logins to sync next.
CREATE FUNCTION public.bank_connections_due(every interval, max_rows integer)
    RETURNS TABLE(tenant_id uuid, book_id uuid, id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT c.tenant_id, c.book_id, c.id FROM public.bank_connections c
    WHERE c.status = 'active' AND c.access = 'book'
    AND (c.sync_requested_at IS NOT NULL
        OR c.last_attempt_at IS NULL OR c.last_attempt_at < clock_timestamp() - every)
    ORDER BY c.sync_requested_at NULLS LAST, c.last_attempt_at NULLS FIRST
    LIMIT least(greatest(max_rows, 1), 100)
$$;

-- Logins whose provider access must still end: disconnected ones, and an
-- operator's paused ones. It reveals no secrets.
CREATE FUNCTION public.bank_connections_to_release(max_rows integer)
    RETURNS TABLE(tenant_id uuid, book_id uuid, id uuid, access text, status text)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT c.tenant_id, c.book_id, c.id, c.access, c.status FROM public.bank_connections c
    WHERE c.access_ended_at IS NULL AND (c.status = 'disconnected' OR c.status = 'paused' AND c.access = 'operator')
    ORDER BY coalesce(c.disconnected_at, c.last_attempt_at) NULLS FIRST
    LIMIT least(greatest(max_rows, 1), 100)
$$;

-- Asks for a sync of a login a provider notification named. It changes
-- only the request time.
CREATE FUNCTION public.bank_request_sync(connection uuid) RETURNS void
    LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
UPDATE public.bank_connections SET sync_requested_at = clock_timestamp()
    WHERE id = connection AND status = 'active'
$$;

-- Owner changes to settings, logins and account links need a fresh sign-in.
CREATE FUNCTION public.bank_owner_allowed(t uuid, b uuid) RETURNS boolean
    LANGUAGE plpgsql VOLATILE SET search_path = pg_catalog AS $$
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN true;
    END IF;
    IF NOT public.ledger_context(t, b) THEN
        RETURN false;
    END IF;
    -- The owner approving an agent's prepared bank action, in this
    -- transaction. Only the owner's role can run that check.
    IF coalesce(current_setting('vult.owner_posting', true), '') <> '' THEN
        RETURN has_function_privilege(session_user, 'access.owner_posting_allowed(uuid,uuid)'::regprocedure, 'EXECUTE')
            AND access.owner_posting_allowed(t, b);
    END IF;
    RETURN coalesce(current_setting('vult.connection_id', true), '') = '' AND access.owner_decision_allowed(t, b);
END $$;

CREATE FUNCTION public.bank_settings_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        IF TG_OP = 'UPDATE' AND NEW.revision <> OLD.revision + 1 OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'provider settings changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER bank_settings_guard BEFORE INSERT OR UPDATE ON public.bank_provider_settings
    FOR EACH ROW EXECUTE FUNCTION public.bank_settings_guard();

-- A login's identity never changes. The owner disconnects it or replaces its
-- token after reconnecting; the sync moves its cursor and status.
CREATE FUNCTION public.bank_connection_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE owner_change boolean; sync_change boolean; restarting boolean := false;
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a bank login is disconnected, never deleted' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) OR NEW.status <> 'active' THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.created_at := clock_timestamp();
        RETURN NEW;
    END IF;
    -- The server records that the provider ended a login's access, and
    -- forgets its token; nothing else changes.
    IF OLD.access_ended_at IS NULL AND NEW.access_ended_at IS NOT NULL
        AND (OLD.status = 'disconnected' OR OLD.status = 'paused' AND OLD.access = 'operator')
        AND public.bank_sync_allowed(NEW.tenant_id, NEW.book_id, NEW.id) AND NEW.token_sealed IS NULL AND NEW.key_version IS NULL
        AND (to_jsonb(NEW) - ARRAY['access_ended_at', 'token_sealed', 'key_version']) = (to_jsonb(OLD) - ARRAY['access_ended_at', 'token_sealed', 'key_version']) THEN
        NEW.access_ended_at := clock_timestamp();
        RETURN NEW;
    END IF;
    IF OLD.disconnected_at IS NOT NULL THEN
        RAISE EXCEPTION 'a disconnected bank login does not change' USING ERRCODE = '23514';
    END IF;
    -- An operator's login restarts on a new provider login after a pause,
    -- from the start, keeping its accounts and the Book's records.
    IF OLD.access = 'operator' AND NEW.provider_item_id IS DISTINCT FROM OLD.provider_item_id
        AND public.bank_sync_allowed(NEW.tenant_id, NEW.book_id, NEW.id)
        AND NEW.sync_cursor = '' AND NEW.status = 'active' AND NEW.token_sealed IS NULL AND NEW.access_ended_at IS NULL THEN
        OLD.provider_item_id := NEW.provider_item_id;
        restarting := true;
    END IF;
    IF ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.provider, NEW.environment, NEW.provider_item_id, NEW.actor_id, NEW.created_at, NEW.access)
        IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.provider, OLD.environment, OLD.provider_item_id, OLD.actor_id, OLD.created_at, OLD.access) THEN
        RAISE EXCEPTION 'a bank login keeps its identity' USING ERRCODE = '23514';
    END IF;
    -- Asking for a sync changes nothing else; a provider notification does it.
    IF (to_jsonb(NEW) - 'sync_requested_at') = (to_jsonb(OLD) - 'sync_requested_at') THEN
        RETURN NEW;
    END IF;
    owner_change := public.bank_owner_allowed(NEW.tenant_id, NEW.book_id);
    sync_change := public.bank_sync_allowed(NEW.tenant_id, NEW.book_id, NEW.id);
    -- The sync may change only how far it got and how it went.
    -- The sync never resumes a paused login; only the owner, or the
    -- operator restarting its own login, does.
    IF NOT owner_change AND (NOT sync_change
        OR ROW(NEW.token_sealed, NEW.key_version, NEW.institution_id, NEW.institution_name, NEW.disconnected_at)
            IS DISTINCT FROM ROW(OLD.token_sealed, OLD.key_version, OLD.institution_id, OLD.institution_name, OLD.disconnected_at)
        OR NEW.status = 'disconnected'
        OR OLD.status = 'paused' AND NEW.status <> 'paused' AND NOT restarting
        OR NEW.status = 'paused' AND OLD.status <> 'paused' AND OLD.access <> 'operator') THEN
        RAISE EXCEPTION 'only the owner changes a bank login' USING ERRCODE = '42501';
    END IF;
    IF NEW.disconnected_at IS NOT NULL THEN
        NEW.disconnected_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER bank_connection_guard BEFORE INSERT OR UPDATE OR DELETE ON public.bank_connections
    FOR EACH ROW EXECUTE FUNCTION public.bank_connection_guard();

-- The sync adds accounts and refreshes what the provider says about them;
-- only the owner links one to a Book account, and only to an asset (a cash
-- account) or a liability (a card or loan) in the account's own currency.
CREATE FUNCTION public.bank_account_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE target public.accounts%ROWTYPE; book_currency text; book_scale integer;
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'bank accounts are kept with their history' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' AND ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.connection_id, NEW.provider_account_id, NEW.currency, NEW.scale)
        IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.connection_id, OLD.provider_account_id, OLD.currency, OLD.scale) OR NEW.kind <> OLD.kind THEN
        RAISE EXCEPTION 'a bank account keeps its identity and currency' USING ERRCODE = '23514';
    END IF;
    IF (TG_OP = 'INSERT' AND NEW.account_id IS NOT NULL OR TG_OP = 'UPDATE' AND NEW.account_id IS DISTINCT FROM OLD.account_id)
        AND NOT public.bank_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner links a bank account to the Book' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.account_id IS DISTINCT FROM OLD.account_id THEN
        -- Linking changes what the Book's records mean; it waits for writers.
        PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    END IF;
    IF NEW.account_id IS NOT NULL AND (TG_OP = 'INSERT' OR NEW.account_id IS DISTINCT FROM OLD.account_id) THEN
        IF NEW.linked_by::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.linked_at := clock_timestamp();
        SELECT * INTO target FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.account_id;
        SELECT currency, scale INTO book_currency, book_scale FROM public.books WHERE tenant_id = NEW.tenant_id AND id = NEW.book_id;
        IF NOT (NEW.kind = 'cash' AND target.class = 'asset' OR NEW.kind = 'card' AND target.class = 'liability') THEN
            RAISE EXCEPTION 'a cash account is linked to an asset account and a card account to a liability account' USING ERRCODE = '23514';
        END IF;
        IF coalesce(target.currency, book_currency) <> NEW.currency THEN
            RAISE EXCEPTION 'a bank account is linked to a Book account in its own currency' USING ERRCODE = '23514';
        END IF;
        IF target.status = 'retired' OR target.heading THEN
            RAISE EXCEPTION 'a bank account is linked to an account that takes postings' USING ERRCODE = '23514';
        END IF;
        -- Changing the link of an account with recorded activity would leave
        -- those records pointing at the wrong account.
        IF TG_OP = 'UPDATE' AND OLD.account_id IS NOT NULL AND EXISTS (SELECT 1 FROM public.bank_transaction_links l
                WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.bank_account_id = NEW.id AND l.revoked_at IS NULL) THEN
            RAISE EXCEPTION 'unlink the recorded activity before linking another Book account' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_OP = 'UPDATE' AND NEW.account_id IS NULL AND OLD.account_id IS NOT NULL AND EXISTS (SELECT 1 FROM public.bank_transaction_links l
            WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.bank_account_id = NEW.id AND l.revoked_at IS NULL) THEN
        RAISE EXCEPTION 'unlink the recorded activity before unlinking the account' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER bank_account_guard BEFORE INSERT OR UPDATE OR DELETE ON public.bank_accounts
    FOR EACH ROW EXECUTE FUNCTION public.bank_account_guard();

-- Only the sync writes transactions, keeping their identity. A transaction
-- with a live link keeps its amount and account: the provider changing a
-- recorded amount leaves the record for a person to review, not rewritten.
CREATE FUNCTION public.bank_transaction_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE login uuid; by_sync boolean; by_import boolean;
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'removed bank activity is marked, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NEW.bank_account_id IS NOT NULL THEN
        SELECT a.connection_id INTO STRICT login FROM public.bank_accounts a
            WHERE a.tenant_id = NEW.tenant_id AND a.book_id = NEW.book_id AND a.id = NEW.bank_account_id;
    END IF;
    -- The sync of the account's own login, or of a login that took the
    -- account over after the Book disconnected the first one.
    by_sync := login IS NOT NULL AND (public.bank_sync_allowed(NEW.tenant_id, NEW.book_id, login)
        OR EXISTS (SELECT 1 FROM public.bank_account_aliases x WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.book_id
            AND x.bank_account_id = NEW.bank_account_id AND public.bank_sync_allowed(NEW.tenant_id, NEW.book_id, x.connection_id)));
    -- A statement import adds items for its own lines, in the same
    -- transaction and with the same authority that recorded them.
    by_import := NEW.statement_row_id IS NOT NULL AND public.ledger_allowed(NEW.tenant_id, NEW.book_id, true)
        AND EXISTS (SELECT 1 FROM public.statement_rows r WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id
            AND r.id = NEW.statement_row_id AND public.ledger_written_here(r.xmin));
    IF TG_OP = 'INSERT' THEN
        IF NOT (by_sync AND NEW.source = 'bank' OR by_import AND NEW.source = 'statement' AND NEW.bank_account_id IS NULL) THEN
            RAISE EXCEPTION 'only the bank sync or a statement import adds bank activity' USING ERRCODE = '42501';
        END IF;
        NEW.revision := 1;
        NEW.first_seen_at := clock_timestamp();
        NEW.updated_at := clock_timestamp();
        RETURN NEW;
    END IF;
    IF ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.provider_transaction_id, NEW.first_seen_at, NEW.currency, NEW.scale, NEW.source, NEW.account_id)
        IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.provider_transaction_id, OLD.first_seen_at, OLD.currency, OLD.scale, OLD.source, OLD.account_id) THEN
        RAISE EXCEPTION 'bank activity keeps its identity' USING ERRCODE = '23514';
    END IF;
    IF NEW.bank_account_id IS DISTINCT FROM OLD.bank_account_id AND NOT (OLD.bank_account_id IS NULL AND by_sync) THEN
        RAISE EXCEPTION 'bank activity keeps its bank account' USING ERRCODE = '23514';
    END IF;
    IF NEW.statement_row_id IS DISTINCT FROM OLD.statement_row_id THEN
        -- A statement line joins an item from the bank; nothing else changes.
        IF OLD.statement_row_id IS NOT NULL OR NOT by_import
            OR (to_jsonb(NEW) - 'statement_row_id') IS DISTINCT FROM (to_jsonb(OLD) - 'statement_row_id') THEN
            RAISE EXCEPTION 'a statement line joins an item only once, by its import' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NOT by_sync THEN
        RAISE EXCEPTION 'only the bank sync records bank activity' USING ERRCODE = '42501';
    END IF;
    IF NEW IS NOT DISTINCT FROM OLD THEN
        RETURN NULL;
    END IF;
    NEW.revision := OLD.revision + 1;
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END $$;
CREATE TRIGGER bank_transaction_guard BEFORE INSERT OR UPDATE OR DELETE ON public.bank_transactions
    FOR EACH ROW EXECUTE FUNCTION public.bank_transaction_guard();

-- A transaction is recordable when it has posted, is still reported, and its
-- bank account is linked to a Book account.
CREATE FUNCTION public.bank_link_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE observed record;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a link is revoked with a reason, never deleted' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL
            OR (to_jsonb(NEW) - ARRAY['revoked_by', 'revoke_reason', 'revoke_receipt_id', 'revoked_at'])
                IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['revoked_by', 'revoke_reason', 'revoke_receipt_id', 'revoked_at']) THEN
            RAISE EXCEPTION 'a link changes only by being revoked once' USING ERRCODE = '23514';
        END IF;
        IF NOT public.ledger_import_owner() THEN
            IF NEW.revoked_by::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
                RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
            END IF;
            NEW.revoked_at := clock_timestamp();
        END IF;
        RETURN NEW;
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.recorded_at := clock_timestamp();
        IF NEW.revoked_at IS NOT NULL THEN
            RAISE EXCEPTION 'a new link is live' USING ERRCODE = '23514';
        END IF;
    END IF;
    SELECT t.pending, t.removed_at, t.bank_account_id, coalesce(a.account_id, t.account_id) AS account_id INTO STRICT observed
        FROM public.bank_transactions t LEFT JOIN public.bank_accounts a
        ON a.tenant_id = t.tenant_id AND a.book_id = t.book_id AND a.id = t.bank_account_id
        WHERE t.tenant_id = NEW.tenant_id AND t.book_id = NEW.book_id AND t.id = NEW.transaction_id;
    IF observed.bank_account_id IS DISTINCT FROM NEW.bank_account_id OR observed.account_id IS DISTINCT FROM NEW.account_id THEN
        RAISE EXCEPTION 'a link names the transaction''s own accounts' USING ERRCODE = '23514';
    END IF;
    IF observed.pending OR observed.removed_at IS NOT NULL THEN
        RAISE EXCEPTION 'pending or removed bank activity is not recorded' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (SELECT 1 FROM public.bank_transaction_duplicates d WHERE d.tenant_id = NEW.tenant_id AND d.book_id = NEW.book_id
            AND d.transaction_id = NEW.transaction_id) THEN
        RAISE EXCEPTION 'a transaction marked as a repeat is not recorded' USING ERRCODE = '23514';
    END IF;
    IF observed.account_id IS NULL THEN
        RAISE EXCEPTION 'link the bank account to a Book account first' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER bank_link_guard BEFORE INSERT OR UPDATE OR DELETE ON public.bank_transaction_links
    FOR EACH ROW EXECUTE FUNCTION public.bank_link_guard();

-- At commit: the linked entry moves the bank account's Book account by
-- exactly the transaction's amount, in the transaction's currency, and isn't
-- a reversal or reversed.
CREATE FUNCTION public.bank_link_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE observed record; moved numeric; lines_on integer; comparable_lines integer;
BEGIN
    IF NEW.revoked_at IS NOT NULL OR public.ledger_restoring(NEW.tenant_id, NEW.book_id) THEN
        RETURN NULL;
    END IF;
    SELECT t.amount_minor, t.currency, t.scale, NEW.account_id AS account_id INTO STRICT observed
        FROM public.bank_transactions t
        WHERE t.tenant_id = NEW.tenant_id AND t.book_id = NEW.book_id AND t.id = NEW.transaction_id;
    IF public.statement_entry_reversed(NEW.tenant_id, NEW.book_id, NEW.entry_id) THEN
        RAISE EXCEPTION 'a reversed or reversing entry does not record bank activity' USING ERRCODE = '23514';
    END IF;
    SELECT count(*), count(public.statement_line_amount(l.tenant_id, l.book_id, l.id, observed.currency, observed.scale)),
        sum(public.statement_line_amount(l.tenant_id, l.book_id, l.id, observed.currency, observed.scale))
        INTO lines_on, comparable_lines, moved
        FROM public.lines l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id
        AND l.entry_id = NEW.entry_id AND l.account_id = observed.account_id;
    IF lines_on = 0 OR comparable_lines <> lines_on OR moved <> observed.amount_minor THEN
        RAISE EXCEPTION 'the entry moves the bank account''s Book account by the transaction amount' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER bank_link_final AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.bank_link_final();
CREATE CONSTRAINT TRIGGER bank_link_receipt AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('link_bank_activity', 'record_bank_activity', 'record_bank_transfer');
-- Bank management decisions keep a receipt under the owner's request.


CREATE FUNCTION public.bank_duplicate_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE a record; c record;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.recorded_at := clock_timestamp();
    END IF;
    SELECT coalesce(b.account_id, t.account_id)::text AS account INTO STRICT a FROM public.bank_transactions t
        LEFT JOIN public.bank_accounts b ON b.tenant_id = t.tenant_id AND b.book_id = t.book_id AND b.id = t.bank_account_id
        WHERE t.tenant_id = NEW.tenant_id AND t.book_id = NEW.book_id AND t.id = NEW.transaction_id;
    SELECT coalesce(b.account_id, t.account_id)::text AS account INTO STRICT c FROM public.bank_transactions t
        LEFT JOIN public.bank_accounts b ON b.tenant_id = t.tenant_id AND b.book_id = t.book_id AND b.id = t.bank_account_id
        WHERE t.tenant_id = NEW.tenant_id AND t.book_id = NEW.book_id AND t.id = NEW.canonical_transaction_id;
    IF a.account IS DISTINCT FROM c.account THEN
        RAISE EXCEPTION 'a repeat is of a transaction on the same account' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id
            AND l.transaction_id = NEW.transaction_id AND l.revoked_at IS NULL)
        OR EXISTS (SELECT 1 FROM public.bank_transaction_duplicates d WHERE d.tenant_id = NEW.tenant_id AND d.book_id = NEW.book_id
            AND d.transaction_id = NEW.canonical_transaction_id) THEN
        RAISE EXCEPTION 'a recorded transaction, or one that is itself a repeat, is not marked as a repeat' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER bank_duplicate_guard BEFORE INSERT ON public.bank_transaction_duplicates
    FOR EACH ROW EXECUTE FUNCTION public.bank_duplicate_guard();
CREATE TRIGGER bank_duplicate_immutable BEFORE UPDATE OR DELETE ON public.bank_transaction_duplicates
    FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE CONSTRAINT TRIGGER bank_duplicate_receipt AFTER INSERT ON public.bank_transaction_duplicates DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('mark_bank_duplicate', 'import_statement');

CREATE FUNCTION public.bank_unlink_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.revoke_receipt_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM public.requests r
        WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id AND r.id = NEW.revoke_receipt_id
        AND r.actor_id = NEW.revoked_by AND r.operation = 'unlink_bank_activity') THEN
        RAISE EXCEPTION 'decision receipt differs from the record' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER bank_unlink_receipt AFTER UPDATE ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.bank_unlink_receipt();

-- Row security.
DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['bank_provider_settings', 'bank_connections', 'bank_accounts', 'bank_transactions', 'bank_transaction_links',
        'bank_transaction_duplicates', 'bank_account_aliases', 'bank_transaction_aliases'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
    END LOOP;
END $$;
-- Provider settings and tokens are read only by the owner and the sync.
-- Agents never read them, sealed or not.
CREATE POLICY bank_read ON public.bank_provider_settings FOR SELECT
    USING (access.owner_allowed(tenant_id) OR public.ledger_context(tenant_id, book_id) AND coalesce(current_setting('vult.bank_sync', true), '') <> '');
CREATE POLICY bank_owner_insert ON public.bank_provider_settings FOR INSERT WITH CHECK (public.bank_owner_allowed(tenant_id, book_id));
CREATE POLICY bank_owner_update ON public.bank_provider_settings FOR UPDATE
    USING (public.bank_owner_allowed(tenant_id, book_id)) WITH CHECK (public.bank_owner_allowed(tenant_id, book_id));
CREATE POLICY bank_read ON public.bank_connections FOR SELECT
    USING (access.owner_allowed(tenant_id) OR public.bank_sync_allowed(tenant_id, book_id, id) OR public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY bank_owner_insert ON public.bank_connections FOR INSERT WITH CHECK (public.bank_owner_allowed(tenant_id, book_id));
CREATE POLICY bank_update ON public.bank_connections FOR UPDATE
    USING (public.bank_owner_allowed(tenant_id, book_id) OR public.bank_sync_allowed(tenant_id, book_id, id))
    WITH CHECK (public.bank_owner_allowed(tenant_id, book_id) OR public.bank_sync_allowed(tenant_id, book_id, id));
CREATE POLICY bank_read ON public.bank_accounts FOR SELECT USING (public.bank_read_allowed(tenant_id, book_id));
CREATE POLICY bank_write ON public.bank_accounts FOR INSERT
    WITH CHECK (public.bank_owner_allowed(tenant_id, book_id) OR public.bank_sync_allowed(tenant_id, book_id, connection_id));
CREATE POLICY bank_update ON public.bank_accounts FOR UPDATE
    USING (public.bank_owner_allowed(tenant_id, book_id) OR public.bank_sync_allowed(tenant_id, book_id, connection_id))
    WITH CHECK (public.bank_owner_allowed(tenant_id, book_id) OR public.bank_sync_allowed(tenant_id, book_id, connection_id));
CREATE POLICY bank_read ON public.bank_transactions FOR SELECT USING (public.bank_read_allowed(tenant_id, book_id));
CREATE POLICY bank_sync_insert ON public.bank_transactions FOR INSERT WITH CHECK (public.ledger_context(tenant_id, book_id)
    AND coalesce(current_setting('vult.bank_sync', true), '') <> '');
CREATE POLICY bank_sync_update ON public.bank_transactions FOR UPDATE
    USING (public.ledger_context(tenant_id, book_id) AND coalesce(current_setting('vult.bank_sync', true), '') <> '')
    WITH CHECK (public.ledger_context(tenant_id, book_id) AND coalesce(current_setting('vult.bank_sync', true), '') <> '');
-- A statement import adds items, or joins its line to one, with the
-- authority that recorded the statement; the guard checks the rest.
CREATE POLICY statement_import_insert ON public.bank_transactions FOR INSERT WITH CHECK (public.ledger_allowed(tenant_id, book_id, true));
CREATE POLICY statement_import_update ON public.bank_transactions FOR UPDATE
    USING (public.ledger_allowed(tenant_id, book_id, true)) WITH CHECK (public.ledger_allowed(tenant_id, book_id, true));
CREATE TRIGGER proposal_record_only AFTER INSERT OR UPDATE ON public.bank_transactions
    FOR EACH ROW EXECUTE FUNCTION public.ledger_proposal_record_only();
-- Links follow connection levels like any Book record: a write agent links
-- directly, a proposing agent only through an approved proposal, and the
-- owner directly from a fresh sign-in.
CREATE POLICY bank_read ON public.bank_transaction_links FOR SELECT USING (public.bank_read_allowed(tenant_id, book_id));
CREATE POLICY scoped_insert ON public.bank_transaction_links FOR INSERT WITH CHECK (public.ledger_allowed(tenant_id, book_id, true));
CREATE POLICY scoped_revoke ON public.bank_transaction_links FOR UPDATE
    USING (public.ledger_allowed(tenant_id, book_id, true)) WITH CHECK (public.ledger_allowed(tenant_id, book_id, true));
CREATE TRIGGER proposal_record_only AFTER INSERT OR UPDATE ON public.bank_transaction_links
    FOR EACH ROW EXECUTE FUNCTION public.ledger_proposal_record_only();
-- Repeat decisions follow connection levels like links.
CREATE POLICY bank_read ON public.bank_transaction_duplicates FOR SELECT USING (public.bank_read_allowed(tenant_id, book_id));
CREATE POLICY scoped_insert ON public.bank_transaction_duplicates FOR INSERT WITH CHECK (public.ledger_allowed(tenant_id, book_id, true));
CREATE TRIGGER proposal_record_only AFTER INSERT OR UPDATE ON public.bank_transaction_duplicates
    FOR EACH ROW EXECUTE FUNCTION public.ledger_proposal_record_only();
-- Aliases are written only by the sync for its own login.
CREATE POLICY bank_read ON public.bank_account_aliases FOR SELECT USING (public.bank_read_allowed(tenant_id, book_id));
CREATE POLICY bank_sync_insert ON public.bank_account_aliases FOR INSERT WITH CHECK (public.bank_sync_allowed(tenant_id, book_id, connection_id));
CREATE POLICY bank_read ON public.bank_transaction_aliases FOR SELECT USING (public.bank_read_allowed(tenant_id, book_id));
CREATE POLICY bank_sync_insert ON public.bank_transaction_aliases FOR INSERT WITH CHECK (public.ledger_context(tenant_id, book_id)
    AND coalesce(current_setting('vult.bank_sync', true), '') <> '');

-- The owner may add a statement file Vult read in plain code, such as a
-- CSV export, so its lines reach the inbox without an agent. The owner's
-- direct path still writes no other statement; the application marks this
-- one import in its transaction.
DROP POLICY owner_change_scope ON public.statements;
CREATE POLICY owner_change_scope ON public.statements AS RESTRICTIVE FOR INSERT
    WITH CHECK (NOT public.ledger_owner_direct() OR coalesce(current_setting('vult.statement_file', true), '') = 'on');
DROP POLICY owner_change_scope ON public.statement_rows;
CREATE POLICY owner_change_scope ON public.statement_rows AS RESTRICTIVE FOR INSERT
    WITH CHECK (NOT public.ledger_owner_direct() OR coalesce(current_setting('vult.statement_file', true), '') = 'on');

-- The same money is recorded once, whoever records or approves it. An item
-- marked as repeating another is not recorded; an item flagged as possibly
-- the same as another waits while that one is recorded, until someone
-- marks it a repeat.
CREATE FUNCTION public.bank_link_once() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE other record;
BEGIN
    IF NEW.revoked_at IS NOT NULL OR public.ledger_restoring(NEW.tenant_id, NEW.book_id) THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM public.bank_transaction_duplicates d WHERE d.tenant_id = NEW.tenant_id AND d.book_id = NEW.book_id AND d.transaction_id = NEW.transaction_id) THEN
        RAISE EXCEPTION 'this item repeats one already here; record that one, and this one needs nothing' USING ERRCODE = '23514';
    END IF;
    SELECT y.occurred_on INTO other FROM public.bank_transactions x JOIN public.bank_transactions y
        ON y.tenant_id = x.tenant_id AND y.book_id = x.book_id AND (y.id = x.possible_repeat_of OR y.possible_repeat_of = x.id)
        WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.book_id AND x.id = NEW.transaction_id AND y.id <> x.id
        AND EXISTS (SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id = y.tenant_id AND l.book_id = y.book_id
            AND l.transaction_id = y.id AND l.revoked_at IS NULL)
        LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'this may be the same money as an item already recorded on %; if it is, mark it as a repeat', other.occurred_on USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER bank_link_once AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.bank_link_once();
REVOKE ALL ON FUNCTION public.bank_link_once() FROM PUBLIC;

-- Proposals: recording bank activity and transfers can be prepared for the
-- owner's approval. Approved work records its entry under a second receipt,
-- keyed after the approved request.
ALTER FUNCTION public.operation_dependencies(uuid, uuid, text, jsonb, uuid) RENAME TO operation_dependencies_journal;
CREATE FUNCTION public.operation_dependencies(t uuid, b uuid, op text, args jsonb, excluded uuid)
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
            WHERE t2.tenant_id = t AND t2.book_id = b AND t2.id::text IN (
                SELECT value #>> '{}' FROM jsonb_array_elements(CASE WHEN op = 'record_bank_transfer'
                    THEN args -> 'transaction_ids' ELSE jsonb_build_array(args -> 'transaction_id') END));
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
REVOKE ALL ON FUNCTION public.operation_dependencies(uuid, uuid, text, jsonb, uuid) FROM PUBLIC;

ALTER TABLE public.operation_proposals DROP CONSTRAINT operation_proposals_operation_check,
    ADD CONSTRAINT operation_proposals_operation_check CHECK (operation IN ('post_journal', 'reverse_journal', 'create_account', 'revise_account', 'retire_account', 'reactivate_account', 'apply_starter_chart', 'create_payee', 'revise_payee', 'set_entry_payee', 'import_statement', 'mark_statement_duplicate', 'match_statement', 'revoke_statement_match', 'record_statement_adjustment', 'review_statement', 'close_period', 'reopen_period',
        'link_bank_activity', 'unlink_bank_activity', 'record_bank_activity', 'record_bank_transfer',
        'link_bank_account', 'disconnect_bank', 'pause_bank_connection', 'resume_bank_connection', 'mark_bank_duplicate'));

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
                AND requests.request_key = p.operation_key || '/transfer'
                OR p.operation IN ('record_bank_activity', 'record_bank_transfer') AND requests.operation = 'post_journal'
                AND requests.request_key = p.operation_key || '/entry')));

-- Live updates: a change to a Book's entries, proposals or bank activity
-- tells listening app servers which Book changed and in what area. The
-- notice names IDs only and carries no financial data; PostgreSQL sends it at
-- commit, once per distinct notice in a transaction.
CREATE FUNCTION public.book_changed() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    PERFORM pg_notify('vult_book_changes', NEW.tenant_id::text || ' ' || NEW.book_id::text || ' ' || TG_ARGV[0]);
    RETURN NULL;
END $$;
CREATE TRIGGER book_changed AFTER INSERT ON public.entries FOR EACH ROW EXECUTE FUNCTION public.book_changed('entries');
CREATE TRIGGER book_changed AFTER INSERT ON public.operation_proposals FOR EACH ROW EXECUTE FUNCTION public.book_changed('proposals');
CREATE TRIGGER book_changed AFTER INSERT ON public.operation_decisions FOR EACH ROW EXECUTE FUNCTION public.book_changed('proposals');
CREATE TRIGGER book_changed AFTER INSERT OR UPDATE ON public.bank_transactions FOR EACH ROW EXECUTE FUNCTION public.book_changed('bank');
CREATE TRIGGER book_changed AFTER INSERT OR UPDATE ON public.bank_transaction_links FOR EACH ROW EXECUTE FUNCTION public.book_changed('bank');
CREATE TRIGGER book_changed AFTER INSERT OR UPDATE ON public.bank_connections FOR EACH ROW EXECUTE FUNCTION public.book_changed('bank');
CREATE TRIGGER book_changed AFTER INSERT OR UPDATE ON public.bank_accounts FOR EACH ROW EXECUTE FUNCTION public.book_changed('bank');
CREATE TRIGGER book_changed AFTER INSERT ON public.bank_transaction_duplicates FOR EACH ROW EXECUTE FUNCTION public.book_changed('bank');

REVOKE ALL ON FUNCTION public.book_changed() FROM PUBLIC;
REVOKE ALL ON FUNCTION public.bank_sync_allowed(uuid, uuid, uuid), public.bank_read_allowed(uuid, uuid),
    public.bank_connection_for_item(text, text), public.bank_connections_due(interval, integer), public.bank_connections_to_release(integer),
    public.bank_request_sync(uuid), public.bank_owner_allowed(uuid, uuid),
    public.bank_settings_guard(), public.bank_connection_guard(), public.bank_account_guard(),
    public.bank_transaction_guard(), public.bank_link_guard(), public.bank_link_final(), public.bank_unlink_receipt(), public.bank_duplicate_guard()
    FROM PUBLIC;

-- ------------------------------------------------------------------------
-- 0009 recording_rules
-- ------------------------------------------------------------------------

-- Recording within the owner's rules.
--
-- A rule is a decision the owner made once: money moving one way on one of
-- their accounts, from one counterparty, within an amount range, records to
-- one other account. Only the owner creates, turns on, turns off or changes
-- a rule; no agent can, and no proposal can. A connection the owner grants
-- it to may record bank activity directly only by an active rule, as a
-- transfer between the Book's own accounts, or as a match to an entry
-- already in the Book. Everything else it does stays a proposal. The
-- database checks every such entry, and each one names its rule.
--
-- A rule may split an item: up to seven parts, each a fixed amount, a
-- fraction of the item, or a loan's interest, with the rest going to the
-- rule's other account (for a loan, the principal).

-- A counterparty's key: its letters only, lowercased, single-spaced, so
-- "EXAMPLE CAFE #1021 10/14" and "Example Cafe #1187" are one key. The
-- same function makes a rule's key and matches activity against it.
CREATE FUNCTION public.rule_key(raw text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT SET search_path = pg_catalog AS $$
SELECT btrim(regexp_replace(lower(raw), '[^a-z]+', ' ', 'g'))
$$;

CREATE TABLE public.recording_rules (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    -- The owner's asset or liability account the money moves on.
    account_id uuid NOT NULL,
    -- in: the account's balance rises; out: it falls.
    direction text NOT NULL CHECK (direction IN ('in', 'out')),
    -- The counterparty as the bank names it, folded to lower case.
    counterparty_key text NOT NULL CHECK (length(counterparty_key) BETWEEN 1 AND 200 AND counterparty_key = public.rule_key(counterparty_key)),
    -- Where the other side of the entry goes.
    other_account_id uuid NOT NULL,
    min_minor bigint NOT NULL CHECK (min_minor > 0),
    max_minor bigint NOT NULL,
    -- At least this many days between two entries the rule records.
    min_days_between integer NOT NULL DEFAULT 0 CHECK (min_days_between BETWEEN 0 AND 366),
    description text NOT NULL CHECK (length(btrim(description)) BETWEEN 1 AND 400),
    active boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, other_account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    CHECK (max_minor >= min_minor),
    CHECK (account_id <> other_account_id)
);
CREATE UNIQUE INDEX recording_rules_one_active ON public.recording_rules(tenant_id, book_id, account_id, direction, counterparty_key) WHERE active;

-- Connections the owner lets record within the rules.
CREATE TABLE public.rule_recording_grants (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    granted_by uuid NOT NULL,
    granted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    revoked_at timestamptz,
    PRIMARY KEY (tenant_id, book_id, connection_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

-- The parts a rule splits an item into, in order. A part is never deleted:
-- changing a rule's parts replaces them, and the old ones keep their
-- history.
CREATE TABLE public.recording_rule_parts (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    rule_id uuid NOT NULL,
    position smallint NOT NULL CHECK (position BETWEEN 1 AND 7),
    account_id uuid NOT NULL,
    -- fixed: amount_minor. ratio: the item times numerator / denominator,
    -- rounded half away from zero. interest: the loan's balance on the
    -- payment date times the yearly rate numerator / denominator, for a
    -- month, or for the days since the loan's last entry over 365 or 360.
    kind text NOT NULL CHECK (kind IN ('fixed', 'ratio', 'interest')),
    amount_minor bigint,
    numerator bigint,
    denominator bigint,
    basis text,
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    replaced_at timestamptz,
    FOREIGN KEY (tenant_id, book_id, rule_id) REFERENCES public.recording_rules(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    CHECK (kind = 'fixed' AND amount_minor > 0 AND numerator IS NULL AND denominator IS NULL AND basis IS NULL
        OR kind = 'ratio' AND amount_minor IS NULL AND numerator > 0 AND denominator > numerator AND denominator <= 1000000000 AND basis IS NULL
        OR kind = 'interest' AND amount_minor IS NULL AND numerator > 0 AND denominator > numerator AND denominator <= 1000000000
            AND basis IN ('monthly', 'actual_365', 'actual_360'))
);
CREATE UNIQUE INDEX recording_rule_parts_position ON public.recording_rule_parts(tenant_id, book_id, rule_id, position) WHERE replaced_at IS NULL;
CREATE UNIQUE INDEX recording_rule_parts_account ON public.recording_rule_parts(tenant_id, book_id, rule_id, account_id) WHERE replaced_at IS NULL;

ALTER TABLE public.bank_transaction_links DROP CONSTRAINT bank_transaction_links_basis_check,
    ADD CONSTRAINT bank_transaction_links_basis_check CHECK (basis IN ('judgement', 'existing_entry', 'transfer', 'rule')),
    ADD COLUMN rule_id uuid,
    ADD CONSTRAINT bank_transaction_links_rule_fk FOREIGN KEY (tenant_id, book_id, rule_id) REFERENCES public.recording_rules(tenant_id, book_id, id),
    ADD CONSTRAINT bank_transaction_links_rule_basis CHECK ((basis = 'rule') = (rule_id IS NOT NULL));

-- The owner, acting directly from a fresh sign-in. Never an approval of a
-- proposal, and never an agent.
CREATE FUNCTION public.rule_owner_allowed(t uuid, b uuid) RETURNS boolean
    LANGUAGE plpgsql VOLATILE SET search_path = pg_catalog AS $$
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN true;
    END IF;
    RETURN public.ledger_context(t, b)
        AND coalesce(current_setting('vult.connection_id', true), '') = ''
        AND coalesce(current_setting('vult.owner_posting', true), '') = ''
        AND access.owner_decision_allowed(t, b);
END $$;

-- This transaction is a granted connection recording within the rules. The
-- application marks it; the grant itself is checked here.
CREATE FUNCTION public.ledger_rule_recording() RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.rule_recording', true), '') = 'on' AND EXISTS (
    SELECT 1 FROM public.rule_recording_grants g JOIN public.connections c
        ON c.tenant_id = g.tenant_id AND c.book_id = g.book_id AND c.id = g.connection_id
    WHERE g.connection_id::text = coalesce(current_setting('vult.connection_id', true), '')
        AND g.tenant_id::text = coalesce(current_setting('vult.tenant_id', true), '')
        AND g.book_id::text = coalesce(current_setting('vult.book_id', true), '')
        AND g.revoked_at IS NULL AND c.revoked_at IS NULL AND c.access = 'propose')
$$;

CREATE FUNCTION public.recording_rule_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE a public.accounts%ROWTYPE; o public.accounts%ROWTYPE;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a rule is turned off, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.rule_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner creates or changes a recording rule' USING ERRCODE = '42501';
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.updated_at := clock_timestamp();
        IF TG_OP = 'INSERT' THEN
            NEW.created_at := clock_timestamp();
            NEW.revision := 1;
        ELSE
            IF ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.created_at) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.created_at) THEN
                RAISE EXCEPTION 'a rule keeps its identity' USING ERRCODE = '23514';
            END IF;
            NEW.revision := OLD.revision + 1;
        END IF;
    END IF;
    -- Classified by account types, never by names: the money moves on one
    -- of the owner's asset or liability accounts, and the other side fits
    -- its direction.
    SELECT * INTO STRICT a FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.account_id;
    SELECT * INTO STRICT o FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.other_account_id;
    IF a.class NOT IN ('asset', 'liability') THEN
        RAISE EXCEPTION 'a rule moves money on an asset or liability account' USING ERRCODE = '23514';
    END IF;
    IF NEW.direction = 'out' AND o.class = 'income' OR NEW.direction = 'in' AND o.class = 'expense' THEN
        RAISE EXCEPTION 'money out isn''t income and money in isn''t an expense; record those case by case' USING ERRCODE = '23514';
    END IF;
    IF o.status = 'retired' OR o.heading OR a.status = 'retired' THEN
        RAISE EXCEPTION 'a rule uses accounts that take postings' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER recording_rule_guard BEFORE INSERT OR UPDATE OR DELETE ON public.recording_rules
    FOR EACH ROW EXECUTE FUNCTION public.recording_rule_guard();

-- A part is classified by account types, never by names. Money out
-- splits to spending, debts or other assets; money in to income, debts
-- (such as tax collected) or equity. A part never goes to cash or a card:
-- money between the owner's accounts is a transfer.
CREATE FUNCTION public.recording_rule_part_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE r public.recording_rules%ROWTYPE; a public.accounts%ROWTYPE; o public.accounts%ROWTYPE;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a rule''s parts are replaced, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.rule_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner creates or changes a recording rule' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.replaced_at IS NOT NULL OR NEW.replaced_at IS NULL
            OR ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.rule_id, NEW.position, NEW.account_id, NEW.kind, NEW.amount_minor, NEW.numerator, NEW.denominator, NEW.basis, NEW.actor_id, NEW.created_at)
            IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.rule_id, OLD.position, OLD.account_id, OLD.kind, OLD.amount_minor, OLD.numerator, OLD.denominator, OLD.basis, OLD.actor_id, OLD.created_at) THEN
            RAISE EXCEPTION 'a rule''s part is only ever replaced' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.created_at := clock_timestamp();
        NEW.replaced_at := NULL;
    END IF;
    SELECT * INTO STRICT r FROM public.recording_rules WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.rule_id;
    SELECT * INTO STRICT a FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.account_id;
    SELECT * INTO STRICT o FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = r.other_account_id;
    IF a.status = 'retired' OR a.heading THEN
        RAISE EXCEPTION 'a rule uses accounts that take postings' USING ERRCODE = '23514';
    END IF;
    IF a.id IN (r.account_id, r.other_account_id) THEN
        RAISE EXCEPTION 'a part goes to an account other than the rule''s own two' USING ERRCODE = '23514';
    END IF;
    IF a.role IN ('cash', 'card') OR EXISTS (SELECT 1 FROM public.bank_accounts b
            WHERE b.tenant_id = NEW.tenant_id AND b.book_id = NEW.book_id AND b.account_id = NEW.account_id) THEN
        RAISE EXCEPTION 'a part never goes to cash or a card; money between your own accounts is a transfer' USING ERRCODE = '23514';
    END IF;
    IF r.direction = 'out' AND a.class NOT IN ('expense', 'liability', 'asset')
        OR r.direction = 'in' AND a.class NOT IN ('income', 'liability', 'equity') THEN
        RAISE EXCEPTION 'money out splits to spending, debts or other assets; money in to income, debts or equity' USING ERRCODE = '23514';
    END IF;
    IF NEW.kind = 'interest' AND (a.class <> 'expense' OR r.direction <> 'out' OR o.class <> 'liability' OR o.role IS DISTINCT FROM 'loan') THEN
        RAISE EXCEPTION 'interest is an expense split from a payment whose rest pays down a loan' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER recording_rule_part_guard BEFORE INSERT OR UPDATE OR DELETE ON public.recording_rule_parts
    FOR EACH ROW EXECUTE FUNCTION public.recording_rule_part_guard();

CREATE FUNCTION public.rule_grant_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a grant is revoked, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.rule_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner grants recording within rules' USING ERRCODE = '42501';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id
            AND c.id = NEW.connection_id AND c.access = 'propose' AND c.revoked_at IS NULL) THEN
        RAISE EXCEPTION 'recording within rules is granted to a proposing connection' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER rule_grant_guard BEFORE INSERT OR UPDATE OR DELETE ON public.rule_recording_grants
    FOR EACH ROW EXECUTE FUNCTION public.rule_grant_guard();

-- A link made by a rule matches that rule: active, on its account and in its
-- direction, from its counterparty, within its range and cadence, with an
-- entry of exactly two lines that moves the account by the amount and posts
-- the other side to the rule's account.
CREATE FUNCTION public.rule_link_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE r public.recording_rules%ROWTYPE; t public.bank_transactions%ROWTYPE; lines integer; other_ok boolean; distinct_ok boolean;
BEGIN
    IF NEW.basis <> 'rule' OR NEW.revoked_at IS NOT NULL OR public.ledger_restoring(NEW.tenant_id, NEW.book_id) THEN
        RETURN NULL;
    END IF;
    SELECT * INTO STRICT r FROM public.recording_rules WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.rule_id;
    SELECT * INTO STRICT t FROM public.bank_transactions WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.transaction_id;
    IF NOT r.active OR r.account_id <> NEW.account_id
        OR (t.amount_minor > 0) <> (r.direction = 'in')
        OR abs(t.amount_minor) NOT BETWEEN r.min_minor AND r.max_minor
        OR public.rule_key(coalesce(nullif(t.counterparty, ''), t.description)) <> r.counterparty_key THEN
        RAISE EXCEPTION 'this activity isn''t covered by the rule' USING ERRCODE = '23514';
    END IF;
    IF r.min_days_between > 0 AND EXISTS (SELECT 1 FROM public.bank_transaction_links l JOIN public.bank_transactions x
            ON x.tenant_id = l.tenant_id AND x.book_id = l.book_id AND x.id = l.transaction_id
            WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.rule_id = r.id AND l.id <> NEW.id AND l.revoked_at IS NULL
            AND abs(x.occurred_on - t.occurred_on) < r.min_days_between) THEN
        RAISE EXCEPTION 'the rule recorded this counterparty too recently; this one is a proposal' USING ERRCODE = '23514';
    END IF;
    -- The entry posts once to each of the rule's accounts: its own, the
    -- rest to its other account, and any of its current parts.
    SELECT count(*), count(DISTINCT l.account_id) = count(*), bool_and(l.account_id IN (r.account_id, r.other_account_id)
            OR EXISTS (SELECT 1 FROM public.recording_rule_parts p WHERE p.tenant_id = l.tenant_id AND p.book_id = l.book_id
                AND p.rule_id = r.id AND p.replaced_at IS NULL AND p.account_id = l.account_id))
        INTO lines, distinct_ok, other_ok
        FROM public.lines l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.entry_id = NEW.entry_id;
    IF lines < 2 OR NOT distinct_ok OR NOT other_ok OR NOT EXISTS (SELECT 1 FROM public.lines l WHERE l.tenant_id = NEW.tenant_id
            AND l.book_id = NEW.book_id AND l.entry_id = NEW.entry_id AND l.account_id = r.other_account_id) THEN
        RAISE EXCEPTION 'an entry a rule records posts only to the rule''s accounts' USING ERRCODE = '23514';
    END IF;
    -- Fixed and fractional parts are exactly the rule's.
    IF EXISTS (SELECT 1 FROM public.recording_rule_parts p JOIN public.lines l
            ON l.tenant_id = p.tenant_id AND l.book_id = p.book_id AND l.entry_id = NEW.entry_id AND l.account_id = p.account_id
            WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id AND p.rule_id = r.id AND p.replaced_at IS NULL
            AND (p.kind = 'fixed' AND l.amount_minor <> p.amount_minor
                OR p.kind = 'ratio' AND l.amount_minor <> round(abs(t.amount_minor)::numeric * p.numerator / p.denominator))) THEN
        RAISE EXCEPTION 'a part of the entry differs from the rule' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER rule_link_final AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.rule_link_final();

-- Under a rule grant, an entry is posted only to record bank activity by a
-- rule or as a transfer, and links are made only by a rule, a transfer or a
-- match to an existing entry.
CREATE FUNCTION public.rule_recording_entry_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id
            AND l.entry_id = NEW.id AND l.revoked_at IS NULL AND l.basis IN ('rule', 'transfer')
            AND public.ledger_written_here(l.xmin)) THEN
        RAISE EXCEPTION 'within the rules, an entry records bank activity by a rule or as a transfer' USING ERRCODE = '42501';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER rule_recording_entry AFTER INSERT ON public.entries DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (coalesce(current_setting('vult.rule_recording', true), '') = 'on')
    EXECUTE FUNCTION public.rule_recording_entry_final();
CREATE FUNCTION public.rule_recording_link_check() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF public.ledger_rule_recording() AND NEW.basis NOT IN ('rule', 'transfer', 'existing_entry') THEN
        RAISE EXCEPTION 'within the rules, activity is recorded by a rule, a transfer or a match' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER rule_recording_link BEFORE INSERT ON public.bank_transaction_links
    FOR EACH ROW EXECUTE FUNCTION public.rule_recording_link_check();

-- Write authority: a granted proposing connection may write while
-- recording within the rules; everything else it writes stays a proposal.
CREATE OR REPLACE FUNCTION access.agent_allowed(connection uuid, writing boolean) RETURNS boolean
    LANGUAGE sql VOLATILE SECURITY DEFINER
    SET search_path = pg_catalog AS $$
SELECT EXISTS (SELECT 1
    FROM access.oauth_credentials c
    JOIN access.oauth_grants g ON g.connection_id = c.connection_id
    JOIN access.identities i ON i.id = g.identity_id
    AND i.epoch = g.owner_epoch
    JOIN access.owners o ON o.tenant_id = g.tenant_id
    AND o.identity_id = i.id
    JOIN public.connections p ON p.id = g.connection_id AND p.tenant_id = g.tenant_id AND p.book_id = g.book_id
    WHERE c.connection_id = connection
    AND c.kind = 'access'
    AND c.token_hash = coalesce(current_setting('vult.agent_token', true), '')
    AND g.resource = coalesce(current_setting('vult.agent_resource', true), '') AND c.generation = g.generation
    AND c.expires_at > clock_timestamp()
    AND c.consumed_at IS NULL
    AND p.revoked_at IS NULL
    AND (NOT writing
    OR p.access = 'write'
    OR p.access = 'propose' AND (public.ledger_proposing() OR public.ledger_rule_recording())))
$$;

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
        AND (NOT writing OR c.access = 'write' OR c.access = 'propose' AND (public.ledger_proposing() OR public.ledger_rule_recording()))
        AND (NOT EXISTS (SELECT 1 FROM access.oauth_grants g WHERE g.connection_id = c.id)
            OR access.agent_allowed(c.id, writing))))
    OR (coalesce(current_setting('vult.connection_id', true), '') = ''
        AND has_function_privilege(session_user, 'access.owner_identity(uuid)'::regprocedure, 'EXECUTE')
        AND access.owner_decision_allowed(t, b))
ELSE access.owner_posting_allowed(t, b) END
$$;

-- Links recorded by a rule keep a receipt under its request.
DROP TRIGGER bank_link_receipt ON public.bank_transaction_links;
CREATE CONSTRAINT TRIGGER bank_link_receipt AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('link_bank_activity', 'record_bank_activity', 'record_bank_transfer', 'record_by_rule');

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['recording_rules', 'rule_recording_grants', 'recording_rule_parts'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY rule_read ON public.%I FOR SELECT USING (public.ledger_allowed(tenant_id, book_id, false) OR access.owner_allowed(tenant_id))', tab);
        EXECUTE format('CREATE POLICY rule_owner_insert ON public.%I FOR INSERT WITH CHECK (public.rule_owner_allowed(tenant_id, book_id))', tab);
        EXECUTE format('CREATE POLICY rule_owner_update ON public.%I FOR UPDATE USING (public.rule_owner_allowed(tenant_id, book_id)) WITH CHECK (public.rule_owner_allowed(tenant_id, book_id))', tab);
    END LOOP;
END $$;

REVOKE ALL ON FUNCTION public.rule_key(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION public.rule_owner_allowed(uuid, uuid), public.ledger_rule_recording(), public.recording_rule_guard(), public.recording_rule_part_guard(), public.rule_grant_guard(),
    public.rule_link_final(), public.rule_recording_entry_final(), public.rule_recording_link_check() FROM PUBLIC;

-- ------------------------------------------------------------------------
-- 0010 agent_settings
-- ------------------------------------------------------------------------

-- The built-in agent's settings for a Book: the provider and model the
-- owner chose, and the owner's own API key, sealed by the application with
-- a key that never enters this database. Only the owner changes them, from
-- a fresh sign-in; no agent reads them. The agent acts through its own
-- proposing connection, like any agent.
CREATE TABLE public.agent_settings (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    -- service: the service running this Vult provides the agent and its
    -- model, so the owner only turns it on or off and gives no key.
    provider text NOT NULL CHECK (provider IN ('openai_compatible', 'anthropic', 'service')),
    base_url text NOT NULL CHECK (length(base_url) <= 512),
    model text NOT NULL CHECK (length(model) <= 200),
    key_sealed bytea CHECK (octet_length(key_sealed) BETWEEN 34 AND 4096),
    key_version integer CHECK (key_version > 0),
    enabled boolean NOT NULL DEFAULT false,
    connection_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    CHECK ((key_sealed IS NULL) = (key_version IS NULL)),
    CHECK (provider = 'service' AND base_url = '' AND model = '' AND key_sealed IS NULL
        OR provider <> 'service' AND base_url ~ '^https?://[^[:space:]]+$' AND length(model) >= 1)
);

CREATE FUNCTION public.agent_settings_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent settings are turned off, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.rule_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner changes the built-in agent' USING ERRCODE = '42501';
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        IF TG_OP = 'UPDATE' AND NEW.revision <> OLD.revision + 1 OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'agent settings changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id
            AND c.id = NEW.connection_id AND c.access = 'propose') THEN
        RAISE EXCEPTION 'the built-in agent works through a proposing connection' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER agent_settings_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_settings
    FOR EACH ROW EXECUTE FUNCTION public.agent_settings_guard();

-- The engine reads settings for the Books whose agent is on. It reveals
-- the sealed key only to the server, which alone can open it.
CREATE FUNCTION public.agent_settings_for(t uuid, b uuid)
    RETURNS TABLE(provider text, base_url text, model text, key_sealed bytea, key_version integer, enabled boolean, connection_id uuid, actor_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT s.provider, s.base_url, s.model, s.key_sealed, s.key_version, s.enabled, s.connection_id, c.actor_id
    FROM public.agent_settings s JOIN public.connections c ON c.tenant_id = s.tenant_id AND c.book_id = s.book_id AND c.id = s.connection_id
    WHERE s.tenant_id = t AND s.book_id = b
$$;
CREATE FUNCTION public.agent_books()
    RETURNS TABLE(tenant_id uuid, book_id uuid, connection_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT s.tenant_id, s.book_id, s.connection_id FROM public.agent_settings s JOIN public.connections c
    ON c.tenant_id = s.tenant_id AND c.book_id = s.book_id AND c.id = s.connection_id
    WHERE s.enabled AND (s.key_sealed IS NOT NULL OR s.provider = 'service' OR s.base_url ~ '^http://(127\.0\.0\.1|localhost|\[::1\])')
    ORDER BY s.updated_at LIMIT 1000
$$;

ALTER TABLE public.agent_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agent_settings FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.agent_settings FROM PUBLIC;
-- Only the owner reads them; agents never do, sealed or not.
CREATE POLICY owner_read ON public.agent_settings FOR SELECT USING (access.owner_allowed(tenant_id));
CREATE POLICY owner_insert ON public.agent_settings FOR INSERT WITH CHECK (public.rule_owner_allowed(tenant_id, book_id));
CREATE POLICY owner_update ON public.agent_settings FOR UPDATE USING (public.rule_owner_allowed(tenant_id, book_id)) WITH CHECK (public.rule_owner_allowed(tenant_id, book_id));
REVOKE ALL ON FUNCTION public.agent_settings_guard(), public.agent_settings_for(uuid, uuid), public.agent_books() FROM PUBLIC;

-- ------------------------------------------------------------------------
-- 0011 agent_autonomy
-- ------------------------------------------------------------------------

-- Earned autonomy: an agent the owner allows may record what it's sure
-- about, within limits the ledger checks, and asks about the rest. Trust is
-- earned per kind of activity and held here, the same for any agent. The
-- owner decides; approval of proposals stays the owner's alone.

-- How a proposing connection may record directly: only by the owner's rules,
-- or also when confident, within the Book's limits. Without a grant it only
-- drafts proposals.
ALTER TABLE public.rule_recording_grants
    ADD COLUMN recording_mode text NOT NULL DEFAULT 'rules' CHECK (recording_mode IN ('rules', 'confident'));

CREATE FUNCTION public.ledger_confident_recording() RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT public.ledger_rule_recording() AND EXISTS (
    SELECT 1 FROM public.rule_recording_grants g
    WHERE g.connection_id::text = coalesce(current_setting('vult.connection_id', true), '')
        AND g.tenant_id::text = coalesce(current_setting('vult.tenant_id', true), '')
        AND g.book_id::text = coalesce(current_setting('vult.book_id', true), '')
        AND g.revoked_at IS NULL AND g.recording_mode = 'confident')
$$;

-- The Book's limits for recording when confident. Without a row, the cap is
-- 500 in the Book's currency.
CREATE TABLE public.agent_policy (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    amount_cap_minor bigint NOT NULL CHECK (amount_cap_minor > 0),
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

CREATE FUNCTION public.agent_amount_cap(t uuid, b uuid) RETURNS bigint
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT coalesce((SELECT p.amount_cap_minor FROM public.agent_policy p WHERE p.tenant_id = t AND p.book_id = b),
    (SELECT 500 * (10::numeric ^ k.scale)::bigint FROM public.books k WHERE k.tenant_id = t AND k.id = b))
$$;

-- Accounts an agent reaches only by the owner's rules, such as taxes owed.
-- The owner marks them; a starter chart marks its own as it creates them.
CREATE TABLE public.agent_tax_sensitive_accounts (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    account_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    marked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, account_id),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id)
);

CREATE FUNCTION public.agent_limits_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE r record;
BEGIN
    IF TG_OP = 'DELETE' THEN
        r := OLD;
    ELSE
        r := NEW;
    END IF;
    IF public.ledger_import_owner() THEN
        RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    END IF;
    -- Only a tax-sensitive row has an account; the policy row has none, so
    -- its account is read only inside this branch.
    IF TG_TABLE_NAME = 'agent_tax_sensitive_accounts' AND TG_OP = 'INSERT' THEN
        IF public.ledger_context(NEW.tenant_id, NEW.book_id)
            AND EXISTS (SELECT 1 FROM public.accounts a WHERE a.tenant_id = NEW.tenant_id AND a.book_id = NEW.book_id
                AND a.id = (to_jsonb(NEW) ->> 'account_id')::uuid AND public.ledger_written_here(a.xmin)) THEN
            -- A starter chart marks the accounts it creates in this transaction.
            RETURN NEW;
        END IF;
    END IF;
    IF NOT public.agent_owner_change_allowed(r.tenant_id, r.book_id, TG_TABLE_NAME, TG_OP,
            CASE WHEN TG_OP <> 'INSERT' THEN to_jsonb(OLD) END, CASE WHEN TG_OP <> 'DELETE' THEN to_jsonb(NEW) END) THEN
        RAISE EXCEPTION 'only the owner sets an agent''s limits' USING ERRCODE = '42501';
    END IF;
    IF TG_OP <> 'DELETE' AND r.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF TG_TABLE_NAME = 'agent_policy' THEN
        IF TG_OP = 'DELETE' THEN
            RAISE EXCEPTION 'an agent policy is changed, never deleted' USING ERRCODE = '23514';
        END IF;
        IF TG_OP = 'UPDATE' AND NEW.revision <> OLD.revision + 1 OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'agent policy changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
    END IF;
    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE TRIGGER agent_policy_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_policy
    FOR EACH ROW EXECUTE FUNCTION public.agent_limits_guard();
CREATE TRIGGER agent_tax_sensitive_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_tax_sensitive_accounts
    FOR EACH ROW EXECUTE FUNCTION public.agent_limits_guard();

-- Trust per kind of activity: the Book account money moves on, its
-- direction (the balance's, as for rules) and the counterparty's key. The
-- ledger alone writes it, from the owner's decisions.
CREATE TABLE public.agent_kinds (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    account_id uuid NOT NULL,
    direction text NOT NULL CHECK (direction IN ('in', 'out')),
    counterparty_key text NOT NULL CHECK (length(counterparty_key) <= 200),
    accepted_unchanged integer NOT NULL DEFAULT 0 CHECK (accepted_unchanged >= 0),
    accepted_since_correction integer NOT NULL DEFAULT 0 CHECK (accepted_since_correction >= 0),
    corrected integer NOT NULL DEFAULT 0 CHECK (corrected >= 0),
    last_corrected_at timestamptz,
    -- record: may be recorded when confident; draft: proposals only.
    level text NOT NULL DEFAULT 'record' CHECK (level IN ('record', 'draft')),
    -- The owner's decision for the kind: the other account of the last
    -- approved entry, and the one they last turned down.
    decided_account_id uuid,
    declined_account_id uuid,
    offered_at timestamptz,
    rule_id uuid,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, account_id, direction, counterparty_key),
    FOREIGN KEY (tenant_id, book_id, account_id) REFERENCES public.accounts(tenant_id, book_id, id)
);
-- Each bank item counts once per outcome, however it was decided.
CREATE TABLE public.agent_kind_events (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    transaction_id uuid NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('accepted', 'corrected')),
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, transaction_id, outcome),
    FOREIGN KEY (tenant_id, book_id, transaction_id) REFERENCES public.bank_transactions(tenant_id, book_id, id)
);

-- The Book's starting trust for kinds with no history: the built-in agent's
-- skills result once the owner consented; 'record' when there's no built-in
-- agent. Trust only ever demotes a kind from here.
CREATE FUNCTION public.agent_default_trust(t uuid, b uuid) RETURNS text
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    RETURN coalesce((SELECT s.default_trust FROM public.agent_settings s WHERE s.tenant_id = t AND s.book_id = b), 'record');
END $$;

-- Notes an outcome for a bank item's kind. Accepted work counts towards an
-- offer to record the kind by a rule and keeps the account the owner chose;
-- a correction sends the kind back to drafts, keeps the account they turned
-- down, and an undo forgets the decision it undid.
CREATE FUNCTION public.agent_kind_note(t uuid, b uuid, item uuid, outcome text, other uuid) RETURNS void
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE k record; inserted integer;
BEGIN
    SELECT coalesce(a.account_id, x.account_id) AS account_id, CASE WHEN x.amount_minor > 0 THEN 'in' ELSE 'out' END AS direction,
            left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200) AS counterparty_key
        INTO k FROM public.bank_transactions x LEFT JOIN public.bank_accounts a ON a.tenant_id = x.tenant_id AND a.book_id = x.book_id AND a.id = x.bank_account_id
        WHERE x.tenant_id = t AND x.book_id = b AND x.id = item;
    IF k.account_id IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO public.agent_kind_events(tenant_id, book_id, transaction_id, outcome)
        VALUES (t, b, item, CASE WHEN outcome = 'accepted' THEN 'accepted' ELSE 'corrected' END) ON CONFLICT DO NOTHING;
    GET DIAGNOSTICS inserted = ROW_COUNT;
    IF inserted = 0 THEN
        RETURN;
    END IF;
    INSERT INTO public.agent_kinds(tenant_id, book_id, account_id, direction, counterparty_key, level)
        VALUES (t, b, k.account_id, k.direction, k.counterparty_key, public.agent_default_trust(t, b)) ON CONFLICT DO NOTHING;
    IF outcome = 'accepted' THEN
        UPDATE public.agent_kinds SET accepted_unchanged = accepted_unchanged + 1, accepted_since_correction = accepted_since_correction + 1,
            offered_at = CASE WHEN offered_at IS NULL AND rule_id IS NULL AND level = 'draft' AND accepted_since_correction + 1 >= 15 THEN clock_timestamp() ELSE offered_at END,
            decided_account_id = coalesce(other, decided_account_id),
            declined_account_id = CASE WHEN other IS NOT NULL THEN NULL ELSE declined_account_id END,
            updated_at = clock_timestamp()
            WHERE tenant_id = t AND book_id = b AND account_id = k.account_id AND direction = k.direction AND counterparty_key = k.counterparty_key;
    ELSE
        UPDATE public.agent_kinds SET corrected = corrected + 1, accepted_since_correction = 0, last_corrected_at = clock_timestamp(),
            level = 'draft', offered_at = NULL, updated_at = clock_timestamp(),
            declined_account_id = CASE WHEN outcome = 'rejected' THEN coalesce(other, declined_account_id) ELSE declined_account_id END,
            decided_account_id = CASE WHEN outcome = 'undone' AND decided_account_id = other THEN NULL ELSE decided_account_id END
            WHERE tenant_id = t AND book_id = b AND account_id = k.account_id AND direction = k.direction AND counterparty_key = k.counterparty_key;
    END IF;
END $$;

-- The other account of a bank item's entry: the one that isn't the item's
-- own Book account.
CREATE FUNCTION public.agent_other_account(t uuid, b uuid, item uuid, args jsonb, entry uuid) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT coalesce(
    (args ->> 'other_account_id')::uuid,
    (SELECT (l ->> 'account_id')::uuid FROM jsonb_array_elements(coalesce(args -> 'lines', '[]'::jsonb)) l
        WHERE (l ->> 'account_id')::uuid IS DISTINCT FROM (SELECT coalesce(a.account_id, x.account_id) FROM public.bank_transactions x
            LEFT JOIN public.bank_accounts a ON a.tenant_id = x.tenant_id AND a.book_id = x.book_id AND a.id = x.bank_account_id
            WHERE x.tenant_id = t AND x.book_id = b AND x.id = item)
        AND jsonb_array_length(args -> 'lines') = 2 LIMIT 1),
    (SELECT l.account_id FROM public.lines l WHERE l.tenant_id = t AND l.book_id = b AND l.entry_id = entry
        AND (SELECT count(*) FROM public.lines c WHERE c.tenant_id = t AND c.book_id = b AND c.entry_id = entry) = 2
        AND l.account_id IS DISTINCT FROM (SELECT coalesce(a.account_id, x.account_id) FROM public.bank_transactions x
            LEFT JOIN public.bank_accounts a ON a.tenant_id = x.tenant_id AND a.book_id = x.book_id AND a.id = x.bank_account_id
            WHERE x.tenant_id = t AND x.book_id = b AND x.id = item) LIMIT 1))
$$;

-- The owner's decision on a proposal to record bank activity: approval is
-- always of the exact work, so it counts as accepted unchanged.
CREATE FUNCTION public.agent_kind_decision() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE p public.operation_proposals%ROWTYPE; args jsonb;
BEGIN
    SELECT * INTO p FROM public.operation_proposals WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.proposal_id;
    IF p.operation = 'record_bank_activity' THEN
        args := convert_from(p.payload_bytes, 'UTF8')::jsonb -> 'arguments';
        -- Rejecting a copy of activity already recorded another way is
        -- tidying up, not a correction of the agent.
        IF NEW.decision = 'reject' AND EXISTS (SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id = NEW.tenant_id
                AND l.book_id = NEW.book_id AND l.transaction_id = (args ->> 'transaction_id')::uuid AND l.revoked_at IS NULL) THEN
            RETURN NULL;
        END IF;
        PERFORM public.agent_kind_note(NEW.tenant_id, NEW.book_id, (args ->> 'transaction_id')::uuid,
            CASE WHEN NEW.decision = 'approve' THEN 'accepted' ELSE 'rejected' END,
            public.agent_other_account(NEW.tenant_id, NEW.book_id, (args ->> 'transaction_id')::uuid, args, NULL));
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER agent_kind_decision AFTER INSERT ON public.operation_decisions
    FOR EACH ROW EXECUTE FUNCTION public.agent_kind_decision();

-- The owner undoing an agent's recording: reversing its entry or unlinking
-- it from the bank item is a correction of that kind.
CREATE FUNCTION public.agent_kind_undone() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE l record;
BEGIN
    IF EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = NEW.actor_id) THEN
        RETURN NULL;
    END IF;
    IF TG_TABLE_NAME = 'entries' THEN
        FOR l IN SELECT DISTINCT x.transaction_id FROM public.bank_transaction_links x
                WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.book_id AND x.entry_id = NEW.reversal_of
                AND EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = x.actor_id) LOOP
            PERFORM public.agent_kind_note(NEW.tenant_id, NEW.book_id, l.transaction_id, 'undone',
                public.agent_other_account(NEW.tenant_id, NEW.book_id, l.transaction_id, '{}'::jsonb, NEW.reversal_of));
        END LOOP;
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER agent_kind_reversed AFTER INSERT ON public.entries
    FOR EACH ROW WHEN (NEW.reversal_of IS NOT NULL) EXECUTE FUNCTION public.agent_kind_undone();

CREATE FUNCTION public.agent_kind_unlinked() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = OLD.actor_id)
        AND NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = NEW.revoked_by) THEN
        PERFORM public.agent_kind_note(NEW.tenant_id, NEW.book_id, NEW.transaction_id, 'undone',
            public.agent_other_account(NEW.tenant_id, NEW.book_id, NEW.transaction_id, '{}'::jsonb, NEW.entry_id));
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER agent_kind_unlinked AFTER UPDATE ON public.bank_transaction_links
    FOR EACH ROW WHEN (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL) EXECUTE FUNCTION public.agent_kind_unlinked();

-- The owner accepts an offer to record a kind by a rule they just made from
-- it; the kind is trusted again.
CREATE FUNCTION public.agent_kind_rule(t uuid, b uuid, account uuid, dir text, party text, rule uuid) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE updated integer;
BEGIN
    IF NOT public.rule_owner_allowed(t, b) THEN
        RAISE EXCEPTION 'only the owner accepts an offer' USING ERRCODE = '42501';
    END IF;
    UPDATE public.agent_kinds SET rule_id = rule, level = 'record', offered_at = NULL, updated_at = clock_timestamp()
        WHERE tenant_id = t AND book_id = b AND account_id = account AND direction = dir AND counterparty_key = party
        AND EXISTS (SELECT 1 FROM public.recording_rules r WHERE r.tenant_id = t AND r.book_id = b AND r.id = rule
            AND r.account_id = account AND r.direction = dir AND r.counterparty_key = party);
    GET DIAGNOSTICS updated = ROW_COUNT;
    RETURN updated = 1;
END $$;

-- A rule made from an offer of a kind waits while the owner has corrected
-- that kind since; recording by it is refused until trust is earned again.
CREATE FUNCTION public.rule_kind_trusted() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.basis = 'rule' AND NEW.revoked_at IS NULL AND EXISTS (SELECT 1 FROM public.agent_kinds k
            WHERE k.tenant_id = NEW.tenant_id AND k.book_id = NEW.book_id AND k.rule_id = NEW.rule_id AND k.level = 'draft') THEN
        RAISE EXCEPTION 'the owner corrected this kind of activity since making the rule; it''s a proposal until trust is earned again' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER rule_kind_trusted AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.rule_kind_trusted();

-- Recording when confident: what the link and its entry must be. Checked
-- at commit, by account types only.
CREATE FUNCTION public.confident_link_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE x public.bank_transactions%ROWTYPE; o record; lines integer; kind_level text;
BEGIN
    IF NEW.basis NOT IN ('judgement', 'transfer') OR NEW.revoked_at IS NOT NULL OR NOT public.ledger_confident_recording() THEN
        RETURN NULL;
    END IF;
    SELECT * INTO STRICT x FROM public.bank_transactions WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.transaction_id;
    SELECT coalesce((SELECT k.level FROM public.agent_kinds k WHERE k.tenant_id = NEW.tenant_id AND k.book_id = NEW.book_id
        AND k.account_id = NEW.account_id AND k.direction = CASE WHEN x.amount_minor > 0 THEN 'in' ELSE 'out' END
        AND k.counterparty_key = left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200)),
        public.agent_default_trust(NEW.tenant_id, NEW.book_id)) INTO kind_level;
    -- A transfer the ledger matched still keeps to the limit and to trust.
    IF NEW.basis = 'transfer' THEN
        IF abs(x.amount_minor) > public.agent_amount_cap(NEW.tenant_id, NEW.book_id) THEN
            RAISE EXCEPTION 'this transfer is over the Book''s limit for an agent recording on its own; it''s a proposal' USING ERRCODE = '23514';
        END IF;
        IF x.amount_minor < 0 AND EXISTS (SELECT 1 FROM public.agent_kinds k WHERE k.tenant_id = NEW.tenant_id AND k.book_id = NEW.book_id
                AND k.account_id = NEW.account_id AND k.direction = 'out' AND k.level = 'draft'
                AND k.counterparty_key = left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200)) THEN
            RAISE EXCEPTION 'this kind of activity waits for the owner until the agent earns trust for it' USING ERRCODE = '23514';
        END IF;
        RETURN NULL;
    END IF;
    IF length(btrim(coalesce(NEW.reason, ''))) = 0 THEN
        RAISE EXCEPTION 'an agent recording on its own gives its reason' USING ERRCODE = '23514';
    END IF;
    SELECT count(*) INTO lines FROM public.lines l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.entry_id = NEW.entry_id;
    SELECT a.id, a.class, a.role, l.side INTO o FROM public.lines l JOIN public.accounts a ON a.tenant_id = l.tenant_id AND a.book_id = l.book_id AND a.id = l.account_id
        WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.entry_id = NEW.entry_id AND l.account_id <> NEW.account_id LIMIT 1;
    IF lines <> 2 OR o.id IS NULL THEN
        RAISE EXCEPTION 'an agent recording on its own records a whole item to one account; a split is a proposal' USING ERRCODE = '23514';
    END IF;
    IF abs(x.amount_minor) > public.agent_amount_cap(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'this is over the Book''s limit for an agent recording on its own; it''s a proposal' USING ERRCODE = '23514';
    END IF;
    IF o.class = 'equity' THEN
        RAISE EXCEPTION 'money between the owner and the Book is asked, or recorded by a rule' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (SELECT 1 FROM public.agent_tax_sensitive_accounts s WHERE s.tenant_id = NEW.tenant_id AND s.book_id = NEW.book_id AND s.account_id = o.id) THEN
        RAISE EXCEPTION 'this account is reached only by the owner''s rules; it''s a proposal' USING ERRCODE = '23514';
    END IF;
    IF o.role IN ('cash', 'card', 'loan') OR EXISTS (SELECT 1 FROM public.bank_accounts b WHERE b.tenant_id = NEW.tenant_id AND b.book_id = NEW.book_id AND b.account_id = o.id) THEN
        RAISE EXCEPTION 'money between your own accounts, or on a loan, is a transfer or a rule, not a judgement' USING ERRCODE = '23514';
    END IF;
    IF o.class = 'income' AND o.side = 'debit' OR o.class = 'expense' AND o.side = 'credit' THEN
        RAISE EXCEPTION 'this reduces income or an expense, as a refund does; refunds are asked' USING ERRCODE = '23514';
    END IF;
    IF kind_level = 'draft' THEN
        RAISE EXCEPTION 'this kind of activity waits for the owner until the agent earns trust for it (a correction, or a skills test it didn''t pass); it''s a proposal' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER confident_link_final AFTER INSERT ON public.bank_transaction_links DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.confident_link_final();

-- Under a recording grant, entries and links come from rules, transfers and
-- matches; a confident grant adds the agent's own judgement, checked above.
CREATE OR REPLACE FUNCTION public.rule_recording_entry_final() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id
            AND l.entry_id = NEW.id AND l.revoked_at IS NULL
            AND (l.basis IN ('rule', 'transfer') OR l.basis = 'judgement' AND public.ledger_confident_recording())
            AND public.ledger_written_here(l.xmin)) THEN
        RAISE EXCEPTION 'within the rules, an entry records bank activity by a rule or as a transfer' USING ERRCODE = '42501';
    END IF;
    RETURN NULL;
END $$;
CREATE OR REPLACE FUNCTION public.rule_recording_link_check() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF public.ledger_rule_recording() AND NEW.basis NOT IN ('rule', 'transfer', 'existing_entry')
        AND NOT (NEW.basis = 'judgement' AND public.ledger_confident_recording()) THEN
        RAISE EXCEPTION 'within the rules, activity is recorded by a rule, a transfer or a match' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END $$;

-- The built-in agent's skills test: the result the engine measured for the
-- model, which sets where it starts. Changing the model clears it.
ALTER TABLE public.agent_settings
    ADD COLUMN skills_level text CHECK (skills_level IN ('confident', 'rules', 'drafts')),
    ADD COLUMN skills_score integer CHECK (skills_score >= 0),
    ADD COLUMN skills_total integer CHECK (skills_total > 0),
    ADD COLUMN skills_model text CHECK (length(skills_model) <= 200),
    ADD COLUMN skills_tested_at timestamptz,
    -- Where kinds with no history start: 'record' once the model passed the
    -- skills test, 'draft' otherwise.
    ADD COLUMN default_trust text NOT NULL DEFAULT 'draft' CHECK (default_trust IN ('record', 'draft')),
    ADD CONSTRAINT agent_settings_skills CHECK ((skills_level IS NULL) = (skills_tested_at IS NULL) AND (skills_score IS NULL OR skills_score <= skills_total));
CREATE FUNCTION public.agent_record_skills(t uuid, b uuid, tested_model text, tested_level text, tested_score integer, tested_total integer) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE updated integer;
BEGIN
    -- Bypasses the owner-only guard for these columns alone.
    PERFORM set_config('vult.agent_skills', 'on', true);
    UPDATE public.agent_settings s SET skills_level = tested_level, skills_score = tested_score, skills_total = tested_total, skills_model = left(tested_model, 200),
        skills_tested_at = clock_timestamp(), default_trust = CASE WHEN tested_level = 'confident' THEN 'record' ELSE 'draft' END
        WHERE s.tenant_id = t AND s.book_id = b AND s.model = tested_model;
    GET DIAGNOSTICS updated = ROW_COUNT;
    PERFORM set_config('vult.agent_skills', '', true);
    RETURN updated = 1;
END $$;
CREATE OR REPLACE FUNCTION public.agent_settings_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'agent settings are turned off, never deleted' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' AND coalesce(current_setting('vult.agent_skills', true), '') = 'on'
        AND (to_jsonb(NEW) - ARRAY['skills_level', 'skills_score', 'skills_total', 'skills_model', 'skills_tested_at', 'default_trust'])
            = (to_jsonb(OLD) - ARRAY['skills_level', 'skills_score', 'skills_total', 'skills_model', 'skills_tested_at', 'default_trust']) THEN
        RETURN NEW;
    END IF;
    IF NOT public.rule_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner changes the built-in agent' USING ERRCODE = '42501';
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        IF TG_OP = 'UPDATE' AND NEW.revision <> OLD.revision + 1 OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'agent settings changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
        IF TG_OP = 'INSERT' THEN
            NEW.default_trust := 'draft';
        ELSIF NEW.default_trust IS DISTINCT FROM OLD.default_trust THEN
            RAISE EXCEPTION 'the skills test sets where trust starts' USING ERRCODE = '42501';
        END IF;
        -- A different model hasn't taken the test.
        IF TG_OP = 'UPDATE' AND ROW(NEW.provider, NEW.base_url, NEW.model) IS DISTINCT FROM ROW(OLD.provider, OLD.base_url, OLD.model) THEN
            NEW.skills_level := NULL; NEW.skills_score := NULL; NEW.skills_total := NULL; NEW.skills_model := NULL; NEW.skills_tested_at := NULL;
            NEW.default_trust := 'draft';
        END IF;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id
            AND c.id = NEW.connection_id AND c.access = 'propose') THEN
        RAISE EXCEPTION 'the built-in agent works through a proposing connection' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['agent_policy', 'agent_tax_sensitive_accounts', 'agent_kinds', 'agent_kind_events'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY agent_read ON public.%I FOR SELECT USING (public.ledger_allowed(tenant_id, book_id, false) OR access.owner_allowed(tenant_id))', tab);
    END LOOP;
    FOREACH tab IN ARRAY ARRAY['agent_policy', 'agent_tax_sensitive_accounts'] LOOP
        EXECUTE format('CREATE POLICY owner_insert ON public.%I FOR INSERT WITH CHECK (public.rule_owner_allowed(tenant_id, book_id) OR public.ledger_allowed(tenant_id, book_id, true))', tab);
        EXECUTE format('CREATE POLICY owner_update ON public.%I FOR UPDATE USING (public.rule_owner_allowed(tenant_id, book_id)) WITH CHECK (public.rule_owner_allowed(tenant_id, book_id))', tab);
        EXECUTE format('CREATE POLICY owner_delete ON public.%I FOR DELETE USING (public.rule_owner_allowed(tenant_id, book_id))', tab);
    END LOOP;
END $$;

REVOKE ALL ON FUNCTION public.ledger_confident_recording(), public.agent_amount_cap(uuid, uuid), public.agent_kind_note(uuid, uuid, uuid, text, uuid), public.agent_default_trust(uuid, uuid), public.agent_other_account(uuid, uuid, uuid, jsonb, uuid),
    public.agent_kind_decision(), public.agent_kind_undone(), public.agent_kind_unlinked(), public.confident_link_final(),
    public.agent_limits_guard(), public.rule_kind_trusted(), public.agent_kind_rule(uuid, uuid, uuid, text, text, uuid), public.agent_record_skills(uuid, uuid, text, text, integer, integer) FROM PUBLIC;

-- Continuity: what an agent asked and was told, what it said it would do,
-- and what it remembers about the person. Every row is one the person can
-- see and change; money facts are never stored here, only read fresh.

-- An agent connection of the Book (proposing or writing), or the owner.
CREATE FUNCTION public.agent_note_allowed(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT public.ledger_context(t, b) AND (EXISTS (SELECT 1 FROM public.connections c
        WHERE c.tenant_id = t AND c.book_id = b AND c.id::text = coalesce(current_setting('vult.connection_id', true), '')
        AND c.actor_id::text = coalesce(current_setting('vult.actor_id', true), '')
        AND c.revoked_at IS NULL AND c.access IN ('propose', 'write'))
    OR coalesce(current_setting('vult.connection_id', true), '') = '' AND access.owner_decision_allowed(t, b))
$$;
CREATE FUNCTION public.agent_note_owner(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT public.ledger_context(t, b) AND coalesce(current_setting('vult.connection_id', true), '') = '' AND access.owner_decision_allowed(t, b)
$$;

-- A question is asked once per subject. An answer settles it; only the
-- owner changing the answer reopens it, as a new question.
CREATE TABLE public.agent_questions (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    connection_id uuid,
    actor_id uuid NOT NULL,
    subject_kind text NOT NULL CHECK (subject_kind IN ('bank_item', 'kind', 'goal', 'preference', 'account', 'other')),
    subject_key text NOT NULL CHECK (length(subject_key) BETWEEN 1 AND 200),
    question text NOT NULL CHECK (length(btrim(question)) BETWEEN 1 AND 1000),
    answer jsonb CHECK (answer IS NULL OR octet_length(answer::text) <= 4096),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'answered', 'withdrawn', 'superseded')),
    asked_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    answered_at timestamptz,
    answered_by uuid,
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 1000),
    UNIQUE (tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    CHECK ((status = 'answered') <= (answer IS NOT NULL AND answered_at IS NOT NULL))
);
CREATE UNIQUE INDEX agent_questions_once ON public.agent_questions(tenant_id, book_id, subject_kind, subject_key) WHERE status IN ('open', 'answered');

-- Loose ends: what an agent said it would do, or was asked to remind.
CREATE TABLE public.agent_loops (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    connection_id uuid,
    actor_id uuid NOT NULL,
    request_key text NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
    kind text NOT NULL CHECK (kind IN ('promise', 'follow_up', 'reminder')),
    subject_kind text CHECK (subject_kind IN ('bank_item', 'question', 'account', 'other')),
    subject_key text CHECK (length(subject_key) BETWEEN 1 AND 200),
    text text NOT NULL CHECK (length(btrim(text)) BETWEEN 1 AND 1000),
    due_on date,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'done', 'dropped')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    closed_at timestamptz,
    closed_reason text NOT NULL DEFAULT '' CHECK (length(closed_reason) <= 1000),
    UNIQUE (tenant_id, book_id, actor_id, request_key),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id),
    CHECK ((subject_kind IS NULL) = (subject_key IS NULL)),
    CHECK ((status = 'open') = (closed_at IS NULL))
);

-- What the agent remembers about the person: tone and preferences, never
-- money and never standing instructions that change the books (those are
-- rules). Small by design.
CREATE TABLE public.agent_profile (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    goal text NOT NULL DEFAULT '' CHECK (length(goal) <= 280),
    preferences jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(preferences) = 'object' AND octet_length(preferences::text) <= 2048),
    instructions text NOT NULL DEFAULT '' CHECK (length(instructions) <= 1024),
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 1000),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

-- When each person last looked, for "since you were last here".
CREATE TABLE public.agent_seen (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    person uuid NOT NULL,
    seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, person),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);

CREATE FUNCTION public.agent_note_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE owner boolean; open_loops integer;
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'notes are closed or replaced, never deleted' USING ERRCODE = '23514';
    END IF;
    -- Closing a loop because its item was recorded or its question settled
    -- happens with whoever did that; it changes nothing else.
    IF TG_TABLE_NAME = 'agent_loops' AND TG_OP = 'UPDATE' AND coalesce(current_setting('vult.loop_autoclose', true), '') = 'on' THEN
        IF OLD.status = 'open' AND NEW.status = 'done' AND NEW.closed_reason IN ('The item was recorded.', 'The question was answered.', 'The question was withdrawn.')
            AND (to_jsonb(NEW) - ARRAY['status', 'closed_at', 'closed_reason']) = (to_jsonb(OLD) - ARRAY['status', 'closed_at', 'closed_reason']) THEN
            RETURN NEW;
        END IF;
    END IF;
    owner := public.agent_note_owner(NEW.tenant_id, NEW.book_id);
    IF TG_TABLE_NAME = 'agent_seen' THEN
        -- Any signed-in visit counts; no fresh sign-in is needed to look.
        IF NOT (public.ledger_context(NEW.tenant_id, NEW.book_id) AND coalesce(current_setting('vult.connection_id', true), '') = ''
                AND access.owner_allowed(NEW.tenant_id)) OR NEW.person::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'a person records only when they last looked' USING ERRCODE = '42501';
        END IF;
        NEW.seen_at := clock_timestamp();
        RETURN NEW;
    END IF;
    IF NOT public.agent_note_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the Book''s agents and its owner keep these notes' USING ERRCODE = '42501';
    END IF;
    IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF TG_TABLE_NAME = 'agent_profile' THEN
        IF TG_OP = 'UPDATE' AND NEW.revision <> OLD.revision + 1 OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'the profile changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' THEN
        NEW.connection_id := nullif(current_setting('vult.connection_id', true), '')::uuid;
        IF TG_TABLE_NAME = 'agent_questions' THEN
            IF NEW.status <> 'open' AND NOT owner OR NEW.answer IS NOT NULL AND NOT owner THEN
                RAISE EXCEPTION 'only the owner answers a question' USING ERRCODE = '42501';
            END IF;
            NEW.asked_at := clock_timestamp();
        ELSE
            IF NEW.status <> 'open' THEN
                RAISE EXCEPTION 'a loop opens open' USING ERRCODE = '23514';
            END IF;
            SELECT count(*) INTO open_loops FROM public.agent_loops WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND status = 'open';
            IF open_loops >= 50 THEN
                RAISE EXCEPTION 'this Book has 50 open loops; close some first' USING ERRCODE = '23514';
            END IF;
            NEW.created_at := clock_timestamp();
        END IF;
        RETURN NEW;
    END IF;
    -- Updates: a question is answered by the owner, withdrawn by its asker
    -- or the owner, or superseded by the owner; a loop is closed once.
    IF TG_TABLE_NAME = 'agent_questions' THEN
        IF (to_jsonb(NEW) - ARRAY['answer', 'status', 'answered_at', 'answered_by', 'reason']) IS DISTINCT FROM
            (to_jsonb(OLD) - ARRAY['answer', 'status', 'answered_at', 'answered_by', 'reason'])
            OR NOT (OLD.status = 'open' AND NEW.status = 'answered' AND owner
                OR OLD.status = 'open' AND NEW.status = 'withdrawn' AND (owner OR OLD.connection_id::text = current_setting('vult.connection_id', true))
                OR OLD.status = 'answered' AND NEW.status = 'superseded' AND owner) THEN
            RAISE EXCEPTION 'a question is answered or replaced by the owner, or withdrawn by whoever asked it' USING ERRCODE = '42501';
        END IF;
        IF NEW.status = 'answered' THEN
            NEW.answered_at := clock_timestamp();
            NEW.answered_by := current_setting('vult.actor_id', true)::uuid;
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status <> 'open' OR NEW.status = 'open'
        OR (to_jsonb(NEW) - ARRAY['status', 'closed_at', 'closed_reason']) IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['status', 'closed_at', 'closed_reason']) THEN
        RAISE EXCEPTION 'a loop is closed once, with a reason' USING ERRCODE = '23514';
    END IF;
    NEW.closed_at := clock_timestamp();
    RETURN NEW;
END $$;
CREATE TRIGGER agent_note_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_questions FOR EACH ROW EXECUTE FUNCTION public.agent_note_guard();
CREATE TRIGGER agent_note_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_loops FOR EACH ROW EXECUTE FUNCTION public.agent_note_guard();
CREATE TRIGGER agent_note_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_profile FOR EACH ROW EXECUTE FUNCTION public.agent_note_guard();
CREATE TRIGGER agent_note_guard BEFORE INSERT OR UPDATE OR DELETE ON public.agent_seen FOR EACH ROW EXECUTE FUNCTION public.agent_note_guard();

-- A loop about a bank item closes when the item is recorded; one about a
-- question, when it's answered or withdrawn.
CREATE FUNCTION public.agent_loops_close() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    PERFORM set_config('vult.loop_autoclose', 'on', true);
    IF TG_TABLE_NAME = 'bank_transaction_links' THEN
        UPDATE public.agent_loops SET status = 'done', closed_at = clock_timestamp(), closed_reason = 'The item was recorded.'
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND status = 'open' AND subject_kind = 'bank_item' AND subject_key = NEW.transaction_id::text;
    ELSIF NEW.status IN ('answered', 'withdrawn') THEN
        UPDATE public.agent_loops SET status = 'done', closed_at = clock_timestamp(),
            closed_reason = CASE WHEN NEW.status = 'answered' THEN 'The question was answered.' ELSE 'The question was withdrawn.' END
            WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND status = 'open' AND subject_kind = 'question' AND subject_key = NEW.id::text;
    END IF;
    PERFORM set_config('vult.loop_autoclose', '', true);
    RETURN NULL;
END $$;
CREATE TRIGGER agent_loops_close AFTER INSERT ON public.bank_transaction_links FOR EACH ROW EXECUTE FUNCTION public.agent_loops_close();
CREATE TRIGGER agent_loops_close AFTER UPDATE ON public.agent_questions
    FOR EACH ROW WHEN (OLD.status = 'open' AND NEW.status <> 'open') EXECUTE FUNCTION public.agent_loops_close();

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['agent_questions', 'agent_loops', 'agent_profile', 'agent_seen'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY agent_read ON public.%I FOR SELECT USING (public.ledger_allowed(tenant_id, book_id, false) OR access.owner_allowed(tenant_id))', tab);
    END LOOP;
    FOREACH tab IN ARRAY ARRAY['agent_questions', 'agent_loops', 'agent_profile'] LOOP
        EXECUTE format('CREATE POLICY agent_note_insert ON public.%I FOR INSERT WITH CHECK (public.agent_note_allowed(tenant_id, book_id))', tab);
        EXECUTE format('CREATE POLICY agent_note_update ON public.%I FOR UPDATE USING (public.agent_note_allowed(tenant_id, book_id)) WITH CHECK (public.agent_note_allowed(tenant_id, book_id))', tab);
    END LOOP;
END $$;
CREATE POLICY owner_seen_insert ON public.agent_seen FOR INSERT WITH CHECK (access.owner_allowed(tenant_id));
CREATE POLICY owner_seen_update ON public.agent_seen FOR UPDATE USING (access.owner_allowed(tenant_id)) WITH CHECK (access.owner_allowed(tenant_id));
REVOKE ALL ON FUNCTION public.agent_note_allowed(uuid, uuid), public.agent_note_owner(uuid, uuid), public.agent_note_guard(), public.agent_loops_close() FROM PUBLIC;

-- A starter chart also goes into a Book whose only accounts are the ones
-- its bank accounts were linked to, as when a Book starts from the bank.
CREATE OR REPLACE FUNCTION public.ledger_starter_chart_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.operation = 'apply_starter_chart' AND NOT public.ledger_restoring(NEW.tenant_id, NEW.book_id) AND EXISTS(
        SELECT 1 FROM public.accounts a WHERE a.tenant_id = NEW.tenant_id AND a.book_id = NEW.book_id
        AND NOT public.ledger_written_here(a.xmin)
        AND NOT EXISTS (SELECT 1 FROM public.bank_accounts b WHERE b.tenant_id = a.tenant_id AND b.book_id = a.book_id AND b.account_id = a.id)) THEN
        RAISE EXCEPTION 'a starter chart applies only to a Book with no accounts, or only those its bank accounts are linked to' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;

-- Stopping an agent is the easiest thing an owner does: any valid sign-in may
-- disconnect one, without signing in again. The change it allows only ends a
-- connection; giving or changing access still needs a fresh sign-in.
CREATE POLICY owner_disconnect ON public.connections FOR UPDATE
    USING (access.owner_allowed(tenant_id) AND revoked_at IS NULL)
    WITH CHECK (access.owner_allowed(tenant_id) AND revoked_at IS NOT NULL);
CREATE FUNCTION public.connection_disconnect_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    -- Without a fresh sign-in, an owner's change to a connection may only
    -- set when it was disconnected.
    IF NOT access.owner_write_allowed(NEW.tenant_id) AND access.owner_allowed(NEW.tenant_id)
        AND (OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL
            OR (to_jsonb(NEW) - 'revoked_at') IS DISTINCT FROM (to_jsonb(OLD) - 'revoked_at')) THEN
        RAISE EXCEPTION 'owner authorization required' USING ERRCODE = '42501';
    END IF;
    RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION public.connection_disconnect_guard() FROM PUBLIC;
CREATE TRIGGER connection_disconnect_guard BEFORE UPDATE ON public.connections
    FOR EACH ROW EXECUTE FUNCTION public.connection_disconnect_guard();

-- Narrowing what an agent may do needs only a valid sign-in, as stopping it
-- does: turning a rule off, recording by rules instead of when sure, drafts
-- only, a lower limit, or keeping an account to the owner's rules. Giving
-- an agent more still needs a recent sign-in. The guards below allow each
-- such change only when it narrows.
CREATE FUNCTION public.rule_owner_narrowing(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SET search_path = pg_catalog AS $$
SELECT NOT public.ledger_import_owner() AND access.owner_document_allowed(t, b)
$$;
REVOKE ALL ON FUNCTION public.rule_owner_narrowing(uuid, uuid) FROM PUBLIC;

CREATE POLICY rule_owner_narrow ON public.recording_rules FOR UPDATE
    USING (public.rule_owner_narrowing(tenant_id, book_id)) WITH CHECK (public.rule_owner_narrowing(tenant_id, book_id));
CREATE POLICY rule_owner_narrow ON public.rule_recording_grants FOR UPDATE
    USING (public.rule_owner_narrowing(tenant_id, book_id)) WITH CHECK (public.rule_owner_narrowing(tenant_id, book_id));
CREATE POLICY owner_narrow_insert ON public.agent_policy FOR INSERT WITH CHECK (public.rule_owner_narrowing(tenant_id, book_id));
CREATE POLICY owner_narrow_update ON public.agent_policy FOR UPDATE
    USING (public.rule_owner_narrowing(tenant_id, book_id)) WITH CHECK (public.rule_owner_narrowing(tenant_id, book_id));
CREATE POLICY owner_narrow_insert ON public.agent_tax_sensitive_accounts FOR INSERT WITH CHECK (public.rule_owner_narrowing(tenant_id, book_id));

-- Whether this change to a rule, a grant or the limits only narrows what
-- agents may do.
CREATE FUNCTION public.agent_change_narrows(tab text, op text, o jsonb, n jsonb) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT CASE
    WHEN tab = 'recording_rules' AND op = 'UPDATE' THEN
        (o ->> 'active')::boolean AND NOT (n ->> 'active')::boolean
        AND (n - ARRAY['active', 'actor_id', 'updated_at', 'revision']) = (o - ARRAY['active', 'actor_id', 'updated_at', 'revision'])
    WHEN tab = 'rule_recording_grants' AND op = 'UPDATE' THEN
        o ->> 'revoked_at' IS NULL AND (
            n ->> 'revoked_at' IS NOT NULL AND (n - 'revoked_at') = (o - 'revoked_at')
            OR n ->> 'revoked_at' IS NULL AND o ->> 'recording_mode' = 'confident' AND n ->> 'recording_mode' = 'rules'
                AND (n - 'recording_mode') = (o - 'recording_mode'))
    WHEN tab = 'agent_policy' AND op = 'UPDATE' THEN
        (n ->> 'amount_cap_minor')::bigint < (o ->> 'amount_cap_minor')::bigint
        AND (n - ARRAY['amount_cap_minor', 'revision', 'actor_id', 'updated_at']) = (o - ARRAY['amount_cap_minor', 'revision', 'actor_id', 'updated_at'])
    WHEN tab = 'agent_policy' AND op = 'INSERT' THEN
        (n ->> 'amount_cap_minor')::bigint < public.agent_amount_cap((n ->> 'tenant_id')::uuid, (n ->> 'book_id')::uuid)
    WHEN tab = 'agent_tax_sensitive_accounts' AND op = 'INSERT' THEN true
    ELSE false
END
$$;
REVOKE ALL ON FUNCTION public.agent_change_narrows(text, text, jsonb, jsonb) FROM PUBLIC;

-- The owner may make this change: from a recent sign-in, or from any valid
-- one when it only narrows what agents may do.
CREATE FUNCTION public.agent_owner_change_allowed(t uuid, b uuid, tab text, op text, o jsonb, n jsonb) RETURNS boolean
    LANGUAGE sql VOLATILE SET search_path = pg_catalog AS $$
SELECT public.rule_owner_allowed(t, b) OR public.rule_owner_narrowing(t, b) AND public.agent_change_narrows(tab, op, o, n)
$$;
REVOKE ALL ON FUNCTION public.agent_owner_change_allowed(uuid, uuid, text, text, jsonb, jsonb) FROM PUBLIC;

-- The rule and grant guards, as before, admitting a narrowing change on any
-- valid sign-in.
CREATE OR REPLACE FUNCTION public.recording_rule_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE a public.accounts%ROWTYPE; o public.accounts%ROWTYPE;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a rule is turned off, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.agent_owner_change_allowed(NEW.tenant_id, NEW.book_id, TG_TABLE_NAME, TG_OP,
            CASE WHEN TG_OP = 'UPDATE' THEN to_jsonb(OLD) END, to_jsonb(NEW)) THEN
        RAISE EXCEPTION 'only the owner creates or changes a recording rule' USING ERRCODE = '42501';
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.updated_at := clock_timestamp();
        IF TG_OP = 'INSERT' THEN
            NEW.created_at := clock_timestamp();
            NEW.revision := 1;
        ELSE
            IF ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.created_at) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.created_at) THEN
                RAISE EXCEPTION 'a rule keeps its identity' USING ERRCODE = '23514';
            END IF;
            NEW.revision := OLD.revision + 1;
        END IF;
    END IF;
    -- Classified by account types, never by names: the money moves on one
    -- of the owner's asset or liability accounts, and the other side fits
    -- its direction.
    SELECT * INTO STRICT a FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.account_id;
    SELECT * INTO STRICT o FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.other_account_id;
    IF a.class NOT IN ('asset', 'liability') THEN
        RAISE EXCEPTION 'a rule moves money on an asset or liability account' USING ERRCODE = '23514';
    END IF;
    IF NEW.direction = 'out' AND o.class = 'income' OR NEW.direction = 'in' AND o.class = 'expense' THEN
        RAISE EXCEPTION 'money out isn''t income and money in isn''t an expense; record those case by case' USING ERRCODE = '23514';
    END IF;
    IF o.status = 'retired' OR o.heading OR a.status = 'retired' THEN
        RAISE EXCEPTION 'a rule uses accounts that take postings' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION public.rule_grant_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a grant is revoked, never deleted' USING ERRCODE = '23514';
    END IF;
    IF NOT public.agent_owner_change_allowed(NEW.tenant_id, NEW.book_id, TG_TABLE_NAME, TG_OP,
            CASE WHEN TG_OP = 'UPDATE' THEN to_jsonb(OLD) END, to_jsonb(NEW)) THEN
        RAISE EXCEPTION 'only the owner grants recording within rules' USING ERRCODE = '42501';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.tenant_id = NEW.tenant_id AND c.book_id = NEW.book_id
            AND c.id = NEW.connection_id AND c.access = 'propose' AND c.revoked_at IS NULL) THEN
        RAISE EXCEPTION 'recording within rules is granted to a proposing connection' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;

-- ------------------------------------------------------------------------
-- 0012 owner_paid
-- ------------------------------------------------------------------------

-- Business costs the owner pays personally: a business Book and a personal
-- Book of the same owner are linked, and a business cost paid on a personal
-- card is recorded in both as one pair, decided once. Each Book stays
-- balanced on its own; classification is by account type and role, never by
-- name.

-- The owner's link between a business Book and a personal Book, with how
-- owner-paid costs are recorded and the one account on each side for them.
CREATE TABLE public.book_links (
    tenant_id uuid NOT NULL,
    business_book_id uuid NOT NULL,
    personal_book_id uuid NOT NULL,
    mode text NOT NULL CHECK (mode IN ('contribution', 'owed')),
    business_account_id uuid NOT NULL,
    personal_account_id uuid NOT NULL,
    active boolean NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, business_book_id, personal_book_id),
    UNIQUE (tenant_id, business_book_id),
    UNIQUE (tenant_id, personal_book_id),
    CHECK (business_book_id <> personal_book_id),
    FOREIGN KEY (tenant_id, business_book_id) REFERENCES public.books(tenant_id, id),
    FOREIGN KEY (tenant_id, personal_book_id) REFERENCES public.books(tenant_id, id),
    FOREIGN KEY (tenant_id, business_book_id, business_account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, personal_book_id, personal_account_id) REFERENCES public.accounts(tenant_id, book_id, id)
);

-- Two waiting proposals, one in each linked Book, decided as one.
CREATE TABLE public.proposal_pairs (
    tenant_id uuid NOT NULL,
    pair_key text NOT NULL CHECK (pair_key ~ '^[A-Za-z0-9_-]{1,64}$'),
    business_book_id uuid NOT NULL,
    business_proposal_id uuid NOT NULL,
    personal_book_id uuid NOT NULL,
    personal_proposal_id uuid NOT NULL,
    transaction_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, business_book_id, business_proposal_id),
    UNIQUE (tenant_id, personal_book_id, personal_proposal_id),
    FOREIGN KEY (tenant_id, business_book_id, business_proposal_id) REFERENCES public.operation_proposals(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, personal_book_id, personal_proposal_id) REFERENCES public.operation_proposals(tenant_id, book_id, id)
);

-- The two entries a decided pair posted.
CREATE TABLE public.entry_pairs (
    tenant_id uuid NOT NULL,
    pair_key text NOT NULL,
    business_book_id uuid NOT NULL,
    business_entry_id uuid NOT NULL,
    personal_book_id uuid NOT NULL,
    personal_entry_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, business_book_id, business_entry_id),
    UNIQUE (tenant_id, personal_book_id, personal_entry_id)
);

-- The owner may change a link: a recent owner sign-in acting directly, or an
-- operator import.
CREATE FUNCTION public.book_link_owner(t uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SET search_path = pg_catalog AS $$
SELECT public.ledger_import_owner()
    OR coalesce(current_setting('vult.connection_id', true), '') = ''
        AND coalesce(current_setting('vult.owner_posting', true), '') = ''
        AND access.owner_write_allowed(t)
        AND coalesce(access.owner_identity(t)::text = current_setting('vult.actor_id', true), false)
$$;

CREATE FUNCTION public.book_link_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE ba public.accounts%ROWTYPE; pa public.accounts%ROWTYPE;
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END) THEN
        RAISE EXCEPTION 'only the owner links Books' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF EXISTS (SELECT 1 FROM public.proposal_pairs p WHERE p.tenant_id = OLD.tenant_id AND p.business_book_id = OLD.business_book_id AND p.personal_book_id = OLD.personal_book_id)
            OR EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = OLD.tenant_id AND p.business_book_id = OLD.business_book_id AND p.personal_book_id = OLD.personal_book_id) THEN
            RAISE EXCEPTION 'These Books have pairs recorded between them; turn the link off instead.' USING ERRCODE = 'VP020';
        END IF;
        RETURN OLD;
    END IF;
    IF NOT public.ledger_import_owner() AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF ROW(NEW.tenant_id, NEW.business_book_id, NEW.personal_book_id, NEW.created_at) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.business_book_id, OLD.personal_book_id, OLD.created_at) THEN
            RAISE EXCEPTION 'a link keeps its Books' USING ERRCODE = '23514';
        END IF;
        IF NEW.revision <> OLD.revision + 1 THEN
            RAISE EXCEPTION 'the link changed concurrently' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.revision <> 1 THEN
        RAISE EXCEPTION 'the link changed concurrently' USING ERRCODE = '23514';
    END IF;
    NEW.updated_at := clock_timestamp();
    -- A Book's profile is its latest settings revision.
    IF (SELECT r.profile FROM public.book_revisions r WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.business_book_id ORDER BY r.revision DESC LIMIT 1) IS DISTINCT FROM 'business'
        OR (SELECT r.profile FROM public.book_revisions r WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.personal_book_id ORDER BY r.revision DESC LIMIT 1) IS DISTINCT FROM 'personal' THEN
        RAISE EXCEPTION 'A link joins a business Book and a personal Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT * INTO ba FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.business_book_id AND id = NEW.business_account_id;
    SELECT * INTO pa FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.personal_book_id AND id = NEW.personal_account_id;
    IF ba.status = 'retired' OR ba.heading OR pa.status = 'retired' OR pa.heading THEN
        RAISE EXCEPTION 'A link uses accounts that take postings.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.mode = 'contribution' AND (ba.class <> 'equity' OR pa.class <> 'asset' OR pa.role IS DISTINCT FROM 'investment') THEN
        RAISE EXCEPTION 'Money you put in is recorded to an equity account in the business Book and an investment asset in the personal Book.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.mode = 'owed' AND (ba.class <> 'liability' OR ba.role IS DISTINCT FROM 'payable' OR pa.class <> 'asset' OR pa.role IS DISTINCT FROM 'receivable') THEN
        RAISE EXCEPTION 'Money owed back to you is recorded to a payable in the business Book and a receivable in the personal Book.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER book_link_guard BEFORE INSERT OR UPDATE OR DELETE ON public.book_links
    FOR EACH ROW EXECUTE FUNCTION public.book_link_guard();

-- The link that names Book b, read by the owner or by any connection of b.
CREATE FUNCTION public.book_link_for(t uuid, b uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL
    ELSE (SELECT jsonb_build_object('linked', true,
        'linked_book_id', CASE WHEN l.business_book_id = b THEN l.personal_book_id ELSE l.business_book_id END,
        'linked_book_profile', CASE WHEN l.business_book_id = b THEN 'personal' ELSE 'business' END,
        'linked_book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN l.business_book_id = b THEN l.personal_book_id ELSE l.business_book_id END),
        'business_book_id', l.business_book_id, 'personal_book_id', l.personal_book_id,
        'mode', l.mode, 'business_account_id', l.business_account_id, 'personal_account_id', l.personal_account_id,
        'active', l.active, 'revision', l.revision)
        FROM public.book_links l WHERE l.tenant_id = t AND (l.business_book_id = b OR l.personal_book_id = b)) END
$$;

-- Joins the business half of a pair, just prepared in Book b, to its waiting
-- personal half, after checking both against the link and the bank item. A
-- personal half only checks its own side and waits. Refusals say why.
CREATE FUNCTION public.pair_proposal(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; lk public.book_links%ROWTYPE;
    item public.bank_transactions%ROWTYPE; args jsonb; oargs jsonb; key text; line jsonb; acc public.accounts%ROWTYPE;
    amount bigint; debits bigint := 0; credits int := 0; paid text;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    IF NOT FOUND OR NOT public.ledger_allowed(t, b, false)
        OR me.connection_id::text IS DISTINCT FROM current_setting('vult.connection_id', true) THEN
        RAISE EXCEPTION 'only the preparing connection pairs its proposal' USING ERRCODE = '42501';
    END IF;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    IF key IS NULL THEN
        RETURN;
    END IF;
    IF me.operation = 'record_bank_activity' THEN
        SELECT * INTO lk FROM public.book_links WHERE tenant_id = t AND personal_book_id = b AND active;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'The pair can''t be prepared: this Book isn''t linked to a business Book.' USING ERRCODE = 'VP020';
        END IF;
        IF args ->> 'other_account_id' IS DISTINCT FROM lk.personal_account_id::text THEN
            RAISE EXCEPTION 'The pair can''t be prepared: the personal side records the card charge to the link''s account (other_account_id %).', lk.personal_account_id USING ERRCODE = 'VP020';
        END IF;
        RETURN;
    END IF;
    IF me.operation <> 'post_journal' THEN
        RAISE EXCEPTION 'The pair can''t be prepared: a pair is record_bank_activity in the personal Book and post_journal in the business Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT * INTO lk FROM public.book_links WHERE tenant_id = t AND business_book_id = b AND active;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: this Book isn''t linked to a personal Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id = lk.personal_book_id AND q.operation = 'record_bank_activity'
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: prepare the personal half first; no waiting personal half has pair_key %.', key USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = t AND x.personal_book_id = other.book_id AND x.personal_proposal_id = other.id) THEN
        RAISE EXCEPTION 'The pair can''t be prepared: its personal half is already paired.' USING ERRCODE = 'VP020';
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF oargs ->> 'transaction_id' IS DISTINCT FROM args ->> 'paired_transaction_id' THEN
        RAISE EXCEPTION 'The pair can''t be prepared: paired_transaction_id isn''t the card charge its personal half records.' USING ERRCODE = 'VP020';
    END IF;
    IF oargs ->> 'other_account_id' IS DISTINCT FROM lk.personal_account_id::text THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the personal half doesn''t record to the link''s account.' USING ERRCODE = 'VP020';
    END IF;
    SELECT * INTO item FROM public.bank_transactions WHERE tenant_id = t AND book_id = lk.personal_book_id AND id = (oargs ->> 'transaction_id')::uuid;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the card charge isn''t in the personal Book.' USING ERRCODE = 'VP020';
    END IF;
    -- Amounts follow the bank account's own balance: a card charge raises a
    -- liability (positive), money paid from an account lowers an asset
    -- (negative). A pair records money the owner paid out.
    paid := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = lk.personal_book_id
        AND a.id = coalesce((SELECT x.account_id FROM public.bank_accounts x WHERE x.tenant_id = t AND x.book_id = lk.personal_book_id AND x.id = item.bank_account_id), item.account_id));
    IF NOT (paid = 'liability' AND item.amount_minor > 0 OR paid = 'asset' AND item.amount_minor < 0) THEN
        RAISE EXCEPTION 'The pair can''t be prepared: a pair records money paid out, such as a charge on the card.' USING ERRCODE = 'VP020';
    END IF;
    amount := abs(item.amount_minor);
    IF (SELECT k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = b) IS DISTINCT FROM item.currency THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the charge is in another currency than the business Book.' USING ERRCODE = 'VP020';
    END IF;
    IF args ->> 'accounting_date' IS DISTINCT FROM item.occurred_on::text THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the business entry''s date isn''t the charge''s date (%).', item.occurred_on USING ERRCODE = 'VP020';
    END IF;
    FOR line IN SELECT * FROM jsonb_array_elements(coalesce(args -> 'lines', '[]'::jsonb)) LOOP
        SELECT * INTO acc FROM public.accounts WHERE tenant_id = t AND book_id = b AND id = (line ->> 'account_id')::uuid;
        IF line ->> 'side' = 'credit' THEN
            credits := credits + 1;
            IF acc.id IS DISTINCT FROM lk.business_account_id OR (line ->> 'amount_minor')::bigint <> amount THEN
                RAISE EXCEPTION 'The pair can''t be prepared: the business entry credits the link''s account (%) with the whole charge.', lk.business_account_id USING ERRCODE = 'VP020';
            END IF;
        ELSE
            IF NOT (acc.class = 'expense' OR acc.class = 'asset' AND coalesce(acc.role, '') NOT IN ('cash', 'card')
                    AND NOT EXISTS (SELECT 1 FROM public.bank_accounts x WHERE x.tenant_id = t AND x.book_id = b AND x.account_id = acc.id)) THEN
                RAISE EXCEPTION 'The pair can''t be prepared: the business entry debits only expenses, or assets that aren''t cash, a card or a bank account.' USING ERRCODE = 'VP020';
            END IF;
            debits := debits + (line ->> 'amount_minor')::bigint;
        END IF;
    END LOOP;
    IF credits <> 1 OR debits <> amount THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the business entry is for the charge''s amount, with one credit to the link''s account.' USING ERRCODE = 'VP020';
    END IF;
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, business_book_id, business_proposal_id, personal_book_id, personal_proposal_id, transaction_id)
        VALUES (t, key, b, p, other.book_id, other.id, item.id);
END $$;

-- The pair a proposal belongs to, for reads in either Book: the partner's
-- Book, proposal and side.
CREATE FUNCTION public.proposal_pair_of(t uuid, b uuid, p uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE (
    SELECT jsonb_build_object('pair_key', x.pair_key, 'transaction_id', x.transaction_id,
        'book_id', CASE WHEN x.business_book_id = b THEN x.personal_book_id ELSE x.business_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.business_book_id = b THEN x.personal_book_id ELSE x.business_book_id END),
        'proposal_id', CASE WHEN x.business_book_id = b THEN x.personal_proposal_id ELSE x.business_proposal_id END,
        'side', CASE WHEN x.business_book_id = b THEN 'personal' ELSE 'business' END)
    FROM public.proposal_pairs x WHERE x.tenant_id = t
        AND (x.business_book_id = b AND x.business_proposal_id = p OR x.personal_book_id = b AND x.personal_proposal_id = p)) END
$$;

-- The other entry of a decided pair, for the owner or a connection of
-- Book b: its Book, entry and side.
CREATE FUNCTION public.entry_pair_of(t uuid, b uuid, e uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE (
    SELECT jsonb_build_object('pair_key', x.pair_key,
        'book_id', CASE WHEN x.business_book_id = b THEN x.personal_book_id ELSE x.business_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.business_book_id = b THEN x.personal_book_id ELSE x.business_book_id END),
        'entry_id', CASE WHEN x.business_book_id = b THEN x.personal_entry_id ELSE x.business_entry_id END,
        'side', CASE WHEN x.business_book_id = b THEN 'personal' ELSE 'business' END)
    FROM public.entry_pairs x WHERE x.tenant_id = t
        AND (x.business_book_id = b AND x.business_entry_id = e OR x.personal_book_id = b AND x.personal_entry_id = e)) END
$$;

-- Undoing half of a pair reverses its partner too. That partner's reversal
-- follows the owner's decision on the other half; it isn't a correction of
-- the agent's kind of activity.
CREATE OR REPLACE FUNCTION public.agent_kind_undone() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE l record;
BEGIN
    IF coalesce(current_setting('vult.pair_partner', true), '') = 'on' THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = NEW.actor_id) THEN
        RETURN NULL;
    END IF;
    IF TG_TABLE_NAME = 'entries' THEN
        FOR l IN SELECT DISTINCT x.transaction_id FROM public.bank_transaction_links x
                WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.book_id AND x.entry_id = NEW.reversal_of
                AND EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = x.actor_id) LOOP
            PERFORM public.agent_kind_note(NEW.tenant_id, NEW.book_id, l.transaction_id, 'undone',
                public.agent_other_account(NEW.tenant_id, NEW.book_id, l.transaction_id, '{}'::jsonb, NEW.reversal_of));
        END LOOP;
    END IF;
    RETURN NULL;
END $$;

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['book_links', 'proposal_pairs', 'entry_pairs'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY pair_read ON public.%I FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, business_book_id, false) OR public.ledger_allowed(tenant_id, personal_book_id, false))', tab);
    END LOOP;
END $$;
CREATE POLICY owner_write ON public.book_links FOR INSERT WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_update ON public.book_links FOR UPDATE USING (public.book_link_owner(tenant_id)) WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_delete ON public.book_links FOR DELETE USING (public.book_link_owner(tenant_id));
-- The owner's decision posts both entries of a pair and records them here.
CREATE POLICY owner_pairs ON public.entry_pairs FOR INSERT WITH CHECK (access.owner_allowed(tenant_id) AND access.owner_write_allowed(tenant_id));

REVOKE ALL ON FUNCTION public.book_link_owner(uuid), public.book_link_guard(), public.book_link_for(uuid, uuid), public.pair_proposal(uuid, uuid, uuid),
    public.proposal_pair_of(uuid, uuid, uuid), public.entry_pair_of(uuid, uuid, uuid) FROM PUBLIC;

-- ------------------------------------------------------------------------
-- 0013 linked_books
-- ------------------------------------------------------------------------

-- Linked Books replaces the single link between a business Book and a
-- personal Book: any two Books of the same owner pay costs for each other.
-- The links and pairs recorded before are carried over as they were: the
-- personal Book pays for the business Book, by the link's mode, with the
-- same accounts.

-- Keep the earlier rows while their tables are replaced.
ALTER TABLE public.book_links NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.proposal_pairs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.entry_pairs NO FORCE ROW LEVEL SECURITY;
CREATE TEMP TABLE earlier_links AS SELECT * FROM public.book_links;
CREATE TEMP TABLE earlier_proposal_pairs AS SELECT * FROM public.proposal_pairs;
CREATE TEMP TABLE earlier_entry_pairs AS SELECT * FROM public.entry_pairs;
DROP TABLE public.entry_pairs, public.proposal_pairs, public.book_links;
DROP FUNCTION public.book_link_guard(), public.book_link_for(uuid, uuid), public.pair_proposal(uuid, uuid, uuid),
    public.proposal_pair_of(uuid, uuid, uuid), public.entry_pair_of(uuid, uuid, uuid), public.book_link_owner(uuid);

-- Linked Books: two Books of the same owner pay costs for each other. A cost
-- one Book pays for the other is recorded in both as one pair, decided and
-- undone as one; the money owed between them, or put in, is kept on the
-- accounts each direction's terms name. Each Book stays balanced on its own;
-- classification is by account type and role, never by name.

-- A link between two Books of one tenant (book_a < book_b).
CREATE TABLE public.book_links (
    tenant_id uuid NOT NULL,
    id uuid NOT NULL,
    book_a uuid NOT NULL,
    book_b uuid NOT NULL,
    active boolean NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, book_a, book_b),
    CHECK (book_a < book_b),
    FOREIGN KEY (tenant_id, book_a) REFERENCES public.books(tenant_id, id),
    FOREIGN KEY (tenant_id, book_b) REFERENCES public.books(tenant_id, id)
);

-- How a cost the payer Book pays for the beneficiary Book is recorded: the
-- treatment, and the account on each side.
CREATE TABLE public.book_link_terms (
    tenant_id uuid NOT NULL,
    link_id uuid NOT NULL,
    payer_book_id uuid NOT NULL,
    beneficiary_book_id uuid NOT NULL,
    treatment text NOT NULL CHECK (treatment IN ('contribution', 'owed', 'owner_equity')),
    payer_account_id uuid NOT NULL,
    beneficiary_account_id uuid NOT NULL,
    active boolean NOT NULL,
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, link_id, payer_book_id),
    UNIQUE (tenant_id, payer_book_id, beneficiary_book_id),
    CHECK (payer_book_id <> beneficiary_book_id),
    FOREIGN KEY (tenant_id, link_id) REFERENCES public.book_links(tenant_id, id),
    FOREIGN KEY (tenant_id, payer_book_id, payer_account_id) REFERENCES public.accounts(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, beneficiary_book_id, beneficiary_account_id) REFERENCES public.accounts(tenant_id, book_id, id)
);

-- Two waiting proposals, one in each linked Book, decided as one.
CREATE TABLE public.proposal_pairs (
    tenant_id uuid NOT NULL,
    pair_key text NOT NULL CHECK (pair_key ~ '^[A-Za-z0-9_-]{1,64}$'),
    kind text NOT NULL DEFAULT 'cost' CHECK (kind IN ('cost', 'settlement')),
    payer_book_id uuid NOT NULL,
    payer_proposal_id uuid NOT NULL,
    beneficiary_book_id uuid NOT NULL,
    beneficiary_proposal_id uuid NOT NULL,
    transaction_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, beneficiary_book_id, beneficiary_proposal_id),
    UNIQUE (tenant_id, payer_book_id, payer_proposal_id),
    FOREIGN KEY (tenant_id, payer_book_id, payer_proposal_id) REFERENCES public.operation_proposals(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, beneficiary_book_id, beneficiary_proposal_id) REFERENCES public.operation_proposals(tenant_id, book_id, id)
);

-- The two entries a decided pair posted.
CREATE TABLE public.entry_pairs (
    tenant_id uuid NOT NULL,
    pair_key text NOT NULL,
    kind text NOT NULL DEFAULT 'cost' CHECK (kind IN ('cost', 'settlement')),
    payer_book_id uuid NOT NULL,
    payer_entry_id uuid NOT NULL,
    beneficiary_book_id uuid NOT NULL,
    beneficiary_entry_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, beneficiary_book_id, beneficiary_entry_id),
    UNIQUE (tenant_id, payer_book_id, payer_entry_id)
);

-- The owner may change a link: a recent owner sign-in acting directly, or an
-- operator import.
CREATE FUNCTION public.book_link_owner(t uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SET search_path = pg_catalog AS $$
SELECT public.ledger_import_owner()
    OR coalesce(current_setting('vult.connection_id', true), '') = ''
        AND coalesce(current_setting('vult.owner_posting', true), '') = ''
        AND access.owner_write_allowed(t)
        AND coalesce(access.owner_identity(t)::text = current_setting('vult.actor_id', true), false)
$$;

-- A Book's profile is its latest settings revision.
CREATE FUNCTION public.book_profile(t uuid, b uuid) RETURNS text
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT r.profile FROM public.book_revisions r WHERE r.tenant_id = t AND r.book_id = b ORDER BY r.revision DESC LIMIT 1
$$;

CREATE FUNCTION public.book_link_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END) THEN
        RAISE EXCEPTION 'only the owner links Books' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND (p.payer_book_id, p.beneficiary_book_id) IN ((OLD.book_a, OLD.book_b), (OLD.book_b, OLD.book_a)))
            OR EXISTS (SELECT 1 FROM public.proposal_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND (p.payer_book_id, p.beneficiary_book_id) IN ((OLD.book_a, OLD.book_b), (OLD.book_b, OLD.book_a))) THEN
            RAISE EXCEPTION 'These Books have pairs between them; turn the link off instead.' USING ERRCODE = 'VP020';
        END IF;
        RETURN OLD;
    END IF;
    IF NOT public.ledger_import_owner() AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF ROW(NEW.tenant_id, NEW.id, NEW.book_a, NEW.book_b, NEW.created_at) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.id, OLD.book_a, OLD.book_b, OLD.created_at) THEN
            RAISE EXCEPTION 'a link keeps its Books' USING ERRCODE = '23514';
        END IF;
        IF NEW.revision <> OLD.revision + 1 THEN
            RAISE EXCEPTION 'the link changed concurrently' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.revision <> 1 THEN
        RAISE EXCEPTION 'the link changed concurrently' USING ERRCODE = '23514';
    END IF;
    NEW.updated_at := clock_timestamp();
    IF (SELECT k.currency FROM public.books k WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.book_a)
        IS DISTINCT FROM (SELECT k.currency FROM public.books k WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.book_b) THEN
        RAISE EXCEPTION 'Linked Books keep the same currency.' USING ERRCODE = 'VP020';
    END IF;
    IF public.book_profile(NEW.tenant_id, NEW.book_a) IS NULL OR public.book_profile(NEW.tenant_id, NEW.book_b) IS NULL THEN
        RAISE EXCEPTION 'Choose Personal or Business for both Books before linking them.' USING ERRCODE = 'VP020';
    END IF;
    IF public.book_profile(NEW.tenant_id, NEW.book_a) = 'personal' AND public.book_profile(NEW.tenant_id, NEW.book_b) = 'personal' THEN
        RAISE EXCEPTION 'Two personal Books aren''t linked; link a personal Book to a business.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER book_link_guard BEFORE INSERT OR UPDATE OR DELETE ON public.book_links
    FOR EACH ROW EXECUTE FUNCTION public.book_link_guard();

-- Each direction's treatment and accounts, checked by class and role:
--   personal -> business: contribution (asset role investment / equity), or owed;
--   business -> business: owed (asset role receivable / liability role payable);
--   business -> personal: owed, or owner_equity (equity / asset role investment).
-- An account carries one direction of one link only, so each balance is one link's.
CREATE FUNCTION public.book_link_terms_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE lk public.book_links%ROWTYPE; pa public.accounts%ROWTYPE; ba public.accounts%ROWTYPE; pp text; bp text;
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END) THEN
        RAISE EXCEPTION 'only the owner links Books' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN
        -- Terms go only with their link, and only while no pair names either direction.
        IF EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND p.payer_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id)
                AND p.beneficiary_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id))
            OR EXISTS (SELECT 1 FROM public.proposal_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND p.payer_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id)
                AND p.beneficiary_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id)) THEN
            RAISE EXCEPTION 'These Books have pairs between them; turn the link off instead.' USING ERRCODE = 'VP020';
        END IF;
        RETURN OLD;
    END IF;
    IF NOT public.ledger_import_owner() AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    NEW.updated_at := clock_timestamp();
    SELECT * INTO lk FROM public.book_links WHERE tenant_id = NEW.tenant_id AND id = NEW.link_id;
    IF NOT ((NEW.payer_book_id, NEW.beneficiary_book_id) IN ((lk.book_a, lk.book_b), (lk.book_b, lk.book_a))) THEN
        RAISE EXCEPTION 'a direction is between the link''s two Books' USING ERRCODE = '23514';
    END IF;
    pp := public.book_profile(NEW.tenant_id, NEW.payer_book_id);
    bp := public.book_profile(NEW.tenant_id, NEW.beneficiary_book_id);
    SELECT * INTO pa FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.payer_book_id AND id = NEW.payer_account_id;
    SELECT * INTO ba FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.beneficiary_book_id AND id = NEW.beneficiary_account_id;
    IF pa.status = 'retired' OR pa.heading OR ba.status = 'retired' OR ba.heading THEN
        RAISE EXCEPTION 'A link uses accounts that take postings.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'contribution' AND NOT (pp = 'personal' AND bp = 'business') THEN
        RAISE EXCEPTION 'Money put in is only from a personal Book to a business.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'owner_equity' AND NOT (pp = 'business' AND bp = 'personal') THEN
        RAISE EXCEPTION 'A draw or distribution is only from a business to a personal Book.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'contribution' AND (pa.class <> 'asset' OR pa.role IS DISTINCT FROM 'investment' OR ba.class <> 'equity') THEN
        RAISE EXCEPTION 'Money put in is recorded to an investment asset in the personal Book and an equity account in the business.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'owner_equity' AND (pa.class <> 'equity' OR ba.class <> 'asset' OR ba.role IS DISTINCT FROM 'investment') THEN
        RAISE EXCEPTION 'A draw or distribution is recorded to an equity account in the business and an investment asset in the personal Book.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'owed' AND (pa.class <> 'asset' OR pa.role IS DISTINCT FROM 'receivable' OR ba.class <> 'liability' OR ba.role IS DISTINCT FROM 'payable') THEN
        RAISE EXCEPTION 'Money owed back is recorded to a receivable in the Book that paid and a payable in the Book it paid for.' USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = NEW.tenant_id AND NOT (x.link_id = NEW.link_id AND x.payer_book_id = NEW.payer_book_id)
            AND (x.payer_book_id = NEW.payer_book_id AND x.payer_account_id = NEW.payer_account_id
                OR x.beneficiary_book_id = NEW.payer_book_id AND x.beneficiary_account_id = NEW.payer_account_id
                OR x.payer_book_id = NEW.beneficiary_book_id AND x.payer_account_id = NEW.beneficiary_account_id
                OR x.beneficiary_book_id = NEW.beneficiary_book_id AND x.beneficiary_account_id = NEW.beneficiary_account_id)) THEN
        RAISE EXCEPTION 'Each account carries one direction of one link, so its balance is that link''s; choose or add another account.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER book_link_terms_guard BEFORE INSERT OR UPDATE OR DELETE ON public.book_link_terms
    FOR EACH ROW EXECUTE FUNCTION public.book_link_terms_guard();

-- Book b's links, from b's side, for the owner or any connection of b.
CREATE FUNCTION public.book_links_for(t uuid, b uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE coalesce((
    SELECT jsonb_agg(jsonb_build_object('link_id', l.id, 'active', l.active, 'revision', l.revision,
        'other', (SELECT jsonb_build_object('id', k.id, 'name', k.name, 'profile', public.book_profile(t, k.id)) FROM public.books k
            WHERE k.tenant_id = t AND k.id = CASE WHEN l.book_a = b THEN l.book_b ELSE l.book_a END),
        'pays_for_them', (SELECT jsonb_build_object('treatment', x.treatment, 'my_account_id', x.payer_account_id, 'their_account_id', x.beneficiary_account_id, 'active', x.active)
            FROM public.book_link_terms x WHERE x.tenant_id = t AND x.link_id = l.id AND x.payer_book_id = b),
        'they_pay_for_me', (SELECT jsonb_build_object('treatment', x.treatment, 'my_account_id', x.beneficiary_account_id, 'their_account_id', x.payer_account_id, 'active', x.active)
            FROM public.book_link_terms x WHERE x.tenant_id = t AND x.link_id = l.id AND x.beneficiary_book_id = b)) ORDER BY l.created_at, l.id)
    FROM public.book_links l WHERE l.tenant_id = t AND (l.book_a = b OR l.book_b = b)), '[]'::jsonb) END
$$;

-- The active terms for a cost paid by Book p for Book q, if any.
CREATE FUNCTION public.active_link_terms(t uuid, p uuid, q uuid) RETURNS public.book_link_terms
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT x.* FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id
WHERE x.tenant_id = t AND x.payer_book_id = p AND x.beneficiary_book_id = q AND x.active AND l.active
$$;

-- Joins the beneficiary's half of a pair, just prepared in Book b, to its
-- waiting payer half, after checking both against the terms and the bank
-- item. A payer half only checks its own side and waits. Refusals say why.
CREATE FUNCTION public.pair_proposal(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; terms public.book_link_terms%ROWTYPE;
    item public.bank_transactions%ROWTYPE; args jsonb; oargs jsonb; key text; line jsonb; acc public.accounts%ROWTYPE;
    amount bigint; debits bigint := 0; credits int := 0; paid text;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    IF NOT FOUND OR NOT public.ledger_allowed(t, b, false)
        OR me.connection_id::text IS DISTINCT FROM current_setting('vult.connection_id', true) THEN
        RAISE EXCEPTION 'only the preparing connection pairs its proposal' USING ERRCODE = '42501';
    END IF;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    IF key IS NULL THEN
        RETURN;
    END IF;
    IF me.operation = 'record_bank_activity' AND (args -> 'settlement') = 'true'::jsonb THEN
        PERFORM public.pair_settlement(t, b, p);
        RETURN;
    END IF;
    IF me.operation = 'record_bank_activity' THEN
        IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id
                WHERE x.tenant_id = t AND x.payer_book_id = b AND x.payer_account_id::text = args ->> 'other_account_id' AND x.active AND l.active) THEN
            RAISE EXCEPTION 'The pair can''t be prepared: other_account_id is no linked Book''s account in this Book, or that link is turned off.' USING ERRCODE = 'VP020';
        END IF;
        RETURN;
    END IF;
    IF me.operation <> 'post_journal' THEN
        RAISE EXCEPTION 'The pair can''t be prepared: a pair is record_bank_activity in the Book that paid and post_journal in the Book it paid for.' USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id <> b AND q.operation = 'record_bank_activity'
            AND (args ->> 'payer_book_id' IS NULL OR q.book_id::text = args ->> 'payer_book_id')
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' -> 'settlement') IS DISTINCT FROM 'true'::jsonb
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: prepare the half in the Book that paid first; no waiting half has pair_key %.', key USING ERRCODE = 'VP020';
    END IF;
    terms := public.active_link_terms(t, other.book_id, b);
    IF terms.link_id IS NULL THEN
        RAISE EXCEPTION 'The pair can''t be prepared: these Books aren''t linked for this direction, or the link is turned off.' USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = t AND x.payer_book_id = other.book_id AND x.payer_proposal_id = other.id) THEN
        RAISE EXCEPTION 'The pair can''t be prepared: its other half is already paired.' USING ERRCODE = 'VP020';
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF oargs ->> 'transaction_id' IS DISTINCT FROM args ->> 'paired_transaction_id' THEN
        RAISE EXCEPTION 'The pair can''t be prepared: paired_transaction_id isn''t the bank item its other half records.' USING ERRCODE = 'VP020';
    END IF;
    IF oargs ->> 'other_account_id' IS DISTINCT FROM terms.payer_account_id::text THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the other half doesn''t record to the account this link names in that Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT * INTO item FROM public.bank_transactions WHERE tenant_id = t AND book_id = other.book_id AND id = (oargs ->> 'transaction_id')::uuid;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the bank item isn''t in the Book that paid.' USING ERRCODE = 'VP020';
    END IF;
    -- Amounts follow the bank account's own balance: a card charge raises a
    -- liability (positive), money paid from an account lowers an asset
    -- (negative). A cost pair records money paid out.
    paid := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = other.book_id
        AND a.id = coalesce((SELECT x.account_id FROM public.bank_accounts x WHERE x.tenant_id = t AND x.book_id = other.book_id AND x.id = item.bank_account_id), item.account_id));
    IF NOT (paid = 'liability' AND item.amount_minor > 0 OR paid = 'asset' AND item.amount_minor < 0) THEN
        RAISE EXCEPTION 'The pair can''t be prepared: a cost pair records money paid out, such as a charge on a card.' USING ERRCODE = 'VP020';
    END IF;
    amount := abs(item.amount_minor);
    IF (SELECT k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = b) IS DISTINCT FROM item.currency THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the bank item is in another currency than this Book.' USING ERRCODE = 'VP020';
    END IF;
    IF args ->> 'accounting_date' IS DISTINCT FROM item.occurred_on::text THEN
        RAISE EXCEPTION 'The pair can''t be prepared: this entry''s date isn''t the bank item''s date (%).', item.occurred_on USING ERRCODE = 'VP020';
    END IF;
    FOR line IN SELECT * FROM jsonb_array_elements(coalesce(args -> 'lines', '[]'::jsonb)) LOOP
        SELECT * INTO acc FROM public.accounts WHERE tenant_id = t AND book_id = b AND id = (line ->> 'account_id')::uuid;
        IF line ->> 'side' = 'credit' THEN
            credits := credits + 1;
            IF acc.id IS DISTINCT FROM terms.beneficiary_account_id OR (line ->> 'amount_minor')::bigint <> amount THEN
                RAISE EXCEPTION 'The pair can''t be prepared: this entry credits the account the link names here (%) with the whole amount.', terms.beneficiary_account_id USING ERRCODE = 'VP020';
            END IF;
        ELSE
            IF NOT (acc.class = 'expense' OR acc.class = 'asset' AND coalesce(acc.role, '') NOT IN ('cash', 'card')
                    AND NOT EXISTS (SELECT 1 FROM public.bank_accounts x WHERE x.tenant_id = t AND x.book_id = b AND x.account_id = acc.id)) THEN
                RAISE EXCEPTION 'The pair can''t be prepared: this entry debits only expenses, or assets that aren''t cash, a card or a bank account.' USING ERRCODE = 'VP020';
            END IF;
            debits := debits + (line ->> 'amount_minor')::bigint;
        END IF;
    END LOOP;
    IF credits <> 1 OR debits <> amount THEN
        RAISE EXCEPTION 'The pair can''t be prepared: this entry is for the bank item''s amount, with one credit to the link''s account.' USING ERRCODE = 'VP020';
    END IF;
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id)
        VALUES (t, key, other.book_id, other.id, b, p, item.id);
END $$;

-- The pair a proposal belongs to, for reads in either Book: the partner's
-- Book, proposal and side, and whether its link is still on. A payer half
-- not yet joined names the Book its account's link pays for.
CREATE FUNCTION public.proposal_pair_of(t uuid, b uuid, p uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE coalesce((
    SELECT jsonb_build_object('state', 'paired', 'pair_key', x.pair_key, 'transaction_id', x.transaction_id, 'kind', x.kind,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'proposal_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_proposal_id ELSE x.payer_proposal_id END,
        'side', CASE WHEN x.payer_book_id = b THEN 'beneficiary' ELSE 'payer' END,
        -- A settlement's debtor is the owed direction's beneficiary.
        'link_active', CASE WHEN x.kind = 'settlement' THEN (public.active_link_terms(t, x.beneficiary_book_id, x.payer_book_id)).link_id
            ELSE (public.active_link_terms(t, x.payer_book_id, x.beneficiary_book_id)).link_id END IS NOT NULL)
    FROM public.proposal_pairs x WHERE x.tenant_id = t
        AND (x.payer_book_id = b AND x.payer_proposal_id = p OR x.beneficiary_book_id = b AND x.beneficiary_proposal_id = p)), (
    SELECT jsonb_build_object('state', 'waiting_for_pair', 'pair_key', args ->> 'pair_key', 'side', 'beneficiary',
        'book_id', x.beneficiary_book_id,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = x.beneficiary_book_id),
        'link_active', (public.active_link_terms(t, b, x.beneficiary_book_id)).link_id IS NOT NULL)
    FROM public.operation_proposals q
        CROSS JOIN LATERAL (SELECT convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' AS args) a
        JOIN public.book_link_terms x ON x.tenant_id = q.tenant_id AND x.payer_book_id = q.book_id AND x.payer_account_id::text = a.args ->> 'other_account_id'
    WHERE q.tenant_id = t AND q.book_id = b AND q.id = p AND q.operation = 'record_bank_activity' AND a.args ->> 'pair_key' IS NOT NULL
        AND (a.args -> 'settlement') IS DISTINCT FROM 'true'::jsonb
    LIMIT 1), (
    -- A settlement half waiting for the other Book's: the side is the
    -- partner's, the Book that pays (owes) or the Book paid.
    SELECT jsonb_build_object('state', 'waiting_for_pair', 'pair_key', a.args ->> 'pair_key', 'kind', 'settlement',
        'side', CASE WHEN x.payer_book_id = b THEN 'payer' ELSE 'beneficiary' END,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'link_active', (public.active_link_terms(t, x.payer_book_id, x.beneficiary_book_id)).link_id IS NOT NULL)
    FROM public.operation_proposals q
        CROSS JOIN LATERAL (SELECT convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' AS args) a
        JOIN public.book_link_terms x ON x.tenant_id = q.tenant_id AND x.treatment = 'owed'
            AND (x.payer_book_id = q.book_id AND x.payer_account_id::text = a.args ->> 'other_account_id'
                OR x.beneficiary_book_id = q.book_id AND x.beneficiary_account_id::text = a.args ->> 'other_account_id')
    WHERE q.tenant_id = t AND q.book_id = b AND q.id = p AND q.operation = 'record_bank_activity' AND a.args ->> 'pair_key' IS NOT NULL
        AND (a.args -> 'settlement') = 'true'::jsonb
    LIMIT 1)) END
$$;

-- The cost pairs a settlement repays. A settlement is live until its
-- debtor's entry is reversed; while it is, the costs it names are repaid.
CREATE TABLE public.settlement_repays (
    tenant_id uuid NOT NULL,
    settlement_book_id uuid NOT NULL,
    settlement_entry_id uuid NOT NULL,
    cost_book_id uuid NOT NULL,
    cost_entry_id uuid NOT NULL,
    PRIMARY KEY (tenant_id, cost_book_id, cost_entry_id, settlement_entry_id),
    FOREIGN KEY (tenant_id, settlement_book_id, settlement_entry_id) REFERENCES public.entry_pairs(tenant_id, payer_book_id, payer_entry_id),
    FOREIGN KEY (tenant_id, cost_book_id, cost_entry_id) REFERENCES public.entry_pairs(tenant_id, beneficiary_book_id, beneficiary_entry_id)
);

-- Whether entry e of Book b has been reversed.
CREATE FUNCTION public.entry_reversed(t uuid, b uuid, e uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT EXISTS (SELECT 1 FROM public.entries r WHERE r.tenant_id = t AND r.book_id = b AND r.reversal_of = e)
$$;

-- The live settlement repaying a cost pair, named by the cost's entry in
-- the Book it was paid for; other_than leaves one settlement out.
CREATE FUNCTION public.cost_repaid_by(t uuid, b uuid, e uuid, other_than uuid) RETURNS text
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT p.pair_key FROM public.settlement_repays r
    JOIN public.entry_pairs p ON p.tenant_id = r.tenant_id AND p.payer_book_id = r.settlement_book_id AND p.payer_entry_id = r.settlement_entry_id
WHERE r.tenant_id = t AND r.cost_book_id = b AND r.cost_entry_id = e AND r.settlement_entry_id IS DISTINCT FROM other_than
    AND NOT public.entry_reversed(t, r.settlement_book_id, r.settlement_entry_id)
LIMIT 1
$$;

-- An amount in a Book's own units, for refusals.
CREATE FUNCTION public.book_amount(t uuid, b uuid, minor bigint) RETURNS text
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT round(minor::numeric / power(10::numeric, k.scale), k.scale::int)::text || ' ' || k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = b
$$;

-- Checks a settlement: the debtor's bank item paying out and the
-- creditor's taking in the same amount, at most five days apart, between
-- the two accounts an owed direction names, for no more than is owed. The
-- costs it names (repays) must be open cost pairs from the creditor to the
-- debtor adding up to exactly the amount; a business repaying its owner's
-- personal Book names them always. leaving_out is the settlement's own
-- entry once posted. Returns the cost pairs' entries in the debtor's Book.
CREATE FUNCTION public.settlement_check(t uuid, debtor uuid, debtor_account uuid, debtor_item uuid, creditor uuid, creditor_account uuid, creditor_item uuid,
        repays jsonb, leaving_out uuid) RETURNS uuid[]
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.bank_transactions%ROWTYPE; c public.bank_transactions%ROWTYPE; dclass text; cclass text; owed bigint; total bigint := 0;
    k text; entry uuid; amount bigint; costs uuid[] := '{}';
BEGIN
    SELECT * INTO d FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = debtor AND x.id = debtor_item;
    SELECT * INTO c FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = creditor AND x.id = creditor_item;
    IF d.id IS NULL OR c.id IS NULL THEN
        RAISE EXCEPTION 'A settlement is two bank items, one in each linked Book.' USING ERRCODE = 'VP020';
    END IF;
    dclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = debtor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = debtor AND y.id = d.bank_account_id), d.account_id));
    cclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = creditor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = creditor AND y.id = c.bank_account_id), c.account_id));
    IF NOT (dclass = 'asset' AND d.amount_minor < 0 OR dclass = 'liability' AND d.amount_minor > 0)
        OR NOT (cclass = 'asset' AND c.amount_minor > 0 OR cclass = 'liability' AND c.amount_minor < 0) THEN
        RAISE EXCEPTION 'A settlement is money paid out of the Book that owes and into the Book that is owed.' USING ERRCODE = 'VP020';
    END IF;
    amount := abs(d.amount_minor);
    IF abs(c.amount_minor) <> amount OR c.currency IS DISTINCT FROM d.currency OR abs(c.occurred_on - d.occurred_on) > 5 THEN
        RAISE EXCEPTION 'A settlement''s two bank items are the same amount, at most five days apart.' USING ERRCODE = 'VP020';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed' AND x.payer_book_id = creditor AND x.payer_account_id = creditor_account
                AND x.beneficiary_book_id = debtor AND x.beneficiary_account_id = debtor_account) THEN
        RAISE EXCEPTION 'A settlement pays what one Book owes the other: to the accounts an owed direction of an active link names.' USING ERRCODE = 'VP020';
    END IF;
    SELECT coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO owed
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.account_id = debtor_account AND x.entry_id IS DISTINCT FROM leaving_out;
    IF amount > owed THEN
        RAISE EXCEPTION 'This pays more than is owed (%, of which % is owed).', public.book_amount(t, debtor, amount), public.book_amount(t, debtor, greatest(owed, 0)) USING ERRCODE = 'VP020';
    END IF;
    IF jsonb_typeof(coalesce(repays, '[]'::jsonb)) <> 'array' THEN
        RAISE EXCEPTION 'repays is a list of pair keys.' USING ERRCODE = 'VP020';
    END IF;
    FOR k IN SELECT jsonb_array_elements_text(coalesce(repays, '[]'::jsonb)) LOOP
        SELECT p.beneficiary_entry_id INTO entry FROM public.entry_pairs p
            WHERE p.tenant_id = t AND p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor AND p.pair_key = k
                AND NOT public.entry_reversed(t, debtor, p.beneficiary_entry_id);
        IF entry IS NULL THEN
            RAISE EXCEPTION 'repays names %, which isn''t a recorded cost % paid for this Book.', k, (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = creditor) USING ERRCODE = 'VP020';
        END IF;
        IF entry = ANY (costs) THEN
            RAISE EXCEPTION 'repays names % twice.', k USING ERRCODE = 'VP020';
        END IF;
        IF public.cost_repaid_by(t, debtor, entry, leaving_out) IS NOT NULL THEN
            RAISE EXCEPTION 'The cost % is already repaid.', k USING ERRCODE = 'VP020';
        END IF;
        costs := costs || entry;
        total := total + (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = entry AND x.side = 'credit');
    END LOOP;
    IF cardinality(costs) = 0 AND public.book_profile(t, creditor) = 'personal' AND public.book_profile(t, debtor) = 'business' THEN
        RAISE EXCEPTION 'A business repaying its owner names the costs it repays (repays), adding up to the payment.' USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) > 0 AND total <> amount THEN
        RAISE EXCEPTION 'The costs named add up to %, not the payment''s %.', public.book_amount(t, debtor, total), public.book_amount(t, debtor, amount) USING ERRCODE = 'VP020';
    END IF;
    RETURN costs;
END $$;

-- Records a settlement the owner decided, or recorded at once: both
-- entries each record their own bank item to its term account, and the
-- costs it repays are kept.
CREATE FUNCTION public.settle_pair(t uuid, key text, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid, repays jsonb) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE ditem uuid; dbank uuid; dacc uuid; citem uuid; cbank uuid; cacc uuid; costs uuid[]; e uuid;
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    SELECT l.transaction_id, l.account_id INTO ditem, dbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = debtor AND l.entry_id = debtor_entry AND l.revoked_at IS NULL;
    SELECT l.transaction_id, l.account_id INTO citem, cbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = creditor AND l.entry_id = creditor_entry AND l.revoked_at IS NULL;
    SELECT min(x.account_id::text)::uuid INTO dacc FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry AND x.account_id <> dbank;
    SELECT min(x.account_id::text)::uuid INTO cacc FROM public.lines x WHERE x.tenant_id = t AND x.book_id = creditor AND x.entry_id = creditor_entry AND x.account_id <> cbank;
    IF ditem IS NULL OR citem IS NULL
        OR (SELECT count(*) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry) <> 2
        OR (SELECT count(*) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = creditor AND x.entry_id = creditor_entry) <> 2
        OR EXISTS (SELECT 1 FROM public.entries x JOIN public.bank_transactions i ON i.tenant_id = x.tenant_id AND i.book_id = x.book_id
                AND i.id = CASE WHEN x.book_id = debtor THEN ditem ELSE citem END
            WHERE x.tenant_id = t AND (x.book_id = debtor AND x.id = debtor_entry OR x.book_id = creditor AND x.id = creditor_entry)
                AND (x.accounting_date <> i.occurred_on OR x.reversal_of IS NOT NULL)) THEN
        RAISE EXCEPTION 'Each entry of a settlement records its own bank item, on its date, to one account.' USING ERRCODE = 'VP020';
    END IF;
    costs := public.settlement_check(t, debtor, dacc, ditem, creditor, cacc, citem, repays, debtor_entry);
    PERFORM set_config('vult.settling', key, true);
    INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id)
        VALUES (t, key, 'settlement', debtor, debtor_entry, creditor, creditor_entry);
    PERFORM set_config('vult.settling', '', true);
    FOREACH e IN ARRAY costs LOOP
        INSERT INTO public.settlement_repays(tenant_id, settlement_book_id, settlement_entry_id, cost_book_id, cost_entry_id) VALUES (t, debtor, debtor_entry, debtor, e);
    END LOOP;
END $$;

-- Joins the halves of a settlement prepared as proposals: the first waits,
-- the second, in the other Book, joins it once both are checked.
CREATE FUNCTION public.pair_settlement(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; args jsonb; oargs jsonb; key text;
    debtor uuid; creditor uuid; dp uuid; cp uuid; dargs jsonb; cargs jsonb;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed'
                AND (x.payer_book_id = b AND x.payer_account_id::text = args ->> 'other_account_id' OR x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id')) THEN
        RAISE EXCEPTION 'The settlement can''t be prepared: other_account_id is no account an owed direction of an active link names in this Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id <> b AND q.operation = 'record_bank_activity'
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' -> 'settlement') = 'true'::jsonb
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
            AND NOT EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = q.tenant_id
                AND (x.payer_book_id = q.book_id AND x.payer_proposal_id = q.id OR x.beneficiary_book_id = q.book_id AND x.beneficiary_proposal_id = q.id))
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF coalesce(args -> 'repays', '[]'::jsonb) <> coalesce(oargs -> 'repays', '[]'::jsonb) THEN
        RAISE EXCEPTION 'The settlement can''t be prepared: both halves name the same costs in repays.' USING ERRCODE = 'VP020';
    END IF;
    -- The Book whose account is the owed direction's payable owes.
    IF EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t AND x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id') THEN
        debtor := b; dp := p; dargs := args; creditor := other.book_id; cp := other.id; cargs := oargs;
    ELSE
        debtor := other.book_id; dp := other.id; dargs := oargs; creditor := b; cp := p; cargs := args;
    END IF;
    PERFORM public.settlement_check(t, debtor, (dargs ->> 'other_account_id')::uuid, (dargs ->> 'transaction_id')::uuid,
        creditor, (cargs ->> 'other_account_id')::uuid, (cargs ->> 'transaction_id')::uuid, args -> 'repays', NULL);
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, kind, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id)
        VALUES (t, key, 'settlement', debtor, dp, creditor, cp, (dargs ->> 'transaction_id')::uuid);
END $$;

-- Who owes whom: for each link with an owed direction, the net between the
-- two Books from the term accounts on both sides, whether both sides agree
-- (and the entries only one side has), and the cost pairs along it. Each
-- Book's other liabilities follow, cards and loans included. The owner
-- reads every link (b NULL); a connection of Book b reads b's links and b's
-- own liabilities.
CREATE FUNCTION public.linked_balances(t uuid, b uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
WITH allowed AS (
    SELECT CASE WHEN b IS NULL THEN access.owner_allowed(t) ELSE access.owner_allowed(t) OR public.ledger_allowed(t, b, false) END AS ok
), links AS (
    SELECT l.* FROM public.book_links l, allowed WHERE allowed.ok AND l.tenant_id = t AND (b IS NULL OR b IN (l.book_a, l.book_b))
), balance AS (
    -- Each line's effect, debit positive, on the accounts terms name.
    SELECT x.book_id, x.account_id, x.entry_id, CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END AS amount
    FROM public.lines x WHERE x.tenant_id = t
), dirs AS (
    -- Owed directions: the payer is owed (receivable), the beneficiary owes (payable).
    SELECT k.link_id, k.payer_book_id AS creditor, k.payer_account_id AS creditor_account, k.beneficiary_book_id AS debtor, k.beneficiary_account_id AS debtor_account,
        coalesce((SELECT sum(y.amount) FROM balance y WHERE y.book_id = k.payer_book_id AND y.account_id = k.payer_account_id), 0) AS creditor_says,
        coalesce((SELECT -sum(y.amount) FROM balance y WHERE y.book_id = k.beneficiary_book_id AND y.account_id = k.beneficiary_account_id), 0) AS debtor_says
    FROM public.book_link_terms k JOIN links l ON l.tenant_id = t AND l.id = k.link_id WHERE k.tenant_id = t AND k.treatment = 'owed'
), paired AS (
    SELECT p.payer_book_id AS book_id, p.payer_entry_id AS entry_id FROM public.entry_pairs p WHERE p.tenant_id = t
    UNION ALL SELECT p.beneficiary_book_id, p.beneficiary_entry_id FROM public.entry_pairs p WHERE p.tenant_id = t
), net AS (
    SELECT l.id AS link_id, l.book_a, l.book_b,
        coalesce(sum(CASE WHEN d.creditor = l.book_a THEN d.debtor_says ELSE -d.debtor_says END), 0) AS owed_to_a,
        bool_and(d.creditor_says = d.debtor_says) AS matches
    FROM links l JOIN dirs d ON d.link_id = l.id GROUP BY l.id, l.book_a, l.book_b
)
SELECT jsonb_build_object(
    'links', coalesce((SELECT jsonb_agg(jsonb_build_object(
        'link_id', n.link_id,
        'debtor_book_id', CASE WHEN n.owed_to_a >= 0 THEN n.book_b ELSE n.book_a END,
        'creditor_book_id', CASE WHEN n.owed_to_a >= 0 THEN n.book_a ELSE n.book_b END,
        'amount_minor', abs(n.owed_to_a)::text,
        'currency', (SELECT k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = n.book_a),
        'matches', n.matches,
        'mismatched_entry_ids', coalesce((SELECT jsonb_agg(DISTINCT y.entry_id) FROM dirs d JOIN balance y
                ON (y.book_id = d.creditor AND y.account_id = d.creditor_account OR y.book_id = d.debtor AND y.account_id = d.debtor_account)
            WHERE d.link_id = n.link_id
                AND NOT EXISTS (SELECT 1 FROM paired q WHERE q.book_id = y.book_id AND q.entry_id = y.entry_id)
                AND NOT EXISTS (SELECT 1 FROM public.entries e JOIN paired q ON q.book_id = e.book_id AND q.entry_id = e.reversal_of
                    WHERE e.tenant_id = t AND e.book_id = y.book_id AND e.id = y.entry_id)), '[]'::jsonb),
        'open_pairs', coalesce((SELECT jsonb_agg(jsonb_build_object(
                'pair_key', p.pair_key, 'payer_book_id', p.payer_book_id, 'beneficiary_book_id', p.beneficiary_book_id,
                'date', e.accounting_date, 'description', e.description,
                'amount_minor', (SELECT sum(z.amount_minor) FROM public.lines z WHERE z.tenant_id = t AND z.book_id = p.beneficiary_book_id AND z.entry_id = p.beneficiary_entry_id AND z.side = 'credit')::text,
                'repaid', public.cost_repaid_by(t, p.beneficiary_book_id, p.beneficiary_entry_id, NULL) IS NOT NULL,
                'has_receipt', EXISTS (SELECT 1 FROM public.evidence_links v WHERE v.tenant_id = t
                        AND (v.book_id = p.beneficiary_book_id AND v.entry_id = p.beneficiary_entry_id OR v.book_id = p.payer_book_id AND v.entry_id = p.payer_entry_id)
                        AND v.action = 'attach' AND v.role IN ('receipt', 'invoice')
                        AND NOT EXISTS (SELECT 1 FROM public.evidence_links w WHERE w.tenant_id = v.tenant_id AND w.book_id = v.book_id AND w.link_id = v.link_id AND w.revision > v.revision))
                    OR EXISTS (SELECT 1 FROM public.source_citations c WHERE c.tenant_id = t
                        AND (c.book_id = p.beneficiary_book_id AND c.entry_id = p.beneficiary_entry_id OR c.book_id = p.payer_book_id AND c.entry_id = p.payer_entry_id)))
                ORDER BY e.accounting_date, p.pair_key)
            FROM public.entry_pairs p JOIN dirs d ON d.link_id = n.link_id AND d.creditor = p.payer_book_id AND d.debtor = p.beneficiary_book_id
                JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
            WHERE p.tenant_id = t AND p.kind = 'cost' AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)), '[]'::jsonb))
        ORDER BY n.link_id) FROM net n), '[]'::jsonb),
    'books', coalesce((SELECT jsonb_agg(jsonb_build_object('book_id', k.id, 'name', k.name,
        'liabilities', coalesce((SELECT jsonb_agg(jsonb_build_object('account_id', a.id, 'name', a.name, 'role', coalesce(a.role, ''),
                'balance_minor', coalesce((SELECT -sum(y.amount) FROM balance y WHERE y.book_id = a.book_id AND y.account_id = a.id), 0)::text) ORDER BY a.code, a.id)
            FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = k.id AND a.class = 'liability'
                AND NOT EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t
                    AND (x.payer_book_id = k.id AND x.payer_account_id = a.id OR x.beneficiary_book_id = k.id AND x.beneficiary_account_id = a.id))), '[]'::jsonb))
        ORDER BY k.name, k.id)
        FROM public.books k, allowed WHERE allowed.ok AND k.tenant_id = t AND (b IS NULL AND EXISTS (SELECT 1 FROM links l WHERE k.id IN (l.book_a, l.book_b)) OR k.id = b)), '[]'::jsonb))
$$;

-- The owner answers and closes notes agents wrote: only a new note, or a
-- profile change, must carry the writer's own actor.
CREATE OR REPLACE FUNCTION public.agent_note_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE owner boolean; open_loops integer;
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'notes are closed or replaced, never deleted' USING ERRCODE = '23514';
    END IF;
    -- Closing a loop because its item was recorded or its question settled
    -- happens with whoever did that; it changes nothing else.
    IF TG_TABLE_NAME = 'agent_loops' AND TG_OP = 'UPDATE' AND coalesce(current_setting('vult.loop_autoclose', true), '') = 'on' THEN
        IF OLD.status = 'open' AND NEW.status = 'done' AND NEW.closed_reason IN ('The item was recorded.', 'The question was answered.', 'The question was withdrawn.')
            AND (to_jsonb(NEW) - ARRAY['status', 'closed_at', 'closed_reason']) = (to_jsonb(OLD) - ARRAY['status', 'closed_at', 'closed_reason']) THEN
            RETURN NEW;
        END IF;
    END IF;
    owner := public.agent_note_owner(NEW.tenant_id, NEW.book_id);
    IF TG_TABLE_NAME = 'agent_seen' THEN
        -- Any signed-in visit counts; no fresh sign-in is needed to look.
        IF NOT (public.ledger_context(NEW.tenant_id, NEW.book_id) AND coalesce(current_setting('vult.connection_id', true), '') = ''
                AND access.owner_allowed(NEW.tenant_id)) OR NEW.person::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'a person records only when they last looked' USING ERRCODE = '42501';
        END IF;
        NEW.seen_at := clock_timestamp();
        RETURN NEW;
    END IF;
    IF NOT public.agent_note_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the Book''s agents and its owner keep these notes' USING ERRCODE = '42501';
    END IF;
    -- A note's actor is whoever wrote it; the owner answers, withdraws or
    -- closes notes agents wrote, and a profile names its last editor.
    IF (TG_OP = 'INSERT' OR TG_TABLE_NAME = 'agent_profile') AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF TG_TABLE_NAME = 'agent_profile' THEN
        IF TG_OP = 'UPDATE' AND NEW.revision <> OLD.revision + 1 OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'the profile changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' THEN
        NEW.connection_id := nullif(current_setting('vult.connection_id', true), '')::uuid;
        IF TG_TABLE_NAME = 'agent_questions' THEN
            IF NEW.status <> 'open' AND NOT owner OR NEW.answer IS NOT NULL AND NOT owner THEN
                RAISE EXCEPTION 'only the owner answers a question' USING ERRCODE = '42501';
            END IF;
            NEW.asked_at := clock_timestamp();
        ELSE
            IF NEW.status <> 'open' THEN
                RAISE EXCEPTION 'a loop opens open' USING ERRCODE = '23514';
            END IF;
            SELECT count(*) INTO open_loops FROM public.agent_loops WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND status = 'open';
            IF open_loops >= 50 THEN
                RAISE EXCEPTION 'this Book has 50 open loops; close some first' USING ERRCODE = '23514';
            END IF;
            NEW.created_at := clock_timestamp();
        END IF;
        RETURN NEW;
    END IF;
    -- Updates: a question is answered by the owner, withdrawn by its asker
    -- or the owner, or superseded by the owner; a loop is closed once.
    IF TG_TABLE_NAME = 'agent_questions' THEN
        IF (to_jsonb(NEW) - ARRAY['answer', 'status', 'answered_at', 'answered_by', 'reason']) IS DISTINCT FROM
            (to_jsonb(OLD) - ARRAY['answer', 'status', 'answered_at', 'answered_by', 'reason'])
            OR NOT (OLD.status = 'open' AND NEW.status = 'answered' AND owner
                OR OLD.status = 'open' AND NEW.status = 'withdrawn' AND (owner OR OLD.connection_id::text = current_setting('vult.connection_id', true))
                OR OLD.status = 'answered' AND NEW.status = 'superseded' AND owner) THEN
            RAISE EXCEPTION 'a question is answered or replaced by the owner, or withdrawn by whoever asked it' USING ERRCODE = '42501';
        END IF;
        IF NEW.status = 'answered' THEN
            NEW.answered_at := clock_timestamp();
            NEW.answered_by := current_setting('vult.actor_id', true)::uuid;
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status <> 'open' OR NEW.status = 'open'
        OR (to_jsonb(NEW) - ARRAY['status', 'closed_at', 'closed_reason']) IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['status', 'closed_at', 'closed_reason']) THEN
        RAISE EXCEPTION 'a loop is closed once, with a reason' USING ERRCODE = '23514';
    END IF;
    NEW.closed_at := clock_timestamp();
    RETURN NEW;
END $$;

-- Questions about which Book a charge belongs to, and which costs a
-- payment repays, offer the owner choices; the answer is one of them.
ALTER TABLE public.agent_questions DROP CONSTRAINT agent_questions_subject_kind_check;
ALTER TABLE public.agent_questions ADD CONSTRAINT agent_questions_subject_kind_check
    CHECK (subject_kind IN ('bank_item', 'kind', 'goal', 'preference', 'account', 'other', 'entity', 'settlement'));
ALTER TABLE public.agent_questions ADD COLUMN choices jsonb CHECK (choices IS NULL OR jsonb_typeof(choices) = 'array'
    AND jsonb_array_length(choices) BETWEEN 2 AND 8 AND octet_length(choices::text) <= 4096);

CREATE FUNCTION public.agent_question_choices() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.subject_kind IN ('entity', 'settlement') AND NEW.choices IS NULL THEN
        RAISE EXCEPTION 'A question about an entity or a settlement offers choices.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.choices IS NOT NULL AND EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.choices) c
            WHERE jsonb_typeof(c) <> 'object' OR jsonb_typeof(c -> 'value') <> 'string' OR length(c ->> 'value') NOT BETWEEN 1 AND 200
                OR jsonb_typeof(c -> 'label') <> 'string' OR length(c ->> 'label') NOT BETWEEN 1 AND 200
                OR (c - 'value' - 'label') <> '{}'::jsonb)
        OR NEW.choices IS NOT NULL AND (SELECT count(DISTINCT c ->> 'value') FROM jsonb_array_elements(NEW.choices) c) <> jsonb_array_length(NEW.choices) THEN
        RAISE EXCEPTION 'Choices are {value, label} pairs with different values.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.choices IS NOT NULL AND NEW.answer IS NOT NULL AND (jsonb_typeof(NEW.answer) <> 'object' OR (NEW.answer - 'value' - 'make_rule') <> '{}'::jsonb
            OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.choices) c WHERE c ->> 'value' = NEW.answer ->> 'value')
            OR NEW.answer ? 'make_rule' AND jsonb_typeof(NEW.answer -> 'make_rule') <> 'boolean') THEN
        RAISE EXCEPTION 'The answer is one of the question''s choices.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER agent_question_choices BEFORE INSERT OR UPDATE ON public.agent_questions
    FOR EACH ROW EXECUTE FUNCTION public.agent_question_choices();

-- Where the owner said charges go: one bank item, or every charge from a
-- counterparty (on one bank account, or any). The target is a linked Book
-- or personal (this Book itself).
CREATE TABLE public.assignment_rules (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    transaction_id uuid,
    bank_account_id uuid,
    counterparty_key text CHECK (counterparty_key IS NULL OR length(counterparty_key) BETWEEN 1 AND 200),
    target text NOT NULL CHECK (target = 'personal' OR target ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
    source text NOT NULL CHECK (source IN ('owner', 'answer', 'reassigned')),
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    CHECK ((transaction_id IS NULL) <> (counterparty_key IS NULL)),
    CHECK (transaction_id IS NULL OR bank_account_id IS NULL),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);
CREATE UNIQUE INDEX assignment_rules_item ON public.assignment_rules(tenant_id, book_id, transaction_id) WHERE transaction_id IS NOT NULL;
CREATE UNIQUE INDEX assignment_rules_party ON public.assignment_rules(tenant_id, book_id, coalesce(bank_account_id, '00000000-0000-0000-0000-000000000000'::uuid), counterparty_key)
    WHERE counterparty_key IS NOT NULL;

CREATE FUNCTION public.assignment_rule_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END) THEN
        RAISE EXCEPTION 'only the owner makes assignment rules' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'an assignment rule is replaced, not changed' USING ERRCODE = '23514';
    END IF;
    IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF NEW.target <> 'personal' AND NOT EXISTS (SELECT 1 FROM public.book_links l WHERE l.tenant_id = NEW.tenant_id
            AND (l.book_a = NEW.book_id AND l.book_b = NEW.target::uuid OR l.book_b = NEW.book_id AND l.book_a = NEW.target::uuid)) THEN
        RAISE EXCEPTION 'Charges go to this Book or to a Book linked to it.' USING ERRCODE = 'VP020';
    END IF;
    -- The counterparty is kept as the key Vult forms from it, as kind_key gives it.
    IF NEW.counterparty_key IS NOT NULL THEN
        NEW.counterparty_key := left(public.rule_key(NEW.counterparty_key), 200);
        IF NEW.counterparty_key = '' THEN
            RAISE EXCEPTION 'A rule for a counterparty names one.' USING ERRCODE = 'VP020';
        END IF;
    END IF;
    NEW.created_at := clock_timestamp();
    RETURN NEW;
END $$;
CREATE TRIGGER assignment_rule_guard BEFORE INSERT OR UPDATE OR DELETE ON public.assignment_rules
    FOR EACH ROW EXECUTE FUNCTION public.assignment_rule_guard();

-- A rejection may say which Book the cost belongs to instead.
ALTER TABLE public.operation_decisions ADD COLUMN reassigned_to text
    CHECK (reassigned_to IS NULL OR decision = 'reject' AND (reassigned_to = 'personal'
        OR reassigned_to ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'));

-- A cost pair is checked again when its two entries are recorded together,
-- however they were made: the owner's approval of two proposals, or the
-- owner recording both at once. The payer's entry records its bank item to
-- the term account; the beneficiary's credits its term account with the
-- whole amount and debits only expenses or assets that aren't money.
CREATE FUNCTION public.entry_pair_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE terms public.book_link_terms%ROWTYPE; item public.bank_transactions%ROWTYPE; bank_account uuid; tid uuid;
    paid text; amount bigint; n int; bad int;
BEGIN
    IF NOT (access.owner_allowed(NEW.tenant_id) AND access.owner_write_allowed(NEW.tenant_id)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    IF NEW.kind = 'settlement' THEN
        -- Settlements are checked and recorded by settle_pair only.
        IF current_setting('vult.settling', true) IS DISTINCT FROM NEW.pair_key THEN
            RAISE EXCEPTION 'a settlement is recorded with settle_pair' USING ERRCODE = '42501';
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
CREATE TRIGGER entry_pair_guard BEFORE INSERT ON public.entry_pairs
    FOR EACH ROW EXECUTE FUNCTION public.entry_pair_guard();

-- For a bank item paid out of Book b, the linked Books b pays for and the
-- account each used before for the same counterparty: in its own bank
-- activity, or in an earlier pair paid from b. Read with the caller's own
-- rights, so a connection sees only Books it can read.
CREATE FUNCTION public.suggested_beneficiaries(t uuid, b uuid, i uuid) RETURNS jsonb
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
WITH item AS (
    SELECT x.*, left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200) AS party,
        (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = b
            AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = b AND y.id = x.bank_account_id), x.account_id)) AS paid
    FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = b AND x.id = i
), used AS (
    -- The beneficiary's own items from the same counterparty, recorded with two lines.
    SELECT o.book_id, o2.account_id, x.occurred_on AS seen
    FROM item, public.book_link_terms k JOIN public.book_links lk ON lk.tenant_id = k.tenant_id AND lk.id = k.link_id AND lk.active
        JOIN public.bank_transactions x ON x.tenant_id = k.tenant_id AND x.book_id = k.beneficiary_book_id
        JOIN public.bank_transaction_links o ON o.tenant_id = x.tenant_id AND o.book_id = x.book_id AND o.transaction_id = x.id AND o.revoked_at IS NULL
        JOIN public.lines o2 ON o2.tenant_id = o.tenant_id AND o2.book_id = o.book_id AND o2.entry_id = o.entry_id AND o2.account_id <> o.account_id
    WHERE k.tenant_id = t AND k.payer_book_id = b AND k.active
        AND left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200) = item.party
        AND (SELECT count(*) FROM public.lines z WHERE z.tenant_id = o.tenant_id AND z.book_id = o.book_id AND z.entry_id = o.entry_id) = 2
    UNION ALL
    -- Earlier pairs paid from this Book for the same counterparty.
    SELECT p.beneficiary_book_id, d.account_id, x.occurred_on
    FROM item, public.entry_pairs p
        JOIN public.bank_transaction_links o ON o.tenant_id = p.tenant_id AND o.book_id = p.payer_book_id AND o.entry_id = p.payer_entry_id AND o.revoked_at IS NULL
        JOIN public.bank_transactions x ON x.tenant_id = o.tenant_id AND x.book_id = o.book_id AND x.id = o.transaction_id
        JOIN public.lines d ON d.tenant_id = p.tenant_id AND d.book_id = p.beneficiary_book_id AND d.entry_id = p.beneficiary_entry_id AND d.side = 'debit'
    WHERE p.tenant_id = t AND p.payer_book_id = b AND p.kind = 'cost'
        AND (public.active_link_terms(t, b, p.beneficiary_book_id)).link_id IS NOT NULL
        AND left(public.rule_key(coalesce(nullif(x.counterparty, ''), x.description)), 200) = item.party
        AND NOT EXISTS (SELECT 1 FROM public.entries r WHERE r.tenant_id = p.tenant_id AND r.book_id = p.beneficiary_book_id AND r.reversal_of = p.beneficiary_entry_id)
), best AS (
    SELECT DISTINCT ON (u.book_id) u.book_id, u.account_id, u.seen FROM used u ORDER BY u.book_id, u.seen DESC, u.account_id
)
SELECT coalesce(jsonb_agg(jsonb_build_object('book_id', s.book_id, 'account_id', s.account_id,
        'why', k.name || ' recorded ' || coalesce(nullif((SELECT x.counterparty FROM item x), ''), (SELECT x.description FROM item x)) || ' to ' || a.name || ' on ' || s.seen)
        ORDER BY s.seen DESC, s.book_id), '[]'::jsonb)
FROM best s JOIN public.books k ON k.tenant_id = t AND k.id = s.book_id
    JOIN public.accounts a ON a.tenant_id = t AND a.book_id = s.book_id AND a.id = s.account_id
WHERE (SELECT paid = 'liability' AND amount_minor > 0 OR paid = 'asset' AND amount_minor < 0 FROM item)
    AND (a.class = 'expense' OR a.class = 'asset' AND coalesce(a.role, '') NOT IN ('cash', 'card'))
$$;

-- The other entry of a decided pair, for the owner or a connection of
-- Book b: its Book, entry and side.
CREATE FUNCTION public.entry_pair_of(t uuid, b uuid, e uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE (
    SELECT jsonb_build_object('pair_key', x.pair_key, 'kind', x.kind,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'entry_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_entry_id ELSE x.payer_entry_id END,
        'side', CASE WHEN x.payer_book_id = b THEN 'beneficiary' ELSE 'payer' END,
        'repaid_by', CASE WHEN x.kind = 'cost' THEN public.cost_repaid_by(t, x.beneficiary_book_id, x.beneficiary_entry_id, NULL) END)
    FROM public.entry_pairs x WHERE x.tenant_id = t
        AND (x.payer_book_id = b AND x.payer_entry_id = e OR x.beneficiary_book_id = b AND x.beneficiary_entry_id = e)) END
$$;

-- Undoing half of a pair reverses its partner too. That partner's reversal
-- follows the owner's decision on the other half; it isn't a correction of
-- the agent's kind of activity.
CREATE OR REPLACE FUNCTION public.agent_kind_undone() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE l record;
BEGIN
    IF coalesce(current_setting('vult.pair_partner', true), '') = 'on' THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = NEW.actor_id) THEN
        RETURN NULL;
    END IF;
    IF TG_TABLE_NAME = 'entries' THEN
        FOR l IN SELECT DISTINCT x.transaction_id FROM public.bank_transaction_links x
                WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.book_id AND x.entry_id = NEW.reversal_of
                AND EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = x.actor_id) LOOP
            PERFORM public.agent_kind_note(NEW.tenant_id, NEW.book_id, l.transaction_id, 'undone',
                public.agent_other_account(NEW.tenant_id, NEW.book_id, l.transaction_id, '{}'::jsonb, NEW.reversal_of));
        END LOOP;
    END IF;
    RETURN NULL;
END $$;

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['book_links', 'book_link_terms', 'proposal_pairs', 'entry_pairs', 'settlement_repays', 'assignment_rules'] LOOP
        EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', tab);
    END LOOP;
END $$;
CREATE POLICY link_read ON public.book_links FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_a, false) OR public.ledger_allowed(tenant_id, book_b, false));
CREATE POLICY link_read ON public.book_link_terms FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, payer_book_id, false) OR public.ledger_allowed(tenant_id, beneficiary_book_id, false));
CREATE POLICY pair_read ON public.proposal_pairs FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, payer_book_id, false) OR public.ledger_allowed(tenant_id, beneficiary_book_id, false));
CREATE POLICY rule_read ON public.assignment_rules FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY owner_write ON public.assignment_rules FOR INSERT WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_delete ON public.assignment_rules FOR DELETE USING (public.book_link_owner(tenant_id));
CREATE POLICY repays_read ON public.settlement_repays FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, settlement_book_id, false));
CREATE POLICY pair_read ON public.entry_pairs FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, payer_book_id, false) OR public.ledger_allowed(tenant_id, beneficiary_book_id, false));
CREATE POLICY owner_write ON public.book_links FOR INSERT WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_update ON public.book_links FOR UPDATE USING (public.book_link_owner(tenant_id)) WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_delete ON public.book_links FOR DELETE USING (public.book_link_owner(tenant_id));
CREATE POLICY owner_write ON public.book_link_terms FOR INSERT WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_update ON public.book_link_terms FOR UPDATE USING (public.book_link_owner(tenant_id)) WITH CHECK (public.book_link_owner(tenant_id));
CREATE POLICY owner_delete ON public.book_link_terms FOR DELETE USING (public.book_link_owner(tenant_id));
-- The owner's decision, or the owner recording both at once, posts both
-- entries of a pair and records them here.
CREATE POLICY owner_pairs ON public.entry_pairs FOR INSERT WITH CHECK (access.owner_allowed(tenant_id) AND access.owner_write_allowed(tenant_id));

REVOKE ALL ON FUNCTION public.book_link_owner(uuid), public.book_profile(uuid, uuid), public.book_link_guard(), public.book_link_terms_guard(),
    public.book_links_for(uuid, uuid), public.active_link_terms(uuid, uuid, uuid), public.pair_proposal(uuid, uuid, uuid),
    public.proposal_pair_of(uuid, uuid, uuid), public.entry_pair_of(uuid, uuid, uuid), public.entry_pair_guard(),
    public.suggested_beneficiaries(uuid, uuid, uuid), public.entry_reversed(uuid, uuid, uuid), public.cost_repaid_by(uuid, uuid, uuid, uuid),
    public.book_amount(uuid, uuid, bigint), public.settlement_check(uuid, uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid),
    public.settle_pair(uuid, text, uuid, uuid, uuid, uuid, jsonb), public.pair_settlement(uuid, uuid, uuid), public.linked_balances(uuid, uuid),
    public.agent_question_choices(), public.assignment_rule_guard() FROM PUBLIC;


-- Carry the earlier links and pairs over, as the owner recorded them. The
-- checks on new rows were met when they were first made.
ALTER TABLE public.book_links DISABLE TRIGGER USER;
ALTER TABLE public.book_link_terms DISABLE TRIGGER USER;
ALTER TABLE public.entry_pairs DISABLE TRIGGER USER;
ALTER TABLE public.book_links NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.book_link_terms NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.proposal_pairs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.entry_pairs NO FORCE ROW LEVEL SECURITY;
CREATE TEMP TABLE carried_links AS SELECT gen_random_uuid() AS id, x.* FROM earlier_links x;
INSERT INTO public.book_links(tenant_id, id, book_a, book_b, active, revision, actor_id, created_at, updated_at)
    SELECT tenant_id, id, least(business_book_id, personal_book_id), greatest(business_book_id, personal_book_id), active, 1, actor_id, created_at, updated_at
    FROM carried_links;
INSERT INTO public.book_link_terms(tenant_id, link_id, payer_book_id, beneficiary_book_id, treatment, payer_account_id, beneficiary_account_id, active, actor_id, updated_at)
    SELECT tenant_id, id, personal_book_id, business_book_id, mode, personal_account_id, business_account_id, true, actor_id, updated_at
    FROM carried_links;
INSERT INTO public.proposal_pairs(tenant_id, pair_key, kind, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id, created_at)
    SELECT tenant_id, pair_key, 'cost', personal_book_id, personal_proposal_id, business_book_id, business_proposal_id, transaction_id, created_at
    FROM earlier_proposal_pairs;
INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id, created_at)
    SELECT tenant_id, pair_key, 'cost', personal_book_id, personal_entry_id, business_book_id, business_entry_id, created_at
    FROM earlier_entry_pairs;
ALTER TABLE public.book_links ENABLE TRIGGER USER;
ALTER TABLE public.book_link_terms ENABLE TRIGGER USER;
ALTER TABLE public.entry_pairs ENABLE TRIGGER USER;
ALTER TABLE public.book_links FORCE ROW LEVEL SECURITY;
ALTER TABLE public.book_link_terms FORCE ROW LEVEL SECURITY;
ALTER TABLE public.proposal_pairs FORCE ROW LEVEL SECURITY;
ALTER TABLE public.entry_pairs FORCE ROW LEVEL SECURITY;
DROP TABLE earlier_links, earlier_proposal_pairs, earlier_entry_pairs, carried_links;

-- ------------------------------------------------------------------------
-- 0014 vult_keys_and_document_groups
-- ------------------------------------------------------------------------

-- The owner's bank provider keys, once for the whole Vult: every Book in it
-- connects banks with them. A Book's own keys, saved before, are still read
-- when the Vult has none.
CREATE TABLE public.vult_bank_provider_settings (
    tenant_id uuid NOT NULL REFERENCES public.tenants(id),
    provider text NOT NULL CHECK (provider IN ('plaid')),
    environment text NOT NULL CHECK (environment IN ('sandbox', 'production')),
    client_id text NOT NULL CHECK (length(client_id) BETWEEN 1 AND 128 AND client_id !~ '[[:space:]]'),
    -- The provider secret, sealed by the application to the Vult; the key
    -- version names the key that sealed it.
    secret_sealed bytea NOT NULL CHECK (octet_length(secret_sealed) BETWEEN 34 AND 4096),
    key_version integer NOT NULL CHECK (key_version > 0),
    webhook_url text NOT NULL DEFAULT '' CHECK (webhook_url = '' OR webhook_url ~ '^https://[^[:space:]]{1,2040}$'),
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, provider)
);

-- The owner changes the Vult's keys from a fresh sign-in, never through an
-- agent's connection.
CREATE FUNCTION public.vult_bank_owner_allowed(t uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = '' AND access.owner_write_allowed(t)
$$;

CREATE FUNCTION public.vult_bank_settings_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        IF TG_OP = 'UPDATE' AND (NEW.revision <> OLD.revision + 1 OR NEW.tenant_id <> OLD.tenant_id OR NEW.provider <> OLD.provider)
            OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'provider settings changed concurrently' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER vult_bank_settings_guard BEFORE INSERT OR UPDATE ON public.vult_bank_provider_settings
    FOR EACH ROW EXECUTE FUNCTION public.vult_bank_settings_guard();

ALTER TABLE public.vult_bank_provider_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.vult_bank_provider_settings FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.vult_bank_provider_settings FROM PUBLIC;
-- Read only by the owner and by a sync of one of the Vult's Books; agents
-- never read them, sealed or not.
CREATE POLICY bank_read ON public.vult_bank_provider_settings FOR SELECT
    USING (access.owner_allowed(tenant_id) OR tenant_id::text = coalesce(current_setting('vult.tenant_id', true), '')
        AND coalesce(current_setting('vult.bank_sync', true), '') <> '');
CREATE POLICY bank_owner_insert ON public.vult_bank_provider_settings FOR INSERT WITH CHECK (public.vult_bank_owner_allowed(tenant_id));
CREATE POLICY bank_owner_update ON public.vult_bank_provider_settings FOR UPDATE
    USING (public.vult_bank_owner_allowed(tenant_id)) WITH CHECK (public.vult_bank_owner_allowed(tenant_id));
REVOKE ALL ON FUNCTION public.vult_bank_owner_allowed(uuid), public.vult_bank_settings_guard() FROM PUBLIC;

-- Which group a document is filed in: statements, receipts and invoices,
-- company records, or tax. A document with none is read as a statement
-- when a statement was imported from it, else as a receipt.
CREATE TABLE public.document_groups (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    document_id uuid NOT NULL,
    document_group text NOT NULL CHECK (document_group IN ('statement', 'receipt', 'company', 'tax')),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, document_id),
    FOREIGN KEY (tenant_id, book_id, document_id) REFERENCES public.documents(tenant_id, book_id, id)
);

-- The owner files documents when signed in; a fresh sign-in isn't needed,
-- since filing changes no records. A connection that may write files
-- what it adds.
CREATE FUNCTION public.document_group_writer(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = '' AND public.ledger_context(t, b) AND access.owner_allowed(t)
    OR public.ledger_allowed(t, b, true)
$$;

CREATE FUNCTION public.document_group_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        IF TG_OP = 'UPDATE' AND ROW(NEW.tenant_id, NEW.book_id, NEW.document_id) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.document_id) THEN
            RAISE EXCEPTION 'a document keeps its filing' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER document_group_guard BEFORE INSERT OR UPDATE ON public.document_groups
    FOR EACH ROW EXECUTE FUNCTION public.document_group_guard();

ALTER TABLE public.document_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.document_groups FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.document_groups FROM PUBLIC;
CREATE POLICY group_read ON public.document_groups FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_id, false));
CREATE POLICY group_insert ON public.document_groups FOR INSERT WITH CHECK (public.document_group_writer(tenant_id, book_id));
CREATE POLICY group_update ON public.document_groups FOR UPDATE
    USING (public.document_group_writer(tenant_id, book_id)) WITH CHECK (public.document_group_writer(tenant_id, book_id));
REVOKE ALL ON FUNCTION public.document_group_writer(uuid, uuid), public.document_group_guard() FROM PUBLIC;

-- ------------------------------------------------------------------------
-- 0015 linked_books_settling
-- ------------------------------------------------------------------------

-- Settling up between linked Books, as the owner decided after review:
-- the business's direction to its owner decides whether the costs repaid
-- are named, an overpayment is recorded plainly rather than refused, and
-- undoing an entry frees the bank item it recorded.

-- Checks a settlement: the debtor's bank item paying out and the
-- creditor's taking in the same amount, at most five days apart, between
-- the two accounts an owed direction names. Returns the cost pairs it
-- repays (their entries in the debtor's Book) and the excess over what is
-- owed, which the reverse direction's accounts record: a draw where the
-- business's direction to its owner is owner's equity, else owed back.
-- Naming the costs (repays) is required of a business repaying its owner
-- with no owner's-equity direction (an accountable plan), and named costs
-- add up to exactly what the payment repays. Unnamed, a payment repays the
-- oldest open costs it covers whole. leaving_out is the settlement's own
-- entry once posted.
DROP FUNCTION public.settlement_check(uuid, uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid);
CREATE FUNCTION public.settlement_check(t uuid, debtor uuid, debtor_account uuid, debtor_item uuid, creditor uuid, creditor_account uuid, creditor_item uuid,
        repays jsonb, leaving_out uuid) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.bank_transactions%ROWTYPE; c public.bank_transactions%ROWTYPE; dclass text; cclass text; owed bigint; total bigint := 0;
    k text; entry uuid; cost bigint; amount bigint; excess bigint; repaying bigint; costs uuid[] := '{}'; rev public.book_link_terms%ROWTYPE;
    required boolean; debtor_name text; creditor_name text; r record;
BEGIN
    SELECT * INTO d FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = debtor AND x.id = debtor_item;
    SELECT * INTO c FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = creditor AND x.id = creditor_item;
    IF d.id IS NULL OR c.id IS NULL THEN
        RAISE EXCEPTION 'A settlement is two bank items, one in each linked Book.' USING ERRCODE = 'VP020';
    END IF;
    debtor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = debtor);
    creditor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = creditor);
    dclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = debtor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = debtor AND y.id = d.bank_account_id), d.account_id));
    cclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = creditor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = creditor AND y.id = c.bank_account_id), c.account_id));
    IF NOT (dclass = 'asset' AND d.amount_minor < 0 OR dclass = 'liability' AND d.amount_minor > 0)
        OR NOT (cclass = 'asset' AND c.amount_minor > 0 OR cclass = 'liability' AND c.amount_minor < 0) THEN
        RAISE EXCEPTION 'A settlement is money paid out of the Book that owes and into the Book that is owed; pick the payment in %, and its arrival in %.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    amount := abs(d.amount_minor);
    IF abs(c.amount_minor) <> amount OR c.currency IS DISTINCT FROM d.currency OR abs(c.occurred_on - d.occurred_on) > 5 THEN
        RAISE EXCEPTION 'A settlement''s two bank items are the same amount, at most five days apart; these are % and %, on % and %.',
            public.book_amount(t, debtor, amount), public.book_amount(t, creditor, abs(c.amount_minor)), d.occurred_on, c.occurred_on USING ERRCODE = 'VP020';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed' AND x.payer_book_id = creditor AND x.payer_account_id = creditor_account
                AND x.beneficiary_book_id = debtor AND x.beneficiary_account_id = debtor_account) THEN
        RAISE EXCEPTION 'Nothing is owed this way: % pays % only for what an active owed direction names. Set "% pays for %" to owed back in Links.',
            debtor_name, creditor_name, creditor_name, debtor_name USING ERRCODE = 'VP020';
    END IF;
    rev := public.active_link_terms(t, debtor, creditor);
    SELECT coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO owed
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.account_id = debtor_account AND x.entry_id IS DISTINCT FROM leaving_out;
    excess := greatest(amount - greatest(owed, 0), 0);
    repaying := amount - excess;
    IF excess > 0 AND rev.link_id IS NULL THEN
        RAISE EXCEPTION 'This pays % more than % owes (%). Set "% pays for %" in Links, and the extra is recorded there.',
            public.book_amount(t, debtor, excess), debtor_name, public.book_amount(t, debtor, greatest(owed, 0)), debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    IF jsonb_typeof(coalesce(repays, '[]'::jsonb)) <> 'array' THEN
        RAISE EXCEPTION 'repays is a list of pair keys.' USING ERRCODE = 'VP020';
    END IF;
    required := public.book_profile(t, creditor) = 'personal' AND public.book_profile(t, debtor) = 'business' AND rev.treatment IS DISTINCT FROM 'owner_equity';
    FOR k IN SELECT jsonb_array_elements_text(coalesce(repays, '[]'::jsonb)) LOOP
        entry := NULL;
        SELECT p.beneficiary_entry_id INTO entry FROM public.entry_pairs p
            WHERE p.tenant_id = t AND p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor AND p.pair_key = k
                AND NOT public.entry_reversed(t, debtor, p.beneficiary_entry_id);
        IF entry IS NULL THEN
            RAISE EXCEPTION 'repays names %, which isn''t a recorded cost % paid for %; name one from the open costs.', k, creditor_name, debtor_name USING ERRCODE = 'VP020';
        END IF;
        IF entry = ANY (costs) THEN
            RAISE EXCEPTION 'repays names % twice; name each cost once.', k USING ERRCODE = 'VP020';
        END IF;
        IF public.cost_repaid_by(t, debtor, entry, leaving_out) IS NOT NULL THEN
            RAISE EXCEPTION 'The cost % is already repaid; name an open cost instead.', k USING ERRCODE = 'VP020';
        END IF;
        costs := costs || entry;
        total := total + (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = entry AND x.side = 'credit');
    END LOOP;
    IF cardinality(costs) = 0 AND required THEN
        RAISE EXCEPTION '% repaying % names the costs it repays (repays), adding up to %; pick them from the open costs.',
            debtor_name, creditor_name, public.book_amount(t, debtor, repaying) USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) > 0 AND total <> repaying THEN
        RAISE EXCEPTION 'The costs named add up to %, but this payment repays %; name costs that add up to it exactly.',
            public.book_amount(t, debtor, total), public.book_amount(t, debtor, repaying) USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) = 0 THEN
        -- Unnamed: the oldest open costs the payment covers whole.
        FOR r IN SELECT p.beneficiary_entry_id AS entry,
                (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = p.beneficiary_entry_id AND x.side = 'credit') AS amount
            FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id = t AND e.book_id = debtor AND e.id = p.beneficiary_entry_id
            WHERE p.tenant_id = t AND p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
                AND NOT public.entry_reversed(t, debtor, p.beneficiary_entry_id) AND public.cost_repaid_by(t, debtor, p.beneficiary_entry_id, leaving_out) IS NULL
            ORDER BY e.accounting_date, e.sequence LOOP
            EXIT WHEN total + r.amount > repaying;
            costs := costs || r.entry;
            total := total + r.amount;
        END LOOP;
    END IF;
    RETURN jsonb_build_object('costs', to_jsonb(costs), 'excess', excess, 'repaying', repaying, 'required', required);
END $$;

-- Records a settlement the owner decided, or recorded at once: each entry
-- records its own bank item, on its date, to the owed direction's account
-- for what was owed and, for any excess, to the reverse direction's
-- account. The costs it repays are kept.
CREATE OR REPLACE FUNCTION public.settle_pair(t uuid, key text, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid, repays jsonb) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE ditem uuid; dbank uuid; citem uuid; cbank uuid; owed public.book_link_terms%ROWTYPE; rev public.book_link_terms%ROWTYPE;
    checked jsonb; e uuid; excess bigint; repaying bigint; amount bigint; bad int;
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    SELECT l.transaction_id, l.account_id INTO ditem, dbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = debtor AND l.entry_id = debtor_entry AND l.revoked_at IS NULL;
    SELECT l.transaction_id, l.account_id INTO citem, cbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = creditor AND l.entry_id = creditor_entry AND l.revoked_at IS NULL;
    owed := public.active_link_terms(t, creditor, debtor);
    rev := public.active_link_terms(t, debtor, creditor);
    IF ditem IS NULL OR citem IS NULL OR owed.treatment IS DISTINCT FROM 'owed' THEN
        RAISE EXCEPTION 'Each entry of a settlement records its own bank item, between Books an owed direction links.' USING ERRCODE = 'VP020';
    END IF;
    checked := public.settlement_check(t, debtor, owed.beneficiary_account_id, ditem, creditor, owed.payer_account_id, citem, repays, debtor_entry);
    excess := (checked ->> 'excess')::bigint;
    repaying := (checked ->> 'repaying')::bigint;
    amount := excess + repaying;
    -- The debtor's entry: the bank account for the whole amount, the payable
    -- for what was owed, the reverse direction's account for the excess.
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = dbank AND x.amount_minor = amount
            OR x.account_id = owed.beneficiary_account_id AND x.side = 'debit' AND x.amount_minor = repaying
            OR x.account_id = rev.payer_account_id AND x.side = 'debit' AND x.amount_minor = excess, false))
        + abs(count(*) - (1 + (repaying > 0)::int + (excess > 0)::int)) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The paying Book''s entry records % to what it owed and % as the extra; record it that way.',
            public.book_amount(t, debtor, repaying), public.book_amount(t, debtor, excess) USING ERRCODE = 'VP020';
    END IF;
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = cbank AND x.amount_minor = amount
            OR x.account_id = owed.payer_account_id AND x.side = 'credit' AND x.amount_minor = repaying
            OR x.account_id = rev.beneficiary_account_id AND x.side = 'credit' AND x.amount_minor = excess, false))
        + abs(count(*) - (1 + (repaying > 0)::int + (excess > 0)::int)) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = creditor AND x.entry_id = creditor_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The paid Book''s entry records % to what it was owed and % as the extra; record it that way.',
            public.book_amount(t, creditor, repaying), public.book_amount(t, creditor, excess) USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.entries x JOIN public.bank_transactions i ON i.tenant_id = x.tenant_id AND i.book_id = x.book_id
                AND i.id = CASE WHEN x.book_id = debtor THEN ditem ELSE citem END
            WHERE x.tenant_id = t AND (x.book_id = debtor AND x.id = debtor_entry OR x.book_id = creditor AND x.id = creditor_entry)
                AND (x.accounting_date <> i.occurred_on OR x.reversal_of IS NOT NULL)) THEN
        RAISE EXCEPTION 'Each entry of a settlement is dated its own bank item''s date.' USING ERRCODE = 'VP020';
    END IF;
    PERFORM set_config('vult.settling', key, true);
    INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id)
        VALUES (t, key, 'settlement', debtor, debtor_entry, creditor, creditor_entry);
    PERFORM set_config('vult.settling', '', true);
    FOR e IN SELECT (jsonb_array_elements_text(checked -> 'costs'))::uuid LOOP
        INSERT INTO public.settlement_repays(tenant_id, settlement_book_id, settlement_entry_id, cost_book_id, cost_entry_id) VALUES (t, debtor, debtor_entry, debtor, e);
    END LOOP;
END $$;

-- Joins the halves of a settlement prepared as proposals: the first waits,
-- the second, in the other Book, joins it once both are checked.
CREATE OR REPLACE FUNCTION public.pair_settlement(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; args jsonb; oargs jsonb; key text;
    debtor uuid; creditor uuid; dp uuid; cp uuid; dargs jsonb; cargs jsonb; checked jsonb;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed'
                AND (x.payer_book_id = b AND x.payer_account_id::text = args ->> 'other_account_id' OR x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id')) THEN
        RAISE EXCEPTION 'The settlement can''t be prepared: other_account_id is no account an owed direction of an active link names in this Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id <> b AND q.operation = 'record_bank_activity'
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' -> 'settlement') = 'true'::jsonb
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
            AND NOT EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = q.tenant_id
                AND (x.payer_book_id = q.book_id AND x.payer_proposal_id = q.id OR x.beneficiary_book_id = q.book_id AND x.beneficiary_proposal_id = q.id))
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF coalesce(args -> 'repays', '[]'::jsonb) <> coalesce(oargs -> 'repays', '[]'::jsonb) THEN
        RAISE EXCEPTION 'The settlement can''t be prepared: both halves name the same costs in repays.' USING ERRCODE = 'VP020';
    END IF;
    -- The Book whose account is the owed direction's payable owes.
    IF EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t AND x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id') THEN
        debtor := b; dp := p; dargs := args; creditor := other.book_id; cp := other.id; cargs := oargs;
    ELSE
        debtor := other.book_id; dp := other.id; dargs := oargs; creditor := b; cp := p; cargs := args;
    END IF;
    checked := public.settlement_check(t, debtor, (dargs ->> 'other_account_id')::uuid, (dargs ->> 'transaction_id')::uuid,
        creditor, (cargs ->> 'other_account_id')::uuid, (cargs ->> 'transaction_id')::uuid, args -> 'repays', NULL);
    IF (checked ->> 'excess')::bigint > 0 THEN
        RAISE EXCEPTION 'This pays % more than is owed. The owner records it in Vult, which records the extra plainly; don''t prepare it as a settlement.',
            public.book_amount(t, debtor, (checked ->> 'excess')::bigint) USING ERRCODE = 'VP020';
    END IF;
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, kind, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id)
        VALUES (t, key, 'settlement', debtor, dp, creditor, cp, (dargs ->> 'transaction_id')::uuid);
END $$;

-- Undoing an entry frees the bank item it recorded: the undo revokes the
-- item's link with its own receipt, so the item can be recorded again, or
-- for another Book, without a separate unlink.
CREATE OR REPLACE FUNCTION public.bank_unlink_receipt() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NEW.revoke_receipt_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM public.requests r
        WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id AND r.id = NEW.revoke_receipt_id
        AND r.actor_id = NEW.revoked_by AND (r.operation = 'unlink_bank_activity'
            OR r.operation = 'reverse_journal' AND EXISTS (SELECT 1 FROM public.entries e WHERE e.tenant_id = NEW.tenant_id
                AND e.book_id = NEW.book_id AND e.reversal_of = NEW.entry_id AND e.receipt_id = NEW.revoke_receipt_id))) THEN
        RAISE EXCEPTION 'decision receipt differs from the record' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;

-- The undo already counted against the agent's kind of activity; the link
-- it revokes doesn't count again.
CREATE OR REPLACE FUNCTION public.agent_kind_unlinked() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF coalesce(current_setting('vult.undo_unlink', true), '') = 'on' THEN
        RETURN NULL;
    END IF;
    IF EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = OLD.actor_id)
        AND NOT EXISTS (SELECT 1 FROM public.connections c WHERE c.actor_id = NEW.revoked_by) THEN
        PERFORM public.agent_kind_note(NEW.tenant_id, NEW.book_id, NEW.transaction_id, 'undone',
            public.agent_other_account(NEW.tenant_id, NEW.book_id, NEW.transaction_id, '{}'::jsonb, NEW.entry_id));
    END IF;
    RETURN NULL;
END $$;

REVOKE ALL ON FUNCTION public.settlement_check(uuid, uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid) FROM PUBLIC;


-- An opening balance between linked Books: what one Book owed the other
-- before their first bank line, entered once. It is two mirrored entries,
-- decided and undone as one: the Book that owes credits the owed
-- direction's payable, the Book owed debits its receivable, and each posts
-- the other side to its own opening-balance equity account. Pairs record
-- the Book that owes as the payer.
ALTER TABLE public.proposal_pairs DROP CONSTRAINT proposal_pairs_kind_check;
ALTER TABLE public.proposal_pairs ADD CONSTRAINT proposal_pairs_kind_check CHECK (kind IN ('cost', 'settlement', 'opening'));
ALTER TABLE public.entry_pairs DROP CONSTRAINT entry_pairs_kind_check;
ALTER TABLE public.entry_pairs ADD CONSTRAINT entry_pairs_kind_check CHECK (kind IN ('cost', 'settlement', 'opening'));

CREATE OR REPLACE FUNCTION public.entry_pair_guard() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE terms public.book_link_terms%ROWTYPE; item public.bank_transactions%ROWTYPE; bank_account uuid; tid uuid;
    paid text; amount bigint; n int; bad int;
BEGIN
    IF NOT (access.owner_allowed(NEW.tenant_id) AND access.owner_write_allowed(NEW.tenant_id)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    IF NEW.kind IN ('settlement', 'opening') THEN
        -- Settlements and opening balances are checked and recorded by
        -- settle_pair and open_link_balance only.
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

-- A Book's opening-balance equity account, by role.
CREATE FUNCTION public.opening_equity_account(t uuid, b uuid) RETURNS uuid
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT a.id FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = b AND a.class = 'equity' AND a.role = 'opening_balance'
    AND a.status IS DISTINCT FROM 'retired' AND NOT a.heading ORDER BY a.code, a.id LIMIT 1
$$;

-- Checks an opening balance's two entries against the owed direction and
-- each Book's opening-balance account, and that the direction has no other
-- live opening balance. leaving_out names this pair's debtor entry, once
-- posted.
CREATE FUNCTION public.opening_check(t uuid, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid, leaving_out uuid) RETURNS void
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE owed public.book_link_terms%ROWTYPE; dequity uuid; cequity uuid; amount bigint; bad int; debtor_name text; creditor_name text;
BEGIN
    debtor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = debtor);
    creditor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = creditor);
    owed := public.active_link_terms(t, creditor, debtor);
    IF owed.treatment IS DISTINCT FROM 'owed' THEN
        RAISE EXCEPTION 'Nothing is owed this way: set "% pays for %" to owed back in Links, then enter the opening balance.', creditor_name, debtor_name USING ERRCODE = 'VP020';
    END IF;
    dequity := public.opening_equity_account(t, debtor);
    cequity := public.opening_equity_account(t, creditor);
    IF dequity IS NULL OR cequity IS NULL THEN
        RAISE EXCEPTION 'Add an opening balances account (equity, role opening balance) in %, then enter the opening balance.',
            CASE WHEN dequity IS NULL THEN debtor_name ELSE creditor_name END USING ERRCODE = 'VP020';
    END IF;
    amount := (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry AND x.side = 'credit');
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = owed.beneficiary_account_id AND x.side = 'credit' AND x.amount_minor = amount
            OR x.account_id = dequity AND x.side = 'debit' AND x.amount_minor = amount, false)) + abs(count(*) - 2) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry;
    IF bad <> 0 OR amount IS NULL THEN
        RAISE EXCEPTION 'An opening balance in % credits what it owes % and debits its opening balances account, for the same amount.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = owed.payer_account_id AND x.side = 'debit' AND x.amount_minor = amount
            OR x.account_id = cequity AND x.side = 'credit' AND x.amount_minor = amount, false)) + abs(count(*) - 2) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = creditor AND x.entry_id = creditor_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'An opening balance in % debits what % owes it and credits its opening balances account, for the same amount (%).',
            creditor_name, debtor_name, public.book_amount(t, debtor, amount) USING ERRCODE = 'VP020';
    END IF;
    IF (SELECT count(DISTINCT e.accounting_date) FROM public.entries e WHERE e.tenant_id = t
            AND (e.book_id = debtor AND e.id = debtor_entry OR e.book_id = creditor AND e.id = creditor_entry) AND e.reversal_of IS NULL) <> 1
        OR (SELECT count(*) FROM public.entries e WHERE e.tenant_id = t
            AND (e.book_id = debtor AND e.id = debtor_entry OR e.book_id = creditor AND e.id = creditor_entry) AND e.reversal_of IS NULL) <> 2 THEN
        RAISE EXCEPTION 'Both entries of an opening balance are dated the same day, the day it was owed as of.' USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = t AND p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor
            AND p.payer_entry_id IS DISTINCT FROM leaving_out AND NOT public.entry_reversed(t, debtor, p.payer_entry_id)) THEN
        RAISE EXCEPTION 'What % owed % at the start is already entered; undo it to enter it differently.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
END $$;

-- Records an opening balance the owner decided, or entered at once.
CREATE FUNCTION public.open_link_balance(t uuid, key text, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    PERFORM public.opening_check(t, debtor, debtor_entry, creditor, creditor_entry, debtor_entry);
    PERFORM set_config('vult.settling', key, true);
    INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id)
        VALUES (t, key, 'opening', debtor, debtor_entry, creditor, creditor_entry);
    PERFORM set_config('vult.settling', '', true);
END $$;

REVOKE ALL ON FUNCTION public.opening_equity_account(uuid, uuid), public.opening_check(uuid, uuid, uuid, uuid, uuid, uuid),
    public.open_link_balance(uuid, text, uuid, uuid, uuid, uuid) FROM PUBLIC;


-- Opening balances prepared as proposals: each Book's half is a journal
-- with opening and other_book_id; the first waits, the second joins it once
-- both mirror each other by the owed direction and opening-balance
-- accounts. Opening balances have no bank item.
ALTER TABLE public.proposal_pairs ALTER COLUMN transaction_id DROP NOT NULL;

CREATE FUNCTION public.pair_opening(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; args jsonb; oargs jsonb; key text; them uuid;
    debtor uuid; creditor uuid; dargs jsonb; cargs jsonb; dp uuid; cp uuid; owed public.book_link_terms%ROWTYPE; dequity uuid; cequity uuid;
    amount bigint; bad int; debtor_name text; creditor_name text;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    them := (args ->> 'other_book_id')::uuid;
    -- This half owes when it credits the payable of the other Book's owed direction.
    IF EXISTS (SELECT 1 FROM jsonb_array_elements(args -> 'lines') l
            WHERE l ->> 'account_id' = (public.active_link_terms(t, them, b)).beneficiary_account_id::text AND l ->> 'side' = 'credit') THEN
        debtor := b; creditor := them;
    ELSIF EXISTS (SELECT 1 FROM jsonb_array_elements(args -> 'lines') l
            WHERE l ->> 'account_id' = (public.active_link_terms(t, b, them)).payer_account_id::text AND l ->> 'side' = 'debit') THEN
        debtor := them; creditor := b;
    ELSE
        RAISE EXCEPTION 'The opening balance can''t be prepared: it credits what this Book owes the other, or debits what the other owes it, by an owed direction of an active link.' USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id = them AND q.operation = 'post_journal'
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' -> 'opening') = 'true'::jsonb
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'other_book_id' = b::text
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
            AND NOT EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = q.tenant_id
                AND (x.payer_book_id = q.book_id AND x.payer_proposal_id = q.id OR x.beneficiary_book_id = q.book_id AND x.beneficiary_proposal_id = q.id))
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF debtor = b THEN dargs := args; dp := p; cargs := oargs; cp := other.id; ELSE dargs := oargs; dp := other.id; cargs := args; cp := p; END IF;
    debtor_name := (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = debtor);
    creditor_name := (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = creditor);
    owed := public.active_link_terms(t, creditor, debtor);
    dequity := public.opening_equity_account(t, debtor);
    cequity := public.opening_equity_account(t, creditor);
    IF owed.treatment IS DISTINCT FROM 'owed' OR dequity IS NULL OR cequity IS NULL THEN
        RAISE EXCEPTION 'The opening balance can''t be prepared: % needs an owed direction to %, and both Books an opening balances account.', creditor_name, debtor_name USING ERRCODE = 'VP020';
    END IF;
    amount := (SELECT sum((l ->> 'amount_minor')::bigint) FROM jsonb_array_elements(dargs -> 'lines') l WHERE l ->> 'side' = 'credit');
    SELECT count(*) FILTER (WHERE NOT coalesce(l ->> 'account_id' = owed.beneficiary_account_id::text AND l ->> 'side' = 'credit' AND (l ->> 'amount_minor')::bigint = amount
            OR l ->> 'account_id' = dequity::text AND l ->> 'side' = 'debit' AND (l ->> 'amount_minor')::bigint = amount, false)) + abs(count(*) - 2)
        INTO bad FROM jsonb_array_elements(dargs -> 'lines') l;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The opening balance can''t be prepared: in % it credits what it owes % and debits its opening balances account, for the same amount.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    SELECT count(*) FILTER (WHERE NOT coalesce(l ->> 'account_id' = owed.payer_account_id::text AND l ->> 'side' = 'debit' AND (l ->> 'amount_minor')::bigint = amount
            OR l ->> 'account_id' = cequity::text AND l ->> 'side' = 'credit' AND (l ->> 'amount_minor')::bigint = amount, false)) + abs(count(*) - 2)
        INTO bad FROM jsonb_array_elements(cargs -> 'lines') l;
    IF bad <> 0 OR dargs ->> 'accounting_date' IS DISTINCT FROM cargs ->> 'accounting_date' THEN
        RAISE EXCEPTION 'The opening balance can''t be prepared: in % it mirrors %, on the same date, for the same amount.', creditor_name, debtor_name USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.entry_pairs x WHERE x.tenant_id = t AND x.kind = 'opening' AND x.payer_book_id = debtor AND x.beneficiary_book_id = creditor
            AND NOT public.entry_reversed(t, debtor, x.payer_entry_id)) THEN
        RAISE EXCEPTION 'The opening balance can''t be prepared: what % owed % at the start is already entered.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, kind, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id)
        VALUES (t, key, 'opening', debtor, dp, creditor, cp, NULL);
END $$;
REVOKE ALL ON FUNCTION public.pair_opening(uuid, uuid, uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.pair_proposal(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; terms public.book_link_terms%ROWTYPE;
    item public.bank_transactions%ROWTYPE; args jsonb; oargs jsonb; key text; line jsonb; acc public.accounts%ROWTYPE;
    amount bigint; debits bigint := 0; credits int := 0; paid text;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    IF NOT FOUND OR NOT public.ledger_allowed(t, b, false)
        OR me.connection_id::text IS DISTINCT FROM current_setting('vult.connection_id', true) THEN
        RAISE EXCEPTION 'only the preparing connection pairs its proposal' USING ERRCODE = '42501';
    END IF;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    IF key IS NULL THEN
        RETURN;
    END IF;
    IF me.operation = 'post_journal' AND (args -> 'opening') = 'true'::jsonb THEN
        PERFORM public.pair_opening(t, b, p);
        RETURN;
    END IF;
    IF me.operation = 'record_bank_activity' AND (args -> 'settlement') = 'true'::jsonb THEN
        PERFORM public.pair_settlement(t, b, p);
        RETURN;
    END IF;
    IF me.operation = 'record_bank_activity' THEN
        IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id
                WHERE x.tenant_id = t AND x.payer_book_id = b AND x.payer_account_id::text = args ->> 'other_account_id' AND x.active AND l.active) THEN
            RAISE EXCEPTION 'The pair can''t be prepared: other_account_id is no linked Book''s account in this Book, or that link is turned off.' USING ERRCODE = 'VP020';
        END IF;
        RETURN;
    END IF;
    IF me.operation <> 'post_journal' THEN
        RAISE EXCEPTION 'The pair can''t be prepared: a pair is record_bank_activity in the Book that paid and post_journal in the Book it paid for.' USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id <> b AND q.operation = 'record_bank_activity'
            AND (args ->> 'payer_book_id' IS NULL OR q.book_id::text = args ->> 'payer_book_id')
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' -> 'settlement') IS DISTINCT FROM 'true'::jsonb
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: prepare the half in the Book that paid first; no waiting half has pair_key %.', key USING ERRCODE = 'VP020';
    END IF;
    terms := public.active_link_terms(t, other.book_id, b);
    IF terms.link_id IS NULL THEN
        RAISE EXCEPTION 'The pair can''t be prepared: these Books aren''t linked for this direction, or the link is turned off.' USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = t AND x.payer_book_id = other.book_id AND x.payer_proposal_id = other.id) THEN
        RAISE EXCEPTION 'The pair can''t be prepared: its other half is already paired.' USING ERRCODE = 'VP020';
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF oargs ->> 'transaction_id' IS DISTINCT FROM args ->> 'paired_transaction_id' THEN
        RAISE EXCEPTION 'The pair can''t be prepared: paired_transaction_id isn''t the bank item its other half records.' USING ERRCODE = 'VP020';
    END IF;
    IF oargs ->> 'other_account_id' IS DISTINCT FROM terms.payer_account_id::text THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the other half doesn''t record to the account this link names in that Book.' USING ERRCODE = 'VP020';
    END IF;
    SELECT * INTO item FROM public.bank_transactions WHERE tenant_id = t AND book_id = other.book_id AND id = (oargs ->> 'transaction_id')::uuid;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the bank item isn''t in the Book that paid.' USING ERRCODE = 'VP020';
    END IF;
    -- Amounts follow the bank account's own balance: a card charge raises a
    -- liability (positive), money paid from an account lowers an asset
    -- (negative). A cost pair records money paid out.
    paid := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = other.book_id
        AND a.id = coalesce((SELECT x.account_id FROM public.bank_accounts x WHERE x.tenant_id = t AND x.book_id = other.book_id AND x.id = item.bank_account_id), item.account_id));
    IF NOT (paid = 'liability' AND item.amount_minor > 0 OR paid = 'asset' AND item.amount_minor < 0) THEN
        RAISE EXCEPTION 'The pair can''t be prepared: a cost pair records money paid out, such as a charge on a card.' USING ERRCODE = 'VP020';
    END IF;
    amount := abs(item.amount_minor);
    IF (SELECT k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = b) IS DISTINCT FROM item.currency THEN
        RAISE EXCEPTION 'The pair can''t be prepared: the bank item is in another currency than this Book.' USING ERRCODE = 'VP020';
    END IF;
    IF args ->> 'accounting_date' IS DISTINCT FROM item.occurred_on::text THEN
        RAISE EXCEPTION 'The pair can''t be prepared: this entry''s date isn''t the bank item''s date (%).', item.occurred_on USING ERRCODE = 'VP020';
    END IF;
    FOR line IN SELECT * FROM jsonb_array_elements(coalesce(args -> 'lines', '[]'::jsonb)) LOOP
        SELECT * INTO acc FROM public.accounts WHERE tenant_id = t AND book_id = b AND id = (line ->> 'account_id')::uuid;
        IF line ->> 'side' = 'credit' THEN
            credits := credits + 1;
            IF acc.id IS DISTINCT FROM terms.beneficiary_account_id OR (line ->> 'amount_minor')::bigint <> amount THEN
                RAISE EXCEPTION 'The pair can''t be prepared: this entry credits the account the link names here (%) with the whole amount.', terms.beneficiary_account_id USING ERRCODE = 'VP020';
            END IF;
        ELSE
            IF NOT (acc.class = 'expense' OR acc.class = 'asset' AND coalesce(acc.role, '') NOT IN ('cash', 'card')
                    AND NOT EXISTS (SELECT 1 FROM public.bank_accounts x WHERE x.tenant_id = t AND x.book_id = b AND x.account_id = acc.id)) THEN
                RAISE EXCEPTION 'The pair can''t be prepared: this entry debits only expenses, or assets that aren''t cash, a card or a bank account.' USING ERRCODE = 'VP020';
            END IF;
            debits := debits + (line ->> 'amount_minor')::bigint;
        END IF;
    END LOOP;
    IF credits <> 1 OR debits <> amount THEN
        RAISE EXCEPTION 'The pair can''t be prepared: this entry is for the bank item''s amount, with one credit to the link''s account.' USING ERRCODE = 'VP020';
    END IF;
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id)
        VALUES (t, key, other.book_id, other.id, b, p, item.id);
END $$;

CREATE OR REPLACE FUNCTION public.proposal_pair_of(t uuid, b uuid, p uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE coalesce((
    SELECT jsonb_build_object('state', 'paired', 'pair_key', x.pair_key, 'transaction_id', x.transaction_id, 'kind', x.kind,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'proposal_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_proposal_id ELSE x.payer_proposal_id END,
        'side', CASE WHEN x.payer_book_id = b THEN 'beneficiary' ELSE 'payer' END,
        -- A settlement's debtor is the owed direction's beneficiary.
        'link_active', CASE WHEN x.kind IN ('settlement', 'opening') THEN (public.active_link_terms(t, x.beneficiary_book_id, x.payer_book_id)).link_id
            ELSE (public.active_link_terms(t, x.payer_book_id, x.beneficiary_book_id)).link_id END IS NOT NULL)
    FROM public.proposal_pairs x WHERE x.tenant_id = t
        AND (x.payer_book_id = b AND x.payer_proposal_id = p OR x.beneficiary_book_id = b AND x.beneficiary_proposal_id = p)), (
    SELECT jsonb_build_object('state', 'waiting_for_pair', 'pair_key', args ->> 'pair_key', 'side', 'beneficiary',
        'book_id', x.beneficiary_book_id,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = x.beneficiary_book_id),
        'link_active', (public.active_link_terms(t, b, x.beneficiary_book_id)).link_id IS NOT NULL)
    FROM public.operation_proposals q
        CROSS JOIN LATERAL (SELECT convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' AS args) a
        JOIN public.book_link_terms x ON x.tenant_id = q.tenant_id AND x.payer_book_id = q.book_id AND x.payer_account_id::text = a.args ->> 'other_account_id'
    WHERE q.tenant_id = t AND q.book_id = b AND q.id = p AND q.operation = 'record_bank_activity' AND a.args ->> 'pair_key' IS NOT NULL
        AND (a.args -> 'settlement') IS DISTINCT FROM 'true'::jsonb
    LIMIT 1), (
    -- A settlement half waiting for the other Book's: the side is the
    -- partner's, the Book that pays (owes) or the Book paid.
    SELECT jsonb_build_object('state', 'waiting_for_pair', 'pair_key', a.args ->> 'pair_key', 'kind', 'settlement',
        'side', CASE WHEN x.payer_book_id = b THEN 'payer' ELSE 'beneficiary' END,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'link_active', (public.active_link_terms(t, x.payer_book_id, x.beneficiary_book_id)).link_id IS NOT NULL)
    FROM public.operation_proposals q
        CROSS JOIN LATERAL (SELECT convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' AS args) a
        JOIN public.book_link_terms x ON x.tenant_id = q.tenant_id AND x.treatment = 'owed'
            AND (x.payer_book_id = q.book_id AND x.payer_account_id::text = a.args ->> 'other_account_id'
                OR x.beneficiary_book_id = q.book_id AND x.beneficiary_account_id::text = a.args ->> 'other_account_id')
    WHERE q.tenant_id = t AND q.book_id = b AND q.id = p AND q.operation = 'record_bank_activity' AND a.args ->> 'pair_key' IS NOT NULL
        AND (a.args -> 'settlement') = 'true'::jsonb
    LIMIT 1), (
    -- An opening balance's half waiting for the other Book's.
    SELECT jsonb_build_object('state', 'waiting_for_pair', 'pair_key', a.args ->> 'pair_key', 'kind', 'opening',
        'side', CASE WHEN EXISTS (SELECT 1 FROM jsonb_array_elements(a.args -> 'lines') l
            WHERE l ->> 'account_id' = (public.active_link_terms(t, (a.args ->> 'other_book_id')::uuid, b)).beneficiary_account_id::text) THEN 'beneficiary' ELSE 'payer' END,
        'book_id', a.args ->> 'other_book_id',
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id::text = a.args ->> 'other_book_id'),
        'link_active', EXISTS (SELECT 1 FROM public.book_links l WHERE l.tenant_id = t AND l.active
            AND (l.book_a = b AND l.book_b::text = a.args ->> 'other_book_id' OR l.book_b = b AND l.book_a::text = a.args ->> 'other_book_id')))
    FROM public.operation_proposals q
        CROSS JOIN LATERAL (SELECT convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' AS args) a
    WHERE q.tenant_id = t AND q.book_id = b AND q.id = p AND q.operation = 'post_journal' AND a.args ->> 'pair_key' IS NOT NULL
        AND (a.args -> 'opening') = 'true'::jsonb
    LIMIT 1)) END
$$;

-- ------------------------------------------------------------------------
-- 0016 linked_books_followups
-- ------------------------------------------------------------------------

-- Follow-ups to settling up between linked Books: an agent's overpaying
-- half is refused whichever comes first, unnamed payments repay the oldest
-- first, an opening balance is repaid like a cost, and turning a link off
-- needs only a valid sign-in.

-- Each half checks, from its own Book, that it pays no more than is owed.
CREATE OR REPLACE FUNCTION public.pair_settlement(t uuid, b uuid, p uuid) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE me public.operation_proposals%ROWTYPE; other public.operation_proposals%ROWTYPE; args jsonb; oargs jsonb; key text;
    debtor uuid; creditor uuid; dp uuid; cp uuid; dargs jsonb; cargs jsonb; checked jsonb; paid bigint; owing bigint;
BEGIN
    SELECT * INTO me FROM public.operation_proposals WHERE tenant_id = t AND book_id = b AND id = p;
    args := convert_from(me.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    key := args ->> 'pair_key';
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed'
                AND (x.payer_book_id = b AND x.payer_account_id::text = args ->> 'other_account_id' OR x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id')) THEN
        RAISE EXCEPTION 'The settlement can''t be prepared: other_account_id is no account an owed direction of an active link names in this Book.' USING ERRCODE = 'VP020';
    END IF;
    -- Each half checks, from its own Book, that it pays no more than is owed,
    -- so an overpayment is refused on whichever half comes first.
    SELECT abs(x.amount_minor) INTO paid FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = b AND x.id::text = args ->> 'transaction_id';
    SELECT coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO owing
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = b AND x.account_id::text = args ->> 'other_account_id';
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t AND x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id') THEN
        owing := -owing;
    END IF;
    IF paid > greatest(owing, 0) THEN
        RAISE EXCEPTION 'This pays % more than is owed. The owner records it in Vult, which records the extra plainly; don''t prepare it as a settlement.',
            public.book_amount(t, b, paid - greatest(owing, 0)) USING ERRCODE = 'VP020';
    END IF;
    SELECT q.* INTO other FROM public.operation_proposals q
        WHERE q.tenant_id = t AND q.book_id <> b AND q.operation = 'record_bank_activity'
            AND convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' ->> 'pair_key' = key
            AND (convert_from(q.payload_bytes, 'UTF8')::jsonb -> 'arguments' -> 'settlement') = 'true'::jsonb
            AND (q.expires_at IS NULL OR q.expires_at > clock_timestamp())
            AND NOT EXISTS (SELECT 1 FROM public.operation_decisions d WHERE d.tenant_id = q.tenant_id AND d.book_id = q.book_id AND d.proposal_id = q.id)
            AND NOT EXISTS (SELECT 1 FROM public.proposal_pairs x WHERE x.tenant_id = q.tenant_id
                AND (x.payer_book_id = q.book_id AND x.payer_proposal_id = q.id OR x.beneficiary_book_id = q.book_id AND x.beneficiary_proposal_id = q.id))
        ORDER BY q.sequence DESC LIMIT 1;
    IF NOT FOUND THEN
        RETURN;
    END IF;
    oargs := convert_from(other.payload_bytes, 'UTF8')::jsonb -> 'arguments';
    IF coalesce(args -> 'repays', '[]'::jsonb) <> coalesce(oargs -> 'repays', '[]'::jsonb) THEN
        RAISE EXCEPTION 'The settlement can''t be prepared: both halves name the same costs in repays.' USING ERRCODE = 'VP020';
    END IF;
    -- The Book whose account is the owed direction's payable owes.
    IF EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t AND x.beneficiary_book_id = b AND x.beneficiary_account_id::text = args ->> 'other_account_id') THEN
        debtor := b; dp := p; dargs := args; creditor := other.book_id; cp := other.id; cargs := oargs;
    ELSE
        debtor := other.book_id; dp := other.id; dargs := oargs; creditor := b; cp := p; cargs := args;
    END IF;
    checked := public.settlement_check(t, debtor, (dargs ->> 'other_account_id')::uuid, (dargs ->> 'transaction_id')::uuid,
        creditor, (cargs ->> 'other_account_id')::uuid, (cargs ->> 'transaction_id')::uuid, args -> 'repays', NULL);
    IF (checked ->> 'excess')::bigint > 0 THEN
        RAISE EXCEPTION 'This pays % more than is owed. The owner records it in Vult, which records the extra plainly; don''t prepare it as a settlement.',
            public.book_amount(t, debtor, (checked ->> 'excess')::bigint) USING ERRCODE = 'VP020';
    END IF;
    INSERT INTO public.proposal_pairs(tenant_id, pair_key, kind, payer_book_id, payer_proposal_id, beneficiary_book_id, beneficiary_proposal_id, transaction_id)
        VALUES (t, key, 'settlement', debtor, dp, creditor, cp, (dargs ->> 'transaction_id')::uuid);
END $$;

-- Who owes whom counts every payment toward the costs, and the opening
-- balance, it covers: named
-- costs are repaid by their settlement, and the rest are repaid oldest
-- first by whatever paid down the balance, so nothing reads open once
-- nothing is owed.
CREATE OR REPLACE FUNCTION public.linked_balances(t uuid, b uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
WITH allowed AS (
    SELECT CASE WHEN b IS NULL THEN access.owner_allowed(t) ELSE access.owner_allowed(t) OR public.ledger_allowed(t, b, false) END AS ok
), links AS (
    SELECT l.* FROM public.book_links l, allowed WHERE allowed.ok AND l.tenant_id = t AND (b IS NULL OR b IN (l.book_a, l.book_b))
), balance AS (
    -- Each line's effect, debit positive, on the accounts terms name.
    SELECT x.book_id, x.account_id, x.entry_id, CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END AS amount
    FROM public.lines x WHERE x.tenant_id = t
), dirs AS (
    -- Owed directions: the payer is owed (receivable), the beneficiary owes (payable).
    SELECT k.link_id, k.payer_book_id AS creditor, k.payer_account_id AS creditor_account, k.beneficiary_book_id AS debtor, k.beneficiary_account_id AS debtor_account,
        coalesce((SELECT sum(y.amount) FROM balance y WHERE y.book_id = k.payer_book_id AND y.account_id = k.payer_account_id), 0) AS creditor_says,
        coalesce((SELECT -sum(y.amount) FROM balance y WHERE y.book_id = k.beneficiary_book_id AND y.account_id = k.beneficiary_account_id), 0) AS debtor_says
    FROM public.book_link_terms k JOIN links l ON l.tenant_id = t AND l.id = k.link_id WHERE k.tenant_id = t AND k.treatment = 'owed'
), paired AS (
    SELECT p.payer_book_id AS book_id, p.payer_entry_id AS entry_id FROM public.entry_pairs p WHERE p.tenant_id = t
    UNION ALL SELECT p.beneficiary_book_id, p.beneficiary_entry_id FROM public.entry_pairs p WHERE p.tenant_id = t
), net AS (
    SELECT l.id AS link_id, l.book_a, l.book_b,
        coalesce(sum(CASE WHEN d.creditor = l.book_a THEN d.debtor_says ELSE -d.debtor_says END), 0) AS owed_to_a,
        bool_and(d.creditor_says = d.debtor_says) AS matches
    FROM links l JOIN dirs d ON d.link_id = l.id GROUP BY l.id, l.book_a, l.book_b
), costs AS (
    -- Cost pairs along each owed direction, with what is still owed there.
    SELECT p.*, d.link_id, d.creditor, e.accounting_date, e.description, e.sequence,
        (SELECT sum(z.amount_minor) FROM public.lines z WHERE z.tenant_id = t AND z.book_id = p.beneficiary_book_id AND z.entry_id = p.beneficiary_entry_id AND z.side = 'credit')::bigint AS amount,
        public.cost_repaid_by(t, p.beneficiary_book_id, p.beneficiary_entry_id, NULL) IS NOT NULL AS named,
        greatest(d.debtor_says, 0) AS owed
    FROM public.entry_pairs p JOIN dirs d ON (p.kind = 'cost' AND d.creditor = p.payer_book_id AND d.debtor = p.beneficiary_book_id
            OR p.kind = 'opening' AND d.creditor = p.beneficiary_book_id AND d.debtor = p.payer_book_id)
        JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
    WHERE p.tenant_id = t AND p.kind IN ('cost', 'opening') AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)
), ranked AS (
    -- Payments that named no costs repay the oldest first: a cost is open
    -- only while what is still owed reaches back to it, newest first.
    SELECT c.*, sum(CASE WHEN c.named THEN 0 ELSE c.amount END)
        OVER (PARTITION BY c.link_id, c.creditor ORDER BY c.accounting_date DESC, c.sequence DESC, c.pair_key DESC ROWS UNBOUNDED PRECEDING) AS newer
    FROM costs c
)
SELECT jsonb_build_object(
    'links', coalesce((SELECT jsonb_agg(jsonb_build_object(
        'link_id', n.link_id,
        'debtor_book_id', CASE WHEN n.owed_to_a >= 0 THEN n.book_b ELSE n.book_a END,
        'creditor_book_id', CASE WHEN n.owed_to_a >= 0 THEN n.book_a ELSE n.book_b END,
        'amount_minor', abs(n.owed_to_a)::text,
        'currency', (SELECT k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = n.book_a),
        'matches', n.matches,
        'mismatched_entry_ids', coalesce((SELECT jsonb_agg(DISTINCT y.entry_id) FROM dirs d JOIN balance y
                ON (y.book_id = d.creditor AND y.account_id = d.creditor_account OR y.book_id = d.debtor AND y.account_id = d.debtor_account)
            WHERE d.link_id = n.link_id
                AND NOT EXISTS (SELECT 1 FROM paired q WHERE q.book_id = y.book_id AND q.entry_id = y.entry_id)
                AND NOT EXISTS (SELECT 1 FROM public.entries e JOIN paired q ON q.book_id = e.book_id AND q.entry_id = e.reversal_of
                    WHERE e.tenant_id = t AND e.book_id = y.book_id AND e.id = y.entry_id)), '[]'::jsonb),
        'open_pairs', coalesce((SELECT jsonb_agg(jsonb_build_object(
                'pair_key', r.pair_key, 'kind', r.kind, 'payer_book_id', r.payer_book_id, 'beneficiary_book_id', r.beneficiary_book_id,
                'date', r.accounting_date, 'description', r.description,
                'amount_minor', r.amount::text,
                'repaid', r.named OR r.newer - r.amount >= r.owed,
                'has_receipt', EXISTS (SELECT 1 FROM public.evidence_links v WHERE v.tenant_id = t
                        AND (v.book_id = r.beneficiary_book_id AND v.entry_id = r.beneficiary_entry_id OR v.book_id = r.payer_book_id AND v.entry_id = r.payer_entry_id)
                        AND v.action = 'attach' AND v.role IN ('receipt', 'invoice')
                        AND NOT EXISTS (SELECT 1 FROM public.evidence_links w WHERE w.tenant_id = v.tenant_id AND w.book_id = v.book_id AND w.link_id = v.link_id AND w.revision > v.revision))
                    OR EXISTS (SELECT 1 FROM public.source_citations c WHERE c.tenant_id = t
                        AND (c.book_id = r.beneficiary_book_id AND c.entry_id = r.beneficiary_entry_id OR c.book_id = r.payer_book_id AND c.entry_id = r.payer_entry_id)))
                ORDER BY r.accounting_date, r.pair_key)
            FROM ranked r WHERE r.link_id = n.link_id), '[]'::jsonb))
        ORDER BY n.link_id) FROM net n), '[]'::jsonb),
    'books', coalesce((SELECT jsonb_agg(jsonb_build_object('book_id', k.id, 'name', k.name,
        'liabilities', coalesce((SELECT jsonb_agg(jsonb_build_object('account_id', a.id, 'name', a.name, 'role', coalesce(a.role, ''),
                'balance_minor', coalesce((SELECT -sum(y.amount) FROM balance y WHERE y.book_id = a.book_id AND y.account_id = a.id), 0)::text) ORDER BY a.code, a.id)
            FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = k.id AND a.class = 'liability'
                AND NOT EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t
                    AND (x.payer_book_id = k.id AND x.payer_account_id = a.id OR x.beneficiary_book_id = k.id AND x.beneficiary_account_id = a.id))), '[]'::jsonb))
        ORDER BY k.name, k.id)
        FROM public.books k, allowed WHERE allowed.ok AND k.tenant_id = t AND (b IS NULL AND EXISTS (SELECT 1 FROM links l WHERE k.id IN (l.book_a, l.book_b)) OR k.id = b)), '[]'::jsonb))
$$;


-- Turning a link or a direction off only narrows what can happen, so any
-- valid sign-in of the owner may do it, as disconnecting an agent; turning
-- one on, or changing its terms, needs a recent sign-in.
CREATE FUNCTION public.link_owner_narrowing(t uuid) RETURNS boolean
    LANGUAGE sql VOLATILE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = ''
    AND coalesce(current_setting('vult.owner_posting', true), '') = ''
    AND access.owner_allowed(t)
    AND coalesce(access.owner_identity(t)::text = current_setting('vult.actor_id', true), false)
$$;
REVOKE ALL ON FUNCTION public.link_owner_narrowing(uuid) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.book_link_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END)
        AND NOT (TG_OP = 'UPDATE' AND public.link_owner_narrowing(NEW.tenant_id) AND NOT NEW.active
            AND ROW(NEW.tenant_id, NEW.id, NEW.book_a, NEW.book_b) IS NOT DISTINCT FROM ROW(OLD.tenant_id, OLD.id, OLD.book_a, OLD.book_b)) THEN
        RAISE EXCEPTION 'only the owner links Books' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND (p.payer_book_id, p.beneficiary_book_id) IN ((OLD.book_a, OLD.book_b), (OLD.book_b, OLD.book_a)))
            OR EXISTS (SELECT 1 FROM public.proposal_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND (p.payer_book_id, p.beneficiary_book_id) IN ((OLD.book_a, OLD.book_b), (OLD.book_b, OLD.book_a))) THEN
            RAISE EXCEPTION 'These Books have pairs between them; turn the link off instead.' USING ERRCODE = 'VP020';
        END IF;
        RETURN OLD;
    END IF;
    IF NOT public.ledger_import_owner() AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF ROW(NEW.tenant_id, NEW.id, NEW.book_a, NEW.book_b, NEW.created_at) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.id, OLD.book_a, OLD.book_b, OLD.created_at) THEN
            RAISE EXCEPTION 'a link keeps its Books' USING ERRCODE = '23514';
        END IF;
        IF NEW.revision <> OLD.revision + 1 THEN
            RAISE EXCEPTION 'the link changed concurrently' USING ERRCODE = '23514';
        END IF;
    ELSIF NEW.revision <> 1 THEN
        RAISE EXCEPTION 'the link changed concurrently' USING ERRCODE = '23514';
    END IF;
    NEW.updated_at := clock_timestamp();
    IF (SELECT k.currency FROM public.books k WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.book_a)
        IS DISTINCT FROM (SELECT k.currency FROM public.books k WHERE k.tenant_id = NEW.tenant_id AND k.id = NEW.book_b) THEN
        RAISE EXCEPTION 'Linked Books keep the same currency.' USING ERRCODE = 'VP020';
    END IF;
    IF public.book_profile(NEW.tenant_id, NEW.book_a) IS NULL OR public.book_profile(NEW.tenant_id, NEW.book_b) IS NULL THEN
        RAISE EXCEPTION 'Choose Personal or Business for both Books before linking them.' USING ERRCODE = 'VP020';
    END IF;
    IF public.book_profile(NEW.tenant_id, NEW.book_a) = 'personal' AND public.book_profile(NEW.tenant_id, NEW.book_b) = 'personal' THEN
        RAISE EXCEPTION 'Two personal Books aren''t linked; link a personal Book to a business.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION public.book_link_terms_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE lk public.book_links%ROWTYPE; pa public.accounts%ROWTYPE; ba public.accounts%ROWTYPE; pp text; bp text;
BEGIN
    IF NOT public.book_link_owner(CASE WHEN TG_OP = 'DELETE' THEN OLD.tenant_id ELSE NEW.tenant_id END)
        AND NOT (TG_OP = 'UPDATE' AND public.link_owner_narrowing(NEW.tenant_id) AND NOT (NEW.active AND NOT OLD.active)
            AND ROW(NEW.tenant_id, NEW.link_id, NEW.payer_book_id, NEW.beneficiary_book_id, NEW.treatment, NEW.payer_account_id, NEW.beneficiary_account_id)
                IS NOT DISTINCT FROM ROW(OLD.tenant_id, OLD.link_id, OLD.payer_book_id, OLD.beneficiary_book_id, OLD.treatment, OLD.payer_account_id, OLD.beneficiary_account_id)) THEN
        RAISE EXCEPTION 'only the owner links Books' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN
        -- Terms go only with their link, and only while no pair names either direction.
        IF EXISTS (SELECT 1 FROM public.entry_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND p.payer_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id)
                AND p.beneficiary_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id))
            OR EXISTS (SELECT 1 FROM public.proposal_pairs p WHERE p.tenant_id = OLD.tenant_id
                AND p.payer_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id)
                AND p.beneficiary_book_id IN (OLD.payer_book_id, OLD.beneficiary_book_id)) THEN
            RAISE EXCEPTION 'These Books have pairs between them; turn the link off instead.' USING ERRCODE = 'VP020';
        END IF;
        RETURN OLD;
    END IF;
    IF NOT public.ledger_import_owner() AND NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
    END IF;
    NEW.updated_at := clock_timestamp();
    SELECT * INTO lk FROM public.book_links WHERE tenant_id = NEW.tenant_id AND id = NEW.link_id;
    IF NOT ((NEW.payer_book_id, NEW.beneficiary_book_id) IN ((lk.book_a, lk.book_b), (lk.book_b, lk.book_a))) THEN
        RAISE EXCEPTION 'a direction is between the link''s two Books' USING ERRCODE = '23514';
    END IF;
    pp := public.book_profile(NEW.tenant_id, NEW.payer_book_id);
    bp := public.book_profile(NEW.tenant_id, NEW.beneficiary_book_id);
    SELECT * INTO pa FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.payer_book_id AND id = NEW.payer_account_id;
    SELECT * INTO ba FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.beneficiary_book_id AND id = NEW.beneficiary_account_id;
    IF pa.status = 'retired' OR pa.heading OR ba.status = 'retired' OR ba.heading THEN
        RAISE EXCEPTION 'A link uses accounts that take postings.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'contribution' AND NOT (pp = 'personal' AND bp = 'business') THEN
        RAISE EXCEPTION 'Money put in is only from a personal Book to a business.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'owner_equity' AND NOT (pp = 'business' AND bp = 'personal') THEN
        RAISE EXCEPTION 'A draw or distribution is only from a business to a personal Book.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'contribution' AND (pa.class <> 'asset' OR pa.role IS DISTINCT FROM 'investment' OR ba.class <> 'equity') THEN
        RAISE EXCEPTION 'Money put in is recorded to an investment asset in the personal Book and an equity account in the business.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'owner_equity' AND (pa.class <> 'equity' OR ba.class <> 'asset' OR ba.role IS DISTINCT FROM 'investment') THEN
        RAISE EXCEPTION 'A draw or distribution is recorded to an equity account in the business and an investment asset in the personal Book.' USING ERRCODE = 'VP020';
    END IF;
    IF NEW.treatment = 'owed' AND (pa.class <> 'asset' OR pa.role IS DISTINCT FROM 'receivable' OR ba.class <> 'liability' OR ba.role IS DISTINCT FROM 'payable') THEN
        RAISE EXCEPTION 'Money owed back is recorded to a receivable in the Book that paid and a payable in the Book it paid for.' USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = NEW.tenant_id AND NOT (x.link_id = NEW.link_id AND x.payer_book_id = NEW.payer_book_id)
            AND (x.payer_book_id = NEW.payer_book_id AND x.payer_account_id = NEW.payer_account_id
                OR x.beneficiary_book_id = NEW.payer_book_id AND x.beneficiary_account_id = NEW.payer_account_id
                OR x.payer_book_id = NEW.beneficiary_book_id AND x.payer_account_id = NEW.beneficiary_account_id
                OR x.beneficiary_book_id = NEW.beneficiary_book_id AND x.beneficiary_account_id = NEW.beneficiary_account_id)) THEN
        RAISE EXCEPTION 'Each account carries one direction of one link, so its balance is that link''s; choose or add another account.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NEW;
END $$;

CREATE POLICY owner_narrow ON public.book_links FOR UPDATE USING (public.link_owner_narrowing(tenant_id)) WITH CHECK (public.link_owner_narrowing(tenant_id));
CREATE POLICY owner_narrow ON public.book_link_terms FOR UPDATE USING (public.link_owner_narrowing(tenant_id)) WITH CHECK (public.link_owner_narrowing(tenant_id));


-- An opening balance is repaid like a cost: a settlement may name it in
-- repays, and an unnamed payment repays it, oldest first, with the costs.
DROP FUNCTION public.settlement_check(uuid, uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid);
CREATE FUNCTION public.settlement_check(t uuid, debtor uuid, debtor_account uuid, debtor_item uuid, creditor uuid, creditor_account uuid, creditor_item uuid,
        repays jsonb, leaving_out uuid) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.bank_transactions%ROWTYPE; c public.bank_transactions%ROWTYPE; dclass text; cclass text; owed bigint; total bigint := 0;
    k text; entry uuid; entry_book uuid; cost bigint; amount bigint; excess bigint; repaying bigint; costs uuid[] := '{}'; items jsonb := '[]'; rev public.book_link_terms%ROWTYPE;
    required boolean; debtor_name text; creditor_name text; r record;
BEGIN
    SELECT * INTO d FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = debtor AND x.id = debtor_item;
    SELECT * INTO c FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = creditor AND x.id = creditor_item;
    IF d.id IS NULL OR c.id IS NULL THEN
        RAISE EXCEPTION 'A settlement is two bank items, one in each linked Book.' USING ERRCODE = 'VP020';
    END IF;
    debtor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = debtor);
    creditor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = creditor);
    dclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = debtor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = debtor AND y.id = d.bank_account_id), d.account_id));
    cclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = creditor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = creditor AND y.id = c.bank_account_id), c.account_id));
    IF NOT (dclass = 'asset' AND d.amount_minor < 0 OR dclass = 'liability' AND d.amount_minor > 0)
        OR NOT (cclass = 'asset' AND c.amount_minor > 0 OR cclass = 'liability' AND c.amount_minor < 0) THEN
        RAISE EXCEPTION 'A settlement is money paid out of the Book that owes and into the Book that is owed; pick the payment in %, and its arrival in %.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    amount := abs(d.amount_minor);
    IF abs(c.amount_minor) <> amount OR c.currency IS DISTINCT FROM d.currency OR abs(c.occurred_on - d.occurred_on) > 5 THEN
        RAISE EXCEPTION 'A settlement''s two bank items are the same amount, at most five days apart; these are % and %, on % and %.',
            public.book_amount(t, debtor, amount), public.book_amount(t, creditor, abs(c.amount_minor)), d.occurred_on, c.occurred_on USING ERRCODE = 'VP020';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed' AND x.payer_book_id = creditor AND x.payer_account_id = creditor_account
                AND x.beneficiary_book_id = debtor AND x.beneficiary_account_id = debtor_account) THEN
        RAISE EXCEPTION 'Nothing is owed this way: % pays % only for what an active owed direction names. Set "% pays for %" to owed back in Links.',
            debtor_name, creditor_name, creditor_name, debtor_name USING ERRCODE = 'VP020';
    END IF;
    rev := public.active_link_terms(t, debtor, creditor);
    SELECT coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO owed
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.account_id = debtor_account AND x.entry_id IS DISTINCT FROM leaving_out;
    excess := greatest(amount - greatest(owed, 0), 0);
    repaying := amount - excess;
    IF excess > 0 AND rev.link_id IS NULL THEN
        RAISE EXCEPTION 'This pays % more than % owes (%). Set "% pays for %" in Links, and the extra is recorded there.',
            public.book_amount(t, debtor, excess), debtor_name, public.book_amount(t, debtor, greatest(owed, 0)), debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    IF jsonb_typeof(coalesce(repays, '[]'::jsonb)) <> 'array' THEN
        RAISE EXCEPTION 'repays is a list of pair keys.' USING ERRCODE = 'VP020';
    END IF;
    required := public.book_profile(t, creditor) = 'personal' AND public.book_profile(t, debtor) = 'business' AND rev.treatment IS DISTINCT FROM 'owner_equity';
    FOR k IN SELECT jsonb_array_elements_text(coalesce(repays, '[]'::jsonb)) LOOP
        entry := NULL;
        -- A cost the creditor paid for the debtor, or the debtor's opening
        -- balance owed to the creditor; each is kept by its entry in the
        -- pair's beneficiary Book.
        SELECT p.beneficiary_book_id, p.beneficiary_entry_id INTO entry_book, entry FROM public.entry_pairs p
            WHERE p.tenant_id = t AND p.pair_key = k
                AND (p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
                    OR p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor)
                AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id);
        IF entry IS NULL THEN
            RAISE EXCEPTION 'repays names %, which isn''t a cost % paid for % or an opening balance % owes; name one from the open items.', k, creditor_name, debtor_name, debtor_name USING ERRCODE = 'VP020';
        END IF;
        IF entry = ANY (costs) THEN
            RAISE EXCEPTION 'repays names % twice; name each cost once.', k USING ERRCODE = 'VP020';
        END IF;
        IF public.cost_repaid_by(t, entry_book, entry, leaving_out) IS NOT NULL THEN
            RAISE EXCEPTION 'The cost % is already repaid; name an open cost instead.', k USING ERRCODE = 'VP020';
        END IF;
        costs := costs || entry;
        items := items || jsonb_build_array(jsonb_build_object('book', entry_book, 'entry', entry));
        total := total + (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = entry_book AND x.entry_id = entry AND x.side = 'credit');
    END LOOP;
    IF cardinality(costs) = 0 AND required THEN
        RAISE EXCEPTION '% repaying % names what it repays (repays: costs, or the opening balance), adding up to %; pick them from the open items.',
            debtor_name, creditor_name, public.book_amount(t, debtor, repaying) USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) > 0 AND total <> repaying THEN
        RAISE EXCEPTION 'The costs named add up to %, but this payment repays %; name costs that add up to it exactly.',
            public.book_amount(t, debtor, total), public.book_amount(t, debtor, repaying) USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) = 0 THEN
        -- Unnamed: the oldest open costs the payment covers whole.
        FOR r IN SELECT p.beneficiary_book_id AS book, p.beneficiary_entry_id AS entry,
                (SELECT sum(x.amount_minor) FROM public.lines x WHERE x.tenant_id = t AND x.book_id = p.beneficiary_book_id AND x.entry_id = p.beneficiary_entry_id AND x.side = 'credit') AS amount
            FROM public.entry_pairs p JOIN public.entries e ON e.tenant_id = t AND e.book_id = p.beneficiary_book_id AND e.id = p.beneficiary_entry_id
            WHERE p.tenant_id = t
                AND (p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
                    OR p.kind = 'opening' AND p.payer_book_id = debtor AND p.beneficiary_book_id = creditor)
                AND NOT public.entry_reversed(t, p.beneficiary_book_id, p.beneficiary_entry_id)
                AND public.cost_repaid_by(t, p.beneficiary_book_id, p.beneficiary_entry_id, leaving_out) IS NULL
            ORDER BY e.accounting_date, e.sequence LOOP
            EXIT WHEN total + r.amount > repaying;
            costs := costs || r.entry;
            items := items || jsonb_build_array(jsonb_build_object('book', r.book, 'entry', r.entry));
            total := total + r.amount;
        END LOOP;
    END IF;
    RETURN jsonb_build_object('costs', items, 'excess', excess, 'repaying', repaying, 'required', required);
END $$;

-- Records a settlement the owner decided, or recorded at once: each entry
-- records its own bank item, on its date, to the owed direction's account
-- for what was owed and, for any excess, to the reverse direction's
-- account. The costs it repays are kept.
CREATE OR REPLACE FUNCTION public.settle_pair(t uuid, key text, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid, repays jsonb) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE ditem uuid; dbank uuid; citem uuid; cbank uuid; owed public.book_link_terms%ROWTYPE; rev public.book_link_terms%ROWTYPE;
    checked jsonb; item jsonb; excess bigint; repaying bigint; amount bigint; bad int;
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    SELECT l.transaction_id, l.account_id INTO ditem, dbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = debtor AND l.entry_id = debtor_entry AND l.revoked_at IS NULL;
    SELECT l.transaction_id, l.account_id INTO citem, cbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = creditor AND l.entry_id = creditor_entry AND l.revoked_at IS NULL;
    owed := public.active_link_terms(t, creditor, debtor);
    rev := public.active_link_terms(t, debtor, creditor);
    IF ditem IS NULL OR citem IS NULL OR owed.treatment IS DISTINCT FROM 'owed' THEN
        RAISE EXCEPTION 'Each entry of a settlement records its own bank item, between Books an owed direction links.' USING ERRCODE = 'VP020';
    END IF;
    checked := public.settlement_check(t, debtor, owed.beneficiary_account_id, ditem, creditor, owed.payer_account_id, citem, repays, debtor_entry);
    excess := (checked ->> 'excess')::bigint;
    repaying := (checked ->> 'repaying')::bigint;
    amount := excess + repaying;
    -- The debtor's entry: the bank account for the whole amount, the payable
    -- for what was owed, the reverse direction's account for the excess.
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = dbank AND x.amount_minor = amount
            OR x.account_id = owed.beneficiary_account_id AND x.side = 'debit' AND x.amount_minor = repaying
            OR x.account_id = rev.payer_account_id AND x.side = 'debit' AND x.amount_minor = excess, false))
        + abs(count(*) - (1 + (repaying > 0)::int + (excess > 0)::int)) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The paying Book''s entry records % to what it owed and % as the extra; record it that way.',
            public.book_amount(t, debtor, repaying), public.book_amount(t, debtor, excess) USING ERRCODE = 'VP020';
    END IF;
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = cbank AND x.amount_minor = amount
            OR x.account_id = owed.payer_account_id AND x.side = 'credit' AND x.amount_minor = repaying
            OR x.account_id = rev.beneficiary_account_id AND x.side = 'credit' AND x.amount_minor = excess, false))
        + abs(count(*) - (1 + (repaying > 0)::int + (excess > 0)::int)) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = creditor AND x.entry_id = creditor_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The paid Book''s entry records % to what it was owed and % as the extra; record it that way.',
            public.book_amount(t, creditor, repaying), public.book_amount(t, creditor, excess) USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.entries x JOIN public.bank_transactions i ON i.tenant_id = x.tenant_id AND i.book_id = x.book_id
                AND i.id = CASE WHEN x.book_id = debtor THEN ditem ELSE citem END
            WHERE x.tenant_id = t AND (x.book_id = debtor AND x.id = debtor_entry OR x.book_id = creditor AND x.id = creditor_entry)
                AND (x.accounting_date <> i.occurred_on OR x.reversal_of IS NOT NULL)) THEN
        RAISE EXCEPTION 'Each entry of a settlement is dated its own bank item''s date.' USING ERRCODE = 'VP020';
    END IF;
    PERFORM set_config('vult.settling', key, true);
    INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id)
        VALUES (t, key, 'settlement', debtor, debtor_entry, creditor, creditor_entry);
    PERFORM set_config('vult.settling', '', true);
    FOR item IN SELECT jsonb_array_elements(checked -> 'costs') LOOP
        INSERT INTO public.settlement_repays(tenant_id, settlement_book_id, settlement_entry_id, cost_book_id, cost_entry_id)
            VALUES (t, debtor, debtor_entry, (item ->> 'book')::uuid, (item ->> 'entry')::uuid);
    END LOOP;
END $$;

REVOKE ALL ON FUNCTION public.settlement_check(uuid, uuid, uuid, uuid, uuid, uuid, uuid, jsonb, uuid) FROM PUBLIC;

-- An opening balance a settlement repays is undone only after it.
CREATE OR REPLACE FUNCTION public.entry_pair_of(t uuid, b uuid, e uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT CASE WHEN NOT (public.ledger_allowed(t, b, false) OR access.owner_allowed(t)) THEN NULL ELSE (
    SELECT jsonb_build_object('pair_key', x.pair_key, 'kind', x.kind,
        'book_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END,
        'book_name', (SELECT k.name FROM public.books k WHERE k.tenant_id = t AND k.id = CASE WHEN x.payer_book_id = b THEN x.beneficiary_book_id ELSE x.payer_book_id END),
        'entry_id', CASE WHEN x.payer_book_id = b THEN x.beneficiary_entry_id ELSE x.payer_entry_id END,
        'side', CASE WHEN x.payer_book_id = b THEN 'beneficiary' ELSE 'payer' END,
        'repaid_by', CASE WHEN x.kind IN ('cost', 'opening') THEN public.cost_repaid_by(t, x.beneficiary_book_id, x.beneficiary_entry_id, NULL) END)
    FROM public.entry_pairs x WHERE x.tenant_id = t
        AND (x.payer_book_id = b AND x.payer_entry_id = e OR x.beneficiary_book_id = b AND x.beneficiary_entry_id = e)) END
$$;

-- ------------------------------------------------------------------------
-- 0017 book_archive_and_erasure
-- ------------------------------------------------------------------------

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

-- ------------------------------------------------------------------------
-- 0018 mercury_connections
-- ------------------------------------------------------------------------

-- Banks connected directly, not through Plaid: Mercury, read with the
-- owner's own read-only token. The first sync's checks that each account's
-- activity adds up to the balance the bank reports are kept, so the owner
-- sees what was found.
ALTER TABLE public.bank_connections DROP CONSTRAINT bank_connections_provider_check;
ALTER TABLE public.bank_connections ADD CONSTRAINT bank_connections_provider_check CHECK (provider IN ('plaid', 'mercury'));
ALTER TABLE public.bank_connections ADD COLUMN first_sync_checks jsonb
    CHECK (first_sync_checks IS NULL OR jsonb_typeof(first_sync_checks) = 'array' AND octet_length(first_sync_checks::text) <= 65536);

-- ------------------------------------------------------------------------
-- 0019 bank_account_takeovers
-- ------------------------------------------------------------------------

-- A bank may open new accounts to take over old ones, as when it moves its
-- customers to a new partner bank. The sync records which new account the
-- bank says takes over which old one, and which accounts closed. The owner
-- confirms a takeover once by linking the new account to the old one's Book
-- account: the old account is marked as taken over, and the two share that
-- Book account. Money moving from one to the other then leaves and reaches
-- the same Book account, so it is recorded as moved within the account,
-- with no entry. A closed account stops syncing without needing the owner.

ALTER TABLE public.bank_accounts
    ADD COLUMN replaces uuid,
    ADD COLUMN replaces_reason text NOT NULL DEFAULT '' CHECK (length(replaces_reason) <= 1000),
    ADD COLUMN replaced_by uuid,
    ADD COLUMN closed_at timestamptz,
    ADD CONSTRAINT bank_accounts_replaces_other CHECK (replaces IS DISTINCT FROM id AND replaced_by IS DISTINCT FROM id),
    ADD CONSTRAINT bank_accounts_replaces_fkey FOREIGN KEY (tenant_id, book_id, replaces) REFERENCES public.bank_accounts(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    ADD CONSTRAINT bank_accounts_replaced_by_fkey FOREIGN KEY (tenant_id, book_id, replaced_by) REFERENCES public.bank_accounts(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED;

-- One bank account per Book account, except an account the bank's new
-- account took over: it keeps its Book account for its own history.
DROP INDEX public.bank_accounts_book_account;
CREATE UNIQUE INDEX bank_accounts_book_account ON public.bank_accounts(tenant_id, book_id, account_id)
    WHERE account_id IS NOT NULL AND replaced_by IS NULL;

-- The sync may say once that an account takes over another or closed; only
-- the owner confirms a takeover.
CREATE OR REPLACE FUNCTION public.bank_account_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE target public.accounts%ROWTYPE; book_currency text; book_scale integer;
BEGIN
    IF public.ledger_import_owner() THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'bank accounts are kept with their history' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' AND ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.connection_id, NEW.provider_account_id, NEW.currency, NEW.scale)
        IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.connection_id, OLD.provider_account_id, OLD.currency, OLD.scale) OR NEW.kind <> OLD.kind THEN
        RAISE EXCEPTION 'a bank account keeps its identity and currency' USING ERRCODE = '23514';
    END IF;
    -- The sync records what the bank says: that a new account takes over an
    -- old one, and that an account closed. Each is said once.
    IF TG_OP = 'UPDATE' AND (OLD.replaces IS NOT NULL AND NEW.replaces IS DISTINCT FROM OLD.replaces
            OR OLD.replaces_reason <> '' AND NEW.replaces_reason <> OLD.replaces_reason
            OR OLD.closed_at IS NOT NULL AND NEW.closed_at IS DISTINCT FROM OLD.closed_at) THEN
        RAISE EXCEPTION 'a bank says once that an account takes over another, or closed' USING ERRCODE = '23514';
    END IF;
    IF NEW.replaces IS NOT NULL AND (TG_OP = 'INSERT' OR OLD.replaces IS NULL) AND NOT EXISTS (SELECT 1 FROM public.bank_accounts o
            WHERE o.tenant_id = NEW.tenant_id AND o.book_id = NEW.book_id AND o.id = NEW.replaces
            AND o.kind = NEW.kind AND o.currency = NEW.currency) THEN
        RAISE EXCEPTION 'an account takes over one of its own kind and currency' USING ERRCODE = '23514';
    END IF;
    -- Only the owner confirms, once, that a new account took over this one.
    -- The two then share this account's Book account.
    IF TG_OP = 'INSERT' AND NEW.replaced_by IS NOT NULL OR TG_OP = 'UPDATE' AND NEW.replaced_by IS DISTINCT FROM OLD.replaced_by THEN
        IF NOT public.bank_owner_allowed(NEW.tenant_id, NEW.book_id) OR TG_OP = 'INSERT' OR OLD.replaced_by IS NOT NULL
            OR NOT EXISTS (SELECT 1 FROM public.bank_accounts n WHERE n.tenant_id = NEW.tenant_id AND n.book_id = NEW.book_id
                AND n.id = NEW.replaced_by AND n.replaces = NEW.id) THEN
            RAISE EXCEPTION 'only the owner confirms that the bank''s new account takes over this one' USING ERRCODE = '42501';
        END IF;
        PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    END IF;
    IF (TG_OP = 'INSERT' AND NEW.account_id IS NOT NULL OR TG_OP = 'UPDATE' AND NEW.account_id IS DISTINCT FROM OLD.account_id)
        AND NOT public.bank_owner_allowed(NEW.tenant_id, NEW.book_id) THEN
        RAISE EXCEPTION 'only the owner links a bank account to the Book' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.account_id IS DISTINCT FROM OLD.account_id THEN
        -- Linking changes what the Book's records mean; it waits for writers.
        PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    END IF;
    IF NEW.account_id IS NOT NULL AND (TG_OP = 'INSERT' OR NEW.account_id IS DISTINCT FROM OLD.account_id) THEN
        IF NEW.linked_by::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.linked_at := clock_timestamp();
        SELECT * INTO target FROM public.accounts WHERE tenant_id = NEW.tenant_id AND book_id = NEW.book_id AND id = NEW.account_id;
        SELECT currency, scale INTO book_currency, book_scale FROM public.books WHERE tenant_id = NEW.tenant_id AND id = NEW.book_id;
        IF NOT (NEW.kind = 'cash' AND target.class = 'asset' OR NEW.kind = 'card' AND target.class = 'liability') THEN
            RAISE EXCEPTION 'a cash account is linked to an asset account and a card account to a liability account' USING ERRCODE = '23514';
        END IF;
        IF coalesce(target.currency, book_currency) <> NEW.currency THEN
            RAISE EXCEPTION 'a bank account is linked to a Book account in its own currency' USING ERRCODE = '23514';
        END IF;
        IF target.status = 'retired' OR target.heading THEN
            RAISE EXCEPTION 'a bank account is linked to an account that takes postings' USING ERRCODE = '23514';
        END IF;
        -- Changing the link of an account with recorded activity would leave
        -- those records pointing at the wrong account.
        IF TG_OP = 'UPDATE' AND OLD.account_id IS NOT NULL AND EXISTS (SELECT 1 FROM public.bank_transaction_links l
                WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.bank_account_id = NEW.id AND l.revoked_at IS NULL) THEN
            RAISE EXCEPTION 'unlink the recorded activity before linking another Book account' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_OP = 'UPDATE' AND NEW.account_id IS NULL AND OLD.account_id IS NOT NULL AND EXISTS (SELECT 1 FROM public.bank_transaction_links l
            WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.bank_account_id = NEW.id AND l.revoked_at IS NULL) THEN
        RAISE EXCEPTION 'unlink the recorded activity before unlinking the account' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;


-- Activity recorded as moved within its Book account isn't linked again.
CREATE OR REPLACE FUNCTION public.bank_link_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE observed record;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'a link is revoked with a reason, never deleted' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF OLD.revoked_at IS NOT NULL OR NEW.revoked_at IS NULL
            OR (to_jsonb(NEW) - ARRAY['revoked_by', 'revoke_reason', 'revoke_receipt_id', 'revoked_at'])
                IS DISTINCT FROM (to_jsonb(OLD) - ARRAY['revoked_by', 'revoke_reason', 'revoke_receipt_id', 'revoked_at']) THEN
            RAISE EXCEPTION 'a link changes only by being revoked once' USING ERRCODE = '23514';
        END IF;
        IF NOT public.ledger_import_owner() THEN
            IF NEW.revoked_by::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
                RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
            END IF;
            NEW.revoked_at := clock_timestamp();
        END IF;
        RETURN NEW;
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.recorded_at := clock_timestamp();
        IF NEW.revoked_at IS NOT NULL THEN
            RAISE EXCEPTION 'a new link is live' USING ERRCODE = '23514';
        END IF;
    END IF;
    SELECT t.pending, t.removed_at, t.bank_account_id, coalesce(a.account_id, t.account_id) AS account_id INTO STRICT observed
        FROM public.bank_transactions t LEFT JOIN public.bank_accounts a
        ON a.tenant_id = t.tenant_id AND a.book_id = t.book_id AND a.id = t.bank_account_id
        WHERE t.tenant_id = NEW.tenant_id AND t.book_id = NEW.book_id AND t.id = NEW.transaction_id;
    IF observed.bank_account_id IS DISTINCT FROM NEW.bank_account_id OR observed.account_id IS DISTINCT FROM NEW.account_id THEN
        RAISE EXCEPTION 'a link names the transaction''s own accounts' USING ERRCODE = '23514';
    END IF;
    IF observed.pending OR observed.removed_at IS NOT NULL THEN
        RAISE EXCEPTION 'pending or removed bank activity is not recorded' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (SELECT 1 FROM public.bank_transaction_duplicates d WHERE d.tenant_id = NEW.tenant_id AND d.book_id = NEW.book_id
            AND d.transaction_id = NEW.transaction_id) THEN
        RAISE EXCEPTION 'a transaction marked as a repeat is not recorded' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (SELECT 1 FROM public.bank_transaction_moves m WHERE m.tenant_id = NEW.tenant_id AND m.book_id = NEW.book_id
            AND m.transaction_id = NEW.transaction_id) THEN
        RAISE EXCEPTION 'this item is recorded as money moved within its Book account' USING ERRCODE = '23514';
    END IF;
    IF observed.account_id IS NULL THEN
        RAISE EXCEPTION 'link the bank account to a Book account first' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;


-- Money moved between two bank accounts that share a Book account: both
-- items are recorded together, with no entry, since the Book account's
-- balance doesn't change. One row per item names the other.
CREATE TABLE public.bank_transaction_moves (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    transaction_id uuid NOT NULL,
    other_transaction_id uuid NOT NULL,
    actor_id uuid NOT NULL,
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 4000),
    receipt_id uuid NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, transaction_id),
    FOREIGN KEY (tenant_id, book_id, transaction_id) REFERENCES public.bank_transactions(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, other_transaction_id) REFERENCES public.bank_transactions(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, receipt_id) REFERENCES public.requests(tenant_id, book_id, id)
        DEFERRABLE INITIALLY DEFERRED,
    CHECK (transaction_id <> other_transaction_id)
);

CREATE FUNCTION public.bank_move_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE a record; b record;
BEGIN
    PERFORM public.ledger_lock(NEW.tenant_id, NEW.book_id);
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        NEW.recorded_at := clock_timestamp();
    END IF;
    SELECT t.amount_minor, t.currency, t.pending, t.removed_at, t.bank_account_id, coalesce(x.account_id, t.account_id) AS account INTO STRICT a
        FROM public.bank_transactions t LEFT JOIN public.bank_accounts x ON x.tenant_id = t.tenant_id AND x.book_id = t.book_id AND x.id = t.bank_account_id
        WHERE t.tenant_id = NEW.tenant_id AND t.book_id = NEW.book_id AND t.id = NEW.transaction_id;
    SELECT t.amount_minor, t.currency, t.pending, t.removed_at, t.bank_account_id, coalesce(x.account_id, t.account_id) AS account INTO STRICT b
        FROM public.bank_transactions t LEFT JOIN public.bank_accounts x ON x.tenant_id = t.tenant_id AND x.book_id = t.book_id AND x.id = t.bank_account_id
        WHERE t.tenant_id = NEW.tenant_id AND t.book_id = NEW.book_id AND t.id = NEW.other_transaction_id;
    IF a.account IS NULL OR a.account IS DISTINCT FROM b.account OR a.bank_account_id IS NULL OR b.bank_account_id IS NULL
        OR a.bank_account_id = b.bank_account_id OR a.currency <> b.currency OR a.amount_minor = 0 OR a.amount_minor <> -b.amount_minor THEN
        RAISE EXCEPTION 'money moved within a Book account leaves one of its bank accounts and reaches another, in the same amount' USING ERRCODE = '23514';
    END IF;
    IF a.pending OR b.pending OR a.removed_at IS NOT NULL OR b.removed_at IS NOT NULL THEN
        RAISE EXCEPTION 'pending or removed bank activity is not recorded' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (SELECT 1 FROM public.bank_transaction_links l WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id
            AND l.transaction_id IN (NEW.transaction_id, NEW.other_transaction_id) AND l.revoked_at IS NULL)
        OR EXISTS (SELECT 1 FROM public.bank_transaction_duplicates d WHERE d.tenant_id = NEW.tenant_id AND d.book_id = NEW.book_id
            AND d.transaction_id IN (NEW.transaction_id, NEW.other_transaction_id)) THEN
        RAISE EXCEPTION 'a recorded transaction, or one marked as a repeat, is not recorded again' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER bank_move_guard BEFORE INSERT ON public.bank_transaction_moves
    FOR EACH ROW EXECUTE FUNCTION public.bank_move_guard();
CREATE TRIGGER bank_move_immutable BEFORE UPDATE OR DELETE ON public.bank_transaction_moves
    FOR EACH ROW EXECUTE FUNCTION public.ledger_immutable();
CREATE CONSTRAINT TRIGGER bank_move_receipt AFTER INSERT ON public.bank_transaction_moves DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.foundation_receipt('record_bank_transfer');

-- At commit: both items of a move are recorded, each naming the other.
CREATE FUNCTION public.bank_move_pair() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF public.ledger_restoring(NEW.tenant_id, NEW.book_id) THEN
        RETURN NULL;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.bank_transaction_moves m WHERE m.tenant_id = NEW.tenant_id AND m.book_id = NEW.book_id
            AND m.transaction_id = NEW.other_transaction_id AND m.other_transaction_id = NEW.transaction_id) THEN
        RAISE EXCEPTION 'money moved within a Book account is recorded for both its items' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER bank_move_pair AFTER INSERT ON public.bank_transaction_moves DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION public.bank_move_pair();
REVOKE ALL ON FUNCTION public.bank_move_pair() FROM PUBLIC;

ALTER TABLE public.bank_transaction_moves ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bank_transaction_moves FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.bank_transaction_moves FROM PUBLIC;
-- Moves follow connection levels like links.
CREATE POLICY bank_read ON public.bank_transaction_moves FOR SELECT USING (public.bank_read_allowed(tenant_id, book_id));
CREATE POLICY scoped_insert ON public.bank_transaction_moves FOR INSERT WITH CHECK (public.ledger_allowed(tenant_id, book_id, true));
CREATE TRIGGER proposal_record_only AFTER INSERT OR UPDATE ON public.bank_transaction_moves
    FOR EACH ROW EXECUTE FUNCTION public.ledger_proposal_record_only();
CREATE TRIGGER book_changed AFTER INSERT ON public.bank_transaction_moves FOR EACH ROW EXECUTE FUNCTION public.book_changed('bank');
-- Like every table holding a Book's rows, moves wait while the Book is
-- being deleted.
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.bank_transaction_moves
    FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('book_id');

-- ------------------------------------------------------------------------
-- 0020 document_checks
-- ------------------------------------------------------------------------

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

-- ------------------------------------------------------------------------
-- 0021 vult_agent_key
-- ------------------------------------------------------------------------

-- The built-in agent's model and key, once for the whole Vult: every Book
-- runs its agent with them, as Books share the Vult's bank provider keys.
-- Each Book keeps its own on/off switch, its connection, its limits and
-- its skills result. A Book row whose provider is 'vult' uses these.
CREATE TABLE public.vult_agent_settings (
    tenant_id uuid PRIMARY KEY REFERENCES public.tenants(id),
    provider text NOT NULL CHECK (provider IN ('openai_compatible', 'anthropic')),
    base_url text NOT NULL CHECK (length(base_url) <= 512 AND base_url ~ '^https?://[^[:space:]]+$'),
    model text NOT NULL CHECK (length(model) BETWEEN 1 AND 200),
    -- The owner's API key, sealed by the application with a key that never
    -- enters this database. None for a model on this computer.
    key_sealed bytea CHECK (octet_length(key_sealed) BETWEEN 34 AND 4096),
    key_version integer CHECK (key_version > 0),
    -- A key carried forward from one Book's settings is still sealed for
    -- that Book; the server seals it for the Vult the next time it opens it.
    sealed_for uuid,
    -- Whether the provider accepts the key, as the agent last found:
    -- unchecked until it's used, working, or rejected with why (refused,
    -- expired or out of credit). A new key starts unchecked.
    key_state text NOT NULL DEFAULT 'unchecked' CHECK (key_state IN ('unchecked', 'working', 'rejected')),
    key_problem text NOT NULL DEFAULT '' CHECK (key_problem IN ('', 'refused', 'expired', 'out_of_credit')),
    key_checked_at timestamptz,
    -- When the owner says the key expires, for a reminder a week before.
    key_expires_on date,
    revision bigint NOT NULL CHECK (revision > 0),
    actor_id uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((key_sealed IS NULL) = (key_version IS NULL)),
    CHECK (sealed_for IS NULL OR key_sealed IS NOT NULL),
    CHECK ((key_state = 'rejected') = (key_problem <> ''))
);

-- The owner changes the Vult's model and key from a fresh sign-in, never
-- through an agent's connection.
CREATE FUNCTION public.vult_agent_owner_allowed(t uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = ''
    AND coalesce(current_setting('vult.owner_posting', true), '') = ''
    AND access.owner_write_allowed(t)
$$;

CREATE FUNCTION public.vult_agent_settings_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'the Vult''s agent settings are changed, never deleted' USING ERRCODE = '23514';
    END IF;
    -- The server re-seals a carried-forward key for the Vult: only the key
    -- changes, and only from a Book's seal to the Vult's.
    IF TG_OP = 'UPDATE' AND coalesce(current_setting('vult.agent_reseal', true), '') = 'on'
        AND OLD.sealed_for IS NOT NULL AND NEW.sealed_for IS NULL AND NEW.key_sealed IS NOT NULL
        AND (to_jsonb(NEW) - ARRAY['key_sealed', 'key_version', 'sealed_for']) = (to_jsonb(OLD) - ARRAY['key_sealed', 'key_version', 'sealed_for']) THEN
        RETURN NEW;
    END IF;
    -- The agent records whether the provider accepted the key: only those
    -- columns change.
    IF TG_OP = 'UPDATE' AND coalesce(current_setting('vult.agent_key_state', true), '') = 'on'
        AND (to_jsonb(NEW) - ARRAY['key_state', 'key_problem', 'key_checked_at']) = (to_jsonb(OLD) - ARRAY['key_state', 'key_problem', 'key_checked_at']) THEN
        RETURN NEW;
    END IF;
    -- A new key, or a new address, hasn't been tried yet.
    IF TG_OP = 'UPDATE' AND (NEW.key_sealed IS DISTINCT FROM OLD.key_sealed AND NEW.sealed_for IS NOT DISTINCT FROM OLD.sealed_for
        OR NEW.base_url IS DISTINCT FROM OLD.base_url) OR TG_OP = 'INSERT' AND NOT public.ledger_import_owner() THEN
        NEW.key_state := 'unchecked'; NEW.key_problem := ''; NEW.key_checked_at := NULL;
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NOT public.vult_agent_owner_allowed(NEW.tenant_id) THEN
            RAISE EXCEPTION 'only the owner changes the Vult''s agent model and key' USING ERRCODE = '42501';
        END IF;
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
        IF TG_OP = 'UPDATE' AND (NEW.revision <> OLD.revision + 1 OR NEW.tenant_id <> OLD.tenant_id) OR TG_OP = 'INSERT' AND NEW.revision <> 1 THEN
            RAISE EXCEPTION 'the Vult''s agent settings changed concurrently' USING ERRCODE = '23514';
        END IF;
        IF NEW.sealed_for IS NOT NULL AND (TG_OP = 'INSERT' OR NEW.key_sealed IS DISTINCT FROM OLD.key_sealed) THEN
            RAISE EXCEPTION 'a new key is sealed for the Vult' USING ERRCODE = '23514';
        END IF;
        NEW.updated_at := clock_timestamp();
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER vult_agent_settings_guard BEFORE INSERT OR UPDATE OR DELETE ON public.vult_agent_settings
    FOR EACH ROW EXECUTE FUNCTION public.vult_agent_settings_guard();

-- A different model hasn't taken the skills test in any Book.
CREATE FUNCTION public.vult_agent_model_changed() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF TG_OP = 'INSERT' OR ROW(NEW.provider, NEW.base_url, NEW.model) IS DISTINCT FROM ROW(OLD.provider, OLD.base_url, OLD.model) THEN
        PERFORM set_config('vult.agent_skills', 'on', true);
        UPDATE public.agent_settings s SET skills_level = NULL, skills_score = NULL, skills_total = NULL, skills_model = NULL,
            skills_tested_at = NULL, default_trust = 'draft'
            WHERE s.tenant_id = NEW.tenant_id AND s.provider = 'vult' AND s.skills_model IS DISTINCT FROM NEW.model
                AND (s.skills_level IS NOT NULL OR s.default_trust <> 'draft')
                AND NOT EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = s.tenant_id AND d.book_id = s.book_id);
        PERFORM set_config('vult.agent_skills', '', true);
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER vult_agent_model_changed AFTER INSERT OR UPDATE ON public.vult_agent_settings
    FOR EACH ROW EXECUTE FUNCTION public.vult_agent_model_changed();

ALTER TABLE public.vult_agent_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.vult_agent_settings FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.vult_agent_settings FROM PUBLIC;
-- Read only by the owner; agents never read it, sealed or not. The engine
-- reads it through agent_secret_for.
CREATE POLICY owner_read ON public.vult_agent_settings FOR SELECT USING (access.owner_allowed(tenant_id));
CREATE POLICY owner_insert ON public.vult_agent_settings FOR INSERT WITH CHECK (public.vult_agent_owner_allowed(tenant_id));
CREATE POLICY owner_update ON public.vult_agent_settings FOR UPDATE
    USING (public.vult_agent_owner_allowed(tenant_id)) WITH CHECK (public.vult_agent_owner_allowed(tenant_id));

-- A Book's row may now say 'vult': use the Vult's model and key.
DO $$
DECLARE c record;
BEGIN
    FOR c IN SELECT conname FROM pg_constraint WHERE conrelid = 'public.agent_settings'::regclass AND contype = 'c'
        AND pg_get_constraintdef(oid) LIKE '%provider%' LOOP
        EXECUTE format('ALTER TABLE public.agent_settings DROP CONSTRAINT %I', c.conname);
    END LOOP;
END $$;
ALTER TABLE public.agent_settings
    ADD CONSTRAINT agent_settings_provider CHECK (provider IN ('openai_compatible', 'anthropic', 'service', 'vult')),
    ADD CONSTRAINT agent_settings_model CHECK (
        provider IN ('service', 'vult') AND base_url = '' AND model = '' AND key_sealed IS NULL
        OR provider IN ('openai_compatible', 'anthropic') AND base_url ~ '^https?://[^[:space:]]+$' AND length(model) >= 1);

-- Carry each Vult's most recent key forward, with its Book's model, and
-- point every Book at it. The other Books' copies of a key are dropped; a
-- Book being deleted is left as it is. Skills results stay only where the
-- Book had tested the same model.
INSERT INTO public.vult_agent_settings(tenant_id, provider, base_url, model, key_sealed, key_version, sealed_for, revision, actor_id)
SELECT DISTINCT ON (s.tenant_id) s.tenant_id, s.provider, s.base_url, s.model, s.key_sealed, s.key_version,
    CASE WHEN s.key_sealed IS NOT NULL THEN s.book_id END, 1, s.actor_id
    FROM public.agent_settings s WHERE s.provider IN ('openai_compatible', 'anthropic')
    ORDER BY s.tenant_id, s.key_sealed IS NOT NULL DESC, s.updated_at DESC, s.book_id;
UPDATE public.agent_settings s SET provider = 'vult', base_url = '', model = '', key_sealed = NULL, key_version = NULL,
    skills_level = CASE WHEN s.model = v.model THEN s.skills_level END,
    skills_score = CASE WHEN s.model = v.model THEN s.skills_score END,
    skills_total = CASE WHEN s.model = v.model THEN s.skills_total END,
    skills_model = CASE WHEN s.model = v.model THEN s.skills_model END,
    skills_tested_at = CASE WHEN s.model = v.model THEN s.skills_tested_at END,
    default_trust = CASE WHEN s.model = v.model THEN s.default_trust ELSE 'draft' END
    FROM public.vult_agent_settings v
    WHERE v.tenant_id = s.tenant_id AND s.provider IN ('openai_compatible', 'anthropic')
        AND NOT EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = s.tenant_id AND d.book_id = s.book_id);

-- What the engine reads for a Book: the Book's switch and connection, with
-- the Vult's model and still-sealed key when the Book uses them.
CREATE FUNCTION public.agent_secret_for(t uuid, b uuid)
    RETURNS TABLE(provider text, base_url text, model text, key_sealed bytea, key_version integer, enabled boolean,
        connection_id uuid, actor_id uuid, vult_key boolean, sealed_for uuid, key_state text, key_checked_at timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT coalesce(v.provider, s.provider), coalesce(v.base_url, s.base_url), coalesce(v.model, s.model),
    CASE WHEN s.provider = 'vult' THEN v.key_sealed ELSE s.key_sealed END,
    CASE WHEN s.provider = 'vult' THEN v.key_version ELSE s.key_version END,
    s.enabled AND (s.provider <> 'vult' OR v.tenant_id IS NOT NULL), s.connection_id, c.actor_id, s.provider = 'vult', v.sealed_for,
    coalesce(v.key_state, ''), v.key_checked_at
    FROM public.agent_settings s JOIN public.connections c ON c.tenant_id = s.tenant_id AND c.book_id = s.book_id AND c.id = s.connection_id
    LEFT JOIN public.vult_agent_settings v ON s.provider = 'vult' AND v.tenant_id = s.tenant_id
    WHERE s.tenant_id = t AND s.book_id = b
$$;
DROP FUNCTION public.agent_settings_for(uuid, uuid);

-- The Vult's model and still-sealed key alone, for testing the connection
-- from a Book whose agent isn't set up yet.
CREATE FUNCTION public.vult_agent_secret(t uuid)
    RETURNS TABLE(provider text, base_url text, model text, key_sealed bytea, key_version integer, sealed_for uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT v.provider, v.base_url, v.model, v.key_sealed, v.key_version, v.sealed_for FROM public.vult_agent_settings v WHERE v.tenant_id = t
$$;

-- The agent records whether the provider accepted the key it used. A key
-- replaced meanwhile is left alone: the record is about the key it names.
CREATE FUNCTION public.vult_agent_key_checked(t uuid, used bytea, state text, problem text) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE updated integer;
BEGIN
    IF state NOT IN ('working', 'rejected') OR (state = 'rejected') <> (problem IN ('refused', 'expired', 'out_of_credit')) THEN
        RAISE EXCEPTION 'unknown key state' USING ERRCODE = '22023';
    END IF;
    PERFORM set_config('vult.agent_key_state', 'on', true);
    UPDATE public.vult_agent_settings v SET key_state = state, key_problem = CASE WHEN state = 'rejected' THEN problem ELSE '' END, key_checked_at = clock_timestamp()
        WHERE v.tenant_id = t AND v.key_sealed = used
            AND (v.key_state, v.key_problem) IS DISTINCT FROM (state, CASE WHEN state = 'rejected' THEN problem ELSE '' END)
            OR v.tenant_id = t AND v.key_sealed = used AND state = 'rejected' AND v.key_checked_at < clock_timestamp() - interval '1 hour';
    GET DIAGNOSTICS updated = ROW_COUNT;
    PERFORM set_config('vult.agent_key_state', '', true);
    RETURN updated = 1;
END $$;

-- The owner removes the Vult's key: it is cleared, and every Book's agent
-- is turned off until a new key is saved. The owner, from a fresh sign-in,
-- never through an agent; the caller checks the Book revision it was shown.
CREATE FUNCTION public.vult_agent_remove_key(t uuid, who uuid) RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE turned_off integer := 0; selected text; b uuid;
BEGIN
    IF NOT public.vult_agent_owner_allowed(t) OR who::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
        RAISE EXCEPTION 'only the owner removes the Vult''s agent key' USING ERRCODE = '42501';
    END IF;
    UPDATE public.vult_agent_settings v SET key_sealed = NULL, key_version = NULL, sealed_for = NULL, key_state = 'unchecked',
        key_problem = '', key_checked_at = NULL, key_expires_on = NULL, revision = revision + 1, actor_id = who
        WHERE v.tenant_id = t AND v.key_sealed IS NOT NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'This Vult keeps no AI key.' USING ERRCODE = '23514';
    END IF;
    -- Each Book's own guard checks the change in that Book.
    selected := current_setting('vult.book_id', true);
    FOR b IN SELECT s.book_id FROM public.agent_settings s
        WHERE s.tenant_id = t AND s.provider = 'vult' AND s.enabled
            AND NOT EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = s.tenant_id AND d.book_id = s.book_id)
        ORDER BY s.book_id FOR UPDATE LOOP
        PERFORM set_config('vult.book_id', b::text, true);
        UPDATE public.agent_settings s SET enabled = false, revision = revision + 1, actor_id = who WHERE s.tenant_id = t AND s.book_id = b;
        turned_off := turned_off + 1;
    END LOOP;
    PERFORM set_config('vult.book_id', coalesce(selected, ''), true);
    RETURN turned_off;
END $$;

-- The server seals a carried-forward key for the Vult. It changes the key
-- only when it is still the one it opened.
CREATE FUNCTION public.vult_agent_reseal(t uuid, opened bytea, sealed bytea, version integer) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE updated integer;
BEGIN
    PERFORM set_config('vult.agent_reseal', 'on', true);
    UPDATE public.vult_agent_settings v SET key_sealed = sealed, key_version = version, sealed_for = NULL
        WHERE v.tenant_id = t AND v.sealed_for IS NOT NULL AND v.key_sealed = opened;
    GET DIAGNOSTICS updated = ROW_COUNT;
    PERFORM set_config('vult.agent_reseal', '', true);
    RETURN updated = 1;
END $$;

CREATE OR REPLACE FUNCTION public.agent_books()
    RETURNS TABLE(tenant_id uuid, book_id uuid, connection_id uuid)
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
SELECT s.tenant_id, s.book_id, s.connection_id FROM public.agent_settings s JOIN public.connections c
    ON c.tenant_id = s.tenant_id AND c.book_id = s.book_id AND c.id = s.connection_id
    LEFT JOIN public.vult_agent_settings v ON s.provider = 'vult' AND v.tenant_id = s.tenant_id
    WHERE s.enabled AND (s.provider = 'service'
        OR s.provider = 'vult' AND (v.key_sealed IS NOT NULL OR v.base_url ~ '^http://(127\.0\.0\.1|localhost|\[::1\])')
        OR s.provider NOT IN ('service', 'vult') AND (s.key_sealed IS NOT NULL OR s.base_url ~ '^http://(127\.0\.0\.1|localhost|\[::1\])'))
        AND NOT EXISTS (SELECT 1 FROM public.book_deletions d WHERE d.tenant_id = s.tenant_id AND d.book_id = s.book_id)
    ORDER BY s.updated_at LIMIT 1000
$$;

-- The skills test result belongs to the model the Book's agent runs on.
CREATE OR REPLACE FUNCTION public.agent_record_skills(t uuid, b uuid, tested_model text, tested_level text, tested_score integer, tested_total integer) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE updated integer;
BEGIN
    PERFORM set_config('vult.agent_skills', 'on', true);
    UPDATE public.agent_settings s SET skills_level = tested_level, skills_score = tested_score, skills_total = tested_total, skills_model = left(tested_model, 200),
        skills_tested_at = clock_timestamp(), default_trust = CASE WHEN tested_level = 'confident' THEN 'record' ELSE 'draft' END
        WHERE s.tenant_id = t AND s.book_id = b AND (s.model = tested_model
            OR s.provider = 'vult' AND EXISTS (SELECT 1 FROM public.vult_agent_settings v WHERE v.tenant_id = t AND v.model = tested_model));
    GET DIAGNOSTICS updated = ROW_COUNT;
    PERFORM set_config('vult.agent_skills', '', true);
    RETURN updated = 1;
END $$;

REVOKE ALL ON FUNCTION public.vult_agent_owner_allowed(uuid), public.vult_agent_settings_guard(), public.vult_agent_model_changed(),
    public.agent_secret_for(uuid, uuid), public.vult_agent_secret(uuid), public.vult_agent_key_checked(uuid, bytea, text, text), public.vult_agent_remove_key(uuid, uuid), public.vult_agent_reseal(uuid, bytea, bytea, integer), public.agent_books(),
    public.agent_record_skills(uuid, uuid, text, text, integer, integer) FROM PUBLIC;

-- ------------------------------------------------------------------------
-- 0022 partial_repayments
-- ------------------------------------------------------------------------

-- A repayment between linked Books may repay part of a cost. Unless the
-- owner chooses, the payment goes to the oldest open costs first, and the
-- last one it reaches is repaid in part and stays open for the rest. Each
-- cost a settlement repays keeps the amount it repaid; earlier rows, with
-- none, repaid the whole cost.
ALTER TABLE public.settlement_repays ADD COLUMN amount_minor bigint CHECK (amount_minor IS NULL OR amount_minor > 0);

-- What a cost (or an opening balance) amounts to, by its entry in the Book
-- it was paid for.
CREATE FUNCTION public.cost_amount(t uuid, b uuid, e uuid) RETURNS bigint
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(sum(z.amount_minor), 0)::bigint FROM public.lines z WHERE z.tenant_id = t AND z.book_id = b AND z.entry_id = e AND z.side = 'credit'
$$;

-- How much of a cost live settlements repaid; other_than leaves one out.
CREATE FUNCTION public.cost_repaid_amount(t uuid, b uuid, e uuid, other_than uuid) RETURNS bigint
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(sum(coalesce(r.amount_minor, public.cost_amount(t, b, e))), 0)::bigint FROM public.settlement_repays r
WHERE r.tenant_id = t AND r.cost_book_id = b AND r.cost_entry_id = e AND r.settlement_entry_id IS DISTINCT FROM other_than
    AND NOT public.entry_reversed(t, r.settlement_book_id, r.settlement_entry_id)
$$;

-- The settlement that finished repaying a cost, or NULL while any of it is open.
CREATE OR REPLACE FUNCTION public.cost_repaid_by(t uuid, b uuid, e uuid, other_than uuid) RETURNS text
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT CASE WHEN public.cost_repaid_amount(t, b, e, other_than) >= public.cost_amount(t, b, e) THEN (
    SELECT p.pair_key FROM public.settlement_repays r
        JOIN public.entry_pairs p ON p.tenant_id = r.tenant_id AND p.payer_book_id = r.settlement_book_id AND p.payer_entry_id = r.settlement_entry_id
        JOIN public.entries s ON s.tenant_id = r.tenant_id AND s.book_id = r.settlement_book_id AND s.id = r.settlement_entry_id
    WHERE r.tenant_id = t AND r.cost_book_id = b AND r.cost_entry_id = e AND r.settlement_entry_id IS DISTINCT FROM other_than
        AND NOT public.entry_reversed(t, r.settlement_book_id, r.settlement_entry_id)
    ORDER BY s.accounting_date DESC, s.sequence DESC LIMIT 1) END
$$;

-- The costs a debtor owes a creditor, oldest first, with what is open of
-- each: what settlements repaid is taken off each cost, and what is still
-- owed on the debtor's account reaches back from the newest; a payment that
-- named nothing repays the oldest. leaving_out leaves one settlement out.
CREATE FUNCTION public.open_costs(t uuid, creditor uuid, debtor uuid, debtor_account uuid, leaving_out uuid)
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
        AND (p.kind = 'cost' AND p.payer_book_id = creditor AND p.beneficiary_book_id = debtor
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
REVOKE ALL ON FUNCTION public.cost_amount(uuid, uuid, uuid), public.cost_repaid_amount(uuid, uuid, uuid, uuid),
    public.open_costs(uuid, uuid, uuid, uuid, uuid) FROM PUBLIC;

-- Checks a settlement, as before; a cost may be named with the part of it
-- this repays, and a refusal says what to do in the owner's words.
CREATE OR REPLACE FUNCTION public.settlement_check(t uuid, debtor uuid, debtor_account uuid, debtor_item uuid, creditor uuid, creditor_account uuid, creditor_item uuid,
        repays jsonb, leaving_out uuid) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE d public.bank_transactions%ROWTYPE; c public.bank_transactions%ROWTYPE; dclass text; cclass text; owed bigint; total bigint := 0;
    k text; entry uuid; entry_book uuid; cost bigint; amount bigint; excess bigint; repaying bigint; costs uuid[] := '{}'; items jsonb := '[]'; rev public.book_link_terms%ROWTYPE;
    required boolean; debtor_name text; creditor_name text; r record; elem jsonb; part bigint; open_amount bigint; key_text text; chosen text[] := '{}';
BEGIN
    SELECT * INTO d FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = debtor AND x.id = debtor_item;
    SELECT * INTO c FROM public.bank_transactions x WHERE x.tenant_id = t AND x.book_id = creditor AND x.id = creditor_item;
    IF d.id IS NULL OR c.id IS NULL THEN
        RAISE EXCEPTION 'A settlement is two bank items, one in each linked Book.' USING ERRCODE = 'VP020';
    END IF;
    debtor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = debtor);
    creditor_name := (SELECT b.name FROM public.books b WHERE b.tenant_id = t AND b.id = creditor);
    dclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = debtor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = debtor AND y.id = d.bank_account_id), d.account_id));
    cclass := (SELECT a.class FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = creditor
        AND a.id = coalesce((SELECT y.account_id FROM public.bank_accounts y WHERE y.tenant_id = t AND y.book_id = creditor AND y.id = c.bank_account_id), c.account_id));
    IF NOT (dclass = 'asset' AND d.amount_minor < 0 OR dclass = 'liability' AND d.amount_minor > 0)
        OR NOT (cclass = 'asset' AND c.amount_minor > 0 OR cclass = 'liability' AND c.amount_minor < 0) THEN
        RAISE EXCEPTION 'A settlement is money paid out of the Book that owes and into the Book that is owed; pick the payment in %, and its arrival in %.', debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    amount := abs(d.amount_minor);
    IF abs(c.amount_minor) <> amount OR c.currency IS DISTINCT FROM d.currency OR abs(c.occurred_on - d.occurred_on) > 5 THEN
        RAISE EXCEPTION 'A settlement''s two bank items are the same amount, at most five days apart; these are % and %, on % and %.',
            public.book_amount(t, debtor, amount), public.book_amount(t, creditor, abs(c.amount_minor)), d.occurred_on, c.occurred_on USING ERRCODE = 'VP020';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM public.book_link_terms x JOIN public.book_links l ON l.tenant_id = x.tenant_id AND l.id = x.link_id AND l.active
            WHERE x.tenant_id = t AND x.active AND x.treatment = 'owed' AND x.payer_book_id = creditor AND x.payer_account_id = creditor_account
                AND x.beneficiary_book_id = debtor AND x.beneficiary_account_id = debtor_account) THEN
        RAISE EXCEPTION 'Nothing is owed this way: % pays % only for what an active owed direction names. Set "% pays for %" to owed back in Links.',
            debtor_name, creditor_name, creditor_name, debtor_name USING ERRCODE = 'VP020';
    END IF;
    rev := public.active_link_terms(t, debtor, creditor);
    SELECT coalesce(sum(CASE WHEN x.side = 'credit' THEN x.amount_minor ELSE -x.amount_minor END), 0) INTO owed
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.account_id = debtor_account AND x.entry_id IS DISTINCT FROM leaving_out;
    excess := greatest(amount - greatest(owed, 0), 0);
    repaying := amount - excess;
    IF excess > 0 AND rev.link_id IS NULL THEN
        RAISE EXCEPTION 'This pays % more than % owes (%). Set "% pays for %" in Links, and the extra is recorded there.',
            public.book_amount(t, debtor, excess), debtor_name, public.book_amount(t, debtor, greatest(owed, 0)), debtor_name, creditor_name USING ERRCODE = 'VP020';
    END IF;
    IF jsonb_typeof(coalesce(repays, '[]'::jsonb)) <> 'array' OR jsonb_array_length(coalesce(repays, '[]'::jsonb)) > 50 THEN
        RAISE EXCEPTION 'Choose the costs this repays from the open items.' USING ERRCODE = 'VP020';
    END IF;
    -- Whether the payer's books expect the costs named (an accountable plan);
    -- reported, not required: unnamed, the oldest are repaid first.
    required := public.book_profile(t, creditor) = 'personal' AND public.book_profile(t, debtor) = 'business' AND rev.treatment IS DISTINCT FROM 'owner_equity';
    FOR elem IN SELECT jsonb_array_elements(coalesce(repays, '[]'::jsonb)) LOOP
        -- A cost by its key repays what is open of it; with an amount, that part.
        IF jsonb_typeof(elem) = 'string' THEN
            key_text := elem #>> '{}';
            part := NULL;
        ELSIF jsonb_typeof(elem) = 'object' AND jsonb_typeof(elem -> 'pair_key') = 'string' AND (elem - 'pair_key' - 'amount_minor') = '{}'::jsonb
                AND (elem -> 'amount_minor' IS NULL OR jsonb_typeof(elem -> 'amount_minor') = 'string' AND elem ->> 'amount_minor' ~ '^[1-9][0-9]{0,17}$') THEN
            key_text := elem ->> 'pair_key';
            part := (elem ->> 'amount_minor')::bigint;
        ELSE
            RAISE EXCEPTION 'Choose the costs this repays from the open items.' USING ERRCODE = 'VP020';
        END IF;
        IF key_text = ANY (chosen) THEN
            RAISE EXCEPTION 'A cost is chosen twice; choose each cost once.' USING ERRCODE = 'VP020';
        END IF;
        chosen := chosen || key_text;
        SELECT o.book_id, o.entry_id, o.open INTO entry_book, entry, open_amount
            FROM public.open_costs(t, creditor, debtor, debtor_account, leaving_out) o WHERE o.pair_key = key_text;
        IF entry IS NULL THEN
            RAISE EXCEPTION 'One of the costs chosen isn''t a cost % paid for % or an opening balance % owes; choose from the open items.', creditor_name, debtor_name, debtor_name USING ERRCODE = 'VP020';
        END IF;
        IF open_amount <= 0 THEN
            RAISE EXCEPTION 'One of the costs chosen is already repaid; choose an open cost instead.' USING ERRCODE = 'VP020';
        END IF;
        part := coalesce(part, open_amount);
        IF part > open_amount THEN
            RAISE EXCEPTION 'This repays % of a cost that has % left to repay; repay at most what is left.',
                public.book_amount(t, debtor, part), public.book_amount(t, debtor, open_amount) USING ERRCODE = 'VP020';
        END IF;
        costs := costs || entry;
        items := items || jsonb_build_array(jsonb_build_object('book', entry_book, 'entry', entry, 'pair_key', key_text, 'amount_minor', part));
        total := total + part;
    END LOOP;
    IF cardinality(costs) > 0 AND total <> repaying THEN
        RAISE EXCEPTION 'The costs chosen add up to %, but this payment repays %; choose costs and amounts that add up to it exactly.',
            public.book_amount(t, debtor, total), public.book_amount(t, debtor, repaying) USING ERRCODE = 'VP020';
    END IF;
    IF cardinality(costs) = 0 THEN
        -- Unnamed: the oldest open costs first; the last one reached is
        -- repaid in part and stays open for the rest.
        FOR r IN SELECT o.* FROM public.open_costs(t, creditor, debtor, debtor_account, leaving_out) o WHERE o.open > 0
            ORDER BY o.accounting_date, o.sequence, o.pair_key LOOP
            EXIT WHEN total >= repaying;
            part := least(r.open, repaying - total);
            costs := costs || r.entry_id;
            items := items || jsonb_build_array(jsonb_build_object('book', r.book_id, 'entry', r.entry_id, 'pair_key', r.pair_key, 'amount_minor', part));
            total := total + part;
        END LOOP;
    END IF;
    RETURN jsonb_build_object('costs', items, 'excess', excess, 'repaying', repaying, 'required', required, 'named', jsonb_array_length(coalesce(repays, '[]'::jsonb)) > 0);
END $$;


-- Records a settlement, keeping the part of each cost it repays.
CREATE OR REPLACE FUNCTION public.settle_pair(t uuid, key text, debtor uuid, debtor_entry uuid, creditor uuid, creditor_entry uuid, repays jsonb) RETURNS void
    LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE ditem uuid; dbank uuid; citem uuid; cbank uuid; owed public.book_link_terms%ROWTYPE; rev public.book_link_terms%ROWTYPE;
    checked jsonb; item jsonb; excess bigint; repaying bigint; amount bigint; bad int;
BEGIN
    IF NOT (access.owner_allowed(t) AND access.owner_write_allowed(t)) THEN
        RAISE EXCEPTION 'only the owner records a pair' USING ERRCODE = '42501';
    END IF;
    SELECT l.transaction_id, l.account_id INTO ditem, dbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = debtor AND l.entry_id = debtor_entry AND l.revoked_at IS NULL;
    SELECT l.transaction_id, l.account_id INTO citem, cbank FROM public.bank_transaction_links l
        WHERE l.tenant_id = t AND l.book_id = creditor AND l.entry_id = creditor_entry AND l.revoked_at IS NULL;
    owed := public.active_link_terms(t, creditor, debtor);
    rev := public.active_link_terms(t, debtor, creditor);
    IF ditem IS NULL OR citem IS NULL OR owed.treatment IS DISTINCT FROM 'owed' THEN
        RAISE EXCEPTION 'Each entry of a settlement records its own bank item, between Books an owed direction links.' USING ERRCODE = 'VP020';
    END IF;
    checked := public.settlement_check(t, debtor, owed.beneficiary_account_id, ditem, creditor, owed.payer_account_id, citem, repays, debtor_entry);
    excess := (checked ->> 'excess')::bigint;
    repaying := (checked ->> 'repaying')::bigint;
    amount := excess + repaying;
    -- The debtor's entry: the bank account for the whole amount, the payable
    -- for what was owed, the reverse direction's account for the excess.
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = dbank AND x.amount_minor = amount
            OR x.account_id = owed.beneficiary_account_id AND x.side = 'debit' AND x.amount_minor = repaying
            OR x.account_id = rev.payer_account_id AND x.side = 'debit' AND x.amount_minor = excess, false))
        + abs(count(*) - (1 + (repaying > 0)::int + (excess > 0)::int)) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = debtor AND x.entry_id = debtor_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The paying Book''s entry records % to what it owed and % as the extra; record it that way.',
            public.book_amount(t, debtor, repaying), public.book_amount(t, debtor, excess) USING ERRCODE = 'VP020';
    END IF;
    SELECT count(*) FILTER (WHERE NOT coalesce(x.account_id = cbank AND x.amount_minor = amount
            OR x.account_id = owed.payer_account_id AND x.side = 'credit' AND x.amount_minor = repaying
            OR x.account_id = rev.beneficiary_account_id AND x.side = 'credit' AND x.amount_minor = excess, false))
        + abs(count(*) - (1 + (repaying > 0)::int + (excess > 0)::int)) INTO bad
        FROM public.lines x WHERE x.tenant_id = t AND x.book_id = creditor AND x.entry_id = creditor_entry;
    IF bad <> 0 THEN
        RAISE EXCEPTION 'The paid Book''s entry records % to what it was owed and % as the extra; record it that way.',
            public.book_amount(t, creditor, repaying), public.book_amount(t, creditor, excess) USING ERRCODE = 'VP020';
    END IF;
    IF EXISTS (SELECT 1 FROM public.entries x JOIN public.bank_transactions i ON i.tenant_id = x.tenant_id AND i.book_id = x.book_id
                AND i.id = CASE WHEN x.book_id = debtor THEN ditem ELSE citem END
            WHERE x.tenant_id = t AND (x.book_id = debtor AND x.id = debtor_entry OR x.book_id = creditor AND x.id = creditor_entry)
                AND (x.accounting_date <> i.occurred_on OR x.reversal_of IS NOT NULL)) THEN
        RAISE EXCEPTION 'Each entry of a settlement is dated its own bank item''s date.' USING ERRCODE = 'VP020';
    END IF;
    PERFORM set_config('vult.settling', key, true);
    INSERT INTO public.entry_pairs(tenant_id, pair_key, kind, payer_book_id, payer_entry_id, beneficiary_book_id, beneficiary_entry_id)
        VALUES (t, key, 'settlement', debtor, debtor_entry, creditor, creditor_entry);
    PERFORM set_config('vult.settling', '', true);
    FOR item IN SELECT jsonb_array_elements(checked -> 'costs') LOOP
        INSERT INTO public.settlement_repays(tenant_id, settlement_book_id, settlement_entry_id, cost_book_id, cost_entry_id, amount_minor)
            VALUES (t, debtor, debtor_entry, (item ->> 'book')::uuid, (item ->> 'entry')::uuid, (item ->> 'amount_minor')::bigint);
    END LOOP;
END $$;

-- Who owes whom shows what is still open of each cost.
CREATE OR REPLACE FUNCTION public.linked_balances(t uuid, b uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
WITH allowed AS (
    SELECT CASE WHEN b IS NULL THEN access.owner_allowed(t) ELSE access.owner_allowed(t) OR public.ledger_allowed(t, b, false) END AS ok
), links AS (
    SELECT l.* FROM public.book_links l, allowed WHERE allowed.ok AND l.tenant_id = t AND (b IS NULL OR b IN (l.book_a, l.book_b))
), balance AS (
    -- Each line's effect, debit positive, on the accounts terms name.
    SELECT x.book_id, x.account_id, x.entry_id, CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END AS amount
    FROM public.lines x WHERE x.tenant_id = t
), dirs AS (
    -- Owed directions: the payer is owed (receivable), the beneficiary owes (payable).
    SELECT k.link_id, k.payer_book_id AS creditor, k.payer_account_id AS creditor_account, k.beneficiary_book_id AS debtor, k.beneficiary_account_id AS debtor_account,
        coalesce((SELECT sum(y.amount) FROM balance y WHERE y.book_id = k.payer_book_id AND y.account_id = k.payer_account_id), 0) AS creditor_says,
        coalesce((SELECT -sum(y.amount) FROM balance y WHERE y.book_id = k.beneficiary_book_id AND y.account_id = k.beneficiary_account_id), 0) AS debtor_says
    FROM public.book_link_terms k JOIN links l ON l.tenant_id = t AND l.id = k.link_id WHERE k.tenant_id = t AND k.treatment = 'owed'
), paired AS (
    SELECT p.payer_book_id AS book_id, p.payer_entry_id AS entry_id FROM public.entry_pairs p WHERE p.tenant_id = t
    UNION ALL SELECT p.beneficiary_book_id, p.beneficiary_entry_id FROM public.entry_pairs p WHERE p.tenant_id = t
), net AS (
    SELECT l.id AS link_id, l.book_a, l.book_b,
        coalesce(sum(CASE WHEN d.creditor = l.book_a THEN d.debtor_says ELSE -d.debtor_says END), 0) AS owed_to_a,
        bool_and(d.creditor_says = d.debtor_says) AS matches
    FROM links l JOIN dirs d ON d.link_id = l.id GROUP BY l.id, l.book_a, l.book_b
), costs AS (
    -- Costs and opening balances along each owed direction, with what is
    -- still open of each.
    SELECT o.*, d.link_id, d.creditor FROM dirs d, public.open_costs(t, d.creditor, d.debtor, d.debtor_account, NULL) o
), ranked AS (
    SELECT c.* FROM costs c
)
SELECT jsonb_build_object(
    'links', coalesce((SELECT jsonb_agg(jsonb_build_object(
        'link_id', n.link_id,
        'debtor_book_id', CASE WHEN n.owed_to_a >= 0 THEN n.book_b ELSE n.book_a END,
        'creditor_book_id', CASE WHEN n.owed_to_a >= 0 THEN n.book_a ELSE n.book_b END,
        'amount_minor', abs(n.owed_to_a)::text,
        'currency', (SELECT k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = n.book_a),
        'matches', n.matches,
        'mismatched_entry_ids', coalesce((SELECT jsonb_agg(DISTINCT y.entry_id) FROM dirs d JOIN balance y
                ON (y.book_id = d.creditor AND y.account_id = d.creditor_account OR y.book_id = d.debtor AND y.account_id = d.debtor_account)
            WHERE d.link_id = n.link_id
                AND NOT EXISTS (SELECT 1 FROM paired q WHERE q.book_id = y.book_id AND q.entry_id = y.entry_id)
                AND NOT EXISTS (SELECT 1 FROM public.entries e JOIN paired q ON q.book_id = e.book_id AND q.entry_id = e.reversal_of
                    WHERE e.tenant_id = t AND e.book_id = y.book_id AND e.id = y.entry_id)), '[]'::jsonb),
        'open_pairs', coalesce((SELECT jsonb_agg(jsonb_build_object(
                'pair_key', r.pair_key, 'kind', r.kind, 'payer_book_id', r.payer_book_id, 'beneficiary_book_id', r.beneficiary_book_id,
                'date', r.accounting_date, 'description', r.description,
                'amount_minor', r.amount::text,
                'open_minor', r.open::text,
                'repaid', r.open = 0,
                'partly_repaid', r.open > 0 AND r.open < r.amount,
                'has_receipt', EXISTS (SELECT 1 FROM public.evidence_links v WHERE v.tenant_id = t
                        AND (v.book_id = r.beneficiary_book_id AND v.entry_id = r.beneficiary_entry_id OR v.book_id = r.payer_book_id AND v.entry_id = r.payer_entry_id)
                        AND v.action = 'attach' AND v.role IN ('receipt', 'invoice')
                        AND NOT EXISTS (SELECT 1 FROM public.evidence_links w WHERE w.tenant_id = v.tenant_id AND w.book_id = v.book_id AND w.link_id = v.link_id AND w.revision > v.revision))
                    OR EXISTS (SELECT 1 FROM public.source_citations c WHERE c.tenant_id = t
                        AND (c.book_id = r.beneficiary_book_id AND c.entry_id = r.beneficiary_entry_id OR c.book_id = r.payer_book_id AND c.entry_id = r.payer_entry_id)))
                ORDER BY r.accounting_date, r.pair_key)
            FROM ranked r WHERE r.link_id = n.link_id), '[]'::jsonb))
        ORDER BY n.link_id) FROM net n), '[]'::jsonb),
    'books', coalesce((SELECT jsonb_agg(jsonb_build_object('book_id', k.id, 'name', k.name,
        'liabilities', coalesce((SELECT jsonb_agg(jsonb_build_object('account_id', a.id, 'name', a.name, 'role', coalesce(a.role, ''),
                'balance_minor', coalesce((SELECT -sum(y.amount) FROM balance y WHERE y.book_id = a.book_id AND y.account_id = a.id), 0)::text) ORDER BY a.code, a.id)
            FROM public.accounts a WHERE a.tenant_id = t AND a.book_id = k.id AND a.class = 'liability'
                AND NOT EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t
                    AND (x.payer_book_id = k.id AND x.payer_account_id = a.id OR x.beneficiary_book_id = k.id AND x.beneficiary_account_id = a.id))), '[]'::jsonb))
        ORDER BY k.name, k.id)
        FROM public.books k, allowed WHERE allowed.ok AND k.tenant_id = t AND (b IS NULL AND EXISTS (SELECT 1 FROM links l WHERE k.id IN (l.book_a, l.book_b)) OR k.id = b)), '[]'::jsonb))
$$;

-- What a repayment would do, before it is recorded: which costs it repays
-- and how much of each, what each has left after it, and any extra. Only
-- the owner reads it.
CREATE FUNCTION public.settlement_preview(t uuid, debtor_item uuid, creditor_item uuid, repays jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE debtor uuid; creditor uuid; owed public.book_link_terms%ROWTYPE; checked jsonb;
BEGIN
    IF NOT access.owner_allowed(t) THEN
        RAISE EXCEPTION 'only the owner previews a repayment' USING ERRCODE = '42501';
    END IF;
    debtor := (SELECT x.book_id FROM public.bank_transactions x WHERE x.tenant_id = t AND x.id = debtor_item);
    creditor := (SELECT x.book_id FROM public.bank_transactions x WHERE x.tenant_id = t AND x.id = creditor_item);
    owed := public.active_link_terms(t, creditor, debtor);
    IF debtor IS NULL OR creditor IS NULL OR debtor = creditor OR owed.treatment IS DISTINCT FROM 'owed' THEN
        RAISE EXCEPTION 'Nothing is owed this way between these Books: no active owed direction links them, or the link is turned off.' USING ERRCODE = 'VP020';
    END IF;
    checked := public.settlement_check(t, debtor, owed.beneficiary_account_id, debtor_item, creditor, owed.payer_account_id, creditor_item, repays, NULL);
    RETURN jsonb_build_object(
        'repaying_minor', (checked ->> 'repaying'),
        'excess_minor', (checked ->> 'excess'),
        'excess_recorded_as', CASE WHEN (checked ->> 'excess')::bigint > 0 THEN
            CASE WHEN (public.active_link_terms(t, debtor, creditor)).treatment = 'owner_equity' THEN 'owner_draw' ELSE 'owed_back' END END,
        'names_expected', checked -> 'required',
        'costs', coalesce((SELECT jsonb_agg(jsonb_build_object('pair_key', c ->> 'pair_key', 'amount_minor', c ->> 'amount_minor',
                'open_minor', o.open::text, 'left_after_minor', (o.open - (c ->> 'amount_minor')::bigint)::text) ORDER BY o.accounting_date, o.sequence, o.pair_key)
            FROM jsonb_array_elements(checked -> 'costs') c
                JOIN public.open_costs(t, creditor, debtor, owed.beneficiary_account_id, NULL) o ON o.pair_key = c ->> 'pair_key'), '[]'::jsonb));
END $$;
REVOKE ALL ON FUNCTION public.settlement_preview(uuid, uuid, uuid, jsonb) FROM PUBLIC;

-- ------------------------------------------------------------------------
-- 0023 clear_refusals
-- ------------------------------------------------------------------------

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

-- ------------------------------------------------------------------------
-- 0024 book_tax_classification
-- ------------------------------------------------------------------------

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

-- ------------------------------------------------------------------------
-- 0030 linked_balance_totals
-- ------------------------------------------------------------------------

-- Sum account balances once; keep individual postings for pair mismatch checks.
CREATE OR REPLACE FUNCTION public.linked_balances(t uuid, b uuid) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
WITH allowed AS (
    SELECT CASE WHEN b IS NULL THEN access.owner_allowed(t) ELSE access.owner_allowed(t) OR public.ledger_allowed(t, b, false) END AS ok
), links AS (
    SELECT l.* FROM public.book_links l, allowed WHERE allowed.ok AND l.tenant_id = t AND (b IS NULL OR b IN (l.book_a, l.book_b))
), balance AS (
    -- Each line's effect, debit positive, on the accounts terms name.
    SELECT x.book_id, x.account_id, x.entry_id, CASE WHEN x.side = 'debit' THEN x.amount_minor ELSE -x.amount_minor END AS amount
    FROM public.lines x WHERE x.tenant_id = t
), account_balances AS MATERIALIZED (
    SELECT book_id, account_id, sum(amount) AS amount FROM balance GROUP BY book_id, account_id
), dirs AS (
    -- Owed directions: the payer is owed (receivable), the beneficiary owes (payable).
    SELECT k.link_id, k.payer_book_id AS creditor, k.payer_account_id AS creditor_account, k.beneficiary_book_id AS debtor, k.beneficiary_account_id AS debtor_account,
        coalesce(creditor.amount, 0) AS creditor_says,
        coalesce(-debtor.amount, 0) AS debtor_says
    FROM public.book_link_terms k JOIN links l ON l.tenant_id = t AND l.id = k.link_id
        LEFT JOIN account_balances creditor ON creditor.book_id = k.payer_book_id AND creditor.account_id = k.payer_account_id
        LEFT JOIN account_balances debtor ON debtor.book_id = k.beneficiary_book_id AND debtor.account_id = k.beneficiary_account_id
    WHERE k.tenant_id = t AND k.treatment = 'owed'
), paired AS (
    SELECT p.payer_book_id AS book_id, p.payer_entry_id AS entry_id FROM public.entry_pairs p WHERE p.tenant_id = t
    UNION ALL SELECT p.beneficiary_book_id, p.beneficiary_entry_id FROM public.entry_pairs p WHERE p.tenant_id = t
), net AS (
    SELECT l.id AS link_id, l.book_a, l.book_b,
        coalesce(sum(CASE WHEN d.creditor = l.book_a THEN d.debtor_says ELSE -d.debtor_says END), 0) AS owed_to_a,
        bool_and(d.creditor_says = d.debtor_says) AS matches
    FROM links l JOIN dirs d ON d.link_id = l.id GROUP BY l.id, l.book_a, l.book_b
), costs AS (
    -- Costs and opening balances along each owed direction, with what is
    -- still open of each.
    SELECT o.*, d.link_id, d.creditor FROM dirs d, public.open_costs(t, d.creditor, d.debtor, d.debtor_account, NULL) o
), ranked AS (
    SELECT c.* FROM costs c
)
SELECT jsonb_build_object(
    'links', coalesce((SELECT jsonb_agg(jsonb_build_object(
        'link_id', n.link_id,
        'debtor_book_id', CASE WHEN n.owed_to_a >= 0 THEN n.book_b ELSE n.book_a END,
        'creditor_book_id', CASE WHEN n.owed_to_a >= 0 THEN n.book_a ELSE n.book_b END,
        'amount_minor', abs(n.owed_to_a)::text,
        'currency', (SELECT k.currency FROM public.books k WHERE k.tenant_id = t AND k.id = n.book_a),
        'matches', n.matches,
        'mismatched_entry_ids', coalesce((SELECT jsonb_agg(DISTINCT y.entry_id) FROM dirs d JOIN balance y
                ON (y.book_id = d.creditor AND y.account_id = d.creditor_account OR y.book_id = d.debtor AND y.account_id = d.debtor_account)
            WHERE d.link_id = n.link_id
                AND NOT EXISTS (SELECT 1 FROM paired q WHERE q.book_id = y.book_id AND q.entry_id = y.entry_id)
                AND NOT EXISTS (SELECT 1 FROM public.entries e JOIN paired q ON q.book_id = e.book_id AND q.entry_id = e.reversal_of
                    WHERE e.tenant_id = t AND e.book_id = y.book_id AND e.id = y.entry_id)), '[]'::jsonb),
        'open_pairs', coalesce((SELECT jsonb_agg(jsonb_build_object(
                'pair_key', r.pair_key, 'kind', r.kind, 'payer_book_id', r.payer_book_id, 'beneficiary_book_id', r.beneficiary_book_id,
                'date', r.accounting_date, 'description', r.description,
                'amount_minor', r.amount::text,
                'open_minor', r.open::text,
                'repaid', r.open = 0,
                'partly_repaid', r.open > 0 AND r.open < r.amount,
                'has_receipt', EXISTS (SELECT 1 FROM public.evidence_links v WHERE v.tenant_id = t
                        AND (v.book_id = r.beneficiary_book_id AND v.entry_id = r.beneficiary_entry_id OR v.book_id = r.payer_book_id AND v.entry_id = r.payer_entry_id)
                        AND v.action = 'attach' AND v.role IN ('receipt', 'invoice')
                        AND NOT EXISTS (SELECT 1 FROM public.evidence_links w WHERE w.tenant_id = v.tenant_id AND w.book_id = v.book_id AND w.link_id = v.link_id AND w.revision > v.revision))
                    OR EXISTS (SELECT 1 FROM public.source_citations c WHERE c.tenant_id = t
                        AND (c.book_id = r.beneficiary_book_id AND c.entry_id = r.beneficiary_entry_id OR c.book_id = r.payer_book_id AND c.entry_id = r.payer_entry_id)))
                ORDER BY r.accounting_date, r.pair_key)
            FROM ranked r WHERE r.link_id = n.link_id), '[]'::jsonb))
        ORDER BY n.link_id) FROM net n), '[]'::jsonb),
    'books', coalesce((SELECT jsonb_agg(jsonb_build_object('book_id', k.id, 'name', k.name,
        'liabilities', coalesce((SELECT jsonb_agg(jsonb_build_object('account_id', a.id, 'name', a.name, 'role', coalesce(a.role, ''),
                'balance_minor', coalesce(-y.amount, 0)::text) ORDER BY a.code, a.id)
            FROM public.accounts a LEFT JOIN account_balances y ON y.book_id = a.book_id AND y.account_id = a.id
            WHERE a.tenant_id = t AND a.book_id = k.id AND a.class = 'liability'
                AND NOT EXISTS (SELECT 1 FROM public.book_link_terms x WHERE x.tenant_id = t
                    AND (x.payer_book_id = k.id AND x.payer_account_id = a.id OR x.beneficiary_book_id = k.id AND x.beneficiary_account_id = a.id))), '[]'::jsonb))
        ORDER BY k.name, k.id)
        FROM public.books k, allowed WHERE allowed.ok AND k.tenant_id = t AND (b IS NULL AND EXISTS (SELECT 1 FROM links l WHERE k.id IN (l.book_a, l.book_b)) OR k.id = b)), '[]'::jsonb))
$$;

-- ------------------------------------------------------------------------
-- 0031 projects
-- ------------------------------------------------------------------------

-- Projects in a Book: a light label on what a Book spends, such as a side
-- project paid from personal cards, so the owner can see what each one
-- cost and later turn one into a company. A label never changes an
-- account, an amount or an entry: it says which project a line's amount,
-- or part of it, went to.
CREATE TABLE public.projects (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    request_key text NOT NULL CHECK (length(request_key) BETWEEN 1 AND 128),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120 AND name = btrim(name)),
    starts_on date,
    ends_on date,
    archived boolean NOT NULL DEFAULT false,
    revision integer NOT NULL DEFAULT 1 CHECK (revision > 0),
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    UNIQUE (tenant_id, book_id, request_key),
    CHECK (starts_on IS NULL OR ends_on IS NULL OR ends_on >= starts_on),
    FOREIGN KEY (tenant_id, book_id) REFERENCES public.books(tenant_id, id)
);
-- A Book's projects have different names.
CREATE UNIQUE INDEX projects_name ON public.projects(tenant_id, book_id, lower(name));

-- Which project a line's amount, or part of it, went to. A line on an
-- income or expense account may be split across projects; the parts never
-- add up to more than the line.
CREATE TABLE public.project_labels (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    line_id uuid NOT NULL,
    project_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    -- How the label was given: by the owner or an agent, by a rule, or by
    -- carrying the cost into a company made from the project.
    source text NOT NULL CHECK (source IN ('label', 'rule', 'carried')),
    actor_id uuid NOT NULL,
    labelled_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, book_id, line_id, project_id),
    FOREIGN KEY (tenant_id, book_id, line_id) REFERENCES public.lines(tenant_id, book_id, id),
    FOREIGN KEY (tenant_id, book_id, project_id) REFERENCES public.projects(tenant_id, book_id, id)
);
CREATE INDEX project_labels_project ON public.project_labels(tenant_id, book_id, project_id);
CREATE INDEX project_labels_entry ON public.project_labels(tenant_id, book_id, entry_id);

-- "Always label Example Cloud as Side app", optionally only on one bank
-- account and only between two dates. A rule labels; it never chooses the
-- account an item is recorded to.
CREATE TABLE public.project_rules (
    tenant_id uuid NOT NULL,
    book_id uuid NOT NULL,
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL,
    counterparty_key text NOT NULL CHECK (length(counterparty_key) BETWEEN 1 AND 200),
    bank_account_id uuid,
    starts_on date,
    ends_on date,
    actor_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (tenant_id, book_id, id),
    CHECK (starts_on IS NULL OR ends_on IS NULL OR ends_on >= starts_on),
    FOREIGN KEY (tenant_id, book_id, project_id) REFERENCES public.projects(tenant_id, book_id, id)
);

-- The owner, or a connection that may record in the Book, keeps projects
-- and their labels and rules.
CREATE FUNCTION public.project_writer(t uuid, b uuid) RETURNS boolean
    LANGUAGE sql STABLE SET search_path = pg_catalog AS $$
SELECT coalesce(current_setting('vult.connection_id', true), '') = '' AND public.ledger_context(t, b) AND access.owner_allowed(t)
    OR public.ledger_allowed(t, b, true)
$$;

CREATE FUNCTION public.project_guard() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
DECLARE kind text; line_amount bigint; entry uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    IF NOT public.ledger_import_owner() THEN
        IF NEW.actor_id::text IS DISTINCT FROM current_setting('vult.actor_id', true) THEN
            RAISE EXCEPTION 'authenticated actor required' USING ERRCODE = '42501';
        END IF;
    END IF;
    IF TG_TABLE_NAME = 'projects' THEN
        IF TG_OP = 'UPDATE' THEN
            IF ROW(NEW.tenant_id, NEW.book_id, NEW.id, NEW.request_key, NEW.created_at) IS DISTINCT FROM ROW(OLD.tenant_id, OLD.book_id, OLD.id, OLD.request_key, OLD.created_at) THEN
                RAISE EXCEPTION 'a project keeps its Book' USING ERRCODE = '23514';
            END IF;
            IF NOT public.ledger_import_owner() THEN
                NEW.revision := OLD.revision + 1;
                NEW.updated_at := clock_timestamp();
            END IF;
        END IF;
        RETURN NEW;
    END IF;
    IF TG_TABLE_NAME = 'project_labels' THEN
        SELECT a.class, l.amount_minor, l.entry_id INTO kind, line_amount, entry
            FROM public.lines l JOIN public.accounts a ON a.tenant_id = l.tenant_id AND a.book_id = l.book_id AND a.id = l.account_id
            WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.id = NEW.line_id;
        IF kind IS NULL OR kind NOT IN ('expense', 'income') THEN
            RAISE EXCEPTION 'Only spending and income go to a project; this line moves money between accounts.' USING ERRCODE = 'VP020';
        END IF;
        IF NEW.entry_id IS DISTINCT FROM entry THEN
            RAISE EXCEPTION 'a label names its line''s entry' USING ERRCODE = '23514';
        END IF;
        IF NEW.amount_minor > line_amount THEN
            RAISE EXCEPTION 'A project''s part of a line is at most the line''s amount.' USING ERRCODE = 'VP020';
        END IF;
    END IF;
    RETURN NEW;
END $$;

-- The parts of a line never add up to more than the line.
CREATE FUNCTION public.project_labels_fit() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM public.project_labels p JOIN public.lines l ON l.tenant_id = p.tenant_id AND l.book_id = p.book_id AND l.id = p.line_id
            WHERE p.tenant_id = NEW.tenant_id AND p.book_id = NEW.book_id AND p.line_id = NEW.line_id
            GROUP BY l.amount_minor HAVING sum(p.amount_minor) > l.amount_minor) THEN
        RAISE EXCEPTION 'The projects'' parts add up to more than the line.' USING ERRCODE = 'VP020';
    END IF;
    RETURN NULL;
END $$;

CREATE TRIGGER project_guard BEFORE INSERT OR UPDATE ON public.projects FOR EACH ROW EXECUTE FUNCTION public.project_guard();
CREATE TRIGGER project_guard BEFORE INSERT OR UPDATE ON public.project_labels FOR EACH ROW EXECUTE FUNCTION public.project_guard();
CREATE TRIGGER project_guard BEFORE INSERT OR UPDATE ON public.project_rules FOR EACH ROW EXECUTE FUNCTION public.project_guard();
CREATE CONSTRAINT TRIGGER project_labels_fit AFTER INSERT OR UPDATE ON public.project_labels
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.project_labels_fit();
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.projects FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('book_id');
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.project_labels FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('book_id');
CREATE TRIGGER book_lock_guard BEFORE INSERT OR UPDATE OR DELETE ON public.project_rules FOR EACH ROW EXECUTE FUNCTION public.book_lock_guard('book_id');

-- When a bank item is recorded, a rule for its counterparty labels the
-- entry's spending lines in full, if the item's date is in the rule's
-- range and the lines aren't labelled yet. The newest matching rule wins.
CREATE FUNCTION public.project_rules_apply() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE item public.bank_transactions%ROWTYPE; rule public.project_rules%ROWTYPE;
BEGIN
    IF NEW.revoked_at IS NOT NULL THEN
        RETURN NULL;
    END IF;
    SELECT * INTO item FROM public.bank_transactions x WHERE x.tenant_id = NEW.tenant_id AND x.book_id = NEW.book_id AND x.id = NEW.transaction_id;
    IF item.id IS NULL THEN
        RETURN NULL;
    END IF;
    SELECT r.* INTO rule FROM public.project_rules r JOIN public.projects p ON p.tenant_id = r.tenant_id AND p.book_id = r.book_id AND p.id = r.project_id
        WHERE r.tenant_id = NEW.tenant_id AND r.book_id = NEW.book_id AND NOT p.archived
            AND r.counterparty_key = public.rule_key(coalesce(nullif(item.counterparty, ''), item.description))
            AND (r.bank_account_id IS NULL OR r.bank_account_id = item.bank_account_id)
            AND (r.starts_on IS NULL OR item.occurred_on >= r.starts_on) AND (r.ends_on IS NULL OR item.occurred_on <= r.ends_on)
        ORDER BY r.created_at DESC LIMIT 1;
    IF rule.id IS NULL THEN
        RETURN NULL;
    END IF;
    INSERT INTO public.project_labels(tenant_id, book_id, line_id, project_id, entry_id, amount_minor, source, actor_id)
        SELECT l.tenant_id, l.book_id, l.id, rule.project_id, l.entry_id, l.amount_minor, 'rule', NEW.actor_id
        FROM public.lines l JOIN public.accounts a ON a.tenant_id = l.tenant_id AND a.book_id = l.book_id AND a.id = l.account_id
        WHERE l.tenant_id = NEW.tenant_id AND l.book_id = NEW.book_id AND l.entry_id = NEW.entry_id AND a.class IN ('expense', 'income')
            AND NOT EXISTS (SELECT 1 FROM public.project_labels q WHERE q.tenant_id = l.tenant_id AND q.book_id = l.book_id AND q.line_id = l.id)
        ON CONFLICT DO NOTHING;
    RETURN NULL;
END $$;
CREATE TRIGGER project_rules_apply AFTER INSERT ON public.bank_transaction_links FOR EACH ROW EXECUTE FUNCTION public.project_rules_apply();

DO $$
DECLARE tab text;
BEGIN
    FOREACH tab IN ARRAY ARRAY['public.projects', 'public.project_labels', 'public.project_rules'] LOOP
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', tab);
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', tab);
        EXECUTE format('REVOKE ALL ON %s FROM PUBLIC', tab);
        EXECUTE format('CREATE POLICY project_read ON %s FOR SELECT USING (access.owner_allowed(tenant_id) OR public.ledger_allowed(tenant_id, book_id, false))', tab);
        EXECUTE format('CREATE POLICY project_insert ON %s FOR INSERT WITH CHECK (public.project_writer(tenant_id, book_id))', tab);
        EXECUTE format('CREATE POLICY project_update ON %s FOR UPDATE USING (public.project_writer(tenant_id, book_id)) WITH CHECK (public.project_writer(tenant_id, book_id))', tab);
        EXECUTE format('CREATE POLICY project_delete ON %s FOR DELETE USING (public.project_writer(tenant_id, book_id))', tab);
    END LOOP;
END $$;
REVOKE ALL ON FUNCTION public.project_writer(uuid, uuid), public.project_guard(), public.project_labels_fit(), public.project_rules_apply() FROM PUBLIC;

-- ------------------------------------------------------------------------
-- 0032 carried_costs
-- ------------------------------------------------------------------------

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
