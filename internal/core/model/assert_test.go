package model

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// A hand-authored assertion that omits `enabled:` must still RUN. Go's bool
// zero value would silently disable it, and a test that never runs reports
// green — false confidence in CI, the worst failure mode for a test tool.
func TestAssertionEnabledDefaultsToTrueWhenAbsent(t *testing.T) {
	var a Assertion
	if err := yaml.Unmarshal([]byte("source: status\noperator: eq\nvalue: \"404\"\n"), &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !a.Enabled {
		t.Error("assertion with no `enabled:` key must default to enabled — a silently-skipped assertion reports a false green")
	}
	if a.Source != AssertStatus || a.Operator != OpEq || a.Value != "404" {
		t.Errorf("other fields mis-decoded: %+v", a)
	}
}

// An EXPLICIT `enabled: false` must still disable it.
func TestAssertionExplicitDisableIsHonored(t *testing.T) {
	var a Assertion
	if err := yaml.Unmarshal([]byte("source: status\noperator: eq\nvalue: \"200\"\nenabled: false\n"), &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a.Enabled {
		t.Error("explicit `enabled: false` must disable the assertion")
	}
}

// The default must survive being decoded as part of a whole RequestDef, which
// is how it actually arrives from the file store.
func TestAssertionEnabledDefaultWithinRequestDef(t *testing.T) {
	const doc = `
id: r1
name: Deliberate 404
method: GET
url: https://example.com/nope
assertions:
  - source: status
    operator: eq
    value: "404"
  - source: status
    operator: neq
    value: "500"
    enabled: false
`
	var req RequestDef
	if err := yaml.Unmarshal([]byte(doc), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(req.Assertions) != 2 {
		t.Fatalf("want 2 assertions, got %d", len(req.Assertions))
	}
	if !req.Assertions[0].Enabled {
		t.Error("assertion without `enabled:` inside a RequestDef must default to enabled")
	}
	if req.Assertions[1].Enabled {
		t.Error("explicit `enabled: false` inside a RequestDef must stay disabled")
	}
}
