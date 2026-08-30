package reporters

import (
	"encoding/json"
	"io"
	"time"

	"apitool/internal/runner"
)

// JSONSchemaVersion is bumped only on a BREAKING change to the JSON report
// shape (a removed or retyped field). New optional fields do not bump it, so
// a consumer can pin `schemaVersion == 1` and keep working.
const JSONSchemaVersion = 1

// JSON renders the whole run as one machine-readable document — the format
// for dashboards, flaky-test trackers, and anything that wants more than
// JUnit's pass/fail (status codes, durations, per-check messages). The shape
// is defined by the structs below rather than by marshalling the runner's
// internal types, so refactoring the runner can't silently break somebody's
// jq pipeline. Documented in docs/09-ci-runner.md.
type JSON struct{}

func (JSON) Name() string        { return "json" }
func (JSON) DefaultFile() string { return "auk-results.json" }

type jsonReport struct {
	Tool          string       `json:"tool"`
	SchemaVersion int          `json:"schemaVersion"`
	Target        string       `json:"target"`
	Environment   string       `json:"environmentId,omitempty"`
	DataFile      string       `json:"dataFile,omitempty"`
	StartedAt     string       `json:"startedAt"`
	DurationMs    int64        `json:"durationMs"`
	Iterations    int          `json:"iterations"`
	Passed        bool         `json:"passed"`
	Bailed        bool         `json:"bailed"`
	Summary       jsonTotals   `json:"summary"`
	Results       []jsonResult `json:"results"`
}

type jsonTotals struct {
	Requests       int `json:"requests"`
	RequestsPassed int `json:"requestsPassed"`
	RequestsFailed int `json:"requestsFailed"`
	Checks         int `json:"checks"`
	ChecksPassed   int `json:"checksPassed"`
	ChecksFailed   int `json:"checksFailed"`
}

type jsonResult struct {
	Iteration   int         `json:"iteration"`
	RequestID   string      `json:"requestId"`
	RequestName string      `json:"requestName"`
	FolderPath  []string    `json:"folderPath"`
	Method      string      `json:"method"`
	URL         string      `json:"url"`
	Status      int         `json:"status"`
	StatusText  string      `json:"statusText,omitempty"`
	DurationMs  int64       `json:"durationMs"`
	Passed      bool        `json:"passed"`
	Reason      string      `json:"reason,omitempty"`
	Error       string      `json:"error,omitempty"`
	ScriptError string      `json:"scriptError,omitempty"`
	Checks      []jsonCheck `json:"checks"`
}

type jsonCheck struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // assertion | test | script | request
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}

func (JSON) Report(w io.Writer, summary runner.RunSummary) error {
	report := jsonReport{
		Tool:          "auk",
		SchemaVersion: JSONSchemaVersion,
		Target:        summary.Target,
		Environment:   summary.EnvironmentID,
		DataFile:      summary.DataFile,
		StartedAt:     summary.StartedAt.UTC().Format(time.RFC3339),
		DurationMs:    summary.DurationMs,
		Iterations:    summary.Iterations,
		Passed:        summary.Passed(),
		Bailed:        summary.Bailed,
		Summary: jsonTotals{
			Requests:       summary.Total(),
			RequestsPassed: summary.PassedCount(),
			RequestsFailed: summary.FailedCount(),
			Checks:         summary.Checks(),
			ChecksPassed:   summary.ChecksPassed(),
			ChecksFailed:   summary.ChecksFailed(),
		},
		Results: make([]jsonResult, 0, len(summary.Results)),
	}

	for _, r := range summary.Results {
		res := jsonResult{
			Iteration:   r.Iteration,
			RequestID:   r.RequestID,
			RequestName: r.RequestName,
			FolderPath:  r.FolderPath,
			Method:      r.Method,
			URL:         r.URL,
			Status:      r.Status,
			StatusText:  r.StatusText,
			DurationMs:  r.DurationMs,
			Passed:      r.Passed,
			Reason:      r.Reason,
			Error:       r.Error,
			ScriptError: r.ScriptError,
			Checks:      make([]jsonCheck, 0, len(r.Checks)),
		}
		if res.FolderPath == nil {
			// Always an array, never null — one less branch for consumers.
			res.FolderPath = []string{}
		}
		for _, c := range r.Checks {
			res.Checks = append(res.Checks, jsonCheck{
				Name:    c.Name,
				Kind:    string(c.Kind),
				Passed:  c.Passed,
				Message: c.Message,
			})
		}
		report.Results = append(report.Results, res)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
