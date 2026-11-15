package updatestatus

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
)

// The owner's backups, as the app sees them. The root helper and the
// installer write backups.json and restore-status.json; the app reads them
// and writes only the request file and notice.json, which the installer
// creates for the account that serves the app. Everything the app writes is
// untrusted input to the helper.
const (
	backupsFile       = "backups.json"
	restoreStatusFile = "restore-status.json"
	noticeFile        = "notice.json"
)

// Backup is the newest copy of the data this Vult can put back.
type Backup struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"` // "upgrade" (taken by the installer) or "update" (taken by the Update button)
	Created time.Time `json:"created_at"`
	// Version is the version whose data the copy holds; To is the version
	// installed after it. Either may be empty when not known.
	Version string `json:"version,omitempty"`
	To      string `json:"to,omitempty"`
	Size    int64  `json:"size"`
}

// Backups is what backups.json holds.
type Backups struct {
	Latest *Backup `json:"latest"`
}

// BackupID is the form of a backup's name: never a path.
var BackupID = regexp.MustCompile(`^(installer-)?[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

var jobID = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidJob reports whether s can name a restore.
func ValidJob(s string) bool { return jobID.MatchString(s) }

// WriteBackups records the newest backup; root only.
func WriteBackups(dir string, b Backups) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return writePublic(dir, backupsFile, data)
}

// ReadBackups returns the newest backup, if there is one.
func ReadBackups(dir string) (*Backup, error) {
	var b Backups
	ok, err := readJSON(filepath.Join(dir, backupsFile), &b)
	if err != nil || !ok || b.Latest == nil {
		return nil, err
	}
	if !BackupID.MatchString(b.Latest.ID) {
		return nil, errors.New("the backup record is not readable")
	}
	return b.Latest, nil
}

// RestoreStatus is how a restore the owner asked for is going.
type RestoreStatus struct {
	Job       string    `json:"job"`
	Backup    string    `json:"backup"`
	State     string    `json:"state"` // "running", "done" or "failed"
	Step      string    `json:"step,omitempty"`
	Error     string    `json:"error,omitempty"`
	Requested time.Time `json:"requested_at"`
	Updated   time.Time `json:"updated_at"`
}

// WriteRestoreStatus records a restore's progress; root only.
func WriteRestoreStatus(dir string, s RestoreStatus) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writePublic(dir, restoreStatusFile, data)
}

// ReadRestoreStatus returns the last restore's progress.
func ReadRestoreStatus(dir string) (RestoreStatus, bool) {
	var s RestoreStatus
	ok, err := readJSON(filepath.Join(dir, restoreStatusFile), &s)
	return s, err == nil && ok
}

// WriteRestoreRequest asks the helper to put a backup back.
func WriteRestoreRequest(dir, backup, job string, now time.Time) error {
	if !BackupID.MatchString(backup) || !ValidJob(job) {
		return errors.New("not a backup")
	}
	data, err := json.Marshal(Request{Restore: backup, Job: job, Requested: now.UTC()})
	if err != nil {
		return err
	}
	return writeSignal(dir, requestFile, data)
}

// NoticeSeen returns the backup whose notice the owner dismissed.
func NoticeSeen(dir string) string {
	f, err := os.OpenFile(filepath.Join(dir, noticeFile), os.O_RDONLY|syscallNoFollow, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	data := make([]byte, 257)
	n, _ := f.Read(data)
	var v struct {
		Seen string `json:"seen"`
	}
	if n == 0 || n > 256 || strictjson.Decode(data[:n], &v) != nil {
		return ""
	}
	return v.Seen
}

// MarkNoticeSeen dismisses the notice for one backup for good.
func MarkNoticeSeen(dir, backup string) error {
	if !BackupID.MatchString(backup) {
		return errors.New("not a backup")
	}
	data, _ := json.Marshal(map[string]string{"seen": backup})
	return writeSignal(dir, noticeFile, data)
}

// writePublic replaces name in dir atomically, readable by the app.
func writePublic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}
