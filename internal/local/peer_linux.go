package local

import (
	"net"

	"golang.org/x/sys/unix"
)

// PeerUID uses credentials captured by the kernel when the socket connected.
// An identity supplied in a request or a socket pathname grants no authority.
func PeerUID(c *net.UnixConn) (uint32, error) {
	peer, err := PeerIdentity(c)
	return peer.UID, err
}

func PeerIdentity(c *net.UnixConn) (Peer, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var peer Peer
	var credentialErr error
	if err = raw.Control(func(fd uintptr) {
		credentials, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			credentialErr = err
			return
		}
		peer = Peer{UID: credentials.Uid, PID: uint32(credentials.Pid)}
	}); err != nil {
		return Peer{}, err
	}
	return peer, credentialErr
}
