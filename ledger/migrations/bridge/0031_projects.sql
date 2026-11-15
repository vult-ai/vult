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
