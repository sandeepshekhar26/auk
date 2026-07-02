package perf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// collectSink captures emitted perf sample events for assertions.
type collectSink struct {
	mu     sync.Mutex
	events []core.Event
}

func (s *collectSink) Emit(e core.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

func (s *collectSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// requireK6 skips the test when no k6 binary is resolvable, so CI without the
// sidecar doesn't fail — the test is meaningful only with a real k6.
func requireK6(t *testing.T) *Runner {
	t.Helper()
	bin, err := ResolveK6()
	if err != nil {
		t.Skipf("k6 not available: %v", err)
	}
	return NewRunnerWith(bin)
}

func TestRunProducesSummaryAndSamples(t *testing.T) {
	runner := requireK6(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	sink := &collectSink{}
	cfg := model.PerfConfig{
		Executor: model.PerfConstantVUs,
		VUs:      3,
		Duration: "3s",
		Thresholds: []model.PerfThreshold{
			{Metric: "http_req_failed", Expression: "rate<0.01"},
		},
	}
	resolved := core.ResolvedRequest{Method: "GET", URL: srv.URL}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := runner.Run(ctx, "req-1", resolved, cfg, sink)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if res.Requests == 0 {
		t.Errorf("expected some requests, got 0")
	}
	if !res.Passed {
		t.Errorf("expected passing run (exit 0), got exitCode=%d error=%q", res.ExitCode, res.Error)
	}
	if res.DurationP95Ms == 0 {
		t.Errorf("expected a non-zero p95 duration")
	}
	if len(res.ThresholdResults) == 0 {
		t.Errorf("expected threshold results to be reported")
	} else if !res.ThresholdResults[0].Passed {
		t.Errorf("expected http_req_failed threshold to pass against a local 200 server")
	}
	// A 3-second constant-vus run should produce roughly 3 one-second buckets.
	if sink.count() == 0 {
		t.Errorf("expected live perf sample events, got 0")
	}
}

func TestRunThresholdFailureIsReportedNotErrored(t *testing.T) {
	runner := requireK6(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := model.PerfConfig{
		Executor: model.PerfConstantVUs,
		VUs:      2,
		Duration: "2s",
		Thresholds: []model.PerfThreshold{
			{Metric: "http_req_failed", Expression: "rate<0.01"}, // will breach: all 500s
		},
	}
	resolved := core.ResolvedRequest{Method: "GET", URL: srv.URL}

	res, err := runner.Run(context.Background(), "req-2", resolved, cfg, nil)
	// A breached threshold is a valid *result* (exit 99), not a Go error.
	if err != nil {
		t.Fatalf("threshold breach should not be a Go error, got %v", err)
	}
	if res.Passed {
		t.Errorf("expected Passed=false for a breached threshold")
	}
	if res.ExitCode != k6ThresholdExitCode {
		t.Errorf("expected exit code %d, got %d", k6ThresholdExitCode, res.ExitCode)
	}
	if len(res.ThresholdResults) == 0 || res.ThresholdResults[0].Passed {
		t.Errorf("expected the http_req_failed threshold to be reported as failed")
	}
}

func TestGenerateScriptInjectsSafely(t *testing.T) {
	// A URL containing a quote+newline must not break out of the JS string.
	resolved := core.ResolvedRequest{
		Method:  "POST",
		URL:     "https://x.test/'\n+injected",
		Headers: []model.KeyValue{{Key: "X-Test", Value: "a\"b", Enabled: true}},
		Body:    &model.RequestBody{Kind: model.BodyJSON, Text: `{"k":"v\"end"}`},
	}
	cfg := model.PerfConfig{Executor: model.PerfConstantVUs, VUs: 1, Duration: "1s"}

	script, err := GenerateScript(resolved, cfg)
	if err != nil {
		t.Fatalf("GenerateScript() error = %v", err)
	}
	// The raw newline from the URL must have been JSON-escaped, not emitted
	// literally into the script source.
	if strings.Contains(script, "'\n+injected") {
		t.Errorf("URL was not safely escaped into a JS literal:\n%s", script)
	}
	if !strings.Contains(script, "handleSummary") {
		t.Errorf("script missing handleSummary")
	}
}
