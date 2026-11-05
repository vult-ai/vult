package access

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

type Builtin struct {
	sessions *Service
	username string
	key      []byte
	verify   chan struct{}
}

func sealAuthenticator(key []byte, id, secret string) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, ErrDenied
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, ErrDenied
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, ErrDenied
	}
	return g.Seal(nonce, nonce, []byte(secret), []byte("vult-authenticator-v1:"+id)), nil
}
func openAuthenticator(key []byte, id string, sealed []byte) (string, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return "", ErrDenied
	}
	g, e := cipher.NewGCM(b)
	if e != nil || len(sealed) < g.NonceSize()+g.Overhead() {
		return "", ErrDenied
	}
	plain, e := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], []byte("vult-authenticator-v1:"+id))
	if e != nil {
		return "", ErrDenied
	}
	return string(plain), nil
}

func NewBuiltin(ctx context.Context, s *Service, username string, key []byte) (*Builtin, error) {
	if s == nil || username == "" || len(username) > 80 || len(key) != 32 {
		return nil, ErrDenied
	}
	var ready bool
	if e := s.auth.QueryRow(ctx, "SELECT access.builtin_ready($1,$2)", username, Hash(string(key))).Scan(&ready); e != nil || !ready {
		return nil, ErrDenied
	}
	return &Builtin{sessions: s, username: username, key: append([]byte(nil), key...), verify: make(chan struct{}, 1)}, nil
}

// SignIn ports the bounded password and durable authenticator/recovery checks.
// All usernames consume the configured owner's shared database attempt budget.
func (b *Builtin) SignIn(ctx context.Context, username, password, code string) (string, error) {
	code = strings.TrimSpace(code)
	if len(username) > 80 || len(password) > 1024 || len(code) > 128 {
		return "", ErrDenied
	}
	var id, actual, hash, keyID string
	var sealed []byte
	var epoch int64
	e := b.sessions.auth.QueryRow(ctx, "SELECT id::text,username,password_hash,totp_cipher,key_id,epoch FROM access.reserve_builtin($1)", b.username).Scan(&id, &actual, &hash, &sealed, &keyID, &epoch)
	if e != nil {
		return "", ErrDenied
	}
	select {
	case b.verify <- struct{}{}:
		defer func() { <-b.verify }()
	default:
		return "", ErrDenied
	}
	valid, upgrade := verifyPassword(password, hash)
	if !valid || subtle.ConstantTimeCompare([]byte(username), []byte(actual)) != 1 || keyID != Hash(string(b.key)) {
		return "", ErrDenied
	}
	secret, e := openAuthenticator(b.key, id, sealed)
	if e != nil {
		return "", ErrDenied
	}
	step := int64(-1)
	current := time.Now().Unix() / 30
	for delta := int64(-1); delta <= 1; delta++ {
		candidate := current + delta
		ok, e := totp.ValidateCustom(code, secret, time.Unix(candidate*30, 0), totp.ValidateOpts{Period: 30, Skew: 0, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if e == nil && ok {
			step = candidate
		}
	}
	next := hash
	if upgrade {
		next, e = hashPassword(password)
		if e != nil {
			return "", ErrDenied
		}
	}
	tx, e := b.sessions.auth.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return "", ErrDenied
	}
	defer tx.Rollback(context.Background())
	var identity Identity
	if e = tx.QueryRow(ctx, "SELECT issuer,subject FROM access.finish_builtin($1,$2,$3,$4,$5,$6)", id, epoch, hash, next, step, Hash(code)).Scan(&identity.Issuer, &identity.Subject); e != nil {
		return "", ErrDenied
	}
	raw := Random()
	var who string
	if e = tx.QueryRow(ctx, "SELECT access.issue_session($1,$2,$3,$4,clock_timestamp())::text", identity.Issuer, identity.Subject, b.sessions.origin, Hash(raw)).Scan(&who); e != nil {
		return "", ErrDenied
	}
	if tx.Commit(ctx) != nil {
		return "", ErrDenied
	}
	return raw, nil
}

type Enrollment struct {
	TenantID, AuthenticatorURI string
	RecoveryCodes              []string
}

// InitializeBuiltin is offline setup. Enrollment output contains secrets and
// must be written only to an operator-held private file, never a service log.
func (o Operator) InitializeBuiltin(ctx context.Context, tenant, username, password string, key []byte) (Enrollment, error) {
	if o.DB == nil || len(key) != 32 || len(username) < 1 || len(username) > 80 {
		return Enrollment{}, ErrDenied
	}
	hash, e := hashPassword(password)
	if e != nil {
		return Enrollment{}, ErrDenied
	}
	otpKey, e := totp.Generate(totp.GenerateOpts{Issuer: "Vult", AccountName: username, SecretSize: 20})
	if e != nil {
		return Enrollment{}, ErrDenied
	}
	tx, e := o.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return Enrollment{}, ErrDenied
	}
	defer tx.Rollback(context.Background())
	tenant, user, e := bindOwner(ctx, tx, tenant, Identity{Issuer: BuiltinIssuer, Subject: username})
	if e != nil {
		return Enrollment{}, e
	}
	sealed, e := sealAuthenticator(key, user, otpKey.Secret())
	if e != nil {
		return Enrollment{}, e
	}
	if _, e = tx.Exec(ctx, "INSERT INTO access.builtin_credentials(identity_id,username,password_hash,totp_cipher,key_id) VALUES($1,$2,$3,$4,$5)", user, username, hash, sealed, Hash(string(key))); e != nil {
		return Enrollment{}, ErrDenied
	}
	out := Enrollment{TenantID: tenant, AuthenticatorURI: otpKey.URL()}
	for i := 0; i < 8; i++ {
		buffer := make([]byte, 16)
		if _, e = rand.Read(buffer); e != nil {
			return Enrollment{}, ErrDenied
		}
		code := base64.RawURLEncoding.EncodeToString(buffer)
		out.RecoveryCodes = append(out.RecoveryCodes, code)
		if _, e = tx.Exec(ctx, "INSERT INTO access.recovery_codes(identity_id,code_hash) VALUES($1,$2)", user, Hash(code)); e != nil {
			return Enrollment{}, ErrDenied
		}
	}
	if tx.Commit(ctx) != nil {
		return Enrollment{}, ErrDenied
	}
	return out, nil
}

// ResetBuiltin is explicit offline recovery. It preserves identity and Books,
// replaces both factors, and advances the identity epoch to revoke old access.
func (o Operator) ResetBuiltin(ctx context.Context, username, password string, key []byte) (Enrollment, error) {
	if o.DB == nil || len(key) != 32 {
		return Enrollment{}, ErrDenied
	}
	hash, e := hashPassword(password)
	if e != nil {
		return Enrollment{}, ErrDenied
	}
	otpKey, e := totp.Generate(totp.GenerateOpts{Issuer: "Vult", AccountName: username, SecretSize: 20})
	if e != nil {
		return Enrollment{}, ErrDenied
	}
	tx, e := o.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if e != nil {
		return Enrollment{}, ErrDenied
	}
	defer tx.Rollback(context.Background())
	var user, tenant string
	if e = tx.QueryRow(ctx, "UPDATE access.identities SET epoch=epoch+1 WHERE issuer=$1 AND subject=$2 RETURNING id::text", BuiltinIssuer, username).Scan(&user); e != nil {
		return Enrollment{}, ErrDenied
	}
	if e = tx.QueryRow(ctx, "SELECT tenant_id::text FROM access.owners WHERE identity_id=$1 ORDER BY tenant_id LIMIT 1", user).Scan(&tenant); e != nil {
		return Enrollment{}, ErrDenied
	}
	sealed, e := sealAuthenticator(key, user, otpKey.Secret())
	if e != nil {
		return Enrollment{}, e
	}
	tag, e := tx.Exec(ctx, "UPDATE access.builtin_credentials SET password_hash=$2,totp_cipher=$3,key_id=$4,last_step=-1,enrolled=false,failed_count=0,window_expires_at=clock_timestamp() WHERE identity_id=$1", user, hash, sealed, Hash(string(key)))
	if e != nil || tag.RowsAffected() != 1 {
		return Enrollment{}, ErrDenied
	}
	if _, e = tx.Exec(ctx, "DELETE FROM access.recovery_codes WHERE identity_id=$1", user); e != nil {
		return Enrollment{}, ErrDenied
	}
	out := Enrollment{TenantID: tenant, AuthenticatorURI: otpKey.URL()}
	for i := 0; i < 8; i++ {
		buffer := make([]byte, 16)
		if _, e = rand.Read(buffer); e != nil {
			return Enrollment{}, ErrDenied
		}
		code := base64.RawURLEncoding.EncodeToString(buffer)
		out.RecoveryCodes = append(out.RecoveryCodes, code)
		if _, e = tx.Exec(ctx, "INSERT INTO access.recovery_codes(identity_id,code_hash) VALUES($1,$2)", user, Hash(code)); e != nil {
			return Enrollment{}, ErrDenied
		}
	}
	if tx.Commit(ctx) != nil {
		return Enrollment{}, ErrDenied
	}
	return out, nil
}
