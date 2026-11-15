package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/updatestatus"
)

// backupsUpdater keeps its files in a folder, as the installer's does.
type backupsUpdater struct {
	fakeUpdater
	dir string
}

func (u *backupsUpdater) LatestBackup() (*updatestatus.Backup, error) {
	return updatestatus.ReadBackups(u.dir)
}
func (u *backupsUpdater) NoticeSeen() string { return updatestatus.NoticeSeen(u.dir) }
func (u *backupsUpdater) MarkNoticeSeen(b string) error {
	return updatestatus.MarkNoticeSeen(u.dir, b)
}
func (u *backupsUpdater) RequestRestore(b, job string) error {
	return updatestatus.WriteRestoreRequest(u.dir, b, job, time.Now())
}
func (u *backupsUpdater) RestoreStatus() (updatestatus.RestoreStatus, bool) {
	return updatestatus.ReadRestoreStatus(u.dir)
}

const synthBackup = "installer-20261113T090000Z-0a0b0c0d"

func backupsServer(t *testing.T) (*documentServerFixture, *http.Server, *backupsUpdater) {
	t.Helper()
	f, _ := browserServer(t)
	login, err := access.NewHTTP(f.sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	u := &backupsUpdater{fakeUpdater: fakeUpdater{report: updatestatus.Report{Enabled: true, State: "idle"}}, dir: t.TempDir()}
	for _, name := range []string{"request.json", "notice.json"} {
		if err := os.WriteFile(filepath.Join(u.dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	created := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	if err := updatestatus.WriteBackups(u.dir, updatestatus.Backups{Latest: &updatestatus.Backup{ID: synthBackup, Kind: "upgrade", Created: created, Version: "0.8.0-rc.5", To: "0.8.0", Size: 4096}}); err != nil {
		t.Fatal(err)
	}
	s, err := New(configuration(), Options{
		Database: f.db.Runtime, Access: &Access{Sessions: f.sessions, HTTP: login},
		Web:     fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}},
		Updater: u,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, s, u
}

func restoreAsked(u *backupsUpdater) bool {
	r, ok := updatestatus.ReadRequest(u.dir)
	return ok && r.Restore != ""
}

// The owner sees the newest backup, what putting it back loses, and the
// notice after an upgrade until they dismiss it.
func TestOwnerSeesTheBackupAndTheUpgradeNotice(t *testing.T) {
	f, s, _ := backupsServer(t)
	var out struct {
		Available bool        `json:"available"`
		Backup    *backupView `json:"backup"`
		Notice    *struct {
			BackupID string `json:"backup_id"`
			Version  string `json:"version"`
		} `json:"notice"`
	}
	read := func() {
		t.Helper()
		w := ownerAPICall(s, f.session, http.MethodGet, backupsPath, "", false)
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	read()
	if !out.Available || out.Backup == nil || out.Backup.ID != synthBackup || out.Backup.Reason != "upgrade" || out.Backup.Version != "0.8.0-rc.5" || out.Backup.Size != 4096 {
		t.Fatal("the backup was not described", out.Backup)
	}
	// It warns that everything after the backup goes, with when that was.
	when := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC).Local().Format("January 2, 2006 at 3:04 PM")
	if !strings.Contains(out.Backup.Lost, "after "+when) || !strings.Contains(out.Backup.Lost, "lost") {
		t.Fatal("the loss is not stated", out.Backup.Lost)
	}
	if out.Notice == nil || out.Notice.BackupID != synthBackup || out.Notice.Version != "0.8.0" {
		t.Fatal("no upgrade notice", out.Notice)
	}
	if w := ownerAPICall(s, f.session, http.MethodPost, backupNoticePath, `{"backup_id":"`+synthBackup+`"}`, true); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	out.Notice = nil
	read()
	if out.Notice != nil {
		t.Fatal("the dismissed notice came back")
	}
	// A server without backups says so instead of failing.
	g, plain := browserServer(t)
	w := ownerAPICall(plain, g.session, http.MethodGet, backupsPath, "", false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

// Putting the backup back is asked of the installer only for the owner's
// own browser session, signed in just now, with the confirmation word.
func TestRestoreNeedsTheOwnerJustNow(t *testing.T) {
	f, s, u := backupsServer(t)
	body := `{"backup_id":"` + synthBackup + `","confirm":"RESTORE"}`
	if w := ownerAPICall(s, f.session, http.MethodPost, backupRestorePath, body, false); w.Code == http.StatusOK || restoreAsked(u) {
		t.Fatal("asked without the CSRF proof", w.Code)
	}
	// An agent's token is never a sign-in for this.
	r := browserRequest(http.MethodPost, backupRestorePath, f.session)
	r.Body = io.NopCloser(strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer synthetic-agent-token")
	r.Header.Set("Origin", configuration().Origin)
	r.Header.Set("X-CSRF-Token", access.Hash("csrf:"+f.session))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || restoreAsked(u) {
		t.Fatal("an agent's request was accepted", w.Code)
	}
	for _, bad := range []string{
		`{"backup_id":"` + synthBackup + `","confirm":"restore"}`,
		`{"backup_id":"` + synthBackup + `"}`,
		`{"backup_id":"installer-20261101T000000Z-0a0b0c0d","confirm":"RESTORE"}`,
		`{"backup_id":"../data","confirm":"RESTORE"}`,
	} {
		if w := ownerAPICall(s, f.session, http.MethodPost, backupRestorePath, bad, true); w.Code == http.StatusOK || restoreAsked(u) {
			t.Fatalf("accepted %s: %d", bad, w.Code)
		}
	}
	// A sign-in from before the recent window has to be made again.
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at = clock_timestamp() - interval '1 hour'"); err != nil {
		t.Fatal(err)
	}
	w = ownerAPICall(s, f.session, http.MethodPost, backupRestorePath, body, true)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "fresh_authentication_required") || restoreAsked(u) {
		t.Fatal("an old sign-in was accepted", w.Code, w.Body.String())
	}
	if _, err := f.db.Admin.Exec(f.db.Context, "UPDATE access.sessions SET authenticated_at = clock_timestamp()"); err != nil {
		t.Fatal(err)
	}
	w = ownerAPICall(s, f.session, http.MethodPost, backupRestorePath, body, true)
	var started struct {
		Job string `json:"job_id"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &started) != nil || !updatestatus.ValidJob(started.Job) {
		t.Fatal(w.Code, w.Body.String())
	}
	req, ok := updatestatus.ReadRequest(u.dir)
	if !ok || req.Restore != synthBackup || req.Job != started.Job || req.Version != "" {
		t.Fatal("the installer was not asked", req)
	}
	// Progress is read from the installer's own record, so it answers
	// after Vult restarts.
	if w := ownerAPICall(s, f.session, http.MethodGet, backupRestorePath+"/"+started.Job, "", false); w.Code != http.StatusNotFound {
		t.Fatal("progress before the installer wrote any", w.Code)
	}
	if err := updatestatus.WriteRestoreStatus(u.dir, updatestatus.RestoreStatus{Job: started.Job, Backup: synthBackup, State: "running", Step: "Putting the backup back"}); err != nil {
		t.Fatal(err)
	}
	w = ownerAPICall(s, f.session, http.MethodGet, backupRestorePath+"/"+started.Job, "", false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"state":"running"`) || !strings.Contains(w.Body.String(), "Putting the backup back") {
		t.Fatal(w.Code, w.Body.String())
	}
	// One at a time.
	if w := ownerAPICall(s, f.session, http.MethodPost, backupRestorePath, body, true); w.Code == http.StatusOK {
		t.Fatal("a second restore started while one runs")
	}
}

// When the backup has gone, or is damaged and no longer listed, there is
// nothing to put back.
func TestRestoreWithoutABackupIsRefused(t *testing.T) {
	f, s, u := backupsServer(t)
	if err := updatestatus.WriteBackups(u.dir, updatestatus.Backups{}); err != nil {
		t.Fatal(err)
	}
	w := ownerAPICall(s, f.session, http.MethodGet, backupsPath, "", false)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"backup":null`) {
		t.Fatal(w.Code, w.Body.String())
	}
	body := `{"backup_id":"` + synthBackup + `","confirm":"RESTORE"}`
	if w := ownerAPICall(s, f.session, http.MethodPost, backupRestorePath, body, true); w.Code == http.StatusOK || restoreAsked(u) {
		t.Fatal("a missing backup was asked for", w.Code)
	}
	if err := os.WriteFile(filepath.Join(u.dir, "backups.json"), []byte(`{"latest":{"id":"../../etc"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := ownerAPICall(s, f.session, http.MethodGet, backupsPath, "", false); w.Code == http.StatusOK {
		t.Fatal("a damaged record was served", w.Body.String())
	}
}
