package daemon

import (
	"strings"
	"testing"
	"time"
)

func TestControlDataClean(t *testing.T) {
	clean := "pg_control version number:            1800\nDatabase cluster state:               shut down\nLatest checkpoint location:           0/1"
	if err := controlDataClean([]byte(clean)); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{
		strings.Replace(clean, "shut down", "in production", 1),
		strings.Replace(clean, "shut down", "shut down in recovery", 1),
		"pg_control version number: 1800\n",
		"",
	} {
		if err := controlDataClean([]byte(out)); err == nil {
			t.Errorf("accepted %q", out)
		}
	}
}

func TestRecoveryIDsSortByTimeAndDiffer(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	a, b := newRecoveryID(now), newRecoveryID(now)
	if a == b || !strings.HasPrefix(a, "20261110T120000Z-") || len(a) != len("20261110T120000Z-")+8 {
		t.Fatalf("%q, %q", a, b)
	}
}
