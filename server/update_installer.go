package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/internal/updatestatus"
	"github.com/vult-ai/vult/internal/updatetrust"
	"github.com/vult-ai/vult/ledger"
)

// Updater is the installer on the owner's own computer: what it has done,
// and a way for the owner to ask it to install an update now. Only Vult
// built to update itself sets it.
type Updater interface {
	Report() updatestatus.Report
	Request(version string) error
}

// updateInstallPath asks the installer to install the newest release.
const updateInstallPath = "/api/v1/update/install"

func (a *webAPI) updateStatus(w http.ResponseWriter, r *http.Request) {
	out := struct {
		SchemaVersion int    `json:"schema_version"`
		Current       string `json:"current"`
		Latest        string `json:"latest,omitempty"`
		Available     bool   `json:"available"`
		// Installer is present only where the owner can install from Vult.
		Installer *updatestatus.Report `json:"installer,omitempty"`
	}{SchemaVersion: 1}
	if a.updates != nil {
		s := a.updates.Status(r.Context())
		out.Current, out.Latest, out.Available = s.Current, s.Latest, s.Available
	}
	if a.updater != nil {
		if report := a.updater.Report(); report.Enabled {
			out.Installer = &report
			if report.Installed != nil {
				out.Current = report.Installed.Version
				if release := report.Release; release != nil && release.Fresh(out.Current, time.Now()) {
					out.Latest = release.Latest
					out.Available = updatetrust.Newer(out.Current, out.Latest)
				}
			}
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (a *webAPI) updateInstall(w http.ResponseWriter, r *http.Request, session string) {
	if a.updater == nil || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/json" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	if _, err := a.sessions.Identity(r.Context(), session); err != nil {
		failure(w, http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256))
	var q struct {
		Version string `json:"version"`
	}
	if err != nil || strictjson.Decode(body, &q) != nil || q.Version == "" {
		apiFailure(w, ledger.ErrInvalid)
		return
	}
	if err := a.updater.Request(q.Version); err != nil {
		failure(w, http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(struct {
		SchemaVersion int                 `json:"schema_version"`
		Installer     updatestatus.Report `json:"installer"`
	}{1, a.updater.Report()})
}
