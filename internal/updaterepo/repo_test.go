package updaterepo

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"

	"github.com/vult-ai/vult/internal/updatetrust"
)

var testNow = time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)

const testPassphrase = "correct horse battery staple"

func init() { ScryptWorkFactor = 10 }

type testKeys struct {
	dir   string
	files map[string]string
	pub   map[string]*metadata.Key
}

func newKeys(t *testing.T, names ...string) testKeys {
	t.Helper()
	k := testKeys{dir: t.TempDir(), files: map[string]string{}, pub: map[string]*metadata.Key{}}
	for _, name := range names {
		file := filepath.Join(k.dir, name+".key")
		pub, _, err := NewKey(file, []byte(testPassphrase))
		if err != nil {
			t.Fatal(err)
		}
		k.files[name], k.pub[name] = file, pub
	}
	return k
}

func (k testKeys) open(t *testing.T, name string) *Signer {
	t.Helper()
	s, err := OpenKey(k.files[name], []byte(testPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func yes(string) bool { return true }

type fixture struct {
	repo Repo
	keys testKeys
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	k := newKeys(t, "K1", "K2", "K3", "T1", "T2", "T3", "S", "B", "N", "M")
	r, err := Init(filepath.Join(t.TempDir(), "repo"), Keys{
		Root:    []*metadata.Key{k.pub["K1"], k.pub["K2"], k.pub["K3"]},
		Targets: []*metadata.Key{k.pub["T1"], k.pub["T2"], k.pub["T3"]},
		Release: []*metadata.Key{k.pub["S"]}, Rebuild: k.pub["B"], Snapshot: k.pub["N"], Timestamp: k.pub["M"],
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{repo: r, keys: k}
	for _, step := range []struct{ role, key string }{
		{metadata.ROOT, "K1"}, {metadata.ROOT, "K2"},
		{metadata.TARGETS, "T1"}, {metadata.TARGETS, "T2"},
		{updatetrust.RoleReleases, "S"}, {updatetrust.RoleHolds, "S"},
		{updatetrust.RoleRebuild, "B"},
		{updatetrust.RoleUpdater, "K1"}, {updatetrust.RoleUpdater, "K2"},
	} {
		f.sign(t, step.role, step.key)
	}
	f.publish(t)
	return f
}

func (f fixture) sign(t *testing.T, role, key string) {
	t.Helper()
	if err := f.repo.Sign(role, f.keys.open(t, key), yes); err != nil {
		t.Fatalf("sign %s with %s: %v", role, key, err)
	}
}

func (f fixture) publish(t *testing.T) {
	t.Helper()
	if err := f.repo.Freshen(f.keys.open(t, "N"), f.keys.open(t, "M"), testNow); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Publish(testNow); err != nil {
		t.Fatal(err)
	}
}

func contract(version string) updatetrust.Contract {
	return updatetrust.Contract{
		Version: version, Platform: updatetrust.PlatformMacOS, MinimumOS: "26.0",
		SchemaFrom: []string{"0005", "0006", "0007"}, SchemaTo: "0008",
		Migrations:     map[string]string{"0008_example.sql": strings.Repeat("a", 64)},
		MinimumUpdater: "1.0.0",
		PostgreSQL:     updatetrust.PostgreSQL{Major: 18, Version: "18.6", SourceSHA256: strings.Repeat("b", 64)},
		Source:         updatetrust.Source{Repository: "example/vult", Commit: strings.Repeat("c", 40), Tag: "v" + version},
		Build:          updatetrust.Build{Method: updatetrust.BuildSignatureRemoved, InputsSHA256: strings.Repeat("d", 64)},
	}
}

func artifactFile(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "payload.tar")
	if err := os.WriteFile(file, bytes.Repeat([]byte(content), 256), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func (f fixture) published() string { return filepath.Join(f.repo.Dir, "published") }

func TestReleaseCycleProducesWhatInstallationsAccept(t *testing.T) {
	f := newFixture(t)
	if got, _, err := Check(f.published(), updatetrust.PlatformMacOS, "0.7.0", testNow); err != nil || got != nil {
		t.Fatalf("empty repository: %v, %v", got, err)
	}
	art := artifactFile(t, "payload")
	if _, err := f.repo.AddRelease(art, contract("0.7.1"), testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.AddRebuild(art, contract("0.7.1"), testNow); err != nil {
		t.Fatal(err)
	}
	f.sign(t, updatetrust.RoleRebuild, "B")
	var shown string
	if err := f.repo.Sign(updatetrust.RoleReleases, f.keys.open(t, "S"), func(s string) bool { shown = s; return true }); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NEW Vult 0.7.1 for macos-arm64", "example/vult at " + strings.Repeat("c", 40), "from 0005, 0006, 0007 to 0008", "rebuild agrees"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("summary is missing %q:\n%s", want, shown)
		}
	}
	f.publish(t)
	got, refused, err := Check(f.published(), updatetrust.PlatformMacOS, "0.7.0", testNow)
	if err != nil || got == nil || got.Contract.Version != "0.7.1" || len(refused) != 0 {
		t.Fatalf("Check() = %+v, %v, %v", got, refused, err)
	}
}

func TestOwnerCannotSignBeforeTheRebuildAgrees(t *testing.T) {
	f := newFixture(t)
	art := artifactFile(t, "payload")
	if _, err := f.repo.AddRelease(art, contract("0.7.1"), testNow); err != nil {
		t.Fatal(err)
	}
	asked := false
	err := f.repo.Sign(updatetrust.RoleReleases, f.keys.open(t, "S"), func(string) bool { asked = true; return true })
	if err == nil || !strings.Contains(err.Error(), "rebuild") || asked {
		t.Fatalf("Sign() before rebuild = %v (asked %v)", err, asked)
	}
	// Attested but not yet signed by the rebuild key is still not agreement.
	if _, err := f.repo.AddRebuild(art, contract("0.7.1"), testNow); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Sign(updatetrust.RoleReleases, f.keys.open(t, "S"), yes); err == nil {
		t.Fatal("signed against an unsigned rebuild")
	}
}

func TestOwnerCannotSignWhenTheRebuildDisagrees(t *testing.T) {
	for name, rebuild := range map[string]func(t *testing.T) (string, updatetrust.Contract){
		"other bytes": func(t *testing.T) (string, updatetrust.Contract) {
			return artifactFile(t, "PAYLOAD"), contract("0.7.1")
		},
		"other contract": func(t *testing.T) (string, updatetrust.Contract) {
			c := contract("0.7.1")
			c.SchemaTo = "0009"
			return artifactFile(t, "payload"), c
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if _, err := f.repo.AddRelease(artifactFile(t, "payload"), contract("0.7.1"), testNow); err != nil {
				t.Fatal(err)
			}
			art, c := rebuild(t)
			if _, err := f.repo.AddRebuild(art, c, testNow); err != nil {
				t.Fatal(err)
			}
			f.sign(t, updatetrust.RoleRebuild, "B")
			if err := f.repo.Sign(updatetrust.RoleReleases, f.keys.open(t, "S"), yes); err == nil || !strings.Contains(err.Error(), "disagrees") {
				t.Fatalf("Sign() = %v, want disagreement", err)
			}
		})
	}
}

func TestOnlyTheRolesOwnKeysCanSign(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct{ role, key string }{
		{updatetrust.RoleReleases, "B"}, {updatetrust.RoleRebuild, "S"},
		{metadata.ROOT, "T1"}, {updatetrust.RoleUpdater, "S"}, {metadata.TARGETS, "S"},
	} {
		if err := f.repo.Sign(tc.role, f.keys.open(t, tc.key), yes); err == nil || !strings.Contains(err.Error(), "not one of the keys") {
			t.Fatalf("%s signed %s: %v", tc.key, tc.role, err)
		}
	}
}

func TestDecliningTheSummarySignsNothing(t *testing.T) {
	f := newFixture(t)
	if err := f.repo.Hold("0.7.1", testNow); err == nil {
		t.Fatal("held a version that was never listed")
	}
	before, _ := os.ReadFile(f.repo.staged(updatetrust.RoleHolds))
	if err := f.repo.Sign(updatetrust.RoleHolds, f.keys.open(t, "S"), func(string) bool { return false }); err == nil {
		t.Fatal("declining still signed")
	}
	after, _ := os.ReadFile(f.repo.staged(updatetrust.RoleHolds))
	if !bytes.Equal(before, after) {
		t.Fatal("declining changed the staged role")
	}
}

func TestUnsignedChangesAreNeverPublished(t *testing.T) {
	f := newFixture(t)
	art := artifactFile(t, "payload")
	if _, err := f.repo.AddRelease(art, contract("0.7.1"), testNow); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Freshen(f.keys.open(t, "N"), f.keys.open(t, "M"), testNow); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Publish(testNow); err == nil || !strings.Contains(err.Error(), "releases is not signed") {
		t.Fatalf("Publish() = %v", err)
	}
}

func TestHeldReleaseIsPublishedButNotInstallable(t *testing.T) {
	f := newFixture(t)
	art := artifactFile(t, "payload")
	f.repo.AddRelease(art, contract("0.7.1"), testNow)
	f.repo.AddRebuild(art, contract("0.7.1"), testNow)
	f.sign(t, updatetrust.RoleRebuild, "B")
	f.sign(t, updatetrust.RoleReleases, "S")
	f.publish(t)
	if err := f.repo.Hold("0.7.1", testNow); err != nil {
		t.Fatal(err)
	}
	f.sign(t, updatetrust.RoleHolds, "S")
	f.publish(t)
	got, refused, err := Check(f.published(), updatetrust.PlatformMacOS, "0.7.0", testNow)
	if err != nil || got != nil || len(refused) != 1 || !errors.Is(refused[0].Err, updatetrust.ErrHeld) {
		t.Fatalf("Check() = %v, %+v, %v", got, refused, err)
	}
}

func TestVersionsOnlyMoveForwardAndNeverChangeOncePublished(t *testing.T) {
	f := newFixture(t)
	art := artifactFile(t, "payload")
	if _, err := f.repo.AddRelease(art, contract("0.7.2"), testNow); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"0.7.2", "0.7.1"} {
		if _, err := f.repo.AddRelease(artifactFile(t, "other"), contract(v), testNow); err == nil {
			t.Fatalf("listed %s after 0.7.2", v)
		}
	}
	// Re-signing a published version with different content is refused.
	md, err := f.repo.loadTargets(updatetrust.RoleHolds)
	if err != nil {
		t.Fatal(err)
	}
	md.Signed.Version = f.repo.published(updatetrust.RoleHolds)
	md.Signed.Expires = md.Signed.Expires.Add(time.Hour)
	md.ClearSignatures()
	if err := save(f.repo.staged(updatetrust.RoleHolds), md); err != nil {
		t.Fatal(err)
	}
	f.sign(t, updatetrust.RoleHolds, "S")
	f.sign(t, updatetrust.RoleRebuild, "B")
	f.repo.AddRebuild(art, contract("0.7.2"), testNow)
	f.sign(t, updatetrust.RoleRebuild, "B")
	f.sign(t, updatetrust.RoleReleases, "S")
	if err := f.repo.Freshen(f.keys.open(t, "N"), f.keys.open(t, "M"), testNow); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Publish(testNow); err == nil || !strings.Contains(err.Error(), "already published") {
		t.Fatalf("Publish() = %v", err)
	}
}

func TestKeyFilesOpenOnlyWithTheirPassphraseAndRestoreFromACopy(t *testing.T) {
	k := newKeys(t, "S")
	if _, err := OpenKey(k.files["S"], []byte("not the passphrase at all")); err == nil {
		t.Fatal("opened with the wrong passphrase")
	}
	if _, _, err := NewKey(filepath.Join(k.dir, "short.key"), []byte("short")); err == nil {
		t.Fatal("accepted a short passphrase")
	}
	if _, _, err := NewKey(k.files["S"], []byte(testPassphrase)); err == nil {
		t.Fatal("overwrote an existing key file")
	}
	sealed, err := os.ReadFile(k.files["S"])
	if err != nil || bytes.Contains(sealed, []byte("seed")) {
		t.Fatalf("key file is not encrypted: %v", err)
	}
	if info, _ := os.Stat(k.files["S"]); info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode %v", info.Mode())
	}
	copyB := filepath.Join(t.TempDir(), "S.key")
	if err := os.WriteFile(copyB, sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := TestKey(copyB, []byte(testPassphrase))
	wantID, _ := k.pub["S"].ID()
	if err != nil || id != wantID {
		t.Fatalf("TestKey() = %s, %v; want %s", id, err, wantID)
	}
	damaged := append(bytes.Clone(sealed[:len(sealed)-8]), "XXXXXXXX"...)
	if err := os.WriteFile(copyB+".damaged", damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := TestKey(copyB+".damaged", []byte(testPassphrase)); err == nil {
		t.Fatal("a damaged copy passed the restore test")
	}
}

func TestPublishRefusesAReleaseTheRebuildNoLongerAgreesWith(t *testing.T) {
	f := newFixture(t)
	art := artifactFile(t, "payload")
	f.repo.AddRelease(art, contract("0.7.1"), testNow)
	f.repo.AddRebuild(art, contract("0.7.1"), testNow)
	f.sign(t, updatetrust.RoleRebuild, "B")
	f.sign(t, updatetrust.RoleReleases, "S")
	// The rebuild's record changes after the owner signed.
	md, err := f.repo.loadTargets(updatetrust.RoleRebuild)
	if err != nil {
		t.Fatal(err)
	}
	delete(md.Signed.Targets, updatetrust.RebuildTargetName(updatetrust.TargetName("0.7.1", updatetrust.PlatformMacOS)))
	f.repo.change(updatetrust.RoleRebuild, md, testNow)
	if err := save(f.repo.staged(updatetrust.RoleRebuild), md); err != nil {
		t.Fatal(err)
	}
	f.sign(t, updatetrust.RoleRebuild, "B")
	if err := f.repo.Freshen(f.keys.open(t, "N"), f.keys.open(t, "M"), testNow); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(f.repo.metadataDir())
	if err := f.repo.Publish(testNow); err == nil || !errors.Is(err, updatetrust.ErrNoAgreement) {
		t.Fatalf("Publish() = %v, want no agreement", err)
	}
	after, _ := os.ReadDir(f.repo.metadataDir())
	if len(before) != len(after) {
		t.Fatal("a refused publish wrote files")
	}
}
