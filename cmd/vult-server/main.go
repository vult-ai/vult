package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

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
	s, e := server.New(config, server.Options{Database: db, Access: auth, Documents: documents, Web: web.App()})
	if e != nil {
		return e
	}
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
