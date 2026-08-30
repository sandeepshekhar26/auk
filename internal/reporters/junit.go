package reporters

import (
	"encoding/xml"
	"fmt"
	"io"
	"time"

	"apitool/internal/runner"
)

// JUnit renders the run as JUnit XML — the CI lingua franca. Jenkins
// (junit step), GitLab (artifacts:reports:junit), GitHub Actions (every
// test-reporter action), CircleCI and Bitbucket all parse this shape:
//
//	<testsuites>            the whole run
//	  <testsuite>           one request (one iteration of it)
//	    <testcase>          one assertion or one script test()
//	      <failure>         a check that failed
//	      <error>           the request never completed (transport/engine)
//
// One testcase per CHECK (not per request) is what makes a CI failure
// readable: the build annotation says `status eq 200` failed, not "request 3
// failed". A request that declares no checks still emits one synthetic
// testcase (runner.CheckRequest), so a bare smoke-test folder can still turn
// a build red instead of reporting zero tests.
type JUnit struct{}

func (JUnit) Name() string        { return "junit" }
func (JUnit) DefaultFile() string { return "auk-results.xml" }

type junitTestsuites struct {
	XMLName  xml.Name     `xml:"testsuites"`
	Name     string       `xml:"name,attr"`
	Tests    int          `xml:"tests,attr"`
	Failures int          `xml:"failures,attr"`
	Errors   int          `xml:"errors,attr"`
	Time     string       `xml:"time,attr"`
	Suites   []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	Name      string      `xml:"name,attr"`
	Tests     int         `xml:"tests,attr"`
	Failures  int         `xml:"failures,attr"`
	Errors    int         `xml:"errors,attr"`
	Time      string      `xml:"time,attr"`
	Timestamp string      `xml:"timestamp,attr,omitempty"`
	Cases     []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
	Error     *junitError   `xml:"error,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

type junitError struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Text    string `xml:",chardata"`
}

// Report writes the XML document. Every attribute and body value goes
// through encoding/xml, so a failure message containing <, & or " is escaped
// correctly rather than producing an unparseable report (the classic
// hand-rolled-XML bug).
func (j JUnit) Report(w io.Writer, summary runner.RunSummary) error {
	doc := junitTestsuites{
		Name: "AUK — " + summary.Target,
		Time: seconds(summary.DurationMs),
	}

	for _, r := range summary.Results {
		classname := label(r, summary.Iterations)
		suite := junitSuite{
			Name:      classname,
			Time:      seconds(r.DurationMs),
			Timestamp: summary.StartedAt.UTC().Format(time.RFC3339),
		}

		// A request that never produced a response is an ERROR (the test
		// could not run), which CI systems report differently from a
		// failing assertion.
		if r.Error != "" {
			suite.Cases = append(suite.Cases, junitCase{
				Name:      "request completed",
				Classname: classname,
				Time:      seconds(r.DurationMs),
				Error: &junitError{
					Message: r.Error,
					Type:    "RequestError",
					Text:    fmt.Sprintf("%s %s\n%s", r.Method, r.URL, r.Error),
				},
			})
			suite.Errors++
			suite.Tests++
		}

		for _, c := range r.Checks {
			if r.Error != "" && c.Kind == runner.CheckRequest {
				// Already reported as <error> above; don't double-count.
				continue
			}
			tc := junitCase{
				Name:      c.Name,
				Classname: classname,
				Time:      seconds(r.DurationMs),
			}
			if !c.Passed {
				tc.Failure = &junitFailure{
					Message: c.Message,
					Type:    failureType(c.Kind),
					Text:    fmt.Sprintf("%s %s\nstatus %d\n%s", r.Method, r.URL, r.Status, c.Message),
				}
				suite.Failures++
			}
			suite.Cases = append(suite.Cases, tc)
			suite.Tests++
		}

		doc.Tests += suite.Tests
		doc.Failures += suite.Failures
		doc.Errors += suite.Errors
		doc.Suites = append(doc.Suites, suite)
	}

	// A run that could not COMPLETE (unknown target, malformed data row
	// partway through, cancellation) gets an explicit error suite. Without
	// it an aborted run renders as `tests="0" failures="0"` — a document a
	// CI system reads as "nothing to report, all good", which is the single
	// most dangerous output a test reporter can produce. The exit code is
	// already non-zero; the REPORT must agree with it.
	if summary.Aborted != "" {
		suite := junitSuite{
			Name:      "AUK run",
			Timestamp: summary.StartedAt.UTC().Format(time.RFC3339),
			Tests:     1,
			Errors:    1,
			Cases: []junitCase{{
				Name:      "run completed",
				Classname: "AUK run",
				Error: &junitError{
					Message: "the run did not complete: " + summary.Aborted,
					Type:    "RunAborted",
					Text:    summary.Aborted,
				},
			}},
		}
		doc.Tests += suite.Tests
		doc.Errors += suite.Errors
		doc.Suites = append(doc.Suites, suite)
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Flush(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func failureType(kind runner.CheckKind) string {
	switch kind {
	case runner.CheckAssertion:
		return "AssertionFailure"
	case runner.CheckTest:
		return "TestFailure"
	case runner.CheckScript:
		return "ScriptError"
	default:
		return "RequestFailure"
	}
}
