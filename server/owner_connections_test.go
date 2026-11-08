package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func TestOwnerConnectionRoutesScopeAndRevokeLiveAgent(t *testing.T) {
	f, s := browserServer(t)
	other, otherBook := otherAPIOwner(t, f)
	ctx := f.db.Context
	token := f.token(t, "write")
	scope, err := f.sessions.ResolveAgent(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	var foreign ledger.Connection
	if err = f.sessions.WithOwner(ctx, other, otherBook.TenantID, true, func(a ledger.TenantAdmin) error {
		var err error
		foreign, err = a.CreateConnection(ctx, otherBook.ID, "Synthetic other connection", "read")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Admin.Exec(ctx, `INSERT INTO public.connections(tenant_id,book_id,id,actor_id,name,access,revoked_at)
		SELECT $1,$2,gen_random_uuid(),gen_random_uuid(),'Synthetic revoked connection','read',clock_timestamp()
		FROM generate_series(1,1001)`, f.tenant, f.book.ID); err != nil {
		t.Fatal(err)
	}
	list := func(session string) []browserConnection {
		return decodeOwnerResult[struct{ Tokens []browserConnection }](t, ownerAPICall(s, session, http.MethodGet, connectionPath, "", false)).Tokens
	}
	own := list(f.session)
	if len(own) != 1 || own[0].ID != scope.ConnectionID || !own[0].Connection || own[0].CreatedAt.IsZero() ||
		own[0].ExpiresAt != nil || len(own[0].BookGrants) != 1 || own[0].BookGrants[0].BookID != f.book.ID ||
		own[0].BookGrants[0].Role != "manage" || !slices.Equal(own[0].BookGrants[0].Rights, []string{"read", "write"}) {
		t.Fatal("connection list did not represent the single durable grant", own)
	}
	if visible := list(other); len(visible) != 1 || visible[0].ID != foreign.ID {
		t.Fatal("connection list crossed owner scope", visible)
	}
	detailPath := connectionPath + "/" + scope.ConnectionID + "/books"
	detail := decodeOwnerResult[connectionBooks](t, ownerAPICall(s, f.session, http.MethodGet, detailPath, "", false))
	if detail.Revision != "1" || detail.StateRevision == "" || !slices.Equal(detail.Books, []connectionBook{{f.book.ID, f.book.Name, "manage"}}) {
		t.Fatal("unexpected scoped grant detail", detail)
	}
	ownerAPIStatus(t, ownerAPICall(s, other, http.MethodGet, detailPath, "", false), 404)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, connectionPath+"?book_id="+otherBook.ID, "", false), 400)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, detailPath, `{}`, true), 404)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, connectionPath, `{}`, true), 404)
	revoke := connectionPath + "/" + scope.ConnectionID + "/revoke"
	ownerAPIStatus(t, ownerAPICall(s, other, http.MethodPost, revoke, `{}`, true), 404)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, revoke, `{}`, false), 403)
	for _, body := range []string{`{"id":"forged"}`, "null", "[]", "{} {}"} {
		ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, revoke, body, true), 400)
	}
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, revoke, strings.Repeat(" ", 1025), true), 413)
	if _, err := f.sessions.ResolveAgent(ctx, token); err != nil {
		t.Fatal("invalid revocation changed the connection", err)
	}
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, revoke, `{}`, true), 200)
	if _, err := f.sessions.ResolveAgent(ctx, token); !errors.Is(err, access.ErrDenied) {
		t.Fatal("live agent token survived browser revocation", err)
	}
	if _, err := f.db.Store.Call(ctx, scope, "get_book", []byte(`{}`)); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("already resolved grant survived browser revocation", err)
	}
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, revoke, "", true), 200)
	if visible := list(f.session); len(visible) != 0 {
		t.Fatal("revoked connection remained in the active list", visible)
	}
	recorded := decodeOwnerResult[connectionBooks](t, ownerAPICall(s, f.session, http.MethodGet, detailPath, "", false))
	if recorded.Revision != "2" || !slices.Equal(recorded.Books, []connectionBook{{f.book.ID, f.book.Name, "none"}}) {
		t.Fatal("revoked connection still exposes an active grant", recorded)
	}
}

func TestOwnerRoutesRejectMissingSessionAndBearer(t *testing.T) {
	f, s := browserServer(t)
	token := f.token(t, "read")
	paths := []string{
		"/api/v1/me/settings", avatarPrefix + ledger.ID(),
		connectionPath, connectionPath + "/" + ledger.ID() + "/books", connectionPath + "/" + ledger.ID() + "/revoke",
		bookIdentityPath, bookAddressPath,
		bookExportPrefix + "book.tar", bookExportPrefix + "journal.json", bookExportPrefix + "transactions.csv",
		bookExportPrefix + "journal-lines.csv", bookExportPrefix + "accounts.csv",
	}
	for _, path := range paths {
		method := http.MethodGet
		if strings.HasSuffix(path, "/revoke") {
			method = http.MethodPost
		}
		ownerAPIStatus(t, ownerAPICall(s, "", method, path, "", false), 401)
		for _, cookie := range []string{"", f.session} {
			r := browserRequest(method, path, cookie)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			s.Handler.ServeHTTP(w, r)
			ownerAPIStatus(t, w, 403)
		}
	}
}
