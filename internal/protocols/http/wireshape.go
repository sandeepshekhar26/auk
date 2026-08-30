package http

import (
	"apitool/internal/core/model"
)

// BuildURL exposes the exact URL this package puts on the wire — the
// resolved URL with the enabled query-param rows merged in — to consumers
// that need to REPRODUCE a request without dispatching it (App.
// ResolveForSnippet, which backs "Copy as cURL/Python/JS/Go"). It is a
// one-line pass-through to the same unexported buildURL Execute itself
// calls, deliberately, so a snippet can never disagree with the send about
// what the URL is.
func BuildURL(raw string, params []model.KeyValue) (string, error) {
	return buildURL(raw, params)
}
