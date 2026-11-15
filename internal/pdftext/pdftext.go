// Package pdftext reads the text layer of a PDF, page by page, in plain
// code. It reads what a statement's generator wrote: text drawn with fonts,
// mapped to Unicode through each font's ToUnicode map when there is one. It
// doesn't render, and returns no text for scans; callers read those with a
// vision model instead.
package pdftext

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

// ErrUnreadable means the file has no text layer this package can read:
// encrypted, damaged, or not a PDF.
var ErrUnreadable = errors.New("pdftext: no readable text layer")

const (
	maxObjects  = 20000
	maxInflated = 32 << 20
	maxPages    = 200
)

type ref struct{ num int }

// value is a parsed PDF object: nil, bool, float64, string (a PDF string's
// bytes), name, []value, dict, ref or *stream.
type value any
type name string
type dict map[name]value
type stream struct {
	dict dict
	raw  []byte
}

type document struct {
	objects map[int]value
	cmaps   map[int]map[string]string
}

// Pages returns the text of each page in order. A page with no text is an
// empty string.
func Pages(pdf []byte) ([]string, error) {
	if !bytes.HasPrefix(bytes.TrimLeft(pdf, "\x00\t\r\n "), []byte("%PDF-")) {
		return nil, ErrUnreadable
	}
	d := &document{objects: map[int]value{}, cmaps: map[int]map[string]string{}}
	if err := d.readObjects(pdf); err != nil {
		return nil, err
	}
	if bytes.Contains(pdf, []byte("/Encrypt")) {
		return nil, ErrUnreadable
	}
	var pages []dict
	for _, o := range d.objects {
		if t, ok := o.(dict); ok && t["Type"] == name("Pages") && t["Parent"] == nil {
			d.walk(t, &pages, 0)
			break
		}
	}
	if len(pages) == 0 {
		return nil, ErrUnreadable
	}
	out := make([]string, 0, len(pages))
	for _, p := range pages {
		out = append(out, d.pageText(p))
	}
	return out, nil
}

var objectStart = regexp.MustCompile(`(?m)(?:^|[\r\n\s])(\d+)\s+(\d+)\s+obj\b`)

func (d *document) readObjects(pdf []byte) error {
	for _, m := range objectStart.FindAllSubmatchIndex(pdf, maxObjects) {
		num, _ := strconv.Atoi(string(pdf[m[2]:m[3]]))
		p := &parser{b: pdf, i: m[1]}
		v := p.object()
		if s, ok := v.(*stream); ok {
			if t, _ := s.dict["Type"].(name); t == "ObjStm" {
				d.objectStream(s)
			}
		}
		d.objects[num] = v
	}
	if len(d.objects) == 0 {
		return ErrUnreadable
	}
	return nil
}

// objectStream adds the objects packed in a compressed object stream.
func (d *document) objectStream(s *stream) {
	data, err := d.decode(s)
	if err != nil {
		return
	}
	n, _ := s.dict["N"].(float64)
	first, _ := s.dict["First"].(float64)
	if n <= 0 || n > maxObjects || int(first) > len(data) {
		return
	}
	head := &parser{b: data[:int(first)]}
	for range int(n) {
		num, ok1 := head.object().(float64)
		off, ok2 := head.object().(float64)
		if !ok1 || !ok2 || int(first)+int(off) > len(data) {
			return
		}
		if _, exists := d.objects[int(num)]; !exists {
			p := &parser{b: data, i: int(first) + int(off)}
			d.objects[int(num)] = p.object()
		}
	}
}

func (d *document) get(v value) value {
	for range 8 {
		r, ok := v.(ref)
		if !ok {
			return v
		}
		v = d.objects[r.num]
	}
	return nil
}

func (d *document) walk(node dict, pages *[]dict, depth int) {
	if depth > 32 || len(*pages) >= maxPages {
		return
	}
	kids, _ := d.get(node["Kids"]).([]value)
	for _, k := range kids {
		kid, ok := d.get(k).(dict)
		if !ok {
			if s, isStream := d.get(k).(*stream); isStream {
				kid = s.dict
			} else {
				continue
			}
		}
		switch kid["Type"] {
		case name("Pages"):
			d.walk(kid, pages, depth+1)
		default:
			// Pages inherit resources from their parents.
			if kid["Resources"] == nil {
				kid["Resources"] = node["Resources"]
			}
			*pages = append(*pages, kid)
		}
	}
}

func (d *document) decode(s *stream) ([]byte, error) {
	filter := d.get(s.dict["Filter"])
	if list, ok := filter.([]value); ok {
		if len(list) == 0 {
			filter = nil
		} else if len(list) == 1 {
			filter = d.get(list[0])
		} else {
			return nil, ErrUnreadable
		}
	}
	switch filter {
	case nil:
		return s.raw, nil
	case name("FlateDecode"):
		r, err := zlib.NewReader(bytes.NewReader(s.raw))
		if err != nil {
			return nil, err
		}
		defer r.Close()
		out, err := io.ReadAll(io.LimitReader(r, maxInflated))
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return out, err
		}
		return out, nil
	}
	return nil, ErrUnreadable
}

// fonts maps a page's font names to their ToUnicode maps.
func (d *document) fonts(page dict) map[name]map[string]string {
	out := map[name]map[string]string{}
	resources, _ := d.get(page["Resources"]).(dict)
	fonts, _ := d.get(resources["Font"]).(dict)
	for key, f := range fonts {
		r, isRef := f.(ref)
		font, _ := d.get(f).(dict)
		if font == nil {
			continue
		}
		tu, isMap := font["ToUnicode"].(ref)
		if !isMap {
			continue
		}
		if m, ok := d.cmaps[tu.num]; ok {
			out[key] = m
			continue
		}
		s, ok := d.get(tu).(*stream)
		if !ok {
			continue
		}
		data, err := d.decode(s)
		if err != nil {
			continue
		}
		m := parseCMap(data)
		d.cmaps[tu.num] = m
		out[key] = m
		_, _ = r, isRef
	}
	return out
}

func (d *document) pageText(page dict) string {
	var content []byte
	switch c := d.get(page["Contents"]).(type) {
	case *stream:
		content, _ = d.decode(c)
	case []value:
		for _, part := range c {
			if s, ok := d.get(part).(*stream); ok {
				data, _ := d.decode(s)
				content = append(append(content, data...), '\n')
			}
		}
	}
	return runText(content, d.fonts(page))
}

var bfchar = regexp.MustCompile(`(?s)beginbfchar(.*?)endbfchar`)
var bfrange = regexp.MustCompile(`(?s)beginbfrange(.*?)endbfrange`)
var hexToken = regexp.MustCompile(`<([0-9A-Fa-f\s]*)>|\[([^\]]*)\]`)

func parseCMap(data []byte) map[string]string {
	m := map[string]string{}
	for _, block := range bfchar.FindAllSubmatch(data, -1) {
		tokens := hexes(block[1])
		for i := 0; i+1 < len(tokens); i += 2 {
			m[tokens[i]] = utf16hex(tokens[i+1])
		}
	}
	for _, block := range bfrange.FindAllSubmatch(data, -1) {
		parts := hexToken.FindAllSubmatch(block[1], -1)
		for i := 0; i+2 < len(parts); i += 3 {
			lo, hi := clean(parts[i][1]), clean(parts[i+1][1])
			a, err1 := strconv.ParseUint(lo, 16, 32)
			b, err2 := strconv.ParseUint(hi, 16, 32)
			if err1 != nil || err2 != nil || b < a || b-a > 0xFFFF {
				continue
			}
			width := len(lo)
			if parts[i+2][2] != nil {
				dests := hexes(parts[i+2][2])
				for k := uint64(0); k <= b-a && int(k) < len(dests); k++ {
					m[pad(a+k, width)] = utf16hex(dests[k])
				}
				continue
			}
			start := clean(parts[i+2][1])
			base, err := strconv.ParseUint(start, 16, 32)
			if err != nil {
				continue
			}
			for k := uint64(0); k <= b-a; k++ {
				m[pad(a+k, width)] = utf16hex(pad(base+k, len(start)))
			}
		}
	}
	return m
}

func clean(b []byte) string {
	return strings.ToUpper(strings.Join(strings.Fields(string(b)), ""))
}

func hexes(b []byte) []string {
	var out []string
	for _, m := range hexToken.FindAllSubmatch(b, -1) {
		if m[1] != nil {
			out = append(out, clean(m[1]))
		}
	}
	return out
}

func pad(n uint64, width int) string {
	s := strings.ToUpper(strconv.FormatUint(n, 16))
	for len(s) < width {
		s = "0" + s
	}
	return s
}

func utf16hex(h string) string {
	var units []uint16
	for i := 0; i+4 <= len(h); i += 4 {
		n, err := strconv.ParseUint(h[i:i+4], 16, 16)
		if err != nil {
			return ""
		}
		units = append(units, uint16(n))
	}
	if len(h) == 2 {
		n, _ := strconv.ParseUint(h, 16, 8)
		units = append(units, uint16(n))
	}
	return string(utf16.Decode(units))
}

// runText follows a content stream's text operators.
func runText(content []byte, fonts map[name]map[string]string) string {
	var out strings.Builder
	p := &parser{b: content}
	var stack []value
	var font map[string]string
	show := func(s string) {
		out.WriteString(decodeString(s, font))
	}
	newline := func() {
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			out.WriteByte('\n')
		}
	}
	for p.i < len(p.b) {
		v := p.object()
		op, isOp := v.(operator)
		if !isOp {
			if v == nil && p.i >= len(p.b) {
				break
			}
			stack = append(stack, v)
			if len(stack) > 64 {
				stack = stack[1:]
			}
			continue
		}
		switch op {
		case "Tf":
			if len(stack) >= 2 {
				if n, ok := stack[len(stack)-2].(name); ok {
					font = fonts[n]
				}
			}
		case "Tj":
			if len(stack) >= 1 {
				if s, ok := stack[len(stack)-1].(string); ok {
					show(s)
				}
			}
		case "'", "\"":
			newline()
			if len(stack) >= 1 {
				if s, ok := stack[len(stack)-1].(string); ok {
					show(s)
				}
			}
		case "TJ":
			if len(stack) >= 1 {
				if parts, ok := stack[len(stack)-1].([]value); ok {
					for _, part := range parts {
						switch x := part.(type) {
						case string:
							show(x)
						case float64:
							if x < -200 && !strings.HasSuffix(out.String(), " ") {
								out.WriteByte(' ')
							}
						}
					}
				}
			}
		case "Td", "TD":
			if len(stack) >= 2 {
				if y, ok := stack[len(stack)-1].(float64); ok && y != 0 {
					newline()
				} else if !strings.HasSuffix(out.String(), " ") && out.Len() > 0 {
					out.WriteByte(' ')
				}
			}
		case "T*", "ET":
			newline()
		case "Tm":
			newline()
		}
		stack = stack[:0]
	}
	return strings.TrimSpace(out.String())
}

func decodeString(s string, font map[string]string) string {
	if font != nil {
		// Codes are one or two bytes; try the width the map uses.
		width := 2
		for k := range font {
			width = len(k)
			break
		}
		step := width / 2
		var b strings.Builder
		for i := 0; i+step <= len(s); i += step {
			key := strings.ToUpper(hexOf(s[i : i+step]))
			if r, ok := font[key]; ok {
				b.WriteString(r)
			}
		}
		return b.String()
	}
	// Without a map, read bytes as Latin-1, the usual simple encoding.
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		r := rune(s[i])
		if unicode.IsPrint(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func hexOf(s string) string {
	const digits = "0123456789ABCDEF"
	out := make([]byte, 0, len(s)*2)
	for i := 0; i < len(s); i++ {
		out = append(out, digits[s[i]>>4], digits[s[i]&15])
	}
	return string(out)
}
