// Package assert evaluates a request's declarative assertions against a
// response. It depends only on model + jsonpath so core.Engine can call it
// for every run — GUI, CLI, and MCP get identical verdicts, and the CLI
// turns any failure into a non-zero exit (the CI gate).
package assert

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"apitool/internal/core/model"
	"apitool/internal/jsonpath"
)

// Evaluate runs every enabled assertion against resp. A nil/empty slice in
// returns nil out, so callers can attach the result unconditionally.
func Evaluate(assertions []model.Assertion, resp model.ResponseData) []model.AssertionResult {
	if len(assertions) == 0 {
		return nil
	}
	var results []model.AssertionResult
	for _, a := range assertions {
		if !a.Enabled {
			continue
		}
		results = append(results, evaluateOne(a, resp))
	}
	return results
}

// AllPassed reports whether every result passed — the single bit the CLI
// exit code and MCP verdict key off.
func AllPassed(results []model.AssertionResult) bool {
	for _, r := range results {
		if !r.Passed {
			return false
		}
	}
	return true
}

func evaluateOne(a model.Assertion, resp model.ResponseData) model.AssertionResult {
	res := model.AssertionResult{Assertion: a}

	actual, found, err := extract(a, resp)
	res.Actual = actual

	switch a.Operator {
	case model.OpExists:
		if err != nil && !errors.Is(err, jsonpath.ErrNotFound) {
			res.Error = err.Error()
			return res
		}
		res.Passed = found
		return res
	case model.OpNotExist:
		if err != nil && !errors.Is(err, jsonpath.ErrNotFound) {
			res.Error = err.Error()
			return res
		}
		res.Passed = !found
		return res
	}

	if err != nil {
		res.Error = err.Error()
		return res
	}
	if !found {
		res.Error = "value not found"
		return res
	}

	passed, cmpErr := compare(a.Operator, actual, a.Value)
	if cmpErr != nil {
		res.Error = cmpErr.Error()
		return res
	}
	res.Passed = passed
	return res
}

// extract pulls the actual value an assertion inspects out of the response.
// found=false with a jsonpath.ErrNotFound-ish err means "the thing isn't
// there", which exists/notExists treat as a normal outcome.
func extract(a model.Assertion, resp model.ResponseData) (actual string, found bool, err error) {
	switch a.Source {
	case model.AssertStatus:
		return strconv.Itoa(resp.Status), true, nil

	case model.AssertResponseTime:
		return strconv.FormatInt(resp.TimingMs, 10), true, nil

	case model.AssertHeader:
		if a.Name == "" {
			return "", false, fmt.Errorf("header assertion missing header name")
		}
		for _, h := range resp.Headers {
			if strings.EqualFold(h.Key, a.Name) {
				return h.Value, true, nil
			}
		}
		return "", false, nil

	case model.AssertBody:
		body, decErr := base64.StdEncoding.DecodeString(resp.BodyBase64)
		if decErr != nil {
			return "", false, fmt.Errorf("decode response body: %w", decErr)
		}
		if a.Path == "" {
			// No path = assert against the whole raw body.
			return string(body), true, nil
		}
		v, jErr := jsonpath.Get(string(body), a.Path)
		if jErr != nil {
			if errors.Is(jErr, jsonpath.ErrNotFound) {
				return "", false, jErr
			}
			return "", false, jErr
		}
		return v, true, nil

	default:
		return "", false, fmt.Errorf("unknown assertion source %q", a.Source)
	}
}

func compare(op model.AssertionOperator, actual, expected string) (bool, error) {
	switch op {
	case model.OpEq:
		return actual == expected, nil
	case model.OpNeq:
		return actual != expected, nil
	case model.OpContains:
		return strings.Contains(actual, expected), nil
	case model.OpLt, model.OpGt:
		av, err := strconv.ParseFloat(strings.TrimSpace(actual), 64)
		if err != nil {
			return false, fmt.Errorf("actual %q is not numeric", actual)
		}
		ev, err := strconv.ParseFloat(strings.TrimSpace(expected), 64)
		if err != nil {
			return false, fmt.Errorf("expected %q is not numeric", expected)
		}
		if op == model.OpLt {
			return av < ev, nil
		}
		return av > ev, nil
	case model.OpMatches:
		re, err := regexp.Compile(expected)
		if err != nil {
			return false, fmt.Errorf("invalid regexp %q: %w", expected, err)
		}
		return re.MatchString(actual), nil
	default:
		return false, fmt.Errorf("unknown operator %q", op)
	}
}
