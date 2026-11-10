-- Recovery codes belong to the account owner. Setting up sign-in, an
-- operator's reset and recovery with a code no longer hand codes to whoever ran
-- them: they mark codes as owed, and the owner receives a new set once, in the
-- browser, right after signing in with the new method. Issuing that set
-- replaces every earlier code, including any an operator was given before.

ALTER TABLE access.passkey_profiles ADD COLUMN recovery_codes_owed boolean NOT NULL DEFAULT false;

-- Issues the owed codes to a session signed in within the last 10 minutes.
-- Every sign-in from before the setup, reset or recovery was revoked by it, so
-- only the owner who just finished it can receive them. Returns false when no
-- codes are owed.
CREATE FUNCTION access.issue_recovery_codes(session_token text, realm text, hashes text[]) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid;
BEGIN
    IF session_token !~ '^[a-f0-9]{64}$' OR cardinality(hashes) <> 8
        OR EXISTS (SELECT 1 FROM unnest(hashes) c WHERE c !~ '^[a-f0-9]{64}$') THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    SELECT s.id INTO who FROM access.session_identity(session_token, realm) s
    WHERE s.authenticated_at >= clock_timestamp() - interval '10 minutes'
        AND EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = s.id);
    IF who IS NULL THEN
        RAISE EXCEPTION 'authorization unavailable' USING ERRCODE = '42501';
    END IF;
    UPDATE access.passkey_profiles SET recovery_codes_owed = false
    WHERE identity_id = who AND origin = realm AND recovery_codes_owed;
    IF NOT FOUND THEN
        RETURN false;
    END IF;
    DELETE FROM access.recovery_codes WHERE identity_id = who;
    INSERT INTO access.recovery_codes(identity_id, code_hash) SELECT who, c FROM unnest(hashes) c;
    RETURN true;
END $$;
REVOKE ALL ON FUNCTION access.issue_recovery_codes(text,text,text[]) FROM PUBLIC;

CREATE OR REPLACE FUNCTION access.recover_passkey(sub text, realm text, identity uuid, recovery text,
    setup text, browser text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
DECLARE who uuid; generation bigint; profile access.passkey_profiles%ROWTYPE;
BEGIN
    SELECT i.id, i.epoch INTO who, generation FROM access.identities i
    WHERE i.id = identity AND i.issuer = 'urn:vult:builtin' AND i.subject = sub
        AND EXISTS (SELECT 1 FROM access.owners o WHERE o.identity_id = i.id) FOR UPDATE OF i;
    SELECT * INTO profile FROM access.passkey_profiles WHERE identity_id = who AND origin = realm FOR UPDATE;
    IF who IS NULL OR profile.identity_id IS NULL OR recovery IS NULL OR setup IS NULL OR browser IS NULL
        OR recovery !~ '^[a-f0-9]{64}$'
        OR setup !~ '^[a-f0-9]{64}$' OR browser !~ '^[a-f0-9]{64}$'
        OR (profile.attempt_until > clock_timestamp() AND profile.attempt_count >= 32) THEN
        RETURN false;
    END IF;
    -- Invalid codes consume the durable budget too; the caller commits this
    -- statement before starting a replacement credential ceremony.
    UPDATE access.passkey_profiles SET
        attempt_count = CASE WHEN attempt_until <= clock_timestamp() THEN 1 ELSE attempt_count + 1 END,
        attempt_until = CASE WHEN attempt_until <= clock_timestamp() THEN clock_timestamp() + interval '5 minutes' ELSE attempt_until END
    WHERE identity_id = who;
    DELETE FROM access.recovery_codes WHERE identity_id = who AND code_hash = recovery;
    IF NOT FOUND THEN RETURN false; END IF;
    UPDATE access.identities SET epoch = epoch + 1 WHERE id = who RETURNING epoch INTO generation;
    UPDATE access.passkey_credentials SET revoked_at = clock_timestamp() WHERE identity_id = who AND revoked_at IS NULL;
    UPDATE access.passkey_setups SET consumed_at = clock_timestamp() WHERE identity_id = who AND consumed_at IS NULL;
    UPDATE access.passkey_profiles SET state = 'pending', attempt_count = 0, attempt_until = clock_timestamp()
    WHERE identity_id = who;
    INSERT INTO access.passkey_setups(token_hash, identity_id, epoch, browser_hash) VALUES (setup, who, generation, browser);
    -- The replacement sign-in comes with a fresh set of codes.
    UPDATE access.passkey_profiles SET recovery_codes_owed = true WHERE identity_id = who;
    INSERT INTO access.passkey_events(identity_id, kind) VALUES (who, 'recovery');
    RETURN true;
END $$;
