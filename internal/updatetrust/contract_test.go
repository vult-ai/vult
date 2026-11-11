package updatetrust

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestContractAcceptsBothPlatforms(t *testing.T) {
	for _, platform := range []string{PlatformMacOS, PlatformLinux} {
		if _, err := parseContract(customJSON(t, testContract("0.7.1", platform))); err != nil {
			t.Fatalf("%s: %v", platform, err)
		}
	}
}

func TestContractRefusesWhatItDoesNotFullyUnderstand(t *testing.T) {
	raw := func(s string) *json.RawMessage { m := json.RawMessage(s); return &m }
	valid := customJSON(t, testContract("0.7.1", PlatformMacOS))
	withField := strings.Replace(string(*valid), `"version"`, `"run_after_install":"/bin/sh","version"`, 1)
	duplicate := strings.Replace(string(*valid), `"version":"0.7.1"`, `"version":"0.7.1","version":"9.9.9"`, 1)
	for name, input := range map[string]*json.RawMessage{
		"missing":       nil,
		"unknown field": raw(withField),
		"duplicate key": raw(duplicate),
		"other wrapper": raw(`{"other":{}}`),
		"extra wrapper": raw(strings.TrimSuffix(string(*valid), "}") + `,"other":1}`),
		"null contract": raw(`{"vult":null}`),
		"not an object": raw(`[]`),
	} {
		if _, err := parseContract(input); !errors.Is(err, ErrContract) {
			t.Errorf("%s: error = %v, want contract error", name, err)
		}
	}
}

func TestContractFieldsAreChecked(t *testing.T) {
	for name, change := range map[string]func(*Contract){
		"version with leading zero": func(c *Contract) { c.Version = "0.07.1" },
		"unknown platform":          func(c *Contract) { c.Platform = "macos-x86_64" },
		"macOS without minimum OS":  func(c *Contract) { c.MinimumOS = "" },
		"macOS with an image":       func(c *Contract) { c.ServerImage = "sha256:" + strings.Repeat("0", 64) },
		"image by tag": func(c *Contract) {
			c.Platform = PlatformLinux
			c.MinimumOS = ""
			c.ServerImage = "vult-server:latest"
			c.MaintenanceImage = "sha256:" + strings.Repeat("0", 64)
		},
		"no starting schema":           func(c *Contract) { c.SchemaFrom = nil },
		"starting schema after target": func(c *Contract) { c.SchemaFrom = []string{"0009"} },
		"repeated starting schema":     func(c *Contract) { c.SchemaFrom = []string{"0005", "0005"} },
		"migration path":               func(c *Contract) { c.Migrations = map[string]string{"../0007_x.sql": strings.Repeat("a", 64)} },
		"migration beyond target":      func(c *Contract) { c.Migrations = map[string]string{"0008_x.sql": strings.Repeat("a", 64)} },
		"migration checksum":           func(c *Contract) { c.Migrations = map[string]string{"0007_x.sql": "abc"} },
		"minimum updater":              func(c *Contract) { c.MinimumUpdater = "1" },
		"PostgreSQL major disagrees":   func(c *Contract) { c.PostgreSQL.Major = 17 },
		"tag disagrees with version":   func(c *Contract) { c.Source.Tag = "v0.7.0" },
		"short commit":                 func(c *Contract) { c.Source.Commit = "abc123" },
		"unknown build method":         func(c *Contract) { c.Build.Method = "trusted" },
		"negative delay":               func(c *Contract) { c.MaxDelayHours = -1 },
		"delay beyond the limit":       func(c *Contract) { c.MaxDelayHours = MaxDelayHours + 1 },
	} {
		c := testContract("0.7.1", PlatformMacOS)
		change(&c)
		if _, err := parseContract(customJSON(t, c)); !errors.Is(err, ErrContract) {
			t.Errorf("%s: error = %v, want contract error", name, err)
		}
	}
}

func TestVersionsCompareNumerically(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"0.7.9", "0.7.10", true},
		{"0.7.10", "0.7.9", false},
		{"0.7.1", "0.7.1", false},
		{"0.7.1", "1.0.0", true},
		{"0.7.1", "0.7.1-rc1", false},
		{"bad", "0.7.1", false},
	} {
		if got := newer(tc.a, tc.b); got != tc.want {
			t.Errorf("newer(%s, %s) = %v", tc.a, tc.b, got)
		}
	}
}
