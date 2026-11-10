package access

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/vult-ai/vult/internal/strictjson"
)

// authenticatorRoute serves authenticator-app enrolment and sign-in for the
// local owner. Like the passkey routes, every request is same-origin JSON that
// carries the browser's sign-in nonce as both a cookie and a header.
func (h *HTTP) authenticatorRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	switch r.URL.Path {
	case "/auth/authenticator/enroll/options", "/auth/authenticator/enroll/verify", "/auth/authenticator/login":
	default:
		fail(w, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		fail(w, http.StatusNotFound)
		return
	}
	if r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 {
		fail(w, http.StatusForbidden)
		return
	}
	nonces := r.CookiesNamed(loginCookie)
	if len(nonces) != 1 || !validToken(nonces[0].Value) || len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.sessions.origin || len(r.Header.Values("X-CSRF-Token")) != 1 || subtle.ConstantTimeCompare([]byte(nonces[0].Value), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
		fail(w, http.StatusForbidden)
		return
	}
	if len(r.Header.Values("Content-Type")) != 1 {
		fail(w, http.StatusForbidden)
		return
	}
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		fail(w, http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		fail(w, http.StatusBadRequest)
		return
	}
	browser := nonces[0].Value

	if r.URL.Path == "/auth/authenticator/enroll/options" {
		var q struct {
			Setup string `json:"setup_token,omitempty"`
		}
		if strictjson.Decode(data, &q) != nil {
			fail(w, http.StatusBadRequest)
			return
		}
		// Without a setup code, an owner who signed in within the last five
		// minutes may add an authenticator to their passkey.
		session := ""
		if q.Setup == "" {
			var fresh bool
			if session, fresh = h.freshSession(r); !fresh {
				fail(w, http.StatusUnauthorized)
				return
			}
		}
		enrollment, err := h.totp.BeginEnrollment(r.Context(), q.Setup, session, browser)
		if err != nil {
			fail(w, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(enrollment)
		return
	}

	var q struct {
		Code string `json:"code"`
	}
	if strictjson.Decode(data, &q) != nil || len(q.Code) > 32 {
		fail(w, http.StatusBadRequest)
		return
	}
	var session string
	enrolling := r.URL.Path == "/auth/authenticator/enroll/verify"
	if enrolling {
		session, err = h.totp.FinishEnrollment(r.Context(), q.Code, browser)
	} else {
		session, err = h.totp.SignIn(r.Context(), q.Code)
	}
	switch {
	case errors.Is(err, ErrInvalidCode):
		codeError(w, http.StatusBadRequest, "invalid_code", "That code didn't work. Enter the newest 6-digit code from the authenticator app. If it keeps failing, check that the phone's clock is set automatically.")
		return
	case errors.Is(err, ErrTooManyAttempts):
		codeError(w, http.StatusTooManyRequests, "too_many_attempts", "Too many codes were tried. Wait five minutes, then try again.")
		return
	case err != nil:
		fail(w, http.StatusUnauthorized)
		return
	}
	cookie(w, loginCookie, "", -1)
	h.setSession(w, r, session)
	w.Header().Set("Content-Type", "application/json")
	destination := h.signInDestination(w, r, "/")
	if enrolling {
		// A new sign-in method: show any recovery codes owed once, in this
		// browser, exactly as after a first passkey.
		destination = "/auth/recovery-codes"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"signed_in": true, "continue": destination})
}

func codeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}
