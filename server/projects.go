package server

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/vult-ai/vult/ledger"
)

// Projects in a Book: a light label on spending, a report of what each
// project cost, and rules that label a counterparty's charges.
const (
	projectsPath       = "/api/v1/projects"
	projectsLabelPath  = "/api/v1/projects/label"
	projectsReportPath = "/api/v1/projects/report"
	projectsReportCSV  = "/api/v1/projects/report.csv"
	projectsRulesPath  = "/api/v1/projects/rules"
	projectsRuleDelete = "/api/v1/projects/rules/delete"
)

func projectsRoute(path string) bool {
	return path == projectsPath || strings.HasPrefix(path, projectsPath+"/")
}

func (a *webAPI) projects(w http.ResponseWriter, r *http.Request, session string) {
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && (p == projectsPath || p == projectsReportPath || p == projectsReportCSV || p == projectsRulesPath):
		q := r.URL.Query()
		book := q.Get("book_id")
		year := 0
		if y := q.Get("year"); y != "" && p != projectsPath {
			var err error
			if year, err = strconv.Atoi(y); err != nil {
				apiFailure(w, ledger.ErrInvalid)
				return
			}
		}
		tenant, err := a.sessions.BookTenant(r.Context(), session, book)
		if err != nil {
			apiFailure(w, err)
			return
		}
		var out any
		var report ledger.ProjectReport
		err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			switch p {
			case projectsPath:
				var list []ledger.Project
				list, err = o.Projects(r.Context(), book)
				out = map[string]any{"projects": list}
			case projectsRulesPath:
				var rules []ledger.ProjectRule
				rules, err = o.ProjectRules(r.Context(), book)
				out = map[string]any{"rules": rules}
			default:
				report, err = o.ProjectReport(r.Context(), book, year)
				out = report
			}
			return err
		})
		if err != nil {
			apiFailure(w, err)
			return
		}
		if p == projectsReportCSV {
			writeProjectCSV(w, report)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost:
		var q struct {
			BookID     string                `json:"book_id"`
			RequestKey string                `json:"request_key"`
			Name       *string               `json:"name,omitempty"`
			StartsOn   *string               `json:"starts_on,omitempty"`
			EndsOn     *string               `json:"ends_on,omitempty"`
			Archived   *bool                 `json:"archived,omitempty"`
			BankItemID string                `json:"bank_item_id,omitempty"`
			AccountID  string                `json:"account_id,omitempty"`
			ProjectID  *string               `json:"project_id,omitempty"`
			EntryID    string                `json:"entry_id,omitempty"`
			Splits     []ledger.ProjectSplit `json:"splits,omitempty"`
			ID         string                `json:"id,omitempty"`
			// A rule: the counterparty as the bank shows it, and optionally
			// one bank account; starts_on and ends_on bound it.
			Counterparty  string `json:"counterparty,omitempty"`
			BankAccountID string `json:"bank_account_id,omitempty"`
			// Turning a project into a company.
			FormationDate *string  `json:"formation_date,omitempty"`
			TaxedAs       string   `json:"taxed_as,omitempty"`
			EntryIDs      []string `json:"entry_ids,omitempty"`
		}
		if !readRuleBody(w, r, &q) {
			return
		}
		tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
		if err != nil {
			apiFailure(w, err)
			return
		}
		var out any
		err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			change := ledger.ProjectChange{RequestKey: q.RequestKey, Name: q.Name, StartsOn: q.StartsOn, EndsOn: q.EndsOn, Archived: q.Archived}
			switch {
			case p == projectsPath:
				v, err := o.CreateProject(r.Context(), q.BookID, who, change)
				out = v
				return err
			case p == projectsLabelPath && q.BankItemID != "":
				if q.ProjectID == nil {
					return ledger.ErrInvalid
				}
				entry, err := o.LabelCharge(r.Context(), q.BookID, who, q.BankItemID, q.AccountID, *q.ProjectID)
				out = map[string]string{"entry_id": entry}
				return err
			case p == projectsLabelPath:
				splits := q.Splits
				if splits == nil && q.ProjectID != nil {
					splits = []ledger.ProjectSplit{{ProjectID: *q.ProjectID}}
				}
				labels, err := o.LabelEntry(r.Context(), q.BookID, who, q.EntryID, splits)
				out = map[string]any{"entry_id": q.EntryID, "labels": labels}
				return err
			case p == projectsRulesPath:
				rule := ledger.ProjectRule{CounterpartyKey: q.Counterparty, BankAccountID: q.BankAccountID}
				if q.ProjectID != nil {
					rule.ProjectID = *q.ProjectID
				}
				if q.StartsOn != nil {
					rule.StartsOn = *q.StartsOn
				}
				if q.EndsOn != nil {
					rule.EndsOn = *q.EndsOn
				}
				v, err := o.SetProjectRule(r.Context(), q.BookID, who, rule)
				out = v
				return err
			case p == projectsRuleDelete:
				out = map[string]string{"id": q.ID, "status": "deleted"}
				return o.DeleteProjectRule(r.Context(), q.BookID, who, q.ID)
			case p == projectsPath+"/promote/undo":
				out = map[string]string{"request_key": q.RequestKey, "status": "undone"}
				return o.UndoPromotion(r.Context(), q.BookID, who, q.RequestKey)
			case strings.HasSuffix(p, "/promote"):
				id := strings.TrimSuffix(strings.TrimPrefix(p, projectsPath+"/"), "/promote")
				promotion := ledger.PromoteProject{RequestKey: q.RequestKey, TaxedAs: q.TaxedAs, EntryIDs: q.EntryIDs}
				if q.Name != nil {
					promotion.Name = *q.Name
				}
				if q.FormationDate != nil {
					promotion.FormationDate = *q.FormationDate
				}
				v, err := o.Promote(r.Context(), q.BookID, who, id, promotion)
				out = v
				return err
			case strings.HasSuffix(p, "/promote/preview"):
				id := strings.TrimSuffix(strings.TrimPrefix(p, projectsPath+"/"), "/promote/preview")
				formation := ""
				if q.FormationDate != nil {
					formation = *q.FormationDate
				}
				costs, err := o.PromotionPreview(r.Context(), q.BookID, id, formation)
				out = map[string]any{"costs": costs}
				return err
			case strings.Count(strings.TrimPrefix(p, projectsPath+"/"), "/") == 0:
				v, err := o.UpdateProject(r.Context(), q.BookID, who, strings.TrimPrefix(p, projectsPath+"/"), change)
				out = v
				return err
			}
			return ledger.ErrNotFound
		})
		if err != nil {
			apiFailure(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	default:
		failure(w, http.StatusNotFound)
	}
}

// writeProjectCSV writes the report as rows an accountant can read: one per
// project, year and account, then what is in no project.
func writeProjectCSV(w http.ResponseWriter, report ledger.ProjectReport) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="projects.csv"`)
	out := csv.NewWriter(w)
	_ = out.Write([]string{"project", "year", "account", "amount_minor", "receipts_missing"})
	for _, p := range report.Projects {
		for _, y := range p.ByYear {
			for _, k := range y.ByKind {
				_ = out.Write([]string{p.Name, strconv.Itoa(y.Year), k.AccountName, k.AmountMinor, strconv.Itoa(y.ReceiptGaps.Count)})
			}
		}
	}
	for _, u := range report.Unlabelled {
		_ = out.Write([]string{"(no project)", strconv.Itoa(u.Year), "", u.AmountMinor, ""})
	}
	out.Flush()
}
