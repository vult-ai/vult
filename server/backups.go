package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/ledger"
)

// BackupStore is the owner's newest backup and a way to put it back, on a Vult
// whose installer keeps them. Putting it back is done by the installer's
// own privileged helper; this server only asks.
type BackupStore interface {
	LatestBackup() (*updatestatus.Backup, error)
	NoticeSeen() string
	MarkNoticeSeen(backup string) error
	RequestRestore(backup, job string) error
	RestoreStatus() (updatestatus.RestoreStatus, bool)
}

// Restoring a backup is for the owner in the browser only: a session
// cookie, a recent passkey sign-in and the confirmation word. Agents sign in
// with tokens, never a session, and the MCP server has no such tool.
const (
	backupsPath       = "/api/v1/backups"
	backupNoticePath  = "/api/v1/backups/notice-seen"
	backupRestorePath = "/api/v1/backups/restore"
	restoreConfirm    = "RESTORE"
)

func backupsRoute(path string) bool {
	return path == backupsPath || path == backupNoticePath || path == backupRestorePath || strings.HasPrefix(path, backupRestorePath+"/")
}

type backupView struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Version   string    `json:"version,omitempty"`
	To        string    `json:"to,omitempty"`
	Reason    string    `json:"reason"`
	Size      int64     `json:"size"`
	// Lost says plainly what putting it back loses.
	Lost string `json:"lost"`
}

func viewBackup(b *updatestatus.Backup) *backupView {
	if b == nil {
		return nil
	}
	return &backupView{ID: b.ID, CreatedAt: b.Created.UTC(), Version: b.Version, To: b.To, Reason: b.Kind, Size: b.Size,
		Lost: "Everything recorded in Vult after " + b.Created.Local().Format("January 2, 2006 at 3:04 PM") + " will be lost, in every Book, and you'll sign in again."}
}

func (a *webAPI) backupsAPI(w http.ResponseWriter, r *http.Request, session string) {
	// A token is never a sign-in here.
	if len(r.Header.Values("Authorization")) != 0 {
		failure(w, http.StatusForbidden)
		return
	}
	tenants, err := a.sessions.Tenants(r.Context(), session)
	if err != nil || len(tenants) == 0 {
		failure(w, http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == backupsPath && r.URL.RawQuery == "":
		a.backupsRead(w)
	case r.Method == http.MethodPost && r.URL.Path == backupNoticePath && a.backups != nil:
		var q struct {
			BackupID string `json:"backup_id"`
		}
		if !readBackupBody(w, r, &q) || a.backups.MarkNoticeSeen(q.BackupID) != nil {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"seen": q.BackupID})
	case r.Method == http.MethodPost && r.URL.Path == backupRestorePath && a.backups != nil:
		a.backupRestore(w, r, session)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, backupRestorePath+"/") && a.backups != nil:
		job := strings.TrimPrefix(r.URL.Path, backupRestorePath+"/")
		st, ok := a.backups.RestoreStatus()
		if !updatestatus.ValidJob(job) || !ok || st.Job != job {
			failure(w, http.StatusNotFound)
			return
		}
		out := map[string]any{"state": st.State}
		if st.Step != "" {
			out["step"] = st.Step
		}
		if st.Error != "" {
			out["error"] = st.Error
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		failure(w, http.StatusNotFound)
	}
}

func readBackupBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	return err == nil && strictjson.Decode(body, v) == nil
}

// backupsRead says whether this Vult can put a backup back, which one, and
// whether the owner still has to see that an upgrade backed it up first.
func (a *webAPI) backupsRead(w http.ResponseWriter) {
	out := map[string]any{"available": a.backups != nil, "backup": nil, "notice": nil}
	if a.backups != nil {
		b, err := a.backups.LatestBackup()
		if err != nil {
			failure(w, http.StatusServiceUnavailable)
			return
		}
		out["backup"] = viewBackup(b)
		if b != nil && b.Kind == "upgrade" && b.To != "" && a.backups.NoticeSeen() != b.ID {
			out["notice"] = map[string]any{"backup_id": b.ID, "version": b.To, "backed_up_at": b.Created.UTC()}
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *webAPI) backupRestore(w http.ResponseWriter, r *http.Request, session string) {
	var q struct {
		BackupID string `json:"backup_id"`
		Confirm  string `json:"confirm"`
	}
	if !readBackupBody(w, r, &q) || !updatestatus.BackupID.MatchString(q.BackupID) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	id, err := a.sessions.Identity(r.Context(), session)
	if err != nil {
		failure(w, http.StatusUnauthorized)
		return
	}
	// Putting a backup back needs the owner's passkey just now.
	if time.Since(id.AuthenticatedAt) > access.RecentSignIn {
		apiFailure(w, ledger.ErrFreshOwnerProof)
		return
	}
	if q.Confirm != restoreConfirm {
		apiFailure(w, &ledger.RefusedError{Reason: "Type RESTORE to put the backup back."})
		return
	}
	b, err := a.backups.LatestBackup()
	if err != nil || b == nil || b.ID != q.BackupID {
		apiFailure(w, &ledger.RefusedError{Reason: "That backup is no longer the newest one. Open Vult settings again."})
		return
	}
	if st, ok := a.backups.RestoreStatus(); ok && st.State == "running" {
		apiFailure(w, &ledger.RefusedError{Reason: "A restore is already running."})
		return
	}
	job := strings.ReplaceAll(ledger.ID(), "-", "")
	if err := a.backups.RequestRestore(q.BackupID, job); err != nil {
		failure(w, http.StatusServiceUnavailable)
		return
	}
	slog.Info("restore requested", "backup", q.BackupID, "backup_created", b.Created.UTC(), "job", job, "subject", id.Subject)
	_ = json.NewEncoder(w).Encode(map[string]any{"job_id": job})
}

// backupsOf is the updater's backups, where it keeps them.
func backupsOf(u Updater) BackupStore {
	b, _ := u.(BackupStore)
	return b
}
