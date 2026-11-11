package updatestatus

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReleaseCheckRequestsAreSeparateAndBounded(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{requestFile, checkFile} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteRequest(dir, Request{Version: "0.7.1"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	if checking, err := RequestCheck(dir, "0.7.0", now); err != nil || !checking {
		t.Fatalf("%v, %v", checking, err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, checkFile))
	if checking, err := RequestCheck(dir, "0.7.0", now.Add(time.Second)); err != nil || !checking {
		t.Fatalf("%v, %v", checking, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, checkFile))
	if string(before) != string(after) {
		t.Fatal("status reads duplicated the pending check")
	}
	if checking, err := RequestCheck(dir, "0.7.0", now.Add(checkWait)); err != nil || checking {
		t.Fatalf("an absent helper is polled forever: %v, %v", checking, err)
	}
	if r, ok := ReadRequest(dir); !ok || r.Version != "0.7.1" {
		t.Fatal("checking overwrote the install request")
	}
	if err := ClearCheck(dir); err != nil {
		t.Fatal(err)
	}
	if CheckRequested(dir) {
		t.Fatal("the completed check stayed pending")
	}
	if _, ok := ReadRequest(dir); !ok {
		t.Fatal("clearing a check cleared the install request")
	}
	if err := WriteReleaseStatus(dir, ReleaseStatus{Current: "0.7.0", Latest: "0.7.1", Checked: now}); err != nil {
		t.Fatal(err)
	}
	if checking, err := RequestCheck(dir, "0.7.0", now.Add(time.Hour)); err != nil || checking {
		t.Fatalf("fresh metadata was checked again: %v, %v", checking, err)
	}
	if checking, err := RequestCheck(dir, "0.7.0", now.Add(CheckEvery)); err != nil || !checking {
		t.Fatalf("old metadata was not refreshed: %v, %v", checking, err)
	}
}

func TestReleaseChecksDoNotFollowLinks(t *testing.T) {
	dir := t.TempDir()
	elsewhere := filepath.Join(t.TempDir(), "kept")
	if err := os.WriteFile(elsewhere, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, checkFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := RequestCheck(dir, "0.7.0", time.Now()); err == nil {
		t.Fatal("a check request followed a link")
	}
	if err := ClearCheck(dir); err == nil {
		t.Fatal("clearing a check followed a link")
	}
	if data, _ := os.ReadFile(elsewhere); string(data) != "kept" {
		t.Fatal("the linked file changed")
	}
}
