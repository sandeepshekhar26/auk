package perf

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// k6ThresholdExitCode is the exit status k6 uses when at least one threshold
// was breached (documented k6 behavior). Any other non-zero code is a real
// execution error, not a threshold verdict.
const k6ThresholdExitCode = 99

// Runner executes k6 load tests. It holds only the resolved k6 binary path;
// all per-run state lives in Run's locals so a Runner is safe to share.
type Runner struct {
	k6Bin string
}

// NewRunner resolves the k6 binary once. A missing binary is an error here so
// the GUI/CLI can surface "install k6" before a user configures a whole test.
func NewRunner() (*Runner, error) {
	bin, err := ResolveK6()
	if err != nil {
		return nil, err
	}
	return &Runner{k6Bin: bin}, nil
}

// NewRunnerWith is the injectable constructor used by tests.
func NewRunnerWith(k6Bin string) *Runner { return &Runner{k6Bin: k6Bin} }

// Run generates a k6 script from the resolved request + config, executes it,
// streams coalesced per-second sample points to sink (Kind "perf"), and
// returns the authoritative end-of-test result. ctx cancellation stops the
// run (k6 receives a kill; the wall clock and partial summary are still
// returned). requestID is echoed into the result for the UI.
func (r *Runner) Run(ctx context.Context, requestID string, resolved core.ResolvedRequest, cfg model.PerfConfig, sink core.EventSink) (model.PerfResult, error) {
	if sink == nil {
		sink = core.NoopSink{}
	}

	script, err := GenerateScript(resolved, cfg)
	if err != nil {
		return model.PerfResult{RequestID: requestID, Error: err.Error()}, err
	}

	scriptPath, cleanup, err := writeTempScript(script)
	if err != nil {
		return model.PerfResult{RequestID: requestID, Error: err.Error()}, err
	}
	defer cleanup()

	// --out json=- streams NDJSON metric points to stdout, interleaved with
	// our handleSummary blob. --quiet drops the progress bar. We deliberately
	// do NOT pass --no-summary: that flag suppresses handleSummary too, which
	// is exactly the delimited JSON blob we rely on. Because our
	// handleSummary returns only {stdout: <blob>}, k6 prints no default text
	// summary anyway — only our blob.
	cmd := exec.CommandContext(ctx, r.k6Bin, "run", "--quiet", "--out", "json=-", scriptPath)
	cmd.Env = append(os.Environ(), "K6_NO_USAGE_REPORT=true")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return model.PerfResult{RequestID: requestID, Error: err.Error()}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return model.PerfResult{RequestID: requestID, Error: err.Error()}, fmt.Errorf("start k6: %w", err)
	}

	summary, streamErr := streamAndBucket(requestID, start, stdout, sink)

	runErr := cmd.Wait()
	wallMs := time.Since(start).Milliseconds()

	exitCode := 0
	if runErr != nil {
		if ee, ok := runErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return model.PerfResult{RequestID: requestID, WallMs: wallMs, Error: runErr.Error()}, fmt.Errorf("k6 run: %w", runErr)
		}
	}

	// exit 0 = all good; exit 99 = a threshold failed (a valid, reportable
	// result, NOT an execution error). Any other non-zero is a real failure.
	if exitCode != 0 && exitCode != k6ThresholdExitCode {
		msg := stderr.String()
		if msg == "" && streamErr != nil {
			msg = streamErr.Error()
		}
		return model.PerfResult{RequestID: requestID, ExitCode: exitCode, WallMs: wallMs, Error: fmt.Sprintf("k6 exited %d: %s", exitCode, msg)},
			fmt.Errorf("k6 exited %d", exitCode)
	}

	result := model.PerfResult{
		RequestID: requestID,
		ExitCode:  exitCode,
		Passed:    exitCode == 0,
		WallMs:    wallMs,
		Timestamp: start.UTC().Format(time.RFC3339),
	}
	if summary != nil {
		result.Requests = summary.Requests
		result.RPS = summary.RPS
		result.FailRate = summary.FailRate
		result.DurationAvgMs = summary.DurationAvgMs
		result.DurationMinMs = summary.DurationMinMs
		result.DurationMedMs = summary.DurationMedMs
		result.DurationP90Ms = summary.DurationP90Ms
		result.DurationP95Ms = summary.DurationP95Ms
		result.DurationMaxMs = summary.DurationMaxMs
		result.ThresholdResults = summary.ThresholdResults
	}
	return result, nil
}

// streamAndBucket reads k6's NDJSON stdout line by line, aggregating
// http_req_duration / http_reqs / http_req_failed points into 1-second
// buckets that are emitted to the sink as they close, and captures the
// delimited handleSummary blob (which arrives mixed into stdout).
func streamAndBucket(requestID string, start time.Time, stdout io.Reader, sink core.EventSink) (*perfSummary, error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var summaryBuf bytes.Buffer
	capturingSummary := false

	agg := newBucketAggregator()

	for scanner.Scan() {
		line := scanner.Bytes()

		// The handleSummary blob is written as one stdout line containing our
		// delimiters; capture it separately from the NDJSON metric stream.
		if capturingSummary || bytes.Contains(line, []byte(summaryStart)) {
			summaryBuf.Write(line)
			summaryBuf.WriteByte('\n')
			if bytes.Contains(line, []byte(summaryEnd)) {
				capturingSummary = false
			} else {
				capturingSummary = true
			}
			continue
		}

		if !bytes.HasPrefix(bytes.TrimSpace(line), []byte("{")) {
			continue
		}

		var pt ndjsonLine
		if err := json.Unmarshal(line, &pt); err != nil || pt.Type != "Point" {
			continue
		}

		bucketIdx := int64(pt.Data.Time.Sub(start).Seconds())
		if bucketIdx < 0 {
			bucketIdx = 0
		}

		// A later bucket index means every second up to it is complete —
		// close and advance through them (emitting only non-empty ones).
		for agg.current >= 0 && bucketIdx > agg.current {
			if p, ok := agg.closeAndAdvance(); ok {
				emitSample(requestID, sink, p)
			}
		}
		agg.add(bucketIdx, pt)
	}

	// Close the final partial bucket.
	if p, ok := agg.closeAndAdvance(); ok {
		emitSample(requestID, sink, p)
	}

	if err := scanner.Err(); err != nil {
		return tryDecodeSummary(summaryBuf.Bytes()), err
	}
	return tryDecodeSummary(summaryBuf.Bytes()), nil
}

func tryDecodeSummary(b []byte) *perfSummary {
	if len(b) == 0 {
		return nil
	}
	s, err := decodeSummary(b)
	if err != nil {
		return nil
	}
	return s
}

func emitSample(requestID string, sink core.EventSink, p model.PerfSamplePoint) {
	payload, err := json.Marshal(p)
	if err != nil {
		return
	}
	sink.Emit(core.Event{SessionID: requestID, Kind: "perf", Direction: "meta", Payload: payload})
}

// ndjsonLine is the subset of a k6 --out json line we care about.
type ndjsonLine struct {
	Metric string `json:"metric"`
	Type   string `json:"type"`
	Data   struct {
		Time  time.Time `json:"time"`
		Value float64   `json:"value"`
		Tags  struct {
			ExpectedResponse string `json:"expected_response"`
		} `json:"tags"`
	} `json:"data"`
}

// bucketAggregator accumulates one 1-second bucket at a time. k6 emits points
// roughly in time order, so a single in-flight bucket (flushed when a later
// second appears) is sufficient and keeps memory flat regardless of run
// length.
type bucketAggregator struct {
	current   int64
	durations []float64
	reqCount  int
	failCount int
	maxVUs    int
}

func newBucketAggregator() *bucketAggregator {
	return &bucketAggregator{current: -1}
}

func (b *bucketAggregator) add(bucketIdx int64, pt ndjsonLine) {
	if b.current < 0 {
		b.current = bucketIdx
	}
	switch pt.Metric {
	case "http_req_duration":
		b.durations = append(b.durations, pt.Data.Value)
	case "http_reqs":
		b.reqCount += int(pt.Data.Value)
	case "http_req_failed":
		if pt.Data.Value >= 1 {
			b.failCount++
		}
	case "vus":
		if v := int(pt.Data.Value); v > b.maxVUs {
			b.maxVUs = v
		}
	}
}

// closeAndAdvance finalizes the current bucket, clears its counters, and
// advances current to the next second. ok is false when the bucket had no
// requests (an idle second, or before the first point arrived) — the caller
// skips emitting those. current is left unchanged at -1 if nothing has been
// added yet, so an all-empty stream terminates the flush loop immediately.
func (b *bucketAggregator) closeAndAdvance() (model.PerfSamplePoint, bool) {
	if b.current < 0 {
		return model.PerfSamplePoint{}, false
	}
	var p model.PerfSamplePoint
	ok := b.reqCount > 0
	if ok {
		p = model.PerfSamplePoint{
			TimeOffsetMs: b.current * 1000,
			RPS:          float64(b.reqCount),
			P95Ms:        percentile(b.durations, 0.95),
			P99Ms:        percentile(b.durations, 0.99),
			AvgMs:        mean(b.durations),
			ErrorRate:    float64(b.failCount) / float64(b.reqCount),
			ActiveVUs:    b.maxVUs,
		}
	}
	b.current++
	b.durations = nil
	b.reqCount = 0
	b.failCount = 0
	b.maxVUs = 0
	return p, ok
}

// writeTempScript writes the generated k6 script to a temp file and returns
// its path plus a cleanup func. k6 needs a file path (it doesn't run scripts
// from stdin for the default executor path we use).
func writeTempScript(script string) (string, func(), error) {
	f, err := os.CreateTemp("", "apitool-k6-*.js")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp script: %w", err)
	}
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, fmt.Errorf("write temp script: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", func() {}, err
	}
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// percentile returns the p-quantile (0..1) via nearest-rank on a copy of xs.
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := make([]float64, len(xs))
	copy(sorted, xs)
	sort.Float64s(sorted)
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
