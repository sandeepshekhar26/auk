package main

import (
	"fmt"

	"apitool/internal/core/model"
	graphqlprotocol "apitool/internal/protocols/graphql"
	httpprotocol "apitool/internal/protocols/http"
)

// SnippetRequest is the fully-resolved shape of a request as it WOULD go on
// the wire — the input every "Copy as <language>" generator in
// frontend/src/lib/snippets.ts renders from.
//
// It exists so a snippet is produced from the same resolve path a real Send
// uses (environment + folder variables expanded, `:name` path params
// substituted, query params merged, auth headers computed, pre-request
// script applied) instead of from the raw RequestDef the editor holds. The
// old client-side generator read the stored definition directly, so it
// emitted `${baseUrl}/users/:id` and a hand-rolled approximation of only the
// three simplest auth kinds — a snippet you couldn't paste into a terminal.
type SnippetRequest struct {
	Method string `json:"method"`
	// URL is post-substitution and post-query-merge: exactly the string the
	// protocol client would dial.
	URL     string           `json:"url"`
	Headers []model.KeyValue `json:"headers"`
	// Body is the literal byte payload as a string; HasBody distinguishes
	// "no request body at all" from "a body that happens to be empty".
	Body    string `json:"body"`
	HasBody bool   `json:"hasBody"`
}

// ResolveForSnippet resolves requestID against environmentID and returns
// what would be sent, WITHOUT sending it — the backend half of "Copy as
// cURL" working before a request has ever been run.
//
// It goes through engine.ResolveForExecution, i.e. the identical
// resolve+auth+script+policy front half of RunRequest (the k6 perf runner
// already reuses it the same way), so there is one resolution
// implementation, not a second one that can drift. Origin is "gui" because
// that is literally what it is: a human clicked Copy in the app. Nothing is
// persisted and no history entry is written — this is a read of what a send
// would do.
//
// Credentials: the returned headers can contain real secrets (a Bearer
// token, a Basic credential, an OAuth2 access token just fetched, an AWS
// SigV4 signature). That is the point of the feature — the user explicitly
// asked for a runnable snippet — but it is also why this value is returned
// straight to the caller and nowhere else: never logged, never emitted as an
// event, never written to history or the response cache.
func (a *App) ResolveForSnippet(requestID string, environmentID string) (SnippetRequest, error) {
	req, resolved, err := a.engine.ResolveForExecution(a.ctx, requestID, environmentID, "gui")
	if err != nil {
		return SnippetRequest{}, err
	}

	switch req.Protocol {
	case model.ProtocolHTTP, model.ProtocolGraphQL:
	default:
		// WS/SSE/gRPC don't fit a one-shot request/response snippet. The UI
		// disables the menu for them; this is the backstop.
		return SnippetRequest{}, fmt.Errorf("code snippets are only available for HTTP and GraphQL requests (this one is %s)", req.Protocol)
	}

	out := SnippetRequest{Method: resolved.Method, Headers: enabledHeaders(resolved.Headers)}

	if req.Protocol == model.ProtocolGraphQL {
		// internal/protocols/graphql always POSTs a {query,variables}
		// envelope with an explicit Content-Type, regardless of the stored
		// body kind — reproduce that, via the protocol's own builder.
		payload, err := graphqlprotocol.BuildEnvelope(resolved.Body)
		if err != nil {
			return SnippetRequest{}, err
		}
		out.Method = "POST"
		out.URL = resolved.URL
		out.Headers = append([]model.KeyValue{{Key: "Content-Type", Value: "application/json", Enabled: true}}, out.Headers...)
		out.Body, out.HasBody = string(payload), true
		return out, nil
	}

	// HTTP: query params merge into the URL and the body goes out verbatim,
	// both through the http protocol's own helpers so encoding can't drift
	// from the send. (A "form" body is already kept encoded in Body.Text by
	// BodyEditor; there is no per-kind server-side encoding step to mirror.)
	fullURL, err := httpprotocol.BuildURL(resolved.URL, resolved.Params)
	if err != nil {
		return SnippetRequest{}, fmt.Errorf("build url: %w", err)
	}
	out.URL = fullURL
	if resolved.Body != nil && resolved.Body.Kind != model.BodyNone && resolved.Body.Text != "" {
		out.Body, out.HasBody = resolved.Body.Text, true
	}
	return out, nil
}

// enabledHeaders drops the rows Execute itself would drop (disabled, or a
// half-typed row with no name at all), so a snippet doesn't carry a header
// the real send would never emit.
func enabledHeaders(headers []model.KeyValue) []model.KeyValue {
	out := make([]model.KeyValue, 0, len(headers))
	for _, h := range headers {
		if h.Enabled && h.Key != "" {
			out = append(out, h)
		}
	}
	return out
}
