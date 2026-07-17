package sandbox

import (
	"io"
	"strings"

	"encoding/json"
)

// readArgs decodes the JSON object on stdin into a generic map. Empty stdin is
// treated as no arguments.
func readArgs(stdin io.Reader) (map[string]any, error) {
	raw, err := io.ReadAll(io.LimitReader(stdin, 16<<20))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	return args, nil
}

func argStr(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func argBool(args map[string]any, key string) bool {
	b, _ := args[key].(bool)
	return b
}

// argBoolPtr returns a pointer to a bool-valued arg, or nil when the key is
// absent (or not a bool). Callers use the nil case to omit the field entirely so
// the control plane falls back to its own default, matching the Python tools
// which forward `undefined` for unset optional booleans.
func argBoolPtr(args map[string]any, key string) *bool {
	if b, ok := args[key].(bool); ok {
		return &b
	}
	return nil
}

// argInt returns an integer-valued arg (JSON numbers decode as float64).
func argInt(args map[string]any, key string) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	return 0
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
