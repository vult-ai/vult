package server

import (
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
)

func TestDatabaseTransportRequiresVerifiedRemoteTLS(t *testing.T) {
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "absent"))
	for _, raw := range []string{"", "postgres://user@example.com/book?sslmode=disable", "postgres://user@example.com/book?sslmode=prefer", "postgres://user@example.com/book?sslmode=require", "postgres://user@example.com/book?sslmode=verify-ca"} {
		if _, e := databaseConfig(raw); !errors.Is(e, ErrDatabase) {
			t.Fatal("unverified remote database transport accepted")
		}
	}
	for _, raw := range []string{"postgres://user@127.0.0.1/book?sslmode=disable", "postgres://user@example.com/book?sslmode=verify-full"} {
		if _, e := databaseConfig(raw); e != nil {
			t.Fatal("explicit database transport rejected", e)
		}
	}
}

func TestPrivateConfigurationFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "credential")
	if e := os.WriteFile(file, []byte("synthetic"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadPrivateFile(file, 16); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadPrivateFile(file, 2); !errors.Is(e, ErrConfiguration) {
		t.Fatal("oversized private file accepted")
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(file, link); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadPrivateFile(link, 16); !errors.Is(e, ErrConfiguration) {
		t.Fatal("symlink credential accepted")
	}
	if e := os.Chmod(file, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadPrivateFile(file, 16); !errors.Is(e, ErrConfiguration) {
		t.Fatal("shared credential accepted")
	}
}

func TestServerRequiresAnActualRestrictedDatabaseRole(t *testing.T) {
	db := testutil.New(t)
	file := filepath.Join(t.TempDir(), "runtime.dsn")
	for _, pool := range []bool{true, false} {
		c := db.Runtime.Config().ConnConfig
		if !pool {
			c = db.Admin.Config().ConnConfig
		}
		u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.Host, "5432"), User: url.UserPassword(c.User, c.Password), Path: "/" + c.Database}
		// The synthetic guard assigns an ephemeral port.
		u.Host = net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port)))
		u.RawQuery = "sslmode=disable"
		if e := os.WriteFile(file, []byte(u.String()), 0600); e != nil {
			t.Fatal(e)
		}
		p, e := OpenDatabase(db.Context, file)
		if pool {
			if e != nil || p == nil {
				t.Fatal("restricted LOGIN did not start", e)
			}
			p.Close()
		} else if p != nil || !errors.Is(e, ErrDatabase) {
			if p != nil {
				p.Close()
			}
			t.Fatal("privileged LOGIN was accepted")
		}
	}
}
