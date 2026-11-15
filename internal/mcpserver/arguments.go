package mcpserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Every tool checks its arguments against its own published schema before
// Vult acts, and explains a mismatch the same way: which argument, what it
// expects, an example, and what was given. An agent can fix the call from
// the message alone.

// argumentError is a refusal of a call's shape. Nothing was done.
type argumentError struct{ text string }

func (e *argumentError) Error() string { return e.text }

// checkArguments returns nil when input fits the tool's schema.
func checkArguments(tool string, schema map[string]any, input json.RawMessage) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return &argumentError{tool + " takes a JSON object of arguments."}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return &argumentError{tool + " takes a JSON object of arguments, not " + describe(value) + "."}
	}
	var problems []string
	checkObject("", schema, object, &problems)
	if len(problems) == 0 && tool == "prepare_operation" {
		if operation, _ := object["operation"].(string); operation != "" {
			if args, ok := object["arguments"].(map[string]any); ok {
				if inner := operationSchema(schema, operation); inner != nil {
					checkObject("arguments.", inner, args, &problems)
					// A preparation's own argument put inside arguments.
					outer, _ := schema["properties"].(map[string]any)
					innerProperties, _ := inner["properties"].(map[string]any)
					for _, name := range sortedKeys(args) {
						if _, mine := innerProperties[name]; !mine && name != "arguments" {
							if _, theirs := outer[name]; theirs {
								problems = append(problems, name+" goes beside arguments, not inside it")
							}
						}
					}
				}
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return &argumentError{"These arguments don't fit " + tool + ": " + strings.Join(problems, "; ") + "."}
}

func operationSchema(schema map[string]any, operation string) map[string]any {
	choices, _ := schema["oneOf"].([]any)
	for _, c := range choices {
		choice, _ := c.(map[string]any)
		properties, _ := choice["properties"].(map[string]any)
		op, _ := properties["operation"].(map[string]any)
		if op["const"] == operation {
			inner, _ := properties["arguments"].(map[string]any)
			return inner
		}
	}
	return nil
}

func checkObject(path string, schema map[string]any, object map[string]any, problems *[]string) {
	properties, _ := schema["properties"].(map[string]any)
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	var unknown, missing []string
	if schema["additionalProperties"] == false {
		for name := range object {
			if _, ok := properties[name]; !ok {
				unknown = append(unknown, name)
			}
		}
	}
	for _, r := range strings2(schema["required"]) {
		if _, ok := object[r]; !ok {
			missing = append(missing, path+r)
		}
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		where := "it doesn't take "
		if path != "" {
			where = strings.TrimSuffix(path, ".") + " doesn't take "
		}
		*problems = append(*problems, where+strings.Join(unknown, ", ")+" (its arguments are "+strings.Join(names, ", ")+")")
	}
	if len(missing) > 0 {
		*problems = append(*problems, "it needs "+strings.Join(missing, ", "))
	}
	for _, name := range sortedKeys(object) {
		if p, ok := properties[name].(map[string]any); ok {
			checkValue(path+name, p, object[name], problems)
		}
	}
}

func checkValue(path string, schema map[string]any, value any, problems *[]string) {
	want := func() string { return path + " must be " + expected(schema) }
	got := "; got " + describe(value)
	fail := func(text string) { *problems = append(*problems, text) }
	if c, ok := schema["const"]; ok && value != c {
		fail(want() + got)
		return
	}
	switch schema["type"] {
	case "string":
		s, ok := value.(string)
		if !ok {
			fail(want() + got)
			return
		}
		n := len([]rune(s))
		if min, ok := number(schema["minLength"]); ok && float64(n) < min {
			if n == 0 {
				fail(path + " can't be empty")
			} else {
				fail(want() + got)
			}
			return
		}
		if max, ok := number(schema["maxLength"]); ok && float64(n) > max {
			fail(fmt.Sprintf("%s is %d characters; at most %v", path, n, max))
			return
		}
		if enum := strings2(schema["enum"]); len(enum) > 0 && !contains(enum, s) {
			fail(want() + got)
			return
		}
		if pattern, ok := schema["pattern"].(string); ok {
			if re, err := regexp.Compile(pattern); err == nil && !re.MatchString(s) {
				fail(want() + got)
				return
			}
		}
		if idArgument(path, schema) && !(s == "" && strings.HasSuffix(path, "parent_account_id")) && !uuidShape.MatchString(s) {
			fail(path + ` must be an ID as Vult returns it, like "0190a8f2-6c1e-7d4b-9a63-2f5e8c1b7d40"; read it from the tool that lists it` + got)
			return
		}
		switch schema["format"] {
		case "date":
			if _, err := time.Parse(time.DateOnly, s); err != nil {
				if regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`).MatchString(s) {
					fail(path + " " + quote(s) + " isn't a real date")
				} else {
					fail(want() + got)
				}
			}
		case "uuid":
			if !uuidShape.MatchString(s) {
				fail(want() + got)
			}
		}
	case "integer", "number":
		n, ok := value.(json.Number)
		if !ok {
			fail(want() + got)
			return
		}
		f, err := n.Float64()
		if err != nil || schema["type"] == "integer" && strings.ContainsAny(n.String(), ".eE") {
			fail(want() + got)
			return
		}
		if min, ok := number(schema["minimum"]); ok && f < min {
			fail(want() + got)
		} else if max, ok := number(schema["maximum"]); ok && f > max {
			fail(want() + got)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			fail(want() + got)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			fail(want() + got)
			return
		}
		if min, ok := number(schema["minItems"]); ok && float64(len(items)) < min {
			fail(fmt.Sprintf("%s needs at least %v items; got %d", path, min, len(items)))
			return
		}
		if max, ok := number(schema["maxItems"]); ok && float64(len(items)) > max {
			fail(fmt.Sprintf("%s takes at most %v items; got %d", path, max, len(items)))
			return
		}
		if item, ok := schema["items"].(map[string]any); ok {
			for i, v := range items {
				checkValue(fmt.Sprintf("%s[%d]", path, i), item, v, problems)
			}
		}
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			fail(want() + got)
			return
		}
		checkObject(path+".", schema, object, problems)
	}
}

// expected says what a schema accepts, with an example.
func expected(schema map[string]any) string {
	if c, ok := schema["const"]; ok {
		return describe(c)
	}
	switch schema["type"] {
	case "string":
		if enum := strings2(schema["enum"]); len(enum) > 0 {
			if len(enum) == 1 {
				return quote(enum[0])
			}
			quoted := make([]string, len(enum))
			for i, e := range enum {
				quoted[i] = quote(e)
			}
			return "one of " + strings.Join(quoted, ", ")
		}
		if p, ok := schema["pattern"].(string); ok {
			if text, ok := patterns[p]; ok {
				return text
			}
			return "text matching " + p
		}
		switch schema["format"] {
		case "date":
			return `a date as YYYY-MM-DD, like "2026-01-31"`
		case "uuid":
			return "an ID as Vult returns it, like \"0190a8f2-6c1e-7d4b-9a63-2f5e8c1b7d40\""
		}
		if min, ok := number(schema["minLength"]); ok && min > 0 {
			return "text"
		}
		return "a string"
	case "integer":
		text := "a whole number"
		min, hasMin := number(schema["minimum"])
		max, hasMax := number(schema["maximum"])
		switch {
		case hasMin && hasMax:
			text += fmt.Sprintf(" from %v to %v", min, max)
		case hasMin:
			text += fmt.Sprintf(" of at least %v", min)
		case hasMax:
			text += fmt.Sprintf(" of at most %v", max)
		}
		return text + ", like " + fmt.Sprint(exampleInteger(min, hasMin))
	case "number":
		return "a number"
	case "boolean":
		return "true or false"
	case "array":
		if item, ok := schema["items"].(map[string]any); ok {
			if item["type"] == "string" && item["enum"] == nil && item["pattern"] == nil && item["format"] == nil {
				return `a list of text, like ["First point.", "Second point."]`
			}
			return "a list of " + strings.TrimPrefix(expected(item), "a ")
		}
		return "a list"
	case "object":
		return "an object"
	}
	return "a value"
}

func exampleInteger(min float64, hasMin bool) int {
	if hasMin && min > 1 {
		return int(min)
	}
	return 1
}

// patterns describe the formats the tools use, each with an example.
var patterns = map[string]string{
	`^(0|[1-9][0-9]{0,18})$`:                 `an amount in minor units as a string of digits, like "1999" for 19.99`,
	`^[1-9][0-9]{0,18}$`:                     `a positive amount in minor units as a string of digits, like "1999" for 19.99`,
	`^-?(0|[1-9][0-9]{0,18})$`:               `an amount in minor units as a string of digits, negative allowed, like "-1999" for -19.99`,
	`^(0|-?[1-9][0-9]{0,18})$`:               `an amount in minor units as a string of digits, negative allowed, like "-1999" for -19.99`,
	`^-?[1-9][0-9]{0,18}$`:                   `a nonzero amount in minor units as a string of digits, like "-1999" for -19.99`,
	`^(0|[1-9][0-9]{0,63})(\.[0-9]{1,64})?$`: `a decimal number as a string, like "1.0825"`,
	`^[A-Z]{3}$`:                             `a three-letter currency code in capitals, like "USD"`,
	`^[0-9a-f]{64}$`:                         "a SHA-256 as 64 lowercase hexadecimal characters",
	`^[a-f0-9]{64}$`:                         "a SHA-256 as 64 lowercase hexadecimal characters",
	`^[a-z0-9][a-z0-9._-]{0,127}$`:           `lowercase letters, digits, ".", "_" and "-", like "example-key"`,
	`^[a-z][a-z0-9_]{0,63}$`:                 `lowercase letters, digits and "_", starting with a letter, like "example_key"`,
}

// describe names a given value briefly, without echoing long text.
func describe(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		if len(v) > 40 {
			return "a string of " + fmt.Sprint(len([]rune(v))) + " characters"
		}
		return quote(v)
	case json.Number:
		return "the number " + v.String()
	case bool:
		return fmt.Sprint(v)
	case []any:
		return fmt.Sprintf("a list of %d items", len(v))
	case map[string]any:
		return "an object"
	}
	return "a value"
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case int:
		return float64(n), true
	}
	return 0, false
}

func strings2(v any) []string {
	var out []string
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// toolSchema turns a tool's schema into plain values for checking.
func toolSchema(schema map[string]any) map[string]any {
	b, err := json.Marshal(schema)
	if err != nil {
		panic("invalid built-in tool schema")
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		panic("invalid built-in tool schema")
	}
	return out
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// idArgument is an argument that takes an ID Vult gave out: named *_id or
// an item of *_ids, a plain string with no format of its own.
func idArgument(path string, schema map[string]any) bool {
	if len(schema) != 1 {
		return false
	}
	name := path
	if i := strings.LastIndex(name, "["); i >= 0 && strings.HasSuffix(name, "]") {
		name = name[:i]
		return strings.HasSuffix(name, "_ids")
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return strings.HasSuffix(name, "_id") && !strings.HasSuffix(name, "external_id")
}
