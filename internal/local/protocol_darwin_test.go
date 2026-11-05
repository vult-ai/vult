package local

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRejectedServicePeerClosesSocketImmediately(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "vult-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "service.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	closed := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var b [1]byte
		_, err = conn.Read(b[:])
		closed <- err
	}()
	client := Client{Socket: path, ExpectedUID: uint32(os.Getuid()) + 1}
	if _, err = client.Call(context.Background(), "status", "", struct{}{}); err == nil {
		t.Fatal("wrong service peer accepted")
	}
	select {
	case err = <-closed:
		if err != io.EOF {
			t.Fatal("rejected peer socket left open", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("rejected peer retained socket")
	}
}
