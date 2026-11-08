package access

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/vult-ai/vult/ledger"
)

// PasskeySetup contains one-use setup authority and optional new recovery codes.
// Deliver it only through a protected operator channel, never service logs.
type PasskeySetup struct {
	TenantID      string
	Token         string
	RecoveryCodes []string
}

func (o Operator) InitializePasskeys(ctx context.Context, tenant, subject, origin string) (PasskeySetup, error) {
	creating := tenant == ""
	if o.DB == nil || len(subject) == 0 || len(subject) > 80 || tenant != "" && !ledger.ValidID(tenant) {
		return PasskeySetup{}, ErrDenied
	}
	if _, err := newPasskeyProtocol(origin); err != nil {
		return PasskeySetup{}, err
	}
	tx, err := o.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PasskeySetup{}, ErrDenied
	}
	defer tx.Rollback(context.Background())
	var who string
	if tenant == "" {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access.identities i JOIN access.owners o ON o.identity_id=i.id
 WHERE i.issuer=$1 AND i.subject=$2)`, BuiltinIssuer, subject).Scan(&exists); err != nil || exists {
			return PasskeySetup{}, ErrDenied
		}
	}
	if tenant != "" {
		err = tx.QueryRow(ctx, `SELECT i.id::text FROM access.owners o JOIN access.identities i ON i.id=o.identity_id
 WHERE o.tenant_id=$1 AND i.issuer=$2 AND i.subject=$3 FOR UPDATE OF i`, tenant, BuiltinIssuer, subject).Scan(&who)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return PasskeySetup{}, ErrDenied
		}
	}
	if who == "" {
		tenant, who, err = bindOwner(ctx, tx, tenant, Identity{Issuer: BuiltinIssuer, Subject: subject})
		if err != nil {
			return PasskeySetup{}, err
		}
	}
	var epoch int64
	if err := tx.QueryRow(ctx, "SELECT epoch FROM access.identities WHERE id=$1 FOR UPDATE", who).Scan(&epoch); err != nil {
		return PasskeySetup{}, ErrDenied
	}
	inserted, err := tx.Exec(ctx, "INSERT INTO access.passkey_profiles(identity_id,origin) VALUES($1,$2) ON CONFLICT(identity_id) DO NOTHING", who, origin)
	if err != nil || creating && inserted.RowsAffected() != 1 {
		return PasskeySetup{}, ErrDenied
	}
	var ready bool
	if err := tx.QueryRow(ctx, `SELECT p.state='pending' AND p.origin=$2 AND NOT EXISTS
 (SELECT 1 FROM access.passkey_credentials c WHERE c.identity_id=p.identity_id AND c.revoked_at IS NULL)
 FROM access.passkey_profiles p WHERE p.identity_id=$1 FOR UPDATE`, who, origin).Scan(&ready); err != nil || !ready {
		return PasskeySetup{}, ErrDenied
	}
	out, err := preparePasskeySetup(ctx, tx, tenant, who, epoch, false)
	if err != nil || tx.Commit(ctx) != nil {
		return PasskeySetup{}, ErrDenied
	}
	return out, nil
}

// ResetPasskeys is explicit offline recovery. It retains Books and identities,
// invalidates prior sign-ins and ceremonies, and requires fresh registration.
func (o Operator) ResetPasskeys(ctx context.Context, subject, origin string) (PasskeySetup, error) {
	if o.DB == nil || len(subject) == 0 || len(subject) > 80 {
		return PasskeySetup{}, ErrDenied
	}
	if _, err := newPasskeyProtocol(origin); err != nil {
		return PasskeySetup{}, err
	}
	tx, err := o.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return PasskeySetup{}, ErrDenied
	}
	defer tx.Rollback(context.Background())
	var who, tenant string
	var epoch int64
	if err := tx.QueryRow(ctx, `UPDATE access.identities i SET epoch=epoch+1 WHERE issuer=$1 AND subject=$2
 AND EXISTS (SELECT 1 FROM access.passkey_profiles p WHERE p.identity_id=i.id)
 RETURNING id::text,epoch`, BuiltinIssuer, subject).Scan(&who, &epoch); err != nil {
		return PasskeySetup{}, ErrDenied
	}
	if err := tx.QueryRow(ctx, "SELECT tenant_id::text FROM access.owners WHERE identity_id=$1 ORDER BY tenant_id LIMIT 1", who).Scan(&tenant); err != nil {
		return PasskeySetup{}, ErrDenied
	}
	if _, err := tx.Exec(ctx, "UPDATE access.passkey_credentials SET revoked_at=clock_timestamp() WHERE identity_id=$1 AND revoked_at IS NULL", who); err != nil {
		return PasskeySetup{}, ErrDenied
	}
	if _, err := tx.Exec(ctx, "UPDATE access.passkey_profiles SET origin=$2,state='pending',attempt_count=0,attempt_until=clock_timestamp() WHERE identity_id=$1", who, origin); err != nil {
		return PasskeySetup{}, ErrDenied
	}
	if _, err := tx.Exec(ctx, "DELETE FROM access.recovery_codes WHERE identity_id=$1", who); err != nil {
		return PasskeySetup{}, ErrDenied
	}
	out, err := preparePasskeySetup(ctx, tx, tenant, who, epoch, true)
	if err != nil || tx.Commit(ctx) != nil {
		return PasskeySetup{}, ErrDenied
	}
	return out, nil
}

func preparePasskeySetup(ctx context.Context, tx pgx.Tx, tenant, who string, epoch int64, recovery bool) (PasskeySetup, error) {
	if _, err := tx.Exec(ctx, "UPDATE access.passkey_setups SET consumed_at=clock_timestamp() WHERE identity_id=$1 AND consumed_at IS NULL", who); err != nil {
		return PasskeySetup{}, ErrDenied
	}
	out := PasskeySetup{TenantID: tenant, Token: Random()}
	if _, err := tx.Exec(ctx, "INSERT INTO access.passkey_setups(token_hash,identity_id,epoch) VALUES($1,$2,$3)", Hash(out.Token), who, epoch); err != nil {
		return PasskeySetup{}, ErrDenied
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM access.recovery_codes WHERE identity_id=$1", who).Scan(&count); err != nil {
		return PasskeySetup{}, ErrDenied
	}
	if count == 0 {
		var err error
		out.RecoveryCodes, err = addRecoveryCodes(ctx, tx, who)
		if err != nil {
			return PasskeySetup{}, err
		}
	}
	kind := "setup"
	if recovery {
		kind = "recovery"
	}
	if _, err := tx.Exec(ctx, "INSERT INTO access.passkey_events(identity_id,kind) VALUES($1,$2)", who, kind); err != nil {
		return PasskeySetup{}, ErrDenied
	}
	return out, nil
}
