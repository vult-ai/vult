package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid JSON")

// Decode reads one object, rejecting ambiguous keys and invalid Unicode.
func Decode(raw []byte, value any) error {
	return decode(raw, value, false, "")
}

// DecodeNonNull also rejects null values, except at the named field path.
// An empty path disallows every null.
func DecodeNonNull(raw []byte, value any, nullable string) error {
	return decode(raw, value, true, nullable)
}

func decode(raw []byte, value any, rejectNull bool, nullable string) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '{' || !utf8.Valid(raw) || !validEscapes(raw) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var scan func(int, reflect.Type, string) error
	scan = func(depth int, shape reflect.Type, path string) error {
		if depth > 32 {
			return ErrInvalid
		}
		tok, err := d.Token()
		if err != nil {
			return ErrInvalid
		}
		if rejectNull && tok == nil && (path == "" || path != nullable) {
			return ErrInvalid
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				s, ok := key.(string)
				if err != nil || !ok || seen[s] {
					return ErrInvalid
				}
				seen[s] = true
				child, ok := objectField(shape, s)
				if !ok || scan(depth+1, child, joinPath(path, s)) != nil {
					return ErrInvalid
				}
			}
		case '[':
			shape = dereference(shape)
			var child reflect.Type
			if shape != nil && (shape.Kind() == reflect.Slice || shape.Kind() == reflect.Array) {
				child = shape.Elem()
			}
			for d.More() {
				if scan(depth+1, child, path+"[]") != nil {
					return ErrInvalid
				}
			}
		default:
			return ErrInvalid
		}
		end, err := d.Token()
		if err != nil || (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
			return ErrInvalid
		}
		return nil
	}
	if scan(0, reflect.TypeOf(value), "") != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(value) != nil {
		return ErrInvalid
	}
	return nil
}

func joinPath(parent, field string) string {
	if parent == "" {
		return field
	}
	return parent + "." + field
}

func dereference(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// encoding/json matches struct fields without regard to case. The wire contract
// is case-sensitive: aliases must not override an earlier, differently cased key.
func objectField(t reflect.Type, key string) (reflect.Type, bool) {
	t = dereference(t)
	if t == nil {
		return nil, true
	}
	if t.Kind() == reflect.Map {
		return t.Elem(), true
	}
	if t.Kind() != reflect.Struct {
		return nil, true
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		if name != "-" && name == key {
			return field.Type, true
		}
	}
	return nil, false
}

// encoding/json replaces lone UTF-16 surrogates. Reject them before decoding so
// different request bytes cannot silently become the same recorded text.
func validEscapes(raw []byte) bool {
	quoted := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		read := func(start int) (uint16, bool) {
			if start+4 > len(raw) {
				return 0, false
			}
			var n uint16
			for _, b := range raw[start : start+4] {
				n <<= 4
				switch {
				case b >= '0' && b <= '9':
					n |= uint16(b - '0')
				case b >= 'a' && b <= 'f':
					n |= uint16(b-'a') + 10
				case b >= 'A' && b <= 'F':
					n |= uint16(b-'A') + 10
				default:
					return 0, false
				}
			}
			return n, true
		}
		n, ok := read(i + 1)
		if !ok || n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		i += 4
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, ok := read(i + 3)
			if !ok || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return !quoted
}
