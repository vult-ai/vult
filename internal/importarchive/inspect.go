// Package importarchive validates data-only source packages without accepting
// accounting records or executing their contents.
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
	"strings"

	"github.com/vult-ai/vult/internal/strictjson"
)

const MaxArchiveSize int64 = 128 << 20
const maxExpanded int64 = 256 << 20
const maxSource int64 = 16 << 20
const maxBlob int64 = 32 << 20
const maxFiles = 4096

var (
	ErrFormat    = errors.New("unsupported import archive; provide a reconciled export package with source.json and hash-addressed blobs")
	ErrIntegrity = errors.New("import archive failed integrity checks; retain the original and obtain a verified copy")
	ErrLimit     = errors.New("import archive exceeds inspection limits; provide a smaller supported package")
)

type Report struct {
	Format           string `json:"format"`
	ArchiveSHA256    string `json:"archive_sha256"`
	SourceSHA256     string `json:"source_sha256"`
	ArchiveBytes     int64  `json:"archive_bytes"`
	ExpandedBytes    int64  `json:"expanded_bytes"`
	VerifiedBlobs    int    `json:"verified_blobs"`
	AccountingStatus string `json:"accounting_status"`
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// Inspect reads a bounded immutable snapshot of the input. It checks container,
// JSON and byte integrity only: balanced or complete accounting is not implied.
func Inspect(ctx context.Context, input io.Reader) (Report, error) {
	if input == nil {
		return Report{}, ErrFormat
	}
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, input}, MaxArchiveSize+1))
	if err != nil {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		return Report{}, ErrIntegrity
	}
	if int64(len(data)) > MaxArchiveSize {
		return Report{}, ErrLimit
	}
	if err := checkDirectory(data); err != nil {
		return Report{}, err
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Report{}, ErrFormat
	}
	archiveHash := sha256.Sum256(data)
	report := Report{Format: "vult-source-inspection-v1", ArchiveSHA256: hex.EncodeToString(archiveHash[:]), ArchiveBytes: int64(len(data)), AccountingStatus: "not_checked"}
	wanted, actual := map[string]bool{}, map[string]bool{}
	for _, member := range reader.File {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if !member.Mode().IsRegular() || member.Mode().Perm()&0111 != 0 || member.Flags&1 != 0 || member.Method != zip.Store && member.Method != zip.Deflate {
			return Report{}, ErrFormat
		}
		limit := maxBlob
		if member.Name == "source.json" {
			limit = maxSource
		}
		if member.UncompressedSize64 > uint64(limit) || member.UncompressedSize64 > uint64(maxExpanded-report.ExpandedBytes) {
			return Report{}, ErrLimit
		}
		r, err := member.Open()
		if err != nil {
			return Report{}, ErrIntegrity
		}
		hash := sha256.New()
		var source bytes.Buffer
		writer := io.Writer(hash)
		if member.Name == "source.json" {
			writer = io.MultiWriter(hash, &source)
		}
		n, err := io.Copy(writer, io.LimitReader(contextReader{ctx, r}, limit+1))
		closeErr := r.Close()
		if err != nil {
			if ctx.Err() != nil {
				return Report{}, ctx.Err()
			}
			return Report{}, ErrIntegrity
		}
		if closeErr != nil || uint64(n) != member.UncompressedSize64 {
			return Report{}, ErrIntegrity
		}
		if n > limit || n > maxExpanded-report.ExpandedBytes {
			return Report{}, ErrLimit
		}
		report.ExpandedBytes += n
		digest := hex.EncodeToString(hash.Sum(nil))
		if member.Name == "source.json" {
			var fields map[string]json.RawMessage
			if strictjson.Decode(source.Bytes(), &fields) != nil {
				return Report{}, ErrFormat
			}
			var version int
			if json.Unmarshal(fields["schema"], &version) != nil || version != 1 {
				return Report{}, ErrFormat
			}
			for name, first := range map[string]byte{"book": '{', "accounts": '[', "records": '[', "artifacts": '['} {
				value := bytes.TrimSpace(fields[name])
				if len(value) == 0 || value[0] != first {
					return Report{}, ErrFormat
				}
			}
			report.SourceSHA256 = digest
			wanted, err = artifactHashes(ctx, fields["artifacts"])
			if err != nil {
				return Report{}, err
			}
		} else {
			if digest != strings.TrimPrefix(member.Name, "blobs/") {
				return Report{}, ErrIntegrity
			}
			report.VerifiedBlobs++
			actual[digest] = true
		}
	}
	if report.SourceSHA256 == "" {
		return Report{}, ErrFormat
	}
	if len(wanted) != len(actual) {
		return Report{}, ErrIntegrity
	}
	for digest := range wanted {
		if !actual[digest] {
			return Report{}, ErrIntegrity
		}
	}
	return report, nil
}

func artifactHashes(ctx context.Context, data []byte) (map[string]bool, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	if token, err := d.Token(); err != nil || token != json.Delim('[') {
		return nil, ErrFormat
	}
	out := map[string]bool{}
	count := 0
	for d.More() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count++
		if count > 16384 {
			return nil, ErrLimit
		}
		var row map[string]json.RawMessage
		if d.Decode(&row) != nil {
			return nil, ErrFormat
		}
		var digest string
		if json.Unmarshal(row["blob_sha256"], &digest) != nil {
			return nil, ErrFormat
		}
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
			return nil, ErrFormat
		}
		out[digest] = true
	}
	if token, err := d.Token(); err != nil || token != json.Delim(']') {
		return nil, ErrFormat
	}
	return out, nil
}

// Validate the central directory before zip.NewReader can allocate its list.
// This package supports bounded, single-disk, non-ZIP64 export containers.
func checkDirectory(data []byte) error {
	if len(data) < 22 || binary.LittleEndian.Uint32(data[:4]) != 0x04034b50 {
		return ErrFormat
	}
	end := -1
	for i := len(data) - 22; i >= 0 && i >= len(data)-22-65535; i-- {
		if binary.LittleEndian.Uint32(data[i:]) == 0x06054b50 && i+22+int(binary.LittleEndian.Uint16(data[i+20:])) == len(data) {
			end = i
			break
		}
	}
	if end < 0 {
		return ErrFormat
	}
	footer := data[end:]
	// A comment can contain a later footer that archive/zip selects instead.
	// Keep the preflight and decoder on the same unambiguous final footer.
	if binary.LittleEndian.Uint16(footer[20:]) != 0 {
		return ErrFormat
	}
	count := int(binary.LittleEndian.Uint16(footer[10:]))
	if count > maxFiles {
		return ErrLimit
	}
	if count == 0 || binary.LittleEndian.Uint16(footer[4:]) != 0 || binary.LittleEndian.Uint16(footer[6:]) != 0 || int(binary.LittleEndian.Uint16(footer[8:])) != count {
		return ErrFormat
	}
	start := uint64(binary.LittleEndian.Uint32(footer[16:]))
	size := uint64(binary.LittleEndian.Uint32(footer[12:]))
	if start+size != uint64(end) {
		return ErrFormat
	}
	seen := map[string]bool{}
	var expanded uint64
	position := int(start)
	for i := 0; i < count; i++ {
		if position > end-46 || binary.LittleEndian.Uint32(data[position:]) != 0x02014b50 {
			return ErrFormat
		}
		header := data[position : position+46]
		name, extra, comment := int(binary.LittleEndian.Uint16(header[28:])), int(binary.LittleEndian.Uint16(header[30:])), int(binary.LittleEndian.Uint16(header[32:]))
		if name > 70 || extra > 1024 || comment > 1024 {
			return ErrLimit
		}
		length := 46 + name + extra + comment
		if length > end-position || binary.LittleEndian.Uint16(header[34:]) != 0 {
			return ErrFormat
		}
		path := string(data[position+46 : position+46+name])
		if seen[path] {
			return ErrFormat
		}
		seen[path] = true
		if path != "source.json" {
			hash := strings.TrimPrefix(path, "blobs/")
			decoded, err := hex.DecodeString(hash)
			if !strings.HasPrefix(path, "blobs/") || len(decoded) != 32 || err != nil || hash != strings.ToLower(hash) {
				return ErrFormat
			}
		}
		if binary.LittleEndian.Uint32(header[20:]) == 0xffffffff || binary.LittleEndian.Uint32(header[24:]) == 0xffffffff || binary.LittleEndian.Uint32(header[42:]) == 0xffffffff {
			return ErrFormat
		}
		lengthLimit := uint64(maxBlob)
		if path == "source.json" {
			lengthLimit = uint64(maxSource)
		}
		uncompressed := uint64(binary.LittleEndian.Uint32(header[24:]))
		if uncompressed > lengthLimit || uncompressed > uint64(maxExpanded)-expanded {
			return ErrLimit
		}
		expanded += uncompressed
		position += length
	}
	if position != end || !seen["source.json"] {
		return ErrFormat
	}
	return nil
}
