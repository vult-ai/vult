package main

import (
	"os/exec"
	"strings"

	"github.com/vult-ai/vult/internal/updatehelper"
	"github.com/vult-ai/vult/internal/updatetrust"
)

const platform = updatetrust.PlatformMacOS

// checkRelease has nothing more to check on macOS.
func checkRelease(updatehelper.Layout) func(string) error { return nil }

func osVersion() string {
	cmd := exec.Command("/usr/bin/sw_vers", "-productVersion")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C"}
	out, err := cmd.Output()
	if err != nil {
		return "0"
	}
	return strings.TrimSpace(string(out))
}
