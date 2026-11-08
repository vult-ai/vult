//go:build !darwin && !linux

package local

import (
	"errors"
	"net"
)

func PeerUID(c *net.UnixConn) (uint32, error) {
	return 0, errors.New("native local service requires macOS or Linux")
}

func PeerIdentity(c *net.UnixConn) (Peer, error) {
	return Peer{}, errors.New("native local service requires macOS or Linux")
}
