package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/ledger"
)

const bookLogoPath = bookIdentityPath + "/logo"

func bookLogoWrite(r *http.Request) bool {
	return r.URL.Path == bookLogoPath && (r.Method == http.MethodPut || r.Method == http.MethodDelete)
}

func readBookLogoChange(w http.ResponseWriter, r *http.Request) (ledger.BookLogoChange, error) {
	if r.Method == http.MethodDelete {
		body, err := readOwnerBody(w, r, 1)
		if err == nil && len(body) != 0 {
			err = ledger.ErrInvalid
		}
		return ledger.BookLogoChange{}, err
	}
	body, err := readOwnerBody(w, r, ledger.MaxBookLogoBytes)
	if err != nil {
		return ledger.BookLogoChange{}, err
	}
	return ledger.NewBookLogoChange(one(r.Header, "Content-Type"), one(r.Header, "X-Vult-Image-SHA256"), body)
}

func (a *webAPI) writeBookLogo(w http.ResponseWriter, r *http.Request, session string) {
	book, err := ownerBookID(r)
	if err != nil {
		apiFailure(w, err)
		return
	}
	ctx := r.Context()
	tenant, err := a.sessions.BookTenant(ctx, session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	change, err := readBookLogoChange(w, r)
	if err != nil {
		apiFailure(w, err)
		return
	}
	result, err := a.sessions.SetBookLogo(ctx, session, tenant, book, change, a.admit)
	if err != nil {
		apiFailure(w, err)
		return
	}
	_, _ = w.Write(result)
}

// The remote writer uses the same bounded image path and agent lease as MCP.
func (a *webAPI) agentBookLogo(w http.ResponseWriter, r *http.Request, db *pgxpool.Pool, admission Admission) {
	if len(r.Header.Values("Cookie")) != 0 {
		apiFailure(w, ledger.ErrDenied)
		return
	}
	value := one(r.Header, "Authorization")
	if !strings.HasPrefix(value, "Bearer ") || len(value) != 50 {
		unauthorized(w, a.sessions)
		return
	}
	token := strings.TrimPrefix(value, "Bearer ")
	ctx := r.Context()
	scope, err := a.sessions.ResolveAgent(ctx, token)
	if err != nil {
		unauthorized(w, a.sessions)
		return
	}
	book, err := ownerBookID(r)
	if err != nil {
		apiFailure(w, err)
		return
	}
	if scope.BookID != book {
		apiFailure(w, ledger.ErrNotFound)
		return
	}
	if scope.Access != "write" || admission != nil && admission.Check(ctx, scope, "set_book_logo") != nil {
		apiFailure(w, ledger.ErrDenied)
		return
	}
	change, err := readBookLogoChange(w, r)
	if err != nil {
		apiFailure(w, err)
		return
	}
	key := ""
	if values := r.Header.Values("Idempotency-Key"); len(values) != 0 {
		if len(values) != 1 || values[0] == "" {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
		key = values[0]
	}
	result, err := remoteStore(db, a.sessions, token, admission, nil).SetBookLogo(ctx, scope, key, change)
	if err != nil {
		apiFailure(w, err)
		return
	}
	_, _ = w.Write(result)
}

func (a *webAPI) readBookLogo(w http.ResponseWriter, r *http.Request, session string) {
	q, err := ownerQuery(r)
	book := q.Get("book_id")
	if err != nil || !ledger.ValidID(book) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	ctx := r.Context()
	id := strings.TrimPrefix(r.URL.Path, bookLogoPath+"/")
	history := r.URL.Path == bookLogoPath
	before := int64(0)
	if history {
		if q.Has("before_revision") {
			before, err = strconv.ParseInt(q.Get("before_revision"), 10, 64)
			if err != nil || before < 1 {
				apiFailure(w, ledger.ErrInvalid)
				return
			}
			q.Del("before_revision")
		}
		if len(q) != 1 {
			apiFailure(w, ledger.ErrInvalid)
			return
		}
	} else if !ledger.ValidID(id) || len(q) != 2 || !lowerHexDigest(q.Get("revision")) {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, err := a.sessions.BookTenant(ctx, session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out ledger.BookLogoHistory
	var data []byte
	var media string
	err = a.sessions.WithOwner(ctx, session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		if history {
			out, err = owner.BookLogoHistory(ctx, book, before)
		} else {
			media, data, err = owner.BookLogoImage(ctx, book, id, q.Get("revision"))
		}
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	if history {
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Vary", "Cookie")
	_, _ = w.Write(data)
}

func lowerHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
