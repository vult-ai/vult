package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/vult-ai/vult/internal/statementfile"
	"github.com/vult-ai/vult/ledger"
)

// Statement files the owner uploaded reach the bank activity inbox without
// an agent when Vult can read them in plain code (CSV). Files that need
// reading, such as PDFs and scans, wait for an agent.
const statementFilesPath = "/api/v1/statement-files"

const maxStatementFile = 8 << 20

// activityArrived tells the agent a Book has new activity to work: a
// statement's lines, or bank activity the owner just gave a Book account.
func (a *webAPI) activityArrived(r *http.Request, session, tenant, book string) {
	if a.arrived == nil {
		return
	}
	a.arrived(r.Context(), ledger.AgentTarget{TenantID: tenant, BookID: book}, func(call func(ledger.TenantAdmin, string) error) error {
		return a.sessions.OwnerAct(r.Context(), session, tenant, call)
	})
}

func (a *webAPI) statementFiles(w http.ResponseWriter, r *http.Request, session string) {
	if r.Method != http.MethodPost {
		failure(w, http.StatusNotFound)
		return
	}
	var q struct {
		BookID     string `json:"book_id"`
		DocumentID string `json:"document_id"`
		AccountID  string `json:"account_id"`
	}
	if !readRuleBody(w, r, &q) {
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BookID)
	if !ok {
		return
	}
	var doc ledger.DocumentInspection
	var kind string
	var scale int
	err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		if doc, err = o.Document(r.Context(), q.BookID, q.DocumentID); err != nil {
			return err
		}
		kind, scale, err = o.StatementAccount(r.Context(), q.BookID, q.AccountID)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	if doc.Document.State != "ready" {
		bankAnswer(w, &ledger.RefusedError{Reason: "This file hasn't finished uploading."})
		return
	}
	if doc.Document.MediaType != "text/csv" || doc.Document.Size > maxStatementFile {
		bankAnswer(w, &ledger.RefusedError{Reason: "Vult reads CSV statements by itself. An agent reads PDFs and scans: turn one on in Settings → Agent, or connect one."})
		return
	}
	var data bytes.Buffer
	if a.readDocument != nil {
		err = a.readDocument(r.Context(), session, q.BookID, q.DocumentID, &data)
	} else {
		err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			return o.DownloadDocument(r.Context(), q.BookID, q.DocumentID, a.documents, &data)
		})
	}
	if bankAnswer(w, err) {
		return
	}
	read, err := statementfile.ReadCSV(data.Bytes(), kind, scale)
	var problem *statementfile.Problem
	if errors.As(err, &problem) {
		bankAnswer(w, &ledger.RefusedError{Reason: problem.Reason})
		return
	}
	if bankAnswer(w, err) {
		return
	}
	file := ledger.StatementFile{AccountID: q.AccountID, DocumentID: q.DocumentID, PeriodStart: read.PeriodStart, PeriodEnd: read.PeriodEnd, Balances: read.Balances,
		OpeningMinor: strconv.FormatInt(read.Opening, 10), ClosingMinor: strconv.FormatInt(read.Closing, 10)}
	for _, l := range read.Lines {
		file.Rows = append(file.Rows, ledger.StatementRowInput{RowNumber: l.Row, Date: l.Date, AmountMinor: strconv.FormatInt(l.AmountMinor, 10),
			Description: l.Description, ExternalID: l.ExternalID, Raw: l.Raw})
	}
	var out json.RawMessage
	err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		out, err = o.ImportStatementFile(r.Context(), q.BookID, who, file)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	a.activityArrived(r, session, tenant, q.BookID)
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "lines": len(file.Rows), "balances": read.Balances, "statement": out})
}
