//go:build !darwin

package local

import (
	"errors"
	"net"
)

func PeerUID(c *net.UnixConn) (uint32, error) { return 0, errors.New("native service requires macOS") }
