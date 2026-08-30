package reporters

import (
	"fmt"
	"io"
	"strings"
	"time"

	"apitool/internal/runner"
)

// CLI is the human-readable console summary: one ✓/✗ line per request, the
// failed checks indented underneath with their messages, and a final tally.
// Deliberately un-colored — CI log viewers mangle ANSI escapes, and the
// ✓/✗ glyphs already carry the signal.
type CLI struct{}

func (CLI) Name() string        { return "cli" }
func (CLI) DefaultFile() string { return "" }

const cliRule = "──────────────────────────────────────────────────────────────"

func (CLI) Report(w io.Writer, summary runner.RunSummary) error {
	var b strings.Builder

	head := []string{"AUK · " + summary.Target}
	if summary.EnvironmentID != "" && summary.EnvironmentID != runner.SyntheticEnvironmentID {
		head = append(head, "env "+summary.EnvironmentID)
	}
	if summary.DataFile != "" {
		head = append(head, fmt.Sprintf("data %s (%d iteration(s))", summary.DataFile, summary.Iterations))
	} else if summary.Iterations > 1 {
		head = append(head, fmt.Sprintf("%d iterations", summary.Iterations))
	}
	fmt.Fprintf(&b, "%s\n\n", strings.Join(head, "  ·  "))

	iteration := 0
	for _, r := range summary.Results {
		if summary.Iterations > 1 && r.Iteration != iteration {
			iteration = r.Iteration
			fmt.Fprintf(&b, "Iteration %d\n", iteration)
		}

		mark := "✓"
		if !r.Passed {
			mark = "✗"
		}
		name := strings.Join(append(append([]string{}, r.FolderPath...), r.RequestName), " / ")
		status := fmt.Sprintf("%s %d", strings.ToUpper(r.Method), r.Status)
		if r.Status == 0 {
			status = strings.ToUpper(r.Method) + " —"
		}
		fmt.Fprintf(&b, "  %s %s  ·  %s  ·  %dms\n", mark, name, status, r.DurationMs)

		if r.Error != "" {
			fmt.Fprintf(&b, "      ! %s\n", r.Error)
		}
		for _, c := range r.Checks {
			if c.Passed {
				continue
			}
			if c.Message != "" {
				fmt.Fprintf(&b, "      ✗ %s — %s\n", c.Name, c.Message)
			} else {
				fmt.Fprintf(&b, "      ✗ %s\n", c.Name)
			}
		}
	}

	if summary.Bailed {
		fmt.Fprintf(&b, "\n  bailed after the first failure (--bail)\n")
	}

	fmt.Fprintf(&b, "\n%s\n", cliRule)
	fmt.Fprintf(&b, "  requests   %d (%d passed, %d failed)\n", summary.Total(), summary.PassedCount(), summary.FailedCount())
	fmt.Fprintf(&b, "  checks     %d (%d passed, %d failed)\n", summary.Checks(), summary.ChecksPassed(), summary.ChecksFailed())
	fmt.Fprintf(&b, "  duration   %s\n", (time.Duration(summary.DurationMs) * time.Millisecond).String())
	if summary.Passed() {
		fmt.Fprintf(&b, "  PASSED\n")
	} else if summary.Total() == 0 {
		fmt.Fprintf(&b, "  FAILED — no requests ran\n")
	} else {
		fmt.Fprintf(&b, "  FAILED — %d of %d request(s) failed\n", summary.FailedCount(), summary.Total())
	}

	_, err := io.WriteString(w, b.String())
	return err
}
