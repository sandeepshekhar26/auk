package reporters

import (
	"strings"
	"testing"
	"time"

	"apitool/internal/runner"
)

// sampleSummary is a synthesized run — no live HTTP — covering every shape a
// reporter has to render: a clean pass, a failed check whose message is full
// of XML/JSON metacharacters, and a request that never completed.
func sampleSummary() runner.RunSummary {
	started, _ := time.Parse(time.RFC3339, "2026-08-30T10:00:00Z")
	return runner.RunSummary{
		Target:        "folder f-smoke",
		StartedAt:     started,
		DurationMs:    1234,
		Iterations:    1,
		EnvironmentID: "env-ci",
		Results: []runner.RequestResult{
			{
				Iteration: 1, FolderPath: []string{"Smoke"},
				RequestID: "r-health", RequestName: "health",
				Method: "GET", URL: "https://api.test/health",
				Status: 200, StatusText: "OK", DurationMs: 12, Passed: true,
				Checks: []runner.Check{
					{Name: "status eq 200", Kind: runner.CheckAssertion, Passed: true},
					{Name: "body ok", Kind: runner.CheckTest, Passed: true},
				},
			},
			{
				Iteration: 1, FolderPath: []string{"Smoke", "Deep"},
				RequestID: "r-create", RequestName: `create <user> & "co"`,
				Method: "POST", URL: "https://api.test/users?a=1&b=2",
				Status: 500, StatusText: "Internal Server Error", DurationMs: 31, Passed: false,
				Reason: "1/2 check(s) failed",
				Checks: []runner.Check{
					{Name: "status eq 201", Kind: runner.CheckAssertion, Passed: false,
						Message: `expected 201 but got 500 — body said <error> & "nope"`},
					{Name: "response time < 1s", Kind: runner.CheckAssertion, Passed: true},
				},
			},
			{
				Iteration: 1, FolderPath: nil,
				RequestID: "r-down", RequestName: "unreachable",
				Method: "GET", URL: "https://down.test/",
				Status: 0, DurationMs: 5, Passed: false,
				Reason: `dial tcp: connection refused <"&">`,
				Error:  `dial tcp: connection refused <"&">`,
				Checks: []runner.Check{
					{Name: "request completed", Kind: runner.CheckRequest, Passed: false,
						Message: `dial tcp: connection refused <"&">`},
				},
			},
		},
	}
}

func TestNewAndNames(t *testing.T) {
	for _, name := range Names() {
		r, err := New(name)
		if err != nil {
			t.Fatalf("New(%q) error = %v", name, err)
		}
		if r.Name() != name {
			t.Errorf("New(%q).Name() = %q", name, r.Name())
		}
	}
	if got := strings.Join(Names(), ","); got != "cli,json,junit" {
		t.Errorf("Names() = %q, want a sorted cli,json,junit", got)
	}
	if _, err := New("nope"); err == nil {
		t.Fatal("expected an error for an unknown reporter")
	} else if !strings.Contains(err.Error(), "junit") {
		t.Errorf("error %q should list the available reporters", err)
	}
	// Case/whitespace tolerance: `--reporter JUnit ` should not be a hard error.
	if _, err := New(" JUnit "); err != nil {
		t.Errorf("New(\" JUnit \") error = %v", err)
	}
}
