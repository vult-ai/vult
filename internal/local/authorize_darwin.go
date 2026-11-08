package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func (c Client) authorizeOwner(ctx context.Context, input Authorization, _ any, prompt string) (json.RawMessage, error) {
	command := BinaryPath + " _authorize " + input.Action + " " + input.Digest + " " + strconv.FormatUint(uint64(input.UID), 10)
	script := fmt.Sprintf("do shell script %q with administrator privileges with prompt %q", command, prompt)
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
	output, err := cmd.Output()
	if err != nil {
		return nil, errors.New("owner authorization cancelled or unavailable")
	}
	return json.RawMessage(strings.TrimSpace(string(output))), nil
}

func NativeOwnerAuthorizer() func(context.Context, Peer, OwnerRequest) error { return nil }
