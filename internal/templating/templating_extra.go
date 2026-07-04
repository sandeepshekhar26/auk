package templating

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// registerExtra adds the remainder of the "Templating & dynamic values"
// function library from docs/01-feature-roadmap.md on top of registerBuiltins.
// Kept in a separate file/method so the MVP core functions and this larger
// surface area are easy to review independently.
func (e *Engine) registerExtra() {
	e.funcs["encode.url"] = func(args []string) (string, error) {
		if len(args) < 1 {
			return "", fmt.Errorf("encode.url requires 1 argument")
		}
		return url.QueryEscape(args[0]), nil
	}

	e.funcs["json.get"] = func(args []string) (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("json.get requires 2 arguments: json.get(jsonStr, path)")
		}
		return jsonGetPath(args[0], args[1])
	}

	e.funcs["regex.match"] = func(args []string) (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("regex.match requires 2 arguments: regex.match(str, pattern)")
		}
		re, err := regexp.Compile(args[1])
		if err != nil {
			return "", fmt.Errorf("regex.match: invalid pattern %q: %w", args[1], err)
		}
		match := re.FindString(args[0])
		if match == "" && !re.MatchString(args[0]) {
			return "", fmt.Errorf("regex.match: pattern %q did not match input", args[1])
		}
		return match, nil
	}

	e.funcs["regex.replace"] = func(args []string) (string, error) {
		if len(args) < 3 {
			return "", fmt.Errorf("regex.replace requires 3 arguments: regex.replace(str, pattern, replacement)")
		}
		re, err := regexp.Compile(args[1])
		if err != nil {
			return "", fmt.Errorf("regex.replace: invalid pattern %q: %w", args[1], err)
		}
		return re.ReplaceAllString(args[0], args[2]), nil
	}

	e.funcs["timestamp.offset"] = func(args []string) (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("timestamp.offset requires 2 arguments: timestamp.offset(unixSeconds, offsetSpec)")
		}
		base, err := parseTimestampArg(args[0])
		if err != nil {
			return "", fmt.Errorf("timestamp.offset: %w", err)
		}
		dur, err := time.ParseDuration(args[1])
		if err != nil {
			return "", fmt.Errorf("timestamp.offset: invalid offset %q: %w", args[1], err)
		}
		return strconv.FormatInt(base.Add(dur).Unix(), 10), nil
	}

	e.funcs["timestamp.format"] = func(args []string) (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("timestamp.format requires 2 arguments: timestamp.format(unixSeconds, goLayout)")
		}
		base, err := parseTimestampArg(args[0])
		if err != nil {
			return "", fmt.Errorf("timestamp.format: %w", err)
		}
		return base.UTC().Format(args[1]), nil
	}

	e.funcs["fs.read"] = func(args []string) (string, error) {
		if len(args) < 1 {
			return "", fmt.Errorf("fs.read requires 1 argument: fs.read(path)")
		}
		b, err := os.ReadFile(args[0])
		if err != nil {
			return "", fmt.Errorf("fs.read: %w", err)
		}
		return string(b), nil
	}

	// cookie(name) is intentionally NOT registered in e.funcs — like
	// response() refs, it needs workspace context that a bare Func closure
	// doesn't receive, so it's special-cased in eval() instead (templating.go),
	// backed by the per-workspace jar in internal/cookiejar.

	// prompt() requires interactive UI to ask the user for input at send
	// time, which the headless engine cannot provide. The GUI would need to
	// intercept this via a separate code path (e.g. a PromptHandler
	// callback on the Templater) before send-time resolution reaches this
	// fallback; that wiring is out of scope here.
	e.funcs["prompt"] = func([]string) (string, error) {
		return "", fmt.Errorf("prompt() requires interactive UI — not supported when running headlessly")
	}
}

// parseTimestampArg accepts either explicit unix seconds or the literal
// "now"/"" for the current time. Plain numeric-seconds is the only form that
// worked before 2026-07-05; "now" is a convenience added then, since the
// `${...}` grammar doesn't support nesting `${timestamp.unix}` as an argument
// to another function — without it, "current time + 1h" was unreachable
// (you'd need an already-known unix timestamp to offset from).
func parseTimestampArg(arg string) (time.Time, error) {
	if arg == "" || strings.EqualFold(arg, "now") {
		return time.Now(), nil
	}
	secs, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid unix seconds %q (or \"now\"): %w", arg, err)
	}
	return time.Unix(secs, 0), nil
}
