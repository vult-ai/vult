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
	token, err := c.OwnerAuthorization(ctx, action, payload)
	if err != nil {
		return nil, err
	}
	return c.Call(ctx, action, token, payload)
}

func (c Client) OwnerAuthorization(ctx context.Context, action string, payload any) (string, error) {
	if !OwnerAction(action) {
		return "", errors.New("unknown owner action")
	}
	prompt, e := ownerPrompt(action, payload)
	if e != nil {
		return "", e
	}
	uid := uint32(os.Getuid())
	input := Authorization{Action: action, Digest: Digest(payload), UID: uid}
	var result json.RawMessage
	if os.Geteuid() == 0 {
		result, e = c.Call(ctx, "authorize", "", input)
	} else {
		if runtime.GOOS != "darwin" {
			return "", errors.New("owner authorization requires macOS")
		}
		command := BinaryPath + " _authorize " + action + " " + input.Digest + " " + strconv.FormatUint(uint64(uid), 10)
		script := fmt.Sprintf("do shell script %q with administrator privileges with prompt %q", command, prompt)
		cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
		cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
		output, err := cmd.Output()
		if err != nil {
			return "", errors.New("owner authorization cancelled or unavailable")
		}
		result = json.RawMessage(strings.TrimSpace(string(output)))
	}
	if e != nil {
		return "", e
	}
	var reply struct {
		Token string `json:"token"`
	}
	if strictjson.Decode(result, &reply) != nil || reply.Token == "" {
		return "", errors.New("invalid owner authorization")
	}
	return reply.Token, nil
}

func ownerPrompt(action string, payload any) (string, error) {
	prompt := "Vult requires administrator approval for " + strings.ReplaceAll(action, "_", " ") + "."
	if action == "archive_backup" {
		data, err := json.Marshal(payload)
		var q ArchiveRequest
		if err != nil || strictjson.Decode(data, &q) != nil || !q.Valid() {
			return "", errors.New("invalid archive request")
		}
		if !q.Encrypted {
			prompt += " This exports unencrypted financial records and original documents."
		}
		if q.BookID != "" {
			prompt += " Book " + q.BookID + "."
		} else {
			prompt += " All Books."
		}
		return prompt, nil
	}
	if action == "archive_restore" {
		return prompt + " Existing Books are never overwritten.", nil
	}
	if action != "open_session" {
		return prompt, nil
	}
	data, err := json.Marshal(payload)
	var q SessionRequest
	if err != nil || strictjson.Decode(data, &q) != nil || !q.Valid() {
		return "", errors.New("invalid agent session request")
	}
	prompt += " Connection " + q.ConnectionID + "."
	if q.ImportDirectory != "" {
		prompt += " This agent may read documents inside " + strconv.Quote(q.ImportDirectory) + "."
	}
	if q.ExportDirectory != "" {
		prompt += " This agent may create document exports inside " + strconv.Quote(q.ExportDirectory) + "."
	}
	return prompt, nil
}
func OwnerAction(s string) bool {
	switch s {
	case "init", "book_create", "book_list", "connection_create", "connection_list", "connection_revoke", "period_close", "verify", "export", "backup", "restore", "open_session", "archive_backup", "archive_restore":
		return true
	}
	return false
}
