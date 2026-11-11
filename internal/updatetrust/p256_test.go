package updatetrust

import (
	"io"
	"testing"

	"github.com/sigstore/sigstore/pkg/signature"
)

// withTouchIDReleaseKey makes the everyday release key an ECDSA P-256 key
// that signs in DER form, as a Secure Enclave key does, with an Ed25519 key
// file as its backup, and signs releases and holds with the P-256 key.
func withTouchIDReleaseKey(t *testing.T) *testRepository {
	t.Helper()
	r := newTestRepository(t)
	r.releaseKeys[0] = newP256TestKey(t)
	r.targets.Signed.Delegations = r.standardDelegations()
	r.delegatedSigners[RoleReleases] = r.releaseKeys[:1]
	r.delegatedSigners[RoleHolds] = r.releaseKeys[:1]
	r.publish()
	return r
}

func TestReleaseSignedWithATouchIDKeyInstalls(t *testing.T) {
	r := withTouchIDReleaseKey(t)
	name := r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("payload"), true)
	r.publish()
	sigs := r.delegated[RoleReleases].Signatures
	if len(sigs) != 1 || sigs[0].KeyID != r.releaseKeys[0].id {
		t.Fatalf("releases not signed by the P-256 key: %+v", sigs)
	}
	c := mustOpen(t, r)
	got, refused, err := c.Latest(PlatformMacOS, "0.7.0")
	if err != nil || got == nil || got.Name != name || len(refused) != 0 {
		t.Fatalf("Latest() = %v, %v, %v", got, refused, err)
	}
	if _, err := c.Download(*got, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestHoldSignedWithATouchIDKeyIsHonoured(t *testing.T) {
	r := withTouchIDReleaseKey(t)
	r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("payload"), true)
	r.hold("0.7.1")
	r.publish()
	c := mustOpen(t, r)
	if held, err := c.Held("0.7.1"); err != nil || !held {
		t.Fatalf("Held() = %v, %v", held, err)
	}
	if got, _, err := c.Latest(PlatformMacOS, "0.7.0"); err != nil || got != nil {
		t.Fatalf("a held release was offered: %v, %v", got, err)
	}
}

// damagedSigner flips the last byte of every signature it returns.
type damagedSigner struct{ derP256Signer }

func (d damagedSigner) SignMessage(message io.Reader, opts ...signature.SignOption) ([]byte, error) {
	sig, err := d.derP256Signer.SignMessage(message, opts...)
	if err == nil {
		sig[len(sig)-1] ^= 1
	}
	return sig, err
}

// A P-256 key outside the release role, or a damaged DER signature, signs
// nothing an installation accepts.
func TestTouchIDSignaturesAreChecked(t *testing.T) {
	for name, signer := range map[string]func(*testRepository) testKey{
		"unknown P-256 key": func(*testRepository) testKey { return newP256TestKey(t) },
		"damaged signature": func(r *testRepository) testKey {
			k := r.releaseKeys[0]
			k.der = damagedSigner{k.der.(derP256Signer)}
			return k
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := withTouchIDReleaseKey(t)
			releaseName := r.addRelease(testContract("0.7.1", PlatformMacOS), artifact("payload"), true)
			r.delegatedSigners[RoleReleases] = []testKey{signer(r)}
			r.publish()
			c := mustOpen(t, r)
			if _, err := c.Verify(releaseName); err == nil {
				t.Fatal("verified")
			}
			if got, _, err := c.Latest(PlatformMacOS, "0.7.0"); err == nil && got != nil {
				t.Fatal("a release with a bad signature was offered")
			}
		})
	}
}
