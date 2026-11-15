// Package pdfwrite writes simple text documents as PDF: lines of text in
// the standard Helvetica fonts, wrapped to the page and spread over as many
// US Letter pages as they need. The output depends only on the text, so
// the same document always has the same bytes.
package pdfwrite

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	pageWidth  = 612.0
	pageHeight = 792.0
	margin     = 54.0
)

type line struct {
	size float64
	bold bool
	text string
}

// Document is a PDF being written.
type Document struct {
	title string
	lines []line
}

// New starts a document with the title its metadata gives.
func New(title string) *Document { return &Document{title: title} }

// Text adds text in the given size, wrapped to the page width.
func (d *Document) Text(size float64, bold bool, text string) {
	for _, paragraph := range strings.Split(text, "\n") {
		for _, l := range wrap(paragraph, size, bold) {
			d.lines = append(d.lines, line{size, bold, l})
		}
	}
}

// Gap adds an empty line of the given size.
func (d *Document) Gap(size float64) { d.lines = append(d.lines, line{size: size}) }

// widthOf approximates a string's width: Helvetica's average advance,
// slightly wider for bold.
func widthOf(s string, size float64, bold bool) float64 {
	per := 0.52
	if bold {
		per = 0.56
	}
	return float64(utf8.RuneCountInString(s)) * size * per
}

func wrap(text string, size float64, bold bool) []string {
	limit := pageWidth - 2*margin
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	current := ""
	for _, w := range words {
		next := w
		if current != "" {
			next = current + " " + w
		}
		if current != "" && widthOf(next, size, bold) > limit {
			out = append(out, current)
			current = w
			continue
		}
		current = next
	}
	return append(out, current)
}

// winAnsi encodes text for the standard fonts; characters they lack
// become "?".
func winAnsi(s string) string {
	special := map[rune]byte{'€': 0x80, '‚': 0x82, '„': 0x84, '…': 0x85, '‘': 0x91, '’': 0x92, '“': 0x93, '”': 0x94, '•': 0x95, '–': 0x96, '—': 0x97, '™': 0x99}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r >= 0x20 && r < 0x7f:
			b.WriteRune(r)
		case r >= 0xa0 && r <= 0xff:
			fmt.Fprintf(&b, "\\%03o", r)
		default:
			if c, ok := special[r]; ok {
				fmt.Fprintf(&b, "\\%03o", c)
			} else {
				b.WriteByte('?')
			}
		}
	}
	return b.String()
}

// Bytes is the finished PDF.
func (d *Document) Bytes() []byte {
	// Lay the lines out on pages.
	var pages []string
	var content strings.Builder
	y := pageHeight - margin
	flush := func() {
		pages = append(pages, content.String())
		content.Reset()
		y = pageHeight - margin
	}
	for _, l := range d.lines {
		lead := l.size * 1.35
		if y-lead < margin {
			flush()
		}
		y -= lead
		if l.text == "" {
			continue
		}
		font := "F1"
		if l.bold {
			font = "F2"
		}
		fmt.Fprintf(&content, "BT /%s %.1f Tf %.1f %.1f Td (%s) Tj ET\n", font, l.size, margin, y, winAnsi(l.text))
	}
	if content.Len() > 0 || len(pages) == 0 {
		flush()
	}
	var out bytes.Buffer
	var offsets []int
	object := func(body string) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	// 1 catalog, 2 pages, 3 and 4 fonts, 5 info, then a page and its
	// content for each page.
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 6+2*i)
	}
	object("<< /Type /Catalog /Pages 2 0 R >>")
	object(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages)))
	object("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	object("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	object(fmt.Sprintf("<< /Title (%s) /Producer (Vult) >>", winAnsi(d.title)))
	for i, p := range pages {
		object(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>", pageWidth, pageHeight, 7+2*i))
		object(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(p), p))
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info 5 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes()
}
