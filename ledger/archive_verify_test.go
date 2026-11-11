package ledger_test

import (
	"archive/tar"
	"bytes"
	"io"
	"testing"

	"github.com/vult-ai/vult/internal/testutil"
	"github.com/vult-ai/vult/ledger"
)

func verifiedArchive(t *testing.T) (*testutil.Database, []byte) {
	t.Helper()
	db, scope, _ := documentFixture(t)
	a := readyOriginal(t, db, scope, "statement")
	b := readyOriginal(t, db, scope, "receipt")
	postCitedJournal(t, db, scope, "opening", []ledger.SourceInput{{DocumentID: a.ID}})
	postCitedJournal(t, db, scope, "second", []ledger.SourceInput{{DocumentID: b.ID}})
	var archive bytes.Buffer
	if err := db.Owner.WriteArchive(db.Context, db.Store.Documents, &archive, ""); err != nil {
		t.Fatal(err)
	}
	return db, archive.Bytes()
}

// An archive that reads back completely has as many of each fact as the
// database, and restoring it reproduces every fact exactly.
func TestVerifyArchiveMatchesTheDatabaseItCameFrom(t *testing.T) {
	db, archive := verifiedArchive(t)
	summary, err := ledger.VerifyArchive(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	facts, err := ledger.Fingerprint(db.Context, db.Admin)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Originals != 2 || facts.Counts["entries"] != 2 {
		t.Fatalf("summary %+v, facts %v", summary, facts.Counts)
	}
	for table, n := range facts.Counts {
		if summary.Rows[table] != n {
			t.Errorf("%s: archive %d, database %d", table, summary.Rows[table], n)
		}
	}
	target := testutil.New(t)
	vault, _ := restoreVault(t)
	if err := target.Owner.RestoreArchive(target.Context, vault, bytes.NewReader(archive), nil); err != nil {
		t.Fatal(err)
	}
	restored, err := ledger.Fingerprint(target.Context, target.Admin)
	if err != nil || restored.SHA256 != facts.SHA256 {
		t.Fatalf("restored facts differ: %v", err)
	}
}

// rewrite copies an archive's members, letting change alter or drop each.
func rewrite(t *testing.T, archive []byte, change func(h *tar.Header, body []byte) []byte) []byte {
	t.Helper()
	r := tar.NewReader(bytes.NewReader(archive))
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if body = change(h, body); body == nil {
			continue
		}
		h.Size = int64(len(body))
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		w.Write(body)
	}
	w.Close()
	return out.Bytes()
}

func TestVerifyArchiveRefusesDamage(t *testing.T) {
	_, archive := verifiedArchive(t)
	if _, err := ledger.VerifyArchive(bytes.NewReader(rewrite(t, archive, func(_ *tar.Header, b []byte) []byte { return b }))); err != nil {
		t.Fatal("an unchanged copy was refused", err)
	}
	for name, change := range map[string]func(*tar.Header, []byte) []byte{
		"changed original": func(h *tar.Header, b []byte) []byte {
			if h.Name[:10] == "originals/" {
				b = append([]byte{}, b...)
				b[0] ^= 1
			}
			return b
		},
		"missing original": func(h *tar.Header, b []byte) []byte {
			if h.Name[:min(10, len(h.Name))] == "originals/" {
				return nil
			}
			return b
		},
		"no completion footer": func(h *tar.Header, b []byte) []byte {
			if h.Name == "complete.json" {
				return nil
			}
			return b
		},
		"footer for another manifest": func(h *tar.Header, b []byte) []byte {
			if h.Name == "complete.json" {
				// Change one digit of the digest, keeping the JSON valid.
				b = append([]byte{}, b...)
				i := bytes.Index(b, []byte(`"manifest_sha256":"`)) + len(`"manifest_sha256":"`)
				if b[i] == '0' {
					b[i] = '1'
				} else {
					b[i] = '0'
				}
				return b
			}
			return b
		},
	} {
		if _, err := ledger.VerifyArchive(bytes.NewReader(rewrite(t, archive, change))); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	extra := append(append([]byte{}, archive[:len(archive)-1024]...), rewrite(t, archive, func(h *tar.Header, b []byte) []byte {
		if h.Name == "complete.json" {
			return b
		}
		return nil
	})...)
	if _, err := ledger.VerifyArchive(bytes.NewReader(extra)); err == nil {
		t.Error("a member after the footer was accepted")
	}
	if _, err := ledger.VerifyArchive(bytes.NewReader(archive[:len(archive)/2])); err == nil {
		t.Error("a truncated archive was accepted")
	}
}
