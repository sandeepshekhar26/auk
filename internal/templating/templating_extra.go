package templating

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
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
		secs, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return "", fmt.Errorf("timestamp.offset: invalid unix seconds %q: %w", args[0], err)
		}
		dur, err := time.ParseDuration(args[1])
		if err != nil {
			return "", fmt.Errorf("timestamp.offset: invalid offset %q: %w", args[1], err)
		}
		return strconv.FormatInt(time.Unix(secs, 0).Add(dur).Unix(), 10), nil
	}

	e.funcs["timestamp.format"] = func(args []string) (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("timestamp.format requires 2 arguments: timestamp.format(unixSeconds, goLayout)")
		}
		secs, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			return "", fmt.Errorf("timestamp.format: invalid unix seconds %q: %w", args[0], err)
		}
		return time.Unix(secs, 0).UTC().Format(args[1]), nil
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

	// cookie() needs a cookie jar wired into the HTTP client
	// (internal/protocols/http/http.go currently constructs
	// &http.Client{Timeout: ...} with no CookieJar), so there is nowhere to
	// read a cookie value from yet. Fail loudly rather than silently
	// returning "".
	e.funcs["cookie"] = func([]string) (string, error) {
		return "", fmt.Errorf("cookie(): cookie jar not wired yet")
	}

	// prompt() requires interactive UI to ask the user for input at send
	// time, which the headless engine cannot provide. The GUI would need to
	// intercept this via a separate code path (e.g. a PromptHandler
	// callback on the Templater) before send-time resolution reaches this
	// fallback; that wiring is out of scope here.
	e.funcs["prompt"] = func([]string) (string, error) {
		return "", fmt.Errorf("prompt() requires interactive UI — not supported when running headlessly")
	}
}
