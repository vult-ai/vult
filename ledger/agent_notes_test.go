package ledger_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

// An agent asks once per subject, the owner's answer settles it, and only
// the owner changing it reopens it. Loose ends close with what they wait
// on; the person's profile changes only from the revision read.
func TestAgentsKeepQuestionsLoopsAndAProfileTheOwnerCanSee(t *testing.T) {
	f := newBankFixture(t)
	ctx := f.db.Context
	f.sync(t, "c1", "c2", []ledger.BankSyncTransaction{f.tx("acc-checking", "x", "2026-04-02", -2500, "Example Mystery Shop")}, nil)
	item := byDescription(f.inbox(t, f.s, "all"), "Example Mystery Shop")
	agent := connectionScope(t, f.db, f.book.ID, "propose")
	reader := connectionScope(t, f.db, f.book.ID, "read")
	ask := func(s ledger.Scope) (ledger.AgentQuestion, error) {
		var q ledger.AgentQuestion
		out, err := f.db.Store.Call(ctx, s, "ask_question", value(ledger.AskQuestion{SubjectKind: "bank_item", SubjectKey: item.ID, Question: "What was this purchase?"}))
		if err == nil {
			json.Unmarshal(out, &q)
		}
		return q, err
	}
	if _, err := ask(reader); !errors.Is(err, ledger.ErrDenied) {
		t.Fatalf("a read connection asked: %v", err)
	}
	q, err := ask(agent)
	succeeded(t, err)
	bankRefused(t, func() error { _, err := ask(agent); return err }(), "Already asked")
	// A loop waiting on the question, and one on the item.
	open := func(key, kind, subject string) ledger.AgentLoop {
		var l ledger.AgentLoop
		json.Unmarshal(f.db.Call(t, agent, "open_loop", ledger.OpenLoop{RequestKey: key, Kind: "follow_up", SubjectKind: kind, SubjectKey: subject, Text: "Record it once the owner says"}), &l)
		return l
	}
	onQuestion, onItem := open("q", "question", q.ID), open("i", "bank_item", item.ID)
	if again := open("q", "question", q.ID); again.ID != onQuestion.ID {
		t.Fatal("a retried loop was opened twice")
	}
	answered, err := f.owner.AnswerQuestion(ctx, f.book.ID, f.actor, q.ID, json.RawMessage(`"Office chairs"`))
	succeeded(t, err)
	if answered.Status != "answered" || string(answered.Answer) != `"Office chairs"` {
		t.Fatalf("answered = %+v", answered)
	}
	bankRefused(t, func() error { _, err := ask(agent); return err }(), `Already answered on`)
	// The owner changing the answer keeps the history.
	changed, err := f.owner.AnswerQuestion(ctx, f.book.ID, f.actor, q.ID, json.RawMessage(`"Desks"`))
	succeeded(t, err)
	all, err := f.owner.AgentQuestions(ctx, f.book.ID, ledger.QuestionQuery{Status: "superseded"})
	succeeded(t, err)
	if changed.ID == q.ID || len(all) != 1 || all[0].ID != q.ID {
		t.Fatalf("changed = %+v, superseded = %+v", changed, all)
	}
	// Recording the item closes its loop; the answer closed the other.
	f.db.Call(t, f.s, "record_bank_activity", ledger.RecordBankActivity{RequestKey: "desk", TransactionID: item.ID, Description: "Desks", OtherAccountID: f.office.ID})
	var loops struct {
		Loops []ledger.AgentLoop `json:"loops"`
	}
	json.Unmarshal(f.db.Call(t, agent, "list_agent_loops", ledger.LoopQuery{Status: "all"}), &loops)
	for _, l := range loops.Loops {
		if l.Status != "done" || (l.ID == onItem.ID) != (l.ClosedReason == "The item was recorded.") {
			t.Fatalf("loop = %+v", l)
		}
	}
	// At most 50 open loops.
	for i := 0; i < 51; i++ {
		_, err = f.db.Store.Call(ctx, agent, "open_loop", value(ledger.OpenLoop{RequestKey: fmt.Sprintf("r%d", i), Kind: "reminder", Text: "Example reminder"}))
	}
	bankRefused(t, err, "50 open loops")
	// The profile changes from the revision read, with a reason.
	goal := "Keep three months of costs in savings"
	if _, err := f.db.Store.Call(ctx, agent, "update_agent_profile", value(ledger.UpdateAgentProfile{ExpectedRevision: 0, Goal: &goal, Reason: "They said: keep three months in savings"})); err != nil {
		t.Fatal(err)
	}
	_, err = f.db.Store.Call(ctx, agent, "update_agent_profile", value(ledger.UpdateAgentProfile{ExpectedRevision: 0, Goal: &goal, Reason: "again"}))
	bankRefused(t, err, "changed since you read it")
	profile, err := f.owner.AgentProfile(ctx, f.book.ID)
	succeeded(t, err)
	if profile.Goal != goal || profile.Revision != 1 || !strings.Contains(profile.Reason, "They said") {
		t.Fatalf("profile = %+v", profile)
	}
	var summary ledger.AgentSummary
	json.Unmarshal(f.db.Call(t, agent, "get_agent_summary", map[string]string{}), &summary)
	if summary.Waiting.Questions != 0 {
		t.Fatalf("summary = %+v", summary.Waiting)
	}
}
