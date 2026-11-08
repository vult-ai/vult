package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeUnitsRejectChangedDefinitionsBeforeControl(t *testing.T) {
	text := "LoadState=loaded\nFragmentPath=" + ledgerStartup + "\nDropInPaths=\nNeedDaemonReload=no\nUser=_vultledger\nGroup=_vultledger\nActiveState=active\nMainPID=42\nControlGroup=/system.slice/ai.vult.ledger.service\n"
	if _, err := parseNativeUnit([]byte(text), ledgerStartup, "_vultledger"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(text, "LoadState=loaded", "LoadState=not-found", 1),
		strings.Replace(text, "FragmentPath="+ledgerStartup, "FragmentPath=/run/systemd/transient/ai.vult.ledger.service", 1),
		strings.Replace(text, "DropInPaths=", "DropInPaths=/etc/systemd/system/service.d/override.conf", 1),
		strings.Replace(text, "NeedDaemonReload=no", "NeedDaemonReload=yes", 1),
		strings.Replace(text, "User=_vultledger", "User=root", 1),
		strings.Replace(text, "Group=_vultledger", "Group=root", 1),
		strings.Replace(text, "/system.slice/ai.vult.ledger.service", "/system.slice/other.service", 1),
		strings.Replace(text, "MainPID=42", "MainPID=unknown", 1),
		strings.Replace(text, "ActiveState=active", "ActiveState=inactive", 1),
		strings.Replace(text, "NeedDaemonReload=no\n", "", 1),
		text + "User=_vultledger\n",
	} {
		if _, err := parseNativeUnit([]byte(bad), ledgerStartup, "_vultledger"); err == nil {
			t.Fatal("substituted or ambiguous service definition was accepted")
		}
	}
	stopped := strings.Replace(strings.Replace(strings.Replace(text, "MainPID=42", "MainPID=0", 1), "ActiveState=active", "ActiveState=inactive", 1), "ControlGroup=/system.slice/ai.vult.ledger.service", "ControlGroup=", 1)
	if state, err := parseNativeUnit([]byte(stopped), ledgerStartup, "_vultledger"); err != nil || state.PID != 0 || state.State != "inactive" {
		t.Fatal("an already stopped installation was rejected", err)
	}
}

func TestNativeRemovalRequiresEmptyServiceProcessGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events")
	if err := cgroupStopped(path); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"populated 0\nfrozen 0\n", "populated 1\n", "", "populated 0\npopulated 0\n", "populated unknown\n", strings.Repeat("x", 1025)} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		err := cgroupStopped(path)
		if (err == nil) != (text == "populated 0\nfrozen 0\n") {
			t.Fatal("process cleanup was not verified correctly", err)
		}
	}
}

func TestProgramPublicationGuardsPreserveInterruptedUpdates(t *testing.T) {
	directory := t.TempDir()
	pending, started := filepath.Join(directory, "pending"), filepath.Join(directory, "runtime")
	uid := uint32(os.Geteuid())
	if err := programUpdateReady(pending, started, uid); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte("private update record"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := programUpdateReady(pending, started, uid); err == nil {
		t.Fatal("an incomplete publication could start")
	}
	if err := os.Rename(pending, started); err != nil {
		t.Fatal(err)
	}
	if err := programUpdateReady(pending, started, uid); err != nil {
		t.Fatal("published runtime was rejected", err)
	}
	if err := programUpdateReady(pending, started, uid+1); err == nil {
		t.Fatal("foreign update owner was accepted")
	}
	if err := os.Chmod(started, 0644); err != nil {
		t.Fatal(err)
	}
	if err := programUpdateReady(pending, started, uid); err == nil {
		t.Fatal("unprotected update state was accepted")
	}
	if err := os.Chmod(started, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(started, started+".copy"); err != nil {
		t.Fatal(err)
	}
	if err := programUpdateReady(pending, started, uid); err == nil {
		t.Fatal("linked update state was accepted")
	}
}

func TestLinuxSoftwareRemovalRetainsDataAndUnknownFiles(t *testing.T) {
	p := programFixture(t)
	oldStartup := p.Startup
	p.Startup = filepath.Join(filepath.Dir(oldStartup), "ai.vult.ledger.service")
	if err := os.Rename(oldStartup, p.Startup); err != nil {
		t.Fatal(err)
	}
	p.Policy = filepath.Join(filepath.Dir(p.Code), "policy", "ai.vult.owner.policy")
	browser := filepath.Join(filepath.Dir(p.Startup), "ai.vult.browser.service")
	writeFixture(t, browser, []byte("synthetic browser startup"), 0644)
	writeFixture(t, p.Policy, []byte("synthetic owner policy"), 0644)
	other := filepath.Join(filepath.Dir(p.Policy), "other.policy")
	writeFixture(t, other, []byte("unrelated policy"), 0644)
	var m removalManifest
	if err := json.Unmarshal(fixtureBytes(t, filepath.Join(p.Code, programManifest)), &m); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"NOTICE", "THIRD_PARTY_NOTICES.md", "postgres/bin/postgres", "postgres/bin/initdb"} {
		data := []byte("synthetic program: " + name)
		writeFixture(t, filepath.Join(p.Code, name), data, 0755)
		m.Files[name] = checksum(data)
	}
	m.Format, m.LaunchDaemonSHA256, m.BrowserDaemonSHA256 = "vult-program-files-linux-v1", "", ""
	m.ExternalFiles = map[string]string{ledgerStartup: checksum(fixtureBytes(t, p.Startup)), browserStartup: checksum(fixtureBytes(t, browser)), ownerPolicy: checksum(fixtureBytes(t, p.Policy))}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Code, programManifest), data, 0644); err != nil {
		t.Fatal(err)
	}
	before := retainedBytes(t, p)
	modified := filepath.Join(p.Code, "LICENSE")
	if err := os.WriteFile(modified, []byte("modified license"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := removeProgram(p, uint32(os.Geteuid()), func() error { return nil })
	if err != nil || len(result.Kept) == 0 {
		t.Fatal("retaining removal failed", err)
	}
	assertRetained(t, p, before)
	if string(fixtureBytes(t, modified)) != "modified license" || string(fixtureBytes(t, other)) != "unrelated policy" {
		t.Fatal("unknown or modified software was deleted")
	}
	for _, path := range []string{p.Startup, browser, p.Policy} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("unchanged managed startup remained", err)
		}
	}
}

func TestProgramControlLockSerializesInstallationAndRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.lock")
	if err := os.WriteFile(path, []byte("retained control record"), 0600); err != nil {
		t.Fatal(err)
	}
	before := fixtureBytes(t, path)
	lock, err := lockProgramControl(path, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if other, err := lockProgramControl(path, uint32(os.Geteuid())); err == nil {
		other.Close()
		t.Fatal("removal could race an admitted installer")
	}
	if string(fixtureBytes(t, path)) != string(before) {
		t.Fatal("lock contention changed the installed control state")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := lockProgramControl(path, uint32(os.Geteuid()))
	if err != nil {
		t.Fatal("completed control operation retained the lock", err)
	}
	next.Close()
}
