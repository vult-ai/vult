package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

func settingsRequest(session, body string) *http.Request {
	r := browserRequest(http.MethodPost, "/api/v1/settings", session)
	r.Body = io.NopCloser(strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", configuration().Origin)
	r.Header.Set("X-CSRF-Token", access.Hash("csrf:"+session))
	return r
}

func TestBrowserBookSettingsRequireSessionProofAndAnOwnedBook(t *testing.T) {
	f, s := browserServer(t)
	ctx := f.db.Context
	other := access.Identity{Issuer: f.identity.Issuer, Subject: "other", AuthenticatedAt: time.Now().UTC()}
	foreignTenant, err := f.operator.BindOwner(ctx, "", other)
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := f.sessions.IssueSession(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	var foreign ledger.Book
	if err = f.sessions.WithOwner(ctx, otherSession, foreignTenant, true, func(o ledger.TenantAdmin) error {
		var e error
		foreign, e = o.CreateBook(ctx, "Example separate Book", "USD", 2)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"book_id":"` + f.book.ID + `","idempotency_key":"rename","revision":0,"name":"Example current Book","founder":"Example owner"}`
	for _, tc := range []struct {
		name   string
		change func(*http.Request)
		status int
	}{
		{"no-session", func(r *http.Request) { r.Header.Del("Cookie") }, http.StatusUnauthorized},
		{"bearer-and-session", func(r *http.Request) { r.Header.Set("Authorization", "Bearer example") }, http.StatusForbidden},
		{"no-origin", func(r *http.Request) { r.Header.Del("Origin") }, http.StatusForbidden},
		{"foreign-origin", func(r *http.Request) { r.Header.Set("Origin", "https://other.example.com") }, http.StatusForbidden},
		{"no-csrf", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, http.StatusForbidden},
		{"duplicate-csrf", func(r *http.Request) { r.Header.Add("X-CSRF-Token", access.Hash("csrf:"+f.session)) }, http.StatusForbidden},
		{"foreign-book", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(body, f.book.ID, foreign.ID, 1)))
		}, http.StatusNotFound},
		{"missing-key", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(body, `"idempotency_key":"rename",`, "", 1)))
		}, http.StatusBadRequest},
		{"missing-revision", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(body, `"revision":0,`, "", 1)))
		}, http.StatusBadRequest},
		{"null-founder", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Replace(body, `"Example owner"`, "null", 1)))
		}, http.StatusBadRequest},
		{"unknown-field", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.TrimSuffix(body, "}") + `,"tenant_id":"` + f.tenant + `"}`))
		}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := settingsRequest(f.session, body)
			tc.change(r)
			w := httptest.NewRecorder()
			s.Handler.ServeHTTP(w, r)
			if w.Code != tc.status || strings.Contains(w.Body.String(), "Example current") || strings.Contains(w.Body.String(), foreign.Name) {
				t.Fatalf("settings boundary returned %d, want %d", w.Code, tc.status)
			}
		})
	}
	for _, path := range []string{"/api/v1/settings?book_id=" + foreign.ID, "/api/v1/book-request-outcome?book_id=" + foreign.ID + "&key=rename"} {
		w := httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		if w.Code != http.StatusNotFound {
			t.Fatal("read exposed a foreign Book", w.Code)
		}
	}
	var count int
	if err = f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM public.book_revisions").Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected request recorded a revision", err)
	}
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, settingsRequest(f.session, body))
	if w.Code != http.StatusOK {
		t.Fatalf("proved owner save failed: %d %s", w.Code, w.Body.String())
	}
	accepted := append([]byte{}, w.Body.Bytes()...)
	for _, path := range []string{"/api/v1/settings?book_id=" + f.book.ID, "/api/v1/books"} {
		w = httptest.NewRecorder()
		s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, path, f.session))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Example current Book") || strings.Contains(w.Body.String(), "accounting_today") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("accepted settings or unknown calendar projection differs", w.Code)
		}
	}
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, settingsRequest(f.session, body))
	if w.Code != http.StatusOK || !bytes.Equal(accepted, w.Body.Bytes()) {
		t.Fatal("retry changed the accepted outcome")
	}
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, browserRequest(http.MethodGet, "/api/v1/book-request-outcome?book_id="+f.book.ID+"&key=rename", f.session))
	var outcome ledger.Outcome
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &outcome) != nil || outcome.Status != "recorded" || !bytes.Equal(outcome.Result, accepted) {
		t.Fatal("browser lost-response lookup failed", w.Code)
	}
	w = httptest.NewRecorder()
	s.Handler.ServeHTTP(w, settingsRequest(f.session, strings.Replace(body, `"rename"`, `"new-key"`, 1)))
	if w.Code != http.StatusConflict {
		t.Fatal("stale save overwrote accepted settings", w.Code)
	}
}

// This test also runs in the minimal server image, without system zone files.
func TestBookCalendarUsesEmbeddedZonesAndPreservesUnknowns(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 30, 0, 0, time.UTC)
	month, day := 4, 1
	book := ledger.Book{AccountingTimezone: "America/New_York", FiscalYearStartMonth: &month, FiscalYearStartDay: &day}
	got := browserBook(book, now)
	if got.AccountingToday != "2025-12-31" || !got.CalendarConfigured {
		t.Fatal("named calendar unavailable or uses the host date")
	}
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	_, winter := now.In(zone).Zone()
	_, summer := now.AddDate(0, 6, 0).In(zone).Zone()
	if winter != -5*3600 || summer != -4*3600 {
		t.Fatal("timezone transitions missing")
	}
	for _, zone := range []string{"", "Local", "Unknown/Example"} {
		book.AccountingTimezone = zone
		got = browserBook(book, now)
		if got.AccountingToday != "" || got.CalendarConfigured {
			t.Fatal("unknown calendar acquired host defaults", zone)
		}
	}
	book.AccountingTimezone, book.FiscalYearStartMonth, book.FiscalYearStartDay = "UTC", nil, nil
	if got = browserBook(book, now); got.CalendarConfigured || got.AccountingToday != "2026-01-01" {
		t.Fatal("missing fiscal boundary was invented")
	}
}
