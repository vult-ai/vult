package daemon

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/fssecurity"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/loopback"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

const localOwnerSubject = "local-owner"
const browserConfigurationName = "browser.json"
const browserStateName = "browser-state.json"

type browserIdentityState struct {
	State  string `json:"state"`
	SHA256 string `json:"sha256,omitempty"`
	// Next is the identity an interrupted upgrade was moving to; either it or
	// SHA256 may be the stored identity until the upgrade completes.
	Next string `json:"next,omitempty"`
	// Cleanup retains the old public certificate until its system trust and
	// keychain entry have been removed. It never contains a private key.
	Cleanup string `json:"cleanup,omitempty"`
}

// legacyBrowserCertificate accepts an old public leaf even after it expires:
// expiry must not prevent removing its obsolete trust settings.
func legacyBrowserCertificate(data string) (*x509.Certificate, error) {
	block, rest := pem.Decode([]byte(data))
	if len(data) > 32768 || block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ledger.ErrInvalid
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil || len(leaf.DNSNames) != 1 || leaf.IsCA || !leaf.BasicConstraintsValid || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 || !bytes.Equal(leaf.RawIssuer, leaf.RawSubject) {
		return nil, ledger.ErrInvalid
	}
	key, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() || leaf.KeyUsage != x509.KeyUsageDigitalSignature || len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || len(leaf.UnknownExtKeyUsage) != 0 {
		return nil, ledger.ErrInvalid
	}
	if _, ok := loopback.LegacyInstallationID(leaf.DNSNames[0]); !ok || leaf.Subject.CommonName != leaf.DNSNames[0] || leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) != nil {
		return nil, ledger.ErrInvalid
	}
	return leaf, nil
}

func (s browserIdentityState) cleanupMatches(c browserConfiguration) bool {
	if s.Cleanup == "" {
		return true
	}
	leaf, err := legacyBrowserCertificate(s.Cleanup)
	if err != nil {
		return false
	}
	id := c.Installation
	if c.Format == legacyBrowserFormat {
		id, _ = loopback.LegacyInstallationID(c.Hostname)
	}
	return leaf.DNSNames[0] == "vult-"+id+".localhost"
}

// matches reports whether data is the identity this state records.
func (s browserIdentityState) matches(data []byte) bool {
	sum := access.Hash(string(data))
	return s.State == "ready" && (sum == s.SHA256 || s.Next != "" && sum == s.Next)
}

const browserFormat = "vult-local-browser-v2"

// Before 0.5.1 the address was vult-<installation>.localhost and carried the
// installation identity; this format is read only to upgrade it.
const legacyBrowserFormat = "vult-local-browser-v1"

type browserConfiguration struct {
	Format        string `json:"format"`
	Installation  string `json:"installation,omitempty"`
	Hostname      string `json:"hostname"`
	Port          int    `json:"port"`
	Certificate   string `json:"certificate"`
	Key           string `json:"key"`
	AuthPassword  string `json:"auth_password"`
	OwnerPassword string `json:"owner_password"`
}

func newBrowserConfiguration(port int) (browserConfiguration, error) {
	if port < 1024 || port > 65535 {
		return browserConfiguration{}, ledger.ErrInvalid
	}
	id, err := loopback.InstallationID()
	if err != nil {
		return browserConfiguration{}, err
	}
	certificate, key, err := loopback.NewCertificate(loopback.Host)
	if err != nil {
		return browserConfiguration{}, err
	}
	return browserConfiguration{Format: browserFormat, Installation: id, Hostname: loopback.Host, Port: port, Certificate: string(certificate), Key: string(key), AuthPassword: secret(), OwnerPassword: secret()}, nil
}

// upgraded moves a pre-0.5.1 identity to Host. The installation identity, port
// and database logins stay; only the address and its certificate change, so
// the owner sets up a passkey for the new address once.
func (c browserConfiguration) upgraded() (browserConfiguration, bool, error) {
	if c.Format != legacyBrowserFormat {
		return c, false, nil
	}
	id, ok := loopback.LegacyInstallationID(c.Hostname)
	if !ok {
		return c, false, errors.New("local browser identity needs recovery")
	}
	certificate, key, err := loopback.NewCertificate(loopback.Host)
	if err != nil {
		return c, false, err
	}
	next := c
	next.Format, next.Installation, next.Hostname = browserFormat, id, loopback.Host
	next.Certificate, next.Key = string(certificate), string(key)
	return next, true, nil
}

// legacyValid checks a pre-0.5.1 identity well enough to upgrade it.
func (c browserConfiguration) legacyValid() bool {
	if c.Format != legacyBrowserFormat || c.Installation != "" || c.Port < 1024 || c.Port > 65535 || c.AuthPassword == c.OwnerPassword {
		return false
	}
	_, ok := loopback.LegacyInstallationID(c.Hostname)
	if !ok || !validBrowserPassword(c.AuthPassword) || !validBrowserPassword(c.OwnerPassword) {
		return false
	}
	leaf, err := legacyBrowserCertificate(c.Certificate)
	if err != nil || leaf.DNSNames[0] != c.Hostname {
		return false
	}
	pair, err := tls.X509KeyPair([]byte(c.Certificate), []byte(c.Key))
	return err == nil && len(pair.Certificate) == 1
}

func validBrowserPassword(password string) bool {
	b, err := base64.RawURLEncoding.Strict().DecodeString(password)
	return err == nil && len(b) == 32 && len(password) == 43
}

func (c browserConfiguration) valid() bool {
	if c.Format != browserFormat || !loopback.ValidInstallationID(c.Installation) || !loopback.ValidHostname(c.Hostname) || c.Port < 1024 || c.Port > 65535 {
		return false
	}
	if !validBrowserPassword(c.AuthPassword) || !validBrowserPassword(c.OwnerPassword) {
		return false
	}
	_, err := loopback.ParseCertificate(c.Hostname, []byte(c.Certificate), []byte(c.Key))
	return err == nil && c.AuthPassword != c.OwnerPassword
}

func readBrowserConfiguration(path string, uid uint32) (browserConfiguration, error) {
	var c browserConfiguration
	data, err := readBrowserPrivate(path, uid, 32768)
	if err != nil || strictjson.Decode(data, &c) != nil || !c.valid() {
		return browserConfiguration{}, errors.New("local browser identity needs recovery")
	}
	state, err := readBrowserState(filepath.Join(filepath.Dir(path), browserStateName), uid)
	if err != nil || !state.matches(data) || !state.cleanupMatches(c) {
		return browserConfiguration{}, errors.New("local browser identity needs recovery")
	}
	return c, nil
}

func readBrowserPrivate(path string, uid uint32, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, ledger.ErrInvalid
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != uid || stat.Nlink != 1 || info.Mode().Perm() != 0400 || info.Size() > limit || !fssecurity.NoExtendedACL(f) {
		return nil, ledger.ErrInvalid
	}
	data := make([]byte, info.Size())
	if _, err := f.ReadAt(data, 0); err != nil {
		return nil, ledger.ErrInvalid
	}
	return data, nil
}

func readBrowserState(path string, uid uint32) (browserIdentityState, error) {
	data, err := readBrowserPrivate(path, uid, 65536)
	if err != nil {
		return browserIdentityState{}, err
	}
	var state browserIdentityState
	if strictjson.Decode(data, &state) != nil || state.State != "preparing" && state.State != "ready" {
		return browserIdentityState{}, ledger.ErrInvalid
	}
	if state.Next != "" {
		decoded, err := hex.DecodeString(state.Next)
		if err != nil || len(decoded) != 32 || strings.ToLower(state.Next) != state.Next || state.State != "ready" {
			return browserIdentityState{}, ledger.ErrInvalid
		}
	}
	if state.Cleanup != "" {
		if _, err := legacyBrowserCertificate(state.Cleanup); err != nil || state.State != "ready" {
			return browserIdentityState{}, ledger.ErrInvalid
		}
	}
	if state.State == "preparing" {
		if state.SHA256 != "" {
			return browserIdentityState{}, ledger.ErrInvalid
		}
		return state, nil
	}
	decoded, err := hex.DecodeString(state.SHA256)
	if err != nil || len(decoded) != 32 || strings.ToLower(state.SHA256) != state.SHA256 {
		return browserIdentityState{}, ledger.ErrInvalid
	}
	return state, nil
}

func createBrowserPrivate(path string, data []byte, uid, gid uint32) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0400)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = f.Chown(int(uid), int(gid)); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func writeBrowserFile(path string, data []byte, uid, gid uint32) error {
	return writeBrowserFileMode(path, data, uid, gid, 0400)
}

func writeBrowserFileMode(path string, data []byte, uid, gid uint32, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".browser-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err = f.Chown(int(uid), int(gid)); err != nil {
		return err
	}
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func ensureBrowserConfiguration(data string, uid, gid uint32) (browserConfiguration, error) {
	return ensureBrowserConfigurationCleanup(data, uid, gid, forgetBrowserCertificate)
}

func ensureBrowserConfigurationCleanup(data string, uid, gid uint32, cleanup func(string) error) (browserConfiguration, error) {
	path := filepath.Join(data, browserConfigurationName)
	statePath := filepath.Join(data, browserStateName)
	state, err := readBrowserState(statePath, uid)
	if err != nil && !os.IsNotExist(err) {
		return browserConfiguration{}, err
	}
	if b, err := readBrowserPrivate(path, uid, 32768); err == nil {
		var c browserConfiguration
		if strictjson.Decode(b, &c) != nil || !c.valid() && !c.legacyValid() || state.State == "ready" && !state.matches(b) || !state.cleanupMatches(c) {
			return browserConfiguration{}, errors.New("local browser identity needs recovery")
		}
		if next, moved, err := c.upgraded(); err != nil {
			return browserConfiguration{}, err
		} else if moved {
			return upgradeBrowserConfiguration(path, statePath, b, c, next, uid, gid, cleanup)
		}
		if state.State == "ready" {
			if state.Next != "" || state.Cleanup != "" {
				state.SHA256, state.Next = access.Hash(string(b)), ""
				return c, finishBrowserUpgrade(statePath, state, uid, gid, cleanup)
			}
			return c, nil
		}
		ready, _ := json.Marshal(browserIdentityState{State: "ready", SHA256: access.Hash(string(b))})
		return c, writeBrowserFile(statePath, ready, uid, gid)
	} else if !os.IsNotExist(err) {
		return browserConfiguration{}, err
	}
	if state.State == "ready" {
		return browserConfiguration{}, errors.New("stored browser identity is missing; recovery is required")
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(data), "browser"))
	if err != nil && !os.IsNotExist(err) {
		return browserConfiguration{}, err
	}
	if len(entries) > 0 {
		return browserConfiguration{}, errors.New("browser files exist without their identity; recovery is required")
	}
	if state.State == "" {
		preparing, _ := json.Marshal(browserIdentityState{State: "preparing"})
		if err := createBrowserPrivate(statePath, preparing, uid, gid); err != nil {
			return browserConfiguration{}, err
		}
	}
	// A port becomes part of the permanent WebAuthn origin. Occupied ports fail
	// closed later; an existing installation never silently chooses another one.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return browserConfiguration{}, errors.New("local browser port unavailable")
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err = l.Close(); err != nil {
		return browserConfiguration{}, err
	}
	c, err := newBrowserConfiguration(port)
	if err != nil {
		return c, err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if err = createBrowserPrivate(path, b, uid, gid); err != nil {
		return browserConfiguration{}, err
	}
	ready, _ := json.Marshal(browserIdentityState{State: "ready", SHA256: access.Hash(string(b))})
	return c, writeBrowserFile(statePath, ready, uid, gid)
}

func (c browserConfiguration) origin() string {
	return "https://" + net.JoinHostPort(c.Hostname, strconv.Itoa(c.Port))
}
func (c browserConfiguration) roles() (string, string) {
	id := c.Installation[:16]
	return "vult_browser_auth_" + id, "vult_browser_owner_" + id
}
func (c browserConfiguration) serverConfig() server.Config {
	return server.Config{Origin: c.origin(), Listen: net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port)), LocalBrowser: true,
		LocalCertificateFile: filepath.Join(local.BrowserPath, "certificate.pem"), LocalKeyFile: filepath.Join(local.BrowserPath, "key.pem"),
		Authentication: &server.Authentication{Mode: "passkey", Username: localOwnerSubject, AuthDSNFile: filepath.Join(local.BrowserPath, "auth.dsn"), OwnerDSNFile: filepath.Join(local.BrowserPath, "owner.dsn")}}
}

func browserDSN(role, password string) string {
	u := url.URL{Scheme: "postgresql", User: url.UserPassword(role, password), Path: "/vult"}
	q := url.Values{"host": {local.PostgresSocketPath}, "port": {"5432"}, "sslmode": {"disable"}}
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	return u.String()
}

func (c browserConfiguration) files(native configuration) map[string][]byte {
	auth, owner := c.roles()
	config, _ := json.Marshal(c.serverConfig())
	return map[string][]byte{"server.json": config, "certificate.pem": []byte(c.Certificate), "key.pem": []byte(c.Key),
		"runtime.dsn": []byte(browserDSN("vult_runtime", native.RuntimePassword)), "auth.dsn": []byte(browserDSN(auth, c.AuthPassword)), "owner.dsn": []byte(browserDSN(owner, c.OwnerPassword))}
}

func prepareBrowserRoles(ctx context.Context, admin *pgxpool.Pool, runtimeRole string, c browserConfiguration) error {
	if admin == nil || !c.valid() {
		return ledger.ErrInvalid
	}
	auth, owner := c.roles()
	for i, role := range []string{auth, owner} {
		var exists bool
		if err := admin.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)", role).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			password := c.AuthPassword
			if i == 1 {
				password = c.OwnerPassword
			}
			if _, err := admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD '"+password+"'"); err != nil {
				return err
			}
		}
	}
	return (access.Operator{DB: admin}).ConfigureRoles(ctx, runtimeRole, auth, owner)
}

// upgradeBrowserConfiguration replaces a pre-0.5.1 identity with its upgrade.
// The state first names both identities, so an upgrade interrupted at any
// point leaves one that is accepted; the old certificate leaves the system
// trust store once nothing refers to it.
func upgradeBrowserConfiguration(path, statePath string, current []byte, old, next browserConfiguration, uid, gid uint32, cleanup func(string) error) (browserConfiguration, error) {
	data, err := json.Marshal(next)
	if err != nil {
		return browserConfiguration{}, err
	}
	state := browserIdentityState{State: "ready", SHA256: access.Hash(string(current)), Next: access.Hash(string(data)), Cleanup: old.Certificate}
	moving, _ := json.Marshal(state)
	if err = writeBrowserFile(statePath, moving, uid, gid); err != nil {
		return browserConfiguration{}, err
	}
	if err = writeBrowserFile(path, data, uid, gid); err != nil {
		return browserConfiguration{}, err
	}
	state.SHA256, state.Next = access.Hash(string(data)), ""
	return next, finishBrowserUpgrade(statePath, state, uid, gid, cleanup)
}

// Cleanup is retried on the next installation attempt if the keychain is
// unavailable. The moved identity remains usable while cleanup is pending.
func finishBrowserUpgrade(statePath string, state browserIdentityState, uid, gid uint32, cleanup func(string) error) error {
	if state.Cleanup != "" && cleanup(state.Cleanup) == nil {
		state.Cleanup = ""
	}
	ready, _ := json.Marshal(state)
	return writeBrowserFile(statePath, ready, uid, gid)
}
