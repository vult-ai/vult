//go:build !vult_updates

package daemon

import "github.com/vult-ai/vult/server"

// This build is updated with vult update.
func installer(string) server.Updater { return nil }
