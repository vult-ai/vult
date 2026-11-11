//go:build vult_updates && !darwin && !linux

package main

import (
	"context"
	"errors"
	"io"
)

func runUndo(context.Context, io.Writer) error {
	return errors.New("undo is available on macOS and Ubuntu")
}
