package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSnapshotTimesCompareInstantsWithoutRewritingOpaqueValues(t *testing.T) {
	for _, name := range []string{"created_at", "recorded_at", "closed_at", "ready_at", "expires_at", "used_at"} {
		t.Run(name, func(t *testing.T) {
			row := map[string]any{name: "2026-01-02T13:00:00.123456+13:00", "request_bytes": `\x7b22637265617465645f6174223a226f7061717565227d`, "payload": map[string]any{"created_at": "opaque", "missing": nil}, "amount_minor": "0", "economic_date": "2026-01-02"}
			first, err := canonicalSnapshotTimes(raw([]any{row}))
			if err != nil {
				t.Fatal(err)
			}
			row[name] = "2026-01-02T00:00:00.123456Z"
			second, err := canonicalSnapshotTimes(raw([]any{row}))
			if err != nil || !bytes.Equal(first, second) {
				t.Fatal("equal timestamp instants differed")
			}
			var got []map[string]json.RawMessage
			if json.Unmarshal(first, &got) != nil || !bytes.Equal(got[0]["payload"], raw(row["payload"])) || !bytes.Equal(got[0]["request_bytes"], raw(row["request_bytes"])) {
				t.Fatal("opaque retained values changed")
			}
			row[name] = "2026-01-02T00:00:00.123457Z"
			changed, err := canonicalSnapshotTimes(raw([]any{row}))
			if err != nil || bytes.Equal(first, changed) {
				t.Fatal("different timestamp instants compared equal")
			}
		})
	}
}

func TestArchiveDocumentTimesKeepPostgresSecondsOffsets(t *testing.T) {
	input := map[string]any{
		"tenant_id": "11111111-1111-4111-8111-111111111111", "book_id": "22222222-2222-4222-8222-222222222222", "id": "33333333-3333-4333-8333-333333333333",
		"storage_id": strings.Repeat("a", 64), "key_id": "example", "sha256": strings.Repeat("b", 64), "size_bytes": 1,
		"state": "ready", "original_name": "example.pdf", "media_type": "application/pdf",
		"used_at": "1969-12-31T23:15:30-00:44:30", "expires_at": "1970-01-01T00:15:30-00:44:30",
	}
	var row map[string]json.RawMessage
	if json.Unmarshal(raw(input), &row) != nil {
		t.Fatal("invalid synthetic document")
	}
	_, _, used, expires, err := documentObject(row, false)
	if err != nil || used == nil || !used.Equal(time.Unix(0, 0)) || !expires.Equal(time.Unix(3600, 0)) {
		t.Fatal("document timestamp instants changed", err)
	}
	row["used_at"] = json.RawMessage("null")
	if _, _, used, _, err = documentObject(row, false); err != nil || used != nil {
		t.Fatal("nullable document use changed", err)
	}
}

func TestSnapshotTimesRejectInvalidOrUnrepresentableMetadata(t *testing.T) {
	for _, data := range []string{`null`, `[null]`, `[{"recorded_at":1}]`, `[{"recorded_at":"not-a-time"}]`, `[{"recorded_at":"2026-01-02T00:00:00.123456001Z"}]`, `[{"recorded_at":"2026-01-02T00:00:00.1234560001Z"}]`, `[{"recorded_at":"2026-01-02T00:00:00.12345600000000000001Z"}]`, `[{"recorded_at":"2026-01-02T0:00:00.1234560001Z"}]`, `[{"recorded_at":"2026-01-02T0:00:00,1234560001Z"}]`} {
		if _, err := canonicalSnapshotTimes([]byte(data)); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid timestamp metadata was accepted")
		}
	}
	if got, err := canonicalSnapshotTimes([]byte(`[{"ready_at":null,"used_at":null}]`)); err != nil || string(got) != `[{"ready_at":null,"used_at":null}]` {
		t.Fatal("nullable timestamps changed")
	}
	if got, err := canonicalSnapshotTimes([]byte(`[{"recorded_at":"2026-01-02T00:00:00.123456000000Z"}]`)); err != nil || string(got) != `[{"recorded_at":"2026-01-02T00:00:00.123456Z"}]` {
		t.Fatal("exact trailing zeros changed timestamp precision")
	}
	if got, err := canonicalSnapshotTimes([]byte(`[{"recorded_at":"2026-01-02T05:06:07.123456+05:06:07"}]`)); err != nil || string(got) != `[{"recorded_at":"2026-01-02T00:00:00.123456Z"}]` {
		t.Fatal("PostgreSQL seconds-offset timezone changed the instant", err)
	}
}
