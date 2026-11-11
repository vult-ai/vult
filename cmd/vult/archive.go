package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/vult-ai/vult/internal/daemon"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/ledger"
	"golang.org/x/term"
)

func archiveCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "backup" || args[0] == "restore" {
		return true
	}
	if args[0] == "export" {
		for _, arg := range args[1:] {
			if arg == "--documents" || strings.HasPrefix(arg, "--documents=") {
				return true
			}
		}
	}
	return false
}

func runArchive(ctx context.Context, args []string, out io.Writer) error {
	f := flags(args[0])
	book := f.String("book", "", "Book ID")
	input := f.String("input", "", "encrypted backup")
	output := f.String("output", "", "new output file")
	documents := f.Bool("documents", false, "include originals and relationship history")
	point := f.String("recovery-point", "", "a recovery point's encrypted archive")
	if parse(f, args[1:]) != nil {
		return ledger.ErrInvalid
	}
	if *point != "" {
		if args[0] != "restore" || *book != "" || *input != "" || *output != "" || *documents {
			return ledger.ErrInvalid
		}
		return runRecoveryRestore(ctx, *point, out)
	}
	switch args[0] {
	case "backup":
		if *book != "" || *input != "" || *output == "" || *documents {
			return ledger.ErrInvalid
		}
	case "restore":
		if *book != "" || *output != "" || *input == "" || *documents {
			return ledger.ErrInvalid
		}
	case "export":
		if !ledger.ValidID(*book) || *output == "" || *input != "" || !*documents {
			return ledger.ErrInvalid
		}
	default:
		return ledger.ErrInvalid
	}
	client, err := local.NewClient()
	if err != nil {
		return err
	}
	if args[0] == "restore" {
		return runArchiveRestore(ctx, client, *input, out)
	}
	request := local.ArchiveRequest{BookID: *book, Encrypted: args[0] == "backup"}
	if err = local.WriteNewStream(*output, func(dst io.Writer) error {
		authorization, err := client.OwnerAuthorization(ctx, "archive_backup", request)
		if err != nil {
			return err
		}
		return client.ArchiveBackup(ctx, authorization, request, dst)
	}); err != nil {
		return err
	}
	status := "encrypted_backup_written"
	if !request.Encrypted {
		status = "portable_export_written"
	}
	_ = jsonOut(out, map[string]string{"status": status})
	return nil
}

// runRecoveryRestore restores a recovery point onto a newly installed Vult.
// An administrator runs it with sudo and types the installation recovery
// key; it needs no Vult sign-in, so it works on a Mac where nobody has set
// Vult up yet.
func runRecoveryRestore(ctx context.Context, path string, out io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("run this with sudo: restoring a recovery point needs an administrator")
	}
	key, err := readSecret("Installation recovery key: ")
	if err != nil {
		return err
	}
	defer clear(key)
	return daemon.RestoreRecoveryPoint(ctx, path, string(key), out)
}

func readSecret(prompt string) ([]byte, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil || !term.IsTerminal(int(tty.Fd())) {
		if tty != nil {
			tty.Close()
		}
		return nil, errors.New("recovery requires an interactive terminal")
	}
	defer tty.Close()
	fmt.Fprint(tty, prompt)
	key, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	if err != nil || len(key) == 0 {
		return nil, errors.New("recovery key unavailable")
	}
	return key, nil
}

func runArchiveRestore(ctx context.Context, client local.Client, path string, out io.Writer) error {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("encrypted backup unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > local.MaxEncryptedArchive {
		return errors.New("invalid encrypted backup")
	}
	hash := sha256.New()
	if n, err := io.Copy(hash, io.LimitReader(file, info.Size()+1)); err != nil || n != info.Size() {
		return errors.New("encrypted backup changed while reading")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return errors.New("encrypted backup unavailable")
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return errors.New("recovery requires an interactive owner terminal")
	}
	defer tty.Close()
	if !term.IsTerminal(int(tty.Fd())) {
		return errors.New("recovery requires an interactive owner terminal")
	}
	fmt.Fprint(tty, "Backup recovery key: ")
	key, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	if err != nil {
		return errors.New("recovery key unavailable")
	}
	defer clear(key)
	request := local.RestoreRequest{Size: info.Size(), SHA256: hex.EncodeToString(hash.Sum(nil)), RecoveryKey: string(key)}
	if !request.Valid() {
		return ledger.ErrInvalid
	}
	authorization, err := client.OwnerAuthorization(ctx, "archive_restore", request)
	if err != nil {
		return err
	}
	result, err := client.ArchiveRestore(ctx, authorization, request, file)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, string(result))
	return nil
}
