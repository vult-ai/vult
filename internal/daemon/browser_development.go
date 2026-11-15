//go:build devweb

package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/internal/update"
	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
	"github.com/vult-ai/vult/web"
)

// ServeDevelopmentBrowser runs a development Vult with the same composition as
// an installed one: the browser service, and the native service that holds the
// documents and checks the owner's session for Books, documents, agents and
// recovery. On a Mac those are two processes joined by a privileged socket;
// here the native service runs in this process, on a private socket in dir,
// with this account as the browser's. dir holds the server configuration, the
// database connections, the document store and its key.
func ServeDevelopmentBrowser(ctx context.Context, dir string) error {
	app := web.App()
	if err := browserInterface(app); err != nil {
		return err
	}
	data, err := server.ReadPrivateFile(filepath.Join(dir, "config.json"), 8192)
	if err != nil {
		return server.ErrConfiguration
	}
	var c server.Config
	if strictjson.Decode(data, &c) != nil || !c.LocalBrowser {
		return server.ErrConfiguration
	}
	// The native service holds the documents, as on a Mac.
	c.Documents = nil
	// What this release sets aside stays out of reach.
	c.SetAside = true
	if _, err = server.New(c, server.Options{}); err != nil {
		return err
	}
	db, err := server.OpenDatabase(ctx, filepath.Join(dir, "runtime.dsn"))
	if err != nil {
		return err
	}
	defer db.Close()
	// The native service's own connection, as on a Mac: privileged, so the
	// browser's restricted opener doesn't take it.
	adminDSN, err := server.ReadPrivateFile(filepath.Join(dir, "admin.dsn"), 4096)
	if err != nil {
		return server.ErrConfiguration
	}
	admin, err := pgxpool.New(ctx, strings.TrimSpace(string(adminDSN)))
	if err != nil {
		return errors.New("development native database unavailable")
	}
	defer admin.Close()
	disk, err := documents.OpenDisk(filepath.Join(dir, "documents"))
	if err != nil {
		return err
	}
	keys, err := documents.OpenKeyring(filepath.Join(dir, "documents-key.json"))
	if err != nil {
		return err
	}
	uid := uint32(os.Getuid())
	reader, err := access.NewOwnerDocumentReader(ctx, admin, c.Origin)
	if err != nil {
		return errors.New("development document access unavailable")
	}
	native := &Server{Admin: ledger.Admin{DB: admin}, Ledger: ledger.Store{Runtime: db, Documents: &documents.Vault{Storage: disk, Keys: keys}},
		Browser: &browserOwner{uid: uid, documents: reader, developmentOrigin: c.Origin}}
	socket := filepath.Join(dir, "native.sock")
	if err = os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return err
	}
	if err = os.Chmod(socket, 0o600); err != nil {
		listener.Close()
		return err
	}
	go func() { _ = native.Serve(ctx, listener) }()
	// Development has no update to install: it reports what an installed Vult
	// shows when it is up to date.
	updates := &update.Checker{Current: Version, URL: "https://updates.invalid/manifest.json", Every: time.Hour,
		Client: &http.Client{Transport: currentRelease{}}}
	// A backups folder in dir stands in for the installer's, so the backup
	// screens can be seen; nothing here acts on a restore request.
	var updater server.Updater
	if info, err := os.Stat(filepath.Join(dir, "backups")); err == nil && info.IsDir() {
		updater = developmentBackups{filepath.Join(dir, "backups")}
	}
	return serveBrowser(ctx, c, db, local.Client{Socket: socket, ExpectedUID: uid}, app, updates, updater)
}

// developmentBackups serves backups from a folder of synthetic records and
// installs nothing.
type developmentBackups struct{ dir string }

func (d developmentBackups) Report() updatestatus.Report { return updatestatus.Report{} }
func (d developmentBackups) Request(string) error        { return errors.New("development installs nothing") }
func (d developmentBackups) LatestBackup() (*updatestatus.Backup, error) {
	return updatestatus.ReadBackups(d.dir)
}
func (d developmentBackups) NoticeSeen() string { return updatestatus.NoticeSeen(d.dir) }
func (d developmentBackups) MarkNoticeSeen(b string) error {
	return updatestatus.MarkNoticeSeen(d.dir, b)
}
func (d developmentBackups) RequestRestore(b, job string) error {
	return updatestatus.WriteRestoreRequest(d.dir, b, job, time.Now())
}
func (d developmentBackups) RestoreStatus() (updatestatus.RestoreStatus, bool) {
	return updatestatus.ReadRestoreStatus(d.dir)
}

// currentRelease answers the release check with this build's own version.
type currentRelease struct{}

func (currentRelease) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{"version":"` + Version + `"}`
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
}
