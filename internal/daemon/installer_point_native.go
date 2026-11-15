package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/vult-ai/vult/internal/local"
	"github.com/vult-ai/vult/internal/recovery"
	"github.com/vult-ai/vult/internal/updatestatus"
)

func nativePointSite(root string) recovery.Site {
	return recovery.Site{Root: root, Data: local.DataPath, Clean: pgClean, Clone: recovery.Clone}
}

// InstallerPoint is the installer's step around updating the database:
//
//	take          before the update, when this Mac already has a Vult;
//	              prints the point's name, or nothing for a new install
//	restore NAME  after a failed update: puts the data back as it was
//	finish NAME   after a successful update: keeps the newest points only
func InstallerPoint(ctx context.Context, args []string, out io.Writer) error {
	if err := requireNativeAdministrator(); err != nil {
		return err
	}
	switch {
	case len(args) == 1 && args[0] == "take":
		uid, _, err := serviceIdentity()
		if err != nil {
			return err
		}
		// A new install has no data yet, and one that never finished
		// installing has nothing to keep.
		if _, err := os.Lstat(filepath.Join(local.DataPath, "configuration.json")); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		conf, err := loadConfiguration(uid)
		if err != nil {
			return err
		}
		if conf.State != "ready" {
			return nil
		}
		if _, err := os.Lstat(filepath.Join(local.DataPath, "database", "postmaster.pid")); !errors.Is(err, os.ErrNotExist) {
			return errors.New("the database is still running")
		}
		root, err := recoveryRoot()
		if err != nil {
			return err
		}
		p, err := nativePointSite(root).Take(installedBefore(), Version)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, p.ID)
		return err
	case len(args) == 2 && args[0] == "restore":
		if err := NativeStop(ctx); err != nil {
			return err
		}
		root, err := recoveryRoot()
		if err != nil {
			return err
		}
		return nativePointSite(root).Restore(args[1])
	case len(args) == 2 && args[0] == "finish":
		root, err := recoveryRoot()
		if err != nil {
			return err
		}
		_, err = nativePointSite(root).Finish(args[1])
		return err
	}
	return errors.New("usage: _installer_point take | restore NAME | finish NAME")
}

// installedBefore is the version the update installer last recorded, when
// this Mac has one.
func installedBefore() string {
	if updatestatus.Dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(updatestatus.Dir, "installed.json"))
	if err != nil {
		return ""
	}
	var v struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	return v.Version
}
