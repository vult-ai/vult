package server

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/vult-ai/vult/access"
	"github.com/vult-ai/vult/ledger"
)

const avatarPrefix = "/api/v1/me/avatar/"
const userSettingsLimit = 200704

var avatarRevision = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (a *webAPI) userSettings(w http.ResponseWriter, r *http.Request, session string) {
	if r.URL.RawQuery != "" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	var out access.UserSettings
	var err error
	if r.Method == http.MethodGet {
		out, err = a.sessions.UserSettings(r.Context(), session)
	} else {
		var patch access.UserSettingsPatch
		if err = readOwnerJSON(w, r, userSettingsLimit, &patch, "profile.photo"); err != nil {
			apiFailure(w, err)
			return
		}
		out, err = a.sessions.SaveUserSettings(r.Context(), session, patch)
	}
	if err != nil {
		apiFailure(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *webAPI) userAvatar(w http.ResponseWriter, r *http.Request, session string) {
	id := strings.TrimPrefix(r.URL.Path, avatarPrefix)
	q, err := ownerQuery(r)
	if err != nil || len(q) != 1 || !ledger.ValidID(id) || !avatarRevision.MatchString(q.Get("revision")) {
		apiFailure(w, ledger.ErrNotFound)
		return
	}
	data, err := a.sessions.UserAvatar(r.Context(), session, id, q.Get("revision"))
	if err != nil {
		apiFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(data)
}
