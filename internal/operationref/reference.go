// Package operationref encodes review addresses, never credentials or authority.
package operationref

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
)

func idBytes(id string) ([]byte, bool) {
	if len(id) != 36 || id != strings.ToLower(id) || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return nil, false
	}
	data, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return data, err == nil && len(data) == 16
}

func Encode(book, proposal string) string {
	b, bok := idBytes(book)
	p, pok := idBytes(proposal)
	if !bok || !pok {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(append(b, p...))
}

func Decode(value string) (book, proposal string, ok bool) {
	if len(value) != 43 {
		return "", "", false
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(data) != 32 || base64.RawURLEncoding.EncodeToString(data) != value {
		return "", "", false
	}
	id := func(data []byte) string {
		hexadecimal := hex.EncodeToString(data)
		return hexadecimal[:8] + "-" + hexadecimal[8:12] + "-" + hexadecimal[12:16] + "-" + hexadecimal[16:20] + "-" + hexadecimal[20:]
	}
	return id(data[:16]), id(data[16:]), true
}
