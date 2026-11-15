package updatestatus

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/vult-ai/vult/internal/strictjson"
)

// Request is the owner asking to install an update now. The app writes it;
// the root helper starts when it changes, treats it as untrusted, and only
// ever installs the newest release both signatures agree on.
//
// A request may instead ask to put a backup back (Restore names it, Job
// names this attempt); the helper checks the backup itself.
type Request struct {
	Version   string    `json:"version,omitempty"` // what the owner saw offered
	Restore   string    `json:"restore,omitempty"`
	Job       string    `json:"job,omitempty"`
	Requested time.Time `json:"requested"`
}

// Valid reports whether r is one well-formed request of either kind.
func (r Request) Valid() bool {
	if r.Restore != "" {
		return r.Version == "" && BackupID.MatchString(r.Restore) && ValidJob(r.Job)
	}
	return r.Job == "" && versionPattern.MatchString(r.Version)
}

const requestFile = "request.json"

var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// WriteRequest records the owner's request. The installer creates the file
// for the account that serves the app; writing in place keeps that
// ownership, and never follows a link.
func WriteRequest(dir string, r Request) error {
	if !versionPattern.MatchString(r.Version) {
		return errors.New("not a version")
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeSignal(dir, requestFile, data)
}

// ReadRequest returns a pending request, if the file holds a readable one.
func ReadRequest(dir string) (Request, bool) {
	f, err := os.OpenFile(filepath.Join(dir, requestFile), os.O_RDONLY|syscallNoFollow, 0)
	if err != nil {
		return Request{}, false
	}
	defer f.Close()
	data := make([]byte, 1025)
	n, _ := f.Read(data)
	if n == 0 || n > 1024 {
		return Request{}, false
	}
	var r Request
	if strictjson.Decode(data[:n], &r) != nil || !r.Valid() {
		return Request{}, false
	}
	return r, true
}

// ClearRequest empties the request once the helper has taken it.
func ClearRequest(dir string) error {
	return clearSignal(dir, requestFile)
}
