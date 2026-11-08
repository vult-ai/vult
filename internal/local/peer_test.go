//go:build darwin || linux

package local

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func peerPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "vult-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	address := &net.UnixAddr{Name: filepath.Join(directory, "peer.sock"), Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	client, err := net.DialUnix("unix", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	return client, server
}

func TestPeerUIDMatchesProcessCredentials(t *testing.T) {
	client, server := peerPair(t)
	for _, connection := range []*net.UnixConn{client, server} {
		uid, err := PeerUID(connection)
		if err != nil || uid != uint32(os.Geteuid()) {
			t.Fatalf("kernel peer identity differs: uid=%d error=%v", uid, err)
		}
	}
}

func TestPeerUIDRejectsClosedSocket(t *testing.T) {
	client, _ := peerPair(t)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := PeerUID(client); err == nil {
		t.Fatal("closed socket yielded a trusted identity")
	}
}

func TestClientCancellationClosesAnEstablishedRequest(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "vult-cancel-")
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := (Client{Socket: path, ExpectedUID: uint32(os.Geteuid())}).Call(ctx, "status", "", struct{}{})
		done <- err
	}()
	listener.SetDeadline(time.Now().Add(time.Second))
	connection, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(time.Second))
	// Read the complete request before cancellation: the outcome must remain
	// unresolved rather than reporting that an operation was safely rejected.
	reader := bufio.NewReader(connection)
	header, err := reader.ReadBytes('\n')
	if err != nil || len(header) == 0 {
		t.Fatal("request header missing", err)
	}
	var payload [2]byte
	if _, err := io.ReadFull(reader, payload[:]); err != nil {
		t.Fatal("request payload missing", err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != "request outcome unresolved" {
			t.Fatal("lost response invented a rejected outcome", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request context did not close its established socket")
	}
	var extra [1]byte
	if _, err := connection.Read(extra[:]); err != io.EOF {
		t.Fatal("cancellation did not close the socket", err)
	}
}

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
