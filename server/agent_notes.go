package server

import (
	"encoding/json"
	"net/http"

	"github.com/vult-ai/vult/ledger"
)

// What the Book's agents asked, promised and remember, for the owner:
// answers to questions, the profile they keep, and when the owner last
// looked. Agents keep these over MCP; the owner reads and changes them here.
const agentNotesPath = "/api/v1/agent-notes"

func (a *webAPI) agentNotes(w http.ResponseWriter, r *http.Request, session string) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == agentNotesPath:
		book := r.URL.Query().Get("book_id")
		tenant, ok := a.bankBook(w, r, session, book)
		if !ok {
			return
		}
		var questions []ledger.AgentQuestion
		var profile ledger.AgentProfile
		err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			if questions, err = o.AgentQuestions(r.Context(), book, ledger.QuestionQuery{}); err != nil {
				return err
			}
			profile, err = o.AgentProfile(r.Context(), book)
			return err
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "questions": questions, "profile": profile})
	case r.Method == http.MethodPost && (r.URL.Path == agentNotesPath+"/answer" || r.URL.Path == agentNotesPath+"/profile" || r.URL.Path == agentNotesPath+"/seen"):
		var q struct {
			BookID     string          `json:"book_id"`
			QuestionID string          `json:"question_id"`
			Answer     json.RawMessage `json:"answer"`
			ledger.UpdateAgentProfile
		}
		if !readRuleBody(w, r, &q) {
			return
		}
		tenant, ok := a.bankBook(w, r, session, q.BookID)
		if !ok {
			return
		}
		var out any
		err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			var err error
			switch r.URL.Path {
			case agentNotesPath + "/answer":
				out, err = o.AnswerQuestion(r.Context(), q.BookID, who, q.QuestionID, q.Answer)
			case agentNotesPath + "/profile":
				out, err = o.SetAgentProfile(r.Context(), q.BookID, who, q.UpdateAgentProfile)
			default:
				before, e := o.MarkSeen(r.Context(), q.BookID, who)
				out, err = map[string]any{"previously_seen_at": before}, e
			}
			return err
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		failure(w, http.StatusNotFound)
	}
}
