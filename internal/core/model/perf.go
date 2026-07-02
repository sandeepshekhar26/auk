package model

// PerfExecutor selects the k6 executor for a load test. v1 supports the two
// most common shapes; constant-arrival-rate and ramping-arrival-rate are
// deferred (the script generator maps these to k6 scenarios).
type PerfExecutor string

const (
	// PerfConstantVUs holds a fixed number of virtual users for a duration.
	PerfConstantVUs PerfExecutor = "constant-vus"
	// PerfRampingVUs ramps VUs up (and optionally down) through stages.
	PerfRampingVUs PerfExecutor = "ramping-vus"
)

// PerfStage is one leg of a ramping-vus test: reach Target VUs over Duration.
type PerfStage struct {
	Duration string `yaml:"duration" json:"duration"` // e.g. "30s", "1m"
	Target   int    `yaml:"target" json:"target"`
}

// PerfThreshold is an SLA gate. Metric is a k6 metric name (http_req_duration,
// http_req_failed, ...); Expression is a k6 threshold expression
// (e.g. "p(95)<500", "rate<0.01"). If any threshold fails, k6 exits 99 and
// the run is marked failed — this is what makes perf tests CI-gateable.
type PerfThreshold struct {
	Metric     string `yaml:"metric" json:"metric"`
	Expression string `yaml:"expression" json:"expression"`
}

// PerfConfig is the load-test definition attached to a request. Saved
// alongside the request (git-friendly YAML) so a load test is versioned with
// the API call it exercises.
type PerfConfig struct {
	Executor PerfExecutor `yaml:"executor" json:"executor"`

	// constant-vus params
	VUs      int    `yaml:"vus,omitempty" json:"vus,omitempty"`
	Duration string `yaml:"duration,omitempty" json:"duration,omitempty"`

	// ramping-vus params
	Stages []PerfStage `yaml:"stages,omitempty" json:"stages,omitempty"`

	Thresholds []PerfThreshold `yaml:"thresholds,omitempty" json:"thresholds,omitempty"`
}

// PerfSamplePoint is one coalesced 1-second bucket of live metrics pushed to
// the UI during a run (docs/02-architecture.md §6 — the backend aggregates;
// the webview only ever sees pre-bucketed series, never per-request points).
type PerfSamplePoint struct {
	TimeOffsetMs int64   `json:"timeOffsetMs"` // ms since run start
	RPS          float64 `json:"rps"`
	P95Ms        float64 `json:"p95Ms"`
	P99Ms        float64 `json:"p99Ms"`
	AvgMs        float64 `json:"avgMs"`
	ErrorRate    float64 `json:"errorRate"` // 0..1 over this bucket
	ActiveVUs    int     `json:"activeVUs"`
}

// PerfResult is the end-of-test report, sourced from k6's handleSummary (the
// authoritative numbers) plus the process exit code (the pass/fail verdict).
type PerfResult struct {
	RequestID string `json:"requestId"`

	Requests      int64   `json:"requests"`
	RPS           float64 `json:"rps"`
	FailRate      float64 `json:"failRate"`
	DurationAvgMs float64 `json:"durationAvgMs"`
	DurationMinMs float64 `json:"durationMinMs"`
	DurationMedMs float64 `json:"durationMedMs"`
	DurationP90Ms float64 `json:"durationP90Ms"`
	DurationP95Ms float64 `json:"durationP95Ms"`
	DurationMaxMs float64 `json:"durationMaxMs"`

	// ThresholdResults maps a threshold expression to whether it passed.
	ThresholdResults []PerfThresholdResult `json:"thresholdResults"`

	// Passed is the overall verdict: true iff k6 exited 0 (no threshold
	// breached). k6 exits 99 when any threshold fails.
	Passed    bool   `json:"passed"`
	ExitCode  int    `json:"exitCode"`
	WallMs    int64  `json:"wallMs"`
	Timestamp string `json:"timestamp"`
	Error     string `json:"error,omitempty"`
}

type PerfThresholdResult struct {
	Metric     string `json:"metric"`
	Expression string `json:"expression"`
	Passed     bool   `json:"passed"`
}
