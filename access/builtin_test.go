package access_test

import (
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

const examplePassword = "Example horse battery staple 7428"

func enrollmentCode(t *testing.T, e access.Enrollment) string {
	t.Helper()
	u, err := url.Parse(e.AuthenticatorURI)
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(u.Query().Get("secret"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestBuiltinDurableReplayRecoveryAndReset(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	key := []byte(strings.Repeat("k", 32))
	enrolled, e := f.operator.InitializeBuiltin(ctx, "", "example", examplePassword, key)
	if e != nil {
		t.Fatal(e)
	}
	b, e := access.NewBuiltin(ctx, f.service, "example", key)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.SignIn(ctx, "example", examplePassword, enrolled.RecoveryCodes[0]); !errors.Is(e, access.ErrDenied) {
		t.Fatal("recovery bypassed first authenticator enrollment")
	}
	code := enrollmentCode(t, enrolled)
	session, e := b.SignIn(ctx, "example", examplePassword, code)
	if e != nil {
		t.Fatal("first authenticator sign-in failed", e)
	}
	book := f.book(t, enrolled.TenantID, session)
	restarted, e := access.NewBuiltin(ctx, f.service, "example", key)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = restarted.SignIn(ctx, "example", examplePassword, code); !errors.Is(e, access.ErrDenied) {
		t.Fatal("authenticator replay survived restart")
	}
	if _, e = restarted.SignIn(ctx, "example", examplePassword, enrolled.RecoveryCodes[0]); e != nil {
		t.Fatal("one-use recovery failed", e)
	}
	if _, e = restarted.SignIn(ctx, "example", examplePassword, enrolled.RecoveryCodes[0]); !errors.Is(e, access.ErrDenied) {
		t.Fatal("recovery code replayed")
	}
	if _, e = access.NewBuiltin(ctx, f.service, "example", []byte(strings.Repeat("x", 32))); !errors.Is(e, access.ErrDenied) {
		t.Fatal("wrong credential key accepted")
	}
	reset, e := f.operator.ResetBuiltin(ctx, "example", examplePassword, key)
	if e != nil {
		t.Fatal(e)
	}
	if reset.TenantID != enrolled.TenantID {
		t.Fatal("recovery changed ownership")
	}
	if _, e = f.service.Identity(ctx, session); !errors.Is(e, access.ErrDenied) {
		t.Fatal("recovery retained an old session")
	}
	session, e = restarted.SignIn(ctx, "example", examplePassword, enrollmentCode(t, reset))
	if e != nil {
		t.Fatal(e)
	}
	if e = f.service.WithOwner(ctx, session, enrolled.TenantID, false, func(o ledger.TenantAdmin) error {
		b, e := o.Book(ctx, book.ID)
		if e != nil || b.ID != book.ID {
			t.Fatal("recovery lost a Book", e)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestBuiltinSharedAttemptBudgetAndConcurrentCode(t *testing.T) {
	f := fresh(t)
	ctx := f.db.Context
	key := []byte(strings.Repeat("k", 32))
	enrolled, e := f.operator.InitializeBuiltin(ctx, "", "example", examplePassword, key)
	if e != nil {
		t.Fatal(e)
	}
	a, e := access.NewBuiltin(ctx, f.service, "example", key)
	if e != nil {
		t.Fatal(e)
	}
	b, e := access.NewBuiltin(ctx, f.service, "example", key)
	if e != nil {
		t.Fatal(e)
	}
	code := enrollmentCode(t, enrolled)
	var wg sync.WaitGroup
	result := make([]error, 2)
	for i, provider := range []*access.Builtin{a, b} {
		wg.Add(1)
		go func(i int, p *access.Builtin) {
			defer wg.Done()
			_, result[i] = p.SignIn(ctx, "example", examplePassword, code)
		}(i, provider)
	}
	wg.Wait()
	success := 0
	for _, e := range result {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("one authenticator code did not produce one session")
	}
	for i := 0; i < 8; i++ {
		if _, e = a.SignIn(ctx, "unknown", examplePassword, "000000"); !errors.Is(e, access.ErrDenied) {
			t.Fatal("wrong username authenticated")
		}
	}
	if _, e = a.SignIn(ctx, "example", examplePassword, enrolled.RecoveryCodes[0]); !errors.Is(e, access.ErrDenied) {
		t.Fatal("rotating usernames bypassed account budget")
	}
	if _, e = f.db.Admin.Exec(ctx, "UPDATE access.builtin_credentials SET window_expires_at=clock_timestamp()-interval '1 second'"); e != nil {
		t.Fatal(e)
	}
	if _, e = a.SignIn(ctx, "example", examplePassword, enrolled.RecoveryCodes[0]); e != nil {
		t.Fatal("expired attempt window blocked legitimate recovery", e)
	}
}
