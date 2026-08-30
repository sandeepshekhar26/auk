package reporters

import (
	"bytes"
	"strings"
	"testing"

	"apitool/internal/runner"
)

func renderCLI(t *testing.T, summary runner.RunSummary) string {
	t.Helper()
	var buf bytes.Buffer
	if err := (CLI{}).Report(&buf, summary); err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	return buf.String()
}

func TestCLIReportFormatting(t *testing.T) {
	out := renderCLI(t, sampleSummary())

	wantLines := []string{
		"AUK · folder f-smoke  ·  env env-ci",
		"  ✓ Smoke / health  ·  GET 200  ·  12ms",
		`  ✗ Smoke / Deep / create <user> & "co"  ·  POST 500  ·  31ms`,
		`      ✗ status eq 201 — expected 201 but got 500 — body said <error> & "nope"`,
		"  ✗ unreachable  ·  GET —  ·  5ms",
		`      ! dial tcp: connection refused <"&">`,
		"  requests   3 (1 passed, 2 failed)",
		"  checks     5 (3 passed, 2 failed)",
		"  duration   1.234s",
		"  FAILED — 2 of 3 request(s) failed",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Errorf("output missing line:\n  %s\n--- full output ---\n%s", want, out)
		}
	}

	// A passing check must NOT be listed — only failures are indented under
	// their request, so a green run stays scannable.
	if strings.Contains(out, "✗ response time < 1s") {
		t.Error("a passing check was rendered as a failure")
	}
	if strings.Contains(out, "PASSED") {
		t.Error("a failing run must not print PASSED")
	}
}

func TestCLIReportPassingRun(t *testing.T) {
	summary := runner.RunSummary{
		Target: "folder ok", Iterations: 1, DurationMs: 250,
		Results: []runner.RequestResult{{
			Iteration: 1, FolderPath: []string{"Smoke"}, RequestName: "ping",
			Method: "GET", Status: 200, DurationMs: 9, Passed: true,
			Checks: []runner.Check{{Name: "status eq 200", Kind: runner.CheckAssertion, Passed: true}},
		}},
	}
	out := renderCLI(t, summary)

	if !strings.Contains(out, "  ✓ Smoke / ping  ·  GET 200  ·  9ms") {
		t.Errorf("missing the ✓ line:\n%s", out)
	}
	if !strings.Contains(out, "  PASSED") {
		t.Errorf("missing the PASSED verdict:\n%s", out)
	}
	if !strings.Contains(out, "  duration   250ms") {
		t.Errorf("duration not humanized:\n%s", out)
	}
	if strings.Contains(out, "✗") {
		t.Errorf("a fully passing run should contain no ✗:\n%s", out)
	}
}

func TestCLIReportIterationHeadings(t *testing.T) {
	summary := runner.RunSummary{
		Target: "folder f", Iterations: 2, DurationMs: 20, DataFile: "users.csv",
		Results: []runner.RequestResult{
			{Iteration: 1, RequestName: "create", Method: "POST", Status: 201, Passed: true,
				Checks: []runner.Check{{Name: "created", Passed: true}}},
			{Iteration: 2, RequestName: "create", Method: "POST", Status: 201, Passed: true,
				Checks: []runner.Check{{Name: "created", Passed: true}}},
		},
	}
	out := renderCLI(t, summary)

	if !strings.Contains(out, "data users.csv (2 iteration(s))") {
		t.Errorf("header should name the data file:\n%s", out)
	}
	if !strings.Contains(out, "Iteration 1\n") || !strings.Contains(out, "Iteration 2\n") {
		t.Errorf("iterations should be separated by headings:\n%s", out)
	}
	if strings.Count(out, "Iteration 1") != 1 {
		t.Errorf("iteration heading repeated:\n%s", out)
	}
}

func TestCLIReportBailNotice(t *testing.T) {
	summary := runner.RunSummary{
		Target: "folder f", Iterations: 1, DurationMs: 5, Bailed: true,
		Results: []runner.RequestResult{{
			Iteration: 1, RequestName: "boom", Method: "GET", Status: 500, Passed: false,
			Reason: "HTTP 500",
			Checks: []runner.Check{{Name: "request completed", Kind: runner.CheckRequest, Passed: false, Message: "HTTP 500"}},
		}},
	}
	out := renderCLI(t, summary)
	if !strings.Contains(out, "bailed after the first failure (--bail)") {
		t.Errorf("a bailed run should say so:\n%s", out)
	}
}

func TestCLIReportEmptyRun(t *testing.T) {
	out := renderCLI(t, runner.RunSummary{Target: "folder empty"})
	if !strings.Contains(out, "FAILED — no requests ran") {
		t.Errorf("an empty run must not look like a pass:\n%s", out)
	}
}

// TestCLIReportHidesSyntheticEnvironment: the reserved data-iteration
// environment id is an implementation detail and must never be printed as
// though the user had selected it.
func TestCLIReportHidesSyntheticEnvironment(t *testing.T) {
	summary := sampleSummary()
	summary.EnvironmentID = runner.SyntheticEnvironmentID
	if out := renderCLI(t, summary); strings.Contains(out, runner.SyntheticEnvironmentID) {
		t.Errorf("synthetic environment id leaked into the report:\n%s", out)
	}
}
