package agentservice

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
	"github.com/vult-ai/vult/server"
)

func stepOf(t *testing.T, events ...[2]string) step {
	t.Helper()
	var s step
	for _, e := range events {
		s.Events = append(s.Events, event{e[0], json.RawMessage(e[1])})
	}
	return s
}

// Whatever the service sends, the owner is shown only proposals this turn
// prepared, a few short replies and an answer of bounded length.
func TestWhatAStepShowsIsBounded(t *testing.T) {
	var answer server.ChatTurn
	var passed []string
	long, _ := json.Marshal(map[string]string{"text": strings.Repeat("a", maxAnswerText+500)})
	s := stepOf(t,
		[2]string{"proposal", `{"proposal_id":"someone-elses","summary":"record bank activity"}`},
		[2]string{"proposal", `{"proposal_id":"mine","summary":"record bank activity","then":"anything_else"}`},
		[2]string{"replies", `{"options":["Yes","` + strings.Repeat("b", 500) + `","No","Maybe","More"]}`},
		[2]string{"delta", string(long)},
		[2]string{"delta", `{"text":"and more"}`},
		[2]string{"made_up", `{"text":"x"}`},
	)
	takeEvents(&answer, map[string]bool{"mine": true}, s, func(name string, _ any) { passed = append(passed, name) })

	if len(answer.Proposals) != 1 || answer.Proposals[0].ProposalID != "mine" {
		t.Fatalf("proposals shown: %+v", answer.Proposals)
	}
	if answer.Then != "" {
		t.Fatalf("an unknown follow-up was kept: %q", answer.Then)
	}
	if len(answer.Replies) != maxReplies || len([]rune(answer.Replies[1])) != maxReplyText {
		t.Fatalf("replies: %d, second is %d long", len(answer.Replies), len([]rune(answer.Replies[1])))
	}
	if len(answer.Text) > maxAnswerText {
		t.Fatalf("answer is %d long", len(answer.Text))
	}
	if got := strings.Join(passed, ","); got != "proposal,replies,delta" {
		t.Fatalf("events passed on: %s", got)
	}
}

func TestOnlyAnOfferedToolIsRun(t *testing.T) {
	c := &Client{}
	got := c.run(t.Context(), ledger.Scope{Access: "propose"}, map[string]bool{"list_accounts": true}, "c1", "list_documents", nil)
	if !got.Refused {
		t.Fatalf("a tool that wasn't offered was run: %+v", got)
	}
}
