// Package pdftest writes small synthetic PDFs for tests: each page's lines
// drawn as text, compressed, with a font whose ToUnicode map carries any
// Unicode text, the way statement generators write them.
package pdftest

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
)

// Options shape the file.
type Options struct {
	// Plain draws text as Latin-1 bytes with no ToUnicode map.
	Plain bool
	// Packed puts the fonts and pages in a compressed object stream.
	Packed bool
}

// PDF returns a PDF whose pages hold these lines.
func PDF(pages [][]string, o Options) []byte {
	// Glyph codes: one two-byte code per distinct rune.
	codes := map[rune]int{}
	var runes []rune
	for _, page := range pages {
		for _, line := range page {
			for _, r := range line {
				if _, ok := codes[r]; !ok {
					codes[r] = len(runes) + 1
					runes = append(runes, r)
				}
			}
		}
	}
	type obj struct {
		body   string
		stream []byte
	}
	var objs []obj
	add := func(body string, data []byte) int {
		objs = append(objs, obj{body, data})
		return len(objs)
	}
	deflate := func(data []byte) []byte {
		var b bytes.Buffer
		w := zlib.NewWriter(&b)
		w.Write(data)
		w.Close()
		return b.Bytes()
	}
	var fontRef int
	if o.Plain {
		fontRef = add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>", nil)
	} else {
		var cmap strings.Builder
		cmap.WriteString("/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n1 begincodespacerange <0000> <FFFF> endcodespacerange\n")
		fmt.Fprintf(&cmap, "%d beginbfchar\n", len(runes))
		for _, r := range runes {
			var units strings.Builder
			for _, u := range utf16(r) {
				fmt.Fprintf(&units, "%04X", u)
			}
			fmt.Fprintf(&cmap, "<%04X> <%s>\n", codes[r], units.String())
		}
		cmap.WriteString("endbfchar\nendcmap CMapName currentdict /CMap defineresource pop end end\n")
		toUnicode := add("<< /Filter /FlateDecode", deflate([]byte(cmap.String())))
		fontRef = add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /Example /Encoding /Identity-H /ToUnicode %d 0 R >>", toUnicode), nil)
	}
	pagesRef := len(objs) + 2*len(pages) + 1
	var kids []string
	for _, page := range pages {
		var content strings.Builder
		content.WriteString("BT /F1 10 Tf 40 760 Td 14 TL\n")
		for i, line := range page {
			if i > 0 {
				content.WriteString("T*\n")
			}
			if o.Plain {
				var b []byte
				for _, r := range line {
					if r < 256 {
						b = append(b, byte(r))
					}
				}
				fmt.Fprintf(&content, "(%s) Tj\n", escape(b))
			} else {
				var hex strings.Builder
				for _, r := range line {
					fmt.Fprintf(&hex, "%04X", codes[r])
				}
				fmt.Fprintf(&content, "<%s> Tj\n", hex.String())
			}
		}
		content.WriteString("ET\n")
		c := add("<< /Filter /FlateDecode", deflate([]byte(content.String())))
		p := add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] /Contents %d 0 R >>", pagesRef, c), nil)
		kids = append(kids, fmt.Sprintf("%d 0 R", p))
	}
	pagesBody := fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d /Resources << /Font << /F1 %d 0 R >> >> >>", strings.Join(kids, " "), len(pages), fontRef)
	if add(pagesBody, nil) != pagesRef {
		panic("pdftest: object numbering")
	}
	catalog := add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesRef), nil)

	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	write := func(n int, o obj) {
		if o.stream == nil {
			fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", n, o.body)
			return
		}
		fmt.Fprintf(&out, "%d 0 obj\n%s /Length %d >>\nstream\n", n, o.body, len(o.stream))
		out.Write(o.stream)
		out.WriteString("\nendstream\nendobj\n")
	}
	if !o.Packed {
		for i, o := range objs {
			write(i+1, o)
		}
	} else {
		// Plain dictionaries go in one object stream; streams stay outside.
		var head, body strings.Builder
		n := 0
		for i, o := range objs {
			if o.stream != nil {
				write(i+1, o)
				continue
			}
			fmt.Fprintf(&head, "%d %d ", i+1, body.Len())
			body.WriteString(o.body + "\n")
			n++
		}
		data := head.String() + body.String()
		write(len(objs)+1, obj{fmt.Sprintf("<< /Type /ObjStm /N %d /First %d /Filter /FlateDecode", n, head.Len()), deflate([]byte(data))})
	}
	fmt.Fprintf(&out, "trailer\n<< /Root %d 0 R >>\n%%%%EOF\n", catalog)
	return out.Bytes()
}

func utf16(r rune) []uint16 {
	if r < 0x10000 {
		return []uint16{uint16(r)}
	}
	r -= 0x10000
	return []uint16{uint16(0xD800 + (r >> 10)), uint16(0xDC00 + (r & 0x3FF))}
}

func escape(b []byte) string {
	var s strings.Builder
	for _, c := range b {
		switch c {
		case '(', ')', '\\':
			s.WriteByte('\\')
			s.WriteByte(c)
		default:
			s.WriteByte(c)
		}
	}
	return s.String()
}
