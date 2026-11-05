package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"filippo.io/age"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

var errRestoreResult = errors.New("restore completed; result output is incomplete. Inspect the installation before retrying")
var errRestoreUncertain = errors.New("restore outcome unresolved; inspect the installation before retrying")

func runArchive(ctx context.Context, command, dsn string, q request, output string, out io.Writer) error {
	if q.BookID != "" && !ledger.ValidID(q.BookID) {
		return errOperation
	}
	if command == "export" && q.BookID == "" || command == "restore" && (q.ArchiveFile == "" || q.BookID != "") {
		return errOperation
	}
	if command != "export" && q.RecoveryKeyFile == "" {
		return errOperation
	}
	db, err := openDatabase(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	vault, closer, err := server.OpenDocuments(&server.DocumentStorage{Directory: q.Directory, KeyFile: q.KeyFile, KeyDirectory: q.KeyDirectory})
	if err != nil || vault == nil {
		return errOperation
	}
	defer closer.Close()
	var key string
	if command != "export" {
		data, err := server.ReadPrivateFile(q.RecoveryKeyFile, 128)
		if err != nil {
			return errOperation
		}
		key = strings.TrimSpace(string(data))
		clear(data)
		if len(key) < 32 || len(key) > 128 {
			return errOperation
		}
	}
	admin := ledger.Admin{DB: db}
	restored := false
	err = local.WriteNewStream(output, func(dst io.Writer) error {
		switch command {
		case "export":
			return admin.WriteArchive(ctx, vault, dst, q.BookID)
		case "backup":
			recipient, err := age.NewScryptRecipient(key)
			if err != nil {
				return errOperation
			}
			recipient.SetWorkFactor(18)
			cipher, err := age.Encrypt(dst, recipient)
			if err != nil {
				return errOperation
			}
			if err = admin.WriteArchive(ctx, vault, cipher, q.BookID); err != nil {
				return err
			}
			if err = cipher.Close(); err != nil {
				return errOperation
			}
			return nil
		case "restore":
			file, err := os.OpenFile(q.ArchiveFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if err != nil {
				return errOperation
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > local.MaxEncryptedArchive {
				return errOperation
			}
			identity, err := age.NewScryptIdentity(key)
			if err != nil {
				return errOperation
			}
			identity.SetMaxWorkFactor(18)
			plain, err := age.Decrypt(io.LimitReader(file, local.MaxEncryptedArchive+1), identity)
			if err != nil {
				return documents.ErrIntegrity
			}
			reader := bufio.NewReaderSize(plain, 4096)
			first, err := reader.Peek(1)
			if err != nil {
				return documents.ErrIntegrity
			}
			var provision func(context.Context, string) error
			if keys, ok := vault.Keys.(*documents.KeyDirectory); ok {
				provision = keys.Provision
			}
			if first[0] == '{' {
				data, err := io.ReadAll(io.LimitReader(reader, (64<<20)+1))
				if err != nil || len(data) > 64<<20 {
					return documents.ErrIntegrity
				}
				var snapshot ledger.Snapshot
				if strictjson.Decode(data, &snapshot) != nil {
					return ledger.ErrInvalid
				}
				var tenants []struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(snapshot.Tables["tenants"], &tenants) != nil {
					return ledger.ErrInvalid
				}
				if provision != nil {
					for _, tenant := range tenants {
						if !ledger.ValidID(tenant.ID) {
							return ledger.ErrInvalid
						}
						if err = provision(ctx, tenant.ID); err != nil {
							return err
						}
					}
				}
				if err = admin.Restore(ctx, snapshot); err != nil {
					return err
				}
			} else {
				if err = admin.RestoreArchive(ctx, vault, reader, provision); err != nil {
					return err
				}
			}
			restored = true
			return json.NewEncoder(dst).Encode(map[string]string{"status": "restored", "connections": "reauthorization_required"})
		}
		return errOperation
	})
	if err != nil {
		if restored {
			return errRestoreResult
		}
		if command == "restore" && errors.Is(err, ledger.ErrUncertain) {
			return errRestoreUncertain
		}
		return err
	}
	_, _ = fmt.Fprintln(out, "Completed. Output saved privately.")
	return nil
}
