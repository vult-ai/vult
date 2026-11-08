package importarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

type member struct {
	name string
	data []byte
	mode fs.FileMode
}

func digest(data []byte) string { value := sha256.Sum256(data); return hex.EncodeToString(value[:]) }
func packageBytes(t *testing.T, members ...member) []byte {
	return packageMethod(t, zip.Store, members...)
}
func packageMethod(t *testing.T, method uint16, members ...member) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for _, m := range members {
		header := &zip.FileHeader{Name: m.name, Method: method}
		mode := m.mode
		if mode == 0 {
			mode = 0600
		}
		header.SetMode(mode)
		f, err := w.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(m.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestInspectionChecksDeflatedOriginals(t *testing.T) {
	blob := []byte(strings.Repeat("opaque source bytes\n", 2000))
	data := packageMethod(t, zip.Deflate, member{"source.json", source(t, digest(blob)), 0600}, member{"blobs/" + digest(blob), blob, 0600})
	report, err := Inspect(context.Background(), bytes.NewReader(data))
	if err != nil || report.VerifiedBlobs != 1 || report.AccountingStatus != "not_checked" {
		t.Fatal("deflated export did not verify", err)
	}
}

func TestInspectionRejectsOversizedExpansionBeforeReadingBlobContents(t *testing.T) {
	members := []member{{"source.json", source(t), 0600}}
	for i := 0; i < 9; i++ {
		members = append(members, member{"blobs/" + digest([]byte{byte(i)}), []byte{byte(i)}, 0600})
	}
	data := packageBytes(t, members...)
	for _, m := range members[1:] {
		binary.LittleEndian.PutUint32(central(data, m.name)[24:], uint32(maxBlob))
	}
	if _, err := Inspect(context.Background(), bytes.NewReader(data)); !errors.Is(err, ErrLimit) {
		t.Fatal("declared oversized expansion was not refused before blob processing", err)
	}
}

func TestInspectionPreflightRejectsCommentFooterConfusion(t *testing.T) {
	data := packageBytes(t, member{"source.json", source(t), 0600})
	outerEnd := len(data) - 22
	fakePosition := len(data)
	fake := make([]byte, 22)
	binary.LittleEndian.PutUint32(fake, 0x06054b50)
	binary.LittleEndian.PutUint32(fake[16:], uint32(fakePosition))
	comment := append(fake, byte(0))
	binary.LittleEndian.PutUint16(data[outerEnd+20:], uint16(len(comment)))
	data = append(data, comment...)
	// The pinned standard decoder selects the later footer with trailing data,
	// while the earlier outer footer still describes a complete source member.
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) != 0 {
		t.Fatal("footer-confusion fixture did not reach the alternate directory", err)
	}
	if err := checkDirectory(data); !errors.Is(err, ErrFormat) {
		t.Fatal("ambiguous footer passed preflight before member-list allocation", err)
	}
	if _, err := Inspect(context.Background(), bytes.NewReader(data)); !errors.Is(err, ErrFormat) {
		t.Fatal("ambiguous footer was accepted", err)
	}
}

func TestInspectionRejectsZIP64OffsetEvenWithSmallMembers(t *testing.T) {
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: "source.json", Method: zip.Store, Extra: append([]byte{1, 0, 8, 0}, make([]byte, 8)...)}
	header.SetMode(0600)
	f, err := w.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(source(t)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data := buffer.Bytes()
	binary.LittleEndian.PutUint32(central(data, "source.json")[42:], 0xffffffff)
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) != 1 {
		t.Fatal("synthetic ZIP64 offset did not reach the standard decoder", err)
	}
	if _, err := Inspect(context.Background(), bytes.NewReader(data)); !errors.Is(err, ErrFormat) {
		t.Fatal("ZIP64 offset bypassed the supported-format boundary", err)
	}
}
func source(t *testing.T, hashes ...string) []byte {
	t.Helper()
	artifacts := []map[string]string{}
	for _, hash := range hashes {
		artifacts = append(artifacts, map[string]string{"blob_sha256": hash, "uninterpreted_field": "retained"})
	}
	value, err := json.Marshal(map[string]any{"schema": 1, "book": map[string]string{"name": "Example Book"}, "accounts": []any{}, "records": []any{}, "artifacts": artifacts, "unknown_source_fields": map[string]string{"exact": "kept"}})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestInspectionPreservesOriginalsAndDoesNotClaimAccounting(t *testing.T) {
	blob := []byte("opaque synthetic original\n")
	input := packageBytes(t, member{"source.json", source(t, digest(blob), digest(blob)), 0600}, member{"blobs/" + digest(blob), blob, 0600})
	before := append([]byte{}, input...)
	report, err := Inspect(context.Background(), bytes.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if report.Format != "vult-source-inspection-v1" || report.AccountingStatus != "not_checked" || report.ArchiveSHA256 != digest(input) || report.SourceSHA256 != digest(source(t, digest(blob), digest(blob))) || report.VerifiedBlobs != 1 || report.ArchiveBytes != int64(len(input)) || report.ExpandedBytes != int64(len(blob)+len(source(t, digest(blob), digest(blob)))) {
		t.Fatal("inspection misstated verified byte scope", report)
	}
	if !bytes.Equal(input, before) {
		t.Fatal("inspection changed source bytes")
	}
	encoded, err := json.Marshal(report)
	if err != nil || strings.Contains(string(encoded), "Example Book") || strings.Contains(string(encoded), "opaque synthetic") {
		t.Fatal("inspection report included source contents")
	}
}

func TestInspectionRejectsAmbiguousUnsafeAndIncompletePackages(t *testing.T) {
	blob := []byte("synthetic original")
	hash := digest(blob)
	validSource := source(t, hash)
	for _, test := range []struct {
		name    string
		members []member
		want    error
	}{
		{"missing-source", []member{{"blobs/" + hash, blob, 0600}}, ErrFormat},
		{"duplicate-source", []member{{"source.json", source(t), 0600}, {"source.json", source(t), 0600}}, ErrFormat},
		{"traversal", []member{{"source.json", source(t), 0600}, {"../original", blob, 0600}}, ErrFormat},
		{"absolute", []member{{"source.json", source(t), 0600}, {"/original", blob, 0600}}, ErrFormat},
		{"symlink", []member{{"source.json", source(t), fs.ModeSymlink | 0777}}, ErrFormat},
		{"executable-mode", []member{{"source.json", source(t), 0700}}, ErrFormat},
		{"mismatched-blob", []member{{"source.json", validSource, 0600}, {"blobs/" + hash, []byte("different"), 0600}}, ErrIntegrity},
		{"missing-blob", []member{{"source.json", validSource, 0600}}, ErrIntegrity},
		{"extra-blob", []member{{"source.json", source(t), 0600}, {"blobs/" + hash, blob, 0600}}, ErrIntegrity},
		{"duplicate-json-key", []member{{"source.json", []byte(`{"schema":1,"schema":1}`), 0600}}, ErrFormat},
		{"unknown-schema", []member{{"source.json", []byte(`{"schema":2,"book":{},"accounts":[],"records":[],"artifacts":[]}`), 0600}}, ErrFormat},
		{"missing-artifact-hash", []member{{"source.json", []byte(`{"schema":1,"book":{},"accounts":[],"records":[],"artifacts":[{}]}`), 0600}}, ErrFormat},
		{"case-alias", []member{{"source.json", []byte(`{"schema":1,"book":{},"accounts":[],"records":[],"artifacts":[{"Blob_SHA256":"` + hash + `"}]}`), 0600}}, ErrFormat},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Inspect(context.Background(), bytes.NewReader(packageBytes(t, test.members...)))
			if !errors.Is(err, test.want) {
				t.Fatal("unsupported package was not refused", err)
			}
			if strings.Contains(err.Error(), "synthetic original") || strings.Contains(err.Error(), "../original") {
				t.Fatal("source content or name escaped into an error")
			}
		})
	}
}

func central(data []byte, name string) []byte {
	end := len(data) - 22
	position := int(binary.LittleEndian.Uint32(data[end+16:]))
	for position < end {
		header := data[position : position+46]
		n, x, c := int(binary.LittleEndian.Uint16(header[28:])), int(binary.LittleEndian.Uint16(header[30:])), int(binary.LittleEndian.Uint16(header[32:]))
		if string(data[position+46:position+46+n]) == name {
			return header
		}
		position += 46 + n + x + c
	}
	panic("missing synthetic central header")
}

func TestInspectionRejectsContainerConfusionBeforeMemberAllocation(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func([]byte)
		want  error
	}{
		{"spanned-disk", func(b []byte) { binary.LittleEndian.PutUint16(b[len(b)-18:], 1) }, ErrFormat},
		{"too-many-files", func(b []byte) { binary.LittleEndian.PutUint16(b[len(b)-12:], maxFiles+1) }, ErrLimit},
		{"wrong-count", func(b []byte) {
			binary.LittleEndian.PutUint16(b[len(b)-12:], 2)
			binary.LittleEndian.PutUint16(b[len(b)-14:], 2)
		}, ErrFormat},
		{"zip64-offset", func(b []byte) { binary.LittleEndian.PutUint32(b[len(b)-6:], 0xffffffff) }, ErrFormat},
		{"encrypted", func(b []byte) {
			h := central(b, "source.json")
			binary.LittleEndian.PutUint16(h[8:], binary.LittleEndian.Uint16(h[8:])|1)
		}, ErrFormat},
		{"unsupported-method", func(b []byte) { binary.LittleEndian.PutUint16(central(b, "source.json")[10:], 99) }, ErrFormat},
		{"oversized-source", func(b []byte) { binary.LittleEndian.PutUint32(central(b, "source.json")[24:], uint32(maxSource+1)) }, ErrLimit},
		{"large-metadata", func(b []byte) { binary.LittleEndian.PutUint16(central(b, "source.json")[30:], 1025) }, ErrLimit},
		{"zip64-member-size", func(b []byte) { binary.LittleEndian.PutUint32(central(b, "source.json")[24:], 0xffffffff) }, ErrFormat},
		{"checksum", func(b []byte) {
			h := central(b, "source.json")
			binary.LittleEndian.PutUint32(h[16:], binary.LittleEndian.Uint32(h[16:])+1)
		}, ErrIntegrity},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := packageBytes(t, member{"source.json", source(t), 0600})
			test.alter(data)
			if _, err := Inspect(context.Background(), bytes.NewReader(data)); !errors.Is(err, test.want) {
				t.Fatal("container boundary did not refuse malformed metadata", err)
			}
		})
	}
}

type repeatingReader struct{ read int64 }

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) { return 0, errors.New("synthetic-private-filename.pdf") }

func (r *repeatingReader) Read(p []byte) (int, error) {
	clear(p)
	r.read += int64(len(p))
	return len(p), nil
}

func TestInspectionHonoursCancellationAndCompressedBudget(t *testing.T) {
	reader := &repeatingReader{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Inspect(ctx, reader); !errors.Is(err, context.Canceled) || reader.read != 0 {
		t.Fatal("cancelled inspection consumed input", err)
	}
	if _, err := Inspect(context.Background(), reader); !errors.Is(err, ErrLimit) || reader.read != MaxArchiveSize+1 {
		t.Fatal("compressed input exceeded the finite read budget", err, reader.read)
	}
	if report, err := Inspect(context.Background(), io.LimitReader(strings.NewReader("not a package"), 13)); !errors.Is(err, ErrFormat) || !reflect.DeepEqual(report, Report{}) {
		t.Fatal("unrecognized input produced a report", err)
	}
}

func TestInspectionDoesNotExposeReaderDiagnostics(t *testing.T) {
	_, err := Inspect(context.Background(), failedReader{})
	if !errors.Is(err, ErrIntegrity) || strings.Contains(err.Error(), "synthetic-private-filename") {
		t.Fatal("input reader diagnostics escaped the package boundary", err)
	}
}
