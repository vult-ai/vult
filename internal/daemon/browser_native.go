package daemon

import (
	"context"
	"errors"
	"github.com/vult-ai/vult/agentconfig"
	"github.com/vult-ai/vult/agentservice"
	"github.com/vult-ai/vult/internal/update"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/doccheck"
	"github.com/vult-ai/vult/internal/fssecurity"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
	"github.com/vult-ai/vult/web"
)

func browserInterface(files fs.FS) error {
	if files == nil {
		return errors.New("local browser requires the bundled interface")
	}
	info, err := fs.Stat(files, "index.html")
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 1<<20 {
		return errors.New("local browser interface is unavailable")
	}
	return nil
}

func checkNativeBrowserInterface() error { return browserInterface(web.App()) }

func browserIdentity() (uint32, uint32, error) {
	u, err := user.Lookup(local.BrowserUser)
	if err != nil {
		return 0, 0, errors.New("dedicated browser identity unavailable")
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil || uid == 0 {
		return 0, 0, errors.New("invalid browser identity")
	}
	gid, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil || gid == 0 {
		return 0, 0, errors.New("invalid browser group")
	}
	ledgerUID, _, err := serviceIdentity()
	if err != nil || uint32(uid) == ledgerUID {
		return 0, 0, errors.New("browser and operator identities must be separate")
	}
	marker := filepath.Join(filepath.Dir(local.DataPath), "browser-installation")
	if err := protected(marker, 0, false); err != nil {
		return 0, 0, err
	}
	data, err := os.ReadFile(marker)
	expected := "vult-browser-v1 " + u.Uid + " " + u.Gid
	if err != nil || len(data) > 128 || strings.TrimSpace(string(data)) != expected {
		return 0, 0, errors.New("browser installation identity changed")
	}
	return uint32(uid), uint32(gid), nil
}

// InstallNativeBrowser prepares restricted credentials and the public browser
// identity. Certificate trust is approved separately in the owner's GUI session.
func InstallNativeBrowser(ctx context.Context) error {
	if !nativeSupported() || os.Geteuid() != 0 {
		return errors.New("native administrator installation required")
	}
	if err := nativeStartupReady(); err != nil {
		return err
	}
	if err := checkNativeBrowserInterface(); err != nil {
		return err
	}
	if err := installationCode(); err != nil {
		return err
	}
	uid, _, err := serviceIdentity()
	if err != nil {
		return err
	}
	if err = installationData(uid); err != nil {
		return err
	}
	browserUID, browserGID, err := browserIdentity()
	if err != nil {
		return err
	}
	if err = protected(local.BrowserPath, 0, true); err != nil {
		return err
	}
	info, err := os.Stat(local.BrowserPath)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Gid != browserGID || info.Mode().Perm() != 0750 {
		return errors.New("protected browser directory required")
	}
	if err = prepareBrowserLog(filepath.Join(filepath.Dir(local.BrowserPath), "browser.log"), browserUID, browserGID); err != nil {
		return err
	}
	if err = prepareBrowserKeys(local.BrowserPath, browserUID, browserGID); err != nil {
		return err
	}
	c, err := readBrowserConfiguration(filepath.Join(local.DataPath, browserConfigurationName), uid)
	if err != nil {
		return err
	}
	native, err := loadConfiguration(uid)
	if err != nil {
		return err
	}
	admin, err := pool(ctx, "vult_owner", native.AdminPassword, "vult")
	if err != nil {
		return err
	}
	defer admin.Close()
	if err = prepareBrowserRoles(ctx, admin, "vult_runtime", c); err != nil {
		return errors.New("restricted browser roles unavailable")
	}
	if err := (&browserOwner{configuration: c}).prepareIdentity(ctx, ledger.Admin{DB: admin}); err != nil {
		return err
	}
	auth, owner := c.roles()
	for role, password := range map[string]string{auth: c.AuthPassword, owner: c.OwnerPassword} {
		p, err := pool(ctx, role, password, "vult")
		if err != nil {
			return errors.New("stored browser credentials need recovery")
		}
		p.Close()
	}
	for name, data := range c.files(native) {
		if err = writeBrowserFile(filepath.Join(local.BrowserPath, name), data, browserUID, browserGID); err != nil {
			return errors.New("protected browser files unavailable")
		}
	}
	data, err := browserTrustRecord(c)
	if err != nil {
		return err
	}
	if err := ensureBrowserCertificateStored(ctx, c); err != nil {
		return err
	}
	return writeBrowserFileMode(browserTrustIdentity, data, 0, 0, 0644)
}

// The parent is root-controlled. Launchd opens stderr as the browser account,
// which cannot create files there; an existing diagnostic must retain its bytes.
func prepareBrowserLog(path string, uid, gid uint32) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	created := err == nil
	if os.IsExist(err) {
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	}
	if err != nil {
		return errors.New("protected browser diagnostic unavailable")
	}
	defer f.Close()
	if created {
		if err = f.Chown(int(uid), int(gid)); err != nil {
			return errors.New("protected browser diagnostic ownership unavailable")
		}
	}
	info, err := f.Stat()
	if err != nil {
		return errors.New("protected browser diagnostic unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || stat.Uid != uid || stat.Gid != gid || info.Mode().Perm() != 0600 || !fssecurity.NoExtendedACL(f) {
		return errors.New("protected browser diagnostic has unsafe permissions")
	}
	return nil
}

func ServeNativeBrowser(ctx context.Context) error {
	if !nativeSupported() {
		return errors.New("native browser requires a supported installation")
	}
	if err := nativeStartupReady(); err != nil {
		return err
	}
	if err := updateStartupReady(); err != nil {
		return err
	}
	app := web.App()
	if err := browserInterface(app); err != nil {
		return err
	}
	uid, _, err := browserIdentity()
	if err != nil || uint32(os.Geteuid()) != uid {
		return errors.New("browser must run under its restricted account")
	}
	if err = installationCode(); err != nil {
		return err
	}
	if err = protected(local.BrowserPath, 0, true); err != nil {
		return err
	}
	// The installer makes the keys folder; one that exists but isn't this
	// account's alone is never used. A missing one only stops keys from being
	// saved, with a message saying so, until Vult is installed again.
	if err = checkBrowserKeys(local.BrowserPath, uid); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err != nil {
		log.Printf("bank: %v; reinstalling Vult sets it up", err)
	}
	data, err := server.ReadPrivateFile(filepath.Join(local.BrowserPath, "server.json"), 8192)
	if err != nil {
		return server.ErrConfiguration
	}
	var c server.Config
	if strictjson.Decode(data, &c) != nil || !c.LocalBrowser {
		return server.ErrConfiguration
	}
	// What this release sets aside stays out of reach.
	c.SetAside = true
	if _, err = server.New(c, server.Options{}); err != nil {
		return err
	}
	db, err := server.OpenDatabase(ctx, filepath.Join(local.BrowserPath, "runtime.dsn"))
	if err != nil {
		return err
	}
	defer db.Close()
	client, err := local.NewClient()
	if err != nil {
		return err
	}
	return serveBrowser(ctx, c, db, client, app, updateChecker(runtime.GOOS), installer(runtime.GOOS))
}

// serveBrowser is the browser service an owner signs in to: the same
// composition for an installed Vult and a development one. client reaches the
// native service, which holds the documents and checks the owner's session.
func serveBrowser(ctx context.Context, c server.Config, db *pgxpool.Pool, client local.Client, app fs.FS, updates *update.Checker, updater server.Updater) error {
	a, err := server.OpenAccess(ctx, c, db, nil, localAgents{client})
	if err != nil {
		return err
	}
	defer a.Close()
	if c.Banks == nil && runtime.GOOS == "darwin" {
		// Vult on a Mac keeps the key that seals bank access in the browser
		// service's own keys folder, sealed by the Mac's Secure Enclave.
		c.Banks = &server.BankConnections{KeyFolder: browserKeyFolder(local.BrowserPath)}
	}
	// New bank activity is sorted as it arrives, once an agent is there.
	var keepUp func(tenant, book string)
	banks, err := server.OpenBanks(c, ledger.Store{Runtime: db}, func(tenant, book string) {
		if keepUp != nil {
			keepUp(tenant, book)
		}
	})
	if err != nil {
		return err
	}
	agent := server.OpenAgent(ledger.Store{Runtime: db})
	// An agent service, when one is set, answers what the owner writes.
	var chat server.AgentChat
	if agent != nil {
		// With an agent service set, the owner's messages go to it and it is
		// offered as this Vult's agent, on or off in each Book.
		agent.SignInURL = accountSignInFromEnvironment()
		if service := agentservice.FromEnvironment(); service != nil {
			agent.Provided = &agentconfig.Provided{Name: "Vult", About: "Vult does your bookkeeping. It prepares changes; you approve them."}
			talk := agentservice.NewClient(agentservice.Config{Service: service, Agents: agent, Store: ledger.Store{Runtime: db}, Conversations: keptConversations()})
			chat, keepUp = talk, talk.KeepUp
		}
	}
	var arrived func(context.Context, ledger.AgentTarget, server.OwnerAct)
	if keepUp != nil {
		arrived = func(_ context.Context, t ledger.AgentTarget, _ server.OwnerAct) { keepUp(t.TenantID, t.BookID) }
	}
	s, err := server.New(c, server.Options{
		ActivityArrived: arrived,
		// This program has no assistant of its own to help read documents.
		DocumentAdded: doccheck.Hook(nil),
		Database:      db, Access: a, Web: app,
		ReadDocument: client.DownloadForOwner, UploadDocument: client.UploadForOwner,
		LocalBooks: localBooks{client},
		Updates:    updates,
		// Set only when this build has an update installer.
		Updater:   updater,
		Banks:     banks,
		Agent:     agent,
		AgentChat: chat,
	})
	if err != nil {
		return err
	}
	if banks != nil {
		go banks.Run(ctx)
	}
	l, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return server.ErrUnavailable
	}
	return server.Serve(ctx, s, l)
}

// updateChecker supports Mac installations without the privileged installer.
// With an installer, discovery runs in the helper. The Ubuntu browser service
// may reach only loopback addresses and never checks the network itself.
func updateChecker(goos string) *update.Checker {
	if installer(goos) != nil {
		return nil // This installation discovers verified releases through its helper.
	}
	if goos != "darwin" {
		return nil
	}
	return &update.Checker{Current: Version, URL: update.ManifestURL, Client: &http.Client{Timeout: 10 * time.Second}, Every: 12 * time.Hour}
}
