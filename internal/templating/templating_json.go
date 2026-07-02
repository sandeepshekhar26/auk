package templating

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// jsonGetPath evaluates a small dot/bracket path (e.g. "a.b[0].c") against a
// JSON document string and returns the matched value rendered as a string.
// This is a hand-rolled evaluator, not a JSONPath library — it supports the
// common case (object field access + array indexing) that template
// expressions need, not the full JSONPath spec.
func jsonGetPath(jsonStr, path string) (string, error) {
	var doc any
	if err := json.Unmarshal([]byte(jsonStr), &doc); err != nil {
		return "", fmt.Errorf("json.get: invalid JSON input: %w", err)
	}

	tokens, err := parseJSONPath(path)
	if err != nil {
		return "", fmt.Errorf("json.get: %w", err)
	}

	cur := doc
	for _, tok := range tokens {
		switch t := tok.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return "", fmt.Errorf("json.get: cannot index field %q into non-object value", t)
			}
			v, ok := m[t]
			if !ok {
				return "", fmt.Errorf("json.get: field %q not found", t)
			}
			cur = v
		case int:
			arr, ok := cur.([]any)
			if !ok {
				return "", fmt.Errorf("json.get: cannot index [%d] into non-array value", t)
			}
			if t < 0 || t >= len(arr) {
				return "", fmt.Errorf("json.get: index [%d] out of range (len %d)", t, len(arr))
			}
			cur = arr[t]
		}
	}

	return jsonValueToString(cur), nil
}

// parseJSONPath tokenizes "a.b[0].c" / "[0].a" / "a[1][2]" into a sequence of
// string (object key) and int (array index) tokens.
func parseJSONPath(path string) ([]any, error) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")

	var tokens []any
	var buf strings.Builder

	flush := func() {
		if buf.Len() > 0 {
			tokens = append(tokens, buf.String())
			buf.Reset()
		}
	}

	i := 0
	for i < len(path) {
		c := path[i]
		switch c {
		case '.':
			flush()
			i++
		case '[':
			flush()
			end := strings.IndexByte(path[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("unterminated '[' in path %q", path)
			}
			idxStr := path[i+1 : i+end]
			idx, err := strconv.Atoi(strings.TrimSpace(idxStr))
			if err != nil {
				return nil, fmt.Errorf("invalid array index %q in path %q", idxStr, path)
			}
			tokens = append(tokens, idx)
			i += end + 1
		default:
			buf.WriteByte(c)
			i++
		}
	}
	flush()

	if len(tokens) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	return tokens, nil
}

// jsonValueToString renders a decoded JSON value the way a template
// expansion needs: scalars render plainly (no quotes on strings), objects
// and arrays render as compact JSON.
func jsonValueToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprintf("%v", t)
		}
		return string(b)
	}
}
