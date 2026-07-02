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
	AuthNone   AuthKind = "none"
	AuthBasic  AuthKind = "basic"
	AuthBearer AuthKind = "bearer"
	AuthAPIKey AuthKind = "apikey"
	AuthJWT    AuthKind = "jwt"
	AuthOAuth2 AuthKind = "oauth2"
)

type AuthConfig struct {
	Kind   AuthKind    `yaml:"kind" json:"kind"`
	Basic  *BasicAuth  `yaml:"basic,omitempty" json:"basic,omitempty"`
	Bearer *BearerAuth `yaml:"bearer,omitempty" json:"bearer,omitempty"`
	APIKey *APIKeyAuth `yaml:"apikey,omitempty" json:"apikey,omitempty"`
	JWT    *JWTAuth    `yaml:"jwt,omitempty" json:"jwt,omitempty"`
	OAuth2 *OAuth2Auth `yaml:"oauth2,omitempty" json:"oauth2,omitempty"`
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

// OAuth2Auth is the client-credentials grant only; full authorization-code
// with a system-browser redirect is out of scope (see internal/auth).
type OAuth2Auth struct {
	ClientID     string   `yaml:"clientId" json:"clientId"`
	ClientSecret string   `yaml:"clientSecret" json:"clientSecret"`
	TokenURL     string   `yaml:"tokenUrl" json:"tokenUrl"`
	Scopes       []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
}

type RequestDef struct {
	ID          ID           `yaml:"id" json:"id"`
	WorkspaceID ID           `yaml:"workspaceId" json:"workspaceId"`
	FolderID    *ID          `yaml:"folderId,omitempty" json:"folderId"`
	Name        string       `yaml:"name" json:"name"`
	Protocol    ProtocolKind `yaml:"protocol" json:"protocol"`
	Method      string       `yaml:"method" json:"method"`
	URL         string       `yaml:"url" json:"url"`
	Headers     []KeyValue   `yaml:"headers,omitempty" json:"headers"`
	Params      []KeyValue   `yaml:"params,omitempty" json:"params"`
	Body        *RequestBody `yaml:"body,omitempty" json:"body"`
	Auth        *AuthConfig  `yaml:"auth,omitempty" json:"authRef"`
	// Perf is the optional load-test config attached to this request, versioned
	// with it in the same YAML file (see internal/core/model/perf.go).
	Perf *PerfConfig `yaml:"perf,omitempty" json:"perf,omitempty"`
	// Assertions are declarative response tests versioned with the request
	// (see internal/core/model/assert.go); evaluated on every run.
	Assertions []Assertion `yaml:"assertions,omitempty" json:"assertions,omitempty"`
	OrderKey   string      `yaml:"orderKey" json:"orderKey"`
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
