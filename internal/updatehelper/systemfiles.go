//go:build darwin || linux

package updatehelper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
)

// ErrNeedsInstaller means a release also changes system files (on Ubuntu,
// the systemd units and the permissions policy), which only the full
// installer changes. The Update button never touches them.
var ErrNeedsInstaller = errors.New("this update changes system files; install it with the full installer")

type programInventory struct {
	Version       string            `json:"version"`
	ExternalFiles map[string]string `json:"external_files"`
}

// SameSystemFiles checks that the release archive's program inventory names
// the same system files, with the same contents, as the installed one.
func SameSystemFiles(l Layout) func(artifact string) error {
	return func(artifact string) error {
		installed, err := os.ReadFile(filepath.Join(l.Program, "program-files.json"))
		if err != nil {
			return errors.New("the installed program inventory is not readable")
		}
		var have programInventory
		if json.Unmarshal(installed, &have) != nil || len(have.ExternalFiles) == 0 {
			return errors.New("the installed program inventory is not readable")
		}
		data, err := memberOf(artifact, l.ArchivePrefix+"program-files.json")
		if err != nil {
			return err
		}
		var want programInventory
		if json.Unmarshal(data, &want) != nil || len(want.ExternalFiles) == 0 {
			return errors.New("the release has no readable program inventory")
		}
		if !maps.Equal(have.ExternalFiles, want.ExternalFiles) {
			return ErrNeedsInstaller
		}
		return nil
	}
}

// memberOf reads one member of a gzipped tar.
func memberOf(archive, name string) ([]byte, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, errors.New("the release archive is damaged")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("the release has no program inventory")
		}
		if err != nil {
			return nil, errors.New("the release archive is damaged")
		}
		if h.Name == name && h.Typeflag == tar.TypeReg && h.Size <= 1<<20 {
			var buf bytes.Buffer
			if _, err := io.Copy(&buf, io.LimitReader(tr, 1<<20)); err != nil {
				return nil, err
			}
			return buf.Bytes(), nil
		}
	}
}
