//go:build !linux

package daemon

import (
	"strings"
	"testing"
)

// The removal's own line breaks reach the terminal as line breaks.
func TestUninstallKeepsLineBreaks(t *testing.T) {
	if !strings.HasSuffix(uninstallScript("/usr/local/bin/vult _uninstall"), " without altering line endings") {
		t.Fatal(uninstallScript("/usr/local/bin/vult _uninstall"))
	}
}
