package daemon

import (
	"path/filepath"

	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/internal/updatetxn"
)

func updateStartupReady() error {
	return (&updatetxn.Journal{Dir: filepath.Join(updatestatus.Dir, "journal")}).StartupReady()
}
