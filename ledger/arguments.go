package ledger

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// argumentProblem explains, in words an agent can act on, why arguments
// don't fit an operation: unknown names and missing required ones. It
// returns ErrInvalid when it can't say more.
func argumentProblem(operation string, input []byte, shape any, required ...string) error {
	var given map[string]json.RawMessage
	if json.Unmarshal(input, &given) != nil {
		return &RefusedError{Reason: operation + " takes a JSON object of arguments."}
	}
	known := map[string]bool{}
	t := reflect.TypeOf(shape)
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			known[name] = true
		}
	}
	var unknown, missing []string
	for name := range given {
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	for _, name := range required {
		if _, ok := given[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(unknown) == 0 && len(missing) == 0 {
		return ErrInvalid
	}
	sort.Strings(unknown)
	var parts []string
	if len(unknown) > 0 {
		parts = append(parts, "it doesn't take "+strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		parts = append(parts, "it needs "+strings.Join(missing, ", "))
	}
	all := make([]string, 0, len(known))
	for name := range known {
		all = append(all, name)
	}
	sort.Strings(all)
	return &RefusedError{Reason: "These arguments don't fit " + operation + ": " + strings.Join(parts, "; ") + ". Its arguments are " + strings.Join(all, ", ") + "."}
}
