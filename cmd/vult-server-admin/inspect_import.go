package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/vult-ai/vult/internal/importarchive"
	"github.com/vult-ai/vult/internal/local"
)

func inspectImport(ctx context.Context, args []string, out io.Writer) error {
	f := flag.NewFlagSet("inspect-import", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	input := f.String("archive", "", "reconciled source package")
	output := f.String("output", "", "new private inspection report")
	if f.Parse(args) != nil || f.NArg() != 0 || *input == "" || *output == "" {
		return errOperation
	}
	file, err := os.OpenFile(*input, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errOperation
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errOperation
	}
	if info.Size() > importarchive.MaxArchiveSize {
		return importarchive.ErrLimit
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = local.WriteNewStream(*output, func(w io.Writer) error {
		report, err := importarchive.Inspect(ctx, file)
		if err != nil {
			return err
		}
		return json.NewEncoder(w).Encode(report)
	})
	if err != nil {
		for _, known := range []error{importarchive.ErrFormat, importarchive.ErrIntegrity, importarchive.ErrLimit, context.Canceled, context.DeadlineExceeded} {
			if errors.Is(err, known) {
				return known
			}
		}
		return errOperation
	}
	_, _ = fmt.Fprintln(out, "Import package inspected. Accounting remains not_checked.")
	return nil
}
