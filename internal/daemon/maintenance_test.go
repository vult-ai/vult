package daemon

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// Only the fixed maintenance commands exist; anything else is refused before
// the command looks at the installation.
func TestMaintenanceAcceptsOnlyFixedCommands(t *testing.T) {
	for _, command := range []string{"", "drop", "migrate ", "MIGRATE", "../migrate"} {
		var out bytes.Buffer
		if err := Maintenance(context.Background(), command, nil, &out); !errors.Is(err, ledger.ErrInvalid) {
			t.Errorf("%q: %v", command, err)
		}
		if out.Len() != 0 {
			t.Errorf("%q wrote output", command)
		}
	}
}
