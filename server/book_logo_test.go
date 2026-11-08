package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func logoDigest(image []byte) string {
	hash := sha256.Sum256(image)
	return hex.EncodeToString(hash[:])
}

func logoRequest(s *http.Server, session, method, path, media, digest string, data []byte, csrf bool, headers ...[2]string) *httptest.ResponseRecorder {
	r := browserRequest(method, path, session)
	if data != nil {
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
		r.Header.Set("Content-Type", media)
		r.Header.Set("X-Vult-Image-SHA256", digest)
	}
	if csrf {
		r.Header.Set("Origin", configuration().Origin)
		r.Header.Set("X-CSRF-Token", access.Hash("csrf:"+session))
	}
	for _, header := range headers {
		r.Header.Add(header[0], header[1])
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	return w
}

func putOwnerLogo(s *http.Server, session, book string, data []byte) *httptest.ResponseRecorder {
	return logoRequest(s, session, http.MethodPut, bookLogoPath+"?book_id="+book, "image/jpeg", logoDigest(data), data, true)
}

type bookLogoReceipt struct {
	SchemaVersion    int              `json:"schema_version"`
	Logo             *ledger.BookLogo `json:"logo"`
	MetadataRevision int64            `json:"metadata_revision"`
}

func TestOwnerBookLogoValidationHistoryAndSettingsRevision(t *testing.T) {
	f, s := browserServer(t)
	foreign, other := otherAPIOwner(t, f)
	path := bookLogoPath + "?book_id=" + f.book.ID
	data, _ := exampleJPEG(t, 256, false)
	digest := logoDigest(data)
	wide, _ := exampleJPEG(t, 257, false)
	for _, bad := range []struct {
		name, session, path, media, digest string
		data                               []byte
		csrf                               bool
		status                             int
	}{
		{"session", "", path, "image/jpeg", digest, data, false, 401},
		{"csrf", f.session, path, "image/jpeg", digest, data, false, 403},
		{"other-owner", foreign, path, "image/jpeg", digest, data, true, 404},
		{"other-book", f.session, bookLogoPath + "?book_id=" + other.ID, "image/jpeg", digest, data, true, 404},
		{"missing-book", f.session, bookLogoPath, "image/jpeg", digest, data, true, 400},
		{"extra-selector", f.session, path + "&book_id=" + other.ID, "image/jpeg", digest, data, true, 400},
		{"digest", f.session, path, "image/jpeg", strings.Repeat("0", 64), data, true, 400},
		{"mime", f.session, path, "image/png", digest, data, true, 400},
		{"svg", f.session, path, "image/svg+xml", logoDigest([]byte("<svg/>")), []byte("<svg/>"), true, 400},
		{"decode", f.session, path, "image/jpeg", logoDigest(data[:len(data)-15]), data[:len(data)-15], true, 400},
		{"dimensions", f.session, path, "image/jpeg", logoDigest(wide), wide, true, 400},
		{"bytes", f.session, path, "image/jpeg", digest, make([]byte, ledger.MaxBookLogoBytes+1), true, 413},
	} {
		t.Run(bad.name, func(t *testing.T) {
			ownerAPIStatus(t, logoRequest(s, bad.session, http.MethodPut, bad.path, bad.media, bad.digest, bad.data, bad.csrf), bad.status)
		})
	}
	saved := decodeOwnerResult[bookLogoReceipt](t, putOwnerLogo(s, f.session, f.book.ID, data))
	if saved.SchemaVersion != 1 || saved.Logo == nil || saved.Logo.SHA256 != digest || saved.Logo.Size != len(data) ||
		saved.Logo.ContentType != "image/jpeg" || saved.MetadataRevision != 1 {
		t.Fatal("incorrect logo receipt", saved)
	}
	retry := decodeOwnerResult[bookLogoReceipt](t, putOwnerLogo(s, f.session, f.book.ID, data))
	if *retry.Logo != *saved.Logo || retry.MetadataRevision != saved.MetadataRevision {
		t.Fatal("identical owner upload appended another revision")
	}
	catalog := decodeOwnerResult[struct{ Books []webBook }](t, ownerAPICall(s, f.session, http.MethodGet, "/api/v1/books", "", false))
	if len(catalog.Books) != 1 || catalog.Books[0].Logo == nil || *catalog.Books[0].Logo != *saved.Logo ||
		catalog.Books[0].MetadataRevision != 1 {
		t.Fatal("Book catalog lost its current logo or revision", catalog)
	}
	locator := bookLogoPath + "/" + saved.Logo.ID + "?book_id=" + f.book.ID + "&revision=" + digest
	read := ownerAPICall(s, f.session, http.MethodGet, locator, "", false)
	if read.Code != 200 || !bytes.Equal(read.Body.Bytes(), data) || read.Header().Get("Content-Type") != "image/jpeg" ||
		read.Header().Get("Cache-Control") != "private, max-age=31536000, immutable" || read.Header().Get("Vary") != "Cookie" ||
		read.Header().Get("X-Vult-Schema-Version") != "1" {
		t.Fatal("versioned logo bytes or cache headers differ", read.Code, read.Header())
	}
	ownerAPIStatus(t, ownerAPICall(s, foreign, http.MethodGet, locator, "", false), 404)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, strings.Replace(locator, digest, strings.Repeat("0", 64), 1), "", false), 404)

	settings := `{"book_id":"` + f.book.ID + `","idempotency_key":"example-settings","revision":0,"name":"Example renamed Book","founder":""}`
	if w := ownerAPICall(s, f.session, http.MethodPost, "/api/v1/settings", settings, true); w.Code != 409 {
		t.Fatal("a logo change must conflict with a stale Settings revision", w.Code)
	}
	renamed := ownerAPICall(s, f.session, http.MethodPost, "/api/v1/settings", strings.Replace(settings, `"revision":0`, `"revision":1`, 1), true)
	var book ledger.Book
	if renamed.Code != 200 || json.Unmarshal(renamed.Body.Bytes(), &book) != nil || book.Name != "Example renamed Book" ||
		book.Logo == nil || *book.Logo != *saved.Logo || book.MetadataRevision == nil || *book.MetadataRevision != 2 {
		t.Fatal("display revision lost the active logo", renamed.Code, renamed.Body.String())
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewGray(image.Rect(0, 0, 64, 128))); err != nil {
		t.Fatal(err)
	}
	replaced := decodeOwnerResult[bookLogoReceipt](t, logoRequest(s, f.session, http.MethodPut, path, "image/png", logoDigest(pngData.Bytes()), pngData.Bytes(), true))
	if replaced.Logo == nil || replaced.Logo.ID == saved.Logo.ID || replaced.Logo.ContentType != "image/png" || replaced.MetadataRevision != 3 {
		t.Fatal("replacement did not produce a new immutable image")
	}
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, locator, "", false), 404)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodDelete, path, "", false), 403)
	removed := decodeOwnerResult[bookLogoReceipt](t, ownerAPICall(s, f.session, http.MethodDelete, path, "", true))
	if removed.Logo != nil || removed.MetadataRevision != 4 {
		t.Fatal("remove did not advance the Book revision")
	}
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, bookLogoPath+"/"+replaced.Logo.ID+"?book_id="+f.book.ID+"&revision="+replaced.Logo.SHA256, "", false), 404)
	history := decodeOwnerResult[ledger.BookLogoHistory](t, ownerAPICall(s, f.session, http.MethodGet, path, "", false))
	if history.Logo != nil || history.MetadataRevision != 4 || len(history.History) != 3 ||
		history.History[0].Logo != nil || history.History[1].Revision != 3 || *history.History[2].Logo != *saved.Logo {
		t.Fatal("history did not retain logo events across a settings change", history)
	}
	var actor string
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT identity_id::text FROM access.owners WHERE tenant_id=$1", f.tenant).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	for _, event := range history.History {
		if event.ActorKind != "owner" || event.ActorID != actor || event.RecordedAt.IsZero() {
			t.Fatal("owner provenance was lost", event)
		}
	}
	var blobs int
	if err := f.db.Admin.QueryRow(f.db.Context, "SELECT count(*) FROM public.book_logo_images").Scan(&blobs); err != nil || blobs != 0 {
		t.Fatal("removal retained an image blob", err)
	}
}

func TestAgentBookLogoRequiresLiveWriteAndRetainsExactOutcome(t *testing.T) {
	f, s := browserServer(t)
	_, foreign := otherAPIOwner(t, f)
	reader, writer := f.token(t, "read"), f.token(t, "write")
	scope, err := f.sessions.ResolveAgent(f.db.Context, writer)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := exampleJPEG(t, 32, false)
	digest := logoDigest(data)
	path := bookLogoPath + "?book_id=" + f.book.ID
	agent := func(method, path, token string, data []byte) *httptest.ResponseRecorder {
		return logoRequest(s, "", method, path, "image/jpeg", digest, data, false, [2]string{"Authorization", "Bearer " + token}, [2]string{"Idempotency-Key", "example-logo"})
	}
	ownerAPIStatus(t, agent(http.MethodPut, path, reader, data), 403)
	ownerAPIStatus(t, agent(http.MethodPut, bookLogoPath+"?book_id="+foreign.ID, writer, data), 404)
	ownerAPIStatus(t, logoRequest(s, f.session, http.MethodPut, path, "image/jpeg", digest, data, true, [2]string{"Authorization", "Bearer " + writer}), 403)
	first := decodeOwnerResult[bookLogoReceipt](t, agent(http.MethodPut, path, writer, data))
	if first.Logo == nil || first.MetadataRevision != 1 {
		t.Fatal("agent did not set the Book logo", first)
	}
	ownerAPIStatus(t, agent(http.MethodGet, path, writer, nil), 403)
	ownerAPIStatus(t, agent(http.MethodPost, "/api/v1/me/settings", writer, nil), 403)
	ownerAPIStatus(t, agent(http.MethodDelete, path, writer, nil), 409)
	// The MCP operation uses the same receipt as the direct remote write.
	q := map[string]any{"request_key": "example-logo", "content_type": "image/jpeg", "sha256": digest, "image_base64": base64.StdEncoding.EncodeToString(data)}
	result, err := remoteCall(f.db.Context, f.db.Runtime, f.sessions, writer, scope, nil, nil, "set_book_logo", proposalJSON(t, q))
	var retry bookLogoReceipt
	if err != nil || json.Unmarshal(result, &retry) != nil || retry.Logo == nil || *retry.Logo != *first.Logo || retry.MetadataRevision != 1 {
		t.Fatal("MCP retry did not recover the same receipt", err)
	}
	history := decodeOwnerResult[ledger.BookLogoHistory](t, ownerAPICall(s, f.session, http.MethodGet, path, "", false))
	if len(history.History) != 1 || history.History[0].ActorKind != "agent" || history.History[0].ActorID != scope.ActorID {
		t.Fatal("agent actor missing from the Book history", history)
	}
	removed := decodeOwnerResult[bookLogoReceipt](t, logoRequest(s, "", http.MethodDelete, path, "", "", nil, false,
		[2]string{"Authorization", "Bearer " + writer}))
	if removed.Logo != nil || removed.MetadataRevision != 2 {
		t.Fatal("write connection could not remove the logo", removed)
	}
	retry = decodeOwnerResult[bookLogoReceipt](t, agent(http.MethodPut, path, writer, data))
	history = decodeOwnerResult[ledger.BookLogoHistory](t, ownerAPICall(s, f.session, http.MethodGet, path, "", false))
	if retry.Logo == nil || *retry.Logo != *first.Logo || history.Logo != nil || history.MetadataRevision != 2 {
		t.Fatal("retry after removal changed the current logo", history)
	}
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, connectionPath+"/"+scope.ConnectionID+"/revoke", "{}", true), 200)
	if w := agent(http.MethodPut, path, writer, data); w.Code != 401 {
		t.Fatal("revoked credential reached logo write", w.Code)
	}
}
