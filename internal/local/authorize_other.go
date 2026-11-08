//go:build !darwin && !linux

package local

import (
	"context"
	"encoding/json"
	"errors"
)

func (c Client) authorizeOwner(context.Context, Authorization, any, string) (json.RawMessage, error) {
	return nil, errors.New("owner authorization requires macOS or Linux")
}

func NativeOwnerAuthorizer() func(context.Context, Peer, OwnerRequest) error { return nil }
