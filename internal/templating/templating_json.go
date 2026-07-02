package templating

import (
	"fmt"

	"apitool/internal/jsonpath"
)

// jsonGetPath backs the ${json.get(doc, path)} template function. The actual
// evaluator lives in internal/jsonpath so the assertion engine and chaining
// refs share the exact same path semantics.
func jsonGetPath(jsonStr, path string) (string, error) {
	out, err := jsonpath.Get(jsonStr, path)
	if err != nil {
		return "", fmt.Errorf("json.get: %w", err)
	}
	return out, nil
}
