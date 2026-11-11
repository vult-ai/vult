package updatetrust

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/vult-ai/vult/internal/strictjson"
)

// Platforms a release can target.
const (
	PlatformMacOS = "macos-arm64"
	PlatformLinux = "linux-amd64"
	// PlatformUbuntu is the Ubuntu desktop package: the program is the
	// opt/vult/ part of the release archive.
	PlatformUbuntu = "ubuntu-amd64"
)

// Build methods: how the independent rebuild compared its output.
const (
	// BuildIdentical: every byte of the artifact matched.
	BuildIdentical = "identical"
	// BuildSignatureRemoved: Mach-O files matched once their code signatures
	// were removed; every other byte matched.
	BuildSignatureRemoved = "signature-removed"
)

// MaxDelayHours bounds the longest rollout delay a release may ask for.
const MaxDelayHours = 30 * 24

// Contract is everything one release promises about itself. The same contract
// is signed by the release key and by the independent rebuild, inside each
// target's custom metadata as {"vult": {...}}. An installation acts on nothing
// a contract doesn't state, and refuses a contract it doesn't fully understand.
type Contract struct {
	Version          string            `json:"version"`
	Platform         string            `json:"platform"`
	MinimumOS        string            `json:"minimum_os,omitempty"`
	ServerImage      string            `json:"server_image,omitempty"`
	MaintenanceImage string            `json:"maintenance_image,omitempty"`
	SchemaFrom       []string          `json:"schema_from"`
	SchemaTo         string            `json:"schema_to"`
	Migrations       map[string]string `json:"migrations"`
	MinimumUpdater   string            `json:"minimum_updater"`
	PostgreSQL       PostgreSQL        `json:"postgresql"`
	Source           Source            `json:"source"`
	Build            Build             `json:"build"`
	MaxDelayHours    int               `json:"max_delay_hours,omitempty"`
}

// PostgreSQL names the database server a release runs with. A release never
// changes the major version; that is a separate operation.
type PostgreSQL struct {
	Major        int    `json:"major"`
	Version      string `json:"version"`
	SourceSHA256 string `json:"source_sha256"`
}

// Source is the public commit a release was built from.
type Source struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Tag        string `json:"tag"`
}

// Build records how the rebuild agreed and which pinned inputs it used.
type Build struct {
	Method       string `json:"method"`
	InputsSHA256 string `json:"inputs_sha256"`
}

type custom struct {
	Vult *Contract `json:"vult"`
}

var (
	sha256Hex     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imageDigest   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	schemaID      = regexp.MustCompile(`^[0-9]{4}$`)
	migrationName = regexp.MustCompile(`^([0-9]{4})_[a-z0-9_]+\.sql$`)
	osVersion     = regexp.MustCompile(`^[1-9][0-9]*(\.[0-9]+)?$`)
	pgVersion     = regexp.MustCompile(`^([1-9][0-9]*)\.[0-9]+$`)
	repository    = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	commitID      = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// parseContract reads and checks the contract in a target's custom metadata.
func parseContract(raw *json.RawMessage) (Contract, error) {
	if raw == nil {
		return Contract{}, fmt.Errorf("%w: no release contract", ErrContract)
	}
	var c custom
	if err := strictjson.DecodeNonNull(*raw, &c, ""); err != nil || c.Vult == nil {
		return Contract{}, fmt.Errorf("%w: unreadable or unknown fields", ErrContract)
	}
	if err := c.Vult.check(); err != nil {
		return Contract{}, err
	}
	return *c.Vult, nil
}

func (c Contract) check() error {
	bad := func(field string) error { return fmt.Errorf("%w: invalid %s", ErrContract, field) }
	if parseVersion(c.Version) == nil {
		return bad("version")
	}
	switch c.Platform {
	case PlatformMacOS:
		if !osVersion.MatchString(c.MinimumOS) {
			return bad("minimum_os")
		}
		if c.ServerImage != "" || c.MaintenanceImage != "" {
			return bad("images for a macOS release")
		}
	case PlatformUbuntu:
		if !osVersion.MatchString(c.MinimumOS) {
			return bad("minimum_os")
		}
		if c.ServerImage != "" || c.MaintenanceImage != "" {
			return bad("images for an Ubuntu release")
		}
	case PlatformLinux:
		if c.MinimumOS != "" {
			return bad("minimum_os for a Linux release")
		}
		if !imageDigest.MatchString(c.ServerImage) {
			return bad("server_image")
		}
		if !imageDigest.MatchString(c.MaintenanceImage) {
			return bad("maintenance_image")
		}
	default:
		return bad("platform")
	}
	if !schemaID.MatchString(c.SchemaTo) || len(c.SchemaFrom) == 0 {
		return bad("schema")
	}
	seen := map[string]bool{}
	for _, from := range c.SchemaFrom {
		if !schemaID.MatchString(from) || from > c.SchemaTo || seen[from] {
			return bad("schema_from")
		}
		seen[from] = true
	}
	for name, sum := range c.Migrations {
		m := migrationName.FindStringSubmatch(name)
		if m == nil || m[1] > c.SchemaTo || !sha256Hex.MatchString(sum) {
			return bad("migrations")
		}
	}
	if parseVersion(c.MinimumUpdater) == nil {
		return bad("minimum_updater")
	}
	m := pgVersion.FindStringSubmatch(c.PostgreSQL.Version)
	if m == nil || m[1] != strconv.Itoa(c.PostgreSQL.Major) || !sha256Hex.MatchString(c.PostgreSQL.SourceSHA256) {
		return bad("postgresql")
	}
	if !repository.MatchString(c.Source.Repository) || !commitID.MatchString(c.Source.Commit) || c.Source.Tag != "v"+c.Version {
		return bad("source")
	}
	if (c.Build.Method != BuildIdentical && c.Build.Method != BuildSignatureRemoved) || !sha256Hex.MatchString(c.Build.InputsSHA256) {
		return bad("build")
	}
	if c.MaxDelayHours < 0 || c.MaxDelayHours > MaxDelayHours {
		return bad("max_delay_hours")
	}
	return nil
}

// TargetName is where a release's artifact is listed by the release key.
func TargetName(version, platform string) string {
	return releasePrefix + "vult-" + version + "-" + platform + ".tar"
}

// parseVersion reads major.minor.patch with no leading zeros or extras.
func parseVersion(v string) []int {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return nil
		}
		out[i] = n
	}
	return out
}

// newer reports whether b is a later version than a. Invalid input is never newer.
func newer(a, b string) bool {
	x, y := parseVersion(a), parseVersion(b)
	if x == nil || y == nil {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return y[i] > x[i]
		}
	}
	return false
}

// Validate checks a contract the same way installations do.
func (c Contract) Validate() error { return c.check() }

// RebuildTargetName is where the independent rebuild lists the artifact that
// the release key lists as name.
func RebuildTargetName(name string) string {
	return rebuildPrefix + strings.TrimPrefix(name, releasePrefix)
}

// HoldTargetName is the empty target that withdraws version.
func HoldTargetName(version string) string { return holdPrefix + version }

// DelegationPaths returns the one path pattern each delegated role signs for.
func DelegationPaths() map[string]string {
	out := make(map[string]string, len(layout))
	for k, v := range layout {
		out[k] = v
	}
	return out
}

// CustomMetadata is the custom field a release or rebuild target carries.
// An empty migration list is written as {} rather than null, and the result
// is read back with the same parser installations use, so a contract that
// installations would refuse is never signed.
func CustomMetadata(c Contract) (json.RawMessage, error) {
	if c.Migrations == nil {
		c.Migrations = map[string]string{}
	}
	data, err := json.Marshal(custom{Vult: &c})
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(data)
	if _, err := parseContract(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// Newer reports whether version b is later than a. Invalid input is never newer.
func Newer(a, b string) bool { return newer(a, b) }
