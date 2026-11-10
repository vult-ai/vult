package access_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestPasskeyConcurrentInitializationCreatesOneOwner(t *testing.T) {
	f := fresh(t)
	if f.db.Admin.Config().MaxConns < 4 {
		t.Fatal("concurrent setup fixture requires four isolated connections")
	}
	var before int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.tenants").Scan(&before); err != nil {
		t.Fatal(err)
	}
	const barrier int64 = 749305129
	if _, err := f.db.Admin.Exec(f.db.Context, `CREATE FUNCTION public.wait_initialization() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN PERFORM pg_advisory_xact_lock(749305129); RETURN NEW; END $$;
 CREATE TRIGGER wait_initialization BEFORE INSERT ON public.tenants FOR EACH ROW EXECUTE FUNCTION public.wait_initialization()`); err != nil {
		t.Fatal(err)
	}
	held, err := f.db.Admin.Acquire(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if _, err := held.Exec(f.db.Context, "SELECT pg_advisory_lock($1)", barrier); err != nil {
		t.Fatal(err)
	}
	var unlock sync.Once
	release := func() {
		unlock.Do(func() {
			if _, err := held.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", barrier); err != nil {
				t.Error("initialization barrier cleanup failed")
			}
		})
	}
	ctx, cancel := context.WithTimeout(f.db.Context, 5*time.Second)
	defer cancel()
	var wait sync.WaitGroup
	type result struct {
		setup access.PasskeySetup
		err   error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			setup, err := f.operator.InitializePasskeys(ctx, "", "racing-owner", "https://ledger.example.com")
			results <- result{setup, err}
		}()
	}
	defer func() { release(); wait.Wait() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	blocked := false
	for !blocked {
		var count int
		if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND classid=0 AND objid::bigint=$1 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())", barrier).Scan(&count); err != nil {
			release()
			cancel()
			wait.Wait()
			t.Fatal(err)
		}
		blocked = count == 2
		if !blocked {
			select {
			case <-ctx.Done():
				release()
				cancel()
				wait.Wait()
				t.Fatal("both initializers did not reach the barrier")
			case <-ticker.C:
			}
		}
	}
	release()
	wait.Wait()
	close(results)
	winners := 0
	var accepted access.PasskeySetup
	for result := range results {
		if result.err == nil {
			winners++
			accepted = result.setup
		} else if !errors.Is(result.err, access.ErrDenied) {
			t.Fatal(result.err)
		}
	}
	if winners != 1 {
		t.Fatal("initialization did not have one accepted result", winners)
	}
	var after, profiles, setups int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.tenants").Scan(&after); err != nil || after != before+1 {
		t.Fatal("concurrent initialization retained an extra tenant", err)
	}
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM access.passkey_profiles").Scan(&profiles); err != nil || profiles != 1 {
		t.Fatal("concurrent initialization retained extra profiles", err)
	}
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM access.passkey_setups WHERE consumed_at IS NULL").Scan(&setups); err != nil || setups != 1 {
		t.Fatal("concurrent initialization replaced the accepted setup", err)
	}
	p, err := access.NewPasskeys(f.db.Context, f.service, "racing-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.BeginRegistration(f.db.Context, accepted.Token, access.Random()); err != nil {
		t.Fatal("winning setup was invalidated", err)
	}
}

type passkeyAuthenticator struct {
	key   *ecdsa.PrivateKey
	id    []byte
	user  string
	flags byte
}

func newPasskeyAuthenticator(t *testing.T) *passkeyAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return &passkeyAuthenticator{key: key, id: id, flags: 5}
}
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
func passkeyOptions(t *testing.T, challenge access.PasskeyChallenge) (string, string) {
	t.Helper()
	var options struct {
		PublicKey struct {
			Challenge              string
			User                   struct{ ID string }
			UserVerification       string
			AuthenticatorSelection struct {
				UserVerification string
				ResidentKey      string
			}
		}
	}
	if json.Unmarshal(challenge.Options, &options) != nil || options.PublicKey.Challenge == "" {
		t.Fatal("invalid passkey options")
	}
	if options.PublicKey.User.ID != "" {
		if options.PublicKey.AuthenticatorSelection.UserVerification != "required" || options.PublicKey.AuthenticatorSelection.ResidentKey != "required" {
			t.Fatal("registration does not require a user-verified resident credential")
		}
	} else if options.PublicKey.UserVerification != "required" {
		t.Fatal("login does not require user verification")
	}
	return options.PublicKey.Challenge, options.PublicKey.User.ID
}
func (a *passkeyAuthenticator) registration(t *testing.T, challenge access.PasskeyChallenge, origin, rp string) json.RawMessage {
	t.Helper()
	value, user := passkeyOptions(t, challenge)
	a.user = user
	client, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": value, "origin": origin, "crossOrigin": false})
	public, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: a.key.X.FillBytes(make([]byte, 32)), -3: a.key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(rp))
	auth := append(hash[:], a.flags|0x40)
	auth = append(auth, make([]byte, 4+16)...)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(a.id)))
	auth = append(auth, length...)
	auth = append(auth, a.id...)
	auth = append(auth, public...)
	attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "authData": auth, "attStmt": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"id": b64(a.id), "rawId": b64(a.id), "type": "public-key", "authenticatorAttachment": "platform", "response": map[string]any{"clientDataJSON": b64(client), "attestationObject": b64(attestation), "transports": []string{"internal"}}, "clientExtensionResults": map[string]any{}})
	return data
}
func (a *passkeyAuthenticator) assertion(t *testing.T, challenge access.PasskeyChallenge, origin, rp string, counter uint32, change func(map[string]any)) json.RawMessage {
	t.Helper()
	value, _ := passkeyOptions(t, challenge)
	client := map[string]any{"type": "webauthn.get", "challenge": value, "origin": origin, "crossOrigin": false}
	if change != nil {
		change(client)
	}
	clientBytes, _ := json.Marshal(client)
	hash := sha256.Sum256([]byte(rp))
	auth := append(hash[:], a.flags)
	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, counter)
	auth = append(auth, count...)
	clientHash := sha256.Sum256(clientBytes)
	signed := append(append([]byte{}, auth...), clientHash[:]...)
	digest := sha256.Sum256(signed)
	signature, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"id": b64(a.id), "rawId": b64(a.id), "type": "public-key", "response": map[string]any{"clientDataJSON": b64(clientBytes), "authenticatorData": b64(auth), "signature": b64(signature), "userHandle": a.user}, "clientExtensionResults": map[string]any{}})
	return data
}

// enrolledPasskey is an operator's setup and the recovery codes its owner then
// received in the browser.
type enrolledPasskey struct {
	access.PasskeySetup
	RecoveryCodes []string
}

func enrollPasskey(t *testing.T, f *fixture) (*access.Passkeys, enrolledPasskey, *passkeyAuthenticator, string, string) {
	t.Helper()
	setup, err := f.operator.InitializePasskeys(f.db.Context, "", "example-owner", "https://ledger.example.com")
	if err != nil {
		t.Fatal("prepare passkey", err)
	}
	p, err := access.NewPasskeys(f.db.Context, f.service, "example-owner")
	if err != nil {
		t.Fatal(err)
	}
	browser := access.Random()
	challenge, err := p.BeginRegistration(f.db.Context, setup.Token, browser)
	if err != nil {
		t.Fatal("begin registration", err)
	}
	a := newPasskeyAuthenticator(t)
	session, err := p.FinishRegistration(f.db.Context, challenge.Token, browser, a.registration(t, challenge, "https://ledger.example.com", "ledger.example.com"))
	if err != nil {
		t.Fatal("finish registration", err)
	}
	codes, err := p.IssueRecoveryCodes(f.db.Context, session)
	if err != nil || len(codes) != 8 {
		t.Fatal("the owner did not receive recovery codes after setup", err)
	}
	return p, enrolledPasskey{setup, codes}, a, session, browser
}

func TestPasskeySignedLifecycleRecoveryAndPreservedBooks(t *testing.T) {
	f := fresh(t)
	p, setup, a, session, browser := enrollPasskey(t, f)
	if len(setup.RecoveryCodes) != 8 {
		t.Fatal("recovery codes not generated")
	}
	id, err := f.service.Identity(f.db.Context, session)
	if err != nil || id.Issuer != access.BuiltinIssuer || id.Subject != "example-owner" {
		t.Fatal("passkey did not establish the bound identity", err)
	}
	book := f.book(t, setup.TenantID, session)
	before, err := f.db.Owner.Snapshot(f.db.Context)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := p.BeginLogin(f.db.Context, browser)
	if err != nil {
		t.Fatal(err)
	}
	proof := a.assertion(t, challenge, "https://ledger.example.com", "ledger.example.com", 1, nil)
	login, err := p.FinishLogin(f.db.Context, challenge.Token, browser, proof)
	if err != nil {
		t.Fatal("signed login failed", err)
	}
	if _, err := f.service.Identity(f.db.Context, login); err != nil {
		t.Fatal("login session unavailable", err)
	}
	if _, err := p.FinishLogin(f.db.Context, challenge.Token, browser, proof); !errors.Is(err, access.ErrDenied) {
		t.Fatal("assertion replay succeeded", err)
	}
	if _, err := f.operator.InitializePasskeys(f.db.Context, setup.TenantID, "example-owner", "https://ledger.example.com"); !errors.Is(err, access.ErrDenied) {
		t.Fatal("setup reopened an enrolled profile", err)
	}
	stale, err := p.BeginLogin(f.db.Context, browser)
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := f.operator.ResetPasskeys(f.db.Context, "example-owner", "https://ledger.example.com")
	if err != nil || recovery.TenantID != setup.TenantID {
		t.Fatal("offline recovery changed ownership", err)
	}
	for _, token := range []string{session, login} {
		if _, err := f.service.Identity(f.db.Context, token); !errors.Is(err, access.ErrDenied) {
			t.Fatal("recovery retained prior session", err)
		}
	}
	if _, err := p.FinishLogin(f.db.Context, stale.Token, browser, a.assertion(t, stale, "https://ledger.example.com", "ledger.example.com", 2, nil)); !errors.Is(err, access.ErrDenied) {
		t.Fatal("pre-recovery challenge revived access", err)
	}
	if _, err := p.BeginRegistration(f.db.Context, setup.Token, browser); !errors.Is(err, access.ErrDenied) {
		t.Fatal("old setup authority revived access", err)
	}
	newChallenge, err := p.BeginRegistration(f.db.Context, recovery.Token, browser)
	if err != nil {
		t.Fatal(err)
	}
	replacement := newPasskeyAuthenticator(t)
	newSession, err := p.FinishRegistration(f.db.Context, newChallenge.Token, browser, replacement.registration(t, newChallenge, "https://ledger.example.com", "ledger.example.com"))
	if err != nil {
		t.Fatal("recovery registration failed", err)
	}
	if err := f.service.WithOwner(f.db.Context, newSession, setup.TenantID, false, func(owner ledger.TenantAdmin) error {
		got, err := owner.Book(f.db.Context, book.ID)
		if err == nil && got.ID != book.ID {
			t.Fatal("recovery changed Book identity")
		}
		return err
	}); err != nil {
		t.Fatal("recovered owner cannot read retained Book", err)
	}
	after, err := f.db.Owner.Snapshot(f.db.Context)
	if err != nil || !reflect.DeepEqual(before.Tables, after.Tables) {
		t.Fatal("authentication changed financial state", err)
	}
	var rawStored bool
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT EXISTS(SELECT 1 FROM access.passkey_setups WHERE token_hash=$1)", recovery.Token).Scan(&rawStored); err != nil || rawStored {
		t.Fatal("raw setup token retained", err)
	}
}

func TestPasskeySignedAssertionsRejectWrongContextAndConsumeFailures(t *testing.T) {
	f := fresh(t)
	p, _, a, _, browser := enrollPasskey(t, f)
	for _, fixture := range []struct {
		name, origin, rp string
		flags            byte
		change           func(map[string]any)
	}{
		{"wrong-origin", "https://other.example.com", "ledger.example.com", 5, nil},
		{"wrong-port", "https://ledger.example.com:444", "ledger.example.com", 5, nil},
		{"wrong-rp", "https://ledger.example.com", "other.example.com", 5, nil},
		{"missing-verification", "https://ledger.example.com", "ledger.example.com", 1, nil},
		{"missing-presence", "https://ledger.example.com", "ledger.example.com", 4, nil},
		{"wrong-challenge", "https://ledger.example.com", "ledger.example.com", 5, func(v map[string]any) { v["challenge"] = access.Random() }},
		{"wrong-type", "https://ledger.example.com", "ledger.example.com", 5, func(v map[string]any) { v["type"] = "webauthn.create" }},
		{"cross-origin", "https://ledger.example.com", "ledger.example.com", 5, func(v map[string]any) { v["crossOrigin"] = true; v["topOrigin"] = "https://ledger.example.com" }},
		{"changed-backup-eligibility", "https://ledger.example.com", "ledger.example.com", 13, nil},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			challenge, err := p.BeginLogin(f.db.Context, browser)
			if err != nil {
				t.Fatal(err)
			}
			a.flags = fixture.flags
			proof := a.assertion(t, challenge, fixture.origin, fixture.rp, 1, fixture.change)
			if _, err := p.FinishLogin(f.db.Context, challenge.Token, browser, proof); !errors.Is(err, access.ErrDenied) {
				t.Fatal("invalid signed assertion accepted", err)
			}
			a.flags = 5
			if _, err := p.FinishLogin(f.db.Context, challenge.Token, browser, a.assertion(t, challenge, "https://ledger.example.com", "ledger.example.com", 1, nil)); !errors.Is(err, access.ErrDenied) {
				t.Fatal("failed ceremony was reopened", err)
			}
		})
	}
	challenge, err := p.BeginLogin(f.db.Context, browser)
	if err != nil {
		t.Fatal(err)
	}
	proof := a.assertion(t, challenge, "https://ledger.example.com", "ledger.example.com", 1, nil)
	if _, err := p.FinishLogin(f.db.Context, challenge.Token, access.Random(), proof); !errors.Is(err, access.ErrDenied) {
		t.Fatal("wrong initiating browser accepted", err)
	}
	if _, err := p.FinishRegistration(f.db.Context, challenge.Token, browser, proof); !errors.Is(err, access.ErrDenied) {
		t.Fatal("wrong ceremony intent accepted", err)
	}
	if _, err := p.FinishLogin(f.db.Context, challenge.Token, browser, proof); err != nil {
		t.Fatal("wrong browser/intent consumed another browser's ceremony", err)
	}
	clone, err := p.BeginLogin(f.db.Context, browser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.FinishLogin(f.db.Context, clone.Token, browser, a.assertion(t, clone, "https://ledger.example.com", "ledger.example.com", 1, nil)); !errors.Is(err, access.ErrDenied) {
		t.Fatal("counter rollback accepted", err)
	}
	badKey := newPasskeyAuthenticator(t)
	badKey.id, badKey.user = a.id, a.user
	badSignature, err := p.BeginLogin(f.db.Context, browser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.FinishLogin(f.db.Context, badSignature.Token, browser, badKey.assertion(t, badSignature, "https://ledger.example.com", "ledger.example.com", 2, nil)); !errors.Is(err, access.ErrDenied) {
		t.Fatal("assertion signed by another key accepted", err)
	}
	wrongUser, err := p.BeginLogin(f.db.Context, browser)
	if err != nil {
		t.Fatal(err)
	}
	priorUser := a.user
	a.user = b64([]byte(ledger.ID()))
	wrongUserProof := a.assertion(t, wrongUser, "https://ledger.example.com", "ledger.example.com", 2, nil)
	a.user = priorUser
	if _, err := p.FinishLogin(f.db.Context, wrongUser.Token, browser, wrongUserProof); !errors.Is(err, access.ErrDenied) {
		t.Fatal("different user handle accepted", err)
	}
}

func TestPasskeyCompletionRechecksRevocationAndSetupOwnership(t *testing.T) {
	f := fresh(t)
	p, setup, a, session, browser := enrollPasskey(t, f)
	identity, err := f.service.Identity(f.db.Context, session)
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := p.BeginLogin(f.db.Context, browser)
	if err != nil {
		t.Fatal(err)
	}
	var who string
	var epoch int64
	var ceremony, credentials []byte
	if err := f.auth.QueryRow(f.db.Context, "SELECT identity_id::text,epoch,ceremony,credentials FROM access.take_passkey($1,$2,$3,'login')", access.Hash(challenge.Token), "https://ledger.example.com", access.Hash(browser)).Scan(&who, &epoch, &ceremony, &credentials); err != nil {
		t.Fatal(err)
	}
	var saved []json.RawMessage
	if json.Unmarshal(credentials, &saved) != nil || len(saved) != 1 {
		t.Fatal("invalid saved credential fixture")
	}
	if err := f.operator.RevokeIdentity(f.db.Context, identity); err != nil {
		t.Fatal(err)
	}
	if err := f.auth.QueryRow(f.db.Context, "SELECT access.finish_passkey($1,$2,$3,'login',$4,$5,$5,$6,$7)::text", access.Hash(challenge.Token), "https://ledger.example.com", access.Hash(browser), b64(a.id), saved[0], access.Hash(access.Random()), access.Hash(b64(a.id))).Scan(&who); err == nil {
		t.Fatal("completion ignored revocation after challenge claim")
	}
	if _, err := f.service.Identity(f.db.Context, session); !errors.Is(err, access.ErrDenied) {
		t.Fatal("revoked session remained valid", err)
	}
	var tenantsBefore, tenantsAfter int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.tenants").Scan(&tenantsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := f.operator.InitializePasskeys(f.db.Context, "", "example-owner", "https://ledger.example.com"); !errors.Is(err, access.ErrDenied) {
		t.Fatal("repeated setup silently created another tenant", err)
	}
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.tenants").Scan(&tenantsAfter); err != nil || tenantsAfter != tenantsBefore {
		t.Fatal("denied setup changed tenant records", err)
	}
	if _, err := f.operator.InitializePasskeys(f.db.Context, setup.TenantID, "different-owner", "https://ledger.example.com"); !errors.Is(err, access.ErrDenied) {
		t.Fatal("setup replaced an existing owner", err)
	}
	for _, origin := range []string{"http://localhost:1234", "https://ledger.example.com:443", "https://ledger.example.com/", "https://user@ledger.example.com", "https://127.0.0.1:1234", "https://ledger.example.com?token=x", "https://LEDGER.example.com"} {
		if _, err := f.operator.InitializePasskeys(f.db.Context, "", "new-owner", origin); !errors.Is(err, access.ErrDenied) {
			t.Fatal("ambiguous or insecure passkey origin accepted", origin, err)
		}
	}
}

func TestPasskeyConcurrentUseExpiryAndRestrictedRoles(t *testing.T) {
	f := fresh(t)
	p, _, a, _, browser := enrollPasskey(t, f)
	challenge, err := p.BeginLogin(f.db.Context, browser)
	if err != nil {
		t.Fatal(err)
	}
	proof := a.assertion(t, challenge, "https://ledger.example.com", "ledger.example.com", 1, nil)
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := p.FinishLogin(f.db.Context, challenge.Token, browser, proof)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, access.ErrDenied) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("concurrent use did not have exactly one winner", success)
	}
	expired, err := p.BeginLogin(f.db.Context, browser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.passkey_challenges SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1", access.Hash(expired.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.FinishLogin(f.db.Context, expired.Token, browser, a.assertion(t, expired, "https://ledger.example.com", "ledger.example.com", 2, nil)); !errors.Is(err, access.ErrDenied) {
		t.Fatal("expired assertion accepted", err)
	}
	for _, query := range []string{"SELECT * FROM access.passkey_credentials", "INSERT INTO access.passkey_profiles(identity_id,origin) VALUES(gen_random_uuid(),'https://other.example.com')", "UPDATE access.passkey_challenges SET consumed_at=NULL", "DELETE FROM access.passkey_events"} {
		if _, err := f.auth.Exec(f.db.Context, query); err == nil {
			t.Fatal("restricted authentication role bypassed credential controls")
		}
	}
	if _, err := f.owners.Exec(f.db.Context, "SELECT * FROM access.passkey_credentials"); err == nil {
		t.Fatal("owner data role read credentials")
	}
	if _, err := f.db.Runtime.Exec(f.db.Context, "SELECT access.passkey_profile('example-owner','https://ledger.example.com')"); err == nil {
		t.Fatal("ledger runtime reached authentication primitive")
	}
}

func TestPasskeyRegistrationRejectsUnverifiedAndZeroCounterKeysWork(t *testing.T) {
	f := fresh(t)
	setup, err := f.operator.InitializePasskeys(f.db.Context, "", "example-owner", "https://ledger.example.com")
	if err != nil {
		t.Fatal(err)
	}
	p, err := access.NewPasskeys(f.db.Context, f.service, "example-owner")
	if err != nil {
		t.Fatal(err)
	}
	browser := access.Random()
	a := newPasskeyAuthenticator(t)
	for _, flags := range []byte{1, 4} {
		challenge, err := p.BeginRegistration(f.db.Context, setup.Token, browser)
		if err != nil {
			t.Fatal(err)
		}
		a.flags = flags
		if _, err := p.FinishRegistration(f.db.Context, challenge.Token, browser, a.registration(t, challenge, "https://ledger.example.com", "ledger.example.com")); !errors.Is(err, access.ErrDenied) {
			t.Fatal("unverified registration accepted", err)
		}
	}
	a.flags = 13
	challenge, err := p.BeginRegistration(f.db.Context, setup.Token, browser)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.FinishRegistration(f.db.Context, challenge.Token, browser, a.registration(t, challenge, "https://ledger.example.com", "ledger.example.com")); err != nil {
		t.Fatal("backup-eligible registration failed", err)
	}
	for i := 0; i < 2; i++ {
		challenge, err := p.BeginLogin(f.db.Context, browser)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.FinishLogin(f.db.Context, challenge.Token, browser, a.assertion(t, challenge, "https://ledger.example.com", "ledger.example.com", 0, nil)); err != nil {
			t.Fatal("zero-counter passkey rejected", err)
		}
	}
}
