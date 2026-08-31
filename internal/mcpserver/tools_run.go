package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"apitool/internal/core/model"
	"apitool/internal/runner"
)

// ---- run_folder ---------------------------------------------------------

type runFolderIn struct {
	FolderID      string `json:"folderId" jsonschema:"the id of the folder to run; every request in it and its subfolders"`
	EnvironmentID string `json:"environmentId,omitempty" jsonschema:"optional environment id to resolve variables against"`
	Bail          bool   `json:"bail,omitempty" jsonschema:"stop at the first failing request instead of running the whole folder"`
}

type folderResult struct {
	RequestID string `json:"requestId"`
	Name      string `json:"name"`
	Status    int    `json:"status,omitempty"`
	Passed    bool   `json:"passed"`
	// Reason explains a FAILURE in one line (a transport error, a failed
	// assertion, a script that could not run). Empty on success.
	Reason   string `json:"reason,omitempty"`
	TimingMs int64  `json:"timingMs"`
}

type runFolderOut struct {
	Target     string         `json:"target"`
	Passed     bool           `json:"passed"`
	Total      int            `json:"total"`
	PassedCnt  int            `json:"passedCount"`
	FailedCnt  int            `json:"failedCount"`
	DurationMs int64          `json:"durationMs"`
	Bailed     bool           `json:"bailed,omitempty"`
	// Aborted is set when the RUN did not complete (unknown folder,
	// cancellation) as opposed to completing with failures. An agent must not
	// read an aborted run as "0 failures".
	Aborted string         `json:"aborted,omitempty"`
	Results []folderResult `json:"results"`
}

// runFolder executes a whole folder as a suite — the same path `auk run-folder`
// takes in CI, through the same engine and the same Dispatch policy, so an
// agent asking "does the smoke suite pass?" gets the answer CI would give
// rather than a separate approximation of it.
func (h *handlers) runFolder(ctx context.Context, _ *mcp.CallToolRequest, in runFolderIn) (*mcp.CallToolResult, runFolderOut, error) {
	if in.FolderID == "" {
		return nil, runFolderOut{}, fmt.Errorf("folderId is required")
	}
	rs, ok := h.store.(runner.Store)
	if !ok {
		return nil, runFolderOut{}, fmt.Errorf("this server's store cannot drive folder runs")
	}

	summary, err := runner.RunFolder(ctx, h.engine, rs, model.ID(in.FolderID), runner.Options{
		Target:        runner.Target{Kind: runner.TargetFolder, ID: model.ID(in.FolderID)},
		EnvironmentID: model.ID(in.EnvironmentID),
		Bail:          in.Bail,
		Origin:        "mcp",
	})
	if err != nil {
		return nil, runFolderOut{}, err
	}

	out := runFolderOut{
		Target:     summary.Target,
		Passed:     summary.Passed(),
		Total:      summary.Total(),
		PassedCnt:  summary.PassedCount(),
		FailedCnt:  summary.FailedCount(),
		DurationMs: summary.DurationMs,
		Bailed:     summary.Bailed,
		Aborted:    summary.Aborted,
		Results:    make([]folderResult, 0, len(summary.Results)),
	}
	for _, r := range summary.Results {
		out.Results = append(out.Results, folderResult{
			RequestID: string(r.RequestID),
			Name:      r.RequestName,
			Status:    r.Response.Status,
			Passed:    r.Passed,
			Reason:    r.Reason,
			TimingMs:  r.Response.TimingMs,
		})
	}
	return nil, out, nil
}
