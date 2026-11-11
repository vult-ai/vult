package ledger

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/vult-ai/vult/internal/strictjson"
)

// ArchiveSummary describes a portable archive that read back completely.
type ArchiveSummary struct {
	Format    string           `json:"format"`
	Rows      map[string]int64 `json:"rows"`      // rows per table in the manifest
	Originals int              `json:"originals"` // original documents, each hash-checked
}

// VerifyArchive reads a whole plaintext portable archive without restoring
// it: the manifest must be one this release can restore, every original
// must match its recorded size and SHA-256, and the completion footer must
// name the manifest. It returns the row counts so a caller can compare them
// with the database the archive came from.
func VerifyArchive(source io.Reader) (ArchiveSummary, error) {
	bounded := &io.LimitedReader{R: source, N: MaxArchiveSize + 1}
	reader := tar.NewReader(bounded)
	manifest, err := readArchiveManifest(reader)
	if err != nil {
		return ArchiveSummary{}, err
	}
	summary := ArchiveSummary{Format: manifest.Format, Rows: map[string]int64{}}
	for table, rows := range manifest.Snapshot.Tables {
		var list []json.RawMessage
		if err := json.Unmarshal(rows, &list); err != nil {
			return ArchiveSummary{}, ErrInvalid
		}
		summary.Rows[table] = int64(len(list))
	}
	for _, object := range manifest.Originals {
		header, err := reader.Next()
		if err != nil || !archiveHeader(header, "originals/"+object.DocumentID) || header.Size != object.Size {
			return ArchiveSummary{}, ErrInvalid
		}
		h := sha256.New()
		if n, err := io.Copy(h, reader); err != nil || n != object.Size || hex.EncodeToString(h.Sum(nil)) != object.SHA256 {
			return ArchiveSummary{}, ErrInvalid
		}
		summary.Originals++
	}
	header, err := reader.Next()
	if err != nil || !archiveHeader(header, "complete.json") || header.Size > 4096 {
		return ArchiveSummary{}, ErrInvalid
	}
	data, err := io.ReadAll(reader)
	var footer archiveFooter
	if err != nil || strictjson.Decode(data, &footer) != nil || footer.Format != "vult-archive-complete-v1" || footer.ManifestSHA256 != manifest.digest {
		return ArchiveSummary{}, ErrInvalid
	}
	if _, err := reader.Next(); err != io.EOF {
		return ArchiveSummary{}, ErrInvalid
	}
	if bounded.N <= 0 {
		return ArchiveSummary{}, ErrLimit
	}
	return summary, nil
}
