// Package model holds the domain structs shared across storage, protocols,
// auth, templating, and the GUI/CLI/MCP adapters. Field names and yaml/json
// tags here are the source of truth — frontend/src/types.ts mirrors this by
// hand and must be kept in sync.
package model

type ID = string

type Workspace struct {
	ID       ID     `yaml:"id" json:"id"`
	Name     string `yaml:"name" json:"name"`
	OrderKey string `yaml:"orderKey" json:"orderKey"`
}

type Folder struct {
	ID          ID     `yaml:"id" json:"id"`
	WorkspaceID ID     `yaml:"workspaceId" json:"workspaceId"`
	ParentID    *ID    `yaml:"parentId,omitempty" json:"parentId"`
	Name        string `yaml:"name" json:"name"`
	OrderKey    string `yaml:"orderKey" json:"orderKey"`
	// Variables are folder-scoped, inherited by every request in this folder
	// (and its subfolders) as a layer between the workspace Environment and
	// the request itself — see core.Engine.resolveAndAuthorize, which walks
	// a request's folder chain root-first so the CLOSEST folder wins on a
	// name collision, same as the environment being the outermost/weakest layer.
	Variables []KeyValue `yaml:"variables,omitempty" json:"variables"`
}

type ProtocolKind string

const (
	ProtocolHTTP      ProtocolKind = "http"
	ProtocolWebSocket ProtocolKind = "websocket"
	ProtocolGRPC      ProtocolKind = "grpc"
	ProtocolGraphQL   ProtocolKind = "graphql"
	ProtocolSSE       ProtocolKind = "sse"
)

type KeyValue struct {
	Key     string `yaml:"key" json:"key"`
	Value   string `yaml:"value" json:"value"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}

type BodyKind string

const (
	BodyNone    BodyKind = "none"
	BodyJSON    BodyKind = "json"
	BodyText    BodyKind = "text"
	BodyForm    BodyKind = "form"
	BodyBinary  BodyKind = "binary"
	BodyGraphQL BodyKind = "graphql"
)

type RequestBody struct {
	Kind       BodyKind   `yaml:"kind" json:"kind"`
	Text       string     `yaml:"text,omitempty" json:"text,omitempty"`
	FormFields []KeyValue `yaml:"formFields,omitempty" json:"formFields,omitempty"`
	// GraphQLVariables holds the raw JSON object of GraphQL variables for a
	// BodyGraphQL request; Text holds the raw query/mutation string. Kept as
	// a separate field (rather than packed into Text) so the frontend query
	// editor and variables pane can bind to them independently.
	GraphQLVariables string `yaml:"graphqlVariables,omitempty" json:"graphqlVariables,omitempty"`
}

type AuthKind string

const (
	AuthNone     AuthKind = "none"
	AuthBasic    AuthKind = "basic"
	AuthBearer   AuthKind = "bearer"
	AuthAPIKey   AuthKind = "apikey"
	AuthJWT      AuthKind = "jwt"
	AuthOAuth2   AuthKind = "oauth2"
	AuthAWSSigV4 AuthKind = "awsSigV4"
	AuthOAuth1   AuthKind = "oauth1"
	AuthDigest   AuthKind = "digest"
)

type AuthConfig struct {
	Kind     AuthKind      `yaml:"kind" json:"kind"`
	Basic    *BasicAuth    `yaml:"basic,omitempty" json:"basic,omitempty"`
	Bearer   *BearerAuth   `yaml:"bearer,omitempty" json:"bearer,omitempty"`
	APIKey   *APIKeyAuth   `yaml:"apikey,omitempty" json:"apikey,omitempty"`
	JWT      *JWTAuth      `yaml:"jwt,omitempty" json:"jwt,omitempty"`
	OAuth2   *OAuth2Auth   `yaml:"oauth2,omitempty" json:"oauth2,omitempty"`
	AWSSigV4 *AWSSigV4Auth `yaml:"awsSigV4,omitempty" json:"awsSigV4,omitempty"`
	OAuth1   *OAuth1Auth   `yaml:"oauth1,omitempty" json:"oauth1,omitempty"`
	Digest   *DigestAuth   `yaml:"digest,omitempty" json:"digest,omitempty"`
}

// DigestAuth carries HTTP Digest (RFC 7616) credentials. Unlike every other
// auth kind, Digest is a CHALLENGE-RESPONSE handshake: the first attempt gets
// a 401 + WWW-Authenticate, and the reply is computed from that challenge —
// so it is implemented as a transport wrapper in internal/protocols/http
// (digest.go), not via auth.Apply's compute-once-attach-header model.
type DigestAuth struct {
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
}

type BasicAuth struct {
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`
}

type BearerAuth struct {
	Token string `yaml:"token" json:"token"`
}

type APIKeyLocation string

const (
	APIKeyInHeader APIKeyLocation = "header"
	APIKeyInQuery  APIKeyLocation = "query"
)

type APIKeyAuth struct {
	Key   string         `yaml:"key" json:"key"`
	Value string         `yaml:"value" json:"value"`
	In    APIKeyLocation `yaml:"in" json:"in"`
}

type JWTAuth struct {
	Secret    string `yaml:"secret" json:"secret"`
	Algorithm string `yaml:"algorithm" json:"algorithm"`
	Claims    string `yaml:"claims" json:"claims"`
}

// OAuth2 grant types. The zero value means client-credentials, so every
// workspace YAML written before GrantType existed keeps meaning exactly what
// it meant when it was written.
const (
	OAuth2GrantClientCredentials = "client_credentials"
	OAuth2GrantAuthorizationCode = "authorization_code"
)

// OAuth2Auth configures either the client-credentials grant (machine-to-
// machine; ClientID+ClientSecret+TokenURL) or the authorization-code grant
// with PKCE (a person signs in through the system browser; adds AuthURL, and
// ClientSecret becomes optional because a native app is a public client that
// cannot keep one — RFC 8252 §8.4).
//
// The signed-in token itself is never stored here: this struct is versioned
// into plain git-tracked YAML, so tokens live in the OS keychain, keyed by a
// fingerprint of these fields (internal/auth/oauth2_tokens.go).
type OAuth2Auth struct {
	GrantType    string   `yaml:"grantType,omitempty" json:"grantType,omitempty"`
	ClientID     string   `yaml:"clientId" json:"clientId"`
	ClientSecret string   `yaml:"clientSecret,omitempty" json:"clientSecret,omitempty"`
	TokenURL     string   `yaml:"tokenUrl" json:"tokenUrl"`
	// AuthURL is the authorization endpoint the browser is sent to
	// (authorization-code only).
	AuthURL string   `yaml:"authUrl,omitempty" json:"authUrl,omitempty"`
	Scopes  []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
	// Audience is sent as the `audience` parameter on the authorization
	// request. Optional; Auth0 requires it to issue an API access token
	// rather than an opaque userinfo-only token.
	Audience string `yaml:"audience,omitempty" json:"audience,omitempty"`
}

// EffectiveGrantType folds the pre-GrantType zero value onto the grant those
// configs were written for.
func (o OAuth2Auth) EffectiveGrantType() string {
	if o.GrantType == "" {
		return OAuth2GrantClientCredentials
	}
	return o.GrantType
}

// AWSSigV4Auth carries the credentials + scope Signature Version 4 signs
// with. SessionToken is optional (only set for temporary/STS credentials);
// when present it's both sent as X-Amz-Security-Token and included in the
// signature, matching the "add to canonical request" path some AWS services
// require (see internal/auth/auth_sigv4.go).
type AWSSigV4Auth struct {
	// Profile names an AWS CLI profile (~/.aws/config). When set, AUK asks the
	// user's own AWS CLI for credentials at send time rather than using the
	// pasted key pair below — which is the only way SSO and assumed-role
	// setups can work at all, since those credentials are temporary and
	// rotate. A profile takes PRECEDENCE over pasted keys; see
	// internal/auth/aws_profile.go.
	Profile         string `yaml:"profile,omitempty" json:"profile,omitempty"`
	AccessKeyID     string `yaml:"accessKeyId" json:"accessKeyId"`
	SecretAccessKey string `yaml:"secretAccessKey" json:"secretAccessKey"`
	Region          string `yaml:"region" json:"region"`
	Service         string `yaml:"service" json:"service"`
	SessionToken    string `yaml:"sessionToken,omitempty" json:"sessionToken,omitempty"`
}

// OAuth1Auth carries the four credentials RFC 5849 HMAC-SHA1 signing needs.
// Token/TokenSecret are optional (a two-legged / consumer-only flow has no
// access token yet — see internal/auth/auth_oauth1.go); PLAINTEXT/RSA-SHA1
// signature methods aren't supported, only the common HMAC-SHA1 case.
type OAuth1Auth struct {
	ConsumerKey    string `yaml:"consumerKey" json:"consumerKey"`
	ConsumerSecret string `yaml:"consumerSecret" json:"consumerSecret"`
	Token          string `yaml:"token,omitempty" json:"token,omitempty"`
	TokenSecret    string `yaml:"tokenSecret,omitempty" json:"tokenSecret,omitempty"`
}

type RequestDef struct {
	ID          ID     `yaml:"id" json:"id"`
	WorkspaceID ID     `yaml:"workspaceId" json:"workspaceId"`
	FolderID    *ID    `yaml:"folderId,omitempty" json:"folderId"`
	Name        string `yaml:"name" json:"name"`
	// Description is free-form notes/docs for this request (Markdown-ish plain
	// text), shown in the editor and versioned with the request — the place to
	// record what an endpoint does, gotchas, or example values for teammates
	// reading the git-stored collection.
	Description string       `yaml:"description,omitempty" json:"description,omitempty"`
	Protocol    ProtocolKind `yaml:"protocol" json:"protocol"`
	Method      string       `yaml:"method" json:"method"`
	URL         string       `yaml:"url" json:"url"`
	Headers     []KeyValue   `yaml:"headers,omitempty" json:"headers"`
	Params      []KeyValue   `yaml:"params,omitempty" json:"params"`
	// PathParams are values for `:name` path placeholders in URL (e.g.
	// /users/:id). Rows are DERIVED from the URL by the editor rather than
	// added by hand, so Key is always a placeholder name that appears (or
	// recently appeared) in URL and Enabled is not meaningful. Substituted
	// into the URL path by core.applyPathParams at resolve time — after
	// `${...}` templating of both the URL and these values, before auth —
	// and percent-encoded as single path segments. Only the http and
	// graphql protocols get substitution; see applyPathParams for why a
	// gRPC `host:port` target must be left alone.
	PathParams []KeyValue   `yaml:"pathParams,omitempty" json:"pathParams,omitempty"`
	Body       *RequestBody `yaml:"body,omitempty" json:"body"`
	Auth       *AuthConfig  `yaml:"auth,omitempty" json:"authRef"`
	// Perf is the optional load-test config attached to this request, versioned
	// with it in the same YAML file (see internal/core/model/perf.go).
	Perf *PerfConfig `yaml:"perf,omitempty" json:"perf,omitempty"`
	// Assertions are declarative response tests versioned with the request
	// (see internal/core/model/assert.go); evaluated on every run.
	Assertions []Assertion `yaml:"assertions,omitempty" json:"assertions,omitempty"`
	// PreRequestScript is a small JS snippet (run by internal/scripting via
	// sobek) executed after templating+auth but before the Dispatch policy
	// check — so it can add/override headers (computed signatures,
	// idempotency keys) on the SAME request that then passes through the
	// normal chokepoint, never around it. Empty skips scripting entirely.
	PreRequestScript string `yaml:"preRequestScript,omitempty" json:"preRequestScript,omitempty"`
	// PostResponseScript is a JS snippet run AFTER the response arrives. It is
	// where real test suites live: it can assert with test()/expect(), read the
	// parsed response, and — unlike the pre-request script — WRITE variables
	// back to the environment, which is what makes auth-token chaining across
	// a folder run possible. Empty skips it entirely.
	PostResponseScript string `yaml:"postResponseScript,omitempty" json:"postResponseScript,omitempty"`
	// TLS carries optional per-request transport settings (client cert for
	// mTLS, a custom CA, or skip-verify) — orthogonal to Auth, since a
	// request can need a client certificate at the TLS layer independent of
	// whatever Authorization scheme (or none) it also uses. Read directly by
	// internal/protocols/http.Execute (not threaded through
	// core.ResolvedRequest — Execute already receives this RequestDef
	// alongside the resolved one).
	TLS *RequestTLSConfig `yaml:"tls,omitempty" json:"tls,omitempty"`
	// ProxyURL routes this request through a manual HTTP/HTTPS proxy (e.g.
	// "http://user:pass@proxyhost:8080") — independent of TLS/mTLS, same
	// reasoning as TLS being independent of Auth. Empty/nil means no
	// per-request override (Go's default http.ProxyFromEnvironment still
	// applies via internal/protocols/http's shared client).
	ProxyURL *string `yaml:"proxyUrl,omitempty" json:"proxyUrl,omitempty"`
	OrderKey string  `yaml:"orderKey" json:"orderKey"`
}

// RequestTLSConfig is PEM-encoded material, stored the same way other
// per-request credentials are today (plaintext in the request's YAML file,
// like BasicAuth.Password/BearerAuth.Token/AWSSigV4Auth.SecretAccessKey —
// none of those are keychain-routed either; only Environment.Secrets are).
type RequestTLSConfig struct {
	ClientCertPEM      string `yaml:"clientCertPem,omitempty" json:"clientCertPem,omitempty"`
	ClientKeyPEM       string `yaml:"clientKeyPem,omitempty" json:"clientKeyPem,omitempty"`
	CustomCAPEM        string `yaml:"customCaPem,omitempty" json:"customCaPem,omitempty"`
	InsecureSkipVerify bool   `yaml:"insecureSkipVerify,omitempty" json:"insecureSkipVerify,omitempty"`
}

type Environment struct {
	ID          ID         `yaml:"id" json:"id"`
	WorkspaceID ID         `yaml:"workspaceId" json:"workspaceId"`
	Name        string     `yaml:"name" json:"name"`
	Color       *string    `yaml:"color,omitempty" json:"color"`
	Variables   []KeyValue `yaml:"variables,omitempty" json:"variables"`
	// Secrets holds variable NAMES only; values live in the OS keychain,
	// never in the YAML file (docs/02-architecture.md §7 — secrets stay out of git).
	Secrets []string `yaml:"secrets,omitempty" json:"secrets"`
}

type McpTransportKind string

const (
	McpTransportStdio McpTransportKind = "stdio"
	McpTransportHTTP  McpTransportKind = "http"
)

// McpConnection is a developer-configured target MCP server AUK can connect
// to as a CLIENT to debug it — separate from internal/mcpserver, which is
// AUK acting as an MCP *server* exposing ITS OWN tools to Claude. Stdio
// launches Command with Args as a subprocess and speaks JSON-RPC over its
// stdin/stdout (exactly like `claude mcp add` would); HTTP dials URL as a
// Streamable-HTTP MCP endpoint, optionally with a bearer token.
type McpConnection struct {
	ID          ID               `yaml:"id" json:"id"`
	WorkspaceID ID               `yaml:"workspaceId" json:"workspaceId"`
	Name        string           `yaml:"name" json:"name"`
	Transport   McpTransportKind `yaml:"transport" json:"transport"`
	Command     string           `yaml:"command,omitempty" json:"command,omitempty"`
	Args        []string         `yaml:"args,omitempty" json:"args,omitempty"`
	URL         string           `yaml:"url,omitempty" json:"url,omitempty"`
	BearerToken string           `yaml:"bearerToken,omitempty" json:"bearerToken,omitempty"`
}

type ResponseData struct {
	RequestID  ID         `json:"requestId"`
	Status     int        `json:"status"`
	StatusText string     `json:"statusText"`
	Headers    []KeyValue `json:"headers"`
	BodyBase64 string     `json:"bodyBase64"`
	BodySize   int        `json:"bodySize"`
	TimingMs   int64      `json:"timingMs"`
	Timestamp  string     `json:"timestamp"`
	Error      string     `json:"error,omitempty"`
	// AssertionResults holds the outcome of the request's declarative
	// assertions against this response (empty when the request has none).
	AssertionResults []AssertionResult `json:"assertionResults,omitempty"`
	// TestResults holds the outcome of every test() the post-response script
	// declared (empty when the request has no script, or the script declared
	// none). Distinct from AssertionResults: those come from the fixed
	// declarative matrix, these from arbitrary JS — both feed the same
	// pass/fail verdict a CI run reports on.
	TestResults []TestResult `json:"testResults,omitempty"`
	// ScriptLogs is console.* output captured from the post-response script
	// (capped by the scripting runtime). Shown in the response pane so a
	// script can be debugged without a separate console.
	ScriptLogs []string `json:"scriptLogs,omitempty"`
	// ScriptError is set when the post-response script itself failed to run
	// (syntax error, timeout, threw outside a test). A script that cannot run
	// is a FAILED run, not a silently-passing one.
	ScriptError string `json:"scriptError,omitempty"`
	// Timing is the DNS/connect/TLS/TTFB breakdown for the FINAL hop (nil
	// for protocols other than HTTP, which don't go through net/http's
	// RoundTripper). A phase reading 0 means it was legitimately skipped
	// (e.g. TLS on plain HTTP, DNS on a reused connection), not unmeasured.
	Timing *TimingBreakdown `json:"timing,omitempty"`
	// RedirectChain has one entry per hop actually sent, in order, when the
	// request followed one or more redirects (empty otherwise) — lets the
	// debugger show "GET /a -> 302 -> GET /b -> 200" instead of only the
	// final response.
	RedirectChain []RedirectHop `json:"redirectChain,omitempty"`
}

// FolderRunResult is one request's outcome from a batch "run folder" send —
// snapshots RequestName alongside the full response (same convention as
// HistoryEntry) so the aggregate results panel can render a readable list
// without a separate requests lookup.
type FolderRunResult struct {
	RequestID   ID           `json:"requestId"`
	RequestName string       `json:"requestName"`
	Response    ResponseData `json:"response"`
}

// GrpcMethodInfo reports a gRPC method's streaming shape, discovered via
// reflection (internal/protocols/grpc.Client.DescribeMethod) without
// invoking it. The GUI checks this before deciding whether "Send" should
// start a live-stream session or a normal one-shot send, since GrpcEditor
// lets the target method be typed freely (no reflection-based picker yet).
type GrpcMethodInfo struct {
	ClientStreaming bool `json:"clientStreaming"`
	ServerStreaming bool `json:"serverStreaming"`
}

// ReasonPhrase extracts just the reason phrase from Go's http.Response.Status,
// which is the full status line ("200 OK", "404 Not Found"). StatusText is
// meant to hold only the reason ("OK"), so callers that render code + reason
// separately don't end up doubling the code ("200 200 OK"). Everything after
// the first space is the reason; a status with no space is returned as-is.
func ReasonPhrase(fullStatus string) string {
	for i := 0; i < len(fullStatus); i++ {
		if fullStatus[i] == ' ' {
			return fullStatus[i+1:]
		}
	}
	return fullStatus
}

// StreamEvent is one frame of a live WebSocket/SSE session as surfaced to the
// GUI (the engine's internal core.Event is the backend equivalent; this is the
// JSON-tagged transport shape the StreamConsole renders). frontend/src/types.ts
// mirrors this by hand.
type StreamEvent struct {
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"`      // "ws" | "sse" | "grpc" | "perf"
	Direction string `json:"direction"` // "sent" | "received" | "meta"
	Payload   string `json:"payload"`
	Timestamp string `json:"timestamp"`
}

// StreamDrain is the pull-based batch the GUI fetches after a "stream:<id>"
// wake-up: the frames it hasn't seen yet, the cursor to pass next time, and
// whether the session has closed.
type StreamDrain struct {
	Frames []StreamEvent `json:"frames"`
	Cursor int           `json:"cursor"`
	Closed bool          `json:"closed"`
}

// TimingBreakdown is one hop's latency split into the phases net/http's
// httptrace can observe.
type TimingBreakdown struct {
	DNSMs     int64 `json:"dnsMs"`
	ConnectMs int64 `json:"connectMs"`
	TLSMs     int64 `json:"tlsMs"`
	TTFBMs    int64 `json:"ttfbMs"`
	TotalMs   int64 `json:"totalMs"`
}

// RedirectHop records one leg of a redirect chain: the request that was
// actually sent and the status it got back.
type RedirectHop struct {
	Method   string `json:"method"`
	URL      string `json:"url"`
	Status   int    `json:"status"`
	TimingMs int64  `json:"timingMs"`
}

type HistoryEntry struct {
	ID          ID     `json:"id"`
	RequestID   ID     `json:"requestId"`
	RequestName string `json:"requestName"`
	Method      string `json:"method"`
	URL         string `json:"url"`
	Status      int    `json:"status"`
	TimingMs    int64  `json:"timingMs"`
	Timestamp   string `json:"timestamp"`
}
