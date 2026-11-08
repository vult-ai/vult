package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

func (a *webAPI) readSettings(w http.ResponseWriter, r *http.Request, session string) {
	query := r.URL.Query()
	if len(query) != 1 || len(query["book_id"]) != 1 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	ctx := r.Context()
	id := query.Get("book_id")
	tenant, err := a.sessions.BookTenant(ctx, session, id)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var book ledger.Book
	err = a.sessions.WithOwner(ctx, session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		book, err = owner.Book(ctx, id)
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(browserBook(book, time.Now()))
}

func (a *webAPI) reviseSettings(w http.ResponseWriter, r *http.Request, session string) {
	if r.URL.RawQuery != "" || len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	var q ledger.ReviseBookSettings
	if strictjson.Decode(data, &q) != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	for _, name := range []string{"book_id", "idempotency_key", "revision", "name", "founder"} {
		if _, ok := fields[name]; !ok {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	for _, field := range fields {
		if bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, q.BookID)
	if err != nil {
		apiFailure(w, err)
		return
	}
	result, err := a.sessions.ReviseBookSettings(r.Context(), session, tenant, q, a.admit)
	if err != nil {
		apiFailure(w, err)
		return
	}
	_, _ = w.Write(result)
}

func (a *webAPI) settingsOutcome(w http.ResponseWriter, r *http.Request, session string) {
	query := r.URL.Query()
	if len(query) != 2 || len(query["book_id"]) != 1 || len(query["key"]) != 1 {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, query.Get("book_id"))
	if err != nil {
		apiFailure(w, err)
		return
	}
	result, err := a.sessions.BookSettingsOutcome(r.Context(), session, tenant, query.Get("book_id"), query.Get("key"))
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func browserBook(b ledger.Book, now time.Time) webBook {
	out := webBook{ID: b.ID, Name: b.Name, Currency: b.Currency, Scale: b.Scale, Logo: b.Logo, Founder: b.Founder, Profile: b.Profile, StartDate: b.StartDate,
		FiscalYearStartMonth: b.FiscalYearStartMonth, FiscalYearStartDay: b.FiscalYearStartDay, AccountingTimezone: b.AccountingTimezone}
	if b.MetadataRevision != nil {
		out.MetadataRevision = *b.MetadataRevision
	}
	if b.AccountingTimezone != "" && b.AccountingTimezone != "Local" {
		zone, err := time.LoadLocation(b.AccountingTimezone)
		if err == nil {
			out.AccountingToday = now.In(zone).Format("2006-01-02")
			out.CalendarConfigured = b.FiscalYearStartMonth != nil && b.FiscalYearStartDay != nil
		}
	}
	return out
}
