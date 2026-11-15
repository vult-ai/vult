package main

import (
	"context"

	"github.com/vult-ai/vult/internal/agentbridge"
	"github.com/vult-ai/vult/internal/local"
)

// runAgentBridge serves an agent on this computer, reaching the installed Vult.
func runAgentBridge(ctx context.Context, name, imports, exports string) error {
	c, err := local.NewClient()
	if err != nil {
		return err
	}
	return agentbridge.Run(ctx, c, name, imports, exports)
}
