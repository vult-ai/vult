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
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

func TestNativeBrowserFailureLeavesLedgerSocketOperationsAvailable(t *testing.T) {
	db := testutil.New(t)
	book, err := db.Owner.CreateBook(db.Context, "Example retained Book", "USD", 2)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := db.Owner.CreateConnection(db.Context, book.ID, "Example retained agent", "read")
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"missing", "expired", "changed"} {
		t.Run(variant, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if variant != "missing" {
				c, err := newBrowserConfiguration(51999)
				if err != nil {
					t.Fatal(err)
				}
				if variant == "expired" {
					pair, err := tls.X509KeyPair([]byte(c.Certificate), []byte(c.Key))
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
					c.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
				}
				data, _ := json.Marshal(c)
				if variant == "changed" {
					data = []byte("invalid")
				}
				if err := createBrowserPrivate(filepath.Join(dir, browserConfigurationName), data, uint32(os.Geteuid()), uint32(os.Getegid())); err != nil {
					t.Fatal(err)
				}
				state, _ := json.Marshal(browserIdentityState{State: "ready", SHA256: access.Hash(string(data))})
				if err := createBrowserPrivate(filepath.Join(dir, browserStateName), state, uint32(os.Geteuid()), uint32(os.Getegid())); err != nil {
					t.Fatal(err)
				}
			}
			browser := loadBrowserOwner(dir, uint32(os.Geteuid()))
			if browser != nil {
				t.Fatal("invalid browser identity was enabled")
			}
			scope := ledger.Scope{TenantID: connection.TenantID, BookID: book.ID, ActorID: connection.ActorID, ConnectionID: connection.ID, Access: "read"}
			s := &Server{Admin: db.Owner, Ledger: db.Store, Browser: browser, sessions: map[string]session{tokenKey("synthetic-session"): {UID: 501, Scope: scope, Expires: time.Now().Add(time.Hour)}}}
			data, err := s.Handle(db.Context, 501, local.Request{Method: "get_book", Authorization: "synthetic-session", Payload: marshal(struct{}{})})
			var got ledger.Book
			if err != nil || json.Unmarshal(data, &got) != nil || got.ID != book.ID {
				t.Fatal("browser failure disabled retained native agent access", err)
			}
			if _, err := s.Handle(db.Context, 501, local.Request{Method: "browser_status", Payload: marshal(struct{}{})}); !errors.Is(err, ledger.ErrDenied) {
				t.Fatal("unavailable browser did not fail closed", err)
			}
		})
	}
}

func TestNativeBrowserRequiresBundledInterface(t *testing.T) {
	for _, files := range []fstest.MapFS{nil, {}, {"index.html": {Data: []byte{}}}} {
		if err := browserInterface(files); err == nil {
			t.Fatal("missing interface allowed a native browser installation")
		}
	}
	if err := browserInterface(nil); err == nil {
		t.Fatal("nil interface allowed a native browser installation")
	}
	if err := browserInterface(fstest.MapFS{"index.html": {Data: []byte("<!doctype html><div id=root></div>")}}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeBrowserMissingOrChangedIdentityRequiresRecovery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	c, err := ensureBrowserConfiguration(dir, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, browserConfigurationName)
	retained := fixtureBytes(t, path)
	state := fixtureBytes(t, filepath.Join(dir, browserStateName))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureBrowserConfiguration(dir, uid, gid); err == nil {
		t.Fatal("missing durable browser identity was replaced")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("missing identity was recreated", err)
	}
	if !reflect.DeepEqual(state, fixtureBytes(t, filepath.Join(dir, browserStateName))) {
		t.Fatal("ready identity marker changed after loss")
	}
	if err := createBrowserPrivate(path, retained, uid, gid); err != nil {
		t.Fatal(err)
	}
	again, err := ensureBrowserConfiguration(dir, uid, gid)
	if err != nil || !reflect.DeepEqual(c, again) {
		t.Fatal("restored identity was not retained", err)
	}
	changed, err := newBrowserConfiguration(51998)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(changed)
	if err := writeBrowserFile(path, data, uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureBrowserConfiguration(dir, uid, gid); err == nil {
		t.Fatal("a different browser identity replaced the committed one")
	}
}

func TestNativeBrowserResumesOnlyUnfinishedInitialConfiguration(t *testing.T) {
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	for _, completeFile := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "data")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		state, _ := json.Marshal(browserIdentityState{State: "preparing"})
		if err := createBrowserPrivate(filepath.Join(dir, browserStateName), state, uid, gid); err != nil {
			t.Fatal(err)
		}
		var expected browserConfiguration
		if completeFile {
			var err error
			expected, err = newBrowserConfiguration(51999)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(expected)
			if err := createBrowserPrivate(filepath.Join(dir, browserConfigurationName), data, uid, gid); err != nil {
				t.Fatal(err)
			}
		}
		c, err := ensureBrowserConfiguration(dir, uid, gid)
		if err != nil || completeFile && !reflect.DeepEqual(expected, c) {
			t.Fatal("unfinished first setup did not retain its written identity", err)
		}
		if _, err := readBrowserConfiguration(filepath.Join(dir, browserConfigurationName), uid); err != nil {
			t.Fatal("initial identity was not made durable", err)
		}
	}
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(root, "browser", "key.pem"), []byte("retained synthetic key"), 0400)
	if _, err := ensureBrowserConfiguration(dir, uid, gid); err == nil {
		t.Fatal("orphaned browser files caused a new identity")
	}
}

func TestNativeBrowserConfigurationPreservesIdentityAndExcludesOperatorSecrets(t *testing.T) {
	dir := t.TempDir()
	uid, gid := uint32(os.Geteuid()), uint32(os.Getegid())
	c, err := ensureBrowserConfiguration(dir, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ensureBrowserConfiguration(dir, uid, gid)
	if err != nil || !reflect.DeepEqual(c, again) {
		t.Fatal("local browser identity changed on reinstall", err)
	}
	native := configuration{AdminPassword: secret(), RuntimePassword: secret(), RecoveryKey: secret()}
	for name, data := range c.files(native) {
		if strings.Contains(string(data), native.AdminPassword) || strings.Contains(string(data), native.RecoveryKey) {
			t.Fatal("browser received operator or backup authority", name)
		}
	}
	path := filepath.Join(dir, browserConfigurationName)
	for _, mode := range []os.FileMode{0600, 0444} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := readBrowserConfiguration(path, uid); err == nil {
			t.Fatal("mutable or shared identity configuration accepted")
		}
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, filepath.Join(dir, "copy")); err != nil {
		t.Fatal(err)
	}
	if _, err := readBrowserConfiguration(path, uid); err == nil {
		t.Fatal("multiply-linked configuration accepted")
	}
	if err := os.Remove(filepath.Join(dir, "copy")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(dir, "retained")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "retained"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureBrowserConfiguration(dir, uid, gid); err == nil {
		t.Fatal("symlinked identity was followed or reinitialized")
	}
}

func TestNativeBrowserRolesAndAdministratorSetupPreserveExistingBooks(t *testing.T) {
	db := testutil.New(t)
	c, err := newBrowserConfiguration(51999)
	if err != nil {
		t.Fatal(err)
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
	if err := prepareBrowserRoles(db.Context, db.Admin, db.Role, c); err != nil {
		t.Fatal("reinstall changed the role contract", err)
	}
	var files []string
	dir := t.TempDir()
	for i, role := range []string{authRole, ownerRole} {
		password := c.AuthPassword
		if i == 1 {
			password = c.OwnerPassword
		}
		config := db.Admin.Config().Copy()
		config.ConnConfig.User, config.ConnConfig.Password = role, password
		p, err := pgxpool.NewWithConfig(db.Context, config)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Ping(db.Context); err != nil {
			p.Close()
			t.Fatal("restricted browser LOGIN unavailable", err)
		}
		path := filepath.Join(dir, role+".dsn")
		if err := os.WriteFile(path, []byte(testutil.DSN(p)), 0600); err != nil {
			p.Close()
			t.Fatal(err)
		}
		p.Close()
		files = append(files, path)
	}
	book, err := db.Owner.CreateBook(db.Context, "Example existing Book", "USD", 2)
	if err != nil {
		t.Fatal(err)
	}
	before, err := db.Owner.Snapshot(db.Context)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Admin: db.Owner, Browser: &browserOwner{configuration: c}}
	if err := s.Browser.prepareIdentity(db.Context, db.Owner); err != nil {
		t.Fatal("native installer could not prepare its fixed owner", err)
	}
	input := marshal(struct{}{})
	q := local.Request{Method: "browser_setup", Payload: input}
	if _, err := s.Handle(db.Context, 501, q); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("local browser enrollment bypassed native approval", err)
	}
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
		t.Fatal(err)
	}
	var enrollment local.BrowserSetup
	if json.Unmarshal(data, &enrollment) != nil || enrollment.Origin != c.origin() || !strings.HasPrefix(enrollment.SetupURL, c.origin()+"/auth/setup#setup=") {
		t.Fatal("local enrollment omitted its bounded setup result")
	}
	// Recovery codes and the backup key wait for the owner's browser; the
	// terminal never sees them.
	if strings.Contains(string(data), "recovery") || s.recovery == nil {
		t.Fatal("local enrollment returned its recovery codes to the terminal")
	}
	if _, err := s.Handle(db.Context, 501, q); !errors.Is(err, ledger.ErrDenied) {
		t.Fatal("native browser approval was reusable", err)
	}
	config := c.serverConfig()
	config.Authentication.AuthDSNFile, config.Authentication.OwnerDSNFile = files[0], files[1]
	// Network files are checked separately from the operator's retained identity.
	cert, key := filepath.Join(dir, "certificate.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, []byte(c.Certificate), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte(c.Key), 0600); err != nil {
		t.Fatal(err)
	}
	config.LocalCertificateFile, config.LocalKeyFile = cert, key
	a, err := server.OpenAccess(db.Context, config, db.Runtime, nil)
	if err != nil {
		t.Fatal("prepared local browser roles could not start authentication", err)
	}
	defer a.Close()
	raw, err := a.Sessions.IssueSession(db.Context, access.Identity{Issuer: access.BuiltinIssuer, Subject: localOwnerSubject, AuthenticatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Sessions.WithOwner(db.Context, raw, book.TenantID, false, func(owner ledger.TenantAdmin) error {
		got, err := owner.Book(db.Context, book.ID)
		if err == nil && got.ID != book.ID {
			t.Fatal("native browser bound a new Book")
		}
		return err
	}); err != nil {
		t.Fatal("existing Book was not owned by the prepared browser identity", err)
	}
	after, err := db.Owner.Snapshot(db.Context)
	if err != nil || !reflect.DeepEqual(before.Tables, after.Tables) {
		t.Fatal("browser bootstrap changed financial rows", err)
	}
	if _, err := db.Admin.Exec(db.Context, "ALTER ROLE "+pgx.Identifier{authRole}.Sanitize()+" BYPASSRLS"); err != nil {
		t.Fatal(err)
	}
	if err := prepareBrowserRoles(db.Context, db.Admin, db.Role, c); !errors.Is(err, access.ErrDenied) {
		t.Fatal("unsafe retained role was adopted", err)
	}
}
