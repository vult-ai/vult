package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/local"
)

func TestSyntheticSocketChecksActualPeerAndRejectsUnauthenticatedCalls(t *testing.T) {
	if os.Getuid() == 0 {
		t.Fatal("run socket coverage as a regular user")
	}
	dir, e := os.MkdirTemp("/tmp", "vult-socket-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "service.sock")
	l, e := Listen(path)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	s := &Server{}
	go func() { done <- s.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		l.Close()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(7 * time.Second):
			t.Error("socket did not stop")
		}
	})
	c := local.Client{Socket: path, ExpectedUID: uint32(os.Getuid())}
	if _, e = c.Call(ctx, "status", "", struct{}{}); e != nil {
		t.Fatal("real peer check failed", e)
	}
	if _, e = c.Call(ctx, "backup", "", struct{}{}); e == nil {
		t.Fatal("backup lacked authorization")
	}
	if _, e = c.Call(ctx, "get_book", "invalid", struct{}{}); e == nil {
		t.Fatal("unauthenticated financial read")
	}
	c.ExpectedUID++
	if _, e = c.Call(ctx, "status", "", struct{}{}); e == nil {
		t.Fatal("client trusted the wrong service identity")
	}
	// A large unauthenticated header is denied before its body is sent.
	conn, e := net.Dial("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	json.NewEncoder(conn).Encode(local.Header{Method: "restore", Size: local.MaxMessage})
	response, e := local.ReadMessage(conn)
	if e != nil || !strings.Contains(string(response), "access denied") {
		t.Fatal("unauthenticated large body was not rejected promptly", e)
	}
}
