package main

import (
	"encoding/base64"
	"fmt"
	"os"

	"apitool/internal/core/model"
)

// printResponse writes status/timing/body to stdout in the same shape
// regardless of whether the request ultimately errored, so CI logs always
// show what happened before a non-zero exit is raised by the caller.
func printResponse(resp model.ResponseData) {
	if resp.Error != "" {
		fmt.Fprintf(os.Stdout, "error: %s\n", resp.Error)
		return
	}

	fmt.Fprintf(os.Stdout, "status: %d %s\n", resp.Status, resp.StatusText)
	fmt.Fprintf(os.Stdout, "time: %dms\n", resp.TimingMs)
	fmt.Fprintln(os.Stdout, "headers:")
	for _, h := range resp.Headers {
		fmt.Fprintf(os.Stdout, "  %s: %s\n", h.Key, h.Value)
	}

	body, err := base64.StdEncoding.DecodeString(resp.BodyBase64)
	if err != nil {
		fmt.Fprintf(os.Stdout, "body: <undecodable: %s>\n", err)
		return
	}
	fmt.Fprintln(os.Stdout, "body:")
	os.Stdout.Write(body)
	fmt.Fprintln(os.Stdout)

	if len(resp.AssertionResults) > 0 {
		fmt.Fprintln(os.Stdout, "assertions:")
		for _, r := range resp.AssertionResults {
			mark := "✓"
			if !r.Passed {
				mark = "✗"
			}
			desc := assertionLabel(r.Assertion)
			if r.Error != "" {
				fmt.Fprintf(os.Stdout, "  %s %s (%s)\n", mark, desc, r.Error)
			} else {
				fmt.Fprintf(os.Stdout, "  %s %s [actual: %s]\n", mark, desc, r.Actual)
			}
		}
	}
}

// assertionLabel renders an assertion as a compact human-readable line,
// e.g. `body.user.id gt 10` or `status eq 200`.
func assertionLabel(a model.Assertion) string {
	target := string(a.Source)
	switch a.Source {
	case model.AssertBody:
		if a.Path != "" {
			target = "body." + a.Path
		}
	case model.AssertHeader:
		target = "header[" + a.Name + "]"
	}
	if a.Value == "" {
		return fmt.Sprintf("%s %s", target, a.Operator)
	}
	return fmt.Sprintf("%s %s %s", target, a.Operator, a.Value)
}
