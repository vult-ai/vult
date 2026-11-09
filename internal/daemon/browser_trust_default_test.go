//go:build !linux

package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestBrowserCertificateCleanupUsesTheExactPublicCertificate(t *testing.T) {
	legacy, _ := legacyBrowserIdentity(t, t.TempDir(), uint32(os.Geteuid()), uint32(os.Getegid()))
	leaf, err := legacyBrowserCertificate(legacy.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	calls := 0
	err = removeBrowserCertificate(legacy.Certificate, dir, func(args ...string) ([]byte, error) {
		calls++
		switch calls {
		case 1:
			if len(args) != 3 || args[0] != "remove-trusted-cert" || args[1] != "-d" {
				t.Fatal("cleanup did not select the administrator trust store", args)
			}
			data, err := os.ReadFile(args[2])
			if err != nil || string(data) != legacy.Certificate {
				t.Fatal("cleanup did not use the recorded public certificate", err)
			}
			info, err := os.Stat(args[2])
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("temporary certificate was not private", err)
			}
		case 2:
			sum := sha256.Sum256(leaf.Raw)
			want := []string{"delete-certificate", "-Z", hex.EncodeToString(sum[:]), "/Library/Keychains/System.keychain"}
			if !reflect.DeepEqual(args, want) {
				t.Fatal("cleanup did not delete by exact fingerprint", args)
			}
		default:
			t.Fatal("cleanup performed an unnecessary lookup", args)
		}
		return nil, nil
	})
	if err != nil || calls != 2 {
		t.Fatal("certificate cleanup failed", err, calls)
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("cleanup left temporary certificates", err)
	}
}

func TestBrowserCertificateCleanupDistinguishesAbsenceFromFailure(t *testing.T) {
	legacy, _ := legacyBrowserIdentity(t, t.TempDir(), uint32(os.Geteuid()), uint32(os.Getegid()))
	for _, variant := range []string{"never trusted", "already removed", "entry remains", "keychain unavailable", "trust unavailable", "unrelated certificate"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			calls := 0
			err := removeBrowserCertificate(legacy.Certificate, dir, func(args ...string) ([]byte, error) {
				calls++
				switch args[0] {
				case "remove-trusted-cert":
					if variant == "trust unavailable" {
						return []byte("security: authorization was denied"), errors.New("denied")
					}
					return []byte("SecTrustSettingsRemoveTrustSettings: The specified item could not be found in the keychain.\n"), errors.New("exit status 1")
				case "delete-certificate":
					if variant == "never trusted" {
						return nil, nil
					}
					return []byte("Unable to delete certificate"), errors.New("delete did not complete")
				case "find-certificate":
					want := []string{"find-certificate", "-a", "-c", legacy.Hostname, "-p", "/Library/Keychains/System.keychain"}
					if !reflect.DeepEqual(args, want) {
						t.Fatal("unexpected absence check", args)
					}
					switch variant {
					case "already removed":
						return nil, nil
					case "entry remains":
						return []byte(legacy.Certificate), nil
					case "keychain unavailable":
						return []byte("security: keychain could not be opened"), errors.New("unavailable")
					case "unrelated certificate":
						other, _ := legacyBrowserIdentity(t, t.TempDir(), uint32(os.Geteuid()), uint32(os.Getegid()))
						return []byte(other.Certificate), nil
					}
				}
				t.Fatal("unexpected certificate command", args)
				return nil, nil
			})
			wantSuccess := variant == "never trusted" || variant == "already removed" || variant == "unrelated certificate"
			if (err == nil) != wantSuccess {
				t.Fatal("cleanup confused absence with failure", err)
			}
			if variant == "trust unavailable" && calls != 1 {
				t.Fatal("cleanup removed the certificate before removing its trust")
			}
			if variant == "never trusted" && calls != 2 {
				t.Fatal("an untrusted certificate was not removed directly")
			}
			files, readErr := os.ReadDir(dir)
			if readErr != nil || len(files) != 0 {
				t.Fatal("failed cleanup left temporary certificates", readErr)
			}
		})
	}
}
