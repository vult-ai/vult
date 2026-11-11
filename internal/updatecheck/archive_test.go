package updatecheck

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type entry struct {
	name string
	body string
	mode int64
	kind byte
}

func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: e.kind, Size: int64(len(e.body))}
		if e.kind == tar.TypeSymlink {
			h.Linkname, h.Size = "elsewhere", 0
		}
		if e.kind == tar.TypeDir {
			h.Size = 0
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			w.Write([]byte(e.body))
		}
	}
	w.Close()
	return b.Bytes()
}

func TestProgramArchivesWithUnsafeEntriesAreRefused(t *testing.T) {
	ok := entry{name: "vult", body: "x", mode: 0o755, kind: tar.TypeReg}
	for name, entries := range map[string][]entry{
		"absolute path":      {{name: "/vult", body: "x", mode: 0o755, kind: tar.TypeReg}},
		"parent path":        {{name: "../vult", body: "x", mode: 0o755, kind: tar.TypeReg}},
		"hidden parent":      {{name: "bin/../../vult", body: "x", mode: 0o755, kind: tar.TypeReg}},
		"symbolic link":      {ok, {name: "link", mode: 0o777, kind: tar.TypeSymlink}},
		"set-user-id":        {{name: "vult", body: "x", mode: 0o4755, kind: tar.TypeReg}},
		"writable by others": {{name: "vult", body: "x", mode: 0o757, kind: tar.TypeReg}},
		"named twice":        {ok, ok},
		"device":             {ok, {name: "dev", mode: 0o644, kind: tar.TypeChar}},
	} {
		file := filepath.Join(t.TempDir(), "p.tar")
		os.WriteFile(file, archive(t, entries...), 0o600)
		if err := ValidateArchive(file); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, data := range map[string][]byte{"empty": archive(t), "not an archive": []byte("plain text, not a tar archive at all")} {
		file := filepath.Join(t.TempDir(), "p.tar")
		os.WriteFile(file, data, 0o600)
		if err := ValidateArchive(file); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	file := filepath.Join(t.TempDir(), "p.tar")
	os.WriteFile(file, archive(t, ok), 0o600)
	if err := ValidateArchive(file); err != nil {
		t.Fatalf("a plain program archive was refused: %v", err)
	}

}

func TestVersionFloorsCompareNumerically(t *testing.T) {
	for _, tc := range []struct {
		have, need string
		want       bool
	}{{"26.1", "26.0", true}, {"26", "26.0", true}, {"26.0", "26.1", false}, {"27.0", "26.9", true}, {"x", "26.0", false}} {
		if got := atLeast(tc.have, tc.need); got != tc.want {
			t.Errorf("atLeast(%s, %s) = %v", tc.have, tc.need, got)
		}
	}
}
