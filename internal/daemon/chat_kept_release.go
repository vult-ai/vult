//go:build !devweb

package daemon

import "github.com/vult-ai/vult/agentservice"

// keptConversations keeps conversations until this program stops.
func keptConversations() agentservice.Conversations { return &agentservice.Memory{} }
