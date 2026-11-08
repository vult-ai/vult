package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

type bookViewCatalog struct {
	SchemaVersion int                                 `json:"schema_version"`
	Scope         string                              `json:"view_scope"`
	Views         map[string]access.BookViewSelection `json:"views"`
}

func getBookViewCatalog(t *testing.T, s *http.Server, session string) bookViewCatalog {
	t.Helper()
	out := decodeOwnerResult[bookViewCatalog](t, ownerAPICall(s, session, http.MethodGet, "/api/v1/books", "", false))
	if out.SchemaVersion != 1 || out.Scope == "" || out.Views == nil {
		t.Fatal("catalog omitted the owner's view scope or views")
	}
	return out
}

func viewPreferenceBody(book, scope, selection string) string {
	return fmt.Sprintf(`{"book_id":%q,"scope":%q,"selection":%s}`, book, scope, selection)
}

func TestBookViewPreferencePersistsAcrossSessionsAndBooks(t *testing.T) {
	f, s := browserServer(t)
	ctx := f.db.Context
	device, secondBook := ownerAPIBook(t, f, f.identity)
	other, foreignBook := otherAPIOwner(t, f)
	first := getBookViewCatalog(t, s, f.session)
	foreign := getBookViewCatalog(t, s, other)
	if getBookViewCatalog(t, s, device).Scope != first.Scope || foreign.Scope == first.Scope ||
		len(first.Views) != 0 || len(foreign.Views) != 0 {
		t.Fatal("empty catalog views are not scoped to the signed-in owner")
	}
	before, err := f.db.Owner.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A session can still save a display choice after its recent proof expires.
	if _, err := f.db.Admin.Exec(ctx, `UPDATE access.sessions SET authenticated_at=created_at-interval '10 minutes' WHERE token_hash=$1`, access.Hash(device)); err != nil {
		t.Fatal(err)
	}
	week := `{"period":"last-week","range":{"from":"2026-01-05","to":"2026-01-11"}}`
	custom := `{"period":"custom","range":{"from":"2026-02-28","to":"2026-02-28"}}`
	all := `{"period":"all","range":{"from":"0001-01-01","to":"9999-12-31"}}`
	save := func(session, book, scope, selection string, status int) {
		t.Helper()
		w := ownerAPICall(s, session, http.MethodPost, bookViewPreferencePath, viewPreferenceBody(book, scope, selection), true)
		ownerAPIStatus(t, w, status)
		if status == 200 && (!json.Valid(w.Body.Bytes()) || !strings.Contains(w.Body.String(), `"status":"saved"`)) {
			t.Fatal("save did not acknowledge persistence", w.Body.String())
		}
	}
	save(f.session, f.book.ID, foreign.Scope, week, 403)
	save(other, f.book.ID, foreign.Scope, week, 404)
	save(f.session, foreignBook.ID, first.Scope, week, 404)
	save(f.session, ledger.ID(), first.Scope, week, 404)
	save(f.session, f.book.ID, first.Scope, week, 200)
	save(device, secondBook.ID, first.Scope, all, 200)
	save(device, f.book.ID, first.Scope, custom, 200)
	save(device, f.book.ID, first.Scope, custom, 200)
	save(other, foreignBook.ID, foreign.Scope, week, 200)

	want := make(map[string]access.BookViewSelection)
	for book, selection := range map[string]string{f.book.ID: custom, secondBook.ID: all} {
		var view access.BookViewSelection
		if err := json.Unmarshal([]byte(selection), &view); err != nil {
			t.Fatal(err)
		}
		want[book] = view
	}
	for _, session := range []string{f.session, device} {
		got := getBookViewCatalog(t, s, session)
		if got.Scope != first.Scope || !maps.Equal(got.Views, want) {
			t.Fatalf("catalog did not carry independent choices across sessions: %+v", got)
		}
	}
	if got := getBookViewCatalog(t, s, other); len(got.Views) != 1 || got.Views[foreignBook.ID].Period != "last-week" {
		t.Fatal("another owner received the wrong selections", got)
	}
	after, err := f.db.Owner.Snapshot(ctx)
	if err != nil || !maps.EqualFunc(before.Tables, after.Tables, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
		t.Fatal("saving a display choice changed Book data", err)
	}

	// Simulate replacing an owner while the same Book and saved rows remain.
	if tag, err := f.db.Admin.Exec(ctx, `UPDATE access.owners SET identity_id=(SELECT id FROM access.session_identity($1,$2)) WHERE tenant_id=$3`,
		access.Hash(other), configuration().Origin, f.tenant); err != nil || tag.RowsAffected() != 1 {
		t.Fatal("could not replace synthetic ownership", err)
	}
	if got := getBookViewCatalog(t, s, device); len(got.Views) != 1 || got.Views[secondBook.ID] != want[secondBook.ID] {
		t.Fatal("catalog retained a Book view after access was lost", got)
	}
	if got := getBookViewCatalog(t, s, other); len(got.Views) != 1 || got.Views[foreignBook.ID].Period != "last-week" {
		t.Fatal("a replacement owner inherited the previous owner's display choice", got)
	}
	save(device, f.book.ID, first.Scope, week, 404)
	save(other, f.book.ID, first.Scope, week, 403)
	save(other, f.book.ID, foreign.Scope, week, 200)
	tx, err := f.owners.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('vult.owner_session',$1,true),set_config('vult.owner_origin',$2,true),set_config('vult.tenant_id',$3,true)`,
		access.Hash(other), configuration().Origin, f.tenant); err != nil {
		t.Fatal(err)
	}
	var count int
	var period string
	if err := tx.QueryRow(ctx, `SELECT count(*),coalesce(max(period),'') FROM access.book_view_preferences WHERE book_id=$1`, f.book.ID).Scan(&count, &period); err != nil || count != 1 || period != "last-week" {
		t.Fatal("owner role did not isolate the two selections for the same Book", count, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.sessions.EndSession(ctx, device); err != nil {
		t.Fatal(err)
	}
	save(device, secondBook.ID, first.Scope, week, 401)
	ownerAPIStatus(t, ownerAPICall(s, device, http.MethodGet, "/api/v1/books", "", false), 401)
}

func TestBookViewPreferenceRejectsInvalidWrites(t *testing.T) {
	f, s := browserServer(t)
	scope := getBookViewCatalog(t, s, f.session).Scope
	valid := viewPreferenceBody(f.book.ID, scope, `{"period":"month","range":{"from":"2026-02-01","to":"2026-02-28"}}`)
	ownerAPIStatus(t, ownerAPICall(s, "", http.MethodPost, bookViewPreferencePath, valid, false), 401)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, bookViewPreferencePath, valid, false), 403)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, bookViewPreferencePath+"?book_id="+f.book.ID, valid, true), 400)
	r := browserRequest(http.MethodPost, bookViewPreferencePath, f.session)
	r.Header.Set("Authorization", "Bearer "+f.token(t, "write"))
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	ownerAPIStatus(t, w, 403)

	for _, selection := range []string{
		`null`,
		`{"period":"month","range":null}`,
		`{"period":"month","range":{"from":"2026-02-30","to":"2026-03-01"}}`,
		`{"period":"month","range":{"from":"2026-02-02","to":"2026-02-01"}}`,
		`{"period":"month","range":{"from":"0000-01-01","to":"2026-02-01"}}`,
		`{"period":"month","range":{"from":"2026-02-01T00:00:00Z","to":"2026-02-01"}}`,
		`{"period":"month","range":{"from":"2026-02-01","to":"2026-02-01","account":"extra"}}`,
		`{"period":"month","range":{"from":"2026-02-01","from":"2026-02-01","to":"2026-02-01"}}`,
		`{"period":"rolling","range":{"from":"2026-02-01","to":"2026-02-01"}}`,
	} {
		ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, bookViewPreferencePath, viewPreferenceBody(f.book.ID, scope, selection), true), 400)
	}
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, bookViewPreferencePath, valid+strings.Repeat(" ", 1024), true), 413)
	if got := getBookViewCatalog(t, s, f.session); len(got.Views) != 0 {
		t.Fatal("a rejected request saved a view", got)
	}
}
