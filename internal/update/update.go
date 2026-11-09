// Package update finds out whether a newer Vult release is published. It reads
// only the public release manifest and sends nothing about the installation.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ManifestURL is the manifest of the latest published release.
const ManifestURL = "https://github.com/vult-ai/vult/releases/latest/download/manifest.json"

// ErrUnavailable means the latest release could not be read; callers treat it
// as "no update known", never as an error the owner must act on.
var ErrUnavailable = errors.New("the latest Vult release is unavailable")

// Status compares this program with the latest published release.
type Status struct {
	Current   string `json:"current"`
	Latest    string `json:"latest,omitempty"`
	Available bool   `json:"available"`
}

// Release describes a published version and its installer checksum, when present.
type Release struct {
	Version         string `json:"version"`
	InstallerSHA256 string `json:"installer_sha256"`
}

// Latest reads the version of the latest published release.
func Latest(ctx context.Context, client *http.Client, url string) (string, error) {
	release, err := LatestRelease(ctx, client, url)
	return release.Version, err
}

// LatestRelease reads release metadata. Installation separately requires a
// valid installer checksum; checking a version does not.
func LatestRelease(ctx context.Context, client *http.Client, url string) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, ErrUnavailable
	}
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return Release{}, ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Release{}, ErrUnavailable
	}
	var manifest Release
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&manifest) != nil || parse(manifest.Version) == nil {
		return Release{}, ErrUnavailable
	}
	return manifest, nil
}

// Newer reports whether latest is a later release than current. Versions are
// plain major.minor.patch numbers; anything else is never newer.
func Newer(current, latest string) bool {
	a, b := parse(current), parse(latest)
	if a == nil || b == nil {
		return false
	}
	for i := range a {
		if b[i] != a[i] {
			return b[i] > a[i]
		}
	}
	return false
}

func parse(v string) []int {
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

// Checker answers from a cached result and asks again at most once in Every.
// A failed check is remembered for the same time, so being offline costs one
// short attempt per period.
type Checker struct {
	Current string
	URL     string
	Client  *http.Client
	Every   time.Duration
	Now     func() time.Time

	mu      sync.Mutex
	checked time.Time
	latest  string
}

// Status returns whether a newer release is available.
func (c *Checker) Status(ctx context.Context) Status {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.checked.IsZero() || now().Sub(c.checked) >= c.Every {
		checked := now()
		latest, err := Latest(ctx, c.Client, c.URL)
		// Leaving the page cancels its request, not the next release check.
		if ctx.Err() == nil {
			c.checked = checked
			if err == nil {
				c.latest = latest
			}
		}
	}
	return Status{Current: c.Current, Latest: c.latest, Available: Newer(c.Current, c.latest)}
}
