package updatetrust

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// A synthetic update repository with throwaway keys, served from memory. It
// mirrors the production layout: a root and top-level targets held by root
// keys, delegated release, rebuild, hold and updater roles, and online
// snapshot and timestamp keys.

const (
	testMetadataURL = "https://updates.invalid/metadata"
	testTargetsURL  = "https://updates.invalid/targets"
)

var testEpoch = time.Date(2027, 1, 15, 12, 0, 0, 0, time.UTC)

type testKey struct {
	private ed25519.PrivateKey
	// der, when set, signs instead of private: an ECDSA P-256 key that
	// returns DER signatures, as a Secure Enclave key does.
	der signature.Signer
	key *metadata.Key
	id  string
}

func newTestKey(t *testing.T) testKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := metadata.KeyFromPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	id, err := key.ID()
	if err != nil {
		t.Fatal(err)
	}
	return testKey{private: private, key: key, id: id}
}

// derP256Signer signs the SHA-256 of a message with ECDSA P-256 and returns
// the DER signature, the form a Secure Enclave key produces.
type derP256Signer struct{ private *ecdsa.PrivateKey }

func (s derP256Signer) PublicKey(...signature.PublicKeyOption) (crypto.PublicKey, error) {
	return s.private.Public(), nil
}

func (s derP256Signer) SignMessage(message io.Reader, _ ...signature.SignOption) ([]byte, error) {
	data, err := io.ReadAll(message)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	return ecdsa.SignASN1(rand.Reader, s.private, digest[:])
}

func newP256TestKey(t *testing.T) testKey {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := metadata.KeyFromPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	if key.Type != metadata.KeyTypeECDSA_SHA2_P256 || key.Scheme != metadata.KeySchemeECDSA_SHA2_P256 {
		t.Fatalf("P-256 key described as %s/%s", key.Type, key.Scheme)
	}
	id, err := key.ID()
	if err != nil {
		t.Fatal(err)
	}
	return testKey{der: derP256Signer{private}, key: key, id: id}
}

func (k testKey) sign(t *testing.T, sign func(signature.Signer) (*metadata.Signature, error)) {
	t.Helper()
	if k.der != nil {
		if _, err := sign(k.der); err != nil {
			t.Fatal(err)
		}
		return
	}
	signer, err := signature.LoadSigner(k.private, crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sign(signer); err != nil {
		t.Fatal(err)
	}
}

type testRepository struct {
	t      *testing.T
	server *memoryServer

	rootKeys, targetsKeys          []testKey
	releaseKeys                    []testKey
	rebuildKey, snapshotKey, tsKey testKey

	root      *metadata.Metadata[metadata.RootType]
	targets   *metadata.Metadata[metadata.TargetsType]
	delegated map[string]*metadata.Metadata[metadata.TargetsType]
	snapshot  *metadata.Metadata[metadata.SnapshotType]
	timestamp *metadata.Metadata[metadata.TimestampType]

	// Signers for each delegated role; tests may replace them.
	delegatedSigners map[string][]testKey
	initialRoot      []byte
}

func newTestRepository(t *testing.T) *testRepository {
	t.Helper()
	r := &testRepository{t: t, server: newMemoryServer(), delegated: map[string]*metadata.Metadata[metadata.TargetsType]{}}
	for range 3 {
		r.rootKeys = append(r.rootKeys, newTestKey(t))
		r.targetsKeys = append(r.targetsKeys, newTestKey(t))
	}
	r.releaseKeys = []testKey{newTestKey(t), newTestKey(t)}
	r.rebuildKey, r.snapshotKey, r.tsKey = newTestKey(t), newTestKey(t), newTestKey(t)

	r.root = metadata.Root(testEpoch.AddDate(1, 0, 0))
	for _, k := range r.rootKeys {
		r.must(r.root.Signed.AddKey(k.key, metadata.ROOT))
	}
	for _, k := range r.targetsKeys {
		r.must(r.root.Signed.AddKey(k.key, metadata.TARGETS))
	}
	r.must(r.root.Signed.AddKey(r.snapshotKey.key, metadata.SNAPSHOT))
	r.must(r.root.Signed.AddKey(r.tsKey.key, metadata.TIMESTAMP))
	r.root.Signed.Roles[metadata.ROOT].Threshold = 2
	r.root.Signed.Roles[metadata.TARGETS].Threshold = 2

	r.delegatedSigners = map[string][]testKey{
		RoleReleases: r.releaseKeys[:1],
		RoleRebuild:  {r.rebuildKey},
		RoleHolds:    r.releaseKeys[:1],
		RoleUpdater:  r.rootKeys[:2],
	}
	r.targets = metadata.Targets(testEpoch.AddDate(1, 0, 0))
	r.targets.Signed.Delegations = r.standardDelegations()
	for name := range layout {
		r.delegated[name] = metadata.Targets(testEpoch.AddDate(0, 4, 0))
	}
	r.snapshot = metadata.Snapshot(testEpoch.AddDate(0, 0, 14))
	r.timestamp = metadata.Timestamp(testEpoch.AddDate(0, 0, 2))
	r.publishRoot()
	r.initialRoot = r.server.get(testMetadataURL + "/1.root.json")
	r.publish()
	return r
}

func (r *testRepository) must(err error) {
	r.t.Helper()
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepository) standardDelegations() *metadata.Delegations {
	d := &metadata.Delegations{Keys: map[string]*metadata.Key{}}
	add := func(name string, threshold int, keys ...testKey) {
		role := metadata.DelegatedRole{Name: name, Threshold: threshold, Terminating: true, Paths: []string{layout[name]}}
		for _, k := range keys {
			d.Keys[k.id] = k.key
			role.KeyIDs = append(role.KeyIDs, k.id)
		}
		d.Roles = append(d.Roles, role)
	}
	add(RoleReleases, 1, r.releaseKeys...)
	add(RoleRebuild, 1, r.rebuildKey)
	add(RoleHolds, 1, r.releaseKeys...)
	add(RoleUpdater, 2, r.rootKeys...)
	return d
}

// publishRoot signs the current root with the first two root keys and serves it.
func (r *testRepository) publishRoot(extra ...testKey) {
	r.root.ClearSignatures()
	for _, k := range append(slicesClone(r.rootKeys[:2]), extra...) {
		k.sign(r.t, r.root.Sign)
	}
	data, err := r.root.ToBytes(false)
	r.must(err)
	r.server.put(fmt.Sprintf("%s/%d.root.json", testMetadataURL, r.root.Signed.Version), data)
}

func slicesClone[T any](s []T) []T { return append([]T(nil), s...) }

// publish bumps and re-signs targets, every delegated role, snapshot and
// timestamp, as one consistent repository state.
func (r *testRepository) publish() {
	r.t.Helper()
	r.targets.Signed.Version++
	r.targets.ClearSignatures()
	for _, k := range r.targetsKeys[:2] {
		k.sign(r.t, r.targets.Sign)
	}
	r.serveTargets(metadata.TARGETS, r.targets)
	for name, md := range r.delegated {
		md.Signed.Version++
		md.ClearSignatures()
		for _, k := range r.delegatedSigners[name] {
			k.sign(r.t, md.Sign)
		}
		r.serveTargets(name, md)
	}
	r.snapshot.Signed.Version++
	r.snapshot.Signed.Meta = map[string]*metadata.MetaFiles{metadata.TARGETS + ".json": metadata.MetaFile(r.targets.Signed.Version)}
	for name, md := range r.delegated {
		r.snapshot.Signed.Meta[name+".json"] = metadata.MetaFile(md.Signed.Version)
	}
	r.snapshot.ClearSignatures()
	r.snapshotKey.sign(r.t, r.snapshot.Sign)
	data, err := r.snapshot.ToBytes(false)
	r.must(err)
	r.server.put(fmt.Sprintf("%s/%d.snapshot.json", testMetadataURL, r.snapshot.Signed.Version), data)
	r.timestamp.Signed.Version++
	r.timestamp.Signed.Meta = map[string]*metadata.MetaFiles{metadata.SNAPSHOT + ".json": metadata.MetaFile(r.snapshot.Signed.Version)}
	r.timestamp.ClearSignatures()
	r.tsKey.sign(r.t, r.timestamp.Sign)
	data, err = r.timestamp.ToBytes(false)
	r.must(err)
	r.server.put(testMetadataURL+"/timestamp.json", data)
}

func (r *testRepository) serveTargets(name string, md *metadata.Metadata[metadata.TargetsType]) {
	data, err := md.ToBytes(false)
	r.must(err)
	r.server.put(fmt.Sprintf("%s/%d.%s.json", testMetadataURL, md.Signed.Version, name), data)
}

func testContract(version, platform string) Contract {
	c := Contract{
		Version:        version,
		Platform:       platform,
		SchemaFrom:     []string{"0005", "0006"},
		SchemaTo:       "0007",
		Migrations:     map[string]string{"0007_example_change.sql": strings.Repeat("a", 64)},
		MinimumUpdater: "1.0.0",
		PostgreSQL:     PostgreSQL{Major: 18, Version: "18.6", SourceSHA256: strings.Repeat("b", 64)},
		Source:         Source{Repository: "example/vult", Commit: strings.Repeat("c", 40), Tag: "v" + version},
		Build:          Build{Method: BuildSignatureRemoved, InputsSHA256: strings.Repeat("d", 64)},
	}
	if platform == PlatformMacOS {
		c.MinimumOS = "26.0"
	} else {
		c.Build.Method = BuildIdentical
		c.ServerImage = "sha256:" + strings.Repeat("e", 64)
		c.MaintenanceImage = "sha256:" + strings.Repeat("f", 64)
	}
	return c
}

func customJSON(t *testing.T, c any) *json.RawMessage {
	t.Helper()
	data, err := json.Marshal(map[string]any{"vult": c})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(data)
	return &raw
}

func targetFor(t *testing.T, path string, artifact []byte, contract any) *metadata.TargetFiles {
	t.Helper()
	tf, err := metadata.TargetFile().FromBytes(path, artifact, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	tf.Custom = customJSON(t, contract)
	return tf
}

// addRelease lists an artifact under the release key and, when rebuilt is
// set, the same artifact under the rebuild key; it serves the artifact.
func (r *testRepository) addRelease(c Contract, artifact []byte, rebuilt bool) string {
	name := TargetName(c.Version, c.Platform)
	r.delegated[RoleReleases].Signed.Targets[name] = targetFor(r.t, name, artifact, c)
	if rebuilt {
		rebuildName := rebuildPrefix + strings.TrimPrefix(name, releasePrefix)
		r.delegated[RoleRebuild].Signed.Targets[rebuildName] = targetFor(r.t, rebuildName, artifact, c)
	}
	sum := sha256.Sum256(artifact)
	r.server.put(fmt.Sprintf("%s/release/%s.%s", testTargetsURL, hex.EncodeToString(sum[:]), strings.TrimPrefix(name, releasePrefix)), artifact)
	return name
}

func (r *testRepository) hold(version string) {
	name := holdPrefix + version
	tf, err := metadata.TargetFile().FromBytes(name, []byte{}, "sha256")
	r.must(err)
	r.delegated[RoleHolds].Signed.Targets[name] = tf
}

func (r *testRepository) open(t *testing.T, dir string, now time.Time) (*Client, error) {
	t.Helper()
	return Open(Config{InitialRoot: r.initialRoot, MetadataDir: dir, MetadataURL: testMetadataURL, TargetsURL: testTargetsURL, Fetcher: r.server, Now: func() time.Time { return now }})
}

// memoryServer implements the TUF fetcher over an in-memory map.
type memoryServer struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newMemoryServer() *memoryServer { return &memoryServer{files: map[string][]byte{}} }

func (s *memoryServer) put(url string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[url] = append([]byte(nil), data...)
}

func (s *memoryServer) get(url string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.files[url]...)
}

func (s *memoryServer) DownloadFile(url string, maxLength int64, _ time.Duration) ([]byte, error) {
	s.mu.Lock()
	data, ok := s.files[url]
	s.mu.Unlock()
	if !ok {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: 404, URL: url}
	}
	if int64(len(data)) > maxLength {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: "response exceeds the signed length"}
	}
	return append([]byte(nil), data...), nil
}
