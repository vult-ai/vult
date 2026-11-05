package local

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/vult-ai/vult/ledger"
)

func TestArchiveFramesRoundTripAndRejectMissingOrOversizedFrames(t *testing.T) {
	data := bytes.Repeat([]byte("synthetic bytes\n"), 20000)
	var encoded bytes.Buffer
	writer := &FrameWriter{Destination: &encoded}
	if n, err := writer.Write(data); err != nil || n != len(data) {
		t.Fatal(err)
	}
	if err := writer.End(); err != nil {
		t.Fatal(err)
	}
	returned, err := io.ReadAll(&FrameReader{Source: bytes.NewReader(encoded.Bytes())})
	if err != nil || !bytes.Equal(returned, data) {
		t.Fatal("stream frames changed bytes", err)
	}
	if _, err = io.ReadAll(&FrameReader{Source: bytes.NewReader(encoded.Bytes()[:encoded.Len()-4])}); err == nil {
		t.Fatal("missing terminator accepted")
	}
	var oversized bytes.Buffer
	if err = binary.Write(&oversized, binary.BigEndian, uint32(MaxArchiveFrame+1)); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(&FrameReader{Source: &oversized}); !errors.Is(err, ledger.ErrInvalid) {
		t.Fatal("oversized frame accepted", err)
	}
}

func TestStreamedOutputPublishesOnlyCompleteFilesAndNeverReplaces(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "books.age")
	if err := WriteNewStream(path, func(dst io.Writer) error { _, err := io.WriteString(dst, "synthetic complete output"); return err }); err != nil {
		t.Fatal(err)
	}
	if err := WriteNewStream(path, func(io.Writer) error { t.Fatal("existing output reached writer"); return nil }); err == nil {
		t.Fatal("existing output overwritten")
	}
	failed := filepath.Join(directory, "failed.age")
	if err := WriteNewStream(failed, func(dst io.Writer) error { io.WriteString(dst, "partial"); return ledger.ErrUncertain }); !errors.Is(err, ledger.ErrUncertain) {
		t.Fatal("failed output reported success", err)
	}
	if _, err := os.Lstat(failed); !os.IsNotExist(err) {
		t.Fatal("partial output published", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "books.age" {
		t.Fatal("output staging not cleaned", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("output not private", err)
	}
	link := filepath.Join(directory, "symlink.age")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err = WriteNewStream(link, func(io.Writer) error { return nil }); err == nil {
		t.Fatal("output symlink replaced")
	}
}
