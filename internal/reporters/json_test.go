package reporters

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"apitool/internal/runner"
)

func renderJSON(t *testing.T, summary runner.RunSummary) (string, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if err := (JSON{}).Report(&buf, summary); err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("emitted JSON does not parse: %v\n%s", err, buf.String())
	}
	return buf.String(), doc
}

func TestJSONShape(t *testing.T) {
	raw, doc := renderJSON(t, sampleSummary())

	if doc["tool"] != "auk" {
		t.Errorf("tool = %v", doc["tool"])
	}
	if doc["schemaVersion"].(float64) != float64(JSONSchemaVersion) {
		t.Errorf("schemaVersion = %v, want %d", doc["schemaVersion"], JSONSchemaVersion)
	}
	if doc["target"] != "folder f-smoke" {
		t.Errorf("target = %v", doc["target"])
	}
	if doc["environmentId"] != "env-ci" {
		t.Errorf("environmentId = %v", doc["environmentId"])
	}
	if doc["startedAt"] != "2026-08-30T10:00:00Z" {
		t.Errorf("startedAt = %v, want RFC3339 UTC", doc["startedAt"])
	}
	if doc["durationMs"].(float64) != 1234 {
		t.Errorf("durationMs = %v", doc["durationMs"])
	}
	if doc["passed"].(bool) {
		t.Error("passed = true for a summary with failures")
	}
	if doc["bailed"].(bool) {
		t.Error("bailed = true when the run was not bailed")
	}

	summary, ok := doc["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary is not an object: %T", doc["summary"])
	}
	wantTotals := map[string]float64{
		"requests": 3, "requestsPassed": 1, "requestsFailed": 2,
		"checks": 5, "checksPassed": 3, "checksFailed": 2,
	}
	for key, want := range wantTotals {
		got, ok := summary[key].(float64)
		if !ok {
			t.Errorf("summary.%s missing", key)
			continue
		}
		if got != want {
			t.Errorf("summary.%s = %v, want %v", key, got, want)
		}
	}

	results, ok := doc["results"].([]any)
	if !ok || len(results) != 3 {
		t.Fatalf("results = %v, want 3 entries", doc["results"])
	}
	first := results[0].(map[string]any)
	for _, key := range []string{"iteration", "requestId", "requestName", "folderPath", "method", "url", "status", "durationMs", "passed", "checks"} {
		if _, ok := first[key]; !ok {
			t.Errorf("result is missing %q", key)
		}
	}
	if path, ok := first["folderPath"].([]any); !ok || len(path) != 1 || path[0] != "Smoke" {
		t.Errorf("folderPath = %v", first["folderPath"])
	}
	// An unfoldered request must serialize [] rather than null, so consumers
	// need no null check.
	third := results[2].(map[string]any)
	if path, ok := third["folderPath"].([]any); !ok || len(path) != 0 {
		t.Errorf("folderPath for an unfoldered request = %v, want []", third["folderPath"])
	}
	if strings.Contains(raw, `"folderPath": null`) {
		t.Error("folderPath serialized as null")
	}

	checks := first["checks"].([]any)
	if len(checks) != 2 {
		t.Fatalf("checks = %v", checks)
	}
	check := checks[0].(map[string]any)
	if check["name"] != "status eq 200" || check["kind"] != "assertion" || check["passed"] != true {
		t.Errorf("check = %v", check)
	}
	if _, present := check["message"]; present {
		t.Error("a passing check should omit the message field")
	}

	// Metacharacters survive JSON encoding untouched.
	second := results[1].(map[string]any)
	if second["requestName"] != `create <user> & "co"` {
		t.Errorf("requestName = %v", second["requestName"])
	}
	failed := second["checks"].([]any)[0].(map[string]any)
	if failed["message"] != `expected 201 but got 500 — body said <error> & "nope"` {
		t.Errorf("message = %v", failed["message"])
	}
	if second["reason"] != "1/2 check(s) failed" {
		t.Errorf("reason = %v", second["reason"])
	}
	if third["error"] == nil || !strings.Contains(third["error"].(string), "connection refused") {
		t.Errorf("error = %v", third["error"])
	}
}

func TestJSONIsIndentedAndNewlineTerminated(t *testing.T) {
	raw, _ := renderJSON(t, sampleSummary())
	if !strings.HasSuffix(raw, "\n") {
		t.Error("report should end with a newline")
	}
	if !strings.Contains(raw, "\n  \"tool\": \"auk\"") {
		t.Error("report should be indented for human diffing in CI artifacts")
	}
}

func TestJSONPassingRun(t *testing.T) {
	summary := runner.RunSummary{
		Target: "request r1", Iterations: 1, DurationMs: 8,
		Results: []runner.RequestResult{{
			Iteration: 1, RequestID: "r1", RequestName: "ping", Method: "GET", Status: 200, Passed: true,
			Checks: []runner.Check{{Name: "request completed", Kind: runner.CheckRequest, Passed: true}},
		}},
	}
	_, doc := renderJSON(t, summary)
	if !doc["passed"].(bool) {
		t.Error("passed = false for an all-green run")
	}
}
