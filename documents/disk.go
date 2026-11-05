package documents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/vult-ai/vult/internal/fssecurity"
)

// Disk keeps encrypted objects in an existing, service-owned private directory.
// Its root handle prevents path traversal even if a path component is replaced.
type Disk struct{ root *os.Root }

func OpenDisk(path string) (*Disk, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrUnavailable
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrUnavailable
	}
	dir, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, ErrUnavailable
	}
	defer dir.Close()
	info, err = dir.Stat()
	if err != nil {
		root.Close()
		return nil, ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 || !fssecurity.NoExtendedACL(dir) {
		root.Close()
		return nil, ErrUnavailable
	}
	return &Disk{root}, nil
}

func (d *Disk) Close() error { return d.root.Close() }

func (d *Disk) private() bool {
	directory, err := d.root.Open(".")
	if err != nil {
		return false
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid()) && fssecurity.NoExtendedACL(directory)
}

func (d *Disk) Create(ctx context.Context, id string, write func(io.Writer) error) error {
	if !objectPattern.MatchString(id) || write == nil {
		return ErrInvalid
	}
	if ctx.Err() != nil || !d.private() {
		return ErrUnavailable
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ErrUnavailable
	}
	temporary := ".pending-" + hex.EncodeToString(nonce[:])
	file, err := d.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrUnavailable
	}
	defer d.root.Remove(temporary)
	defer file.Close()
	if !fssecurity.NoExtendedACL(file) {
		return ErrUnavailable
	}
	if err = write(file); err != nil {
		return storageError(err)
	}
	if ctx.Err() != nil || file.Sync() != nil || file.Close() != nil {
		return ErrUnavailable
	}
	// Link publishes without replacing a previous object, including a symlink.
	if err = d.root.Link(temporary, id); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrExists
		}
		return ErrUnavailable
	}
	if d.root.Remove(temporary) != nil {
		return ErrUnavailable
	}
	dir, err := d.root.Open(".")
	if err != nil {
		return ErrUnavailable
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrUnavailable
	}
	return nil
}

func (d *Disk) Open(ctx context.Context, id string) (io.ReadCloser, error) {
	if !objectPattern.MatchString(id) {
		return nil, ErrInvalid
	}
	if ctx.Err() != nil || !d.private() {
		return nil, ErrUnavailable
	}
	file, err := d.root.OpenFile(id, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > MaxSize+(1<<20) {
		file.Close()
		return nil, ErrUnavailable
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || !fssecurity.NoExtendedACL(file) {
		file.Close()
		return nil, ErrUnavailable
	}
	return file, nil
}

// Remove is a storage primitive, never an owner or agent deletion capability.
// The caller must hold the ledger's cleanup lease before removing any object.
func (d *Disk) Remove(ctx context.Context, id string) error {
	if !objectPattern.MatchString(id) {
		return ErrInvalid
	}
	if ctx.Err() != nil || !d.private() {
		return ErrUnavailable
	}
	if err := d.root.Remove(id); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	return nil
}
