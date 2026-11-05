package access_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func (f *fixture) client(t *testing.T) access.Client {
	t.Helper()
	c, e := f.service.RegisterClient(f.db.Context, "Example agent", []string{"http://127.0.0.1:53921/callback"})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func (f *fixture) authorization(t *testing.T, c access.Client, permission string) (string, string) {
	t.Helper()
	verifier := access.Random()
	request, e := f.service.StartAuthorization(f.db.Context, access.Authorization{ClientID: c.ID, RedirectURI: c.Redirects[0], Resource: f.service.Resource(), Challenge: access.Challenge(verifier), State: "example-state", Access: permission})
	if e != nil {
		t.Fatal(e)
	}
	return request, verifier
}
func (f *fixture) consent(t *testing.T, c access.Client, session, tenant, book, permission, existing string) (string, url.Values) {
	t.Helper()
	request, verifier := f.authorization(t, c, permission)
	destination, e := f.service.ConsentAuthorization(f.db.Context, session, request, tenant, book, permission, existing, false)
	if e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(destination)
	if e != nil || u.Query().Get("state") != "example-state" || u.Query().Get("iss") != "https://ledger.example.com" {
		t.Fatal("callback binding missing")
	}
	return request, url.Values{"grant_type": {"authorization_code"}, "client_id": {c.ID}, "resource": {f.service.Resource()}, "redirect_uri": {c.Redirects[0]}, "code": {u.Query().Get("code")}, "code_verifier": {verifier}}
}
func (f *fixture) tokens(t *testing.T, c access.Client, session, tenant, book, permission, existing string) access.Tokens {
	t.Helper()
	_, q := f.consent(t, c, session, tenant, book, permission, existing)
	tokens, e := f.service.Exchange(f.db.Context, q)
	if e != nil {
		t.Fatal(e)
	}
	return tokens
}
func (f *fixture) callAgent(ctx context.Context, token, method string, input any) (json.RawMessage, error) {
	scope, e := f.service.ResolveAgent(ctx, token)
	if e != nil {
		return nil, e
	}
	body, _ := json.Marshal(input)
	return (ledger.Store{Runtime: f.db.Runtime, Authorize: f.service.AgentAuthorizer(token)}).Call(ctx, scope, method, body)
}

func TestRemoteCredentialsEnforceScopeRotationAndReplay(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	other, otherSession, _ := f.owner(t, "beta")
	book, foreign := f.book(t, tenant, session), f.book(t, other, otherSession)
	client := f.client(t)
	request, _ := f.authorization(t, client, "write")
	if _, e := f.service.ConsentAuthorization(ctx, session, request, tenant, foreign.ID, "write", "", false); !errors.Is(e, access.ErrDenied) {
		t.Fatal("foreign Book consent accepted")
	}
	tokens := f.tokens(t, client, session, tenant, book.ID, "write", "")
	scope, e := f.service.ResolveAgent(ctx, tokens.Access)
	if e != nil || scope.TenantID != tenant || scope.BookID != book.ID || scope.Access != "write" {
		t.Fatal("credential scope incorrect", e)
	}
	accountInput := ledger.CreateAccount{RequestKey: "example-account", Class: "asset", Code: "1000", Name: "Example cash"}
	first, e := f.callAgent(ctx, tokens.Access, "create_account", accountInput)
	if e != nil {
		t.Fatal(e)
	}
	// Knowing all connection IDs without presenting a credential gives no data.
	if _, e = f.db.Store.Call(ctx, scope, "get_book", json.RawMessage(`{}`)); !errors.Is(e, ledger.ErrDenied) {
		t.Fatal("remote connection accepted without its credential")
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens.Refresh}, "client_id": {client.ID}, "resource": {f.service.Resource()}}
	rotated, e := f.service.Exchange(ctx, refresh)
	if e != nil {
		t.Fatal(e)
	}
	second, e := f.callAgent(ctx, rotated.Access, "create_account", accountInput)
	if e != nil || string(first) != string(second) {
		t.Fatal("rotation changed actor or retry result", e)
	}
	if _, e = f.service.Exchange(ctx, refresh); !errors.Is(e, access.ErrDenied) {
		t.Fatal("refresh replay accepted")
	}
	if _, e = f.callAgent(ctx, rotated.Access, "get_book", struct{}{}); !errors.Is(e, access.ErrDenied) {
		t.Fatal("refresh replay left descendant credential active")
	}
	// Fresh consent can reconnect the same actor after revocation.
	reconnected := f.tokens(t, client, session, tenant, book.ID, "write", scope.ConnectionID)
	newScope, e := f.service.ResolveAgent(ctx, reconnected.Access)
	if e != nil || newScope.ActorID != scope.ActorID || newScope.ConnectionID != scope.ConnectionID {
		t.Fatal("reconnect replaced durable actor", e)
	}
	second, e = f.callAgent(ctx, reconnected.Access, "create_account", accountInput)
	if e != nil || string(first) != string(second) {
		t.Fatal("reconnect lost retained outcomes", e)
	}
	if _, e = f.callAgent(ctx, tokens.Access, "get_book", struct{}{}); !errors.Is(e, access.ErrDenied) {
		t.Fatal("old generation survived reconnect")
	}
}

func TestRemoteConsentStagesPermissionUntilSuccessfulExchange(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	book := f.book(t, tenant, session)
	client := f.client(t)
	read := f.tokens(t, client, session, tenant, book.ID, "read", "")
	scope, e := f.service.ResolveAgent(ctx, read.Access)
	if e != nil {
		t.Fatal(e)
	}
	_, exchange := f.consent(t, client, session, tenant, book.ID, "write", scope.ConnectionID)
	if _, e = f.callAgent(ctx, read.Access, "create_account", ledger.CreateAccount{RequestKey: "attempt", Class: "asset", Code: "1000", Name: "Example"}); !errors.Is(e, ledger.ErrDenied) {
		t.Fatal("unexchanged consent granted writes")
	}
	wrong := exchange.Clone()
	wrong.Set("code_verifier", access.Random())
	if _, e = f.service.Exchange(ctx, wrong); !errors.Is(e, access.ErrDenied) {
		t.Fatal("wrong PKCE proof accepted")
	}
	if _, e = f.callAgent(ctx, read.Access, "get_book", struct{}{}); e != nil {
		t.Fatal("failed exchange disrupted old connection", e)
	}
	write, e := f.service.Exchange(ctx, exchange)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.callAgent(ctx, write.Access, "create_account", ledger.CreateAccount{RequestKey: "attempt", Class: "asset", Code: "1000", Name: "Example"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.Exchange(ctx, exchange); !errors.Is(e, access.ErrDenied) {
		t.Fatal("authorization code replayed")
	}
	if _, e = f.callAgent(ctx, write.Access, "get_book", struct{}{}); !errors.Is(e, access.ErrDenied) {
		t.Fatal("code replay left credentials active")
	}
}

func TestRemoteProofBindingAndOwnerRecovery(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, id := f.owner(t, "alpha")
	book := f.book(t, tenant, session)
	client := f.client(t)
	otherClient := f.client(t)
	_, exchange := f.consent(t, client, session, tenant, book.ID, "write", "")
	for _, field := range []string{"client_id", "resource", "redirect_uri", "code_verifier"} {
		t.Run(field, func(t *testing.T) {
			q := exchange.Clone()
			switch field {
			case "client_id":
				q.Set(field, otherClient.ID)
			case "resource":
				q.Set(field, "https://other.example.com/mcp")
			case "redirect_uri":
				q.Set(field, "http://127.0.0.1:53921/elsewhere")
			case "code_verifier":
				q.Set(field, access.Random())
			}
			if _, e := f.service.Exchange(ctx, q); !errors.Is(e, access.ErrDenied) {
				t.Fatal("unbound credential exchange accepted")
			}
		})
	}
	tokens, e := f.service.Exchange(ctx, exchange)
	if e != nil {
		t.Fatal(e)
	}
	scope, e := f.service.ResolveAgent(ctx, tokens.Access)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.operator.RevokeIdentity(ctx, id); e != nil {
		t.Fatal(e)
	}
	if _, e = f.service.ResolveAgent(ctx, tokens.Access); !errors.Is(e, access.ErrDenied) {
		t.Fatal("identity recovery retained bearer authority")
	}
	// A pre-resolved scope is still rejected inside the financial transaction.
	if _, e = (ledger.Store{Runtime: f.db.Runtime, Authorize: f.service.AgentAuthorizer(tokens.Access)}).Call(ctx, scope, "get_book", json.RawMessage(`{}`)); !errors.Is(e, access.ErrDenied) {
		t.Fatal("pre-resolved credential survived recovery")
	}
}

func TestRemoteCredentialLeaseSerializesIdentityRevocation(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, id := f.owner(t, "alpha")
	book := f.book(t, tenant, session)
	client := f.client(t)
	tokens := f.tokens(t, client, session, tenant, book.ID, "write", "")
	scope, e := f.service.ResolveAgent(ctx, tokens.Access)
	if e != nil {
		t.Fatal(e)
	}
	acquired := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	revoked := make(chan error, 1)
	go func() {
		s := ledger.Store{Runtime: f.db.Runtime, Authorize: func(ctx context.Context, tx pgx.Tx, s ledger.Scope, m string, b json.RawMessage) error {
			if e := f.service.AgentAuthorizer(tokens.Access)(ctx, tx, s, m, b); e != nil {
				return e
			}
			close(acquired)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
		_, e := s.Call(ctx, scope, "get_book", json.RawMessage(`{}`))
		finished <- e
	}()
	select {
	case <-acquired:
	case e := <-finished:
		t.Fatal("credential lease failed", e)
	case <-time.After(5 * time.Second):
		t.Fatal("credential lease timed out")
	}
	go func() { revoked <- f.operator.RevokeIdentity(ctx, id) }()
	select {
	case e := <-revoked:
		close(release)
		t.Fatal("revocation did not wait for the active operation", e)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if e := <-finished; e != nil {
		t.Fatal(e)
	}
	if e := <-revoked; e != nil {
		t.Fatal(e)
	}
	if _, e = f.callAgent(ctx, tokens.Access, "get_book", struct{}{}); !errors.Is(e, access.ErrDenied) {
		t.Fatal("revoked credential started a later operation")
	}
}

func TestRemoteDatabaseRejectsStaleSnapshotAndExcessConsent(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	book := f.book(t, tenant, session)
	client := f.client(t)
	request, _ := f.authorization(t, client, "read")
	if _, e := f.service.ConsentAuthorization(ctx, session, request, tenant, book.ID, "write", "", false); !errors.Is(e, access.ErrDenied) {
		t.Fatal("read request gained write consent")
	}
	tokens := f.tokens(t, client, session, tenant, book.ID, "read", "")
	scope, e := f.service.ResolveAgent(ctx, tokens.Access)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := f.db.Runtime.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(context.Background())
	if e = f.service.AgentAuthorizer(tokens.Access)(ctx, tx, scope, "get_book", json.RawMessage(`{}`)); !errors.Is(e, access.ErrDenied) {
		t.Fatal("remote credential lease accepted a stale-snapshot isolation level")
	}
}

func (f *fixture) abandonedClients(t *testing.T, count int) {
	t.Helper()
	_, err := f.db.Admin.Exec(f.db.Context, `
		INSERT INTO access.oauth_clients(id,name,redirects,created_at)
		SELECT lpad(n::text,43,'0'),'Example agent',ARRAY['http://127.0.0.1:53921/callback'],
		clock_timestamp()-interval '25 hours' FROM generate_series(1,$1::integer) n`, count)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationReclaimsAbandonedCapacity(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	f.abandonedClients(t, 4096)
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO access.oauth_requests(request_hash,client_id,redirect_uri,resource,challenge,state,requested_access,expires_at)
		SELECT lpad(n::text,64,'0'),lpad(n::text,43,'0'),'http://127.0.0.1:53921/callback',
		'https://ledger.example.com/mcp',repeat('a',43),'','read',clock_timestamp()-interval '20 minutes'
		FROM generate_series(1,100) n`); err != nil {
		t.Fatal(err)
	}
	client := f.client(t)
	f.authorization(t, client, "read")
	var clients, requests int
	if err := f.db.Admin.QueryRow(ctx, "SELECT (SELECT count(*) FROM access.oauth_clients),(SELECT count(*) FROM access.oauth_requests)").Scan(&clients, &requests); err != nil || clients != 3969 || requests != 1 {
		t.Fatal("registration did not reclaim a bounded batch of expired dependencies and unused clients", clients, requests, err)
	}
}

func TestRegistrationReclamationMakesProgressWhenFull(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	f.abandonedClients(t, 4096)
	if _, err := f.db.Admin.Exec(ctx, "UPDATE access.oauth_clients SET created_at=clock_timestamp()-interval '23 hours' WHERE id<>lpad('1',43,'0')"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO access.oauth_requests(request_hash,client_id,redirect_uri,resource,challenge,state,requested_access,expires_at)
		SELECT lpad(n::text,64,'0'),lpad('1',43,'0'),'http://127.0.0.1:53921/callback',
		'https://ledger.example.com/mcp',repeat('a',43),'','read',clock_timestamp()-interval '20 minutes'
		FROM generate_series(1,101) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RegisterClient(ctx, "Example agent", []string{"http://127.0.0.1:53921/callback"}); !errors.Is(err, access.ErrDenied) {
		t.Fatal("full registration unexpectedly succeeded", err)
	}
	var requests int
	if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM access.oauth_requests").Scan(&requests); err != nil || requests != 1 {
		t.Fatal("capacity denial rolled back bounded reclamation", requests, err)
	}
	f.client(t)
	var clients int
	if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM access.oauth_clients").Scan(&clients); err != nil || clients != 4096 {
		t.Fatal("reclamation did not recover a client slot", clients, err)
	}
}

func TestRegistrationPreservesHourlyAndRetainedCapacityLimits(t *testing.T) {
	for _, test := range []struct {
		name   string
		recent int
		age    string
	}{{"hourly", 128, "30 minutes"}, {"retained", 4096, "23 hours"}} {
		t.Run(test.name, func(t *testing.T) {
			f := fresh(t)
			ctx := f.db.Context
			f.abandonedClients(t, 4096)
			if _, err := f.db.Admin.Exec(ctx, "UPDATE access.oauth_clients SET created_at=clock_timestamp()-$1::interval WHERE id<=lpad($2::integer::text,43,'0')", test.age, test.recent); err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.RegisterClient(ctx, "Example agent", []string{"http://127.0.0.1:53921/callback"}); !errors.Is(err, access.ErrDenied) {
				t.Fatal("registration exceeded its capacity limit", err)
			}
			var clients int
			expected := 4096
			if test.recent == 128 {
				expected -= 128
			}
			if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM access.oauth_clients").Scan(&clients); err != nil || clients != expected {
				t.Fatal("denied registration did not preserve retained clients and bound cleanup", clients, err)
			}
			if test.recent == 128 {
				if _, err := f.db.Admin.Exec(ctx, "UPDATE access.oauth_clients SET created_at=clock_timestamp()-interval '2 hours' WHERE id=lpad('1',43,'0')"); err != nil {
					t.Fatal(err)
				}
				f.client(t)
				if _, err := f.service.RegisterClient(ctx, "Example agent", []string{"http://127.0.0.1:53921/callback"}); !errors.Is(err, access.ErrDenied) {
					t.Fatal("cleanup reset the hourly registration budget", err)
				}
			}
		})
	}
}

func TestRegistrationPreservesGrantsConsentAndCodeReplay(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	tenant, session, _ := f.owner(t, "alpha")
	book := f.book(t, tenant, session)
	active := f.client(t)
	_, exchange := f.consent(t, active, session, tenant, book.ID, "write", "")
	tokens, err := f.service.Exchange(ctx, exchange)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.callAgent(ctx, tokens.Access, "create_account", ledger.CreateAccount{RequestKey: "preserved", Class: "asset", Code: "1000", Name: "Example cash"})
	if err != nil {
		t.Fatal(err)
	}
	pending := f.client(t)
	request, verifier := f.authorization(t, pending, "read")
	recent := f.client(t)
	recentRequest, _ := f.authorization(t, recent, "read")
	if _, err = f.db.Admin.Exec(ctx, "UPDATE access.oauth_requests SET expires_at=clock_timestamp()-interval '10 minutes' WHERE request_hash=$1", access.Hash(recentRequest)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Admin.Exec(ctx, "UPDATE access.oauth_clients SET created_at=clock_timestamp()-interval '25 hours'"); err != nil {
		t.Fatal(err)
	}
	f.abandonedClients(t, 4093)
	f.client(t)
	var retainedRequest int
	if err = f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM access.oauth_requests WHERE client_id=$1", recent.ID).Scan(&retainedRequest); err != nil || retainedRequest != 1 {
		t.Fatal("cleanup shortened expired-request retention", err)
	}
	if _, err = f.service.PendingAuthorization(ctx, request); err != nil {
		t.Fatal("cleanup removed pending consent", err)
	}
	destination, err := f.service.ConsentAuthorization(ctx, session, request, tenant, book.ID, "read", "", false)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {pending.ID}, "resource": {f.service.Resource()}, "redirect_uri": {pending.Redirects[0]}, "code": {callback.Query().Get("code")}, "code_verifier": {verifier}}); err != nil {
		t.Fatal("preserved consent could not complete", err)
	}
	after, err := f.callAgent(ctx, tokens.Access, "create_account", ledger.CreateAccount{RequestKey: "preserved", Class: "asset", Code: "1000", Name: "Example cash"})
	if err != nil || string(before) != string(after) {
		t.Fatal("cleanup changed an active grant or retained outcome", err)
	}
	if _, err = f.service.Exchange(ctx, exchange); !errors.Is(err, access.ErrDenied) {
		t.Fatal("cleanup removed consumed-code replay protection", err)
	}
	if _, err = f.service.ResolveAgent(ctx, tokens.Access); !errors.Is(err, access.ErrDenied) {
		t.Fatal("code replay failed to revoke the existing generation", err)
	}
	if _, err = f.db.Admin.Exec(ctx, "UPDATE access.oauth_requests SET expires_at=clock_timestamp()-interval '20 minutes' WHERE client_id=$1", active.ID); err != nil {
		t.Fatal(err)
	}
	f.client(t)
	var retained int
	if err = f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM access.oauth_clients WHERE id=$1", active.ID).Scan(&retained); err != nil || retained != 1 {
		t.Fatal("cleanup discarded a revoked client's grant", err)
	}
}

type oauthExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func TestRegistrationAndAuthorizationSerializeClientLifetime(t *testing.T) {
	for _, first := range []string{"authorization", "cleanup", "hourly limit"} {
		t.Run(first, func(t *testing.T) {
			f := fresh(t)
			ctx := f.db.Context
			client := f.client(t)
			if _, err := f.db.Admin.Exec(ctx, "UPDATE access.oauth_clients SET created_at=clock_timestamp()-interval '25 hours'"); err != nil {
				t.Fatal(err)
			}
			if first == "hourly limit" {
				f.abandonedClients(t, 126)
				if _, err := f.db.Admin.Exec(ctx, "UPDATE access.oauth_clients SET created_at=clock_timestamp()"); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := f.auth.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			start := func(q oauthExecutor) error {
				_, err := q.Exec(ctx, "SELECT access.start_oauth($1,$2,$3,$4,$5,'','read',$6)", access.Hash(access.Random()), client.ID, client.Redirects[0], f.service.Resource(), access.Challenge(access.Random()), "https://ledger.example.com")
				return err
			}
			register := func(q oauthExecutor) error {
				var registered bool
				if err := q.QueryRow(ctx, "SELECT access.register_client($1,'Example agent',$2)", access.Random(), client.Redirects).Scan(&registered); err != nil {
					return err
				}
				if !registered {
					return access.ErrDenied
				}
				return nil
			}
			leading, waiting := start, register
			if first == "cleanup" {
				leading, waiting = register, start
			}
			if first == "hourly limit" {
				leading, waiting = register, register
			}
			if err = leading(tx); err != nil {
				t.Fatal(err)
			}
			// A separate physical connection uses the same restricted LOGIN.
			other, err := pgx.ConnectConfig(ctx, f.auth.Config().ConnConfig.Copy())
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close(context.Background())
			done := make(chan error, 1)
			go func() { done <- waiting(other) }()
			blocked := false
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				if err := f.db.Admin.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)", other.PgConn().PID()).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case err := <-done:
					t.Fatal("client lifetime operation bypassed the transaction lock", err)
				case <-time.After(10 * time.Millisecond):
				}
			}
			if !blocked {
				t.Fatal("concurrent client operation never waited on the database lock")
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if first == "authorization" && err != nil {
				t.Fatal("cleanup did not preserve the committed request", err)
			}
			if first == "cleanup" {
				var databaseError *pgconn.PgError
				if !errors.As(err, &databaseError) || databaseError.Code != "42501" {
					t.Fatal("expired client did not fail cleanly after reclamation", err)
				}
			}
			if first == "hourly limit" {
				if !errors.Is(err, access.ErrDenied) {
					t.Fatal("concurrent registration did not enforce the hourly limit", err)
				}
				var clients int
				if err := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM access.oauth_clients").Scan(&clients); err != nil || clients != 128 {
					t.Fatal("concurrent registration exceeded the hourly limit", clients, err)
				}
			}
		})
	}
}

func TestClientLifetimeRejectsStaleTransactionSnapshots(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	client := f.client(t)
	for _, isolation := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
		for _, operation := range []string{"registration", "authorization"} {
			t.Run(string(isolation)+"/"+operation, func(t *testing.T) {
				tx, err := f.auth.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				if operation == "registration" {
					_, err = tx.Exec(ctx, "SELECT access.register_client($1,'Example agent',$2)", access.Random(), client.Redirects)
				} else {
					_, err = tx.Exec(ctx, "SELECT access.start_oauth($1,$2,$3,$4,$5,'','read',$6)", access.Hash(access.Random()), client.ID, client.Redirects[0], f.service.Resource(), strings.Repeat("a", 43), "https://ledger.example.com")
				}
				var databaseError *pgconn.PgError
				if !errors.As(err, &databaseError) || databaseError.Code != "25001" {
					t.Fatal("client lifetime accepted a stale-snapshot isolation level", err)
				}
			})
		}
	}
}
