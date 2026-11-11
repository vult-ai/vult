package updatetxn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vult-ai/vult/internal/strictjson"
)

const (
	journalFile  = "state.json"
	lockFile     = "lock"
	startupBlock = "fence"
	maxJournal   = 1 << 20
)

// Journal is the transaction's durable record: one small file replaced
// atomically, written through to stable storage before each step runs.
type Journal struct {
	Dir string
}

// Read returns the current record; a journal never written is idle.
func (j *Journal) Read() (Record, error) {
	data, err := os.ReadFile(filepath.Join(j.Dir, journalFile))
	if errors.Is(err, os.ErrNotExist) {
		return Record{State: StateIdle}, nil
	}
	if err != nil {
		return Record{}, err
	}
	if len(data) > maxJournal {
		return Record{}, errors.New("the update journal is too large")
	}
	var r Record
	if err := strictjson.DecodeNonNull(data, &r, ""); err != nil {
		return Record{}, fmt.Errorf("the update journal is not readable: %w", err)
	}
	if r.State == "" {
		return Record{}, errors.New("the update journal has no state")
	}
	return r, nil
}

// Write replaces the record durably.
func (j *Journal) Write(r Record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return WriteDurably(filepath.Join(j.Dir, journalFile), data, 0o644)
}

// Lock takes the journal's exclusive lock without waiting. Only one process
// may run or recover a transaction at a time.
func (j *Journal) Lock() (unlock func(), err error) {
	return lockExclusive(filepath.Join(j.Dir, lockFile))
}

// BlockStartup survives a reboot. Installed services check it before opening
// the database, so they cannot race transaction recovery at boot.
func (j *Journal) BlockStartup() error {
	return WriteDurably(filepath.Join(j.Dir, startupBlock), []byte("updating\n"), 0o644)
}

// AllowStartup is called only after the live program and data are consistent
// and automatic rollback can no longer replace them.
func (j *Journal) AllowStartup() error {
	err := os.Remove(filepath.Join(j.Dir, startupBlock))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return SyncDir(j.Dir)
}

// StartupReady fails closed on an unreadable stop state. A normal installation
// without an updater has no marker and may start as usual.
func (j *Journal) StartupReady() error {
	_, err := os.Lstat(filepath.Join(j.Dir, startupBlock))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return errors.New("Vult is updating; the update installer must finish before services can start")
}

// WriteDurably replaces file with data so that after a crash it holds either
// the old content or the new, never a mix, and the new content is on stable
// storage before it returns.
func WriteDurably(file string, data []byte, mode os.FileMode) error {
	tmp := file + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = FullSync(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return SyncDir(filepath.Dir(file))
}

// SyncDir makes a directory's entries (renames, creations) durable.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return FullSync(d)
}
