package daemon

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/loopback"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/server"
)

// legacyBrowserIdentity writes a pre-0.5.1 identity: its address carried the
// installation identity, vult-<installation>.localhost.
func legacyBrowserIdentity(t *testing.T, dir string, uid, gid uint32) (browserConfiguration, []byte) {
	t.Helper()
	current, err := newBrowserConfiguration(51997)
	if err != nil {
		t.Fatal(err)
	}
	legacy := current
	legacy.Format, legacy.Installation, legacy.Hostname = legacyBrowserFormat, "", "vult-"+current.Installation+".localhost"
	pair, err := tls.X509KeyPair([]byte(current.Certificate), []byte(current.Key))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	leaf.Subject.CommonName, leaf.DNSNames = legacy.Hostname, []string{legacy.Hostname}
	leaf.RawSubject = nil
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, leaf.PublicKey, pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	data, _ := json.Marshal(legacy)
	if err := createBrowserPrivate(filepath.Join(dir, browserConfigurationName), data, uid, gid); err != nil {
		t.Fatal(err)
	}
	ready, _ := json.Marshal(browserIdentityState{State: "ready", SHA256: access.Hash(string(data))})
	if err := createBrowserPrivate(filepath.Join(dir, browserStateName), ready, uid, gid); err != nil {
		t.Fatal(err)
	}
	return legacy, data
}

func ensureBrowserUpgrade(dir string, uid, gid uint32) (browserConfiguration, error) {
	return ensureBrowserConfigurationCleanup(dir, uid, gid, func(string) error { return nil })
}

func upgradeDirectory(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// An upgrade moves the address to vult.localhost and keeps everything that
// names the installation: its port and its database logins.
func TestBrowserUpgradeMovesToTheSharedAddressAndKeepsTheInstallation(t *testing.T) {
	dir := upgradeDirectory(t)
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	legacy, _ := legacyBrowserIdentity(t, dir, uid, gid)
	id, _ := loopback.LegacyInstallationID(legacy.Hostname)

	upgraded, err := ensureBrowserUpgrade(dir, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	if !upgraded.valid() || upgraded.Hostname != loopback.Host || upgraded.Installation != id || upgraded.Port != legacy.Port ||
		upgraded.AuthPassword != legacy.AuthPassword || upgraded.OwnerPassword != legacy.OwnerPassword {
		t.Fatal("the upgrade changed what names the installation", upgraded.Hostname, upgraded.Port)
	}
	if upgraded.origin() != "https://vult.localhost:51997" {
		t.Fatal("unexpected address", upgraded.origin())
	}
	auth, owner := upgraded.roles()
	if auth != "vult_browser_auth_"+id[:16] || owner != "vult_browser_owner_"+id[:16] {
		t.Fatal("the upgrade renamed the installation's database logins")
	}
	if _, err := loopback.ParseCertificate(loopback.Host, []byte(upgraded.Certificate), []byte(upgraded.Key)); err != nil {
		t.Fatal("the new certificate does not serve vult.localhost", err)
	}
	read, err := readBrowserConfiguration(filepath.Join(dir, browserConfigurationName), uid)
	if err != nil || read != upgraded {
		t.Fatal("the running service cannot read the upgraded identity", err)
	}
	again, err := ensureBrowserUpgrade(dir, uid, gid)
	if err != nil || again != upgraded {
		t.Fatal("a second upgrade changed the identity again", err)
	}
}

// An upgrade interrupted after writing the new identity finishes on the next
// run; one interrupted before it starts over from the old identity.
func TestBrowserUpgradeSurvivesInterruption(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())

	dir := upgradeDirectory(t)
	legacy, oldData := legacyBrowserIdentity(t, dir, uid, gid)
	next, _, err := legacy.upgraded()
	if err != nil {
		t.Fatal(err)
	}
	nextData, _ := json.Marshal(next)
	moving, _ := json.Marshal(browserIdentityState{State: "ready", SHA256: access.Hash(string(oldData)), Next: access.Hash(string(nextData)), Cleanup: legacy.Certificate})
	if err := writeBrowserFile(filepath.Join(dir, browserStateName), moving, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := writeBrowserFile(filepath.Join(dir, browserConfigurationName), nextData, uid, gid); err != nil {
		t.Fatal(err)
	}
	finished, err := ensureBrowserUpgrade(dir, uid, gid)
	if err != nil || finished != next {
		t.Fatal("an upgrade interrupted after writing its identity did not finish", err)
	}
	state, err := readBrowserState(filepath.Join(dir, browserStateName), uid)
	if err != nil || state.Next != "" || state.SHA256 != access.Hash(string(nextData)) {
		t.Fatal("the finished upgrade still records two identities", err)
	}

	dir = upgradeDirectory(t)
	legacy, oldData = legacyBrowserIdentity(t, dir, uid, gid)
	early, _ := json.Marshal(browserIdentityState{State: "ready", SHA256: access.Hash(string(oldData)), Next: access.Hash("an identity that was never written"), Cleanup: legacy.Certificate})
	if err := writeBrowserFile(filepath.Join(dir, browserStateName), early, uid, gid); err != nil {
		t.Fatal(err)
	}
	restarted, err := ensureBrowserUpgrade(dir, uid, gid)
	if err != nil || restarted.Hostname != loopback.Host || restarted.Port != legacy.Port {
		t.Fatal("an upgrade interrupted before writing its identity did not start over", err)
	}
}

// A changed or unrecognised old identity still needs recovery; the upgrade
// never replaces an identity it cannot verify.
func TestBrowserUpgradeRefusesUnverifiedIdentities(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	dir := upgradeDirectory(t)
	legacyBrowserIdentity(t, dir, uid, gid)
	other, _ := json.Marshal(browserIdentityState{State: "ready", SHA256: access.Hash("another identity")})
	if err := writeBrowserFile(filepath.Join(dir, browserStateName), other, uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureBrowserUpgrade(dir, uid, gid); err == nil {
		t.Fatal("an identity that does not match its record was upgraded")
	}

	dir = upgradeDirectory(t)
	legacy, _ := legacyBrowserIdentity(t, dir, uid, gid)
	legacy.Hostname = "vult-not-an-installation.localhost"
	data, _ := json.Marshal(legacy)
	if err := writeBrowserFile(filepath.Join(dir, browserConfigurationName), data, uid, gid); err != nil {
		t.Fatal(err)
	}
	ready, _ := json.Marshal(browserIdentityState{State: "ready", SHA256: access.Hash(string(data))})
	if err := writeBrowserFile(filepath.Join(dir, browserStateName), ready, uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureBrowserUpgrade(dir, uid, gid); err == nil {
		t.Fatal("an unrecognised old address was upgraded")
	}
}

func TestBrowserUpgradeRetriesCertificateCleanupWithoutChangingIdentity(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	dir := upgradeDirectory(t)
	legacy, _ := legacyBrowserIdentity(t, dir, uid, gid)
	statePath := filepath.Join(dir, browserStateName)
	calls := 0
	cleanup := func(certificate string) error {
		calls++
		state, err := readBrowserState(statePath, uid)
		if err != nil || state.Cleanup != legacy.Certificate || certificate != legacy.Certificate {
			t.Fatal("cleanup was not recorded before replacing the identity", err)
		}
		if calls == 1 {
			return errors.New("certificate store is unavailable")
		}
		return nil
	}
	upgraded, err := ensureBrowserConfigurationCleanup(dir, uid, gid, cleanup)
	if err != nil || !upgraded.valid() {
		t.Fatal("pending cleanup prevented the upgraded identity from being used", err)
	}
	state, err := readBrowserState(statePath, uid)
	if err != nil || state.Cleanup != legacy.Certificate || state.Next != "" {
		t.Fatal("failed cleanup was not retained", err)
	}
	if _, err := readBrowserConfiguration(filepath.Join(dir, browserConfigurationName), uid); err != nil {
		t.Fatal("pending cleanup disabled the running browser", err)
	}
	again, err := ensureBrowserConfigurationCleanup(dir, uid, gid, cleanup)
	if err != nil || again != upgraded || calls != 2 {
		t.Fatal("cleanup retry changed the identity or did not run", err, calls)
	}
	state, err = readBrowserState(statePath, uid)
	if err != nil || state.Cleanup != "" {
		t.Fatal("successful cleanup remained pending", err)
	}
	if _, err := ensureBrowserConfigurationCleanup(dir, uid, gid, cleanup); err != nil || calls != 2 {
		t.Fatal("completed cleanup was repeated", err, calls)
	}
}

func TestBrowserUpgradeResumesCleanupAfterTheIdentityIsCommitted(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	dir := upgradeDirectory(t)
	legacy, _ := legacyBrowserIdentity(t, dir, uid, gid)
	next, _, err := legacy.upgraded()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(next)
	if err := writeBrowserFile(filepath.Join(dir, browserConfigurationName), data, uid, gid); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(browserIdentityState{State: "ready", SHA256: access.Hash(string(data)), Cleanup: legacy.Certificate})
	if err := writeBrowserFile(filepath.Join(dir, browserStateName), state, uid, gid); err != nil {
		t.Fatal(err)
	}
	called := false
	got, err := ensureBrowserConfigurationCleanup(dir, uid, gid, func(certificate string) error {
		called = true
		if certificate != legacy.Certificate {
			t.Fatal("cleanup selected a different certificate")
		}
		return nil
	})
	if err != nil || !called || got != next {
		t.Fatal("committed identity lost its pending cleanup", err)
	}
}

func TestBrowserUpgradeRejectsInvalidCleanupState(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	for _, variant := range []string{"invalid", "another installation", "new address", "multiple certificates", "certificate authority", "additional address", "preparing"} {
		t.Run(variant, func(t *testing.T) {
			dir := upgradeDirectory(t)
			legacy, _ := legacyBrowserIdentity(t, dir, uid, gid)
			next, _, err := legacy.upgraded()
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(next)
			state := browserIdentityState{State: "ready", SHA256: access.Hash(string(data)), Cleanup: legacy.Certificate}
			switch variant {
			case "invalid":
				state.Cleanup = "invalid"
			case "another installation":
				other, _ := legacyBrowserIdentity(t, upgradeDirectory(t), uid, gid)
				state.Cleanup = other.Certificate
			case "new address":
				state.Cleanup = next.Certificate
			case "multiple certificates":
				state.Cleanup += legacy.Certificate
			case "certificate authority", "additional address":
				pair, err := tls.X509KeyPair([]byte(legacy.Certificate), []byte(legacy.Key))
				if err != nil {
					t.Fatal(err)
				}
				leaf, err := x509.ParseCertificate(pair.Certificate[0])
				if err != nil {
					t.Fatal(err)
				}
				if variant == "certificate authority" {
					leaf.IsCA = true
				} else {
					leaf.DNSNames = append(leaf.DNSNames, "other.localhost")
				}
				der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, leaf.PublicKey, pair.PrivateKey)
				if err != nil {
					t.Fatal(err)
				}
				state.Cleanup = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
			case "preparing":
				state.State, state.SHA256 = "preparing", ""
			}
			if err := writeBrowserFile(filepath.Join(dir, browserConfigurationName), data, uid, gid); err != nil {
				t.Fatal(err)
			}
			stored, _ := json.Marshal(state)
			if err := writeBrowserFile(filepath.Join(dir, browserStateName), stored, uid, gid); err != nil {
				t.Fatal(err)
			}
			called := false
			if _, err := ensureBrowserConfigurationCleanup(dir, uid, gid, func(string) error { called = true; return nil }); err == nil || called {
				t.Fatal("invalid cleanup state reached the certificate store", err)
			}
		})
	}
}

func TestBrowserUpgradeCanRemoveAnExpiredCertificate(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	dir := upgradeDirectory(t)
	legacy, _ := legacyBrowserIdentity(t, dir, uid, gid)
	pair, err := tls.X509KeyPair([]byte(legacy.Certificate), []byte(legacy.Key))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	leaf.NotBefore, leaf.NotAfter = time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, leaf.PublicKey, pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	data, _ := json.Marshal(legacy)
	if err := writeBrowserFile(filepath.Join(dir, browserConfigurationName), data, uid, gid); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(browserIdentityState{State: "ready", SHA256: access.Hash(string(data))})
	if err := writeBrowserFile(filepath.Join(dir, browserStateName), state, uid, gid); err != nil {
		t.Fatal(err)
	}
	if next, err := ensureBrowserUpgrade(dir, uid, gid); err != nil || !next.valid() {
		t.Fatal("an expired old certificate prevented the upgrade", err)
	}
}

// An owner whose passkey belongs to the old address keeps working Books after
// the upgrade: the service starts, and vult open sets up a new passkey for
// vult.localhost the same way a reset does.
func TestBrowserUpgradeLeadsTheOwnerToANewPasskey(t *testing.T) {
	db := testutil.New(t)
	c, err := newBrowserConfiguration(51996)
	if err != nil {
		t.Fatal(err)
	}
	oldOrigin := "https://vult-" + c.Installation + ".localhost:51996"
	tenant, err := db.Owner.EnsureTenant(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (access.Operator{DB: db.Admin}).InitializePasskeys(db.Context, tenant, localOwnerSubject, oldOrigin); err != nil {
		t.Fatal(err)
	}
	book, err := db.Owner.CreateBook(db.Context, "Example kept Book", "USD", 2)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Admin: db.Owner, Browser: &browserOwner{configuration: c}}
	if err := s.Browser.prepareIdentity(db.Context, db.Owner); err != nil {
		t.Fatal("the service could not start after its address moved", err)
	}
	if status, err := s.Browser.status(db.Context, db.Owner); err != nil || status.Ready || status.Origin != "https://vult.localhost:51996" {
		t.Fatal("the moved address was reported ready for the old passkey", status, err)
	}
	authRole, ownerRole := c.roles()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, role := range []string{authRole, ownerRole} {
			if _, err := db.Admin.Exec(ctx, "DROP OWNED BY "+pgx.Identifier{role}.Sanitize()); err != nil {
				t.Error("browser role grant cleanup failed")
			}
			if _, err := db.Admin.Exec(ctx, "DROP ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
				t.Error("browser role cleanup failed")
			}
		}
	})
	if err := prepareBrowserRoles(db.Context, db.Admin, db.Role, c); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	config := c.serverConfig()
	config.LocalCertificateFile, config.LocalKeyFile = filepath.Join(dir, "certificate.pem"), filepath.Join(dir, "key.pem")
	for path, contents := range map[string]string{config.LocalCertificateFile: c.Certificate, config.LocalKeyFile: c.Key} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for i, role := range []string{authRole, ownerRole} {
		credentials := db.Runtime.Config().Copy()
		credentials.ConnConfig.User, credentials.ConnConfig.Password = role, c.AuthPassword
		if i == 1 {
			credentials.ConnConfig.Password = c.OwnerPassword
		}
		pool, err := pgxpool.NewWithConfig(db.Context, credentials)
		if err != nil {
			t.Fatal(err)
		}
		dsn := testutil.DSN(pool)
		pool.Close()
		path := filepath.Join(dir, role+".dsn")
		if err := os.WriteFile(path, []byte(dsn), 0600); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			config.Authentication.AuthDSNFile = path
		} else {
			config.Authentication.OwnerDSNFile = path
		}
	}
	// The installer starts the browser before the owner can request enrollment.
	opened, err := server.OpenAccess(db.Context, config, db.Runtime, nil)
	if err != nil {
		t.Fatal("the upgraded browser could not start before owner enrollment", err)
	}
	defer opened.Close()
	input := marshal(struct{}{})
	q := local.Request{Method: "browser_setup", Payload: input}
	approval, err := s.authorize(0, marshal(local.Authorization{Action: q.Method, Digest: local.Digest(json.RawMessage(input)), UID: 501}))
	if err != nil {
		t.Fatal(err)
	}
	var grant struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(approval, &grant) != nil {
		t.Fatal("authorization fixture unavailable")
	}
	q.Authorization = grant.Token
	data, err := s.Handle(db.Context, 501, q)
	if err != nil {
		t.Fatal("vult open could not set up a passkey for the new address", err)
	}
	var enrollment local.BrowserSetup
	if json.Unmarshal(data, &enrollment) != nil || !strings.HasPrefix(enrollment.SetupURL, "https://vult.localhost:51996/auth/setup#setup=") {
		t.Fatal("the new passkey is not set up at vult.localhost", enrollment.SetupURL)
	}
	var origin string
	if err := db.Admin.QueryRow(db.Context, "SELECT origin FROM access.passkey_profiles").Scan(&origin); err != nil || origin != "https://vult.localhost:51996" {
		t.Fatal("the owner's passkey profile still points at the old address", origin, err)
	}
	var kept bool
	if err := db.Admin.QueryRow(db.Context, "SELECT EXISTS(SELECT 1 FROM public.books WHERE id=$1)", book.ID).Scan(&kept); err != nil || !kept {
		t.Fatal("the owner's Book was lost", err)
	}
}

func TestUpdateCheckIsOnlyOnMac(t *testing.T) {
	if c := updateChecker("darwin"); c == nil || c.Current != Version || c.Every <= 0 {
		t.Fatal("Vult on a Mac does not check for updates", c)
	}
	if updateChecker("linux") != nil {
		t.Fatal("the loopback-only Ubuntu service tries to reach the network")
	}
}
