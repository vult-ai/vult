package agentservice

import "unicode/utf8"

// History contains only visible conversation, never executable tool calls.
type historyMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

func recentHistory(c Conversation) []historyMessage {
	const maxTurns, maxBytes, maxText = 40, 32000, 16000
	var out []historyMessage
	used := 0
	for i := len(c.Turns) - 1; i >= 0 && len(out) < maxTurns; i-- {
		t := c.Turns[i]
		role := ""
		switch t.Role {
		case "person":
			role = "user"
		case "agent":
			role = "assistant"
		}
		if role == "" || t.Text == "" || t.Problem != "" {
			continue
		}
		text := t.Text
		if len(text) > maxText {
			end := maxText
			for end > 0 && !utf8.RuneStart(text[end]) {
				end--
			}
			text = text[:end]
		}
		if used+len(text) > maxBytes {
			break
		}
		used += len(text)
		out = append(out, historyMessage{Role: role, Text: text})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
