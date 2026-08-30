// Package reporters turns a runner.RunSummary into the artifacts a CI system
// consumes: JUnit XML (what Jenkins/GitLab/GitHub Actions parse to annotate a
// build), a stable JSON document (for dashboards and custom tooling), and a
// human console summary. Reporters are pure formatters — they never decide
// pass/fail, they only render runner.Verdict's answer, so a report can never
// disagree with the process exit code.
package reporters

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"apitool/internal/runner"
)

// Reporter renders one run summary to one writer.
type Reporter interface {
	// Name is the flag value that selects this reporter (`--reporter junit`).
	Name() string
	// Report writes the summary. A reporter must not mutate the summary.
	Report(w io.Writer, summary runner.RunSummary) error
	// DefaultFile is the conventional filename when a reporter is written to
	// disk without an explicit --reporter-out path; empty means stdout-only
	// by nature (the console reporter).
	DefaultFile() string
}

// registry maps flag value -> constructor. Adding a format is one entry here
// plus one file.
var registry = map[string]func() Reporter{
	"cli":   func() Reporter { return CLI{} },
	"junit": func() Reporter { return JUnit{} },
	"json":  func() Reporter { return JSON{} },
}

// New builds the named reporter.
func New(name string) (Reporter, error) {
	ctor, ok := registry[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("unknown reporter %q (available: %s)", name, strings.Join(Names(), ", "))
	}
	return ctor(), nil
}

// Names lists the available reporter names, sorted, for help text and error
// messages.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// label renders one result's full path — "Folder / Subfolder / Request" —
// with the iteration appended when the run had more than one, so every test
// case in a data-driven run is uniquely identifiable in a CI UI.
func label(r runner.RequestResult, iterations int) string {
	parts := append(append([]string{}, r.FolderPath...), r.RequestName)
	name := strings.Join(parts, " / ")
	if iterations > 1 {
		name = fmt.Sprintf("%s [iteration %d]", name, r.Iteration)
	}
	return name
}

// seconds formats milliseconds as the fractional seconds JUnit's time
// attributes use.
func seconds(ms int64) string {
	return fmt.Sprintf("%.3f", float64(ms)/1000)
}
