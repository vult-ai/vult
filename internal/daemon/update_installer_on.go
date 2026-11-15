//go:build vult_updates

package daemon

import (
	"time"

	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/server"
)

// updaterFiles serves the installer's status and takes the owner's install
// request, through the installer's folder.
type updaterFiles struct{ dir string }

func (u updaterFiles) Report() updatestatus.Report {
	r := updatestatus.Read(u.dir)
	if r.Enabled && r.Installed != nil && r.State == "idle" && r.Requested == "" {
		r.Checking, _ = updatestatus.RequestCheck(u.dir, r.Installed.Version, time.Now())
	}
	return r
}

func (u updaterFiles) Request(version string) error {
	return updatestatus.WriteRequest(u.dir, updatestatus.Request{Version: version, Requested: time.Now().UTC()})
}

// installer is the update installer on macOS and Ubuntu, where this build
// has one.
func installer(goos string) server.Updater {
	if (goos != "darwin" && goos != "linux") || updatestatus.Dir == "" {
		return nil
	}
	return updaterFiles{updatestatus.Dir}
}

func (u updaterFiles) LatestBackup() (*updatestatus.Backup, error) {
	return updatestatus.ReadBackups(u.dir)
}

func (u updaterFiles) NoticeSeen() string { return updatestatus.NoticeSeen(u.dir) }

func (u updaterFiles) MarkNoticeSeen(backup string) error {
	return updatestatus.MarkNoticeSeen(u.dir, backup)
}

func (u updaterFiles) RequestRestore(backup, job string) error {
	return updatestatus.WriteRestoreRequest(u.dir, backup, job, time.Now())
}

func (u updaterFiles) RestoreStatus() (updatestatus.RestoreStatus, bool) {
	return updatestatus.ReadRestoreStatus(u.dir)
}
