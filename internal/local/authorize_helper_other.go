//go:build !linux

package local

import (
	"context"
	"errors"
	"io"
)

func OwnerAuthorizationHelper(context.Context, io.Reader, io.Writer) error {
	return errors.New("owner authorization helper requires Linux")
}
