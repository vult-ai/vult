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

var binding = Binding{Purpose: "bank.access_token", TenantID: "t-1", BookID: "b-1", ConnectionID: "c-1"}

func TestSealedValuesOpenOnlyForTheirBinding(t *testing.T) {
	k, err := NewKeyring()
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("access-sandbox-synthetic-token")
	sealed, err := Seal(k, binding, secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, secret) {
		t.Fatal("sealed value contains its plaintext")
	}
	if got, err := Open(k, binding, sealed); err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("open = %q, %v", got, err)
	}
	for name, other := range map[string]Binding{
		"purpose":    {Purpose: "bank.secret", TenantID: "t-1", BookID: "b-1", ConnectionID: "c-1"},
		"tenant":     {Purpose: binding.Purpose, TenantID: "t-2", BookID: "b-1", ConnectionID: "c-1"},
		"book":       {Purpose: binding.Purpose, TenantID: "t-1", BookID: "b-2", ConnectionID: "c-1"},
		"connection": {Purpose: binding.Purpose, TenantID: "t-1", BookID: "b-1", ConnectionID: "c-2"},
		// Length prefixes keep field boundaries: moving characters between
		// fields is a different binding.
		"boundaries": {Purpose: binding.Purpose, TenantID: "t-1b", BookID: "-1", ConnectionID: "c-1"},
	} {
		if _, err := Open(k, other, sealed); !errors.Is(err, ErrSealed) {
			t.Fatalf("%s: a value copied to another binding opened: %v", name, err)
		}
	}
	for i := range sealed {
		changed := bytes.Clone(sealed)
		changed[i] ^= 1
		if _, err := Open(k, binding, changed); err == nil {
			t.Fatalf("a value changed at byte %d still opened", i)
		}
	}
	again, _ := Seal(k, binding, secret)
	if bytes.Equal(again, sealed) {
		t.Fatal("two seals of one value are identical; nonces repeat")
	}
}

func TestOtherKeyringsCannotOpen(t *testing.T) {
	a, _ := NewKeyring()
	b, _ := NewKeyring()
	sealed, _ := Seal(a, binding, []byte("x"))
	if _, err := Open(b, binding, sealed); !errors.Is(err, ErrSealed) {
		t.Fatalf("another keyring with the same version opened the value: %v", err)
	}
}

func TestRotationKeepsOldValuesReadableAndReseals(t *testing.T) {
	k, _ := NewKeyring()
	old, _ := Seal(k, binding, []byte("token"))
	if v, _ := Version(old); v != 1 {
		t.Fatalf("version = %d", v)
	}
	if v, err := k.Rotate(); err != nil || v != 2 {
		t.Fatalf("rotate = %d, %v", v, err)
	}
	if got, err := Open(k, binding, old); err != nil || string(got) != "token" {
		t.Fatalf("old value after rotation = %q, %v", got, err)
	}
	fresh, changed, err := Reseal(k, binding, old)
	if err != nil || !changed {
		t.Fatalf("reseal = %v, %v", changed, err)
	}
	if v, _ := Version(fresh); v != 2 {
		t.Fatalf("resealed version = %d", v)
	}
	if same, changed, err := Reseal(k, binding, fresh); err != nil || changed || !bytes.Equal(same, fresh) {
		t.Fatalf("resealing a current value changed it: %v %v", changed, err)
	}
	if err := k.Retire(2); err == nil {
		t.Fatal("retired the current key")
	}
	if err := k.Retire(1); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(k, binding, old); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("value sealed with a retired key: %v", err)
	}
	if got, err := Open(k, binding, fresh); err != nil || string(got) != "token" {
		t.Fatalf("resealed value = %q, %v", got, err)
	}
}

func TestInvalidBindingsAreRefused(t *testing.T) {
	k, _ := NewKeyring()
	for _, b := range []Binding{{}, {Purpose: "p", TenantID: "t", BookID: "b"}, {Purpose: "p\n", TenantID: "t", BookID: "b", ConnectionID: "c"}} {
		if _, err := Seal(k, b, []byte("x")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("sealed under %+v", b)
		}
	}
	if _, err := Seal(k, binding, make([]byte, MaxPlaintext+1)); !errors.Is(err, ErrInvalid) {
		t.Fatal("sealed an oversized value")
	}
}

func TestKeyringRoundTripAndStrictParsing(t *testing.T) {
	k, _ := NewKeyring()
	k.Rotate()
	b, err := MarshalKeyring(k)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseKeyring(b)
	if err != nil || parsed.Current() != 2 || len(parsed.Versions()) != 2 {
		t.Fatalf("parsed = %v, %v", parsed, err)
	}
	sealed, _ := Seal(k, binding, []byte("x"))
	if _, err := Open(parsed, binding, sealed); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{}`, `{"format":1,"current":3,"keys":{"1":"AAAA"}}`,
		`{"format":1,"current":1,"keys":{"01":"` + string(bytes.Repeat([]byte("A"), 44)) + `"}}`,
		`{"format":2,"current":1,"keys":{}}`,
	} {
		if _, err := ParseKeyring([]byte(bad)); err == nil {
			t.Fatalf("parsed %s", bad)
		}
	}
}

func TestFileSourceCreatesAPrivateKeyringOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "bank-keys.json")
	src := FileSource{Path: path}
	first, err := Ensure(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("keyring file mode = %v, %v", info.Mode(), err)
	}
	sealed, _ := Seal(first, binding, []byte("x"))
	second, err := Ensure(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(second, binding, sealed); err != nil {
		t.Fatal("a second start replaced the keyring")
	}
	os.Chmod(path, 0o644)
	if _, err := src.Load(ctx); err == nil {
		t.Fatal("loaded a keyring other users can read")
	}
	if _, err := (FileSource{Path: "relative.json"}).Load(ctx); err == nil {
		t.Fatal("loaded a relative path")
	}
}

func TestCachedRotationIsStoredBeforeUse(t *testing.T) {
	ctx := context.Background()
	c := &Cached{Source: FileSource{Path: filepath.Join(t.TempDir(), "keys.json")}}
	k, err := c.Keyring(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := Seal(k, binding, []byte("x"))
	if v, err := c.Rotate(ctx); err != nil || v != 2 {
		t.Fatalf("rotate = %d, %v", v, err)
	}
	reloaded, _ := c.Source.Load(ctx)
	if reloaded.Current() != 2 {
		t.Fatal("the rotation wasn't stored")
	}
	if _, err := Open(reloaded, binding, old); err != nil {
		t.Fatal("the old key was lost in rotation")
	}
}

func TestKeychainSourcePassesTheSecretOnStandardInput(t *testing.T) {
	var calls [][]string
	var stdin [][]byte
	stored := []byte{}
	src := KeychainSource{Service: "ai.vult.bank-keys", Account: "keyring", Keychain: SystemKeychain,
		Run: func(_ context.Context, in []byte, args ...string) ([]byte, error) {
			calls, stdin = append(calls, args), append(stdin, bytes.Clone(in))
			if args[0] == "find-generic-password" {
				if len(stored) == 0 {
					return nil, ErrNoKeyring
				}
				return stored, nil
			}
			// Decode what the interactive command would store.
			fields := bytes.Fields(in)
			for i, f := range fields {
				if string(f) == "-X" {
					stored = make([]byte, len(fields[i+1])/2)
					for j := range stored {
						var v byte
						for _, c := range fields[i+1][2*j : 2*j+2] {
							v <<= 4
							if c >= 'a' {
								v |= c - 'a' + 10
							} else {
								v |= c - '0'
							}
						}
						stored[j] = v
					}
				}
			}
			return nil, nil
		}}
	k, err := Ensure(context.Background(), src)
	if err != nil || k.Current() != 1 {
		t.Fatalf("ensure = %v, %v", k, err)
	}
	marshalled, _ := MarshalKeyring(k)
	for i, args := range calls {
		for _, a := range args {
			if bytes.Contains(marshalled, []byte(a)) && len(a) > 8 {
				t.Fatalf("call %d put key material in its arguments", i)
			}
		}
		if args[0] == "-i" && !bytes.HasPrefix(stdin[i], []byte("add-generic-password -U -s ai.vult.bank-keys -a keyring -X ")) {
			t.Fatalf("store command = %q", stdin[i])
		}
	}
	if (KeychainSource{Service: "a b", Account: "x"}).valid() {
		t.Fatal("accepted a service name that would split the command line")
	}
}

// TestKeychainSourceWithARealKeychain stores and reads a keyring in a
// throwaway keychain file, on macOS only.
func TestKeychainSourceWithARealKeychain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the macOS Keychain exists only on macOS")
	}
	dir := t.TempDir()
	keychain := filepath.Join(dir, "test.keychain-db")
	if out, err := exec.Command("/usr/bin/security", "create-keychain", "-p", "synthetic-test-only", keychain).CombinedOutput(); err != nil {
		t.Fatalf("create keychain: %s", out)
	}
	defer exec.Command("/usr/bin/security", "delete-keychain", keychain).Run()
	if out, err := exec.Command("/usr/bin/security", "unlock-keychain", "-p", "synthetic-test-only", keychain).CombinedOutput(); err != nil {
		t.Fatalf("unlock keychain: %s", out)
	}
	src := KeychainSource{Service: "ai.vult.test-bank-keys", Account: "keyring", Keychain: keychain}
	ctx := context.Background()
	if _, err := src.Load(ctx); !errors.Is(err, ErrNoKeyring) {
		t.Fatalf("empty keychain load = %v", err)
	}
	k, err := Ensure(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := Seal(k, binding, []byte("x"))
	k.Rotate()
	if err := src.Store(ctx, k); err != nil {
		t.Fatal(err)
	}
	again, err := src.Load(ctx)
	if err != nil || again.Current() != 2 {
		t.Fatalf("reloaded = %v, %v", again, err)
	}
	if _, err := Open(again, binding, sealed); err != nil {
		t.Fatal("the keychain lost the first key")
	}
}
