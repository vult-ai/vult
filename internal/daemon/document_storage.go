package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

type documentStorage struct {
	vault *documents.Vault
	disk  *documents.Disk
	keys  *documents.KeyDirectory
}

func (d *documentStorage) Close() error { return errors.Join(d.disk.Close(), d.keys.Close()) }

func prepareDocumentStorage(ctx context.Context, admin ledger.Admin, data string) (*documentStorage, error) {
	for _, name := range []string{"originals", "document-keys"} {
		if err := os.Mkdir(filepath.Join(data, name), 0700); err != nil && !os.IsExist(err) {
			return nil, documents.ErrUnavailable
		}
	}
	disk, err := documents.OpenDisk(filepath.Join(data, "originals"))
	if err != nil {
		return nil, err
	}
	keys, err := documents.OpenKeyDirectory(filepath.Join(data, "document-keys"))
	if err != nil {
		disk.Close()
		return nil, err
	}
	storage := &documentStorage{vault: &documents.Vault{Storage: disk, Keys: keys}, disk: disk, keys: keys}
	ok := false
	defer func() {
		if !ok {
			storage.Close()
		}
	}()
	// Missing historic keys require recovery. Never silently generate new keys
	// over a database that still refers to an existing original or upload.
	rows, err := admin.DB.Query(ctx, "SELECT DISTINCT tenant_id::text,key_id FROM public.documents WHERE state<>'expired'")
	if err != nil {
		return nil, documents.ErrUnavailable
	}
	for rows.Next() {
		var tenant, version string
		if rows.Scan(&tenant, &version) != nil {
			rows.Close()
			return nil, documents.ErrUnavailable
		}
		if _, err = keys.Identity(ctx, tenant, version); err != nil {
			rows.Close()
			return nil, errors.New("document keys need recovery")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, documents.ErrUnavailable
	}
	tenant, err := admin.EnsureTenant(ctx)
	if err != nil {
		return nil, documents.ErrUnavailable
	}
	if err = keys.Provision(ctx, tenant); err != nil {
		return nil, err
	}
	ok = true
	return storage, nil
}

// The native process already owns the offline administration pool under its
// separate system identity. Cleanup runs bounded pages without agent grants.
func maintainDocuments(ctx context.Context, admin ledger.Admin, storage documents.Storage) {
	runDocumentMaintenance(ctx, admin, storage, os.Stderr)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runDocumentMaintenance(ctx, admin, storage, os.Stderr)
		}
	}
}

func runDocumentMaintenance(ctx context.Context, admin ledger.Admin, storage documents.Storage, diagnostics io.Writer) {
	batch, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	books, err := admin.ExpiredDocumentBooks(batch, 20)
	if err != nil {
		if ctx.Err() == nil {
			fmt.Fprintln(diagnostics, "document maintenance could not select expired originals; it will retry")
		}
		return
	}
	failed := false
	for _, book := range books {
		if batch.Err() != nil {
			failed = true
			break
		}
		// A busy Book cannot consume the entire sweep's deadline.
		bookCtx, done := context.WithTimeout(batch, 5*time.Second)
		_, err = admin.ExpireBookDocuments(bookCtx, book, storage, 100)
		done()
		failed = failed || err != nil
	}
	if failed && ctx.Err() == nil {
		fmt.Fprintln(diagnostics, "document maintenance could not finish every Book; it will retry")
	}
}

var _ io.Closer = (*documentStorage)(nil)
