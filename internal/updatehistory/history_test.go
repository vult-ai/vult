package updatehistory

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestHistoryChainsAndShowsEdits(t *testing.T) {
	path := t.TempDir() + "/history.jsonl"
	l := &Log{Path: path, Now: func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) }}
	for _, phase := range []string{"planned", "fenced", "idle"} {
		if err := l.Append(Event{Phase: phase}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := Read(path)
	if err != nil || len(events) != 3 || events[0].Previous != genesis {
		t.Fatalf("%d events, %v", len(events), err)
	}
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for name, changed := range map[string]string{
		"edited":  strings.Join([]string{lines[0], strings.Replace(lines[1], "fenced", "planned", 1), lines[2]}, "\n"),
		"removed": strings.Join([]string{lines[0], lines[2]}, "\n"),
		"garbled": lines[0] + "\n{\n" + lines[2],
	} {
		os.WriteFile(path, []byte(changed+"\n"), 0o644)
		if _, err := Read(path); !errors.Is(err, ErrBroken) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Cutting lines off the end leaves a valid chain; the head recorded
	// elsewhere is what shows it.
	os.WriteFile(path, []byte(lines[0]+"\n"+lines[1]+"\n"), 0o644)
	if _, err := Read(path); err != nil {
		t.Fatal(err)
	}
	if Head(path) == "" {
		t.Fatal("no head")
	}
}

// fill makes the current file look full without writing 10 MB of lines.
func fill(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()
	if err := f.Truncate(info.Size() + MaxSize); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryRotatesAndContinuesTheChain(t *testing.T) {
	path := t.TempDir() + "/history.jsonl"
	l := &Log{Path: path}
	if err := l.Append(Event{Phase: "planned"}); err != nil {
		t.Fatal(err)
	}
	before := Head(path)
	fill(t, path)
	if err := l.Append(Event{Phase: "fenced"}); err != nil {
		t.Fatal(err)
	}
	events, err := Read(path)
	if err != nil || len(events) != 1 || events[0].Phase != "fenced" {
		t.Fatalf("after rotating: %+v, %v", events, err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("the full file was not kept")
	}
	// The new file's first line follows on from the old file's last line,
	// which ends in zero padding here, so compare with that file's head.
	if events[0].Previous != Head(path+".1") || before == "" {
		t.Fatal("the chain did not continue across rotation")
	}
	for i := 0; i < 3; i++ {
		fill(t, path)
		if err := l.Append(Event{Phase: "idle"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{path + ".1", path + ".2"} {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("%s missing", name)
		}
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Error("kept more than two old files")
	}
}
