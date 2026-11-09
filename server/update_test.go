package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/update"
)

// Vult on the owner's own computer tells the signed-in owner when a newer
// release is published; a server without the checker has no such route.
func TestUpdateStatusIsOnlyServedWhereEnabled(t *testing.T) {
	f, plain := browserServer(t)
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.5.2"}`))
	}))
	defer manifest.Close()
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	local, err := New(configuration(), Options{
		Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: login},
		Web:     fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}},
		Updates: &update.Checker{Current: "0.5.1", URL: manifest.URL, Client: manifest.Client(), Every: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := ownerAPICall(local, f.session, http.MethodGet, updatePath, "", false)
	var status struct {
		SchemaVersion int `json:"schema_version"`
		update.Status
	}
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatal("the update status was not served", w.Code, w.Body.String())
	}
	if status.SchemaVersion != 1 || !status.Available || status.Latest != "0.5.2" || status.Current != "0.5.1" {
		t.Fatal("the newer release was not reported", status)
	}
	if w := ownerAPICall(local, "", http.MethodGet, updatePath, "", false); w.Code != http.StatusUnauthorized {
		t.Fatal("the update status was served without a session", w.Code)
	}
	if w := ownerAPICall(plain, f.session, http.MethodGet, updatePath, "", false); w.Code == http.StatusOK {
		t.Fatal("a server without the checker answered", w.Code)
	}
}
