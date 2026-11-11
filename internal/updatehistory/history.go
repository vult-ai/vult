// Package updatehistory keeps this installation's update history: one JSON
// object per line, each carrying the SHA-256 of the line before it, so a
// removed or edited line shows. It holds no Book data.
package updatehistory

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Rotation: the history moves aside at MaxSize, keeping Keep old files.
const (
	MaxSize = 10 << 20
	Keep    = 2
	maxLine = 64 << 10
)

// genesis is the previous-line hash of the first line ever written.
var genesis = hex.EncodeToString(make([]byte, sha256.Size))

// Event is one line of history.
type Event struct {
	Time        time.Time      `json:"time"`
	Transaction string         `json:"transaction,omitempty"`
	Phase       string         `json:"phase"`
	Outcome     string         `json:"outcome,omitempty"`
	Code        string         `json:"code,omitempty"` // a fixed reason code
	Reason      string         `json:"reason,omitempty"`
	Installed   string         `json:"installed,omitempty"` // version
	Available   string         `json:"available,omitempty"`
	Digest      string         `json:"digest,omitempty"`
	Metadata    map[string]int `json:"metadata,omitempty"` // role versions seen
	SchemaFrom  string         `json:"schema_from,omitempty"`
	SchemaTo    string         `json:"schema_to,omitempty"`
	Helper      string         `json:"helper,omitempty"`
	Previous    string         `json:"previous"` // SHA-256 of the line before
}

// Log is a history file.
type Log struct {
	Path string
	Now  func() time.Time
	// Sync makes a write durable; os.File.Sync when nil.
	Sync func(*os.File) error
}

// ErrBroken means the history was edited or lines were removed.
var ErrBroken = errors.New("the update history was changed outside Vult")

func (l *Log) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// lastHash returns the hash of the file's last line, or "" for an empty or
// missing file.
func lastHash(path string) (string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	start := info.Size() - maxLine - 1
	if start < 0 {
		start = 0
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return "", err
	}
	buf = bytes.TrimSuffix(buf, []byte("\n"))
	if len(buf) == 0 {
		return "", nil
	}
	if i := bytes.LastIndexByte(buf, '\n'); i >= 0 {
		buf = buf[i+1:]
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

// Append writes one event, rotating first if the file is full.
func (l *Log) Append(e Event) error {
	if err := l.rotate(); err != nil {
		return err
	}
	prev, err := lastHash(l.Path)
	if err != nil {
		return err
	}
	if prev == "" {
		// A fresh file after rotation continues the chain from the last
		// line of the newest old file.
		if prev, err = lastHash(l.Path + ".1"); err != nil {
			return err
		}
		if prev == "" {
			prev = genesis
		}
	}
	if e.Time.IsZero() {
		e.Time = l.now()
	}
	e.Time = e.Time.UTC()
	e.Previous = prev
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(line) > maxLine {
		return errors.New("history event too large")
	}
	f, err := os.OpenFile(l.Path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(line, '\n')); err == nil {
		if l.Sync != nil {
			err = l.Sync(f)
		} else {
			err = f.Sync()
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (l *Log) rotate() error {
	info, err := os.Stat(l.Path)
	if errors.Is(err, os.ErrNotExist) || err == nil && info.Size() < MaxSize {
		return nil
	}
	if err != nil {
		return err
	}
	for i := Keep; i >= 1; i-- {
		from := l.Path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", l.Path, i-1)
		}
		if err := os.Rename(from, fmt.Sprintf("%s.%d", l.Path, i)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncDir(filepath.Dir(l.Path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Read returns the events in the current file, oldest first, checking the
// chain. A broken chain returns the events read so far and ErrBroken.
func Read(path string) ([]Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4096), maxLine+1)
	prev := ""
	for scanner.Scan() {
		line := scanner.Bytes()
		var e Event
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			return events, fmt.Errorf("%w: unreadable line %d", ErrBroken, len(events)+1)
		}
		// The first line may continue a rotated file's chain.
		if prev != "" && e.Previous != prev {
			return events, fmt.Errorf("%w: line %d doesn't follow the one before", ErrBroken, len(events)+1)
		}
		sum := sha256.Sum256(line)
		prev = hex.EncodeToString(sum[:])
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		return events, err
	}
	return events, nil
}

// Head returns the hash of the newest line, which the helper also records
// in its status file: comparing the two shows if lines were cut from the end.
func Head(path string) string {
	h, err := lastHash(path)
	if err != nil {
		return ""
	}
	return h
}
