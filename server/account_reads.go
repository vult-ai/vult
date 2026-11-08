package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/vult-ai/vult/ledger"
)

func accountQuery(w http.ResponseWriter, r *http.Request) (string, string, []string, ledger.AccountDetailQuery, error) {
	var detail ledger.AccountDetailQuery
	detail.Limit = 100
	if len(r.URL.RawQuery) > 8192 {
		return "", "", nil, detail, ledger.ErrInvalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", "", nil, detail, ledger.ErrInvalid
	}
	isDetail := r.URL.Path == "/api/v1/account-detail"
	allowed := map[string]bool{"book_id": true, "as_of": !isDetail, "codes": r.Method == http.MethodGet && r.URL.Path != "/api/v1/account-activity", "from": isDetail, "to": isDetail, "view": isDetail, "revision": isDetail, "limit": isDetail, "offset": isDetail}
	for key, values := range q {
		if !allowed[key] || len(values) != 1 {
			return "", "", nil, detail, ledger.ErrInvalid
		}
	}
	if !ledger.ValidID(q.Get("book_id")) {
		return "", "", nil, detail, ledger.ErrInvalid
	}
	var ids []string
	if r.Method == http.MethodPost {
		// A JSON list of 10,000 canonical UUIDs takes under 400 KiB.
		var selection struct {
			Codes []string `json:"codes"`
		}
		if err := readOwnerJSON(w, r, 400*1024, &selection, ""); err != nil {
			return "", "", nil, detail, err
		}
		if len(selection.Codes) == 0 {
			return "", "", nil, detail, ledger.ErrInvalid
		}
		ids = selection.Codes
	} else if text, exists := q["codes"]; exists {
		if json.Unmarshal([]byte(text[0]), &ids) != nil || len(ids) == 0 {
			return "", "", nil, detail, ledger.ErrInvalid
		}
	}
	if isDetail {
		if len(ids) == 0 {
			return "", "", nil, detail, ledger.ErrInvalid
		}
		detail.AccountIDs, detail.From, detail.To, detail.Mode, detail.Revision = ids, q.Get("from"), q.Get("to"), q.Get("view"), q.Get("revision")
		for _, key := range []string{"limit", "offset"} {
			if text, exists := q[key]; exists {
				n, err := strconv.Atoi(text[0])
				if err != nil {
					return "", "", nil, detail, ledger.ErrInvalid
				}
				if key == "limit" {
					detail.Limit = n
				} else {
					detail.Offset = n
				}
			}
		}
	}
	return q.Get("book_id"), q.Get("as_of"), ids, detail, nil
}

func (a *webAPI) accountRead(w http.ResponseWriter, r *http.Request, session string) {
	book, asOf, ids, detail, err := accountQuery(w, r)
	if err != nil {
		apiFailure(w, err)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	var out any
	err = a.sessions.WithOwner(r.Context(), session, tenant, false, func(owner ledger.TenantAdmin) error {
		var err error
		if r.URL.Path == "/api/v1/account-detail" {
			out, err = owner.AccountDetail(r.Context(), book, detail)
		} else {
			out, err = owner.AccountActivity(r.Context(), book, asOf, ids)
		}
		return err
	})
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}
