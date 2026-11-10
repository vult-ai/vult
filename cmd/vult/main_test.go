package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestArchiveCommandsKeepBrokenPipesSeparateFromMCPLifecycle(t *testing.T) {
	if command := os.Getenv("VULT_TEST_PIPE_COMMAND"); command != "" {
		// Malformed inputs fail before service access. Their closed diagnostic
		// pipe exercises main's real signal policy without owner authorization.
		os.Args = append([]string{"vult"}, strings.Fields(command)...)
		main()
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Bare mcp starts an agent bridge, so an unknown flag stands in for a malformed call.
	for _, command := range []string{"backup", "restore", "export", "mcp --unknown"} {
		t.Run(command, func(t *testing.T) {
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			read.Close()
			defer write.Close()
			child := exec.Command(executable, "-test.run=^TestArchiveCommandsKeepBrokenPipesSeparateFromMCPLifecycle$")
			child.Env = append(os.Environ(), "VULT_TEST_PIPE_COMMAND="+command)
			child.Stdout, child.Stderr = io.Discard, write
			err = child.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal("malformed command did not fail", err)
			}
			status := exit.Sys().(syscall.WaitStatus)
			if strings.HasPrefix(command, "mcp") {
				if !status.Signaled() || status.Signal() != syscall.SIGPIPE {
					t.Fatal("MCP inherited the owner command's signal policy", status)
				}
			} else if status.Signaled() || exit.ExitCode() != 1 {
				t.Fatal("status pipe masked the operation's actual failure", status)
			}
		})
	}
}

func TestPublicHelpAndVersionNeedNoInstallation(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"--version"}} {
		var out, errout bytes.Buffer
		if e := run(args, &out, &errout); e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(out.String(), "0.6.0") || errout.Len() != 0 {
			t.Fatal("invalid help or version output")
		}
	}
}
func TestMalformedCommandsFailBeforeServiceAccess(t *testing.T) {
	for _, args := range [][]string{{"help", "extra"}, {"--version", "extra"}, {"_authorize", "bad"}, {"_stop", "extra"}, {"_uninstall", "extra"}, {"uninstall", "--data"}, {"uninstall", "--purge"}, {"unknown"}, {"book", "create", "--name", "Example"}, {"connection", "create", "--book", "bad"}, {"mcp", "--connection", "bad"}, {"period", "close", "--book", "bad"}, {"restore"}, {"backup"}, {"export", "--book", "bad"}} {
		var out, errout bytes.Buffer
		if e := run(args, &out, &errout); e == nil || out.Len() != 0 {
			t.Fatal("invalid command succeeded", args)
		}
	}
}
