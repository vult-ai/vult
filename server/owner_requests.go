package server

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/ledger"
)

func readOwnerJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any, nullable string) error {
	if len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Type") != "application/json" {
		return ledger.ErrInvalid
	}
	data, err := readOwnerBody(w, r, limit)
	if err != nil {
		return err
	}
	if strictjson.DecodeNonNull(data, dst, nullable) != nil {
		return ledger.ErrInvalid
	}
	return nil
}

func readOwnerBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, ledger.ErrLimit
	}
	if err != nil {
		return nil, ledger.ErrInvalid
	}
	return body, nil
}

func ownerQuery(r *http.Request) (url.Values, error) {
	if len(r.URL.RawQuery) > 1024 {
		return nil, ledger.ErrInvalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, ledger.ErrInvalid
	}
	for _, values := range q {
		if len(values) != 1 {
			return nil, ledger.ErrInvalid
		}
	}
	return q, nil
}

func ownerBookID(r *http.Request) (string, error) {
	q, err := ownerQuery(r)
	if err != nil || len(q) != 1 || !ledger.ValidID(q.Get("book_id")) {
		return "", ledger.ErrInvalid
	}
	return q.Get("book_id"), nil
}

func ownerSettingsRoute(path string) bool {
	return path == "/api/v1/me/settings" || strings.HasPrefix(path, avatarPrefix) ||
		path == connectionPath || strings.HasPrefix(path, connectionPath+"/") ||
		path == "/api/v1/books" || path == bookViewPreferencePath ||
		path == bookIdentityPath || path == bookLogoPath || strings.HasPrefix(path, bookLogoPath+"/") || path == bookAddressPath || bookExportType(path) != ""
}
