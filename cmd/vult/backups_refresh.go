//go:build vult_updates && (darwin || linux)

package main

import (
	"os"

	"github.com/vult-ai/vult/internal/updatehelper"
	"github.com/vult-ai/vult/internal/updatestatus"
)

// refreshBackups records the newest backup for the app, where this
// installation has an update installer.
func refreshBackups() {
	l := updatehelper.DefaultLayout
	if info, err := os.Stat(l.State); err == nil && info.IsDir() {
		_ = updatehelper.RefreshBackups(l)
	}
}

// backupLine says what the newest backup is, for status.
func backupLine() string {
	b, err := updatestatus.ReadBackups(updatehelper.DefaultLayout.State)
	if err != nil || b == nil {
		return ""
	}
	return describeBackup(*b)
}
