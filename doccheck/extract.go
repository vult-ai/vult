// Package doccheck is Vult's document checker. When the owner adds a file,
// it reads it through a reader chain (a bank's export, the PDF's text, local
// OCR, then an assistant's proposals Vult verifies, then the owner),
// compares whose account it is the way a person would, and runs the number
// checks in plain code. Each check is recorded as it starts and as it ends,
// and none is left pending: what can't be decided goes to a person, with
// why.
package doccheck

import (
	"context"
	"fmt"
	"unicode"
)

// Assistant is an AI the checker may ask for help. This package has none
// of its own; a program that has one passes it in.
type Assistant interface {
	// Read proposes a reading of a document: from its pages' text when it
	// is given some, otherwise from the file. call names the request.
	Read(ctx context.Context, pages []string, mediaType string, data []byte) (reading Reading, call string, err error)
	// Same says whether a name or address printed on a statement is the
	// one the Book has, with one plain sentence of why.
	Same(ctx context.Context, kind, found, expected string) (same bool, reason string, err error)
	// Mark tells the assistant whether a reading it proposed was taken.
	Mark(ctx context.Context, call string, accepted bool)
}

// Enough says whether a text layer has enough text to read a statement from.
func Enough(pages []string) bool {
	n := 0
	for _, p := range pages {
		for _, r := range p {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				n++
			}
		}
	}
	return n >= 40
}

// assistantRead asks the assistant to read a document: from its text when
// there is some, otherwise by showing it the file. Its reading is a
// proposal; the caller verifies it against the document.
func assistantRead(ctx context.Context, a Assistant, pages []string, mediaType string, data []byte) (Reading, string, error) {
	if Enough(pages) {
		return a.Read(ctx, pages, mediaType, nil)
	}
	switch mediaType {
	case "application/pdf", "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return Reading{}, "", ErrNotReadable
	}
	return a.Read(ctx, nil, mediaType, data)
}

// ErrNotReadable means the file is a kind Vult can't read.
var ErrNotReadable = fmt.Errorf("doccheck: not a PDF or image")
