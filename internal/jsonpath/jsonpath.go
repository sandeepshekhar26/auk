// Package jsonpath is a tiny dot/bracket path evaluator ("a.b[0].c") over
// JSON documents — the shared core behind templating's json.get(), the
// response('Name').body.token chaining refs, and the assertion engine's
// body checks. It deliberately supports only object field access and array
// indexing, not the full JSONPath spec.
package jsonpath

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrNotFound reports that the path walked off the document (missing field
// or out-of-range index) — as opposed to malformed input or a malformed
// path. The assertion engine's `exists` operator branches on this.
var ErrNotFound = errors.New("not found")

// Get evaluates path against a JSON document string and returns the matched
// value rendered as a string: scalars render plainly (no quotes on strings),
// objects and arrays render as compact JSON.
func Get(jsonStr, path string) (string, error) {
	var doc any
	if err := json.Unmarshal([]byte(jsonStr), &doc); err != nil {
		return "", fmt.Errorf("invalid JSON input: %w", err)
	}

	tokens, err := parse(path)
	if err != nil {
		return "", err
	}

	cur := doc
	for _, tok := range tokens {
		switch t := tok.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return "", fmt.Errorf("cannot index field %q into non-object value: %w", t, ErrNotFound)
			}
			v, ok := m[t]
			if !ok {
				return "", fmt.Errorf("field %q: %w", t, ErrNotFound)
			}
			cur = v
		case int:
			arr, ok := cur.([]any)
			if !ok {
				return "", fmt.Errorf("cannot index [%d] into non-array value: %w", t, ErrNotFound)
			}
			if t < 0 || t >= len(arr) {
				return "", fmt.Errorf("index [%d] out of range (len %d): %w", t, len(arr), ErrNotFound)
			}
			cur = arr[t]
		}
	}

	return ValueToString(cur), nil
}

// parse tokenizes "a.b[0].c" / "[0].a" / "a[1][2]" into a sequence of
// string (object key) and int (array index) tokens.
func parse(path string) ([]any, error) {
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

// ValueToString renders a decoded JSON value the way template expansion and
// assertion comparison need: scalars plain (no quotes on strings), objects
// and arrays as compact JSON.
func ValueToString(v any) string {
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
