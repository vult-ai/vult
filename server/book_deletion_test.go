package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

// A synthetic passkey: an ECDSA key that signs WebAuthn assertions the way
// a platform authenticator does, with user presence and verification.
type testPasskey struct {
	key   *ecdsa.PrivateKey
	id    []byte
	user  string
	count uint32
	mu    sync.Mutex
}

func newTestPasskey(t *testing.T) *testPasskey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	_, _ = rand.Read(id)
	return &testPasskey{key: key, id: id}
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func passkeyChallengeValue(t *testing.T, options json.RawMessage) (string, string) {
	t.Helper()
	var o struct {
		PublicKey struct {
			Challenge        string
			UserVerification string
			User             struct{ ID string }
		}
	}
	if json.Unmarshal(options, &o) != nil || o.PublicKey.Challenge == "" {
		t.Fatal("invalid passkey options")
	}
	return o.PublicKey.Challenge, o.PublicKey.User.ID
}

func (p *testPasskey) register(t *testing.T, options json.RawMessage, origin, rp string) json.RawMessage {
	t.Helper()
	value, user := passkeyChallengeValue(t, options)
	p.user = user
	client, _ := json.Marshal(map[string]any{"type": "webauthn.create", "challenge": value, "origin": origin, "crossOrigin": false})
	public, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: p.key.X.FillBytes(make([]byte, 32)), -3: p.key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(rp))
	auth := append(hash[:], 5|0x40)
	auth = append(auth, make([]byte, 4+16)...)
	length := make([]byte, 2)
	binary.BigEndian.PutUint16(length, uint16(len(p.id)))
	auth = append(append(auth, length...), p.id...)
	auth = append(auth, public...)
	attestation, _ := cbor.Marshal(map[string]any{"fmt": "none", "authData": auth, "attStmt": map[string]any{}})
	data, _ := json.Marshal(map[string]any{"id": b64url(p.id), "rawId": b64url(p.id), "type": "public-key", "authenticatorAttachment": "platform",
		"response": map[string]any{"clientDataJSON": b64url(client), "attestationObject": b64url(attestation), "transports": []string{"internal"}}, "clientExtensionResults": map[string]any{}})
	return data
}

// assert answers a challenge. flags 5 is user present and verified; origin
// is where the browser says the request came from.
func (p *testPasskey) assert(t *testing.T, options json.RawMessage, origin, rp string, flags byte) json.RawMessage {
	t.Helper()
	p.mu.Lock()
	p.count++
	counter := p.count
	p.mu.Unlock()
	value, _ := passkeyChallengeValue(t, options)
	client, _ := json.Marshal(map[string]any{"type": "webauthn.get", "challenge": value, "origin": origin, "crossOrigin": false})
	hash := sha256.Sum256([]byte(rp))
	auth := append(hash[:], flags)
	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, counter)
	auth = append(auth, count...)
	clientHash := sha256.Sum256(client)
	digest := sha256.Sum256(append(append([]byte{}, auth...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, p.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"id": b64url(p.id), "rawId": b64url(p.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64url(client), "authenticatorData": b64url(auth), "signature": b64url(signature), "userHandle": p.user}, "clientExtensionResults": map[string]any{}})
	return data
}

// deletionFixture is a Vult whose owner, Kit Blanche, signs in with a
// passkey and keeps Books for Eightball, Inc. Another Vult's owner shares
// the server.
type deletionFixture struct {
	*documentServerFixture
	ctx      context.Context
	passkey  *testPasskey
	owner    string // the owner's browser session
	vult     string // the owner's tenant
	web      *http.Server
	vault    *documents.Vault
	keys     *documents.KeyDirectory
	docs     string
	eraser   *BookEraser
	notices  *recordedNotices
	origin   string
	rp       string
	passkeys *access.Passkeys
}

type recordedNotices struct {
	mu                sync.Mutex
	started, finished []BookDeletionNotice
}

func (r *recordedNotices) DeletionStarted(_ context.Context, n BookDeletionNotice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, n)
	return nil
}
func (r *recordedNotices) DeletionFinished(_ context.Context, n BookDeletionNotice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished = append(r.finished, n)
	return nil
}

func newDeletionFixture(t *testing.T) *deletionFixture {
	t.Helper()
	// The attacks below send more requests than one person would; the
	// per-session limit has its own test.
	burst := deletionRequestBurst
	deletionRequestBurst = 1000
	t.Cleanup(func() { deletionRequestBurst = burst })
	base := documentServer(t)
	f := &deletionFixture{documentServerFixture: base, ctx: base.db.Context, origin: configuration().Origin, rp: "ledger.example.com", notices: &recordedNotices{}}
	setup, err := f.operator.InitializePasskeys(f.ctx, "", "kit-blanche", f.origin)
	if err != nil {
		t.Fatal(err)
	}
	f.vult = setup.TenantID
	f.passkeys, err = access.NewPasskeys(f.ctx, f.sessions, "kit-blanche")
	if err != nil {
		t.Fatal(err)
	}
	browser := access.Random()
	challenge, err := f.passkeys.BeginRegistration(f.ctx, setup.Token, browser)
	if err != nil {
		t.Fatal(err)
	}
	f.passkey = newTestPasskey(t)
	f.owner, err = f.passkeys.FinishRegistration(f.ctx, challenge.Token, browser, f.passkey.register(t, challenge.Options, f.origin, f.rp))
	if err != nil {
		t.Fatal("passkey registration", err)
	}
	f.docs = t.TempDir()
	keyDir := t.TempDir()
	for _, d := range []string{f.docs, keyDir} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	disk, err := documents.OpenDisk(f.docs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { disk.Close() })
	f.keys, err = documents.OpenKeyDirectory(keyDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.keys.Close() })
	if err := f.keys.Provision(f.ctx, f.vult); err != nil {
		t.Fatal(err)
	}
	f.vault = &documents.Vault{Storage: disk, Keys: f.keys}
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	policy := BookDeletionPolicy{Notifier: f.notices, BackupRetention: 35 * 24 * time.Hour}
	f.web, err = New(configuration(), Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: login}, Documents: f.vault,
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}}, BookProver: f.passkeys, BookDeletion: policy})
	if err != nil {
		t.Fatal(err)
	}
	f.server = f.web
	f.eraser = &BookEraser{DB: f.db.Runtime, Documents: f.vault, Policy: policy}
	return f
}

func (f *deletionFixture) serve(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.web.Handler.ServeHTTP(w, r)
	return w
}

func (f *deletionFixture) post(session, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	return f.serve(ownerPost(path, session, string(b)))
}

func (f *deletionFixture) book(t *testing.T, name string) ledger.Book {
	t.Helper()
	var book ledger.Book
	if err := f.sessions.WithOwner(f.ctx, f.owner, f.vult, true, func(o ledger.TenantAdmin) error {
		var err error
		book, err = o.CreateBook(f.ctx, name, "USD", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return book
}

// fill gives a Book accounts, entries, a stored original sealed with the
// Book's key, an agent with write access and a live bank login.
func (f *deletionFixture) fill(t *testing.T, book ledger.Book, entries int) (ledger.Scope, string) {
	t.Helper()
	c, err := f.db.Owner.CreateConnection(f.ctx, book.ID, "Synthetic bookkeeper", "write")
	if err != nil {
		t.Fatal(err)
	}
	scope := ledger.Scope{TenantID: c.TenantID, BookID: c.BookID, ActorID: c.ActorID, ConnectionID: c.ID, Access: c.Access}
	store := ledger.Store{Runtime: f.db.Runtime, Documents: f.vault}
	call := func(method string, in any) json.RawMessage {
		t.Helper()
		b, _ := json.Marshal(in)
		out, err := store.Call(f.ctx, scope, method, b)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return out
	}
	var cash, sales ledger.Account
	_ = json.Unmarshal(call("create_account", ledger.CreateAccount{RequestKey: "cash", Class: "asset", Code: "1000", Name: "Operating cash"}), &cash)
	_ = json.Unmarshal(call("create_account", ledger.CreateAccount{RequestKey: "sales", Class: "income", Code: "4000", Name: "Sales"}), &sales)
	for i := 0; i < entries; i++ {
		call("post_journal", ledger.PostJournal{RequestKey: "sale-" + string(rune('a'+i)), EconomicDate: "2026-03-0" + string(rune('1'+i%9)), AccountingDate: "2026-03-0" + string(rune('1'+i%9)),
			Description: "Synthetic sale", Lines: []ledger.PostLine{{AccountID: cash.ID, Side: "debit", AmountMinor: "12500"}, {AccountID: sales.ID, Side: "credit", AmountMinor: "12500"}}})
	}
	data := bytes.Repeat([]byte("Eightball, Inc. synthetic receipt\n"), 64)
	hash := sha256.Sum256(data)
	var intake ledger.DocumentIntake
	_ = json.Unmarshal(call("add_document", ledger.AddDocument{RequestKey: "receipt", Name: "receipt.pdf", MediaType: "application/pdf", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}), &intake)
	if _, err := store.Upload(f.ctx, scope, intake.UploadID, bytes.NewReader(data)); err != nil {
		t.Fatal("upload", err)
	}
	var key string
	if err := f.db.Admin.QueryRow(f.ctx, "SELECT key_id FROM public.documents WHERE id=$1", intake.Document.ID).Scan(&key); err != nil || key != documents.BookKeyID {
		t.Fatalf("a new document is sealed with %q, not the Book's own key (%v)", key, err)
	}
	// A live bank login holding a sealed provider token.
	if _, err := f.db.Admin.Exec(f.ctx, `INSERT INTO public.bank_connections(tenant_id,book_id,id,provider,environment,provider_item_id,institution_name,token_sealed,key_version,actor_id)
 VALUES($1,$2,$3,'mercury','sandbox',$4,'Synthetic Bank',$5,1,$6)`, book.TenantID, book.ID, ledger.ID(), "item-"+ledger.ID(), bytes.Repeat([]byte{7}, 64), c.ActorID); err != nil {
		t.Fatal(err)
	}
	return scope, intake.Document.ID
}

// challenge asks the owner's passkey to approve purpose on book.
func (f *deletionFixture) challenge(t *testing.T, session, book, purpose string) (string, json.RawMessage) {
	t.Helper()
	w := f.post(session, bookDeletionPasskey, map[string]string{"book_id": book, "purpose": purpose})
	var out struct {
		Token   string          `json:"token"`
		Options json.RawMessage `json:"options"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Token == "" {
		t.Fatalf("passkey challenge: %d %s", w.Code, w.Body.String())
	}
	return out.Token, out.Options
}

func (f *deletionFixture) deleteBody(t *testing.T, session string, book ledger.Book, purpose string) map[string]any {
	t.Helper()
	token, options := f.challenge(t, session, book.ID, purpose)
	return map[string]any{"book_id": book.ID, "name": book.Name, "token": token, "credential": f.passkey.assert(t, options, f.origin, f.rp, 5)}
}

// rows counts every row in the database that names id in any uuid column,
// and every text, JSON or byte value that contains it.
func (f *deletionFixture) rows(t *testing.T, id string) map[string]int64 {
	t.Helper()
	cols, err := f.db.Admin.Query(f.ctx, `SELECT n.nspname, c.relname, a.attname, format_type(a.atttypid, a.atttypmod)
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
 WHERE c.relkind='r' AND n.nspname IN ('public','access') AND format_type(a.atttypid, a.atttypmod) IN ('uuid','text','jsonb','bytea')`)
	if err != nil {
		t.Fatal(err)
	}
	type col struct{ schema, table, name, kind string }
	var all []col
	for cols.Next() {
		var c col
		if err := cols.Scan(&c.schema, &c.table, &c.name, &c.kind); err != nil {
			t.Fatal(err)
		}
		all = append(all, c)
	}
	cols.Close()
	out := map[string]int64{}
	for _, c := range all {
		where := map[string]string{"uuid": "%s = $1::uuid", "text": "position($1 in %s) > 0", "jsonb": "position($1 in %s::text) > 0", "bytea": "position(convert_to($1,'UTF8') in %s) > 0"}[c.kind]
		var n int64
		q := "SELECT count(*) FROM " + pgx.Identifier{c.schema, c.table}.Sanitize() + " WHERE " + strings.Replace(where, "%s", pgx.Identifier{c.name}.Sanitize(), 1)
		if err := f.db.Admin.QueryRow(f.ctx, q, id).Scan(&n); err != nil {
			t.Fatal(q, err)
		}
		if n > 0 {
			out[c.schema+"."+c.table+"."+c.name] += n
		}
	}
	return out
}

func sameRows(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (f *deletionFixture) deletion(t *testing.T, book string) (state string, found bool) {
	t.Helper()
	err := f.db.Admin.QueryRow(f.ctx, "SELECT state FROM public.book_deletions WHERE book_id=$1", book).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return state, true
}

// passTime moves a deletion's clock forward, as if the waiting period or
// the hold were over.
func (f *deletionFixture) passTime(t *testing.T, book string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(f.ctx, `UPDATE public.book_deletions SET confirm_after=CASE WHEN confirm_after IS NULL THEN NULL ELSE clock_timestamp()-interval '1 second' END,
 erase_after=CASE WHEN erase_after IS NULL THEN NULL ELSE clock_timestamp()-interval '1 second' END WHERE book_id=$1`, book); err != nil {
		t.Fatal(err)
	}
}

func (f *deletionFixture) agentToken(t *testing.T, book ledger.Book) string {
	t.Helper()
	c, err := f.sessions.RegisterClient(f.ctx, "Synthetic outside agent", []string{"http://127.0.0.1:53921/callback"})
	if err != nil {
		t.Fatal(err)
	}
	verifier := access.Random()
	req, err := f.sessions.StartAuthorization(f.ctx, access.Authorization{ClientID: c.ID, RedirectURI: c.Redirects[0], Resource: f.sessions.Resource(), Challenge: access.Challenge(verifier), State: "s", Access: "write"})
	if err != nil {
		t.Fatal(err)
	}
	destination, err := f.sessions.ConsentAuthorization(f.ctx, f.owner, req, f.vult, book.ID, "write", "", false)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(destination)
	tokens, err := f.sessions.Exchange(f.ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {c.ID}, "resource": {f.sessions.Resource()}, "redirect_uri": {c.Redirects[0]}, "code": {u.Query().Get("code")}, "code_verifier": {verifier}})
	if err != nil {
		t.Fatal(err)
	}
	return tokens.Access
}

func TestArchivingABookHidesItAndKeepsEverything(t *testing.T) {
	f := newDeletionFixture(t)
	book := f.book(t, "Eightball, Inc.")
	f.book(t, "Kit Blanche")
	f.fill(t, book, 3)
	before := f.rows(t, book.ID)
	if w := f.post(f.owner, bookArchivePath, map[string]any{"book_id": book.ID, "archived": true}); w.Code != 200 {
		t.Fatalf("archive: %d %s", w.Code, w.Body.String())
	}
	list := f.serve(browserRequest(http.MethodGet, booksPath, f.owner))
	var books struct{ Books []webBook }
	if json.Unmarshal(list.Body.Bytes(), &books) != nil {
		t.Fatal(list.Body.String())
	}
	archived := false
	for _, b := range books.Books {
		archived = archived || b.ID == book.ID && b.Archived
	}
	if !archived {
		t.Fatal("the archived Book is not marked archived", list.Body.String())
	}
	// An agent asking for access isn't offered an archived Book.
	consent, err := f.auth.Query(f.ctx, "SELECT book_id::text FROM access.consent_books($1,$2)", access.Hash(f.owner), f.origin)
	if err != nil {
		t.Fatal(err)
	}
	for consent.Next() {
		var id string
		_ = consent.Scan(&id)
		if id == book.ID {
			t.Fatal("an archived Book is offered to agents")
		}
	}
	consent.Close()
	if w := f.post(f.owner, bookArchivePath, map[string]any{"book_id": book.ID, "archived": false}); w.Code != 200 {
		t.Fatalf("unarchive: %d", w.Code)
	}
	after := f.rows(t, book.ID)
	delete(after, "access.book_deletion_events.book_ref")
	if !sameRows(before, after) {
		t.Fatalf("archiving changed the Book: %v != %v", before, after)
	}
}

// Deleting an archived Book keeps it archived: cancelled, it stays out of
// menus as before.
func TestCancellingTheDeletionOfAnArchivedBookKeepsItArchived(t *testing.T) {
	f := newDeletionFixture(t)
	book := f.book(t, "Eightball, Inc.")
	f.book(t, "Kit Blanche")
	if w := f.post(f.owner, bookArchivePath, map[string]any{"book_id": book.ID, "archived": true}); w.Code != 200 {
		t.Fatalf("archive: %d %s", w.Code, w.Body.String())
	}
	if w := f.post(f.owner, bookDeletionPath, f.deleteBody(t, f.owner, book, "delete")); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := f.post(f.owner, bookDeletionCancel, map[string]string{"book_id": book.ID}); w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	list := f.serve(browserRequest(http.MethodGet, booksPath, f.owner))
	var books struct{ Books []webBook }
	if json.Unmarshal(list.Body.Bytes(), &books) != nil {
		t.Fatal(list.Body.String())
	}
	for _, b := range books.Books {
		if b.ID == book.ID && !b.Archived {
			t.Fatal("the cancelled Book came back unarchived", list.Body.String())
		}
	}
}

func TestDeletingABookResistsAbuse(t *testing.T) {
	f := newDeletionFixture(t)
	eightball := f.book(t, "Eightball, Inc.")
	personal := f.book(t, "Kit Blanche")
	side := f.book(t, "Eightball Side Projects")
	agentScope, _ := f.fill(t, eightball, 4)
	f.fill(t, personal, 2)
	f.fill(t, side, 1)
	intact := func(t *testing.T, book ledger.Book, before map[string]int64) {
		t.Helper()
		if _, deleting := f.deletion(t, book.ID); deleting {
			t.Fatal("the Book is being deleted")
		}
		after := f.rows(t, book.ID)
		delete(after, "access.book_deletion_events.book_ref")
		delete(after, "access.book_deletion_proofs.book_ref")
		delete(before, "access.book_deletion_events.book_ref")
		delete(before, "access.book_deletion_proofs.book_ref")
		if !sameRows(before, after) {
			t.Fatalf("the Book changed: %v != %v", before, after)
		}
	}
	snapshot := f.rows(t, eightball.ID)

	t.Run("a stolen session without the passkey", func(t *testing.T) {
		f.forgetProofs(t)
		for _, body := range []map[string]any{
			{"book_id": eightball.ID, "name": eightball.Name},
			{"book_id": eightball.ID, "name": eightball.Name, "token": access.Random(), "credential": json.RawMessage(`{}`)},
		} {
			if w := f.post(f.owner, bookDeletionPath, body); w.Code != 403 || !strings.Contains(w.Body.String(), "passkey_required") {
				t.Fatalf("deleted without a passkey: %d %s", w.Code, w.Body.String())
			}
		}
		// A fresh sign-in alone doesn't do either: the database wants a
		// spent proof made for this Book.
		err := f.sessions.WithOwner(f.ctx, f.owner, f.vult, true, func(o ledger.TenantAdmin) error {
			_, err := o.RequestBookDeletion(f.ctx, eightball.ID, access.Hash(access.Random()), eightball.Name, ledger.ClientFacts{})
			return err
		})
		var refused *ledger.BookRefusal
		if !errors.As(err, &refused) || refused.Code != "proof" {
			t.Fatal("a session alone started a deletion", err)
		}
		// A passkey answer without user verification is refused.
		token, options := f.challenge(t, f.owner, eightball.ID, "delete")
		if w := f.post(f.owner, bookDeletionPath, map[string]any{"book_id": eightball.ID, "name": eightball.Name, "token": token, "credential": f.passkey.assert(t, options, f.origin, f.rp, 1)}); w.Code != 403 {
			t.Fatalf("an unverified passkey answer deleted the Book: %d", w.Code)
		}
		intact(t, eightball, snapshot)
	})

	t.Run("CSRF and cross-origin requests", func(t *testing.T) {
		f.forgetProofs(t)
		body := f.deleteBody(t, f.owner, eightball, "delete")
		raw, _ := json.Marshal(body)
		noToken := ownerPost(bookDeletionPath, f.owner, string(raw))
		noToken.Header.Del("X-CSRF-Token")
		foreign := ownerPost(bookDeletionPath, f.owner, string(raw))
		foreign.Header.Set("Origin", "https://attacker.example")
		simple := ownerPost(bookDeletionPath, f.owner, string(raw))
		simple.Header.Set("Content-Type", "text/plain")
		for name, r := range map[string]*http.Request{"no CSRF token": noToken, "foreign origin": foreign, "simple form post": simple} {
			if w := f.serve(r); w.Code == 200 {
				t.Fatalf("%s deleted the Book", name)
			}
		}
		// A passkey answer a page on another origin obtained is refused.
		token, options := f.challenge(t, f.owner, eightball.ID, "delete")
		if w := f.post(f.owner, bookDeletionPath, map[string]any{"book_id": eightball.ID, "name": eightball.Name, "token": token, "credential": f.passkey.assert(t, options, "https://attacker.example", f.rp, 5)}); w.Code != 403 {
			t.Fatalf("a cross-origin passkey answer deleted the Book: %d", w.Code)
		}
		intact(t, eightball, snapshot)
	})

	t.Run("an assertion for a different Book", func(t *testing.T) {
		f.forgetProofs(t)
		body := f.deleteBody(t, f.owner, personal, "delete")
		body["book_id"], body["name"] = eightball.ID, eightball.Name
		if w := f.post(f.owner, bookDeletionPath, body); w.Code != 403 {
			t.Fatalf("an assertion for another Book deleted this one: %d %s", w.Code, w.Body.String())
		}
		// Nor one made to confirm a held deletion.
		body = f.deleteBody(t, f.owner, eightball, "confirm")
		if w := f.post(f.owner, bookDeletionPath, body); w.Code != 403 {
			t.Fatalf("a confirmation assertion started a deletion: %d", w.Code)
		}
		// Nor one made in another session of the owner.
		second := f.signInAgain(t)
		body = f.deleteBody(t, second, eightball, "delete")
		if w := f.post(f.owner, bookDeletionPath, body); w.Code != 403 {
			t.Fatalf("another session's assertion deleted the Book: %d", w.Code)
		}
		intact(t, eightball, snapshot)
	})

	t.Run("the wrong name", func(t *testing.T) {
		f.forgetProofs(t)
		body := f.deleteBody(t, f.owner, eightball, "delete")
		body["name"] = "eightball, inc."
		if w := f.post(f.owner, bookDeletionPath, body); w.Code != 422 {
			t.Fatalf("a near name deleted the Book: %d %s", w.Code, w.Body.String())
		}
		intact(t, eightball, snapshot)
	})

	t.Run("another Vult's owner", func(t *testing.T) {
		f.forgetProofs(t)
		if w := f.post(f.session, bookDeletionPasskey, map[string]string{"book_id": eightball.ID, "purpose": "delete"}); w.Code == 200 {
			t.Fatal("another Vult's owner got a passkey challenge for this Book")
		}
		body := f.deleteBody(t, f.owner, eightball, "delete")
		for _, path := range []string{bookDeletionPath, bookDeletionCancel, bookArchivePath} {
			if w := f.post(f.session, path, body); w.Code == 200 {
				t.Fatalf("another Vult's owner used %s on this Book", path)
			}
		}
		if w := f.serve(browserRequest(http.MethodGet, bookDeletionPath+"?book_id="+eightball.ID, f.session)); w.Code == 200 {
			t.Fatal("another Vult's owner read this Book's deletion summary")
		}
		intact(t, eightball, snapshot)
	})

	t.Run("the built-in agent and an outside MCP agent", func(t *testing.T) {
		f.forgetProofs(t)
		token := f.agentToken(t, eightball)
		client := f.client(t, token)
		// The owner let the agent in: that grant is the only change.
		snapshot = f.rows(t, eightball.ID)
		tools, err := client.ListTools(f.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range tools.Tools {
			for _, word := range []string{"delete_book", "erase", "archive_book", "book_deletion"} {
				if strings.Contains(tool.Name, word) {
					t.Fatalf("MCP offers %s", tool.Name)
				}
			}
		}
		for _, method := range []string{"delete_book", "archive_book", "request_book_deletion", "erase_book"} {
			if ledger.Supported(method) {
				t.Fatalf("agents may call %s", method)
			}
			r, err := client.CallTool(f.ctx, &mcp.CallToolParams{Name: method, Arguments: map[string]string{"book_id": eightball.ID}})
			if err == nil && !r.IsError {
				t.Fatalf("MCP ran %s", method)
			}
		}
		// The agent's credential never reaches the owner's API.
		b, _ := json.Marshal(f.deleteBody(t, f.owner, eightball, "delete"))
		r := ownerPost(bookDeletionPath, "", string(b))
		r.Header.Set("Authorization", "Bearer "+token)
		if w := f.serve(r); w.Code != 403 {
			t.Fatalf("an agent's bearer token reached the deletion API: %d", w.Code)
		}
		// An agent's database connection can't take the owner's steps.
		_, err = f.db.Runtime.Exec(f.ctx, "SELECT * FROM public.request_book_deletion($1,$2,$3,$4,'','')", agentScope.TenantID, eightball.ID, access.Hash(access.Random()), eightball.Name)
		if err == nil {
			t.Fatal("the agent role may start a deletion")
		}
		tx, err := f.owners.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = tx.Exec(f.ctx, "SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin',$2,true),set_config('vult.tenant_id',$3,true),set_config('vult.connection_id',$4,true)", access.Hash(f.owner), f.origin, f.vult, agentScope.ConnectionID)
		_, err = tx.Exec(f.ctx, "SELECT public.archive_book($1,$2,true,'','')", f.vult, eightball.ID)
		_ = tx.Rollback(f.ctx)
		if err == nil {
			t.Fatal("an agent connection archived a Book")
		}
		intact(t, eightball, snapshot)
	})

	t.Run("concurrent double deletes and a second Book at once", func(t *testing.T) {
		f.forgetProofs(t)
		bodies := []map[string]any{f.deleteBody(t, f.owner, personal, "delete"), f.deleteBody(t, f.owner, personal, "delete"), f.deleteBody(t, f.owner, side, "delete")}
		codes := make([]int, len(bodies))
		var wg sync.WaitGroup
		for i := range bodies {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i] = f.post(f.owner, bookDeletionPath, bodies[i]).Code
			}(i)
		}
		wg.Wait()
		ok := 0
		for _, c := range codes {
			// A refusal is 409 (one deletion at a time) or 403: the same
			// passkey answering out of order looks like a cloned key.
			if c == 200 {
				ok++
			} else if c != 409 && c != 403 {
				t.Fatalf("a concurrent delete answered %d", c)
			}
		}
		var pending int
		if err := f.db.Admin.QueryRow(f.ctx, "SELECT count(*) FROM public.book_deletions WHERE tenant_id=$1", f.vult).Scan(&pending); err != nil || ok != 1 || pending != 1 {
			t.Fatalf("%d deletions accepted, %d pending: %v", ok, pending, codes)
		}
	})

	var deleting ledger.Book
	if _, ok := f.deletion(t, personal.ID); ok {
		deleting = personal
	} else {
		deleting = side
	}

	t.Run("a second device during the waiting period", func(t *testing.T) {
		f.forgetProofs(t)
		second := f.signInAgain(t)
		if w := f.post(second, bookDeletionPath, f.deleteBody(t, second, eightball, "delete")); w.Code != 409 {
			t.Fatalf("a second deletion started while one waits: %d %s", w.Code, w.Body.String())
		}
		if w := f.post(second, bookDeletionPath, f.deleteBody(t, second, deleting, "delete")); w.Code != 409 {
			t.Fatalf("the same Book was deleted twice: %d", w.Code)
		}
		// The locked Book refuses every change, from the owner or an agent.
		if w := f.post(second, bookArchivePath, map[string]any{"book_id": deleting.ID, "archived": true}); w.Code != 409 {
			t.Fatalf("a locked Book was archived: %d", w.Code)
		}
		intact(t, eightball, snapshot)
	})

	t.Run("cancelling, then deleting again", func(t *testing.T) {
		f.forgetProofs(t)
		if w := f.post(f.owner, bookDeletionCancel, map[string]string{"book_id": deleting.ID}); w.Code != 200 {
			t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
		}
		if _, ok := f.deletion(t, deleting.ID); ok {
			t.Fatal("the deletion is still waiting")
		}
		// The proof that started it is spent; resending it does nothing.
		used := f.deleteBody(t, f.owner, deleting, "delete")
		if w := f.post(f.owner, bookDeletionPath, used); w.Code != 200 {
			t.Fatalf("delete again: %d %s", w.Code, w.Body.String())
		}
		if w := f.post(f.owner, bookDeletionCancel, map[string]string{"book_id": deleting.ID}); w.Code != 200 {
			t.Fatal("second cancel", w.Code)
		}
		if w := f.post(f.owner, bookDeletionPath, used); w.Code != 403 {
			t.Fatalf("a replayed passkey answer deleted the Book again: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("mass deletion across Books", func(t *testing.T) {
		f.forgetProofs(t)
		codes := []int{}
		for _, book := range []ledger.Book{eightball, personal, side} {
			codes = append(codes, f.post(f.owner, bookDeletionPath, f.deleteBody(t, f.owner, book, "delete")).Code)
		}
		// The first is a second Book within a day, so it is held for an
		// extra confirmation; the others are refused: one deletion at a
		// time, and three a day.
		if codes[0] != 200 || codes[1] != 409 && codes[1] != 429 || codes[2] != 409 && codes[2] != 429 {
			t.Fatalf("mass deletion answered %v", codes)
		}
		if state, _ := f.deletion(t, eightball.ID); state != "held" {
			t.Fatal("a second deletion within a day is", state)
		}
		if n, _ := f.eraser.EraseDue(f.ctx); n != 0 {
			t.Fatal("a held deletion was erased")
		}
		if w := f.post(f.owner, bookDeletionCancel, map[string]string{"book_id": eightball.ID}); w.Code != 200 {
			t.Fatal("cancel", w.Code)
		}
		// Three deletions were started today: the fourth is refused.
		if w := f.post(f.owner, bookDeletionPath, f.deleteBody(t, f.owner, side, "delete")); w.Code != 429 {
			t.Fatalf("a fourth deletion today answered %d %s", w.Code, w.Body.String())
		}
		// Passkey requests are limited too.
		limited := false
		for i := 0; i < 12 && !limited; i++ {
			limited = f.post(f.owner, bookDeletionPasskey, map[string]string{"book_id": eightball.ID, "purpose": "delete"}).Code == 429
		}
		if !limited {
			t.Fatal("passkey requests are not limited")
		}
		var pending int
		_ = f.db.Admin.QueryRow(f.ctx, "SELECT count(*) FROM public.book_deletions WHERE tenant_id=$1", f.vult).Scan(&pending)
		if pending != 0 {
			t.Fatalf("%d Books are being deleted", pending)
		}
		intact(t, eightball, snapshot)
	})

	var events int
	if err := f.db.Admin.QueryRow(f.ctx, "SELECT count(*) FROM access.book_deletion_events WHERE tenant_id=$1", f.vult).Scan(&events); err != nil || events == 0 {
		t.Fatal("no deletion record", err)
	}
	if _, err := f.db.Admin.Exec(f.ctx, "DELETE FROM access.book_deletion_events WHERE tenant_id=$1", f.vult); err == nil {
		t.Fatal("the deletion record was removed")
	}
	if _, err := f.db.Admin.Exec(f.ctx, "TRUNCATE access.book_deletion_events"); err == nil {
		t.Fatal("the deletion record was truncated")
	}
}

// forgetProofs clears earlier passkey requests, as an hour passing would,
// so each attack starts within the hourly limit.
func (f *deletionFixture) forgetProofs(t *testing.T) {
	t.Helper()
	if _, err := f.db.Admin.Exec(f.ctx, "UPDATE access.book_deletion_proofs SET created_at=created_at-interval '2 hours'"); err != nil {
		t.Fatal(err)
	}
}

// signInAgain is the owner on a second device: a new passkey sign-in.
func (f *deletionFixture) signInAgain(t *testing.T) string {
	t.Helper()
	browser := access.Random()
	challenge, err := f.passkeys.BeginLogin(f.ctx, browser)
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.passkeys.FinishLogin(f.ctx, challenge.Token, browser, f.passkey.assert(t, challenge.Options, f.origin, f.rp, 5))
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestTheLastBookIsHeldUntilTheOwnerConfirmsAgain(t *testing.T) {
	f := newDeletionFixture(t)
	only := f.book(t, "Eightball, Inc.")
	w := f.post(f.owner, bookDeletionPath, f.deleteBody(t, f.owner, only, "delete"))
	var out struct{ Deletion ledger.BookDeletionOutcome }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Deletion.State != "held" || out.Deletion.EraseAfter != nil {
		t.Fatalf("the last Book was not held: %d %s", w.Code, w.Body.String())
	}
	if n, _ := f.eraser.EraseDue(f.ctx); n != 0 {
		t.Fatal("a held Book was erased")
	}
	if w := f.post(f.owner, bookDeletionConfirm, f.deleteBody(t, f.owner, only, "confirm")); w.Code != http.StatusTooEarly {
		t.Fatalf("confirmed before the hold ended: %d %s", w.Code, w.Body.String())
	}
	f.passTime(t, only.ID)
	if w := f.post(f.owner, bookDeletionConfirm, f.deleteBody(t, f.owner, only, "delete")); w.Code != 403 {
		t.Fatalf("a delete assertion confirmed: %d", w.Code)
	}
	if w := f.post(f.owner, bookDeletionConfirm, f.deleteBody(t, f.owner, only, "confirm")); w.Code != 200 {
		t.Fatalf("confirm: %d %s", w.Code, w.Body.String())
	}
	if state, _ := f.deletion(t, only.ID); state != "waiting" {
		t.Fatal("confirmed deletion is", state)
	}
	if len(f.notices.started) != 2 {
		t.Fatalf("%d start notices", len(f.notices.started))
	}
}

func TestErasureRemovesEverythingAndResumesAfterACrash(t *testing.T) {
	f := newDeletionFixture(t)
	eightball := f.book(t, "Eightball, Inc.")
	personal := f.book(t, "Kit Blanche")
	scope, document := f.fill(t, eightball, 6)
	f.fill(t, personal, 3)
	kept := f.rows(t, personal.ID)
	var storage string
	if err := f.db.Admin.QueryRow(f.ctx, "SELECT storage_id FROM public.documents WHERE id=$1", document).Scan(&storage); err != nil {
		t.Fatal(err)
	}
	// The summary says what goes.
	summary := f.serve(browserRequest(http.MethodGet, bookDeletionPath+"?book_id="+eightball.ID, f.owner))
	var read struct {
		Summary ledger.BookDeletionSummary
		Backups Backups
	}
	if summary.Code != 200 || json.Unmarshal(summary.Body.Bytes(), &read) != nil || read.Summary.Entries != 6 || read.Summary.Documents != 1 || read.Summary.BankConnections != 1 || read.Backups.RetentionDays != 35 {
		t.Fatalf("summary: %d %s", summary.Code, summary.Body.String())
	}
	if w := f.post(f.owner, bookDeletionPath, f.deleteBody(t, f.owner, eightball, "delete")); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	// Locked: the bank login is disconnected, agents and the owner can't
	// change anything, and the agent can't even read it.
	var status string
	if err := f.db.Admin.QueryRow(f.ctx, "SELECT status FROM public.bank_connections WHERE book_id=$1", eightball.ID).Scan(&status); err != nil || status != "disconnected" {
		t.Fatal("the bank login is", status, err)
	}
	store := ledger.Store{Runtime: f.db.Runtime, Documents: f.vault}
	if _, err := store.Call(f.ctx, scope, "create_account", json.RawMessage(`{"request_key":"late","class":"asset","code":"1100","name":"Late"}`)); err == nil {
		t.Fatal("an agent wrote to a Book being deleted")
	}
	if _, err := store.Call(f.ctx, scope, "list_accounts", json.RawMessage(`{}`)); err == nil {
		t.Fatal("an agent read a Book being deleted")
	}
	// The owner renaming it is told why not.
	if w := f.post(f.owner, "/api/v1/settings", ledger.ReviseBookSettings{BookID: eightball.ID, RequestKey: "rename-late", Revision: 0, Name: "Renamed"}); w.Code == 200 || !strings.Contains(w.Body.String(), "being deleted") {
		t.Fatalf("renaming a Book being deleted: %d %s", w.Code, w.Body.String())
	}
	before := f.rows(t, eightball.ID)
	// Not before the waiting period ends, whoever asks.
	if n, err := f.eraser.EraseDue(f.ctx); n != 0 || err != nil {
		t.Fatal("erased early", n, err)
	}
	if _, err := ledger.BeginBookErasure(f.ctx, f.db.Runtime, f.vult, eightball.ID); err == nil {
		t.Fatal("erasure began early")
	}
	f.passTime(t, eightball.ID)
	// Nor while the bank's provider access hasn't ended.
	if n, _ := f.eraser.EraseDue(f.ctx); n != 0 {
		t.Fatal("erased while the bank still held access")
	}
	if _, err := f.db.Admin.Exec(f.ctx, "UPDATE public.bank_connections SET access_ended_at=clock_timestamp(),token_sealed=NULL,key_version=NULL WHERE book_id=$1", eightball.ID); err != nil {
		t.Fatal(err)
	}

	// Crash 1: the process stops after the files and key are gone.
	f.eraser.removed = func(string, string) error { return errors.New("power lost") }
	if _, err := f.eraser.EraseDue(f.ctx); err == nil {
		t.Fatal("the stopped erasure reported success")
	}
	if state, _ := f.deletion(t, eightball.ID); state != "erasing" {
		t.Fatal("after a crash the deletion is", state)
	}
	if _, err := os.Stat(filepath.Join(f.docs, storage)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the stored original remains", err)
	}
	if err := f.keys.ProvisionBook(f.ctx, f.vult, eightball.ID); !errors.Is(err, documents.ErrBookErased) {
		t.Fatal("an erased Book's key can be made again", err)
	}
	if _, err := f.keys.BookIdentity(f.ctx, f.vult, eightball.ID); err == nil {
		t.Fatal("the Book's key survived")
	}

	// Crash 2: the database transaction fails midway; nothing is removed.
	f.eraser.removed = nil
	locker, err := f.db.Admin.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locker.Exec(f.ctx, "LOCK TABLE public.search_words IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	conn, err := f.db.Runtime.Acquire(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(f.ctx, "SET lock_timeout='300ms'"); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.FinishBookErasure(f.ctx, conn.Conn(), f.vult, eightball.ID); err == nil {
		t.Fatal("erasure finished through a lock")
	}
	_, _ = conn.Exec(f.ctx, "RESET lock_timeout")
	conn.Release()
	_ = locker.Rollback(f.ctx)
	if mid := f.rows(t, eightball.ID); mid["public.entries.book_id"] != before["public.entries.book_id"] || mid["public.books.id"] != 1 {
		t.Fatalf("a failed erasure left half a Book: %v", mid)
	}

	// Resume: it finishes.
	if n, err := f.eraser.EraseDue(f.ctx); n != 1 || err != nil {
		t.Fatal("erasure did not resume", n, err)
	}
	left := f.rows(t, eightball.ID)
	for k := range left {
		if k != "access.book_deletion_events.book_ref" {
			t.Fatalf("erasure left %s: %v", k, left)
		}
	}
	if files, _ := os.ReadDir(f.docs); len(files) != 1 {
		t.Fatalf("%d stored files remain; only the other Book's original should", len(files))
	}
	after := f.rows(t, personal.ID)
	if !sameRows(kept, after) {
		t.Fatalf("erasure changed another Book: %v != %v", kept, after)
	}
	// The deletion record remains and names no records or amounts.
	rows, err := f.db.Admin.Query(f.ctx, "SELECT event, book_name, detail FROM access.book_deletion_events WHERE book_ref=$1 ORDER BY sequence", eightball.ID)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for rows.Next() {
		var event, name, detail string
		_ = rows.Scan(&event, &name, &detail)
		if name != "Eightball, Inc." || strings.Contains(detail, "12500") || strings.Contains(detail, "125.00") {
			t.Fatal("the record holds", name, detail)
		}
		seen = append(seen, event)
	}
	rows.Close()
	if strings.Join(seen, ",") != "requested,banks_disconnected,erasure_started,erased" {
		t.Fatal("the record says", seen)
	}
	if len(f.notices.finished) != 1 || f.notices.finished[0].BookName != "Eightball, Inc." || f.notices.finished[0].Backups.ExpireBy == nil {
		t.Fatal("no finished notice with the backups' expiry", f.notices.finished)
	}
}

func TestOperatorDeletesABookWithAdministratorRights(t *testing.T) {
	f := newDeletionFixture(t)
	book := f.book(t, "Eightball, Inc.")
	f.book(t, "Kit Blanche")
	f.fill(t, book, 2)
	if _, err := f.db.Runtime.Exec(f.ctx, "SELECT * FROM public.operator_book_deletion($1,$2,$3,true)", f.vult, book.ID, book.Name); err == nil {
		t.Fatal("the service role ran the operator's deletion")
	}
	if _, err := f.db.Owner.OperatorDeleteBook(f.ctx, book.ID, "Eightball", true); err == nil {
		t.Fatal("the operator deleted with the wrong name")
	}
	out, err := f.db.Owner.OperatorDeleteBook(f.ctx, book.ID, book.Name, true)
	if err != nil || out.State != "waiting" {
		t.Fatal(out, err)
	}
	if _, err := f.db.Admin.Exec(f.ctx, "UPDATE public.bank_connections SET access_ended_at=clock_timestamp(),token_sealed=NULL,key_version=NULL WHERE book_id=$1", book.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.eraser.EraseDue(f.ctx); n != 1 || err != nil {
		t.Fatal("operator erasure", n, err)
	}
	if left := f.rows(t, book.ID); len(left) != 1 {
		t.Fatal("left", left)
	}
}

// Every table holding a Book's rows refuses changes while it is deleted.
func TestEveryBookTableHasTheDeletionLock(t *testing.T) {
	f := newDeletionFixture(t)
	rows, err := f.db.Admin.Query(f.ctx, `SELECT n.nspname||'.'||c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE c.relkind='r' AND n.nspname IN ('public','access')
 AND EXISTS(SELECT 1 FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped AND a.atttypid='uuid'::regtype AND (a.attname IN ('book_id','book_a','book_b') OR a.attname LIKE '%\_book\_id'))
 AND NOT EXISTS(SELECT 1 FROM pg_trigger g WHERE g.tgrelid=c.oid AND g.tgname='book_lock_guard')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		if name != "public.book_deletions" && name != "access.book_view_preferences" {
			t.Errorf("%s has no deletion lock", name)
		}
	}
}

func TestOneSessionCantFloodDeletionSteps(t *testing.T) {
	f := newDeletionFixture(t)
	deletionRequestBurst = 20
	book := f.book(t, "Eightball, Inc.")
	limited := false
	for i := 0; i < 30 && !limited; i++ {
		limited = f.serve(browserRequest(http.MethodGet, bookDeletionPath+"?book_id="+book.ID, f.owner)).Code == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("one session sent 30 deletion requests at once")
	}
}
