package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/vult-ai/vult/internal/update"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/ledger"
)

// webAPI serves /api/v1 to the bundled interface only. It is not a public API:
// routes may change with the interface. Owner requests need the browser
// session, and writes also prove Origin and CSRF. Remote agent logo writes
// use a separate handler with their own scoped credentials.
type webAPI struct {
	exports         exportBudget
	sessions        *access.Service
	login           *access.HTTP
	admit           access.OwnerAdmission
	admitDecision   ledger.DecisionAdmission
	documents       *documents.Vault
	readDocument    func(context.Context, string, string, string, io.Writer) error
	uploadDocument  func(context.Context, string, string, ledger.AddDocument, io.Reader) (ledger.OwnerDocumentUpload, error)
	documentUploads chan struct{}
	localBooks      LocalBooks
	updates         *update.Checker
}

const webBookLimit = 1000

// updatePath tells the owner of Vult on this computer whether a newer release
// is published.
const updatePath = "/api/v1/update"

type webBook struct {
	ID                   string           `json:"id"`
	Name                 string           `json:"name"`
	Currency             string           `json:"currency"`
	Scale                int              `json:"scale"`
	MetadataRevision     int64            `json:"metadata_revision"`
	Logo                 *ledger.BookLogo `json:"logo,omitempty"`
	Founder              string           `json:"founder"`
	Profile              string           `json:"profile,omitempty"`
	StartDate            string           `json:"start_date"`
	FiscalYearStartMonth *int             `json:"fiscal_year_start_month,omitempty"`
	FiscalYearStartDay   *int             `json:"fiscal_year_start_day,omitempty"`
	AccountingTimezone   string           `json:"accounting_timezone,omitempty"`
	AccountingToday      string           `json:"accounting_today,omitempty"`
	CalendarConfigured   bool             `json:"calendar_configured"`
}

func (a *webAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if len(r.Header.Values("Authorization")) != 0 {
		failure(w, http.StatusForbidden)
		return
	}
	cookies := r.CookiesNamed(access.SessionCookie)
	if len(cookies) != 1 {
		failure(w, http.StatusUnauthorized)
		return
	}
	session := cookies[0]
	if _, e := a.sessions.Identity(r.Context(), session.Value); e != nil {
		failure(w, http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !a.login.CheckCSRF(r, session.Value) {
		failure(w, http.StatusForbidden)
		return
	}
	switch {
	case bookLogoWrite(r):
		a.writeBookLogo(w, r, session.Value)
	case r.Method == http.MethodGet && (r.URL.Path == bookLogoPath || strings.HasPrefix(r.URL.Path, bookLogoPath+"/")):
		a.readBookLogo(w, r, session.Value)
	case r.URL.Path == bookIdentityPath && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		a.bookIdentity(w, r, session.Value)
	case r.URL.Path == bookAddressPath && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		a.bookAddress(w, r, session.Value)
	case r.Method == http.MethodGet && bookExportType(r.URL.Path) != "":
		a.bookExport(w, r, session.Value)
	case r.URL.Path == connectionPath || strings.HasPrefix(r.URL.Path, connectionPath+"/"):
		a.ownerConnections(w, r, session.Value)
	case r.URL.Path == "/api/v1/me/settings" && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		a.userSettings(w, r, session.Value)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, avatarPrefix):
		a.userAvatar(w, r, session.Value)
	case r.Method == http.MethodPost && r.URL.Path == ownerDocumentUploadPath:
		a.documentUpload(w, r, session.Value)
	case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/documents" || strings.HasPrefix(r.URL.Path, "/api/v1/documents/")):
		a.documentRead(w, r, session.Value)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent-owner/review":
		a.proposalReview(w, r, session.Value)
	case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/agent-owner/plans" || strings.HasPrefix(r.URL.Path, "/api/v1/agent-owner/plans/")):
		a.proposalRead(w, r, session.Value)
	case r.Method == http.MethodPost && (r.URL.Path == "/api/v1/agent-owner/approve" || r.URL.Path == "/api/v1/agent-owner/reject"):
		a.proposalDecision(w, r, session.Value)
	case (r.URL.Path == localBookDraftPath && r.Method == http.MethodGet) || (r.URL.Path == localBooksPath && r.Method == http.MethodPost):
		a.localBook(w, r, session.Value)
	case r.URL.Path == localActivityPath && r.Method == http.MethodGet:
		a.localActivity(w, r, session.Value)
	case r.URL.Path == localDecisionPath && r.Method == http.MethodPost:
		a.localDecision(w, r, session.Value)
	case r.URL.Path == booksPath && r.Method == http.MethodGet && r.URL.RawQuery == "":
		a.books(w, r, session.Value)
	case r.URL.Path == booksPath && r.Method == http.MethodPost:
		a.createBook(w, r, session.Value)
	case r.URL.Path == updatePath && r.Method == http.MethodGet && a.updates != nil && r.URL.RawQuery == "":
		_ = json.NewEncoder(w).Encode(struct {
			SchemaVersion int `json:"schema_version"`
			update.Status
		}{1, a.updates.Status(r.Context())})
	case r.URL.Path == periodsPath && r.Method == http.MethodGet:
		a.readClosedPeriods(w, r, session.Value)
	case r.URL.Path == periodsClosePath && r.Method == http.MethodPost:
		a.closePeriod(w, r, session.Value)
	case r.URL.Path == bookViewPreferencePath && r.Method == http.MethodPost:
		a.saveBookViewPreference(w, r, session.Value)
	case r.URL.Path == "/api/v1/settings" && r.Method == http.MethodGet:
		a.readSettings(w, r, session.Value)
	case r.URL.Path == "/api/v1/settings" && r.Method == http.MethodPost:
		a.reviseSettings(w, r, session.Value)
	case r.URL.Path == "/api/v1/book-request-outcome" && r.Method == http.MethodGet:
		a.settingsOutcome(w, r, session.Value)
	case r.URL.Path == "/api/v1/legal-identity-catalog" && r.Method == http.MethodGet:
		a.legalCatalog(w, r)
	case r.URL.Path == "/api/v1/transaction-postings" && r.Method == http.MethodGet:
		a.journalPostings(w, r, session.Value)
	case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/financial-reports" || r.URL.Path == "/api/v1/financial-reports/months" || r.URL.Path == "/api/v1/financial-reports/contributions" || r.URL.Path == "/api/v1/financial-reports/export-permission"):
		a.financialReport(w, r, session.Value)
	case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/dashboard" || r.URL.Path == "/api/v1/dashboard/projection" || r.URL.Path == "/api/v1/dashboard/cash-summary"):
		a.dashboardRead(w, r, session.Value)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/state":
		a.stateRead(w, r, session.Value)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/search":
		a.searchRead(w, r, session.Value)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/transactions/summary":
		a.activityRead(w, r, session.Value)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/account-activity",
		(r.Method == http.MethodGet || r.Method == http.MethodPost) &&
			(r.URL.Path == "/api/v1/account-hierarchy-activity" || r.URL.Path == "/api/v1/account-detail"):
		a.accountRead(w, r, session.Value)
	case strings.HasPrefix(r.URL.Path, "/api/v1/transactions/") && r.Method == http.MethodGet:
		a.journalRead(w, r, session.Value)
	default:
		failure(w, http.StatusNotFound)
	}
}

// books lists every Book the signed-in user owns, across their tenants.
func (a *webAPI) books(w http.ResponseWriter, r *http.Request, session string) {
	tenants, e := a.sessions.TenantsBounded(r.Context(), session, webBookLimit)
	if e != nil {
		apiFailure(w, e)
		return
	}
	out := []webBook{}
	addresses := []ledger.BookAddress{}
	for _, tenant := range tenants {
		e = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
			limit := max(1, webBookLimit-len(out))
			list, err := o.BooksBounded(r.Context(), limit)
			if err != nil {
				return err
			}
			for _, b := range list {
				out = append(out, browserBook(b, time.Now()))
			}
			tenantAddresses, err := o.BookAddressesBounded(r.Context(), limit)
			addresses = append(addresses, tenantAddresses...)
			return err
		})
		if e == nil && len(out) > webBookLimit {
			e = ledger.ErrLimit
		}
		if e != nil {
			apiFailure(w, e)
			return
		}
	}
	bookIDs := make([]string, len(out))
	for i, book := range out {
		bookIDs[i] = book.ID
	}
	scope, views, err := a.sessions.BookViewPreferences(r.Context(), session, bookIDs)
	if err != nil {
		apiFailure(w, err)
		return
	}
	ledger.DisambiguateBookAddresses(addresses)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schema_version": 1, "books": out, "addresses": addresses, "currencies": ledger.Currencies(),
		"view_scope": scope, "views": views,
	})
}

func apiFailure(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, access.ErrDenied) || errors.Is(e, ledger.ErrDenied):
		failure(w, http.StatusForbidden)
	case errors.Is(e, ledger.ErrInvalid):
		failure(w, http.StatusBadRequest)
	case errors.Is(e, ledger.ErrConflict):
		failure(w, http.StatusConflict)
	case errors.Is(e, ledger.ErrNotFound):
		failure(w, http.StatusNotFound)
	case errors.Is(e, ledger.ErrLimit):
		failure(w, http.StatusRequestEntityTooLarge)
	default:
		failure(w, http.StatusServiceUnavailable)
	}
}
