package daemon

import (
	"context"
	"net/url"
	"path/filepath"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/ledger"
)

type browserOwner struct {
	configuration browserConfiguration
	uid           uint32
	documents     *access.OwnerDocumentReader
}

func loadBrowserOwner(data string, uid uint32) *browserOwner {
	c, err := readBrowserConfiguration(filepath.Join(data, browserConfigurationName), uid)
	if err != nil {
		return nil
	}
	browserUID, _, err := browserIdentity()
	if err != nil {
		return nil
	}
	return &browserOwner{configuration: c, uid: browserUID}
}

func (b *browserOwner) prepareIdentity(ctx context.Context, admin ledger.Admin) error {
	status, err := b.status(ctx, admin)
	if err != nil || status.Ready {
		return err
	}
	if moved, err := b.addressMoved(ctx, admin); err != nil {
		return err
	} else if moved {
		// The old passkey cannot serve the new address. Prepare its pending
		// profile now so the browser can start before the owner opens setup.
		// As on first installation, discard secrets until native-approved setup.
		_, err = (access.Operator{DB: admin.DB}).ResetPasskeys(ctx, localOwnerSubject, b.configuration.origin())
		return err
	}
	tenant, err := admin.EnsureTenant(ctx)
	if err != nil {
		return err
	}
	// The installer authorizes the fixed local owner but discards setup secrets.
	// The terminal supplies a fresh, native-approved enrollment when requested.
	_, err = (access.Operator{DB: admin.DB}).InitializePasskeys(ctx, tenant, localOwnerSubject, b.configuration.origin())
	return err
}

func (b *browserOwner) status(ctx context.Context, admin ledger.Admin) (local.BrowserStatus, error) {
	if b == nil || !b.configuration.valid() || admin.DB == nil {
		return local.BrowserStatus{}, ledger.ErrDenied
	}
	var ready bool
	err := admin.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.passkey_profiles p JOIN access.identities i ON i.id=p.identity_id
 WHERE i.issuer=$1 AND i.subject=$2 AND p.origin=$3 AND p.state='ready'
 AND EXISTS(SELECT 1 FROM access.owners o WHERE o.identity_id=i.id))`, access.BuiltinIssuer, localOwnerSubject, b.configuration.origin()).Scan(&ready)
	return local.BrowserStatus{Origin: b.configuration.origin(), Ready: ready}, err
}

func (b *browserOwner) setup(ctx context.Context, admin ledger.Admin, reset bool) (local.BrowserSetup, error) {
	if b == nil || !b.configuration.valid() || admin.DB == nil {
		return local.BrowserSetup{}, ledger.ErrDenied
	}
	op := access.Operator{DB: admin.DB}
	var setup access.PasskeySetup
	moved, err := b.addressMoved(ctx, admin)
	if err != nil {
		return local.BrowserSetup{}, ledger.ErrDenied
	}
	// A passkey is bound to the address it was made at, so a moved address
	// needs a new one, made the same way as after a reset.
	if reset || moved {
		setup, err = op.ResetPasskeys(ctx, localOwnerSubject, b.configuration.origin())
	} else {
		var tenant string
		tenant, err = admin.EnsureTenant(ctx)
		if err == nil {
			setup, err = op.InitializePasskeys(ctx, tenant, localOwnerSubject, b.configuration.origin())
		}
	}
	if err != nil {
		return local.BrowserSetup{}, ledger.ErrDenied
	}
	u, err := url.Parse(b.configuration.origin() + "/auth/setup")
	if err != nil {
		return local.BrowserSetup{}, ledger.ErrInvalid
	}
	u.Fragment = "setup=" + setup.Token
	return local.BrowserSetup{Origin: b.configuration.origin(), SetupURL: u.String()}, nil
}

// addressMoved reports whether the owner's passkeys belong to another address,
// as after an upgrade moved Vult to vult.localhost.
func (b *browserOwner) addressMoved(ctx context.Context, admin ledger.Admin) (bool, error) {
	var moved bool
	err := admin.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.passkey_profiles p JOIN access.identities i ON i.id=p.identity_id
 WHERE i.issuer=$1 AND i.subject=$2 AND p.origin<>$3)`, access.BuiltinIssuer, localOwnerSubject, b.configuration.origin()).Scan(&moved)
	return moved, err
}
