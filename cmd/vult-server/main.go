package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/vult-ai/vult/doccheck"
	"github.com/vult-ai/vult/ledger"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/server"
	"github.com/vult-ai/vult/web"
)

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("vult-server", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configFile := flags.String("config", "", "server configuration file")
	dsnFile := flags.String("dsn-file", "", "private runtime database credential file")
	if e := flags.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			_, _ = fmt.Fprintln(output, "Usage: vult-server --config FILE --dsn-file FILE")
			return nil
		}
		return server.ErrConfiguration
	}
	if os.Geteuid() == 0 || flags.NArg() != 0 || *configFile == "" || *dsnFile == "" {
		return server.ErrConfiguration
	}
	b, e := server.ReadPrivateFile(*configFile, 8<<10)
	if e != nil {
		return server.ErrConfiguration
	}
	var config server.Config
	if strictjson.Decode(b, &config) != nil {
		return server.ErrConfiguration
	}
	// What this release sets aside stays out of reach.
	config.SetAside = true
	// Validate network configuration before opening a database or listener.
	if _, e = server.New(config, server.Options{}); e != nil {
		return e
	}
	db, e := server.OpenDatabase(ctx, *dsnFile)
	if e != nil {
		return e
	}
	defer db.Close()
	auth, e := server.OpenAccess(ctx, config, db, nil)
	if e != nil {
		return e
	}
	defer auth.Close()
	documents, closer, e := server.OpenDocuments(config.Documents)
	if e != nil {
		return e
	}
	if closer != nil {
		defer closer.Close()
	}
	banks, e := server.OpenBanks(config, ledger.Store{Runtime: db}, nil)
	if e != nil {
		return e
	}
	agent := server.OpenAgent(ledger.Store{Runtime: db})
	deletion := server.BookDeletionPolicy{BackupRetention: time.Duration(config.BackupRetentionDays) * 24 * time.Hour}
	options := server.Options{Database: db, Access: auth, Documents: documents, Web: web.App(), Banks: banks, Agent: agent, BookDeletion: deletion}
	// This program has no assistant of its own to help read documents.
	options.DocumentAdded = doccheck.Hook(nil)
	s, e := server.New(config, options)
	if e != nil {
		return e
	}
	if banks != nil {
		go banks.Run(ctx)
	}
	// Books whose deletion waiting period is over are erased here.
	eraser := &server.BookEraser{DB: db, Documents: documents, Policy: deletion}
	go eraser.Run(ctx)
	listener, e := net.Listen("tcp", s.Addr)
	if e != nil {
		return server.ErrUnavailable
	}
	return server.Serve(ctx, s, listener)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if e := run(ctx, os.Args[1:], os.Stdout); e != nil {
		_, _ = fmt.Fprintln(os.Stderr, "vult-server:", e)
		os.Exit(1)
	}
}
