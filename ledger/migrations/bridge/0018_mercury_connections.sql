-- Banks connected directly, not through Plaid: Mercury, read with the
-- owner's own read-only token. The first sync's checks that each account's
-- activity adds up to the balance the bank reports are kept, so the owner
-- sees what was found.
ALTER TABLE public.bank_connections DROP CONSTRAINT bank_connections_provider_check;
ALTER TABLE public.bank_connections ADD CONSTRAINT bank_connections_provider_check CHECK (provider IN ('plaid', 'mercury'));
ALTER TABLE public.bank_connections ADD COLUMN first_sync_checks jsonb
    CHECK (first_sync_checks IS NULL OR jsonb_typeof(first_sync_checks) = 'array' AND octet_length(first_sync_checks::text) <= 65536);
