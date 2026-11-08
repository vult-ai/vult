//go:build !linux

package daemon

import (
	"github.com/vult-ai/vult/internal/local"
	"path/filepath"
	"runtime"
)

const programRoot = "/Library/PrivilegedHelperTools/ai.vult.ledger"
const ledgerStartup = "/Library/LaunchDaemons/ai.vult.ledger.plist"
const browserStartup = "/Library/LaunchDaemons/ai.vult.browser.plist"
const browserTrustIdentity = "/Library/Application Support/ai.vult.ledger/browser-identity.json"
const browserSetupApp = "/Library/PrivilegedHelperTools/ai.vult.ledger/Vult Browser Setup.app"

func nativeSupported() bool     { return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" }
func nativeStartupReady() error { return nil }
func nativeRemovalPaths() removalPaths {
	return removalPaths{Code: programRoot, Startup: ledgerStartup, Shortcut: "/usr/local/bin/vult", Retained: filepath.Dir(local.DataPath)}
}
