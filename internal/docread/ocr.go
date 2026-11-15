package docread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// OCR reads the text of a scan, page by page, on this machine. Nothing
// leaves it.
type OCR interface {
	Pages(ctx context.Context, mediaType string, data []byte) ([]string, error)
}

// ErrNoOCR means no local text recognition is installed.
var ErrNoOCR = errors.New("docread: no local text recognition")

const ocrTime = 2 * time.Minute

// LocalOCR finds this machine's text recognition: Apple's Vision through
// Vult's helper on a Mac, else Tesseract when it's installed. It returns
// nil when there's none.
func LocalOCR() OCR {
	if runtime.GOOS == "darwin" {
		if program, err := os.Executable(); err == nil {
			helper := filepath.Join(filepath.Dir(program), "vult-ocr")
			if _, err := os.Stat(helper); err == nil {
				return Vision{Helper: helper}
			}
		}
	}
	if path, err := exec.LookPath("tesseract"); err == nil {
		return Tesseract{Program: path}
	}
	return nil
}

// Vision runs Vult's macOS helper, which uses Apple's Vision text
// recognition: the file on standard input, the pages as a JSON array of
// strings on standard output.
type Vision struct{ Helper string }

func (v Vision) Pages(ctx context.Context, mediaType string, data []byte) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, ocrTime)
	defer cancel()
	cmd := exec.CommandContext(ctx, v.Helper, "read", mediaType)
	cmd.Stdin = bytes.NewReader(data)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var pages []string
	if err := json.Unmarshal(out.Bytes(), &pages); err != nil {
		return nil, err
	}
	return pages, nil
}

// Tesseract runs the Tesseract command on an image, in the label
// languages it has installed. It reads images, not PDFs.
type Tesseract struct{ Program string }

var tesseractLanguages = []string{"eng", "pol", "deu", "fra", "spa"}

func (t Tesseract) Pages(ctx context.Context, mediaType string, data []byte) ([]string, error) {
	if !strings.HasPrefix(mediaType, "image/") {
		return nil, ErrNoOCR
	}
	ctx, cancel := context.WithTimeout(ctx, ocrTime)
	defer cancel()
	languages := []string{}
	if out, err := exec.CommandContext(ctx, t.Program, "--list-langs").Output(); err == nil {
		have := map[string]bool{}
		for _, l := range strings.Fields(string(out)) {
			have[l] = true
		}
		for _, l := range tesseractLanguages {
			if have[l] {
				languages = append(languages, l)
			}
		}
	}
	args := []string{"stdin", "stdout"}
	if len(languages) > 0 {
		args = append(args, "-l", strings.Join(languages, "+"))
	}
	args = append(args, "--psm", "6")
	cmd := exec.CommandContext(ctx, t.Program, args...)
	cmd.Stdin = bytes.NewReader(data)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return []string{out.String()}, nil
}
