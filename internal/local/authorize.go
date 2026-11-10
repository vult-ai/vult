package local

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

type Authorization struct {
	Action string `json:"action"`
	Digest string `json:"digest"`
	UID    uint32 `json:"uid"`
}

// OwnerRequest identifies a live system-bus connection, never a claimed UID.
type OwnerRequest struct {
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload"`
	BusName string          `json:"bus_name"`
}

// Peer is obtained from the accepted socket, not from a request.
type Peer struct {
	UID uint32
	PID uint32
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
	prompt, e := OwnerPrompt(action, payload)
	if e != nil {
		return "", e
	}
	uid := uint32(os.Getuid())
	input := Authorization{Action: action, Digest: Digest(payload), UID: uid}
	var result json.RawMessage
	if os.Geteuid() == 0 {
		result, e = c.Call(ctx, "authorize", "", input)
	} else {
		result, e = c.authorizeOwner(ctx, input, payload, prompt)
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

// OwnerPrompt describes the exact authority requested in the native dialog.
func OwnerPrompt(action string, payload any) (string, error) {
	if !OwnerAction(action) {
		return "", errors.New("unknown owner action")
	}
	if action == "browser_setup" || action == "browser_reset" {
		data, err := json.Marshal(payload)
		if err != nil || strictjson.Decode(data, &struct{}{}) != nil {
			return "", errors.New("invalid browser setup request")
		}
		if action == "browser_reset" {
			return "Reset Vult browser sign-in. Existing passkeys and browser sign-ins will stop working. Books are preserved.", nil
		}
		return "Finish setting up Vult on this Mac. The browser opens next, where you create the passkey that protects Vult.", nil
	}
	prompt := "Vult requires administrator approval for " + strings.ReplaceAll(action, "_", " ") + "."
	if action == "period_close" {
		data, err := json.Marshal(payload)
		var q PeriodCloseRequest
		if err != nil || strictjson.Decode(data, &q) != nil || !ledger.ValidID(q.BookID) || !ledger.ValidDate(q.From) || !ledger.ValidDate(q.Through) || q.From > q.Through || len(q.Reason) > 4000 {
			return "", errors.New("invalid period close request")
		}
		prompt += " Book " + q.BookID + ", " + q.From + " through " + q.Through + "."
		if q.Reason != "" {
			prompt += " Reason: " + q.Reason
		}
		return prompt, nil
	}
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
	case "init", "book_create", "book_list", "connection_create", "connection_list", "connection_revoke", "period_close", "verify", "export", "backup", "restore", "open_session", "archive_backup", "archive_restore", "browser_setup", "browser_reset":
		return true
	}
	return false
}
