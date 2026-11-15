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
