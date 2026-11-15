package secretbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// buildEnclaveHelper compiles the shipped helper for this test, on a Mac.
func buildEnclaveHelper(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("the Secure Enclave helper exists only on macOS")
	}
	out := filepath.Join(t.TempDir(), "vult-enclave")
	if b, err := exec.Command("/usr/bin/xcrun", "swiftc", "-O", "-o", out, "../../packaging/macos/vult_enclave.swift").CombinedOutput(); err != nil {
		t.Fatalf("build helper: %s", b)
	}
	return out
}

func TestEnclaveKeepsTheKeyringOnlyOnThisMac(t *testing.T) {
	helper := buildEnclaveHelper(t)
	ctx := context.Background()
	dir := t.TempDir()
	src, err := MacSource(ctx, dir, helper, LevelRead)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(EnclaveSource); !ok {
		t.Fatalf("a Mac with a Secure Enclave chose %T", src)
	}
	ring, err := Ensure(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := Seal(ring, binding, []byte("token"))
	again, err := src.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(again, binding, sealed); err != nil || string(got) != "token" {
		t.Fatal("the reloaded keyring doesn't open what it sealed", err)
	}
	marshalled, _ := MarshalKeyring(ring)
	for _, name := range []string{"keyring.read.sealed", "enclave.read.key"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		info, _ := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 || len(data) == 0 || containsAny(data, marshalled) {
			t.Fatalf("%s: mode %v, %v", name, info.Mode(), err)
		}
	}
	// A changed sealed file doesn't open.
	path := filepath.Join(dir, "keyring.read.sealed")
	data, _ := os.ReadFile(path)
	data[len(data)-1] ^= 1
	os.WriteFile(path, data, 0o600)
	if _, err := src.Load(ctx); err == nil {
		t.Fatal("a changed keyring opened")
	}
	// The act level is reserved: it needs the person's presence.
	if _, err := MacSource(ctx, dir, helper, LevelAct); !errors.Is(err, ErrPresenceRequired) {
		t.Fatal("the act level was usable in the background", err)
	}
	if _, err := (EnclaveSource{Dir: dir, Helper: helper, Level: LevelAct}).Load(ctx); !errors.Is(err, ErrPresenceRequired) {
		t.Fatal("the act level loaded", err)
	}
	if out, err := exec.Command(helper, "seal", "act", filepath.Join(dir, "x")).CombinedOutput(); err == nil {
		t.Fatalf("the helper sealed at the act level: %s", out)
	}
}

// containsAny reports whether any 16-byte run of secret appears in data.
func containsAny(data, secret []byte) bool {
	for i := 0; i+16 <= len(secret); i += 16 {
		if bytes.Contains(data, secret[i:i+16]) {
			return true
		}
	}
	return false
}

func TestMacsWithoutAnEnclaveUseALockedFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	helper := filepath.Join(dir, "no-enclave")
	os.WriteFile(helper, []byte("#!/bin/sh\nexit 3\n"), 0o755)
	src, err := MacSource(ctx, dir, helper, LevelRead)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(excludedFile); !ok {
		t.Fatalf("chose %T", src)
	}
	if _, err := Ensure(ctx, src); err != nil {
		t.Fatal(err)
	}
	// A keyring already kept in a file stays there.
	if again, _ := MacSource(ctx, dir, "/nonexistent", LevelRead); again == nil {
		t.Fatal("no source")
	} else if _, ok := again.(excludedFile); !ok {
		t.Fatalf("switched to %T", again)
	}
}
