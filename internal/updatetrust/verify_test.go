package updatetrust

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/theupdateframework/go-tuf/v2/metadata"
)

func artifact(label string) []byte {
	return bytes.Repeat([]byte(label), 512)
}

func mustOpen(t *testing.T, r *testRepository) *Client {
	t.Helper()
	c, err := r.open(t, t.TempDir(), testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestReleaseInstallsOnlyWhenTheReleaseKeyAndRebuildAgree(t *testing.T) {
	r := newTestRepository(t)
	name := r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("payload"), true)
	r.publish()
	c := mustOpen(t, r)
	got, refused, err := c.Latest(PlatformMacOS, "0.7.0")
	if err != nil || got == nil || len(refused) != 0 {
		t.Fatalf("Latest() = %v, %v, %v", got, refused, err)
	}
	if got.Name != name || got.Contract.Version != "0.7.1" || got.Length != int64(len(artifact("payload"))) {
		t.Fatalf("unexpected release %+v", got)
	}
	dir := t.TempDir()
	path, err := c.Download(*got, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, artifact("payload")) {
		t.Fatalf("downloaded %d bytes, %v", len(data), err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("downloaded file mode %v, %v", info.Mode(), err)
	}
	if _, err := c.Download(*got, dir); err == nil {
		t.Fatal("a second download replaced an existing file")
	}
}

func TestReleaseKeyAloneCannotMakeAnInstallationRunCode(t *testing.T) {
	r := newTestRepository(t)
	name := r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("payload"), false)
	r.publish()
	c := mustOpen(t, r)
	if _, err := c.Verify(name); !errors.Is(err, ErrNoAgreement) {
		t.Fatalf("Verify() error = %v, want no agreement", err)
	}
	got, refused, err := c.Latest(PlatformMacOS, "0.7.0")
	if err != nil || got != nil || len(refused) != 1 || !errors.Is(refused[0].Err, ErrNoAgreement) {
		t.Fatalf("Latest() = %v, %v, %v", got, refused, err)
	}
}

func TestRebuildAloneCannotMakeAnInstallationRunCode(t *testing.T) {
	r := newTestRepository(t)
	c := testContract("0.7.1", PlatformMacOS)
	name := TargetName(c.Version, c.Platform)
	rebuildName := rebuildPrefix + strings.TrimPrefix(name, releasePrefix)
	r.delegated[RoleRebuild].Signed.Targets[rebuildName] = targetFor(t, rebuildName, artifact("payload"), c)
	r.publish()
	client := mustOpen(t, r)
	if _, err := client.Verify(name); !errors.Is(err, ErrNoAgreement) {
		t.Fatalf("Verify() error = %v, want no agreement", err)
	}
	if got, _, err := client.Latest(PlatformMacOS, "0.7.0"); err != nil || got != nil {
		t.Fatalf("Latest() = %v, %v", got, err)
	}
}

func TestDisagreementBetweenReleaseAndRebuildIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(r *testRepository, rebuildName string, c Contract)
	}{
		{"different bytes of the same length", func(r *testRepository, rebuildName string, c Contract) {
			r.delegated[RoleRebuild].Signed.Targets[rebuildName] = targetFor(t, rebuildName, artifact("PAYLOAD"), c)
		}},
		{"different length", func(r *testRepository, rebuildName string, c Contract) {
			r.delegated[RoleRebuild].Signed.Targets[rebuildName] = targetFor(t, rebuildName, artifact("poisoned"), c)
		}},
		{"different contract", func(r *testRepository, rebuildName string, c Contract) {
			c.SchemaTo = "0008"
			r.delegated[RoleRebuild].Signed.Targets[rebuildName] = targetFor(t, rebuildName, artifact("payload"), c)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRepository(t)
			c := testContract("0.7.1", PlatformMacOS)
			name := r.addRelease(c, artifact("payload"), true)
			tc.change(r, rebuildPrefix+strings.TrimPrefix(name, releasePrefix), c)
			r.publish()
			if _, err := mustOpen(t, r).Verify(name); !errors.Is(err, ErrNoAgreement) {
				t.Fatalf("Verify() error = %v, want no agreement", err)
			}
		})
	}
}

func TestNewestAgreedReleaseIsChosenAndHeldOrUnattestedOnesAreRefused(t *testing.T) {
	r := newTestRepository(t)
	r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("one"), true)
	r.addRelease(testContract("0.7.2", PlatformMacOS), artifact("two"), true)
	r.addRelease(testContract("0.7.3", PlatformMacOS), artifact("three"), false)
	r.addRelease(testContract("0.7.9", PlatformLinux), artifact("server"), true)
	r.hold("0.7.2")
	r.publish()
	c := mustOpen(t, r)
	got, refused, err := c.Latest(PlatformMacOS, "0.7.0")
	if err != nil || got == nil || got.Contract.Version != "0.7.1" {
		t.Fatalf("Latest() = %+v, %v", got, err)
	}
	if len(refused) != 2 || !errors.Is(refused[0].Err, ErrNoAgreement) || !errors.Is(refused[1].Err, ErrHeld) {
		t.Fatalf("refusals = %+v", refused)
	}
	if got, _, err := c.Latest(PlatformLinux, "0.7.0"); err != nil || got == nil || got.Contract.Version != "0.7.9" {
		t.Fatalf("Linux Latest() = %+v, %v", got, err)
	}
}

func TestOlderOrEqualGenuineReleasesAreNeverOffered(t *testing.T) {
	r := newTestRepository(t)
	r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("one"), true)
	r.addRelease(testContract("0.7.3", PlatformMacOS), artifact("three"), true)
	r.publish()
	c := mustOpen(t, r)
	for _, installed := range []string{"0.7.3", "0.8.0"} {
		if got, refused, err := c.Latest(PlatformMacOS, installed); err != nil || got != nil || len(refused) != 0 {
			t.Fatalf("Latest(%s) = %v, %v, %v", installed, got, refused, err)
		}
	}
	if _, _, err := c.Latest(PlatformMacOS, "0.7"); err == nil {
		t.Fatal("an invalid installed version was accepted")
	}
}

func TestRolledBackMetadataIsRefused(t *testing.T) {
	r := newTestRepository(t)
	r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("one"), true)
	r.publish()
	old := maps.Clone(r.server.files)
	r.addRelease(testContract("0.7.2", PlatformMacOS), artifact("two"), true)
	r.publish()
	dir := t.TempDir()
	if _, err := r.open(t, dir, testEpoch); err != nil {
		t.Fatal(err)
	}
	r.server.files = old
	if _, err := r.open(t, dir, testEpoch); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Open() after rollback = %v, want unavailable", err)
	}
}

func TestExpiredFreshnessMeansNoUpdateRatherThanAStaleOne(t *testing.T) {
	r := newTestRepository(t)
	r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("one"), true)
	r.publish()
	if _, err := r.open(t, t.TempDir(), testEpoch.AddDate(0, 0, 3)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Open() with expired timestamp = %v, want unavailable", err)
	}
	if _, err := r.open(t, t.TempDir(), testEpoch.AddDate(2, 0, 0)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Open() with a clock far ahead = %v, want unavailable", err)
	}
}

func TestMissingHoldsMetadataDefersInsteadOfInstalling(t *testing.T) {
	r := newTestRepository(t)
	name := r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("one"), true)
	r.publish()
	delete(r.server.files, testMetadataURL+"/"+strconv.FormatInt(r.delegated[RoleHolds].Signed.Version, 10)+".holds.json")
	c := mustOpen(t, r)
	if _, err := c.Verify(name); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Verify() without holds = %v, want unavailable", err)
	}
	// Choosing a release stops too, rather than skipping to an older one.
	if got, _, err := mustOpen(t, r).Latest(PlatformMacOS, "0.7.0"); got != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Latest() without holds = %v, %v, want unavailable", got, err)
	}
}

func TestTamperedTruncatedOrOversizeDownloadsChangeNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		body func([]byte) []byte
		want error
	}{
		{"corrupted", func(b []byte) []byte { c := bytes.Clone(b); c[10] ^= 1; return c }, ErrNoAgreement},
		{"truncated", func(b []byte) []byte { return b[:len(b)-1] }, ErrNoAgreement},
		{"oversize", func(b []byte) []byte { return append(bytes.Clone(b), 'x') }, ErrUnavailable},
		{"captive portal page", func([]byte) []byte { return []byte("<html>Sign in to the network</html>") }, ErrNoAgreement},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRepository(t)
			r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("payload"), true)
			r.publish()
			c := mustOpen(t, r)
			got, _, err := c.Latest(PlatformMacOS, "0.7.0")
			if err != nil || got == nil {
				t.Fatal(got, err)
			}
			for url, data := range r.server.files {
				if strings.HasPrefix(url, testTargetsURL) {
					r.server.files[url] = tc.body(data)
				}
			}
			dir := t.TempDir()
			if _, err := c.Download(*got, dir); !errors.Is(err, tc.want) {
				t.Fatalf("Download() error = %v, want %v", err, tc.want)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("download left %d files", len(entries))
			}
		})
	}
}

func TestUnexpectedDelegationLayoutsAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(r *testRepository)
	}{
		{"one key may sign both agreements", func(r *testRepository) {
			d := r.targets.Signed.Delegations
			for i := range d.Roles {
				if d.Roles[i].Name == RoleRebuild {
					d.Roles[i].KeyIDs = append(d.Roles[i].KeyIDs, r.releaseKeys[0].id)
				}
			}
		}},
		{"top-level targets lists an artifact", func(r *testRepository) {
			r.targets.Signed.Targets["release/vult-0.7.1-macos-arm64.tar"] = targetFor(r.t, "release/vult-0.7.1-macos-arm64.tar", artifact("x"), testContract("0.7.1", PlatformMacOS))
		}},
		{"release key holds the updater role", func(r *testRepository) {
			d := r.targets.Signed.Delegations
			for i := range d.Roles {
				if d.Roles[i].Name == RoleUpdater {
					d.Roles[i].KeyIDs, d.Roles[i].Threshold = []string{r.releaseKeys[0].id}, 1
				}
			}
			r.delegatedSigners[RoleUpdater] = r.releaseKeys[:1]
		}},
		{"release keys hold the updater role at the root threshold", func(r *testRepository) {
			d := r.targets.Signed.Delegations
			for i := range d.Roles {
				if d.Roles[i].Name == RoleUpdater {
					d.Roles[i].KeyIDs, d.Roles[i].Threshold = []string{r.releaseKeys[0].id, r.releaseKeys[1].id}, 2
				}
			}
			r.delegatedSigners[RoleUpdater] = r.releaseKeys
		}},
		{"a delegation that isn't terminating", func(r *testRepository) {
			r.targets.Signed.Delegations.Roles[0].Terminating = false
		}},
		{"a delegation for wider paths", func(r *testRepository) {
			r.targets.Signed.Delegations.Roles[0].Paths = []string{"*"}
		}},
		{"an extra delegated role", func(r *testRepository) {
			d := r.targets.Signed.Delegations
			d.Roles = append(d.Roles, metadata.DelegatedRole{Name: "extra", KeyIDs: []string{r.releaseKeys[0].id}, Threshold: 1, Terminating: true, Paths: []string{"extra/*"}})
			r.delegated["extra"] = metadata.Targets(testEpoch.AddDate(0, 4, 0))
			r.delegatedSigners["extra"] = r.releaseKeys[:1]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRepository(t)
			tc.change(r)
			r.publish()
			if _, err := r.open(t, t.TempDir(), testEpoch); !errors.Is(err, ErrStructure) {
				t.Fatalf("Open() = %v, want structure error", err)
			}
		})
	}
}

func TestInstallationsFollowRootRotationAndKeepTheNewestRoot(t *testing.T) {
	r := newTestRepository(t)
	dir := t.TempDir()
	if _, err := r.open(t, dir, testEpoch); err != nil {
		t.Fatal(err)
	}
	// Two rotations while the installation is offline: each replaces one
	// root key and is signed by the previous root's threshold.
	// The second also replaces the timestamp key, so metadata signed after it
	// verifies only against the newest root.
	for i := range 2 {
		retired := r.rootKeys[0]
		fresh := newTestKey(t)
		r.must(r.root.Signed.RevokeKey(retired.id, metadata.ROOT))
		r.must(r.root.Signed.AddKey(fresh.key, metadata.ROOT))
		if i == 1 {
			r.must(r.root.Signed.RevokeKey(r.tsKey.id, metadata.TIMESTAMP))
			r.tsKey = newTestKey(t)
			r.must(r.root.Signed.AddKey(r.tsKey.key, metadata.TIMESTAMP))
		}
		r.rootKeys = append(r.rootKeys[1:], fresh)
		r.root.Signed.Version++
		r.publishRoot(retired)
		r.targets.Signed.Delegations = r.standardDelegations()
		r.delegatedSigners[RoleUpdater] = r.rootKeys[:2]
	}
	r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("one"), true)
	r.publish()
	c, err := r.open(t, dir, testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if v := c.up.GetTrustedMetadataSet().Root.Signed.Version; v != 3 {
		t.Fatalf("trusted root version %d, want 3", v)
	}
	if got, _, err := c.Latest(PlatformMacOS, "0.7.0"); err != nil || got == nil {
		t.Fatalf("Latest() after rotation = %v, %v", got, err)
	}
	cached, err := os.ReadFile(filepath.Join(dir, "root.json"))
	if err != nil || bytes.Equal(cached, r.initialRoot) {
		t.Fatalf("the newest root was not kept: %v", err)
	}
	// Starting again from the shipped root still begins at the cached one,
	// even when the intermediate roots are no longer served.
	delete(r.server.files, testMetadataURL+"/2.root.json")
	delete(r.server.files, testMetadataURL+"/3.root.json")
	r.publish()
	if _, err := r.open(t, dir, testEpoch); err != nil {
		t.Fatal(err)
	}
}

func TestRootSignedTwiceByOneKeyIsNotEnough(t *testing.T) {
	r := newTestRepository(t)
	dir := t.TempDir()
	if _, err := r.open(t, dir, testEpoch); err != nil {
		t.Fatal(err)
	}
	attacker := newTestKey(t)
	r.must(r.root.Signed.AddKey(attacker.key, metadata.ROOT))
	r.root.Signed.Roles[metadata.ROOT].Threshold = 1
	r.root.Signed.Version++
	r.root.ClearSignatures()
	r.rootKeys[0].sign(t, r.root.Sign)
	// The same signature again, as if a second key had signed.
	r.root.Signatures = append(r.root.Signatures, r.root.Signatures[0])
	attacker.sign(t, r.root.Sign)
	data, err := r.root.ToBytes(false)
	r.must(err)
	r.server.put(testMetadataURL+"/2.root.json", data)
	if _, err := r.open(t, dir, testEpoch); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Open() with a duplicated signature = %v, want unavailable", err)
	}
}

func TestReleaseNamesOutsideTheReleaseNamespaceAreRefused(t *testing.T) {
	r := newTestRepository(t)
	r.publish()
	c := mustOpen(t, r)
	for _, name := range []string{"updater/vult-update-helper-1.0.1-macos-arm64", "rebuild/vult-0.7.1-macos-arm64.tar", "release/../vult-0.7.1-macos-arm64.tar", "release/vult-0.7.1-macos-arm64.pkg"} {
		if _, err := c.Verify(name); !errors.Is(err, ErrContract) {
			t.Fatalf("Verify(%q) = %v, want contract error", name, err)
		}
	}
}

func TestContractNameMismatchIsRefused(t *testing.T) {
	r := newTestRepository(t)
	c := testContract("0.7.2", PlatformMacOS)
	name := TargetName("0.7.1", PlatformMacOS)
	rebuildName := rebuildPrefix + strings.TrimPrefix(name, releasePrefix)
	r.delegated[RoleReleases].Signed.Targets[name] = targetFor(t, name, artifact("x"), c)
	r.delegated[RoleRebuild].Signed.Targets[rebuildName] = targetFor(t, rebuildName, artifact("x"), c)
	r.publish()
	if _, err := mustOpen(t, r).Verify(name); !errors.Is(err, ErrContract) {
		t.Fatalf("Verify() = %v, want contract error", err)
	}
}

func TestArtifactsWithoutSHA256AreRefused(t *testing.T) {
	r := newTestRepository(t)
	c := testContract("0.7.1", PlatformMacOS)
	name := TargetName(c.Version, c.Platform)
	for _, path := range []string{name, rebuildPrefix + strings.TrimPrefix(name, releasePrefix)} {
		tf, err := metadata.TargetFile().FromBytes(path, artifact("payload"), "sha512")
		r.must(err)
		tf.Custom = customJSON(t, c)
		if strings.HasPrefix(path, releasePrefix) {
			r.delegated[RoleReleases].Signed.Targets[path] = tf
		} else {
			r.delegated[RoleRebuild].Signed.Targets[path] = tf
		}
	}
	r.publish()
	if _, err := mustOpen(t, r).Verify(name); !errors.Is(err, ErrNoAgreement) {
		t.Fatalf("Verify() = %v, want refusal", err)
	}
}
