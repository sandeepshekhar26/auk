package model

// AssertionSource selects what part of a response an assertion inspects.
type AssertionSource string

const (
	// AssertStatus checks the HTTP status code.
	AssertStatus AssertionSource = "status"
	// AssertBody checks a JSON-path value extracted from the response body
	// (Path holds the dot/bracket path, e.g. "data.items[0].id").
	AssertBody AssertionSource = "body"
	// AssertHeader checks a response header (Name holds the header name).
	AssertHeader AssertionSource = "header"
	// AssertResponseTime checks the total request time in milliseconds.
	AssertResponseTime AssertionSource = "responseTime"
)

// AssertionOperator is the comparison applied between the extracted actual
// value and Assertion.Value.
type AssertionOperator string

const (
	OpEq       AssertionOperator = "eq"
	OpNeq      AssertionOperator = "neq"
	OpContains AssertionOperator = "contains"
	OpExists   AssertionOperator = "exists"    // Value ignored
	OpNotExist AssertionOperator = "notExists" // Value ignored
	OpLt       AssertionOperator = "lt"        // numeric compare
	OpGt       AssertionOperator = "gt"        // numeric compare
	OpMatches  AssertionOperator = "matches"   // Value is a Go regexp
)

// Assertion is one declarative test on a response — Bruno-style lightweight
// contract testing, saved on the request (git-friendly YAML) so tests are
// versioned with the request they guard. Evaluated identically by the GUI,
// the CLI (non-zero exit on failure — the CI gate), and MCP.
type Assertion struct {
	Source   AssertionSource   `yaml:"source" json:"source"`
	Path     string            `yaml:"path,omitempty" json:"path,omitempty"` // body source
	Name     string            `yaml:"name,omitempty" json:"name,omitempty"` // header source
	Operator AssertionOperator `yaml:"operator" json:"operator"`
	Value    string            `yaml:"value,omitempty" json:"value,omitempty"`
	Enabled  bool              `yaml:"enabled" json:"enabled"`
}

// AssertionResult is the outcome of one assertion against one response.
type AssertionResult struct {
	Assertion Assertion `json:"assertion"`
	Passed    bool      `json:"passed"`
	Actual    string    `json:"actual"`
	Error     string    `json:"error,omitempty"` // evaluation error (bad path/regex), also counts as failed
}
