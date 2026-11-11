package updaterepo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"

	"github.com/vult-ai/vult/internal/updatetrust"
)

// How long each role's signatures stay valid.
var validity = map[string]time.Duration{
	metadata.ROOT:            365 * 24 * time.Hour,
	metadata.TARGETS:         365 * 24 * time.Hour,
	updatetrust.RoleReleases: 120 * 24 * time.Hour,
	updatetrust.RoleRebuild:  120 * 24 * time.Hour,
	updatetrust.RoleHolds:    120 * 24 * time.Hour,
	updatetrust.RoleUpdater:  365 * 24 * time.Hour,
	metadata.SNAPSHOT:        14 * 24 * time.Hour,
	metadata.TIMESTAMP:       2 * 24 * time.Hour,
}

var delegatedRoles = []string{updatetrust.RoleReleases, updatetrust.RoleRebuild, updatetrust.RoleHolds, updatetrust.RoleUpdater}

// Repo is a repository working directory:
//
//	staged/<role>.json        metadata being prepared and signed
//	published/metadata/       what installations read (consistent snapshots)
//	published/targets/        release artifacts, named by hash
//
// published/ is uploaded as is to the metadata host and mirrors.
type Repo struct{ Dir string }

func (r Repo) staged(role string) string { return filepath.Join(r.Dir, "staged", role+".json") }
func (r Repo) metadataDir() string       { return filepath.Join(r.Dir, "published", "metadata") }
func (r Repo) targetsDir() string        { return filepath.Join(r.Dir, "published", "targets") }

// Keys names the public keys a new repository starts with.
type Keys struct {
	Root, Targets []*metadata.Key
	// Release lists the keys that may sign releases and holds, any one of
	// them enough: for example a Touch ID key for everyday use and a key
	// file as its backup.
	Release                      []*metadata.Key
	Rebuild, Snapshot, Timestamp *metadata.Key
}

// Init creates a new repository with unsigned first metadata. Root and
// top-level targets need two signatures each; the updater role is held by
// the root keys.
func Init(dir string, k Keys, now time.Time) (Repo, error) {
	if len(k.Root) < 2 || len(k.Targets) < 2 || len(k.Release) == 0 || k.Rebuild == nil || k.Snapshot == nil || k.Timestamp == nil {
		return Repo{}, errors.New("at least two root keys, two targets keys, a release key, and rebuild, snapshot and timestamp keys are required")
	}
	seen := map[string]string{}
	add := func(role string, keys ...*metadata.Key) error {
		for _, key := range keys {
			if prev, ok := seen[key.Value.PublicKey]; ok && prev != role {
				return fmt.Errorf("one key is listed for both %s and %s; every role needs its own keys", prev, role)
			}
			seen[key.Value.PublicKey] = role
		}
		return nil
	}
	for _, step := range []struct {
		role string
		keys []*metadata.Key
	}{{"root", k.Root}, {"targets", k.Targets}, {"releases", k.Release}, {"rebuild", []*metadata.Key{k.Rebuild}}, {"snapshot", []*metadata.Key{k.Snapshot}}, {"timestamp", []*metadata.Key{k.Timestamp}}} {
		if err := add(step.role, step.keys...); err != nil {
			return Repo{}, err
		}
	}
	if len(seen) != len(k.Root)+len(k.Targets)+len(k.Release)+3 {
		return Repo{}, errors.New("a key is listed twice")
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return Repo{}, fmt.Errorf("%s is not empty", dir)
	}
	r := Repo{Dir: dir}
	for _, d := range []string{filepath.Join(dir, "staged"), r.metadataDir(), filepath.Join(r.targetsDir(), "release")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return Repo{}, err
		}
	}
	root := metadata.Root(now.Add(validity[metadata.ROOT]).UTC())
	for _, key := range k.Root {
		if err := root.Signed.AddKey(key, metadata.ROOT); err != nil {
			return Repo{}, err
		}
	}
	for _, key := range k.Targets {
		if err := root.Signed.AddKey(key, metadata.TARGETS); err != nil {
			return Repo{}, err
		}
	}
	if err := root.Signed.AddKey(k.Snapshot, metadata.SNAPSHOT); err != nil {
		return Repo{}, err
	}
	if err := root.Signed.AddKey(k.Timestamp, metadata.TIMESTAMP); err != nil {
		return Repo{}, err
	}
	root.Signed.Roles[metadata.ROOT].Threshold = 2
	root.Signed.Roles[metadata.TARGETS].Threshold = 2

	targets := metadata.Targets(now.Add(validity[metadata.TARGETS]).UTC())
	d := &metadata.Delegations{Keys: map[string]*metadata.Key{}}
	paths := updatetrust.DelegationPaths()
	delegate := func(name string, threshold int, keys ...*metadata.Key) error {
		role := metadata.DelegatedRole{Name: name, Threshold: threshold, Terminating: true, Paths: []string{paths[name]}}
		for _, key := range keys {
			id, err := key.ID()
			if err != nil {
				return err
			}
			d.Keys[id] = key
			role.KeyIDs = append(role.KeyIDs, id)
		}
		d.Roles = append(d.Roles, role)
		return nil
	}
	for _, err := range []error{
		delegate(updatetrust.RoleReleases, 1, k.Release...),
		delegate(updatetrust.RoleRebuild, 1, k.Rebuild),
		delegate(updatetrust.RoleHolds, 1, k.Release...),
		delegate(updatetrust.RoleUpdater, 2, k.Root...),
	} {
		if err != nil {
			return Repo{}, err
		}
	}
	targets.Signed.Delegations = d
	if err := save(r.staged(metadata.ROOT), root); err != nil {
		return Repo{}, err
	}
	if err := save(r.staged(metadata.TARGETS), targets); err != nil {
		return Repo{}, err
	}
	for _, name := range delegatedRoles {
		if err := save(r.staged(name), metadata.Targets(now.Add(validity[name]).UTC())); err != nil {
			return Repo{}, err
		}
	}
	return r, nil
}

type roles interface {
	metadata.RootType | metadata.TargetsType | metadata.SnapshotType | metadata.TimestampType
}

func load[T roles](file string) (*metadata.Metadata[T], error) {
	md, err := (&metadata.Metadata[T]{}).FromFile(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(file), err)
	}
	return md, nil
}

func save[T roles](file string, md *metadata.Metadata[T]) error {
	data, err := md.ToBytes(true)
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

var versioned = regexp.MustCompile(`^([0-9]+)\.([a-z]+)\.json$`)

// published returns the highest published version of role, or 0.
func (r Repo) published(role string) int64 {
	if role == metadata.TIMESTAMP {
		ts, err := load[metadata.TimestampType](filepath.Join(r.metadataDir(), "timestamp.json"))
		if err != nil {
			return 0
		}
		return ts.Signed.Version
	}
	entries, _ := os.ReadDir(r.metadataDir())
	var highest int64
	for _, e := range entries {
		m := versioned.FindStringSubmatch(e.Name())
		if m == nil || m[2] != role {
			continue
		}
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil && v > highest {
			highest = v
		}
	}
	return highest
}

// change prepares a targets-type role for an edit: a new version past what
// is published, a fresh expiry, and no signatures.
func (r Repo) change(role string, md *metadata.Metadata[metadata.TargetsType], now time.Time) {
	if p := r.published(role); md.Signed.Version <= p {
		md.Signed.Version = p + 1
	}
	md.Signed.Expires = now.Add(validity[role]).UTC()
	md.ClearSignatures()
}

func (r Repo) loadTargets(role string) (*metadata.Metadata[metadata.TargetsType], error) {
	return load[metadata.TargetsType](r.staged(role))
}

func readArtifact(file string) ([]byte, error) {
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > updatetrust.MaxArtifactLength {
		return nil, fmt.Errorf("%s is not a usable artifact", file)
	}
	return os.ReadFile(file)
}

func targetFor(name string, data []byte, c updatetrust.Contract) (*metadata.TargetFiles, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	tf, err := metadata.TargetFile().FromBytes(name, data, "sha256")
	if err != nil {
		return nil, err
	}
	raw, err := updatetrust.CustomMetadata(c)
	if err != nil {
		return nil, err
	}
	msg := json.RawMessage(raw)
	tf.Custom = &msg
	return tf, nil
}

func contractOf(tf *metadata.TargetFiles) (updatetrust.Contract, error) {
	var w struct {
		Vult updatetrust.Contract `json:"vult"`
	}
	if tf.Custom == nil || json.Unmarshal(*tf.Custom, &w) != nil {
		return updatetrust.Contract{}, errors.New("target has no release contract")
	}
	return w.Vult, nil
}

// AddRelease lists an artifact under the release key and stores the artifact
// for publishing. Versions only move forward per platform, and a listed
// release is never replaced.
func (r Repo) AddRelease(artifact string, c updatetrust.Contract, now time.Time) (string, error) {
	data, err := readArtifact(artifact)
	if err != nil {
		return "", err
	}
	name := updatetrust.TargetName(c.Version, c.Platform)
	tf, err := targetFor(name, data, c)
	if err != nil {
		return "", err
	}
	md, err := r.loadTargets(updatetrust.RoleReleases)
	if err != nil {
		return "", err
	}
	for existing, t := range md.Signed.Targets {
		if existing == name {
			return "", fmt.Errorf("%s is already listed; a release is never replaced, publish a newer version", name)
		}
		if ec, err := contractOf(t); err == nil && ec.Platform == c.Platform && !versionAfter(ec.Version, c.Version) {
			return "", fmt.Errorf("%s is not newer than %s", c.Version, ec.Version)
		}
	}
	sum := hex.EncodeToString(tf.Hashes["sha256"])
	dest := filepath.Join(r.targetsDir(), "release", sum+"."+path.Base(name))
	if existing, err := os.ReadFile(dest); err == nil {
		if !bytes.Equal(existing, data) {
			return "", fmt.Errorf("%s exists with other content", dest)
		}
	} else if err := writeNew(dest, data, 0o644); err != nil {
		return "", err
	}
	md.Signed.Targets[name] = tf
	r.change(updatetrust.RoleReleases, md, now)
	return name, save(r.staged(updatetrust.RoleReleases), md)
}

// AddRebuild records the independent rebuild's agreement: the same artifact
// and contract under rebuild/. The rebuild job runs this on its own output.
func (r Repo) AddRebuild(artifact string, c updatetrust.Contract, now time.Time) (string, error) {
	data, err := readArtifact(artifact)
	if err != nil {
		return "", err
	}
	name := updatetrust.RebuildTargetName(updatetrust.TargetName(c.Version, c.Platform))
	tf, err := targetFor(name, data, c)
	if err != nil {
		return "", err
	}
	md, err := r.loadTargets(updatetrust.RoleRebuild)
	if err != nil {
		return "", err
	}
	if _, ok := md.Signed.Targets[name]; ok {
		return "", fmt.Errorf("%s is already attested", name)
	}
	md.Signed.Targets[name] = tf
	r.change(updatetrust.RoleRebuild, md, now)
	return name, save(r.staged(updatetrust.RoleRebuild), md)
}

// Hold withdraws a listed release version.
func (r Repo) Hold(version string, now time.Time) error {
	releases, err := r.loadTargets(updatetrust.RoleReleases)
	if err != nil {
		return err
	}
	listed := false
	for _, t := range releases.Signed.Targets {
		if c, err := contractOf(t); err == nil && c.Version == version {
			listed = true
		}
	}
	if !listed {
		return fmt.Errorf("no listed release has version %s", version)
	}
	md, err := r.loadTargets(updatetrust.RoleHolds)
	if err != nil {
		return err
	}
	name := updatetrust.HoldTargetName(version)
	tf, err := metadata.TargetFile().FromBytes(name, []byte{}, "sha256")
	if err != nil {
		return err
	}
	md.Signed.Targets[name] = tf
	r.change(updatetrust.RoleHolds, md, now)
	return save(r.staged(updatetrust.RoleHolds), md)
}

func versionAfter(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	if len(pa) != 3 || len(pb) != 3 {
		return false
	}
	for i := range pa {
		x, e1 := strconv.Atoi(pa[i])
		y, e2 := strconv.Atoi(pb[i])
		if e1 != nil || e2 != nil {
			return false
		}
		if x != y {
			return y > x
		}
	}
	return false
}

// authorized returns the key IDs and threshold allowed to sign role.
func (r Repo) authorized(role string) ([]string, int, error) {
	switch role {
	case metadata.ROOT, metadata.TARGETS, metadata.SNAPSHOT, metadata.TIMESTAMP:
		root, err := load[metadata.RootType](r.staged(metadata.ROOT))
		if err != nil {
			return nil, 0, err
		}
		rr := root.Signed.Roles[role]
		return rr.KeyIDs, rr.Threshold, nil
	}
	targets, err := r.loadTargets(metadata.TARGETS)
	if err != nil {
		return nil, 0, err
	}
	if targets.Signed.Delegations != nil {
		for _, d := range targets.Signed.Delegations.Roles {
			if d.Name == role {
				return d.KeyIDs, d.Threshold, nil
			}
		}
	}
	return nil, 0, fmt.Errorf("unknown role %s", role)
}

// checkAgreement is the rule that the owner never signs a release the
// rebuild hasn't already matched: every release listed must appear, identical,
// in a rebuild role that is signed to its threshold.
func (r Repo) checkAgreement() error {
	releases, err := r.loadTargets(updatetrust.RoleReleases)
	if err != nil {
		return err
	}
	rebuild, err := r.loadTargets(updatetrust.RoleRebuild)
	if err != nil {
		return err
	}
	targets, err := r.loadTargets(metadata.TARGETS)
	if err != nil {
		return err
	}
	if len(releases.Signed.Targets) > 0 {
		if err := targets.VerifyDelegate(updatetrust.RoleRebuild, rebuild); err != nil {
			return errors.New("the rebuild's agreement is not signed yet; the rebuild must agree before the release is signed")
		}
	}
	for name, owner := range releases.Signed.Targets {
		rb, ok := rebuild.Signed.Targets[updatetrust.RebuildTargetName(name)]
		if !ok {
			return fmt.Errorf("the rebuild has not attested %s; it must agree before the release is signed", name)
		}
		oc, err1 := contractOf(owner)
		rc, err2 := contractOf(rb)
		if err1 != nil || err2 != nil || owner.Length != rb.Length || !owner.Hashes.Equal(rb.Hashes) || !reflect.DeepEqual(oc, rc) {
			return fmt.Errorf("the rebuild disagrees about %s; do not sign", name)
		}
	}
	return nil
}

// Sign adds one signature to a staged role. confirm sees exactly what will be
// signed and must return true. The release key's role can only be signed once
// the rebuild agrees.
func (r Repo) Sign(role string, s *Signer, confirm func(summary string) bool) error {
	ids, _, err := r.authorized(role)
	if err != nil {
		return err
	}
	allowed := false
	for _, id := range ids {
		allowed = allowed || id == s.ID
	}
	if !allowed {
		return fmt.Errorf("key %s is not one of the keys for %s", short(s.ID), role)
	}
	if role == updatetrust.RoleReleases {
		if err := r.checkAgreement(); err != nil {
			return err
		}
	}
	summary, err := r.Summary(role)
	if err != nil {
		return err
	}
	if !confirm(summary) {
		return errors.New("not signed")
	}
	signer, err := s.signer()
	if err != nil {
		return err
	}
	switch role {
	case metadata.ROOT:
		return signStaged[metadata.RootType](r.staged(role), s.ID, signer)
	case metadata.SNAPSHOT:
		return signStaged[metadata.SnapshotType](r.staged(role), s.ID, signer)
	case metadata.TIMESTAMP:
		return signStaged[metadata.TimestampType](r.staged(role), s.ID, signer)
	default:
		return signStaged[metadata.TargetsType](r.staged(role), s.ID, signer)
	}
}

func signStaged[T roles](file, id string, signer signature.Signer) error {
	md, err := load[T](file)
	if err != nil {
		return err
	}
	kept := md.Signatures[:0]
	for _, sig := range md.Signatures {
		if sig.KeyID != id {
			kept = append(kept, sig)
		}
	}
	md.Signatures = kept
	if _, err := md.Sign(signer); err != nil {
		return err
	}
	return save(file, md)
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
