package main

import (
	"os"
	"strings"

	"github.com/vult-ai/vult/internal/updatehelper"
	"github.com/vult-ai/vult/internal/updatetrust"
)

const platform = updatetrust.PlatformUbuntu

// checkRelease refuses a release that also changes system files.
func checkRelease(l updatehelper.Layout) func(string) error { return updatehelper.SameSystemFiles(l) }

func osVersion() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "0"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if value, ok := strings.CutPrefix(line, "VERSION_ID="); ok {
			return strings.Trim(value, `"`)
		}
	}
	return "0"
}
