package assert

import (
	"encoding/base64"
	"testing"

	"apitool/internal/core/model"
)

func resp(status int, timingMs int64, body string, headers ...model.KeyValue) model.ResponseData {
	return model.ResponseData{
		Status:     status,
		TimingMs:   timingMs,
		BodyBase64: base64.StdEncoding.EncodeToString([]byte(body)),
		Headers:    headers,
	}
}

func TestEvaluate(t *testing.T) {
	r := resp(200, 150,
		`{"user":{"id":42,"name":"jane","tags":["a","b"]},"ok":true}`,
		model.KeyValue{Key: "Content-Type", Value: "application/json; charset=utf-8", Enabled: true},
	)

	tests := []struct {
		name       string
		a          model.Assertion
		wantPassed bool
		wantErr    bool
	}{
		{"status eq pass", model.Assertion{Source: model.AssertStatus, Operator: model.OpEq, Value: "200", Enabled: true}, true, false},
		{"status eq fail", model.Assertion{Source: model.AssertStatus, Operator: model.OpEq, Value: "404", Enabled: true}, false, false},
		{"status lt", model.Assertion{Source: model.AssertStatus, Operator: model.OpLt, Value: "400", Enabled: true}, true, false},
		{"responseTime lt pass", model.Assertion{Source: model.AssertResponseTime, Operator: model.OpLt, Value: "500", Enabled: true}, true, false},
		{"responseTime lt fail", model.Assertion{Source: model.AssertResponseTime, Operator: model.OpLt, Value: "100", Enabled: true}, false, false},
		{"body path eq", model.Assertion{Source: model.AssertBody, Path: "user.name", Operator: model.OpEq, Value: "jane", Enabled: true}, true, false},
		{"body numeric gt", model.Assertion{Source: model.AssertBody, Path: "user.id", Operator: model.OpGt, Value: "10", Enabled: true}, true, false},
		{"body array index", model.Assertion{Source: model.AssertBody, Path: "user.tags[1]", Operator: model.OpEq, Value: "b", Enabled: true}, true, false},
		{"body exists pass", model.Assertion{Source: model.AssertBody, Path: "user.id", Operator: model.OpExists, Enabled: true}, true, false},
		{"body exists fail", model.Assertion{Source: model.AssertBody, Path: "user.missing", Operator: model.OpExists, Enabled: true}, false, false},
		{"body notExists pass", model.Assertion{Source: model.AssertBody, Path: "user.missing", Operator: model.OpNotExist, Enabled: true}, true, false},
		{"body whole-body contains", model.Assertion{Source: model.AssertBody, Operator: model.OpContains, Value: `"ok":true`, Enabled: true}, true, false},
		{"body matches regex", model.Assertion{Source: model.AssertBody, Path: "user.name", Operator: model.OpMatches, Value: "^ja", Enabled: true}, true, false},
		{"body bad regex errors", model.Assertion{Source: model.AssertBody, Path: "user.name", Operator: model.OpMatches, Value: "([", Enabled: true}, false, true},
		{"header contains", model.Assertion{Source: model.AssertHeader, Name: "content-type", Operator: model.OpContains, Value: "application/json", Enabled: true}, true, false},
		{"header exists (case-insensitive)", model.Assertion{Source: model.AssertHeader, Name: "CONTENT-TYPE", Operator: model.OpExists, Enabled: true}, true, false},
		{"header notExists", model.Assertion{Source: model.AssertHeader, Name: "X-Nope", Operator: model.OpNotExist, Enabled: true}, true, false},
		{"missing header comparison fails cleanly", model.Assertion{Source: model.AssertHeader, Name: "X-Nope", Operator: model.OpEq, Value: "x", Enabled: true}, false, true},
		{"non-numeric lt errors", model.Assertion{Source: model.AssertBody, Path: "user.name", Operator: model.OpLt, Value: "5", Enabled: true}, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := Evaluate([]model.Assertion{tt.a}, r)
			if len(results) != 1 {
				t.Fatalf("expected 1 result, got %d", len(results))
			}
			got := results[0]
			if got.Passed != tt.wantPassed {
				t.Errorf("Passed = %v, want %v (actual=%q err=%q)", got.Passed, tt.wantPassed, got.Actual, got.Error)
			}
			if (got.Error != "") != tt.wantErr {
				t.Errorf("Error = %q, wantErr=%v", got.Error, tt.wantErr)
			}
		})
	}
}

func TestEvaluateSkipsDisabledAndEmpty(t *testing.T) {
	r := resp(200, 10, `{}`)
	if got := Evaluate(nil, r); got != nil {
		t.Errorf("nil assertions should yield nil results")
	}
	results := Evaluate([]model.Assertion{
		{Source: model.AssertStatus, Operator: model.OpEq, Value: "500", Enabled: false},
	}, r)
	if len(results) != 0 {
		t.Errorf("disabled assertion should be skipped, got %d results", len(results))
	}
}

func TestAllPassed(t *testing.T) {
	if !AllPassed(nil) {
		t.Errorf("no results should count as all passed")
	}
	if AllPassed([]model.AssertionResult{{Passed: true}, {Passed: false}}) {
		t.Errorf("one failure should fail the set")
	}
}
