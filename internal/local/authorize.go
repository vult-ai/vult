package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/vult-ai/vult/internal/strictjson"
)

type Authorization struct {
	Action string `json:"action"`
	Digest string `json:"digest"`
	UID    uint32 `json:"uid"`
}

func Digest(payload any) string {
	b, e := json.Marshal(payload)
	if e != nil {
		panic(e)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// OwnerCall obtains a one-use native authorization bound to the exact payload.
func (c Client) OwnerCall(ctx context.Context, action string, payload any) (json.RawMessage, error) {
	if !OwnerAction(action) {
		return nil, errors.New("unknown owner action")
	}
	uid := uint32(os.Getuid())
	input := Authorization{Action: action, Digest: Digest(payload), UID: uid}
	var result json.RawMessage
	var e error
	if os.Geteuid() == 0 {
		result, e = c.Call(ctx, "authorize", "", input)
	} else {
		if runtime.GOOS != "darwin" {
			return nil, errors.New("owner authorization requires macOS")
		}
		command := BinaryPath + " _authorize " + action + " " + input.Digest + " " + strconv.FormatUint(uint64(uid), 10)
		prompt := "Vult requires administrator approval for " + strings.ReplaceAll(action, "_", " ") + "."
		script := fmt.Sprintf("do shell script %q with administrator privileges with prompt %q", command, prompt)
		cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
		output, err := cmd.Output()
		if err != nil {
			return nil, errors.New("owner authorization cancelled or unavailable")
		}
		result = json.RawMessage(strings.TrimSpace(string(output)))
	}
	if e != nil {
		return nil, e
	}
	var reply struct {
		Token string `json:"token"`
	}
	if strictjson.Decode(result, &reply) != nil || reply.Token == "" {
		return nil, errors.New("invalid owner authorization")
	}
	return c.Call(ctx, action, reply.Token, payload)
}
func OwnerAction(s string) bool {
	switch s {
	case "init", "book_create", "book_list", "connection_create", "connection_list", "connection_revoke", "period_close", "verify", "export", "backup", "restore", "open_session":
		return true
	}
	return false
}
