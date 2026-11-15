package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/documents"
	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

// Archiving and deleting a Book, for its owner in the browser. Archiving is
// instant and reversible. Deleting needs the owner's passkey, asked for this
// Book at that moment, and the Book's exact name; the Book is then locked
// and erased after a waiting period the owner can cancel.
const (
	bookArchivePath        = "/api/v1/books/archive"
	bookDeletionPath       = "/api/v1/books/deletion"
	bookDeletionPasskey    = "/api/v1/books/deletion/passkey"
	bookDeletionConfirm    = "/api/v1/books/deletion/confirm"
	bookDeletionCancel     = "/api/v1/books/deletion/cancel"
	bookDeletionHistory    = "/api/v1/books/deletion/history"
	bookDeletionReadLimit  = 100
	bookDeletionEraseEvery = time.Minute
)

func bookDeletionRoute(path string) bool {
	switch path {
	case bookArchivePath, bookDeletionPath, bookDeletionPasskey, bookDeletionConfirm, bookDeletionCancel, bookDeletionHistory:
		return true
	}
	return false
}

// BookProver asks the owner's passkey to approve one action on one Book and
// verifies the answer. A Vult whose owner signs in another way supplies its
// own, or Books can't be deleted from the browser there.
type BookProver interface {
	BeginBookProof(ctx context.Context, session, tenant, book, purpose string) (access.PasskeyChallenge, error)
	FinishBookProof(ctx context.Context, token, session string, response json.RawMessage) error
}

// BookDeletionNotice is what the owner is told when a deletion starts and
// when the Book has been erased. It names the Book, never its records.
type BookDeletionNotice struct {
	TenantID     string     `json:"tenant_id"`
	BookID       string     `json:"book_id"`
	BookName     string     `json:"book_name"`
	State        string     `json:"state"`
	RequestedAt  time.Time  `json:"requested_at"`
	ConfirmAfter *time.Time `json:"confirm_after,omitempty"`
	EraseAfter   *time.Time `json:"erase_after,omitempty"`
	ErasedAt     *time.Time `json:"erased_at,omitempty"`
	Backups      Backups    `json:"backups"`
}

// BookDeletionNotifier tells the owner, for example by email, that a
// deletion started and that it finished. A Vult without one still deletes;
// the owner sees the same facts in the app.
type BookDeletionNotifier interface {
	DeletionStarted(context.Context, BookDeletionNotice) error
	DeletionFinished(context.Context, BookDeletionNotice) error
}

// Backups says how long copies of an erased Book may remain in backups.
type Backups struct {
	// RetentionDays is how long this Vult's own backups are kept; 0 when it
	// keeps none on its own.
	RetentionDays int `json:"retention_days"`
	// ExpireBy is when the last backup that may hold a copy is gone.
	ExpireBy *time.Time `json:"expire_by,omitempty"`
	Note     string     `json:"note"`
}

// BookDeletionPolicy is how this Vult tells the owner about deletions and
// its backups.
type BookDeletionPolicy struct {
	Notifier BookDeletionNotifier
	// BackupRetention is how long this Vult's own backups are kept, 0 when
	// it makes none.
	BackupRetention time.Duration
}

func (p BookDeletionPolicy) backups(from time.Time) Backups {
	if p.BackupRetention <= 0 {
		return Backups{Note: "This Vult makes no backups of its own. Copies you made, such as a downloaded copy, a Time Machine backup or a recovery point, stay until you remove them."}
	}
	end := from.Add(p.BackupRetention)
	return Backups{RetentionDays: int(p.BackupRetention / (24 * time.Hour)), ExpireBy: &end,
		Note: "Backups this Vult made before the Book was erased may hold a copy until they expire."}
}

// One session may take this many deletion steps at once, then one every
// few seconds.
var (
	deletionRequestEvery = rate.Every(3 * time.Second)
	deletionRequestBurst = 20
)

// sessionLimits bounds how often one session may take deletion steps, on
// top of the per-Vult limits the database keeps.
type sessionLimits struct {
	mu   sync.Mutex
	each map[string]*rate.Limiter
}

func (l *sessionLimits) allow(session string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.each == nil || len(l.each) > 4096 {
		l.each = map[string]*rate.Limiter{}
	}
	key := access.Hash(session)
	limiter := l.each[key]
	if limiter == nil {
		limiter = rate.NewLimiter(deletionRequestEvery, deletionRequestBurst)
		l.each[key] = limiter
	}
	return limiter.Allow()
}

func clientFacts(r *http.Request) ledger.ClientFacts {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return ledger.ClientFacts{Address: host, Agent: r.UserAgent()}
}

func bookRefused(w http.ResponseWriter, e error) bool {
	var refused *ledger.BookRefusal
	if !errors.As(e, &refused) {
		return false
	}
	status := map[string]int{"proof": http.StatusForbidden, "name": http.StatusUnprocessableEntity, "busy": http.StatusConflict,
		"linked": http.StatusConflict, "early": http.StatusTooEarly, "limit": http.StatusTooManyRequests, "locked": http.StatusConflict}[refused.Code]
	if status == 0 {
		status = http.StatusUnprocessableEntity
	}
	code := "refused"
	if refused.Code == "proof" {
		code = "passkey_required"
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "reason": refused.Code, "message": refused.Reason})
	return true
}

func passkeyRequired(w http.ResponseWriter, message string) {
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "passkey_required", "reason": "proof", "message": message})
}

func (a *webAPI) bookDeletion(w http.ResponseWriter, r *http.Request, session string) {
	if !a.deletionLimits.allow(session) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "refused", "reason": "limit", "message": "Too many requests. Wait a moment, then try again."})
		return
	}
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case bookDeletionPath:
			a.bookDeletionSummary(w, r, session)
		case bookDeletionHistory:
			a.bookDeletionRecord(w, r, session)
		default:
			failure(w, http.StatusNotFound)
		}
		return
	}
	if r.Method != http.MethodPost || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" || r.URL.Path == bookDeletionHistory {
		failure(w, http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 80<<10))
	var q struct {
		BookID     string          `json:"book_id"`
		Archived   *bool           `json:"archived,omitempty"`
		Purpose    string          `json:"purpose,omitempty"`
		Name       string          `json:"name,omitempty"`
		Token      string          `json:"token,omitempty"`
		Credential json.RawMessage `json:"credential,omitempty"`
	}
	if err != nil || strictjson.Decode(body, &q) != nil || !ledger.ValidID(q.BookID) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if err != nil {
		apiFailure(w, err)
		return
	}
	from := clientFacts(r)
	switch r.URL.Path {
	case bookArchivePath:
		if q.Archived == nil {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		err = a.sessions.WithOwnerSignedIn(r.Context(), session, tenant, func(o ledger.TenantAdmin) error {
			return o.ArchiveBook(r.Context(), q.BookID, *q.Archived, from)
		})
		if err == nil {
			a.bookEvents.Publish(tenant, q.BookID, "books")
			_ = json.NewEncoder(w).Encode(map[string]any{"book_id": q.BookID, "archived": *q.Archived})
			return
		}
	case bookDeletionPasskey:
		if a.prover == nil {
			passkeyRequired(w, "Deleting a Book needs a passkey. Add one to your sign-in, then try again.")
			return
		}
		challenge, err := a.prover.BeginBookProof(r.Context(), session, tenant, q.BookID, q.Purpose)
		if errors.Is(err, access.ErrProofLimit) {
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "refused", "reason": "limit", "message": "Too many passkey requests in the last hour. Try again later."})
			return
		}
		if err != nil {
			apiFailure(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"token": challenge.Token, "options": challenge.Options})
		return
	case bookDeletionPath, bookDeletionConfirm:
		if a.prover == nil {
			passkeyRequired(w, "Deleting a Book needs a passkey. Add one to your sign-in, then try again.")
			return
		}
		if a.prover.FinishBookProof(r.Context(), q.Token, session, q.Credential) != nil {
			passkeyRequired(w, "Your passkey didn't confirm this. Try again.")
			return
		}
		var out ledger.BookDeletionOutcome
		var name string
		err = a.sessions.WithOwner(r.Context(), session, tenant, true, func(o ledger.TenantAdmin) error {
			var err error
			if r.URL.Path == bookDeletionPath {
				out, err = o.RequestBookDeletion(r.Context(), q.BookID, access.Hash(q.Token), q.Name, from)
				name = q.Name
			} else {
				out, err = o.ConfirmBookDeletion(r.Context(), q.BookID, access.Hash(q.Token), from)
				if err == nil {
					var s ledger.BookDeletionSummary
					s, err = o.BookDeletionSummary(r.Context(), q.BookID)
					name = s.Name
				}
			}
			return err
		})
		if err == nil {
			if a.banks != nil && out.BanksDisconnected > 0 {
				a.banks.Wake()
			}
			a.bookEvents.Publish(tenant, q.BookID, "books")
			a.notifyStarted(r.Context(), tenant, name, out)
			_ = json.NewEncoder(w).Encode(map[string]any{"deletion": out, "backups": a.deletion.backups(eraseTime(out))})
			return
		}
	case bookDeletionCancel:
		err = a.sessions.WithOwnerSignedIn(r.Context(), session, tenant, func(o ledger.TenantAdmin) error {
			return o.CancelBookDeletion(r.Context(), q.BookID, from)
		})
		if err == nil {
			a.bookEvents.Publish(tenant, q.BookID, "books")
			_ = json.NewEncoder(w).Encode(map[string]any{"book_id": q.BookID, "cancelled": true})
			return
		}
	default:
		failure(w, http.StatusNotFound)
		return
	}
	if !bookRefused(w, err) {
		apiFailure(w, err)
	}
}

func eraseTime(out ledger.BookDeletionOutcome) time.Time {
	if out.EraseAfter != nil {
		return *out.EraseAfter
	}
	if out.ConfirmAfter != nil {
		return out.ConfirmAfter.Add(ledger.BookDeletionWait)
	}
	return time.Now().Add(ledger.BookDeletionWait)
}

func (a *webAPI) notifyStarted(ctx context.Context, tenant, name string, out ledger.BookDeletionOutcome) {
	if a.deletion.Notifier == nil {
		return
	}
	notice := BookDeletionNotice{TenantID: tenant, BookID: out.BookID, BookName: name, State: out.State, RequestedAt: time.Now().UTC(),
		ConfirmAfter: out.ConfirmAfter, EraseAfter: out.EraseAfter, Backups: a.deletion.backups(eraseTime(out))}
	if err := a.deletion.Notifier.DeletionStarted(context.WithoutCancel(ctx), notice); err != nil {
		slog.Default().Warn("book deletion notice not sent", "kind", "started")
	}
}

// bookDeletionSummary is what the owner reads before deleting: what goes,
// whether the Book is archived or being deleted, and how long backups last.
func (a *webAPI) bookDeletionSummary(w http.ResponseWriter, r *http.Request, session string) {
	book, err := ownerBookID(r)
	if err != nil {
		apiFailure(w, err)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var summary ledger.BookDeletionSummary
	var state ledger.BookLifecycle
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(o ledger.TenantAdmin) error {
		var err error
		if summary, err = o.BookDeletionSummary(r.Context(), book); err != nil {
			return err
		}
		all, err := o.BookLifecycles(r.Context())
		for _, v := range all {
			if v.BookID == book {
				state = v
			}
		}
		return err
	})
	if err != nil {
		if !bookRefused(w, err) {
			apiFailure(w, err)
		}
		return
	}
	state.BookID = book
	_ = json.NewEncoder(w).Encode(map[string]any{
		"summary": summary, "lifecycle": state, "wait_days": int(ledger.BookDeletionWait / (24 * time.Hour)),
		"passkey": a.prover != nil, "backups": a.deletion.backups(time.Now().Add(ledger.BookDeletionWait)),
	})
}

// bookDeletionRecord is the Vult's deletion record.
func (a *webAPI) bookDeletionRecord(w http.ResponseWriter, r *http.Request, session string) {
	if r.URL.RawQuery != "" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenants, err := a.sessions.TenantsBounded(r.Context(), session, 2)
	if err != nil || len(tenants) != 1 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	var events []ledger.BookDeletionEvent
	err = a.sessions.WithOwner(r.Context(), session, tenants[0], false, func(o ledger.TenantAdmin) error {
		var err error
		events, err = o.BookDeletionHistory(r.Context(), bookDeletionReadLimit)
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	type erased struct {
		ledger.BookDeletionEvent
		Backups Backups `json:"backups"`
	}
	out := make([]erased, 0, len(events))
	for _, e := range events {
		out = append(out, erased{e, a.deletion.backups(e.RecordedAt)})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"events": out})
}

// BookEraser erases Books whose waiting period is over: the stored
// documents, the Book's document key, then every row in one transaction.
// Each step can be repeated, so a stopped erasure resumes and finishes.
type BookEraser struct {
	DB        ledger.ErasureDatabase
	Documents *documents.Vault
	Policy    BookDeletionPolicy
	Log       *slog.Logger
	// removed is told after the stored files are gone and before the rows
	// are; tests stop an erasure there.
	removed func(tenant, book string) error
}

// Run erases due Books until ctx ends.
func (e *BookEraser) Run(ctx context.Context) {
	tick := time.NewTicker(bookDeletionEraseEvery)
	defer tick.Stop()
	for {
		if _, err := e.EraseDue(ctx); err != nil && ctx.Err() == nil {
			e.logger().Warn("book erasure did not finish; it resumes on the next run")
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (e *BookEraser) logger() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

// EraseDue erases every due Book and says how many it finished.
func (e *BookEraser) EraseDue(ctx context.Context) (int, error) {
	if e == nil || e.DB == nil {
		return 0, ledger.ErrDenied
	}
	due, err := ledger.BookErasuresDue(ctx, e.DB, 20)
	if err != nil {
		return 0, err
	}
	done := 0
	var first error
	for _, t := range due {
		if err := e.Erase(ctx, t.TenantID, t.BookID); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		done++
	}
	return done, first
}

// Erase starts or resumes one due erasure and finishes it.
func (e *BookEraser) Erase(ctx context.Context, tenant, book string) error {
	plan, err := ledger.BeginBookErasure(ctx, e.DB, tenant, book)
	if err != nil {
		return err
	}
	if len(plan.StorageIDs) > 0 && (e.Documents == nil || e.Documents.Storage == nil) {
		return documents.ErrUnavailable
	}
	for _, id := range plan.StorageIDs {
		if err := e.Documents.Storage.Remove(ctx, id); err != nil {
			return err
		}
	}
	if e.Documents != nil {
		if err := e.Documents.DestroyBookKey(ctx, tenant, book); err != nil {
			return err
		}
	}
	if e.removed != nil {
		if err := e.removed(tenant, book); err != nil {
			return err
		}
	}
	if _, err := ledger.FinishBookErasure(ctx, e.DB, tenant, book); err != nil {
		return err
	}
	now := time.Now().UTC()
	e.logger().Info("book erased")
	if e.Policy.Notifier != nil {
		notice := BookDeletionNotice{TenantID: tenant, BookID: book, BookName: plan.Name, State: "erased", RequestedAt: plan.RequestedAt,
			ErasedAt: &now, Backups: e.Policy.backups(now)}
		if err := e.Policy.Notifier.DeletionFinished(context.WithoutCancel(ctx), notice); err != nil {
			e.logger().Warn("book deletion notice not sent", "kind", "finished")
		}
	}
	return nil
}
