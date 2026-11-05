package server

import (
	"errors"
	"io"
	"path/filepath"

	"github.com/vult-ai/vult/documents"
)

type DocumentStorage struct {
	Directory    string `json:"directory"`
	KeyFile      string `json:"key_file,omitempty"`
	KeyDirectory string `json:"key_directory,omitempty"`
}

// OpenDocuments opens explicitly configured private storage and keys. It never
// provisions new keys or initializes data. Alternative storage/key providers
// can supply Options.Documents directly without this disk configuration.
func OpenDocuments(c *DocumentStorage) (*documents.Vault, io.Closer, error) {
	if c == nil {
		return nil, nil, nil
	}
	if !filepath.IsAbs(c.Directory) || (c.KeyFile == "") == (c.KeyDirectory == "") {
		return nil, nil, ErrConfiguration
	}
	if c.KeyFile != "" && !filepath.IsAbs(c.KeyFile) {
		return nil, nil, ErrConfiguration
	}
	if c.KeyDirectory != "" && (!filepath.IsAbs(c.KeyDirectory) || filepath.Clean(c.KeyDirectory) == filepath.Clean(c.Directory)) {
		return nil, nil, ErrConfiguration
	}
	var keys documents.Keys
	var keyCloser io.Closer
	var err error
	if c.KeyFile != "" {
		keys, err = documents.OpenKeyring(c.KeyFile)
	} else {
		var directory *documents.KeyDirectory
		directory, err = documents.OpenKeyDirectory(c.KeyDirectory)
		keys, keyCloser = directory, directory
	}
	if err != nil {
		return nil, nil, ErrConfiguration
	}
	disk, err := documents.OpenDisk(c.Directory)
	if err != nil {
		if keyCloser != nil {
			keyCloser.Close()
		}
		return nil, nil, ErrConfiguration
	}
	return &documents.Vault{Storage: disk, Keys: keys}, documentClosers{disk, keyCloser}, nil
}

type documentClosers []io.Closer

func (c documentClosers) Close() error {
	var err error
	for _, closer := range c {
		if closer != nil {
			err = errors.Join(err, closer.Close())
		}
	}
	return err
}
