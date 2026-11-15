package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/vult-ai/vult/ledger"
)

// The owner's recording rules: only the owner makes, changes or grants
// them, from a fresh sign-in. Agents read them over MCP and record by them
// only when granted.
const recordingRulesPath = "/api/v1/recording-rules"

func recordingRulesRoute(path string) bool {
	return path == recordingRulesPath || strings.HasPrefix(path, recordingRulesPath+"/")
}

func (a *webAPI) recordingRules(w http.ResponseWriter, r *http.Request, session string) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == recordingRulesPath:
		q := r.URL.Query()
		book := q.Get("book_id")
		if len(q) != 1 {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		tenant, ok := a.bankBook(w, r, session, book)
		if !ok {
			return
		}
		var rules []ledger.RecordingRule
		var grants []ledger.RuleGrant
		var policy ledger.AgentPolicy
		var kinds []ledger.AgentKind
		err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			if rules, grants, err = o.RecordingRules(r.Context(), book); err != nil {
				return err
			}
			if policy, err = o.AgentPolicy(r.Context(), book); err != nil {
				return err
			}
			kinds, err = o.AgentKinds(r.Context(), book)
			return err
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "rules": rules, "grants": grants, "policy": policy, "kinds": kinds})
	case r.Method == http.MethodGet && r.URL.Path == recordingRulesPath+"/summary":
		q := r.URL.Query()
		book := q.Get("book_id")
		tenant, ok := a.bankBook(w, r, session, book)
		if !ok {
			return
		}
		var out ledger.AgentSummary
		err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			out, err = o.AgentSummary(r.Context(), book, ledger.AgentSummaryQuery{Since: q.Get("since")})
			return err
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && (r.URL.Path == recordingRulesPath+"/policy" || r.URL.Path == recordingRulesPath+"/tax" || r.URL.Path == recordingRulesPath+"/kinds/accept"):
		var q struct {
			BookID           string `json:"book_id"`
			AmountCapMinor   string `json:"amount_cap_minor"`
			ExpectedRevision int64  `json:"expected_revision"`
			AccountID        string `json:"account_id"`
			On               bool   `json:"on"`
			Direction        string `json:"direction"`
			Counterparty     string `json:"counterparty"`
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
			case recordingRulesPath + "/policy":
				out, err = o.SetAgentCap(r.Context(), q.BookID, who, q.AmountCapMinor, q.ExpectedRevision)
			case recordingRulesPath + "/tax":
				err = o.MarkTaxSensitive(r.Context(), q.BookID, who, q.AccountID, q.On)
				out = map[string]bool{"on": q.On}
			default:
				out, err = o.AcceptKindOffer(r.Context(), q.BookID, who, q.AccountID, q.Direction, q.Counterparty)
			}
			return err
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && r.URL.Path == recordingRulesPath:
		var q struct {
			BookID string `json:"book_id"`
			ledger.RuleFromActivity
		}
		if !readRuleBody(w, r, &q) {
			return
		}
		tenant, ok := a.bankBook(w, r, session, q.BookID)
		if !ok {
			return
		}
		var out ledger.RecordingRule
		err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			var err error
			out, err = o.CreateRecordingRule(r.Context(), q.BookID, who, q.RuleFromActivity)
			return err
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && (r.URL.Path == recordingRulesPath+"/state" || r.URL.Path == recordingRulesPath+"/grant"):
		var q struct {
			BookID       string `json:"book_id"`
			RuleID       string `json:"rule_id"`
			ConnectionID string `json:"connection_id"`
			On           bool   `json:"on"`
			// Mode is "rules" or "confident"; "on" alone means rules.
			Mode string `json:"mode"`
		}
		if !readRuleBody(w, r, &q) {
			return
		}
		tenant, ok := a.bankBook(w, r, session, q.BookID)
		if !ok {
			return
		}
		err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			if strings.HasSuffix(r.URL.Path, "/grant") {
				mode := q.Mode
				if mode == "" && q.On {
					mode = ledger.RecordByRules
				}
				if !q.On {
					mode = ledger.RecordDraftsOnly
				}
				return o.SetRecordingMode(r.Context(), q.BookID, who, q.ConnectionID, mode)
			}
			return o.SetRecordingRule(r.Context(), q.BookID, who, q.RuleID, q.On)
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"on": q.On})
	case r.Method == http.MethodPost && r.URL.Path == recordingRulesPath+"/parts":
		var q struct {
			BookID string            `json:"book_id"`
			RuleID string            `json:"rule_id"`
			Parts  []ledger.RulePart `json:"parts"`
		}
		if !readRuleBody(w, r, &q) {
			return
		}
		tenant, ok := a.bankBook(w, r, session, q.BookID)
		if !ok {
			return
		}
		var out ledger.RecordingRule
		err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			var err error
			out, err = o.SetRuleParts(r.Context(), q.BookID, who, q.RuleID, q.Parts)
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

func readRuleBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(out); err != nil {
		apiFailure(w, ledger.ErrInvalid)
		return false
	}
	return true
}
