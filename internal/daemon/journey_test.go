package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vult-ai/vult/internal/local"
)

// ownerRequest supplies a simulated native authorization to the server boundary.
// Actual administrator prompts and installed file permissions require macOS
// installation tests; this helper cannot authorize an installed service.
func ownerRequest(t *testing.T, s *Server, uid uint32, method string, payload any) json.RawMessage {
	t.Helper()
	input := marshal(payload)
	out, e := s.authorize(0, marshal(local.Authorization{Action: method, Digest: local.Digest(input), UID: uid}))
	if e != nil {
		t.Fatal(e)
	}
	var grant struct {
		Token string `json:"token"`
	}
	json.Unmarshal(out, &grant)
	out, e = s.Handle(context.Background(), uid, local.Request{Method: method, Authorization: grant.Token, Payload: input})
	if e != nil {
		t.Fatal(method, e)
	}
	return out
}
