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
