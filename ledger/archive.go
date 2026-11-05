package ledger

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/strictjson"
)

const MaxArchiveSize int64 = 8 << 30
const maxArchiveMetadata int64 = 64 << 20

var ErrArchive = errors.New("archive unavailable; discard incomplete output")
var ErrArchiveCleanup = errors.New("restore failed; staged encrypted objects need operator cleanup")
var archiveTables = append(append([]string{}, snapshotTables...), "documents", "document_uploads", "source_citations", "evidence_links")

type archiveManifest struct {
	Format     string             `json:"format"`
	Migrations map[string]string  `json:"migrations"`
	Snapshot   Snapshot           `json:"snapshot"`
	Originals  []documents.Object `json:"originals"`
	digest     string
}

type archiveFooter struct {
	Format         string `json:"format"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

func archiveHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func documentRows(data []byte) ([]map[string]json.RawMessage, error) {
	var rows []map[string]json.RawMessage
	if json.Unmarshal(data, &rows) != nil || rows == nil {
		return nil, ErrInvalid
	}
	return rows, nil
}

func documentObject(row map[string]json.RawMessage) (documents.Object, string, *time.Time, time.Time, error) {
	var object documents.Object
	var state, name, media string
	var used *time.Time
	var expires time.Time
	fields := map[string]any{"tenant_id": &object.TenantID, "book_id": &object.BookID, "id": &object.DocumentID, "storage_id": &object.StorageID, "key_id": &object.KeyID, "sha256": &object.SHA256, "size_bytes": &object.Size, "state": &state, "used_at": &used, "expires_at": &expires, "original_name": &name, "media_type": &media}
	for key, dst := range fields {
		if json.Unmarshal(row[key], dst) != nil {
			return object, "", nil, expires, ErrInvalid
		}
	}
	if !ValidID(object.TenantID) || !ValidID(object.BookID) || !ValidID(object.DocumentID) ||
		!validDocument(AddDocument{RequestKey: "archive", Name: name, MediaType: media, Size: object.Size, SHA256: object.SHA256}) ||
		(state != "pending" && state != "ready" && state != "expired") || used != nil && state != "ready" {
		return object, "", nil, expires, ErrInvalid
	}
	if decoded, err := hex.DecodeString(object.StorageID); err != nil || len(decoded) != 32 || strings.ToLower(object.StorageID) != object.StorageID {
		return object, "", nil, expires, ErrInvalid
	}
	return object, state, used, expires, nil
}

func archiveFailure(err error) error {
	if err == nil {
		return nil
	}
	for _, safe := range []error{ErrInvalid, ErrDenied, ErrConflict, ErrNotFound, ErrUncertain, ErrLimit, ErrArchiveCleanup, documents.ErrIntegrity, documents.ErrUnavailable} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return ErrArchive
}

// WriteArchive streams a plaintext portable archive. Callers encrypt backups
// and publish output only after this method succeeds. book="" selects the
// installation; otherwise only that Book is included. Grants are never exported.
func (a Admin) WriteArchive(ctx context.Context, vault *documents.Vault, dst io.Writer, book string) (err error) {
	defer func() { err = archiveFailure(err) }()
	if dst == nil || vault == nil || vault.Keys == nil || vault.Storage == nil || book != "" && !ValidID(book) {
		return ErrInvalid
	}
	tx, err := a.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if book == "" {
		// Freeze the catalog and every Book write through metadata and byte copy.
		_, err = tx.Exec(ctx, "LOCK TABLE public.books IN SHARE MODE")
	} else {
		var id string
		err = tx.QueryRow(ctx, "SELECT id::text FROM public.books WHERE id=$1 FOR SHARE", book).Scan(&id)
	}
	if err != nil {
		return dbError(err)
	}
	var cutoff time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&cutoff); err != nil {
		return err
	}
	manifest := archiveManifest{Format: "vult-archive-v1", Migrations: map[string]string{}, Snapshot: Snapshot{Format: "vult-documents-backup-v1", SchemaSHA256: SchemaSHA256(), CreatedAt: cutoff.UTC(), Tables: map[string]json.RawMessage{}}, Originals: []documents.Object{}}
	for _, name := range migrationNames() {
		manifest.Migrations[name] = migrationSum(name)
	}
	total := 0
	for _, table := range archiveTables {
		data, err := snapshotRows(ctx, tx, table, book, int(maxArchiveMetadata)-total)
		if err != nil {
			return err
		}
		total += len(data)
		manifest.Snapshot.Tables[table] = data
	}
	rows, err := documentRows(manifest.Snapshot.Tables["documents"])
	if err != nil {
		return err
	}
	for _, row := range rows {
		object, state, used, expires, err := documentObject(row)
		if err != nil {
			return err
		}
		if used == nil && !expires.After(manifest.Snapshot.CreatedAt) {
			// Already-expired unused content may have been reclaimed. Preserve
			// identity/history, while recording its unavailable operational state.
			row["state"] = raw("expired")
			continue
		}
		if state == "ready" {
			manifest.Originals = append(manifest.Originals, object)
		}
	}
	manifest.Snapshot.Tables["documents"] = raw(rows)
	metadata := raw(manifest)
	if err = archiveBudget(int64(len(metadata)), manifest.Originals); err != nil {
		return err
	}
	writer := tar.NewWriter(&archiveBoundWriter{dst: dst, remaining: MaxArchiveSize})
	if err = writer.WriteHeader(&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(metadata))}); err != nil {
		return err
	}
	if _, err = writer.Write(metadata); err != nil {
		return err
	}
	for _, object := range manifest.Originals {
		if err = writer.WriteHeader(&tar.Header{Name: "originals/" + object.DocumentID, Typeflag: tar.TypeReg, Mode: 0600, Size: object.Size}); err != nil {
			return err
		}
		if err = vault.Read(ctx, object, writer); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return ErrUncertain
	}
	footer := raw(archiveFooter{Format: "vult-archive-complete-v1", ManifestSHA256: archiveHash(metadata)})
	if err = writer.WriteHeader(&tar.Header{Name: "complete.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(footer))}); err != nil {
		return err
	}
	if _, err = writer.Write(footer); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return nil
}

type archiveBoundWriter struct {
	dst       io.Writer
	remaining int64
}

func (w *archiveBoundWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, ErrLimit
	}
	n, err := w.dst.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func archiveHeader(header *tar.Header, name string) bool {
	return header != nil && header.Typeflag == tar.TypeReg && header.Name == name && header.Format == tar.FormatUSTAR && header.Linkname == "" && len(header.PAXRecords) == 0
}

// Count logical bytes before any key provisioning or storage effect. Raw tar
// length alone is insufficient: sparse extensions can expand during Read.
func archiveBudget(metadata int64, originals []documents.Object) error {
	if metadata < 2 || metadata > maxArchiveMetadata {
		return ErrLimit
	}
	total := int64(1024)
	add := func(size int64) bool {
		if size < 0 || size > MaxArchiveSize {
			return false
		}
		member := int64(512) + (size+511)/512*512
		if total > MaxArchiveSize-member {
			return false
		}
		total += member
		return true
	}
	footer := raw(archiveFooter{Format: "vult-archive-complete-v1", ManifestSHA256: strings.Repeat("0", 64)})
	if !add(metadata) || !add(int64(len(footer))) {
		return ErrLimit
	}
	for _, object := range originals {
		if object.Size < 1 || object.Size > documents.MaxSize {
			return ErrInvalid
		}
		if !add(object.Size) {
			return ErrLimit
		}
	}
	return nil
}

func readArchiveManifest(reader *tar.Reader) (archiveManifest, error) {
	var manifest archiveManifest
	header, err := reader.Next()
	if err != nil || !archiveHeader(header, "manifest.json") || header.Size < 2 || header.Size > maxArchiveMetadata {
		return manifest, ErrInvalid
	}
	data, err := io.ReadAll(reader)
	if err != nil || strictjson.Decode(data, &manifest) != nil || manifest.Format != "vult-archive-v1" || manifest.Snapshot.Format != "vult-documents-backup-v1" || manifest.Snapshot.SchemaSHA256 != SchemaSHA256() || len(manifest.Snapshot.Tables) != len(archiveTables) || len(manifest.Originals) > 100000 {
		return manifest, ErrInvalid
	}
	if len(manifest.Migrations) != len(migrationNames()) {
		return manifest, ErrInvalid
	}
	for _, name := range migrationNames() {
		if manifest.Migrations[name] != migrationSum(name) {
			return manifest, ErrInvalid
		}
	}
	for _, table := range archiveTables {
		if len(manifest.Snapshot.Tables[table]) < 2 {
			return manifest, ErrInvalid
		}
	}
	if err = archiveBudget(int64(len(data)), manifest.Originals); err != nil {
		return manifest, err
	}
	manifest.digest = archiveHash(data)
	return manifest, nil
}

// RestoreArchive verifies each original, encrypts it under destination keys and
// imports metadata atomically into an empty installation. A provisioning callback
// is reserved for trusted recovery tooling; ordinary key lookup never provisions.
func (a Admin) RestoreArchive(ctx context.Context, vault *documents.Vault, source io.Reader, provision func(context.Context, string) error) (err error) {
	defer func() { err = archiveFailure(err) }()
	if source == nil || vault == nil || vault.Keys == nil || vault.Storage == nil {
		return ErrInvalid
	}
	bounded := &io.LimitedReader{R: source, N: MaxArchiveSize + 1}
	reader := tar.NewReader(bounded)
	manifest, err := readArchiveManifest(reader)
	if err != nil {
		return err
	}
	tx, err := a.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(769305014)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "LOCK TABLE public.books IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return err
	}
	var occupied bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM public.books) OR EXISTS(SELECT 1 FROM public.connections)").Scan(&occupied); err != nil {
		return err
	}
	if occupied {
		return errors.New("restore requires an empty installation")
	}
	rows, err := documentRows(manifest.Snapshot.Tables["documents"])
	if err != nil {
		return err
	}
	tenants, err := documentRows(manifest.Snapshot.Tables["tenants"])
	if err != nil {
		return err
	}
	knownTenants := map[string]bool{}
	for _, row := range tenants {
		var id string
		if json.Unmarshal(row["id"], &id) != nil || !ValidID(id) || knownTenants[id] {
			return ErrInvalid
		}
		knownTenants[id] = true
		if provision != nil {
			if err = provision(ctx, id); err != nil {
				return err
			}
		}
	}
	versions := map[string]string{}
	oldObjects := map[string]documents.Object{}
	states := map[string]string{}
	newObjects := map[string]documents.Object{}
	storageMap := map[string]string{}
	for _, row := range rows {
		object, state, _, _, err := documentObject(row)
		if err != nil {
			return err
		}
		if !knownTenants[object.TenantID] {
			return ErrInvalid
		}
		if _, duplicate := oldObjects[object.DocumentID]; duplicate {
			return ErrInvalid
		}
		oldObjects[object.DocumentID] = object
		states[object.DocumentID] = state
		if state == "expired" {
			continue
		}
		version, ok := versions[object.TenantID]
		if !ok {
			version, err = vault.Keys.Current(ctx, object.TenantID)
			if err != nil {
				return documents.ErrUnavailable
			}
			versions[object.TenantID] = version
		}
		fresh := object
		fresh.StorageID = strings.ReplaceAll(ID()+ID(), "-", "")
		fresh.KeyID = version
		storageMap[object.StorageID] = fresh.StorageID
		newObjects[object.DocumentID] = fresh
		row["storage_id"], row["key_id"] = raw(fresh.StorageID), raw(fresh.KeyID)
	}
	manifest.Snapshot.Tables["documents"] = raw(rows)
	uploads, err := documentRows(manifest.Snapshot.Tables["document_uploads"])
	if err != nil {
		return err
	}
	for _, row := range uploads {
		var old string
		if json.Unmarshal(row["storage_id"], &old) != nil {
			return ErrInvalid
		}
		if replacement, ok := storageMap[old]; ok {
			row["storage_id"] = raw(replacement)
		}
	}
	manifest.Snapshot.Tables["document_uploads"] = raw(uploads)
	created := []string{}
	keep := false
	defer func() {
		if keep {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, id := range created {
			if vault.Storage.Remove(cleanup, id) != nil {
				err = ErrArchiveCleanup
			}
		}
	}()
	staging := *vault
	staging.Storage = &archiveStaging{Storage: vault.Storage, created: &created}
	seen := map[string]bool{}
	for _, original := range manifest.Originals {
		old, ok := oldObjects[original.DocumentID]
		fresh, needed := newObjects[original.DocumentID]
		if !ok || !needed || old != original || seen[original.DocumentID] {
			return ErrInvalid
		}
		if states[original.DocumentID] != "ready" {
			return ErrInvalid
		}
		header, err := reader.Next()
		if err != nil || !archiveHeader(header, "originals/"+original.DocumentID) || header.Size != original.Size {
			return ErrInvalid
		}
		if err = staging.Write(ctx, fresh, reader); err != nil {
			return err
		}
		seen[original.DocumentID] = true
	}
	for id, state := range states {
		if state == "ready" && !seen[id] {
			return documents.ErrIntegrity
		}
	}
	header, err := reader.Next()
	if err != nil || !archiveHeader(header, "complete.json") || header.Size < 2 || header.Size > 1024 {
		return documents.ErrIntegrity
	}
	footerBytes, err := io.ReadAll(reader)
	var footer archiveFooter
	if err != nil || strictjson.Decode(footerBytes, &footer) != nil || footer.Format != "vult-archive-complete-v1" || footer.ManifestSHA256 != manifest.digest {
		return documents.ErrIntegrity
	}
	if _, err = reader.Next(); err != io.EOF {
		return ErrInvalid
	}
	// Reading past the tar terminator is mandatory: this authenticates the last
	// encryption frame too and rejects concatenated or trailing archive data.
	var last [1]byte
	if n, err := bounded.Read(last[:]); n != 0 || err != io.EOF || bounded.N <= 0 {
		return documents.ErrIntegrity
	}
	if err = restoreSnapshotRows(ctx, tx, manifest.Snapshot, archiveTables); err != nil {
		return err
	}
	// Once COMMIT is attempted its outcome may be uncertain. Never remove bytes
	// that could already be referenced by a committed restored journal.
	keep = true
	if err = tx.Commit(ctx); err != nil {
		return ErrUncertain
	}
	return nil
}

type archiveStaging struct {
	documents.Storage
	created *[]string
}

func (s *archiveStaging) Create(ctx context.Context, id string, write func(io.Writer) error) error {
	*s.created = append(*s.created, id)
	err := s.Storage.Create(ctx, id, write)
	if errors.Is(err, documents.ErrExists) {
		*s.created = (*s.created)[:len(*s.created)-1]
	}
	return err
}
