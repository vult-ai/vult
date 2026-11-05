package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/vult-ai/vult/server"
)

func TestCommandRejectsUnconfiguredOrSecretArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--unknown=Example-private-value"}, {"--config=x", "extra"}} {
		var output bytes.Buffer
		e := run(context.Background(), args, &output)
		if !errors.Is(e, server.ErrConfiguration) || output.Len() != 0 {
			t.Fatal("invalid options were accepted or echoed")
		}
	}
}

func TestCommandHelpDoesNotRequireDatabase(t *testing.T) {
	var output bytes.Buffer
	if e := run(context.Background(), []string{"--help"}, &output); e != nil || output.Len() == 0 {
		t.Fatal("help required a database", e)
	}
}
