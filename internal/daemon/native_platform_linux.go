package daemon

import (
	"errors"
	"os"
	"runtime"
	"syscall"
)

const programRoot = "/opt/vult"
const ledgerStartup = "/etc/systemd/system/ai.vult.ledger.service"
const browserStartup = "/etc/systemd/system/ai.vult.browser.service"
const ownerPolicy = "/usr/share/polkit-1/actions/ai.vult.owner.policy"
const browserTrustIdentity = "/var/lib/vult/browser-identity.json"
const pendingProgramUpdate = "/var/lib/vult/program-update.json"
const runtimeProgramUpdate = "/var/lib/vult/program-update-runtime.json"

func nativeSupported() bool { return runtime.GOARCH == "amd64" }
func programUpdateReady(pending, started string, uid uint32) error {
	if _, err := os.Lstat(pending); !os.IsNotExist(err) {
		return errors.New("software update is incomplete; rerun its pinned installer before starting or removing Vult")
	}
	info, err := os.Lstat(started)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("software update state is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uid || stat.Nlink != 1 || info.Mode().Perm() != 0600 {
		return errors.New("software update state has unsafe ownership or permissions")
	}
	return nil
}
func nativeStartupReady() error {
	return programUpdateReady(pendingProgramUpdate, runtimeProgramUpdate, 0)
}
func nativeRemovalPaths() removalPaths {
	return removalPaths{Code: programRoot, Startup: ledgerStartup, Policy: ownerPolicy, Shortcut: "/usr/local/bin/vult", Retained: "/var/lib/vult"}
}
