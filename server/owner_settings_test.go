package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func ownerAPICall(s *http.Server, session, method, path, body string, csrf bool) *httptest.ResponseRecorder {
	r := browserRequest(method, path, session)
	if body != "" {
		r.Body = io.NopCloser(strings.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if csrf {
		r.Header.Set("Origin", configuration().Origin)
		r.Header.Set("X-CSRF-Token", access.Hash("csrf:"+session))
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	return w
}

func ownerAPIStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") ||
		w.Header().Get("X-Vult-Schema-Version") != "1" {
		t.Fatalf("owner response status/headers: got %d %v, want %d: %s", w.Code, w.Header(), want, w.Body.String())
	}
	if want >= 400 && (!json.Valid(w.Body.Bytes()) || !strings.Contains(w.Body.String(), `"schema_version":1`)) {
		t.Fatal("error missing schema version")
	}
}

func ownerAPIBook(t *testing.T, f *documentServerFixture, id access.Identity) (string, ledger.Book) {
	t.Helper()
	ctx := f.db.Context
	tenant, err := f.operator.BindOwner(ctx, "", id)
	if err != nil {
		t.Fatal(err)
	}
	session, err := f.sessions.IssueSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var book ledger.Book
	if err = f.sessions.WithOwner(ctx, session, tenant, true, func(owner ledger.TenantAdmin) error {
		var err error
		book, err = owner.CreateBook(ctx, "Synthetic separate Book", "EUR", 2)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return session, book
}

func otherAPIOwner(t *testing.T, f *documentServerFixture) (string, ledger.Book) {
	t.Helper()
	return ownerAPIBook(t, f, access.Identity{Issuer: f.identity.Issuer, Subject: "other-example-owner", AuthenticatedAt: time.Now().UTC()})
}

func decodeOwnerResult[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	ownerAPIStatus(t, w, http.StatusOK)
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOwnerSettingsUseSessionIdentityAcrossBooksAndDevices(t *testing.T) {
	f, s := browserServer(t)
	second, _ := ownerAPIBook(t, f, f.identity)
	other, _ := otherAPIOwner(t, f)
	const path = "/api/v1/me/settings"
	get := func(session string) access.UserSettings {
		return decodeOwnerResult[access.UserSettings](t, ownerAPICall(s, session, http.MethodGet, path, "", false))
	}
	base := get(f.session)
	if base.UserID != access.UserID(f.identity) || base.Revision != "0" || base.Appearance != "system" ||
		base.Profile.Version != 1 || base.Profile.PhotoAsset != nil || base.Profile.DisplayName != "" ||
		base.Preferences != (access.UserPreferences{Version: 1, Language: "en", RegionalFormat: "en-US", DateFormat: "standard", TimeFormat: "24h", DigitGrouping: true, NegativeStyle: "minus"}) {
		t.Fatal("incorrect personal defaults", base)
	}
	sessionResponse := httptest.NewRecorder()
	s.Handler.ServeHTTP(sessionResponse, browserRequest(http.MethodGet, "/auth/session", f.session))
	if !strings.Contains(sessionResponse.Body.String(), base.UserID) {
		t.Fatal("settings do not bind the session's User ID")
	}
	body := `{"expected_revision":"0","profile":{"display_name":"Example Owner"},"preferences":{"regional_format":"en-IN","date_format":"iso","time_format":"12h","digit_grouping":false,"negative_style":"parentheses","language":"en"},"appearance":"dark"}`
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodPost, path, body, false), 403)
	saved := decodeOwnerResult[access.UserSettings](t, ownerAPICall(s, f.session, http.MethodPost, path, body, true))
	if saved.Revision != "1" || saved.Profile.DisplayName != "Example Owner" || saved.Appearance != "dark" ||
		saved.Preferences.RegionalFormat != "en-IN" || saved.Preferences.DigitGrouping || get(second) != saved {
		t.Fatal("save was not durable across independent sessions", saved)
	}
	if hidden := get(other); hidden.UserID == saved.UserID || hidden.Revision != "0" || hidden.Profile.DisplayName != "" {
		t.Fatal("personal settings crossed owners", hidden)
	}
	ownerAPIStatus(t, ownerAPICall(s, second, http.MethodPost, path, body, true), 409)

	for _, bad := range []string{
		`{"expected_revision":"01","appearance":"light"}`,
		`{"expected_revision":"1","appearance":null}`,
		`{"expected_revision":"1","profile":null}`,
		`{"expected_revision":"1","profile":{"display_name":null}}`,
		`{"expected_revision":"1","profile":{"Display_Name":"wrong"}}`,
		`{"expected_revision":"1","preferences":{"version":1}}`,
		`{"expected_revision":"1","preferences":{"digit_grouping":null}}`,
		`{"expected_revision":"1","preferences":{"regional_format":"xx-ZZ"}}`,
		`{"expected_revision":"1","preferences":{"language":"fr"}}`,
		`{"expected_revision":"1","appearance":"light","appearance":"dark"}`,
		`{"expected_revision":"1","book_id":"forged","appearance":"light"}`,
		`{"expected_revision":"1","user_id":"forged","appearance":"light"}`,
		`{"expected_revision":"1","profile":{"display_name":"` + strings.Repeat("🙂", 41) + `"}}`,
		`{"expected_revision":"1","profile":{"display_name":"\ud800"}}`,
	} {
		ownerAPIStatus(t, ownerAPICall(s, second, http.MethodPost, path, bad, true), 400)
	}
	ownerAPIStatus(t, ownerAPICall(s, second, http.MethodGet, path+"?book_id="+f.book.ID, "", false), 400)
	ownerAPIStatus(t, ownerAPICall(s, second, http.MethodPost, path, strings.Repeat(" ", userSettingsLimit+1), true), 413)

	results := make(chan *httptest.ResponseRecorder, 2)
	for i, session := range []string{f.session, second} {
		go func() {
			patch := `{"expected_revision":"1","appearance":"` + []string{"system", "light"}[i] + `"}`
			results <- ownerAPICall(s, session, http.MethodPost, path, patch, true)
		}()
	}
	a, b := <-results, <-results
	if a.Code > b.Code {
		a, b = b, a
	}
	ownerAPIStatus(t, a, 200)
	ownerAPIStatus(t, b, 409)
	if latest := get(second); latest.Revision != "2" || latest.Preferences != saved.Preferences || latest.Profile != saved.Profile {
		t.Fatal("partial concurrent patch changed unrelated settings", latest)
	}
	if _, err := f.db.Runtime.Exec(f.db.Context, "SELECT * FROM access.user_settings"); err == nil {
		t.Fatal("ledger runtime role can read personal settings")
	}
	var visible int
	if err := f.owners.QueryRow(f.db.Context, "SELECT count(*) FROM access.user_settings").Scan(&visible); err != nil || visible != 0 {
		t.Fatal("owner database role could see settings without a session", visible, err)
	}
	if err := f.operator.RevokeIdentity(f.db.Context, f.identity); err != nil {
		t.Fatal(err)
	}
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, path, "", false), 401)
}

func exampleJPEG(t *testing.T, side int, dark bool) ([]byte, string) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, side, side))
	if !dark {
		img.SetGray(0, 0, color.Gray{Y: 255})
	}
	var data bytes.Buffer
	if err := jpeg.Encode(&data, img, nil); err != nil {
		t.Fatal(err)
	}
	return data.Bytes(), "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestOwnerAvatarIsPrivateVersionedAndBounded(t *testing.T) {
	f, s := browserServer(t)
	other, _ := otherAPIOwner(t, f)
	const path = "/api/v1/me/settings"
	bytes1, url1 := exampleJPEG(t, 256, true)
	_, url2 := exampleJPEG(t, 256, false)
	_, small := exampleJPEG(t, 16, true)
	save := func(revision, url string) *httptest.ResponseRecorder {
		return ownerAPICall(s, f.session, http.MethodPost, path,
			`{"expected_revision":"`+revision+`","profile":{"photo":`+url+`}}`, true)
	}
	saved := decodeOwnerResult[access.UserSettings](t, save("0", `"`+url1+`"`))
	photo := saved.Profile.PhotoAsset
	digest := sha256.Sum256(bytes1)
	if saved.Revision != "1" || saved.Profile.Photo != nil || photo == nil ||
		!ledger.ValidID(photo.ID) || photo.SHA256 != hex.EncodeToString(digest[:]) || photo.Size != len(bytes1) ||
		photo.ContentType != "image/jpeg" {
		t.Fatal("unexpected avatar metadata", photo)
	}
	locator := avatarPrefix + photo.ID + "?revision=" + photo.SHA256
	w := ownerAPICall(s, f.session, http.MethodGet, locator, "", false)
	ownerAPIStatus(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), bytes1) || w.Header().Get("Content-Type") != "image/jpeg" ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("avatar response is not the private original JPEG")
	}
	ownerAPIStatus(t, ownerAPICall(s, other, http.MethodGet, locator, "", false), 404)
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, avatarPrefix+photo.ID+"?revision="+strings.Repeat("0", 64), "", false), 404)
	for _, bad := range []string{small, "data:image/jpeg;base64,/9j/AAAA", "data:image/png;base64,AAAA", locator} {
		ownerAPIStatus(t, save("1", `"`+bad+`"`), 400)
	}
	ownerAPIStatus(t, save("1", `"data:image/jpeg;base64,`+strings.Repeat("A", 196608)+`"`), 413)
	replaced := decodeOwnerResult[access.UserSettings](t, save("1", `"`+url2+`"`))
	if replaced.Revision != "2" || replaced.Profile.PhotoAsset == nil || replaced.Profile.PhotoAsset.ID == photo.ID {
		t.Fatal("avatar replacement reused the obsolete asset")
	}
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, locator, "", false), 404)
	removed := decodeOwnerResult[access.UserSettings](t, save("2", "null"))
	if removed.Revision != "3" || removed.Profile.PhotoAsset != nil || removed.Profile.Photo != nil {
		t.Fatal("avatar not removed")
	}
	last := replaced.Profile.PhotoAsset
	ownerAPIStatus(t, ownerAPICall(s, f.session, http.MethodGet, avatarPrefix+last.ID+"?revision="+last.SHA256, "", false), 404)
}
