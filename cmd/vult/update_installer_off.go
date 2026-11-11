//go:build !vult_updates

package main

import (
	"context"
	"io"
)

// This build doesn't install updates itself.
func automaticUpdate(context.Context, []string, io.Writer) (bool, error) { return false, nil }

func updateChecks() []check { return nil }
