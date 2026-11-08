package main

import (
	"strings"
	"testing"
)

func TestLocalBrowserAddressesCannotOpenAnotherAuthority(t *testing.T) {
	origin := "https://vult-0123456789abcdef0123456789abcdef.localhost:51999"
	setup := origin + "/auth/setup#setup=" + strings.Repeat("a", 43)
	if _, err := localBrowserAddress(origin, false); err != nil {
		t.Fatal(err)
	}
	if _, err := localBrowserAddress(setup, true); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://localhost:51999", "https://ledger.example.com:51999", "javascript:alert(1)", origin + "?token=secret", origin + "/other", origin + "#setup=secret", strings.Replace(origin, ":51999", ":443", 1), strings.Replace(origin, "https://", "https://user@", 1)} {
		if _, err := localBrowserAddress(raw, false); err == nil {
			t.Fatal("unsafe browser destination accepted")
		}
	}
	for _, raw := range []string{origin, setup + "&setup=" + strings.Repeat("b", 43), setup + "&return=https://other.example.com", strings.Replace(setup, "/auth/setup", "/other", 1), setup + "%0a", origin + "/auth/setup?setup=" + strings.Repeat("a", 43)} {
		if _, err := localBrowserAddress(raw, true); err == nil {
			t.Fatal("unsafe enrollment destination accepted")
		}
	}
}
