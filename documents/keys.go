package documents

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"syscall"

	"filippo.io/age"
	"github.com/vult-ai/vult/internal/fssecurity"
	"github.com/vult-ai/vult/internal/strictjson"
)

type TenantKeys struct {
	Active string            `json:"active"`
	Keys   map[string]string `json:"keys"`
}

// KeyFile is private service configuration, never a database or export record.
type KeyFile struct {
	Format  string                `json:"format"`
	Tenants map[string]TenantKeys `json:"tenants"`
}

type Keyring struct{ tenants map[string]tenantKeys }
type tenantKeys struct {
	active string
	keys   map[string]*age.X25519Identity
}

func ParseKeyring(data []byte) (*Keyring, error) {
	var file KeyFile
	if len(data) > 1<<20 || strictjson.Decode(data, &file) != nil || file.Format != "vult-document-keys-v1" || len(file.Tenants) == 0 {
		return nil, ErrUnavailable
	}
	ring := &Keyring{tenants: map[string]tenantKeys{}}
	for tenant, versions := range file.Tenants {
		if !idPattern.MatchString(tenant) || !keyPattern.MatchString(versions.Active) || len(versions.Keys) == 0 {
			return nil, ErrUnavailable
		}
		keys := tenantKeys{versions.Active, map[string]*age.X25519Identity{}}
		for version, secret := range versions.Keys {
			if !keyPattern.MatchString(version) {
				return nil, ErrUnavailable
			}
			identity, err := age.ParseX25519Identity(secret)
			if err != nil {
				return nil, ErrUnavailable
			}
			keys.keys[version] = identity
		}
		if keys.keys[keys.active] == nil {
			return nil, ErrUnavailable
		}
		ring.tenants[tenant] = keys
	}
	return ring, nil
}

func OpenKeyring(path string) (*Keyring, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return nil, ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) && os.Geteuid() != 0 || !fssecurity.NoExtendedACL(file) {
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, ErrUnavailable
	}
	return ParseKeyring(data)
}

// NewKeyFile creates one initial tenant key; callers create the private file
// exclusively, synchronize it, and retain it across uninstall and upgrades.
func NewKeyFile(tenant string) ([]byte, error) {
	if !idPattern.MatchString(tenant) {
		return nil, ErrInvalid
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, ErrUnavailable
	}
	return json.Marshal(KeyFile{Format: "vult-document-keys-v1", Tenants: map[string]TenantKeys{
		tenant: {Active: "primary", Keys: map[string]string{"primary": identity.String()}},
	}})
}

func (k *Keyring) Current(ctx context.Context, tenant string) (string, error) {
	if ctx.Err() != nil || k == nil || k.tenants[tenant].active == "" {
		return "", ErrUnavailable
	}
	return k.tenants[tenant].active, nil
}

func (k *Keyring) Recipient(ctx context.Context, tenant, version string) (age.Recipient, error) {
	identity, err := k.Identity(ctx, tenant, version)
	if err != nil {
		return nil, err
	}
	return identity.(*age.X25519Identity).Recipient(), nil
}

func (k *Keyring) Identity(ctx context.Context, tenant, version string) (age.Identity, error) {
	if ctx.Err() != nil || k == nil || k.tenants[tenant].keys[version] == nil {
		return nil, ErrUnavailable
	}
	return k.tenants[tenant].keys[version], nil
}
