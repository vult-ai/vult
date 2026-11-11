package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/update"
	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/internal/updatetxn"
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

type fakeUpdater struct {
	report    updatestatus.Report
	requested []string
}

func (f *fakeUpdater) Report() updatestatus.Report { return f.report }
func (f *fakeUpdater) Request(version string) error {
	f.requested = append(f.requested, version)
	f.report.Requested = version
	return nil
}

// Where Vult has an installer, the signed-in owner can ask it to install an
// update; the request needs the usual Origin and CSRF proof and carries
// nothing but the version.
func TestOwnerCanAskTheInstallerToUpdate(t *testing.T) {
	f, plain := browserServer(t)
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	updater := &fakeUpdater{report: updatestatus.Report{Enabled: true, State: "idle"}}
	local, err := New(configuration(), Options{
		Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: login},
		Web:     fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}},
		Updater: updater,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := ownerAPICall(local, f.session, http.MethodGet, updatePath, "", false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"installer"`) {
		t.Fatal("the installer's status was not served", w.Code, w.Body.String())
	}
	body := `{"version":"0.7.1"}`
	if w := ownerAPICall(local, f.session, http.MethodPost, updateInstallPath, body, false); w.Code == http.StatusOK || len(updater.requested) != 0 {
		t.Fatal("an install was requested without the CSRF proof", w.Code)
	}
	if w := ownerAPICall(local, "", http.MethodPost, updateInstallPath, body, true); w.Code == http.StatusOK || len(updater.requested) != 0 {
		t.Fatal("an install was requested without a session", w.Code)
	}
	for _, bad := range []string{`{"version":"0.7.1","skip_checks":true}`, `{}`, `not json`} {
		if w := ownerAPICall(local, f.session, http.MethodPost, updateInstallPath, bad, true); w.Code == http.StatusOK {
			t.Fatalf("accepted %s", bad)
		}
	}
	w = ownerAPICall(local, f.session, http.MethodPost, updateInstallPath, body, true)
	if w.Code != http.StatusOK || len(updater.requested) != 1 || updater.requested[0] != "0.7.1" || !strings.Contains(w.Body.String(), `"requested":"0.7.1"`) {
		t.Fatal("the request was not passed on", w.Code, w.Body.String(), updater.requested)
	}
	if w := ownerAPICall(plain, f.session, http.MethodPost, updateInstallPath, body, true); w.Code == http.StatusOK {
		t.Fatal("a server without an installer accepted a request")
	}
}

func TestInstallerDiscoveryOffersAnUpdateWithoutABrowserChecker(t *testing.T) {
	f, _ := browserServer(t)
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	updater := &fakeUpdater{report: updatestatus.Report{
		Enabled: true, State: "idle", Installed: &updatetxn.Installation{Version: "0.7.0"},
		Release: &updatestatus.ReleaseStatus{Current: "0.7.0", Latest: "0.7.1", Checked: time.Now()},
	}}
	local, err := New(configuration(), Options{Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: login}, Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}}, Updater: updater})
	if err != nil {
		t.Fatal(err)
	}
	w := ownerAPICall(local, f.session, http.MethodGet, updatePath, "", false)
	var status struct {
		Current, Latest string
		Available       bool
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &status) != nil || !status.Available || status.Current != "0.7.0" || status.Latest != "0.7.1" {
		t.Fatalf("the helper's release was not offered: %d %s", w.Code, w.Body.String())
	}
	for _, release := range []updatestatus.ReleaseStatus{
		{Current: "0.7.0", Latest: "0.7.1", Checked: time.Now().Add(-updatestatus.CheckEvery)},
		{Current: "0.6.0", Latest: "0.7.1", Checked: time.Now()},
	} {
		updater.report.Release = &release
		w = ownerAPICall(local, f.session, http.MethodGet, updatePath, "", false)
		if json.Unmarshal(w.Body.Bytes(), &status) != nil || status.Available {
			t.Fatalf("offered stale metadata: %s", w.Body.String())
		}
	}
}
