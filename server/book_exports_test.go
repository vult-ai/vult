package server

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

type exportedArchive struct {
	Format   string            `json:"format"`
	Snapshot ledger.Snapshot   `json:"snapshot"`
	Receipts map[string]string `json:"migrations"`
}

func readExportedArchive(t *testing.T, data []byte) (exportedArchive, map[string][]byte) {
	t.Helper()
	reader := tar.NewReader(bytes.NewReader(data))
	files := map[string][]byte{}
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name], err = io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
	}
	var manifest exportedArchive
	var footer struct {
		Hash string `json:"manifest_sha256"`
	}
	sum := sha256.Sum256(files["manifest.json"])
	if json.Unmarshal(files["manifest.json"], &manifest) != nil ||
		json.Unmarshal(files["complete.json"], &footer) != nil || footer.Hash != hex.EncodeToString(sum[:]) {
		t.Fatal("archive has no authentic completion record")
	}
	return manifest, files
}

func TestOwnerBookDownloadsShareScopeAndCLIFormats(t *testing.T) {
	staging := t.TempDir()
	t.Setenv("TMPDIR", staging)
	f, scope := browserDocuments(t)
	other, foreign := otherAPIOwner(t, f)
	ctx := f.db.Context
	account := func(code, name, class string) ledger.Account {
		t.Helper()
		var a ledger.Account
		if err := json.Unmarshal(f.db.Call(t, scope, "create_account", ledger.CreateAccount{
			RequestKey: code, Code: code, Name: name, Class: class,
		}), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	debit := account("1000", "+Example", "asset")
	credit := account("3000", "Example equity", "equity")
	debit2 := account("1010", "Example second asset", "asset")
	credit2 := account("3010", "Example second equity", "equity")
	debit3 := account("1020", "Example third asset", "asset")
	credit3 := account("3020", "Example third equity", "equity")
	const amount = "9007199254740993"
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "example-post", EconomicDate: "2026-01-01",
		AccountingDate: "2026-01-02", Description: "=Example entry", Lines: []ledger.PostLine{
			{AccountID: debit.ID, Side: "debit", AmountMinor: amount, Memo: "@Example memo"},
			{AccountID: credit.ID, Side: "credit", AmountMinor: amount},
		}})
	const maxMinor = "9223372036854775807"
	f.db.Call(t, scope, "post_journal", ledger.PostJournal{RequestKey: "example-large-total", EconomicDate: "2026-01-01",
		AccountingDate: "2026-01-02", Description: "Example large total", Lines: []ledger.PostLine{
			{AccountID: debit2.ID, Side: "debit", AmountMinor: maxMinor},
			{AccountID: debit3.ID, Side: "debit", AmountMinor: maxMinor},
			{AccountID: credit2.ID, Side: "credit", AmountMinor: maxMinor},
			{AccountID: credit3.ID, Side: "credit", AmountMinor: maxMinor},
		}})
	original := []byte("synthetic original")
	doc := originalForBrowser(t, f, scope, "synthetic", original, true)
	identity := fmt.Sprintf(`{"book_id":%q,"revision":0,"identity":{"legal_name":"Example entity"},"reason":"Example identity","idempotency_key":"example-identity"}`, f.book.ID)
	address := fmt.Sprintf(`{"book_id":%q,"revision":0,"slug":"example-address"}`, f.book.ID)
	ownerAPIStatus(t, ownerAPICall(f.server, f.session, http.MethodPost, bookIdentityPath, identity, true), 200)
	ownerAPIStatus(t, ownerAPICall(f.server, f.session, http.MethodPost, bookAddressPath, address, true), 200)
	logo, _ := exampleJPEG(t, 64, false)
	logoReceipt := decodeOwnerResult[bookLogoReceipt](t, putOwnerLogo(f.server, f.session, f.book.ID, logo))

	ownerAPIStatus(t, ownerAPICall(f.server, f.session, http.MethodPost, "/api/v1/me/settings",
		`{"expected_revision":"0","profile":{"display_name":"Synthetic private profile"}}`, true), 200)
	var state string
	bodies := map[string][]byte{}
	for _, name := range []string{"book.tar", "journal.json", "transactions.csv", "journal-lines.csv", "accounts.csv"} {
		path := bookExportPrefix + name
		ownerAPIStatus(t, ownerAPICall(f.server, f.session, http.MethodGet, path, "", false), 400)
		ownerAPIStatus(t, ownerAPICall(f.server, f.session, http.MethodGet, path+"?book_id="+foreign.ID, "", false), 404)
		ownerAPIStatus(t, ownerAPICall(f.server, other, http.MethodGet, path+"?book_id="+f.book.ID, "", false), 404)
		ownerAPIStatus(t, ownerAPICall(f.server, f.session, http.MethodGet, path+"?book_id="+f.book.ID+"&book_id="+foreign.ID, "", false), 400)
		w := ownerAPICall(f.server, f.session, http.MethodGet, path+"?book_id="+f.book.ID, "", false)
		ownerAPIStatus(t, w, 200)
		if w.Header().Get("Content-Disposition") != `attachment; filename="`+name+`"` ||
			w.Header().Get("Content-Type") != bookExportType(path) ||
			w.Header().Get("Content-Length") != strconv.Itoa(w.Body.Len()) ||
			w.Header().Get("X-Vult-State-Revision") == "" ||
			bytes.Contains(w.Body.Bytes(), []byte(foreign.ID)) || bytes.Contains(w.Body.Bytes(), []byte("Synthetic private profile")) {
			t.Fatal("export metadata or isolation differs", name, w.Header())
		}
		revision := w.Header().Get("X-Vult-State-Revision")
		if state != "" && state != revision {
			t.Fatal("recorded state differs across formats")
		}
		state = revision
		bodies[name] = w.Body.Bytes()
	}
	if files, err := os.ReadDir(staging); err != nil || len(files) != 0 {
		t.Fatal("download left an export spool behind", err)
	}
	export, err := f.db.Owner.Export(ctx, f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cliJSON bytes.Buffer
	if err := json.NewEncoder(&cliJSON).Encode(export); err != nil || !bytes.Equal(cliJSON.Bytes(), bodies["journal.json"]) {
		t.Fatal("browser journal differs from CLI JSON bytes", err)
	}
	csvRows := func(name string) [][]string {
		t.Helper()
		rows, err := csv.NewReader(bytes.NewReader(bodies[name])).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	txs, lines, accounts := csvRows("transactions.csv"), csvRows("journal-lines.csv"), csvRows("accounts.csv")
	if len(txs) != 3 || txs[1][5] != "'=Example entry" || txs[1][9] != amount ||
		txs[2][9] != "18446744073709551614" || txs[2][10] != "4" ||
		len(lines) != 7 || lines[1][4] != "debit" || lines[1][5] != amount || lines[1][8] != "'@Example memo" ||
		lines[2][4] != "credit" || lines[2][5] != amount ||
		lines[3][2] != "1" || lines[6][2] != "4" || lines[6][5] != maxMinor ||
		len(accounts) != 7 || accounts[1][0] != debit.ID || accounts[1][2] != "'+Example" {
		t.Fatal("CSV rows lost exact amounts, ledger sides or safe text")
	}
	var cliArchive bytes.Buffer
	if err := f.db.Owner.WriteArchive(ctx, f.vault, &cliArchive, f.book.ID); err != nil {
		t.Fatal(err)
	}
	got, files := readExportedArchive(t, bodies["book.tar"])
	want, cliFiles := readExportedArchive(t, cliArchive.Bytes())
	got.Snapshot.CreatedAt, want.Snapshot.CreatedAt = time.Time{}, time.Time{}
	m1, _ := json.Marshal(got)
	m2, _ := json.Marshal(want)
	if !bytes.Equal(m1, m2) || len(files) != len(cliFiles) || !bytes.Equal(files["originals/"+doc.ID], original) ||
		!bytes.Equal(files["originals/"+doc.ID], cliFiles["originals/"+doc.ID]) {
		t.Fatal("browser archive differs from the CLI writer")
	}
	target := testutil.New(t)
	if err := target.Owner.RestoreArchive(target.Context, f.vault, bytes.NewReader(bodies["book.tar"]), nil); err != nil {
		t.Fatal("downloaded archive could not be restored", err)
	}
	var slug, name string
	err = target.Admin.QueryRow(target.Context, `SELECT a.slug,i.new_identity->>'legal_name'
		FROM public.book_addresses a JOIN public.book_identity_revisions i USING(tenant_id,book_id)
		WHERE a.tenant_id=$1 AND a.book_id=$2`, f.tenant, f.book.ID).Scan(&slug, &name)
	if err != nil || slug != "example-address" || name != "Example entity" {
		t.Fatal("archive recovery lost Book metadata", err)
	}
	var restoredImage []byte
	err = target.Admin.QueryRow(target.Context, "SELECT data FROM public.book_logo_images WHERE tenant_id=$1 AND book_id=$2 AND id=$3",
		f.tenant, f.book.ID, logoReceipt.Logo.ID).Scan(&restoredImage)
	if err != nil || !bytes.Equal(restoredImage, logo) {
		t.Fatal("archive recovery lost the current Book logo", err)
	}
}

func TestOwnerLocalArchiveUsesProtectedReaderAndDiscardsFailures(t *testing.T) {
	f, scope := browserDocuments(t)
	data := []byte("local synthetic original")
	doc := originalForBrowser(t, f, scope, "local-original", data, true)
	c, _ := localBrowserConfig(t, "127.0.0.1:51999")
	sessions, err := access.New(f.auth, f.owners, c.Origin)
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.IssueSession(f.db.Context, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	h, err := access.NewHTTP(sessions, nil, nil, func(*http.Request) (access.Identity, error) { return access.Identity{}, access.ErrDenied })
	if err != nil {
		t.Fatal(err)
	}
	reader, err := access.NewOwnerDocumentReader(f.db.Context, f.db.Admin, c.Origin)
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		s, err := New(c, Options{Database: f.db.Runtime, Access: &Access{Sessions: sessions, HTTP: h},
			Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}},
			ReadDocument: func(ctx context.Context, raw, book, id string, dst io.Writer) error {
				if raw != session || book != f.book.ID || id != doc.ID {
					t.Fatal("archive reader received substituted authority or identity")
				}
				if fail {
					_, _ = dst.Write(data)
					return documents.ErrIntegrity
				}
				return reader.Download(ctx, raw, book, id, f.vault, dst)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, c.Origin+bookExportPrefix+"book.tar?book_id="+f.book.ID, nil)
		r.URL.Scheme, r.URL.Host = "", ""
		r.TLS.HandshakeComplete = true
		r.TLS.ServerName = strings.Split(r.Host, ":")[0]
		r.RemoteAddr = "127.0.0.1:12345"
		r.AddCookie(&http.Cookie{Name: access.SessionCookie, Value: session})
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, r)
		if fail {
			ownerAPIStatus(t, w, 503)
			if w.Header().Get("Content-Disposition") != "" || bytes.Contains(w.Body.Bytes(), data) {
				t.Fatal("failed original produced a partial download")
			}
		} else {
			ownerAPIStatus(t, w, 200)
			_, files := readExportedArchive(t, w.Body.Bytes())
			if !bytes.Equal(files["originals/"+doc.ID], data) {
				t.Fatal("protected reader original missing from archive")
			}
		}
	}
}

type pausedExportResponse struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *pausedExportResponse) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.ResponseRecorder.Write(data)
}

func TestOwnerBookTransferDoesNotBlockAnotherExport(t *testing.T) {
	f, server := browserServer(t)
	other, book := otherAPIOwner(t, f)
	w := &pausedExportResponse{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Handler.ServeHTTP(w, browserRequest(http.MethodGet, bookExportPrefix+"journal.json?book_id="+f.book.ID, f.session))
	}()
	defer func() {
		close(w.release)
		<-done
	}()
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first export did not begin transferring")
	}
	ownerAPIStatus(t, ownerAPICall(server, other, http.MethodGet, bookExportPrefix+"accounts.csv?book_id="+book.ID, "", false), 200)
}

type pausedOriginalStore struct {
	documents.Storage
	entered chan struct{}
	release chan struct{}
}

func (s pausedOriginalStore) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	close(s.entered)
	select {
	case <-s.release:
		return s.Storage.Open(ctx, id)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestOwnerSignoutCanProceedDuringOriginalRead(t *testing.T) {
	for _, mode := range []string{"server archive", "protected original"} {
		t.Run(mode, func(t *testing.T) {
			f, scope := browserDocuments(t)
			data := []byte("synthetic document")
			doc := originalForBrowser(t, f, scope, "revocation-original", data, true)
			store := pausedOriginalStore{Storage: f.vault.Storage, entered: make(chan struct{}), release: make(chan struct{})}
			f.vault.Storage = store
			ctx, cancel := context.WithTimeout(f.db.Context, 10*time.Second)
			defer cancel()
			result := make(chan error, 1)
			w := httptest.NewRecorder()
			if mode == "server archive" {
				r := browserRequest(http.MethodGet, bookExportPrefix+"book.tar?book_id="+f.book.ID, f.session).WithContext(ctx)
				go func() {
					f.server.Handler.ServeHTTP(w, r)
					result <- nil
				}()
			} else {
				reader, err := access.NewOwnerDocumentReader(ctx, f.db.Admin, configuration().Origin)
				if err != nil {
					t.Fatal(err)
				}
				go func() {
					result <- reader.Download(ctx, f.session, f.book.ID, doc.ID, f.vault, io.Discard)
				}()
			}
			var released bool
			defer func() {
				if !released {
					cancel()
					close(store.release)
					<-result
				}
			}()
			select {
			case <-store.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("original read never reached storage")
			}
			signoutCtx, stop := context.WithTimeout(ctx, 2*time.Second)
			err := f.sessions.EndSession(signoutCtx, f.session)
			stop()
			if err != nil {
				t.Fatal("original read held a lock needed for sign-out", err)
			}
			close(store.release)
			released = true
			err = <-result
			if mode == "server archive" {
				ownerAPIStatus(t, w, 403)
				if w.Header().Get("Content-Disposition") != "" || bytes.Contains(w.Body.Bytes(), data) {
					t.Fatal("revoked session published staged bytes")
				}
				fresh, err := f.sessions.IssueSession(ctx, f.identity)
				if err != nil {
					t.Fatal(err)
				}
				ownerAPIStatus(t, ownerAPICall(f.server, fresh, http.MethodGet, bookExportPrefix+"accounts.csv?book_id="+f.book.ID, "", false), 200)
			} else if !errors.Is(err, access.ErrDenied) {
				t.Fatal("protected reader completed after revocation", err)
			}
		})
	}
}

func TestExportSpoolReleasesItsCountAndBytes(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	budget := exportBudget{bytes: maxSpoolBytes - 2}
	spool, err := budget.openSpool()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := spool.Write([]byte("ab")); n != 2 || err != nil {
		spool.close()
		t.Fatal("could not use available quota", n, err)
	}
	if n, err := spool.Write([]byte("c")); n != 0 || !errors.Is(err, errExportBusy) {
		spool.close()
		t.Fatal("spool exceeded the byte budget", n, err)
	}
	spool.close()
	if budget.open != 0 || budget.bytes != maxSpoolBytes-2 {
		t.Fatal("closed spool retained its reservation")
	}

	spools := make([]*exportSpool, 0, maxOpenExports)
	defer func() {
		for _, s := range spools {
			s.close()
		}
	}()
	for range maxOpenExports {
		spool, err := budget.openSpool()
		if err != nil {
			t.Fatal(err)
		}
		spools = append(spools, spool)
	}
	if extra, err := budget.openSpool(); !errors.Is(err, errExportBusy) {
		if extra != nil {
			extra.close()
		}
		t.Fatal("open spool limit was exceeded", err)
	}
}
