package main

import (
	"strings"
	"testing"

	"github.com/vult-ai/vult/internal/loopback"
)

func TestLinuxBrowserOpenerNeverReceivesSetupAuthority(t *testing.T) {
	origin := "https://" + loopback.Host + ":51999"
	code := strings.Repeat("a", 43)
	public, secret, err := publicBrowserDestination(origin + "/auth/setup#setup=" + code)
	if err != nil || public != origin+"/auth/setup" || secret != code || strings.Contains(public, code) || strings.ContainsAny(public, "?#") {
		t.Fatal("setup authority reached the public browser destination", err)
	}
	public, secret, err = publicBrowserDestination(origin)
	if err != nil || public != origin || secret != "" {
		t.Fatal("ordinary browser destination changed", err)
	}
	for _, input := range []string{origin + "/auth/setup", origin + "/auth/setup?setup=" + code, origin + "/auth/setup#setup=" + code + "&setup=" + code, "https://other.example.com:51999/auth/setup#setup=" + code} {
		if _, _, err := publicBrowserDestination(input); err == nil {
			t.Fatal("an unsafe browser launch was accepted")
		}
	}
}
