// Package graphql implements core.Protocol for GraphQL-over-HTTP: a POST
// carrying a JSON body of {"query": "...", "variables": {...}}. It is its
// own package (rather than reusing internal/protocols/http) because request
// shaping is GraphQL-specific — the query lives in resolved.Body.Text and
// variables live in resolved.Body.GraphQLVariables (model.RequestBody), and
// both get folded into a single JSON envelope before the wire request goes
// out, which internal/protocols/http never needs to do.
package graphql

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

type Client struct {
	http *http.Client
}

// New builds a GraphQL protocol client. TLS/proxy/mTLS configuration is
// injected via opts, mirroring internal/protocols/http so every protocol
// shares the same crypto/tls backend (docs/03-tech-stack.md).
func New(opts ...Option) *Client {
	c := &Client{http: &http.Client{Timeout: 60 * time.Second}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

type Option func(*Client)

func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.http.Timeout = d }
}

func WithTransport(t http.RoundTripper) Option {
	return func(c *Client) { c.http.Transport = t }
}

func (c *Client) Kind() model.ProtocolKind { return model.ProtocolGraphQL }

// graphqlEnvelope is the standard GraphQL-over-HTTP request body shape.
type graphqlEnvelope struct {
	Query     string          `json:"query"`
	Variables json.RawMessage `json:"variables,omitempty"`
}

func (c *Client) Execute(ctx context.Context, sess *core.Session, req model.RequestDef, resolved core.ResolvedRequest) (model.ResponseData, error) {
	start := time.Now()

	payload, err := buildEnvelope(resolved.Body)
	if err != nil {
		return model.ResponseData{Error: err.Error()}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, resolved.URL, bytes.NewReader(payload))
	if err != nil {
		return model.ResponseData{Error: err.Error()}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for _, h := range resolved.Headers {
		if h.Enabled {
			httpReq.Header.Add(h.Key, h.Value)
		}
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "graphql", Direction: "sent", Payload: payload})
	}

	httpResp, err := c.http.Do(httpReq)
	timing := time.Since(start).Milliseconds()
	if err != nil {
		return model.ResponseData{
			RequestID: req.ID,
			TimingMs:  timing,
			Timestamp: start.UTC().Format(time.RFC3339),
			Error:     err.Error(),
		}, err
	}
	defer httpResp.Body.Close()

	bodyBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return model.ResponseData{Error: err.Error()}, err
	}

	headers := make([]model.KeyValue, 0, len(httpResp.Header))
	for k, vs := range httpResp.Header {
		for _, v := range vs {
			headers = append(headers, model.KeyValue{Key: k, Value: v, Enabled: true})
		}
	}

	resp := model.ResponseData{
		RequestID:  req.ID,
		Status:     httpResp.StatusCode,
		StatusText: model.ReasonPhrase(httpResp.Status),
		Headers:    headers,
		BodyBase64: base64.StdEncoding.EncodeToString(bodyBytes),
		BodySize:   len(bodyBytes),
		TimingMs:   timing,
		Timestamp:  start.UTC().Format(time.RFC3339),
	}

	if sess != nil && sess.Sink != nil {
		sess.Sink.Emit(core.Event{SessionID: sess.ID, Kind: "graphql", Direction: "received", Payload: bodyBytes})
	}

	return resp, nil
}

// buildEnvelope turns a resolved GraphQL body (query in Text, variables in
// GraphQLVariables) into the JSON wire payload. GraphQLVariables, when
// present, must already be a JSON object; an empty/blank value is treated
// as "no variables" rather than an error, since most queries don't need any.
func buildEnvelope(body *model.RequestBody) ([]byte, error) {
	env := graphqlEnvelope{}
	if body != nil {
		env.Query = body.Text
		if v := body.GraphQLVariables; len(bytesTrimSpace(v)) > 0 {
			if !json.Valid([]byte(v)) {
				return nil, fmt.Errorf("graphql variables is not valid JSON")
			}
			env.Variables = json.RawMessage(v)
		}
	}
	return json.Marshal(env)
}

func bytesTrimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

const introspectionQuery = `query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types {
      ...FullType
    }
    directives {
      name
      description
      locations
      args {
        ...InputValue
      }
    }
  }
}

fragment FullType on __Type {
  kind
  name
  description
  fields(includeDeprecated: true) {
    name
    description
    args {
      ...InputValue
    }
    type {
      ...TypeRef
    }
    isDeprecated
    deprecationReason
  }
  inputFields {
    ...InputValue
  }
  interfaces {
    ...TypeRef
  }
  enumValues(includeDeprecated: true) {
    name
    description
    isDeprecated
    deprecationReason
  }
  possibleTypes {
    ...TypeRef
  }
}

fragment InputValue on __InputValue {
  name
  description
  type { ...TypeRef }
  defaultValue
}

fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType {
                kind
                name
              }
            }
          }
        }
      }
    }
  }
}`

// Introspect POSTs the standard GraphQL introspection query to endpoint and
// returns the raw `{"data": {"__schema": ...}}` JSON response. This is the
// entry point a future schema-explorer UI calls; it is intentionally
// decoupled from core.Protocol/Execute since introspection isn't a
// user-authored request — it's schema metadata fetched on demand.
func Introspect(ctx context.Context, endpoint string) (json.RawMessage, error) {
	return IntrospectWith(ctx, http.DefaultClient, endpoint, nil)
}

// IntrospectWith is like Introspect but lets callers supply their own
// *http.Client (for custom TLS/proxy/timeout) and extra headers (for auth).
func IntrospectWith(ctx context.Context, client *http.Client, endpoint string, headers []model.KeyValue) (json.RawMessage, error) {
	if client == nil {
		client = http.DefaultClient
	}

	payload, err := json.Marshal(graphqlEnvelope{Query: introspectionQuery})
	if err != nil {
		return nil, fmt.Errorf("marshal introspection query: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build introspection request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for _, h := range headers {
		if h.Enabled {
			httpReq.Header.Add(h.Key, h.Value)
		}
	}

	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("introspection request: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read introspection response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("introspection request failed: %s: %s", httpResp.Status, string(body))
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("introspection response is not valid JSON")
	}
	return json.RawMessage(body), nil
}
