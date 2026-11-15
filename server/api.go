package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/vult-ai/vult/agentconfig"
	"github.com/vult-ai/vult/bankconnect"
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
	updater         Updater
	backups         BackupStore
	bankRequests    InstitutionRequests
	requestsSent    sentRequests
	lookupHost      func(context.Context, string) error // tests stand in for DNS
	banks           *bankconnect.Service
	bankScreen      *BankScreen
	bankOperator    BankOperator
	bankLimit       *BankLimitSetting
	agent           *agentconfig.Service
	chat            AgentChat
	arrived         func(context.Context, ledger.AgentTarget, OwnerAct)
	added           func(context.Context, ledger.AgentTarget, OwnerAct, AddedDocument)
	checkQueue      *documentCheckQueue
	bookEvents      *bookEvents
	prover          BookProver
	deletion        BookDeletionPolicy
	deletionLimits  sessionLimits
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
	// TaxedAs is how the Book is taxed; TaxedAsSet says the owner chose it.
	TaxedAs    string `json:"taxed_as,omitempty"`
	TaxedAsSet bool   `json:"taxed_as_set,omitempty"`
	// Projects says the Book can label its spending by project.
	Projects bool `json:"projects,omitempty"`
	// ExpenseReports says the Book is a business paying back a person
	// through an owed link, so it keeps expense reports.
	ExpenseReports     bool   `json:"expense_reports,omitempty"`
	AccountingToday    string `json:"accounting_today,omitempty"`
	CalendarConfigured bool   `json:"calendar_configured"`
	// Archived Books stay out of menus; a Book being deleted is locked.
	Archived bool                 `json:"archived,omitempty"`
	Deletion *ledger.BookDeletion `json:"deletion,omitempty"`
}

func (a *webAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if len(r.Header.Values("Authorization")) != 0 {
		failure(w, http.StatusForbidden)
		return
	}
	cookies := r.CookiesNamed(access.SessionCookie)
	if len(cookies) == 0 {
		// No session in this browser at all: the owner is signed out, as
		// /auth/session says. A session that was present and ended answers
		// a plain 401.
		signedOut(w)
		return
	}
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
	case bookDeletionRoute(r.URL.Path):
		a.bookDeletion(w, r, session.Value)
	case backupsRoute(r.URL.Path):
		a.backupsAPI(w, r, session.Value)
	case expenseReportsRoute(r.URL.Path):
		a.expenseReports(w, r, session.Value)
	case bankRoute(r.URL.Path):
		a.bank(w, r, session.Value)
	case projectsRoute(r.URL.Path):
		a.projects(w, r, session.Value)
	case r.URL.Path == remindersPath || r.URL.Path == remindersDonePath:
		a.reminders(w, r, session.Value)
	case r.URL.Path == agentSettingsPath:
		a.agentSettings(w, r, session.Value)
	case recordingRulesRoute(r.URL.Path):
		a.recordingRules(w, r, session.Value)
	case r.URL.Path == bookLinksPath:
		a.bookLinks(w, r, session.Value)
	case r.URL.Path == assignmentRulesPath:
		a.assignmentRules(w, r, session.Value)
	case r.Method == http.MethodPost && r.URL.Path == pairsPath:
		a.pairs(w, r, session.Value)
	case r.Method == http.MethodPost && r.URL.Path == openingBalancePath:
		a.openingBalance(w, r, session.Value)
	case r.Method == http.MethodPost && r.URL.Path == settlementPreview:
		a.previewSettlement(w, r, session.Value)
	case r.Method == http.MethodPost && r.URL.Path == settlementsPath:
		a.settlements(w, r, session.Value)
	case r.Method == http.MethodGet && r.URL.Path == linkedBalancesPath:
		a.linkedBalances(w, r, session.Value)
	case r.URL.Path == statementFilesPath:
		a.statementFiles(w, r, session.Value)
	case r.Method == http.MethodPost && r.URL.Path == agentChatPath:
		a.agentChat(w, r, session.Value)
	case (r.Method == http.MethodGet || r.Method == http.MethodDelete) && r.URL.Path == agentChatPath:
		a.agentConversation(w, r, session.Value)
	case r.URL.Path == agentNotesPath || len(r.URL.Path) > len(agentNotesPath) && r.URL.Path[:len(agentNotesPath)+1] == agentNotesPath+"/":
		a.agentNotes(w, r, session.Value)
	case r.Method == http.MethodGet && r.URL.Path == entryOriginPath:
		a.entryOrigin(w, r, session.Value)
	case r.Method == http.MethodGet && r.URL.Path == eventsPath:
		a.events(w, r, session.Value)
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
	case r.Method == http.MethodPost && r.URL.Path == documentGroupPath:
		a.documentGroup(w, r, session.Value)
	case r.Method == http.MethodGet && r.URL.Path == documentChecksPath:
		a.documentChecks(w, r, session.Value)
	case r.Method == http.MethodPost && r.URL.Path == documentReadingPath:
		a.documentReading(w, r, session.Value)
	case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/documents" || strings.HasPrefix(r.URL.Path, "/api/v1/documents/")):
		a.documentRead(w, r, session.Value)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent-owner/review":
		a.proposalReview(w, r, session.Value)
	case r.Method == http.MethodGet && (r.URL.Path == "/api/v1/agent-owner/plans" || strings.HasPrefix(r.URL.Path, "/api/v1/agent-owner/plans/")):
		a.proposalRead(w, r, session.Value)
	case r.Method == http.MethodPost && r.URL.Path == approveBatchPath:
		a.approveBatch(w, r, session.Value)
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
	case r.URL.Path == updatePath && r.Method == http.MethodGet && (a.updates != nil || a.updater != nil) && r.URL.RawQuery == "":
		a.updateStatus(w, r)
	case r.URL.Path == updateInstallPath && r.Method == http.MethodPost && a.updater != nil:
		a.updateInstall(w, r, session.Value)
	case r.URL.Path == accountChangesPath && r.Method == http.MethodPost:
		a.accountChange(w, r, session.Value)
	case r.URL.Path == payeesPath && r.Method == http.MethodGet:
		a.payees(w, r, session.Value)
	case r.URL.Path == statementsPath && r.Method == http.MethodGet:
		a.statements(w, r, session.Value)
	case r.URL.Path == starterChartsPath && r.Method == http.MethodGet && r.URL.RawQuery == "":
		a.starterCharts(w)
	case r.URL.Path == periodsPath && r.Method == http.MethodGet:
		a.readClosedPeriods(w, r, session.Value)
	case r.URL.Path == periodsClosePath && r.Method == http.MethodPost:
		a.periodChange(w, r, session.Value, "close_period")
	case r.URL.Path == periodsReopenPath && r.Method == http.MethodPost:
		a.periodChange(w, r, session.Value, "reopen_period")
	case r.URL.Path == periodsReviewPath && r.Method == http.MethodGet:
		a.periodReview(w, r, session.Value)
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
			states, err := o.BookLifecycles(r.Context())
			if err != nil {
				return err
			}
			lifecycle := map[string]ledger.BookLifecycle{}
			for _, s := range states {
				lifecycle[s.BookID] = s
			}
			for _, b := range list {
				book := browserBook(b, time.Now())
				book.Archived = lifecycle[b.ID].ArchivedAt != nil
				book.Deletion = lifecycle[b.ID].Deletion
				out = append(out, book)
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
	var limit *ledger.PlanLimitError
	var refused *ledger.RefusedError
	switch {
	case errors.As(e, &refused):
		// A refusal with a reason says it, as bank changes do.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "error": "refused", "message": refused.Reason})
	case errors.Is(e, ledger.ErrFreshOwnerProof):
		// Only this code sends the person to sign in again.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "fresh_authentication_required", "message": "Confirm it’s you, then make this change."})
	case errors.As(e, &limit):
		// A plan limit is not a sign-in problem: say what the limit is.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(ledger.PlanLimitResponse(limit))
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
