// Package updatetrust decides whether a Vult release may be installed. It reads
// signed update metadata through The Update Framework (go-tuf) and adds the
// rules TUF alone doesn't state:
//
//   - Two agreements. A release target is installable only when the release
//     key lists it under release/ and the independent rebuild lists the same
//     artifact under rebuild/ with identical length, hashes and contract. The
//     two roles never share a key.
//   - A fixed delegation layout. Top-level targets lists nothing itself; it
//     delegates release/, rebuild/, hold/ and updater/ to one role each, and
//     the updater role is held by the root keys.
//   - A strict release contract (contract.go), held releases, and versions
//     that only move forward.
//
// Nothing here reports anything about the installation: it only fetches public
// metadata and artifacts.
package updatetrust

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
)

// Delegated roles and the one path pattern each may sign for.
const (
	RoleReleases = "releases"
	RoleRebuild  = "rebuild"
	RoleHolds    = "holds"
	RoleUpdater  = "updater"

	releasePrefix = "release/"
	rebuildPrefix = "rebuild/"
	holdPrefix    = "hold/"
	updaterPrefix = "updater/"
)

var layout = map[string]string{
	RoleReleases: releasePrefix + "*",
	RoleRebuild:  rebuildPrefix + "*",
	RoleHolds:    holdPrefix + "*",
	RoleUpdater:  updaterPrefix + "*",
}

// MaxArtifactLength bounds any artifact this package will download.
const MaxArtifactLength = 2 << 30

var (
	// ErrStructure: the signed metadata doesn't have Vult's delegation layout.
	ErrStructure = errors.New("update metadata does not have the expected structure")
	// ErrNoAgreement: the release key and the independent rebuild don't list
	// the same artifact.
	ErrNoAgreement = errors.New("the release and its independent rebuild do not agree")
	// ErrContract: the release contract is missing, unknown or invalid.
	ErrContract = errors.New("invalid release contract")
	// ErrHeld: the release was withdrawn by a signed hold.
	ErrHeld = errors.New("the release is held")
	// ErrNotNewer: the release is not later than what is installed.
	ErrNotNewer = errors.New("the release is not newer than the installed version")
	// ErrUnavailable: metadata needed to decide could not be read or verified;
	// the caller defers.
	ErrUnavailable = errors.New("update metadata is unavailable")
)

var releaseName = regexp.MustCompile(`^release/vult-([0-9]+\.[0-9]+\.[0-9]+)-([a-z0-9-]+)\.tar$`)

// Config says where trusted metadata lives and where to fetch more.
type Config struct {
	// InitialRoot is the root shipped with the program. It is used only when
	// MetadataDir holds no root yet; afterwards the newest verified root there
	// is the starting point, so key rotations are never forgotten.
	InitialRoot []byte
	// MetadataDir keeps verified metadata between runs. It is what makes
	// rollback to older metadata detectable, so it must be writable only by
	// the process that verifies.
	MetadataDir string
	MetadataURL string
	TargetsURL  string
	// Fetcher is replaceable for tests; nil uses HTTPS with HTTPClient.
	Fetcher    fetcher.Fetcher
	HTTPClient *http.Client
	// Now overrides the clock for expiry checks; nil means the system clock.
	Now func() time.Time
}

// Client answers questions about one consistent repository state. Make a new
// Client for each check.
type Client struct {
	up         *updater.Updater
	fetcher    fetcher.Fetcher
	targetsURL string
}

// Release is a verified, installable release.
type Release struct {
	Name     string
	Length   int64
	SHA256   string
	Contract Contract
	target   *metadata.TargetFiles
}

// Refusal says why a listed release was not offered.
type Refusal struct {
	Name string
	Err  error
}

// Open prepares a client and verifies the current metadata.
func Open(c Config) (*Client, error) {
	if c.MetadataDir == "" || c.MetadataURL == "" || c.TargetsURL == "" {
		return nil, errors.New("update metadata locations are required")
	}
	root := c.InitialRoot
	if cached, err := os.ReadFile(filepath.Join(c.MetadataDir, "root.json")); err == nil {
		root = cached
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	cfg, err := config.New(c.MetadataURL, root)
	if err != nil {
		return nil, err
	}
	cfg.LocalMetadataDir = c.MetadataDir
	cfg.LocalTargetsDir = c.MetadataDir
	cfg.RemoteTargetsURL = c.TargetsURL
	cfg.MaxDelegations = len(layout) + 1
	cfg.PrefixTargetsWithHash = true
	if c.Fetcher != nil {
		cfg.Fetcher = c.Fetcher
	} else {
		client := c.HTTPClient
		if client == nil {
			client = &http.Client{Timeout: 2 * time.Minute}
		}
		if err := cfg.SetDefaultFetcherHTTPClient(client); err != nil {
			return nil, err
		}
	}
	up, err := updater.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if c.Now != nil {
		up.UnsafeSetRefTime(c.Now())
	}
	if err := up.Refresh(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	cl := &Client{up: up, fetcher: cfg.Fetcher, targetsURL: c.TargetsURL}
	if err := cl.checkStructure(); err != nil {
		return nil, err
	}
	return cl, nil
}

func (c *Client) checkStructure() error {
	trusted := c.up.GetTrustedMetadataSet()
	root := trusted.Root.Signed
	top := trusted.Targets[metadata.TARGETS]
	fail := func(what string) error { return fmt.Errorf("%w: %s", ErrStructure, what) }
	if top == nil {
		return fail("no targets metadata")
	}
	if len(top.Signed.Targets) != 0 {
		return fail("top-level targets lists artifacts itself")
	}
	if root.Roles[metadata.ROOT].Threshold < 2 || root.Roles[metadata.TARGETS].Threshold < 2 {
		return fail("root and targets need at least two signatures")
	}
	d := top.Signed.Delegations
	if d == nil || d.SuccinctRoles != nil || len(d.Roles) != len(layout) {
		return fail("unexpected delegations")
	}
	keys := map[string][]string{}
	for _, role := range d.Roles {
		pattern, ok := layout[role.Name]
		if !ok || keys[role.Name] != nil || !role.Terminating || len(role.PathHashPrefixes) != 0 || !slices.Equal(role.Paths, []string{pattern}) {
			return fail("unexpected delegation " + role.Name)
		}
		if role.Threshold < 1 || len(role.KeyIDs) < role.Threshold {
			return fail("delegation without enough keys: " + role.Name)
		}
		var public []string
		for _, id := range role.KeyIDs {
			key, ok := d.Keys[id]
			if !ok {
				return fail("delegation names an unknown key")
			}
			public = append(public, key.Value.PublicKey)
		}
		keys[role.Name] = public
	}
	for _, k := range keys[RoleReleases] {
		if slices.Contains(keys[RoleRebuild], k) {
			return fail("the release key and the rebuild key must be different")
		}
	}
	// Updates to the updater itself are authorized by the root keys only.
	var rootKeys []string
	for _, id := range root.Roles[metadata.ROOT].KeyIDs {
		if key, ok := root.Keys[id]; ok {
			rootKeys = append(rootKeys, key.Value.PublicKey)
		}
	}
	updaterKeys := slices.Clone(keys[RoleUpdater])
	slices.Sort(rootKeys)
	slices.Sort(updaterKeys)
	var updaterRole metadata.DelegatedRole
	for _, role := range d.Roles {
		if role.Name == RoleUpdater {
			updaterRole = role
		}
	}
	if !slices.Equal(rootKeys, updaterKeys) || updaterRole.Threshold < root.Roles[metadata.ROOT].Threshold {
		return fail("the updater role must be held by the root keys")
	}
	return nil
}

// role loads a delegated role's metadata by looking up a path it is
// responsible for. A missing path is fine; missing metadata is not.
func (c *Client) role(name, probe string) (*metadata.Metadata[metadata.TargetsType], error) {
	_, lookup := c.up.GetTargetInfo(probe)
	md := c.up.GetTrustedMetadataSet().Targets[name]
	if md == nil {
		return nil, fmt.Errorf("%w: %s metadata: %v", ErrUnavailable, name, lookup)
	}
	return md, nil
}

// Held reports whether a signed hold withdraws version. If holds can't be
// read, the answer is an error and the caller defers.
func (c *Client) Held(version string) (bool, error) {
	md, err := c.role(RoleHolds, holdPrefix+version)
	if err != nil {
		return false, err
	}
	_, held := md.Signed.Targets[holdPrefix+version]
	return held, nil
}

// Verify checks one release target: both agreements, the contract, its name,
// and that it is not held.
func (c *Client) Verify(name string) (Release, error) {
	m := releaseName.FindStringSubmatch(name)
	if m == nil {
		return Release{}, fmt.Errorf("%w: not a release name", ErrContract)
	}
	releases, err := c.role(RoleReleases, name)
	if err != nil {
		return Release{}, err
	}
	owner, ok := releases.Signed.Targets[name]
	if !ok {
		return Release{}, fmt.Errorf("%w: %s is not listed by the release key", ErrNoAgreement, name)
	}
	rebuildName := RebuildTargetName(name)
	rebuild, err := c.role(RoleRebuild, rebuildName)
	if err != nil {
		return Release{}, err
	}
	rebuilt, ok := rebuild.Signed.Targets[rebuildName]
	if !ok {
		return Release{}, fmt.Errorf("%w: no rebuild attests %s", ErrNoAgreement, name)
	}
	sum, ok := owner.Hashes["sha256"]
	if !ok || len(sum) != sha256.Size || len(owner.Hashes) != len(rebuilt.Hashes) || !owner.Hashes.Equal(rebuilt.Hashes) || owner.Length != rebuilt.Length {
		return Release{}, fmt.Errorf("%w: different artifacts", ErrNoAgreement)
	}
	if owner.Length <= 0 || owner.Length > MaxArtifactLength {
		return Release{}, fmt.Errorf("%w: artifact length", ErrContract)
	}
	contract, err := parseContract(owner.Custom)
	if err != nil {
		return Release{}, err
	}
	attested, err := parseContract(rebuilt.Custom)
	if err != nil {
		return Release{}, err
	}
	if !reflect.DeepEqual(contract, attested) {
		return Release{}, fmt.Errorf("%w: different contracts", ErrNoAgreement)
	}
	if contract.Version != m[1] || contract.Platform != m[2] {
		return Release{}, fmt.Errorf("%w: name does not match contract", ErrContract)
	}
	held, err := c.Held(contract.Version)
	if err != nil {
		return Release{}, err
	}
	if held {
		return Release{}, fmt.Errorf("%w: %s", ErrHeld, contract.Version)
	}
	return Release{Name: name, Length: owner.Length, SHA256: hex.EncodeToString(sum), Contract: contract, target: owner}, nil
}

// ReleaseNames lists every release target the release key signed, verified
// or not, sorted.
func (c *Client) ReleaseNames() ([]string, error) {
	releases, err := c.role(RoleReleases, releasePrefix+"index")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(releases.Signed.Targets))
	for name := range releases.Signed.Targets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Latest returns the newest verified release for platform that is later than
// installed, or nil when there is none. Listed releases that fail
// verification are returned as refusals for the local history.
func (c *Client) Latest(platform, installed string) (*Release, []Refusal, error) {
	if parseVersion(installed) == nil {
		return nil, nil, errors.New("invalid installed version")
	}
	releases, err := c.role(RoleReleases, TargetName(installed, platform))
	if err != nil {
		return nil, nil, err
	}
	var names []string
	for name := range releases.Signed.Targets {
		if m := releaseName.FindStringSubmatch(name); m != nil && m[2] == platform && newer(installed, m[1]) {
			names = append(names, name)
		}
	}
	// Newest first; the first that verifies wins.
	sort.Slice(names, func(i, j int) bool {
		return newer(releaseName.FindStringSubmatch(names[j])[1], releaseName.FindStringSubmatch(names[i])[1])
	})
	var refused []Refusal
	for _, name := range names {
		r, err := c.Verify(name)
		if errors.Is(err, ErrUnavailable) {
			return nil, refused, err
		}
		if err != nil {
			refused = append(refused, Refusal{Name: name, Err: err})
			continue
		}
		return &r, refused, nil
	}
	return nil, refused, nil
}

// Download fetches a verified release's artifact into dir as a new file and
// returns its path. The file is created exclusively, checked against the
// signed length and hashes before it is kept, and synced.
func (c *Client) Download(r Release, dir string) (string, error) {
	if r.target == nil {
		return "", errors.New("release was not verified")
	}
	base := path.Base(r.Name)
	u, err := url.JoinPath(c.targetsURL, path.Dir(r.Name), r.SHA256+"."+base)
	if err != nil {
		return "", err
	}
	data, err := c.fetcher.DownloadFile(u, r.Length, 0)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := r.target.VerifyLengthHashes(data); err != nil {
		return "", fmt.Errorf("%w: downloaded artifact does not match its signed hashes", ErrNoAgreement)
	}
	dest := filepath.Join(dir, r.SHA256+"."+base)
	f, err := os.OpenFile(dest+".partial", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(dest + ".partial")
		}
	}()
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(dest); err == nil {
		return "", fmt.Errorf("refusing to replace %s", dest)
	}
	if err := os.Rename(dest+".partial", dest); err != nil {
		return "", err
	}
	keep = true
	return dest, syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
