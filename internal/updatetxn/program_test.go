//go:build darwin || linux

package updatetxn

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type entry struct {
	name, body string
	mode       int64
	kind       byte
}

func writeTar(t *testing.T, entries ...entry) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		kind := e.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		h := &tar.Header{Name: e.name, Mode: e.mode, Typeflag: kind, Size: int64(len(e.body))}
		if kind != tar.TypeReg {
			h.Size = 0
		}
		if kind == tar.TypeSymlink {
			h.Linkname = "/etc"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	file := filepath.Join(t.TempDir(), "release.tar")
	if err := os.WriteFile(file, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func read(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func oldProgram(t *testing.T) ProgramDir {
	t.Helper()
	live := filepath.Join(t.TempDir(), "ai.example.program")
	if err := os.Mkdir(live, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(live, "vult"), []byte("old"), 0o755)
	return ProgramDir{Live: live}
}

func TestInstallAndUninstallAProgram(t *testing.T) {
	d := oldProgram(t)
	archive := writeTar(t,
		entry{name: "bin/", mode: 0o755, kind: tar.TypeDir},
		entry{name: "bin/vult", body: "new", mode: 0o755},
		entry{name: "share/notice.txt", body: "n", mode: 0o644},
	)
	for i := 0; i < 2; i++ { // the second call finds it done
		if err := d.Install(archive, "bb"); err != nil {
			t.Fatal(err)
		}
	}
	if read(t, filepath.Join(d.Live, "bin/vult")) != "new" || read(t, filepath.Join(d.previous(), "vult")) != "old" {
		t.Fatal("not swapped")
	}
	if sha, _ := Release(d.Live); sha != "bb" {
		t.Fatalf("marker %q", sha)
	}
	if ok, _ := exists(d.next()); ok {
		t.Fatal(".next left behind")
	}
	for i := 0; i < 2; i++ {
		if err := d.Uninstall("bb"); err != nil {
			t.Fatal(err)
		}
	}
	if read(t, filepath.Join(d.Live, "vult")) != "old" {
		t.Fatal("not swapped back")
	}
}

// A crash between the swap and moving the old program aside: both Install
// and Uninstall finish the job.
func TestProgramSwapInterrupted(t *testing.T) {
	archive := writeTar(t, entry{name: "vult", body: "new", mode: 0o755})
	for _, then := range []string{"install", "uninstall"} {
		d := oldProgram(t)
		if err := ExtractArchive(archive, d.next()); err != nil {
			t.Fatal(err)
		}
		WriteDurably(filepath.Join(d.next(), ReleaseMarker), []byte("bb\n"), 0o644)
		if err := SwapDirs(d.next(), d.Live); err != nil {
			t.Fatal(err)
		}
		if then == "install" {
			if err := d.Install(archive, "bb"); err != nil {
				t.Fatal(err)
			}
			if read(t, filepath.Join(d.Live, "vult")) != "new" || read(t, filepath.Join(d.previous(), "vult")) != "old" {
				t.Fatal("install not finished")
			}
		} else {
			if err := d.Uninstall("bb"); err != nil {
				t.Fatal(err)
			}
			if read(t, filepath.Join(d.Live, "vult")) != "old" {
				t.Fatal("uninstall did not find the old program")
			}
		}
	}
}

// A half-extracted .next is replaced, not used.
func TestInstallReplacesAPartialExtraction(t *testing.T) {
	d := oldProgram(t)
	os.Mkdir(d.next(), 0o755)
	os.WriteFile(filepath.Join(d.next(), "vult"), []byte("half"), 0o755)
	archive := writeTar(t, entry{name: "vult", body: "new", mode: 0o755})
	if err := d.Install(archive, "bb"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(d.Live, "vult")) != "new" {
		t.Fatal("used the partial extraction")
	}
}

func TestUninstallWithoutAPreviousProgram(t *testing.T) {
	d := oldProgram(t)
	WriteDurably(filepath.Join(d.Live, ReleaseMarker), []byte("bb\n"), 0o644)
	if err := d.Uninstall("bb"); err == nil {
		t.Fatal("no error")
	}
	if err := d.Uninstall("cc"); err != nil {
		t.Fatalf("a release that isn't live: %v", err)
	}
}

func TestExtractArchiveRefusesUnsafeEntries(t *testing.T) {
	for name, e := range map[string]entry{
		"symlink":     {name: "lib", kind: tar.TypeSymlink},
		"parent path": {name: "../escape", body: "x", mode: 0o644},
		"absolute":    {name: "/etc/x", body: "x", mode: 0o644},
		"set-uid":     {name: "vult", body: "x", mode: 0o4755},
		"world write": {name: "vult", body: "x", mode: 0o666},
		"marker":      {name: ReleaseMarker, body: "cc", mode: 0o644},
	} {
		dest := filepath.Join(t.TempDir(), "out")
		if err := ExtractArchive(writeTar(t, e), dest); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	dest := filepath.Join(t.TempDir(), "exists")
	os.Mkdir(dest, 0o755)
	if err := ExtractArchive(writeTar(t, entry{name: "vult", body: "x", mode: 0o755}), dest); err == nil {
		t.Error("extracted into an existing directory")
	}
}

func TestExtractArchiveSetsModes(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dest := filepath.Join(t.TempDir(), "out")
	archive := writeTar(t,
		entry{name: "vult", body: "x", mode: 0o755},
		entry{name: "notes/readme", body: "y", mode: 0o644},
	)
	if err := ExtractArchive(archive, dest); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]os.FileMode{"vult": 0o755, "notes/readme": 0o644, "notes": 0o755} {
		info, err := os.Stat(filepath.Join(dest, file))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v", file, info.Mode().Perm(), want)
		}
	}
}

func TestSwapDirs(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	os.Mkdir(a, 0o755)
	os.Mkdir(b, 0o755)
	os.WriteFile(filepath.Join(a, "f"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(b, "f"), []byte("b"), 0o644)
	if err := SwapDirs(a, b); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(a, "f")) != "b" || read(t, filepath.Join(b, "f")) != "a" {
		t.Fatal("not swapped")
	}
	if err := SwapDirs(a, filepath.Join(base, "missing")); err == nil || !strings.Contains(err.Error(), "swap") {
		t.Fatalf("swap with a missing directory: %v", err)
	}
}

func gzipped(t *testing.T, entries ...entry) string {
	t.Helper()
	plain, err := os.ReadFile(writeTar(t, entries...))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(plain)
	zw.Close()
	file := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := os.WriteFile(file, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// The Ubuntu release archive carries the program under opt/vult/ beside
// system files; only the program is unpacked, but every member is checked.
func TestExtractProgramTakesOnlyThePrefix(t *testing.T) {
	archive := gzipped(t,
		entry{name: "opt/vult/vult", body: "new", mode: 0o755},
		entry{name: "opt/vult/postgres/bin/postgres", body: "db", mode: 0o755},
		entry{name: "etc/systemd/system/ai.example.ledger.service", body: "unit", mode: 0o644},
	)
	dest := filepath.Join(t.TempDir(), "out")
	if err := ExtractProgram(archive, dest, "opt/vult/"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dest, "vult")) != "new" || read(t, filepath.Join(dest, "postgres/bin/postgres")) != "db" {
		t.Fatal("program not unpacked")
	}
	if _, err := os.Stat(filepath.Join(dest, "etc")); err == nil {
		t.Fatal("a system file was unpacked into the program")
	}
	for name, bad := range map[string]string{
		"link outside the program": gzipped(t, entry{name: "opt/vult/vult", body: "x", mode: 0o755}, entry{name: "etc/x", kind: tar.TypeSymlink}),
		"set-uid outside":          gzipped(t, entry{name: "opt/vult/vult", body: "x", mode: 0o755}, entry{name: "etc/x", body: "x", mode: 0o4755}),
		"no program":               gzipped(t, entry{name: "etc/x", body: "x", mode: 0o644}),
		"named twice":              gzipped(t, entry{name: "opt/vult/vult", body: "x", mode: 0o755}, entry{name: "opt/vult/vult", body: "y", mode: 0o755}),
	} {
		if err := ExtractProgram(bad, filepath.Join(t.TempDir(), "out"), "opt/vult/"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := ExtractProgram(archive, filepath.Join(t.TempDir(), "out"), "/opt/vult/"); err == nil {
		t.Error("an absolute prefix was accepted")
	}
}

func TestInstallWithAPrefix(t *testing.T) {
	d := oldProgram(t)
	d.Prefix = "opt/vult/"
	archive := gzipped(t, entry{name: "opt/vult/vult", body: "new", mode: 0o755}, entry{name: "usr/share/x", body: "x", mode: 0o644})
	if err := d.Install(archive, "bb"); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(d.Live, "vult")) != "new" || read(t, filepath.Join(d.previous(), "vult")) != "old" {
		t.Fatal("not installed")
	}
}
