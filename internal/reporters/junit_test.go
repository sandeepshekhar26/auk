package reporters

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
	"testing"
	"time"

	"apitool/internal/runner"
)

// The parse-back mirror. Deliberately a SEPARATE set of structs from the
// ones that produced the document, so the test proves the XML is valid and
// carries the right values rather than just round-tripping the writer's own
// assumptions.
type parsedSuites struct {
	XMLName  xml.Name      `xml:"testsuites"`
	Name     string        `xml:"name,attr"`
	Tests    int           `xml:"tests,attr"`
	Failures int           `xml:"failures,attr"`
	Errors   int           `xml:"errors,attr"`
	Time     string        `xml:"time,attr"`
	Suites   []parsedSuite `xml:"testsuite"`
}

type parsedSuite struct {
	Name      string       `xml:"name,attr"`
	Tests     int          `xml:"tests,attr"`
	Failures  int          `xml:"failures,attr"`
	Errors    int          `xml:"errors,attr"`
	Time      string       `xml:"time,attr"`
	Timestamp string       `xml:"timestamp,attr"`
	Cases     []parsedCase `xml:"testcase"`
}

type parsedCase struct {
	Name      string `xml:"name,attr"`
	Classname string `xml:"classname,attr"`
	Time      string `xml:"time,attr"`
	Failure   *struct {
		Message string `xml:"message,attr"`
		Type    string `xml:"type,attr"`
		Text    string `xml:",chardata"`
	} `xml:"failure"`
	Error *struct {
		Message string `xml:"message,attr"`
		Type    string `xml:"type,attr"`
		Text    string `xml:",chardata"`
	} `xml:"error"`
}

func renderJUnit(t *testing.T, summary runner.RunSummary) ([]byte, parsedSuites) {
	t.Helper()
	var buf bytes.Buffer
	if err := (JUnit{}).Report(&buf, summary); err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	var doc parsedSuites
	if err := xml.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("the emitted JUnit XML does not parse: %v\n%s", err, buf.String())
	}
	return buf.Bytes(), doc
}

func TestJUnitRoundTripsAndCounts(t *testing.T) {
	raw, doc := renderJUnit(t, sampleSummary())

	if !bytes.HasPrefix(raw, []byte(xml.Header)) {
		t.Errorf("missing XML declaration; got %q", string(raw[:min(40, len(raw))]))
	}
	if len(doc.Suites) != 3 {
		t.Fatalf("testsuites = %d, want one per request", len(doc.Suites))
	}

	// 2 passing checks + 2 checks on the failing request + 1 error case.
	if doc.Tests != 5 {
		t.Errorf("tests = %d, want 5", doc.Tests)
	}
	if doc.Failures != 1 {
		t.Errorf("failures = %d, want 1", doc.Failures)
	}
	if doc.Errors != 1 {
		t.Errorf("errors = %d, want 1 (the request that never completed)", doc.Errors)
	}
	if doc.Time != "1.234" {
		t.Errorf("time = %q, want seconds with 3 decimals", doc.Time)
	}

	if doc.Suites[0].Name != "Smoke / health" {
		t.Errorf("suite name = %q, want the folder path", doc.Suites[0].Name)
	}
	if doc.Suites[0].Timestamp == "" {
		t.Error("suite timestamp missing")
	}
	if doc.Suites[1].Name != `Smoke / Deep / create <user> & "co"` {
		t.Errorf("suite name = %q — nested folder path or escaping is wrong", doc.Suites[1].Name)
	}
	if doc.Suites[0].Time != "0.012" {
		t.Errorf("suite time = %q, want 0.012", doc.Suites[0].Time)
	}
}

// TestJUnitEscapesMetacharacters is the whole reason for using encoding/xml
// rather than string concatenation: a failure message containing <, & or "
// must survive a parse instead of producing an unreadable report.
func TestJUnitEscapesMetacharacters(t *testing.T) {
	raw, doc := renderJUnit(t, sampleSummary())

	failing := doc.Suites[1]
	if len(failing.Cases) != 2 || failing.Cases[0].Failure == nil {
		t.Fatalf("expected the first case of suite 2 to carry a <failure>: %+v", failing.Cases)
	}
	want := `expected 201 but got 500 — body said <error> & "nope"`
	if got := failing.Cases[0].Failure.Message; got != want {
		t.Errorf("failure message = %q, want %q", got, want)
	}
	if got := failing.Cases[0].Failure.Type; got != "AssertionFailure" {
		t.Errorf("failure type = %q", got)
	}
	if !strings.Contains(failing.Cases[0].Failure.Text, "POST https://api.test/users?a=1&b=2") {
		t.Errorf("failure body should carry the request line, got %q", failing.Cases[0].Failure.Text)
	}
	if failing.Cases[1].Failure != nil {
		t.Error("a passing check must not carry a <failure>")
	}

	// The raw document must not contain an unescaped metacharacter inside an
	// attribute value.
	if bytes.Contains(raw, []byte(`message="expected 201 but got 500 — body said <error>`)) {
		t.Error("the failure message was written into the attribute unescaped")
	}
	if !bytes.Contains(raw, []byte("&lt;error&gt;")) || !bytes.Contains(raw, []byte("&amp;")) {
		t.Error("expected < and & to be XML-escaped in the document")
	}

	errored := doc.Suites[2]
	if len(errored.Cases) != 1 || errored.Cases[0].Error == nil {
		t.Fatalf("a request that never completed must produce an <error>: %+v", errored.Cases)
	}
	if errored.Cases[0].Error.Message != `dial tcp: connection refused <"&">` {
		t.Errorf("error message = %q", errored.Cases[0].Error.Message)
	}
	if errored.Cases[0].Error.Type != "RequestError" {
		t.Errorf("error type = %q", errored.Cases[0].Error.Type)
	}
	// The synthetic "request completed" check must not ALSO appear as a
	// failure — that would double-count the same problem.
	if errored.Failures != 0 {
		t.Errorf("suite failures = %d, want 0 (the failure is reported as an error)", errored.Failures)
	}
}

func TestJUnitAllPassing(t *testing.T) {
	summary := runner.RunSummary{
		Target: "folder ok", Iterations: 1, DurationMs: 100,
		Results: []runner.RequestResult{{
			Iteration: 1, RequestName: "ping", Method: "GET", Status: 200, DurationMs: 4, Passed: true,
			Checks: []runner.Check{{Name: "status eq 200", Kind: runner.CheckAssertion, Passed: true}},
		}},
	}
	_, doc := renderJUnit(t, summary)
	if doc.Failures != 0 || doc.Errors != 0 || doc.Tests != 1 {
		t.Fatalf("tests/failures/errors = %d/%d/%d, want 1/0/0", doc.Tests, doc.Failures, doc.Errors)
	}
	if doc.Suites[0].Cases[0].Classname != "ping" {
		t.Errorf("classname = %q", doc.Suites[0].Cases[0].Classname)
	}
}

// TestJUnitIterationLabels: every test case in a data-driven run must be
// uniquely identifiable, or a CI UI collapses iteration 1 and 2 into one.
func TestJUnitIterationLabels(t *testing.T) {
	summary := runner.RunSummary{
		Target: "folder f", Iterations: 2, DurationMs: 10,
		Results: []runner.RequestResult{
			{Iteration: 1, RequestName: "create", Method: "POST", Status: 201, Passed: true,
				Checks: []runner.Check{{Name: "created", Kind: runner.CheckTest, Passed: true}}},
			{Iteration: 2, RequestName: "create", Method: "POST", Status: 201, Passed: true,
				Checks: []runner.Check{{Name: "created", Kind: runner.CheckTest, Passed: true}}},
		},
	}
	_, doc := renderJUnit(t, summary)
	if doc.Suites[0].Name != "create [iteration 1]" || doc.Suites[1].Name != "create [iteration 2]" {
		t.Fatalf("suite names = %q, %q — iterations must be distinguishable", doc.Suites[0].Name, doc.Suites[1].Name)
	}
}

// TestJUnitCountsAreSelfConsistent: the aggregate attributes must equal the
// sum of the suites, which is what CI dashboards display.
func TestJUnitCountsAreSelfConsistent(t *testing.T) {
	_, doc := renderJUnit(t, sampleSummary())
	tests, failures, errs, cases := 0, 0, 0, 0
	for _, s := range doc.Suites {
		tests += s.Tests
		failures += s.Failures
		errs += s.Errors
		cases += len(s.Cases)
		if s.Tests != len(s.Cases) {
			t.Errorf("suite %q reports %d tests but has %d testcases", s.Name, s.Tests, len(s.Cases))
		}
	}
	if tests != doc.Tests || failures != doc.Failures || errs != doc.Errors {
		t.Fatalf("aggregate %d/%d/%d != sum of suites %d/%d/%d",
			doc.Tests, doc.Failures, doc.Errors, tests, failures, errs)
	}
	if cases != doc.Tests {
		t.Fatalf("%d testcase elements but tests=%d", cases, doc.Tests)
	}
	if _, err := strconv.ParseFloat(doc.Time, 64); err != nil {
		t.Errorf("time attribute %q is not a number", doc.Time)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestJUnitAbortedRunIsNotGreen guards the worst possible reporter output: a
// run that never completed rendering as tests="0" failures="0", which every
// CI system reads as "nothing to report, all good".
func TestJUnitAbortedRunIsNotGreen(t *testing.T) {
	summary := runner.RunSummary{
		Target:    "folder no-such-folder",
		StartedAt: time.Now(),
		Aborted:   `folder "no-such-folder" not found`,
	}
	var buf bytes.Buffer
	if err := (JUnit{}).Report(&buf, summary); err != nil {
		t.Fatalf("Report: %v", err)
	}
	out := buf.String()

	// Parse it back — a reporter that emits unparseable XML is useless.
	var doc struct {
		XMLName  xml.Name `xml:"testsuites"`
		Tests    int      `xml:"tests,attr"`
		Errors   int      `xml:"errors,attr"`
		Failures int      `xml:"failures,attr"`
	}
	if err := xml.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("emitted unparseable XML: %v\n%s", err, out)
	}
	if doc.Tests == 0 || doc.Errors == 0 {
		t.Errorf("an aborted run must report at least one error testcase, got tests=%d errors=%d:\n%s", doc.Tests, doc.Errors, out)
	}
	if !strings.Contains(out, "no-such-folder") {
		t.Errorf("the abort reason should appear in the report:\n%s", out)
	}
}
