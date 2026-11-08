package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestOwnerBookAddressRetainsAliasesAndCatalogScope(t *testing.T) {
	f, s := browserServer(t)
	second, otherBook := ownerAPIBook(t, f, f.identity)
	foreign, foreignBook := otherAPIOwner(t, f)
	path := bookAddressPath + "?book_id=" + f.book.ID
	save := func(session string, book ledger.Book, revision int64, slug string, csrf bool) *httptest.ResponseRecorder {
		return ownerAPICall(s, session, http.MethodPost, bookAddressPath+"?book_id="+book.ID,
			fmt.Sprintf(`{"book_id":%q,"revision":%d,"slug":%q}`, book.ID, revision, slug), csrf)
	}
	base := decodeOwnerResult[ledger.BookAddressView](t, ownerAPICall(s, f.session, http.MethodGet, path, "", false))
	if base.BookID != f.book.ID || len(base.Code) != 4 || base.Slug != "" || base.Revision != 0 ||
		!slices.Equal(base.Aliases, []string{strings.ToLower(base.Code)}) || base.StateRevision == "" {
		t.Fatal("new Book has no stable address", base)
	}
	ownerAPIStatus(t, save(f.session, f.book, 0, "first-example", false), 403)
	ownerAPIStatus(t, save(f.session, foreignBook, 0, "first-example", true), 404)
	ownerAPIStatus(t, ownerAPICall(s, foreign, http.MethodGet, path, "", false), 404)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, bookAddressPath, "", false), 400)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, path+"&book_id="+foreignBook.ID, "", false), 400)
	for _, slug := range []string{"auth", "oauth", "mcp", "books", "approvals", "bad--slug", "é", base.Code} {
		ownerAPIStatus(t, save(f.session, f.book, 0, slug, true), 400)
	}
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, path, `{"slug":null}`, true), 400)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, path, strings.Repeat(" ", 2049), true), 413)

	first := decodeOwnerResult[ledger.BookAddressView](t, save(f.session, f.book, 0, "First-Example", true))
	if first.Slug != "first-example" || first.Code != base.Code || first.Revision != 1 || first.StateRevision == base.StateRevision {
		t.Fatal("slug did not become the active address", first)
	}
	ownerAPIStatus(t, save(f.session, f.book, 0, "stale-example", true), 409)
	ownerAPIStatus(t, save(f.session, f.book, 0, "first-example", true), 200)
	ownerAPIStatus(t, save(second, otherBook, 0, "first-example", true), 409)
	ownerAPIStatus(t, save(foreign, foreignBook, 0, "first-example", true), 200)
	final := decodeOwnerResult[ledger.BookAddressView](t, save(f.session, f.book, 1, "second-example", true))
	if final.Slug != "second-example" || final.Code != base.Code || final.Revision != 2 ||
		!slices.Equal(final.Aliases, []string{strings.ToLower(base.Code), "first-example", "second-example"}) {
		t.Fatal("renaming broke an earlier address", final)
	}
	ownerAPIStatus(t, save(second, otherBook, 0, "first-example", true), 409)

	w := ownerAPICall(s, f.session, http.MethodGet, "/api/v1/books", "", false)
	var catalog struct{ Addresses []ledger.BookAddress }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Addresses) != 2 {
		t.Fatal("Book catalog did not supply routable addresses", w.Code)
	}
	for _, address := range catalog.Addresses {
		if address.BookID == foreignBook.ID {
			t.Fatal("foreign address was visible in the catalog")
		}
	}

	// A restored tenant can be bound to an owner who already holds a Book
	// with one of its retained aliases.
	if _, err := f.db.Admin.Exec(f.db.Context, "DELETE FROM access.owners WHERE tenant_id=$1", foreignBook.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.operator.BindOwner(f.db.Context, foreignBook.TenantID, f.identity); err != nil {
		t.Fatal(err)
	}
	w = ownerAPICall(s, f.session, http.MethodGet, "/api/v1/books", "", false)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || len(catalog.Addresses) != 3 {
		t.Fatal("rebound Books could not share one catalog", w.Code)
	}
	for _, address := range catalog.Addresses {
		if slices.Contains(address.Aliases, "first-example") ||
			address.BookID == foreignBook.ID && (address.Slug != "" || !slices.Contains(address.Aliases, strings.ToLower(address.Code))) {
			t.Fatal("ambiguous alias hid the fixed Book address", address)
		}
	}
	ambiguous := decodeOwnerResult[ledger.BookAddressView](t, ownerAPICall(s, f.session, http.MethodGet, bookAddressPath+"?book_id="+foreignBook.ID, "", false))
	if ambiguous.Slug != "" || slices.Contains(ambiguous.Aliases, "first-example") {
		t.Fatal("address read reintroduced an ambiguous alias", ambiguous)
	}
	ownerAPIStatus(t, save(f.session, foreignBook, 1, "first-example", true), 409)
	renamed := decodeOwnerResult[ledger.BookAddressView](t, save(f.session, foreignBook, 1, "unique-example", true))
	if renamed.Slug != "unique-example" || slices.Contains(renamed.Aliases, "first-example") {
		t.Fatal("address update reintroduced an ambiguous alias", renamed)
	}
	var stored []string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT aliases FROM public.book_addresses WHERE tenant_id=$1 AND book_id=$2",
		foreignBook.TenantID, foreignBook.ID).Scan(&stored); err != nil || !slices.Contains(stored, "first-example") {
		t.Fatal("projection removed retained address history", err)
	}
}

func TestOwnerBookIdentityRevisionsAndRetainedOutcomes(t *testing.T) {
	f, s := browserServer(t)
	other, foreign := otherAPIOwner(t, f)
	path := bookIdentityPath + "?book_id=" + f.book.ID
	base := decodeOwnerResult[ledger.BookIdentityView](t, ownerAPICall(s, f.session, http.MethodGet, path, "", false))
	if base.Subject.ID != f.book.ID || base.Subject.Revision != 0 || base.Subject.Kind != nil ||
		base.History == nil || len(base.History) != 0 || base.StateRevision == "" {
		t.Fatal("existing Book did not expose an unknown identity", base)
	}
	post := func(session, body string, csrf bool) *httptest.ResponseRecorder {
		return ownerAPICall(s, session, http.MethodPost, bookIdentityPath, body, csrf)
	}
	change := func(book string, revision int, key string) string {
		return fmt.Sprintf(`{"book_id":%q,"revision":%d,"identity":{"legal_name":"Example Entity","registration_status":"registered","formation_or_governing_country_code":"US","registrations":[{"authority":"","number":"EXAMPLE-1"}]},"reason":"Example revision","effective_date":"2026-01-02","idempotency_key":%q,"kind":"legal_entity"}`, book, revision, key)
	}
	initial := change(f.book.ID, 0, "initial")
	ownerAPIStatus(t, post(f.session, initial, false), 403)
	ownerAPIStatus(t, post(other, initial, true), 404)
	ownerAPIStatus(t, post(f.session, change(foreign.ID, 0, "foreign"), true), 404)
	ownerAPIStatus(t, ownerAPICall(s, other, http.MethodGet, path, "", false), 404)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, bookIdentityPath, "", false), 400)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, path+"&offset=10001", "", false), 400)
	for _, bad := range []string{
		strings.Replace(initial, `"kind":"legal_entity"`, `"kind":null`, 1),
		strings.Replace(initial, `"legal_name":"Example Entity"`, `"legal_name":null`, 1),
		strings.Replace(initial, `"legal_name":"Example Entity"`, `"legal_names":"Example Entity"`, 1),
		strings.Replace(initial, `"registrations":[{"authority":"","number":"EXAMPLE-1"}]`, `"registrations":[null]`, 1),
		strings.Replace(initial, "2026-01-02", "2026-02-30", 1),
		strings.Replace(initial, "Example revision", " ", 1),
	} {
		ownerAPIStatus(t, post(f.session, bad, true), 400)
	}
	ownerAPIStatus(t, post(f.session, strings.Repeat(" ", 8193), true), 413)
	w := post(f.session, initial, true)
	receipt := decodeOwnerResult[ledger.BookIdentityReceipt](t, w)
	if receipt.BookID != f.book.ID || receipt.Revision != 1 || receipt.StateRevision == "" ||
		strings.Contains(w.Body.String(), `"history"`) || strings.Contains(w.Body.String(), "Example Entity") {
		t.Fatal("save did not return a compact revision receipt", receipt)
	}
	saved := decodeOwnerResult[ledger.BookIdentityView](t, ownerAPICall(s, f.session, http.MethodGet, path, "", false))
	if saved.StateRevision != receipt.StateRevision {
		t.Fatal("receipt and identity history describe different states")
	}
	if saved.Subject.Revision != 1 || saved.Subject.Identity.LegalName != "Example Entity" ||
		saved.Subject.Identity.Country == nil || saved.Subject.Identity.Country.Code != "US" ||
		len(saved.History) != 1 || saved.History[0].EffectiveDate == nil || *saved.History[0].EffectiveDate != "2026-01-02" ||
		saved.History[0].Actor == "" || saved.History[0].PriorKind != nil || saved.StateRevision == base.StateRevision {
		t.Fatal("legal identity and history differ from the recorded input", saved)
	}
	retry := post(f.session, initial, true)
	if retry.Code != 200 || retry.Body.String() != w.Body.String() {
		t.Fatal("identity retry did not recover the original accepted outcome")
	}
	ownerAPIStatus(t, post(f.session, change(f.book.ID, 1, "initial"), true), 409)
	ownerAPIStatus(t, post(f.session, change(f.book.ID, 0, "stale"), true), 409)
	for i := 1; i <= 10; i++ {
		ownerAPIStatus(t, post(f.session, change(f.book.ID, i, fmt.Sprint("revision-", i)), true), 200)
	}
	if retry := post(f.session, initial, true); retry.Code != 200 || retry.Body.String() != w.Body.String() {
		t.Fatal("later history changed a retained receipt")
	}
	page := decodeOwnerResult[ledger.BookIdentityView](t, ownerAPICall(s, f.session, http.MethodGet, path, "", false))
	tail := decodeOwnerResult[ledger.BookIdentityView](t, ownerAPICall(s, f.session, http.MethodGet, path+"&offset=10", "", false))
	if !page.HasMore || page.NextOffset != 10 || len(page.History) != 10 || page.History[0].Revision != 11 ||
		tail.HasMore || len(tail.History) != 1 || tail.History[0].Revision != 1 || tail.StateRevision != page.StateRevision {
		t.Fatal("identity history paging skipped or duplicated accepted revisions")
	}
}
