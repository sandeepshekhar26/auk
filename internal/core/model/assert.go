package model

import "gopkg.in/yaml.v3"

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

// TestResult is one named check declared by a post-response script via
// test("name", fn). A script can declare many; each is reported
// independently so a CI reporter can render them as individual test cases
// (JUnit <testcase>, etc.) rather than one opaque pass/fail per request.
type TestResult struct {
	Name   string `yaml:"name" json:"name"`
	Passed bool   `yaml:"passed" json:"passed"`
	// Error carries the assertion message for a failed test (or the thrown
	// error's message), empty when Passed.
	Error string `yaml:"error,omitempty" json:"error,omitempty"`
}

// Passed reports whether every assertion AND every script test succeeded, and
// no script error occurred — the single verdict a folder run or CI exit code
// is derived from. A response with no checks at all counts as passed (there
// was nothing to fail), so callers that need "was anything actually checked"
// should look at the slice lengths.
func (r ResponseData) Passed() bool {
	if r.ScriptError != "" {
		return false
	}
	for _, a := range r.AssertionResults {
		if !a.Passed {
			return false
		}
	}
	for _, t := range r.TestResults {
		if !t.Passed {
			return false
		}
	}
	return true
}

// UnmarshalYAML makes Enabled default to TRUE when the key is absent.
//
// Go's zero value for bool is false, so a hand-authored or generated
// assertion that omits `enabled:` would silently never run — and a test that
// never runs still reports GREEN, which is the single worst failure mode a
// test tool can have (false confidence in CI). Anyone who bothered to write
// an assertion meant it to run; only an explicit `enabled: false` disables
// it. The GUI always writes the key explicitly, so round-tripped data is
// unaffected.
func (a *Assertion) UnmarshalYAML(node *yaml.Node) error {
	// Alias type to avoid recursing into this method.
	type rawAssertion Assertion
	raw := rawAssertion{Enabled: true}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	*a = Assertion(raw)
	return nil
}
