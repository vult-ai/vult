package main

import "github.com/vult-ai/vult/internal/updatestatus"

// describeBackup is one sentence about a backup, for status.
func describeBackup(b updatestatus.Backup) string {
	when := b.Created.Local().Format("January 2, 2006 at 3:04 PM")
	if b.Kind == "upgrade" && b.To != "" {
		return "Updated to " + b.To + ". Your Vult was backed up first, on " + when + "; Vult settings can put that backup back."
	}
	return "The newest backup was taken on " + when + "; Vult settings can put it back."
}
