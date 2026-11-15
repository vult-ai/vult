package pdftext

import (
	"bytes"
	"strconv"
)

// operator is a bare word in a content stream, such as Tj.
type operator string

type parser struct {
	b     []byte
	i     int
	depth int
}

func white(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

func delimiter(c byte) bool {
	return white(c) || bytes.IndexByte([]byte("()<>[]{}/%"), c) >= 0
}

func (p *parser) skip() {
	for p.i < len(p.b) {
		c := p.b[p.i]
		switch {
		case white(c):
			p.i++
		case c == '%':
			for p.i < len(p.b) && p.b[p.i] != '\n' && p.b[p.i] != '\r' {
				p.i++
			}
		default:
			return
		}
	}
}

// object reads the next object. References and streams are resolved from
// what follows the object.
func (p *parser) object() value {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 64 {
		p.i = len(p.b)
		return nil
	}
	p.skip()
	if p.i >= len(p.b) {
		return nil
	}
	c := p.b[p.i]
	switch {
	case c == '/':
		p.i++
		start := p.i
		for p.i < len(p.b) && !delimiter(p.b[p.i]) {
			p.i++
		}
		return name(unescapeName(p.b[start:p.i]))
	case c == '(':
		return p.literal()
	case c == '<' && p.i+1 < len(p.b) && p.b[p.i+1] == '<':
		p.i += 2
		d := dict{}
		for {
			p.skip()
			if p.i+1 < len(p.b) && p.b[p.i] == '>' && p.b[p.i+1] == '>' {
				p.i += 2
				break
			}
			if p.i >= len(p.b) {
				return d
			}
			k, ok := p.object().(name)
			if !ok {
				continue
			}
			d[k] = p.object()
		}
		return p.maybeStream(d)
	case c == '<':
		p.i++
		end := bytes.IndexByte(p.b[p.i:], '>')
		if end < 0 {
			p.i = len(p.b)
			return ""
		}
		h := p.b[p.i : p.i+end]
		p.i += end + 1
		return string(unhex(h))
	case c == '[':
		p.i++
		var list []value
		for {
			p.skip()
			if p.i >= len(p.b) {
				return list
			}
			if p.b[p.i] == ']' {
				p.i++
				return list
			}
			list = append(list, p.object())
		}
	case c == ']' || c == '>' || c == ')' || c == '{' || c == '}':
		p.i++
		return p.object()
	}
	start := p.i
	for p.i < len(p.b) && !delimiter(p.b[p.i]) {
		p.i++
	}
	word := string(p.b[start:p.i])
	if n, err := strconv.ParseFloat(word, 64); err == nil {
		// num gen R is a reference.
		save := p.i
		p.skip()
		gstart := p.i
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
		}
		if p.i > gstart {
			p.skip()
			if p.i < len(p.b) && p.b[p.i] == 'R' && (p.i+1 == len(p.b) || delimiter(p.b[p.i+1])) {
				p.i++
				return ref{num: int(n)}
			}
		}
		p.i = save
		return n
	}
	switch word {
	case "true":
		return true
	case "false":
		return false
	case "null", "":
		return nil
	case "BI":
		// Skip an inline image to its end.
		if end := bytes.Index(p.b[p.i:], []byte("EI")); end >= 0 {
			p.i += end + 2
		} else {
			p.i = len(p.b)
		}
		return operator("EI")
	}
	return operator(word)
}

func (p *parser) maybeStream(d dict) value {
	save := p.i
	p.skip()
	if !bytes.HasPrefix(p.b[p.i:], []byte("stream")) {
		p.i = save
		return d
	}
	p.i += len("stream")
	if p.i < len(p.b) && p.b[p.i] == '\r' {
		p.i++
	}
	if p.i < len(p.b) && p.b[p.i] == '\n' {
		p.i++
	}
	start := p.i
	end := -1
	if n, ok := d["Length"].(float64); ok && n >= 0 && start+int(n) <= len(p.b) {
		after := p.b[start+int(n):]
		trimmed := bytes.TrimLeft(after, "\r\n \t")
		if bytes.HasPrefix(trimmed, []byte("endstream")) {
			end = start + int(n)
		}
	}
	if end < 0 {
		k := bytes.Index(p.b[start:], []byte("endstream"))
		if k < 0 {
			p.i = len(p.b)
			return &stream{dict: d}
		}
		end = start + k
		for end > start && (p.b[end-1] == '\n' || p.b[end-1] == '\r') {
			end--
		}
	}
	p.i = end
	if k := bytes.Index(p.b[p.i:], []byte("endstream")); k >= 0 {
		p.i += k + len("endstream")
	}
	return &stream{dict: d, raw: p.b[start:end]}
}

func (p *parser) literal() value {
	p.i++
	var out []byte
	level := 1
	for p.i < len(p.b) {
		c := p.b[p.i]
		p.i++
		switch c {
		case '(':
			level++
			out = append(out, c)
		case ')':
			level--
			if level == 0 {
				return string(out)
			}
			out = append(out, c)
		case '\\':
			if p.i >= len(p.b) {
				return string(out)
			}
			e := p.b[p.i]
			p.i++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '\r':
				if p.i < len(p.b) && p.b[p.i] == '\n' {
					p.i++
				}
			case '\n':
			default:
				if e >= '0' && e <= '7' {
					n := int(e - '0')
					for k := 0; k < 2 && p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '7'; k++ {
						n = n*8 + int(p.b[p.i]-'0')
						p.i++
					}
					out = append(out, byte(n))
				} else {
					out = append(out, e)
				}
			}
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func unhex(h []byte) []byte {
	var digits []byte
	for _, c := range h {
		if !white(c) {
			digits = append(digits, c)
		}
	}
	if len(digits)%2 == 1 {
		digits = append(digits, '0')
	}
	out := make([]byte, 0, len(digits)/2)
	for i := 0; i+1 < len(digits); i += 2 {
		n, err := strconv.ParseUint(string(digits[i:i+2]), 16, 8)
		if err != nil {
			return out
		}
		out = append(out, byte(n))
	}
	return out
}

func unescapeName(b []byte) string {
	if bytes.IndexByte(b, '#') < 0 {
		return string(b)
	}
	var out []byte
	for i := 0; i < len(b); i++ {
		if b[i] == '#' && i+2 < len(b) {
			if n, err := strconv.ParseUint(string(b[i+1:i+3]), 16, 8); err == nil {
				out = append(out, byte(n))
				i += 2
				continue
			}
		}
		out = append(out, b[i])
	}
	return string(out)
}
