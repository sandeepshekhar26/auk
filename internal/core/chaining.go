package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"apitool/internal/core/model"
)

// ChainResolver is implemented by Engine and called back into by the
// templating package when it encounters a `response('Name').path`
// reference. It is declared here (not in templating) because resolving a
// chain ref may require actually sending the referenced request — i.e. it
// needs Store, Auth, Policy, Protocols and RunRequest itself — none of
// which templating may depend on without creating templating -> core ->
// templating import cycle. templating only ever sees this narrow interface
// (mirrored there as templating.ChainResolver) via the value passed into
// templating.New.
type ChainResolver interface {
	ResolveChainRef(ctx context.Context, workspaceID model.ID, requestName, path string) (string, error)
}

// maxChainDepth bounds recursive auto-send chains (A references B
// references C ...). A cycle (A -> B -> A) is caught immediately via the
// visited set below; this cap is a secondary guard against long
// (non-cyclic) chains and any bug that defeats the visited-set check.
const maxChainDepth = 8

// chainStateKey is the context key an in-flight RunRequest call stores its
// chain bookkeeping under, so a nested RunRequest triggered by auto-send
// resolution can see (and extend) the same depth counter / visited set
// without changing RunRequest's public signature.
type chainStateKey struct{}

type chainState struct {
	depth   int
	visited map[model.ID]bool
}

// withChainRequest returns a context carrying chain state that includes
// requestID in its visited set, plus an error if that would exceed the
// depth cap or revisit a request already in the chain (a cycle).
func withChainRequest(ctx context.Context, requestID model.ID) (context.Context, error) {
	prev, _ := ctx.Value(chainStateKey{}).(*chainState)

	next := &chainState{depth: 0, visited: map[model.ID]bool{}}
	if prev != nil {
		next.depth = prev.depth + 1
		for id := range prev.visited {
			next.visited[id] = true
		}
	}

	if next.visited[requestID] {
		return ctx, fmt.Errorf("chain cycle detected: request %q is already part of this chain", requestID)
	}
	if next.depth > maxChainDepth {
		return ctx, fmt.Errorf("chain depth exceeded %d: aborting to avoid a runaway chain", maxChainDepth)
	}
	next.visited[requestID] = true

	return context.WithValue(ctx, chainStateKey{}, next), nil
}

// ResolveChainRef implements templating.ChainResolver structurally (Engine
// is passed as the resolver value into templating.New; no import of
// templating from core is needed for that). It looks up requestName within
// workspaceID, returns its cached response if one exists, auto-sends it
// (origin="chain") through the SAME RunRequest/Dispatch/Policy chokepoint as
// every other request if no cached response exists yet, and extracts path
// out of the resulting body/headers/status.
func (e *Engine) ResolveChainRef(ctx context.Context, workspaceID model.ID, requestName, path string) (string, error) {
	target, err := e.Store.LookupRequestByName(workspaceID, requestName)
	if err != nil {
		return "", fmt.Errorf("lookup request %q: %w", requestName, err)
	}

	resp, ok := responseLookupFromStore{e.Store}.Lookup(target.ID)
	if !ok {
		// RunRequest itself re-applies the depth/cycle guard (via
		// withChainRequest) for target.ID before doing any network I/O, so a
		// cycle or depth-cap violation surfaces as a clean error here rather
		// than recursing.
		sessionID := requestName + ":chain:" + target.ID
		resp, err = e.RunRequest(ctx, sessionID, target.ID, "", "chain", NoopSink{})
		if err != nil {
			return "", fmt.Errorf("auto-send %q: %w", requestName, err)
		}
	}

	return extractChainValue(resp, path)
}

// extractChainValue reads `status`, `header.<Name>`, or `body[.jsonpath]`
// out of a resolved response.
func extractChainValue(resp model.ResponseData, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || path == "body" {
		body, err := decodeResponseBody(resp)
		if err != nil {
			return "", err
		}
		return body, nil
	}

	switch {
	case path == "status":
		return strconv.Itoa(resp.Status), nil
	case strings.HasPrefix(path, "header."):
		name := strings.TrimPrefix(path, "header.")
		for _, h := range resp.Headers {
			if strings.EqualFold(h.Key, name) {
				return h.Value, nil
			}
		}
		return "", fmt.Errorf("header %q not found on response", name)
	case strings.HasPrefix(path, "body."):
		body, err := decodeResponseBody(resp)
		if err != nil {
			return "", err
		}
		return jsonGetPath(body, strings.TrimPrefix(path, "body."))
	default:
		return "", fmt.Errorf("unsupported response() path %q (expected status, header.<Name>, body, or body.<jsonpath>)", path)
	}
}

func decodeResponseBody(resp model.ResponseData) (string, error) {
	if resp.Error != "" {
		return "", fmt.Errorf("referenced response is an error: %s", resp.Error)
	}
	b, err := base64.StdEncoding.DecodeString(resp.BodyBase64)
	if err != nil {
		return "", fmt.Errorf("decode cached response body: %w", err)
	}
	return string(b), nil
}

// jsonGetPath is a local mirror of internal/templating's JSON path
// evaluator (dot/bracket paths like "a.b[0].c" — object field access + array
// indexing, not the full JSONPath spec). core cannot import templating for
// this because templating already imports core (avoiding the templating ->
// core -> templating cycle), so the small evaluator is duplicated here
// rather than shared.
func jsonGetPath(jsonStr, path string) (string, error) {
	var doc any
	if err := json.Unmarshal([]byte(jsonStr), &doc); err != nil {
		return "", fmt.Errorf("invalid JSON response body: %w", err)
	}

	tokens, err := parseJSONPath(path)
	if err != nil {
		return "", err
	}

	cur := doc
	for _, tok := range tokens {
		switch t := tok.(type) {
		case string:
			m, ok := cur.(map[string]any)
			if !ok {
				return "", fmt.Errorf("cannot index field %q into non-object value", t)
			}
			v, ok := m[t]
			if !ok {
				return "", fmt.Errorf("field %q not found", t)
			}
			cur = v
		case int:
			arr, ok := cur.([]any)
			if !ok {
				return "", fmt.Errorf("cannot index [%d] into non-array value", t)
			}
			if t < 0 || t >= len(arr) {
				return "", fmt.Errorf("index [%d] out of range (len %d)", t, len(arr))
			}
			cur = arr[t]
		}
	}

	return jsonValueToString(cur), nil
}

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
