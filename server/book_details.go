package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/vult-ai/vult/ledger"
)

const bookIdentityPath = "/api/v1/book-identity"
const bookAddressPath = "/api/v1/book-address"

func (a *webAPI) bookIdentity(w http.ResponseWriter, r *http.Request, session string) {
	if r.Method == http.MethodGet {
		a.readBookIdentity(w, r, session)
		return
	}
	if r.URL.RawQuery != "" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	var change ledger.BookIdentityChange
	if err := readOwnerJSON(w, r, 8192, &change, ""); err != nil {
		apiFailure(w, err)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, change.BookID)
	if err != nil {
		apiFailure(w, err)
		return
	}
	input, err := json.Marshal(change)
	if err != nil {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	ctx := r.Context()
	data, err := a.sessions.OwnerRequest(ctx, session, tenant, change.RequestKey, "revise_book_identity", input, a.admit, func(owner ledger.TenantAdmin) (json.RawMessage, error) {
		out, err := owner.ReviseBookIdentity(ctx, change)
		if err != nil {
			return nil, err
		}
		return json.Marshal(out)
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_, _ = w.Write(data)
}

func (a *webAPI) readBookIdentity(w http.ResponseWriter, r *http.Request, session string) {
	q, err := ownerQuery(r)
	book, offset := q.Get("book_id"), 0
	if raw, ok := q["offset"]; ok {
		offset, err = strconv.Atoi(raw[0])
		delete(q, "offset")
	}
	if err != nil || len(q) != 1 || !ledger.ValidID(book) || offset < 0 || offset > ledger.MaxBookIdentityOffset {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	ctx := r.Context()
	var out ledger.BookIdentityView
	err = a.sessions.WithOwner(ctx, session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		out, err = owner.BookIdentity(ctx, book, offset)
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

type bookAddressPatch struct {
	BookID   string  `json:"book_id"`
	Slug     *string `json:"slug"`
	Revision *int64  `json:"revision"`
}

func (a *webAPI) bookAddress(w http.ResponseWriter, r *http.Request, session string) {
	writing := r.Method == http.MethodPost
	var book string
	var patch bookAddressPatch
	var err error
	if writing {
		err = readOwnerJSON(w, r, 2048, &patch, "")
		if err == nil && (patch.Slug == nil || patch.Revision == nil || !ledger.ValidID(patch.BookID)) {
			err = ledger.ErrInvalid
		}
		book = patch.BookID
		if err == nil && r.URL.RawQuery != "" {
			selector, queryErr := ownerBookID(r)
			if queryErr != nil || selector != book {
				err = ledger.ErrInvalid
			}
		}
	} else {
		book, err = ownerBookID(r)
	}
	if err != nil {
		apiFailure(w, err)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	ctx := r.Context()
	var out ledger.BookAddressView
	err = a.sessions.WithOwner(ctx, session, tenant, writing, func(owner ledger.TenantAdmin) error {
		var err error
		if writing {
			out, err = owner.SetBookAddress(ctx, book, *patch.Slug, *patch.Revision)
		} else {
			out, err = owner.BookAddress(ctx, book)
		}
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
