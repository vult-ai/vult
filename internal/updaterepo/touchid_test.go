package updaterepo

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theupdateframework/go-tuf/v2/metadata"

	"github.com/vult-ai/vult/internal/updatetrust"
)

// softTouchID stands in for the Secure Enclave: an ECDSA P-256 key that signs
// the way the helper does, with each signature counted.
type softTouchID struct {
	private *ecdsa.PrivateKey
	calls   int
	corrupt bool
}

func newSoftTouchID(t *testing.T) *softTouchID {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &softTouchID{private: private}
}

func (s *softTouchID) signer(t *testing.T) *Signer {
	t.Helper()
	signer, err := newTouchIDSigner(&s.private.PublicKey, func(msg []byte) ([]byte, error) {
		s.calls++
		digest := sha256.Sum256(msg)
		sig, err := ecdsa.SignASN1(rand.Reader, s.private, digest[:])
		if s.corrupt {
			sig[len(sig)-1] ^= 1
		}
		return sig, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func (s *softTouchID) publicPEM(t *testing.T) string {
	der, err := x509.MarshalPKIXPublicKey(&s.private.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "daily.key.pub")
	os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644)
	return file
}

// touchFixture is a repository whose release role holds a Touch ID key for
// everyday use and the key file S as its backup.
func touchFixture(t *testing.T) (fixture, *softTouchID) {
	t.Helper()
	daily := newSoftTouchID(t)
	dailyKey, _, err := ReadPublicKey(daily.publicPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	if dailyKey.Type != metadata.KeyTypeECDSA_SHA2_P256 {
		t.Fatalf("Touch ID public key read as %s", dailyKey.Type)
	}
	k := newKeys(t, "K1", "K2", "K3", "T1", "T2", "T3", "S", "B", "N", "M")
	r, err := Init(filepath.Join(t.TempDir(), "repo"), Keys{
		Root: []*metadata.Key{k.pub["K1"], k.pub["K2"], k.pub["K3"]}, Targets: []*metadata.Key{k.pub["T1"], k.pub["T2"], k.pub["T3"]},
		Release: []*metadata.Key{dailyKey, k.pub["S"]}, Rebuild: k.pub["B"], Snapshot: k.pub["N"], Timestamp: k.pub["M"],
	}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{repo: r, keys: k}
	for _, step := range []struct{ role, key string }{{"root", "K1"}, {"root", "K2"}, {"targets", "T1"}, {"targets", "T2"}, {"rebuild", "B"}, {"updater", "K1"}, {"updater", "K2"}} {
		f.sign(t, step.role, step.key)
	}
	for _, role := range []string{updatetrust.RoleReleases, updatetrust.RoleHolds} {
		if err := r.Sign(role, daily.signer(t), yes); err != nil {
			t.Fatal(err)
		}
	}
	f.publish(t)
	return f, daily
}

func TestTouchIDKeySignsReleasesInstallationsAccept(t *testing.T) {
	f, daily := touchFixture(t)
	art := artifactFile(t, "payload")
	f.repo.AddRelease(art, contract("0.7.1"), testNow)
	f.repo.AddRebuild(art, contract("0.7.1"), testNow)
	f.sign(t, updatetrust.RoleRebuild, "B")
	before := daily.calls
	if err := f.repo.Sign(updatetrust.RoleReleases, daily.signer(t), yes); err != nil {
		t.Fatal(err)
	}
	if daily.calls != before+1 {
		t.Fatalf("Touch ID was asked %d times for one signature", daily.calls-before)
	}
	f.publish(t)
	got, _, err := Check(f.published(), updatetrust.PlatformMacOS, "0.7.0", testNow)
	if err != nil || got == nil || got.Contract.Version != "0.7.1" {
		t.Fatalf("Check() = %+v, %v", got, err)
	}
}

func TestBackupKeySignsWhenTheTouchIDKeyIsGone(t *testing.T) {
	f, _ := touchFixture(t)
	art := artifactFile(t, "payload")
	f.repo.AddRelease(art, contract("0.7.1"), testNow)
	f.repo.AddRebuild(art, contract("0.7.1"), testNow)
	f.sign(t, updatetrust.RoleRebuild, "B")
	f.sign(t, updatetrust.RoleReleases, "S")
	f.publish(t)
	if got, _, err := Check(f.published(), updatetrust.PlatformMacOS, "0.7.0", testNow); err != nil || got == nil {
		t.Fatalf("Check() with the backup key = %+v, %v", got, err)
	}
}

func TestTouchIDSignatureThatDoesNotVerifyIsNeverAttached(t *testing.T) {
	f, daily := touchFixture(t)
	art := artifactFile(t, "payload")
	f.repo.AddRelease(art, contract("0.7.1"), testNow)
	if err := f.repo.Hold("0.7.1", testNow); err != nil {
		t.Fatal(err)
	}
	daily.corrupt = true
	err := f.repo.Sign(updatetrust.RoleHolds, daily.signer(t), yes)
	if err == nil {
		t.Fatal("a corrupted Touch ID signature was accepted")
	}
	md, _ := f.repo.loadTargets(updatetrust.RoleHolds)
	if len(md.Signatures) != 0 {
		t.Fatalf("holds carries %d signatures after a failed signing", len(md.Signatures))
	}
}

func TestTouchIDPublicKeyMustBeP256(t *testing.T) {
	private, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&private.PublicKey)
	file := filepath.Join(t.TempDir(), "other.pub")
	os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644)
	if _, _, err := ReadPublicKey(file); err == nil || !strings.Contains(err.Error(), "P-256") {
		t.Fatalf("ReadPublicKey(P-384) = %v", err)
	}
}
