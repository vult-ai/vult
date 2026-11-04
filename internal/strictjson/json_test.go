package strictjson

import (
	"strings"
	"testing"
)

func TestDecodeRejectsAmbiguousObjects(t *testing.T) {
	for name, input := range map[string]string{
		"duplicate":             `{"name":"a","name":"b"}`,
		"escaped duplicate":     `{"name":"a","n\u0061me":"b"}`,
		"unknown":               `{"other":1}`,
		"case alias":            `{"name":"a","Name":"b"}`,
		"trailing":              `{"name":"a"} {}`,
		"null":                  `null`,
		"array":                 `[]`,
		"lone high surrogate":   `{"name":"\ud800"}`,
		"lone low surrogate":    `{"name":"\udfff"}`,
		"broken surrogate pair": `{"name":"\ud800\u0041"}`,
		"invalid UTF8":          "{\"name\":\"\xff\"}",
		"deep object":           `{"name":` + strings.Repeat("[", 34) + `0` + strings.Repeat("]", 34) + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			var q struct {
				Name string `json:"name"`
			}
			if Decode([]byte(input), &q) == nil {
				t.Fatal("ambiguous input accepted")
			}
		})
	}
}

func TestDecodePreservesUnicodeAndEscapes(t *testing.T) {
	for _, input := range []string{`{"name":"A 😀"}`, `{"name":"A \ud83d\ude00"}`} {
		var q struct {
			Name string `json:"name"`
		}
		if Decode([]byte(input), &q) != nil || q.Name != "A 😀" {
			t.Fatal("valid Unicode lost")
		}
	}
	var q struct {
		Name string `json:"name"`
	}
	if Decode([]byte(`{"name":"literal \\uD800 and \"quote\""}`), &q) != nil || q.Name != `literal \uD800 and "quote"` {
		t.Fatal("literal escape lost", q.Name)
	}
}

func TestNestedFieldsUseExactContractSpelling(t *testing.T) {
	var q struct {
		Lines []struct {
			Amount string `json:"amount_minor"`
		} `json:"lines"`
	}
	if Decode([]byte(`{"lines":[{"amount_minor":"1250"}]}`), &q) != nil || q.Lines[0].Amount != "1250" {
		t.Fatal("valid nested amount lost")
	}
	if Decode([]byte(`{"lines":[{"amount_minor":"1250","Amount_Minor":"9000"}]}`), &q) == nil {
		t.Fatal("case alias replaced a nested amount")
	}
}
