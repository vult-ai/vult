//go:build darwin

package local

import (
	"golang.org/x/sys/unix"
	"net"
)

func PeerUID(c *net.UnixConn) (uint32, error) {
	var uid uint32
	var result error
	raw, e := c.SyscallConn()
	if e != nil {
		return 0, e
	}
	e = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			result = err
		} else {
			uid = cred.Uid
		}
	})
	if e != nil {
		return 0, e
	}
	return uid, result
}

func PeerIdentity(c *net.UnixConn) (Peer, error) {
	uid, err := PeerUID(c)
	return Peer{UID: uid}, err
}
