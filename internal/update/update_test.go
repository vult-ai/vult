package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLatestReleaseChecksVersionWithoutRequiringAnInstaller(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	for _, c := range []struct {
		name, version, checksum string
		valid                   bool
	}{
		{"complete release", "0.5.2", digest, true},
		{"uppercase checksum", "0.5.2", strings.ToUpper(digest), true},
		{"version only", "0.5.2", "", true},
		{"unusable installer checksum", "0.5.2", "ab", true},
		{"invalid version", "../0.5.2", digest, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"version":%q,"installer_sha256":%q}`, c.version, c.checksum)
			}))
			defer srv.Close()
			release, err := LatestRelease(context.Background(), srv.Client(), srv.URL)
			if c.valid {
				if err != nil || release.Version != c.version || release.InstallerSHA256 != c.checksum {
					t.Fatalf("release: %+v, error: %v", release, err)
				}
			} else if err != ErrUnavailable {
				t.Fatalf("invalid release was accepted: %+v, error: %v", release, err)
			}
		})
	}
}

func TestNewerComparesWholeReleaseNumbers(t *testing.T) {
	for _, c := range []struct {
		current, latest string
		newer           bool
	}{
		{"0.5.1", "0.5.2", true},
		{"0.5.1", "0.10.0", true},
		{"0.9.9", "1.0.0", true},
		{"0.5.1", "0.5.1", false},
		{"0.5.2", "0.5.1", false},
		{"0.5.1", "", false},
		{"0.5.1", "0.6", false},
		{"0.5.1", "0.6.0-rc1", false},
		{"0.5.1", "v0.6.0", false},
		{"0.5.1", "0.06.0", false},
	} {
		if got := Newer(c.current, c.latest); got != c.newer {
			t.Fatalf("Newer(%q, %q) = %v", c.current, c.latest, got)
		}
	}
}

func TestCheckerReadsTheManifestOncePerPeriodAndIgnoresFailures(t *testing.T) {
	var calls atomic.Int32
	body := `{"version":"0.5.2","source_files":{}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	c := &Checker{Current: "0.5.1", URL: srv.URL, Client: srv.Client(), Every: 12 * time.Hour, Now: func() time.Time { return now }}
	if s := c.Status(context.Background()); !s.Available || s.Latest != "0.5.2" || s.Current != "0.5.1" {
		t.Fatal("a newer release was not reported", s)
	}
	c.Status(context.Background())
	if calls.Load() != 1 {
		t.Fatal("the manifest was read again within the period", calls.Load())
	}
	now = now.Add(13 * time.Hour)
	body = `not a manifest`
	if s := c.Status(context.Background()); !s.Available || s.Latest != "0.5.2" {
		t.Fatal("a failed check forgot the last known release", s)
	}
	if calls.Load() != 2 {
		t.Fatal("the manifest was not read after the period", calls.Load())
	}
}

func TestCheckerOfflineReportsNoUpdate(t *testing.T) {
	c := &Checker{Current: "0.5.1", URL: "http://127.0.0.1:1/manifest.json", Client: &http.Client{Timeout: time.Second}, Every: time.Hour}
	if s := c.Status(context.Background()); s.Available || s.Latest != "" {
		t.Fatal("an unreachable release was reported", s)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCheckerCallerCancellationAllowsTheNextRequestToRetry(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "canceled in flight"
		if deadline {
			name = "caller deadline elapsed"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			}
			defer cancel()
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 && !deadline {
					cancel()
				}
				if err := r.Context().Err(); err != nil {
					return nil, err
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"version":"0.5.2"}`))}, nil
			})}
			c := &Checker{Current: "0.5.1", URL: "https://example.invalid/manifest.json", Client: client, Every: 12 * time.Hour}
			if s := c.Status(ctx); s.Available || s.Latest != "" {
				t.Fatal("the canceled check reported a release", s)
			}
			if s := c.Status(context.Background()); !s.Available || s.Latest != "0.5.2" {
				t.Fatal("the next request could not retry the canceled check", s)
			}
			after := calls
			c.Status(context.Background())
			if calls != after {
				t.Fatal("the successful retry was not cached", calls)
			}
		})
	}
}

func TestCheckerCachesNetworkFailuresUntilTheNextPeriod(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("network unavailable")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"version":"0.5.2"}`))}, nil
	})}
	c := &Checker{Current: "0.5.1", URL: "https://example.invalid/manifest.json", Client: client, Every: 12 * time.Hour, Now: func() time.Time { return now }}
	for range 2 {
		if s := c.Status(context.Background()); s.Available || s.Latest != "" {
			t.Fatal("the failed check reported a release", s)
		}
	}
	if calls != 1 {
		t.Fatal("the network failure was retried within the period", calls)
	}
	now = now.Add(13 * time.Hour)
	if s := c.Status(context.Background()); !s.Available || s.Latest != "0.5.2" || calls != 2 {
		t.Fatal("the next period did not retry the failed check", s, calls)
	}
}
