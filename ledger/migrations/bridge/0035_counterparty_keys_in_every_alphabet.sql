-- A counterparty's key keeps the letters of every alphabet.
--
-- The key kept only a to z, so a name in another alphabet had no key at
-- all and every such counterparty in a Book counted as the same one, and a
-- name with accented letters lost them. What goes now is digits, spaces
-- and punctuation, which is what changes from one charge to the next.
--
-- A name written only in a to z has the same key as before. A key already
-- kept for any other name was made the old way and no longer matches its
-- activity: a rule for such a counterparty is made again by the owner, and
-- what an agent learned about it is learned again.

CREATE OR REPLACE FUNCTION public.rule_key(raw text) RETURNS text
    LANGUAGE sql IMMUTABLE STRICT SET search_path = pg_catalog AS $$
SELECT btrim(regexp_replace(lower(raw COLLATE "pg_c_utf8"), '[\x01-\x40\x5b-\x60\x7b-\xbf\u2000-\u206f\u3000-\u303f]+', ' ', 'g'))
$$;


-- Reading what a Book recorded around a day, and an account's balance, go
-- by index.
--
-- Matching a bank item to an entry already recorded looks at the entries a
-- few days either side of it. Entries had no index by that date, so each
-- item read every line ever posted to its account. Summing an account's
-- lines for its balance read each line's row; the amounts now sit in the
-- index beside the account.

CREATE INDEX entries_economic_date ON public.entries(tenant_id, book_id, economic_date);
CREATE INDEX lines_account_amounts ON public.lines(tenant_id, book_id, account_id) INCLUDE (side, amount_minor, entry_id);

-- Matching bank items to entries already recorded checks the caller's access
-- once, then reads as its owner, like the statement reads.
--
-- For each item it reads every entry a few days either side, and row security
-- checked each of those entries and lines again: on a Book with a few thousand
-- items that took seconds. What it finds is the same: for each item, the
-- entries that move its account by the same amount within the days given,
-- are not reversed, and aren't already linked on that account.

CREATE FUNCTION public.bank_entry_matches(t uuid, b uuid, ids uuid[], days integer)
    RETURNS TABLE(transaction_id uuid, entry_id uuid)
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog AS $$
#variable_conflict use_column
BEGIN
    PERFORM public.statement_read_check(t, b);
    RETURN QUERY
SELECT bt.id, m.entry_id FROM public.bank_transactions bt
 LEFT JOIN public.bank_accounts ba ON ba.tenant_id = bt.tenant_id AND ba.book_id = bt.book_id AND ba.id = bt.bank_account_id
 CROSS JOIN LATERAL (SELECT l.entry_id FROM public.entries e JOIN public.lines l ON l.tenant_id = e.tenant_id AND l.book_id = e.book_id AND l.entry_id = e.id
   WHERE e.tenant_id = bt.tenant_id AND e.book_id = bt.book_id AND e.economic_date BETWEEN bt.occurred_on - days AND bt.occurred_on + days
   AND l.account_id = coalesce(ba.account_id, bt.account_id)
   AND e.reversal_of IS NULL AND NOT EXISTS(SELECT 1 FROM public.entries r WHERE r.tenant_id = e.tenant_id AND r.book_id = e.book_id AND r.reversal_of = e.id)
   AND NOT EXISTS(SELECT 1 FROM public.bank_transaction_links k WHERE k.tenant_id = e.tenant_id AND k.book_id = e.book_id
     AND k.entry_id = e.id AND k.account_id = coalesce(ba.account_id, bt.account_id) AND k.revoked_at IS NULL)
   GROUP BY l.entry_id HAVING count(*) = count(public.statement_line_amount(l.tenant_id, l.book_id, l.id, bt.currency, bt.scale))
   AND sum(public.statement_line_amount(l.tenant_id, l.book_id, l.id, bt.currency, bt.scale)) = bt.amount_minor) m
 WHERE bt.tenant_id = t AND bt.book_id = b AND bt.id = ANY(ids);
END $$;
REVOKE ALL ON FUNCTION public.bank_entry_matches(uuid, uuid, uuid[], integer) FROM PUBLIC;
