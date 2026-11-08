package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/vult-ai/vult/internal/local"
)

type observedPayloadReader struct {
	io.Reader
	started chan struct{}
	once    sync.Once
}

func (r *observedPayloadReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.Reader.Read(p)
}

func TestCanceledAdmittedArchiveDoesNotWaitForWithheldPayload(t *testing.T) {
	s := &Server{}
	request := local.ArchiveRequest{Encrypted: true}
	payload := marshal(request)
	uid := uint32(501)
	data, err := s.authorize(0, marshal(local.Authorization{Action: "archive_backup", Digest: local.Digest(request), UID: uid}))
	if err != nil {
		t.Fatal(err)
	}
	var grant struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(data, &grant) != nil {
		t.Fatal("missing grant")
	}
	header := local.Header{Method: "archive_backup", Authorization: grant.Token, Size: len(payload)}
	if !s.admit(uid, header) {
		t.Fatal("positive admitted-header control failed")
	}
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &observedPayloadReader{Reader: server, started: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); s.servePayload(ctx, local.Peer{UID: uid}, header, reader, server) }()
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		t.Fatal("admitted archive did not wait on metadata payload")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled archive retained a blocked worker")
	}
	if len(s.grants) != 1 {
		t.Fatal("withheld payload reached owner effects")
	}
}
