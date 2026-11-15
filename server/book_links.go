package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vult-ai/vult/ledger"
)

const (
	bookLinksPath       = "/api/v1/book-links"
	pairsPath           = "/api/v1/pairs"
	settlementsPath     = "/api/v1/settlements"
	settlementPreview   = "/api/v1/settlements/preview"
	linkedBalancesPath  = "/api/v1/linked-balances"
	assignmentRulesPath = "/api/v1/assignment-rules"
	openingBalancePath  = "/api/v1/book-links/opening"
)

// bookLinks serves the owner's links between Books: listing every link (or
// one Book's), setting one direction of a link, and removing a link.
func (a *webAPI) bookLinks(w http.ResponseWriter, r *http.Request, session string) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		book := q.Get("book_id")
		if len(q) > 1 || len(q) == 1 && book == "" {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		var tenants []string
		if book != "" {
			tenant, ok := a.bankBook(w, r, session, book)
			if !ok {
				return
			}
			tenants = []string{tenant}
		} else {
			var err error
			if tenants, err = a.sessions.TenantsBounded(r.Context(), session, webBookLimit); err != nil {
				apiFailure(w, err)
				return
			}
		}
		out := []ledger.OwnerLink{}
		for _, tenant := range tenants {
			err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
				links, err := o.OwnerLinks(r.Context(), book)
				out = append(out, links...)
				return err
			})
			if bankAnswer(w, err) {
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"links": out})
	case http.MethodPost:
		var q ledger.LinkTermsChange
		if !readRuleBody(w, r, &q) {
			return
		}
		tenant, ok := a.bankBook(w, r, session, q.PayerBookID)
		if !ok {
			return
		}
		var out ledger.OwnerLink
		err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			var err error
			out, err = o.SetLinkTerms(r.Context(), who, q)
			return err
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"link": out})
	case http.MethodDelete:
		q := r.URL.Query()
		link, book := q.Get("id"), q.Get("book_id")
		if len(q) != 2 || !ledger.ValidID(link) {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		tenant, ok := a.bankBook(w, r, session, book)
		if !ok {
			return
		}
		err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			return o.DeleteBookLink(r.Context(), who, link)
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"link": nil})
	default:
		apiFailure(w, ledger.ErrInvalid)
	}
}

// pairs records a cost one Book paid for a linked Book: both entries at
// once, as the owner's own action.
func (a *webAPI) pairs(w http.ResponseWriter, r *http.Request, session string) {
	var q ledger.OwnerPair
	if !readRuleBody(w, r, &q) {
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.BeneficiaryBookID)
	if !ok {
		return
	}
	var out ledger.OwnerPairResult
	err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		out, err = o.RecordPair(r.Context(), who, q)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

// settlements records money one linked Book paid the other for what it
// owes: both entries at once, as the owner's own action. The items name
// their Vult.
func (a *webAPI) settlements(w http.ResponseWriter, r *http.Request, session string) {
	var q ledger.OwnerSettlement
	if !readRuleBody(w, r, &q) {
		return
	}
	tenants, err := a.sessions.TenantsBounded(r.Context(), session, webBookLimit)
	if err != nil {
		apiFailure(w, err)
		return
	}
	for _, tenant := range tenants {
		var out ledger.OwnerSettlementResult
		err = a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			var err error
			out, err = o.RecordSettlement(r.Context(), who, q)
			return err
		})
		if errors.Is(err, ledger.ErrNotFound) {
			continue
		}
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	apiFailure(w, ledger.ErrNotFound)
}

// previewSettlement says what a repayment would record, recording nothing:
// the costs it covers and how much of each, and any extra.
func (a *webAPI) previewSettlement(w http.ResponseWriter, r *http.Request, session string) {
	var q ledger.OwnerSettlement
	if !readRuleBody(w, r, &q) {
		return
	}
	tenants, err := a.sessions.TenantsBounded(r.Context(), session, webBookLimit)
	if err != nil {
		apiFailure(w, err)
		return
	}
	for _, tenant := range tenants {
		var out ledger.SettlementPreview
		err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			out, err = o.PreviewSettlement(r.Context(), q)
			return err
		})
		if errors.Is(err, ledger.ErrNotFound) {
			continue
		}
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	apiFailure(w, ledger.ErrNotFound)
}

// linkedBalances is who owes whom across every Vult's linked Books.
func (a *webAPI) linkedBalances(w http.ResponseWriter, r *http.Request, session string) {
	if len(r.URL.Query()) != 0 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenants, err := a.sessions.TenantsBounded(r.Context(), session, webBookLimit)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out ledger.LinkedBalances
	for _, tenant := range tenants {
		err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			got, err := o.OwnerLinkedBalances(r.Context())
			out.Links = append(out.Links, got.Links...)
			out.Books = append(out.Books, got.Books...)
			return err
		})
		if bankAnswer(w, err) {
			return
		}
	}
	if out.Links == nil {
		out.Links = out.Links[:0:0]
	}
	if out.Books == nil {
		out.Books = out.Books[:0:0]
	}
	_ = json.NewEncoder(w).Encode(out)
}

// assignmentRules serves the owner's rules for which Book charges belong
// to: listing a Book's, making or replacing one, and removing one.
func (a *webAPI) assignmentRules(w http.ResponseWriter, r *http.Request, session string) {
	switch r.Method {
	case http.MethodGet:
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
		var out []ledger.AssignmentRule
		err := a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			var err error
			out, err = o.AssignmentRules(r.Context(), book)
			return err
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rules": out})
	case http.MethodPost:
		var q ledger.AssignmentRuleChange
		if !readRuleBody(w, r, &q) {
			return
		}
		tenant, ok := a.bankBook(w, r, session, q.BookID)
		if !ok {
			return
		}
		var out []ledger.AssignmentRule
		err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			var err error
			out, err = o.SetAssignmentRule(r.Context(), who, q)
			return err
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rules": out})
	case http.MethodDelete:
		q := r.URL.Query()
		rule, book := q.Get("id"), q.Get("book_id")
		if len(q) != 2 || !ledger.ValidID(rule) {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		tenant, ok := a.bankBook(w, r, session, book)
		if !ok {
			return
		}
		err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
			return o.DeleteAssignmentRule(r.Context(), who, book, rule)
		})
		if bankAnswer(w, err) {
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rule": nil})
	default:
		apiFailure(w, ledger.ErrInvalid)
	}
}

// openingBalance enters what one linked Book owed the other as of a date,
// in both at once: the owner's own action, from a fresh sign-in.
func (a *webAPI) openingBalance(w http.ResponseWriter, r *http.Request, session string) {
	var q ledger.OwnerOpeningBalance
	if !readRuleBody(w, r, &q) {
		return
	}
	tenant, ok := a.bankBook(w, r, session, q.DebtorBookID)
	if !ok {
		return
	}
	var out ledger.OwnerOpeningBalanceResult
	err := a.sessions.OwnerAct(r.Context(), session, tenant, func(o ledger.TenantAdmin, who string) error {
		var err error
		out, err = o.RecordOpeningBalance(r.Context(), who, q)
		return err
	})
	if bankAnswer(w, err) {
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
