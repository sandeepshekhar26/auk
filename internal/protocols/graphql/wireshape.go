package graphql

import (
	"apitool/internal/core/model"
)

// BuildEnvelope exposes the exact JSON body this package puts on the wire —
// the {"query":...,"variables":...} envelope — to consumers that need to
// REPRODUCE a GraphQL request without dispatching it (App.ResolveForSnippet,
// which backs "Copy as cURL/Python/JS/Go"). A pass-through to the same
// unexported buildEnvelope Execute itself calls, so a snippet can never
// disagree with the send about the request body.
//
// Callers reproducing the wire request must also set Content-Type:
// application/json, which Execute sets unconditionally for this protocol.
func BuildEnvelope(body *model.RequestBody) ([]byte, error) {
	return buildEnvelope(body)
}
