//go:build !darwin && !linux

package daemon

import (
	"errors"
	"os"
)

func protectedACL(string, os.FileInfo) error {
	return errors.New("installation access controls are unsupported on this platform")
}

func codeParents() []string { return nil }
func dataParents() []string { return nil }
