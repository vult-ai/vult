package updatestatus

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
	"github.com/vult-ai/vult/internal/updatetxn"
)

const (
	checkFile  = "check.json"
	CheckEvery = 12 * time.Hour
	checkWait  = 2 * time.Minute
)

// ReleaseStatus is the result of checking signed release metadata. The helper
// writes it; the browser can read it without access to the update repository.
type ReleaseStatus struct {
	Current string    `json:"current"`
	Latest  string    `json:"latest,omitempty"`
	Checked time.Time `json:"checked"`
	Reason  string    `json:"reason,omitempty"`
}

func (s ReleaseStatus) Fresh(current string, now time.Time) bool {
	age := now.Sub(s.Checked)
	return s.Current == current && age >= 0 && age < CheckEvery
}

func WriteReleaseStatus(dir string, s ReleaseStatus) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return updatetxn.WriteDurably(filepath.Join(dir, "release.json"), data, 0o644)
}

type checkRequest struct {
	Requested time.Time `json:"requested"`
}

func readCheck(dir string) (checkRequest, bool) {
	f, err := os.OpenFile(filepath.Join(dir, checkFile), os.O_RDONLY|syscallNoFollow, 0)
	if err != nil {
		return checkRequest{}, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1025))
	var r checkRequest
	if err != nil || len(data) > 1024 || strictjson.Decode(data, &r) != nil || r.Requested.IsZero() {
		return r, false
	}
	return r, true
}

// CheckRequested is a request to fetch metadata only. It cannot authorize an
// install, and is kept separate so checking cannot overwrite an install request.
func CheckRequested(dir string) bool { _, ok := readCheck(dir); return ok }

// RequestCheck asks the existing privileged helper to discover releases. The
// installer precreates this file for the browser, just like request.json.
// Repeated status reads share one check, and stop polling if the helper is away.
func RequestCheck(dir, current string, now time.Time) (bool, error) {
	var release ReleaseStatus
	if ok, err := readJSON(filepath.Join(dir, "release.json"), &release); err == nil && ok && release.Fresh(current, now) {
		return false, nil
	}
	if r, ok := readCheck(dir); ok {
		age := now.Sub(r.Requested)
		if age >= 0 && age < CheckEvery {
			return age < checkWait, nil
		}
	}
	data, err := json.Marshal(checkRequest{Requested: now.UTC()})
	if err != nil {
		return false, err
	}
	if err := writeSignal(dir, checkFile, data); err != nil {
		return false, err
	}
	return true, nil
}

func ClearCheck(dir string) error { return clearSignal(dir, checkFile) }

func writeSignal(dir, name string, data []byte) error {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_TRUNC|syscallNoFollow, 0)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func clearSignal(dir, name string) error {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_TRUNC|syscallNoFollow, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return f.Close()
}
