package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/vult-ai/vult/internal/reporting"
	"github.com/vult-ai/vult/ledger"
)

func financialReportQuery(r *http.Request, mode string) (ledger.FinancialReportRequest, url.Values, error) {
	var req ledger.FinancialReportRequest
	if len(r.URL.RawQuery) > 8192 {
		return req, nil, ledger.ErrInvalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q["book_id"]) != 1 || !ledger.ValidID(q.Get("book_id")) {
		return req, nil, ledger.ErrInvalid
	}
	if mode == "export-permission" {
		if len(q) != 1 {
			return req, nil, ledger.ErrInvalid
		}
		return ledger.FinancialReportRequest{BookID: q.Get("book_id")}, q, nil
	}
	allowed := map[string]bool{"book_id": true, "report": true, "from": true, "to": true, "as_of": true, "currency": true, "comparison": true, "compare_from": true, "compare_to": true, "account_ids": true}
	if mode == "contributions" {
		for _, key := range []string{"digest", "row_id", "column_id", "knowledge_cutoff", "generated_at"} {
			allowed[key] = true
		}
	} else {
		allowed["include_csv"] = true
	}
	if mode == "months" {
		allowed["months_from"], allowed["months_to"] = true, true
		if len(q["months_from"]) != 1 || len(q["months_to"]) != 1 || q.Get("include_csv") == "true" {
			return req, nil, ledger.ErrInvalid
		}
	}
	for key, values := range q {
		if !allowed[key] || key != "account_ids" && len(values) != 1 {
			return req, nil, ledger.ErrInvalid
		}
	}
	if v, present := q["include_csv"]; present && (len(v) != 1 || v[0] != "true" && v[0] != "false") {
		return req, nil, ledger.ErrInvalid
	}
	req = ledger.FinancialReportRequest{BookID: q.Get("book_id"), Report: reporting.Kind(q.Get("report")), From: q.Get("from"), To: q.Get("to"), AsOf: q.Get("as_of"), Currency: q.Get("currency"), Comparison: q.Get("comparison"), CompareFrom: q.Get("compare_from"), CompareTo: q.Get("compare_to")}
	for _, value := range q["account_ids"] {
		for _, id := range strings.Split(value, ",") {
			if !ledger.ValidID(id) || len(req.AccountIDs) == 256 {
				return req, nil, ledger.ErrInvalid
			}
			req.AccountIDs = append(req.AccountIDs, id)
		}
	}
	return req, q, nil
}

func (a *webAPI) financialReport(w http.ResponseWriter, r *http.Request, session string) {
	mode := strings.TrimPrefix(r.URL.Path, "/api/v1/financial-reports")
	mode = strings.TrimPrefix(mode, "/")
	req, q, err := financialReportQuery(r, mode)
	if err != nil {
		apiFailure(w, err)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, req.BookID)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out any
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		switch mode {
		case "":
			out, err = owner.FinancialReport(r.Context(), req, q.Get("include_csv") != "false")
		case "months":
			out, err = owner.FinancialReportMonthSet(r.Context(), req, q.Get("months_from"), q.Get("months_to"))
		case "contributions":
			out, err = owner.FinancialContributions(r.Context(), ledger.FinancialContributionRequest{Request: req, Digest: q.Get("digest"), RowID: q.Get("row_id"), ColumnID: q.Get("column_id"), KnowledgeCutoff: q.Get("knowledge_cutoff"), GeneratedAt: q.Get("generated_at")})
		case "export-permission":
			// This boundary admits owners only. Agent read grants never imply
			// owner export authority or reach this browser endpoint.
			_, err = owner.Book(r.Context(), req.BookID)
			out = map[string]any{"version": 1, "book_id": req.BookID, "allowed": true}
		default:
			return ledger.ErrNotFound
		}
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
