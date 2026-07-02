// Package importer round-trips individual requests to and from cURL command
// strings ("Paste cURL" / "Copy as cURL" — docs/01-feature-roadmap.md). It
// only knows about model.RequestDef and core.ResolvedRequest; it never
// touches the engine, templater, or store, so it stays usable from the GUI,
// CLI, and MCP alike.
package importer

import (
	"fmt"
	"strings"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// ParseCurl parses a pasted cURL command string into a RequestDef. It
// covers the flags real-world "Copy as cURL" output and hand-written curl
// invocations actually use: -X/--request, -H/--header (repeatable),
// -d/--data/--data-raw/--data-binary/--data-urlencode, -u/--user (Basic
// auth), -b/--cookie, -G (send --data as query params instead of body),
// and the bare URL argument. The returned RequestDef has no ID,
// WorkspaceID, FolderID, or OrderKey set — assigning those is the caller's
// job (matching how every other constructor in this codebase leaves id
// generation to the call site, e.g. app.go's uuid.NewString() calls).
func ParseCurl(command string) (model.RequestDef, error) {
	tokens, err := tokenize(command)
	if err != nil {
		return model.RequestDef{}, fmt.Errorf("tokenize curl command: %w", err)
	}

	var (
		rawURL       string
		method       string
		headers      []model.KeyValue
		dataParts    []string
		dataIsBinary bool
		useGet       bool
		user         string
		cookies      []string
	)

	i := 0
	// Skip a leading bare "curl" token, if present, so both "curl ..." and
	// a bare argument list (no leading command name) are accepted.
	if i < len(tokens) && (tokens[i] == "curl" || tokens[i] == "curl.exe") {
		i++
	}

	for i < len(tokens) {
		tok := tokens[i]
		switch {
		case tok == "-X" || tok == "--request":
			val, next, ok := takeValue(tokens, i)
			if !ok {
				return model.RequestDef{}, fmt.Errorf("%s requires a value", tok)
			}
			method = val
			i = next

		case tok == "-H" || tok == "--header":
			val, next, ok := takeValue(tokens, i)
			if !ok {
				return model.RequestDef{}, fmt.Errorf("%s requires a value", tok)
			}
			if kv, ok := parseHeader(val); ok {
				headers = append(headers, kv)
			}
			i = next

		case tok == "-d" || tok == "--data" || tok == "--data-raw" || tok == "--data-ascii":
			val, next, ok := takeValue(tokens, i)
			if !ok {
				return model.RequestDef{}, fmt.Errorf("%s requires a value", tok)
			}
			dataParts = append(dataParts, val)
			i = next

		case tok == "--data-binary":
			val, next, ok := takeValue(tokens, i)
			if !ok {
				return model.RequestDef{}, fmt.Errorf("%s requires a value", tok)
			}
			dataParts = append(dataParts, val)
			dataIsBinary = true
			i = next

		case tok == "--data-urlencode":
			val, next, ok := takeValue(tokens, i)
			if !ok {
				return model.RequestDef{}, fmt.Errorf("%s requires a value", tok)
			}
			dataParts = append(dataParts, val)
			i = next

		case tok == "-u" || tok == "--user":
			val, next, ok := takeValue(tokens, i)
			if !ok {
				return model.RequestDef{}, fmt.Errorf("%s requires a value", tok)
			}
			user = val
			i = next

		case tok == "-b" || tok == "--cookie":
			val, next, ok := takeValue(tokens, i)
			if !ok {
				return model.RequestDef{}, fmt.Errorf("%s requires a value", tok)
			}
			cookies = append(cookies, val)
			i = next

		case tok == "-G" || tok == "--get":
			useGet = true
			i++

		case tok == "-A" || tok == "--user-agent":
			val, next, ok := takeValue(tokens, i)
			if !ok {
				return model.RequestDef{}, fmt.Errorf("%s requires a value", tok)
			}
			headers = append(headers, model.KeyValue{Key: "User-Agent", Value: val, Enabled: true})
			i = next

		case tok == "-e" || tok == "--referer":
			val, next, ok := takeValue(tokens, i)
			if !ok {
				return model.RequestDef{}, fmt.Errorf("%s requires a value", tok)
			}
			headers = append(headers, model.KeyValue{Key: "Referer", Value: val, Enabled: true})
			i = next

		case tok == "--compressed" || tok == "-k" || tok == "--insecure" ||
			tok == "-s" || tok == "--silent" || tok == "-v" || tok == "--verbose" ||
			tok == "-i" || tok == "--include" || tok == "-L" || tok == "--location":
			// Flags that don't affect the RequestDef shape: acknowledged and
			// skipped rather than erroring, since "Copy as cURL" output
			// commonly includes them.
			i++

		case strings.HasPrefix(tok, "-") && tok != "-":
			// Unknown flag: best-effort skip. If the next token looks like
			// this flag's value (not another flag), skip it too so parsing
			// doesn't misinterpret an option's argument as the URL.
			i++
			if i < len(tokens) && !strings.HasPrefix(tokens[i], "-") && rawURL == "" && !looksLikeURL(tokens[i]) {
				i++
			}

		default:
			if rawURL == "" {
				rawURL = tok
			}
			i++
		}
	}

	if rawURL == "" {
		return model.RequestDef{}, fmt.Errorf("no URL found in curl command")
	}

	if method == "" {
		switch {
		case useGet:
			method = "GET"
		case len(dataParts) > 0:
			method = "POST"
		default:
			method = "GET"
		}
	}
	method = strings.ToUpper(method)

	req := model.RequestDef{
		Protocol: model.ProtocolHTTP,
		Method:   method,
		URL:      rawURL,
		Headers:  headers,
	}

	if len(cookies) > 0 {
		req.Headers = append(req.Headers, model.KeyValue{Key: "Cookie", Value: strings.Join(cookies, "; "), Enabled: true})
	}

	if user != "" {
		username, password, _ := strings.Cut(user, ":")
		req.Auth = &model.AuthConfig{
			Kind:  model.AuthBasic,
			Basic: &model.BasicAuth{Username: username, Password: password},
		}
	}

	if len(dataParts) > 0 {
		joined := strings.Join(dataParts, "&")
		if useGet {
			req.URL = appendQueryString(req.URL, joined)
		} else if dataIsBinary {
			req.Body = &model.RequestBody{Kind: model.BodyBinary, Text: joined}
		} else if looksLikeJSON(joined) {
			req.Body = &model.RequestBody{Kind: model.BodyJSON, Text: joined}
		} else {
			req.Body = &model.RequestBody{Kind: model.BodyForm, Text: joined, FormFields: parseFormFields(joined)}
		}
	}

	return req, nil
}

// ToCurl renders a resolved request as a copyable, properly shell-quoted
// cURL command string — the inverse of ParseCurl. It reads the wire-level
// shape (method/headers/body/url) from resolved rather than req, since
// resolved is what actually goes out on the wire (templates expanded, auth
// applied); req is only consulted for values ResolvedRequest doesn't carry
// (none currently, but keeping the parameter matches the Protocol.Execute
// signature convention used across this codebase).
func ToCurl(req model.RequestDef, resolved core.ResolvedRequest) string {
	var b strings.Builder
	b.WriteString("curl")

	method := resolved.Method
	if method == "" {
		method = req.Method
	}
	if method == "" {
		method = "GET"
	}
	if method != "GET" {
		b.WriteString(" -X ")
		b.WriteString(shellQuote(method))
	}

	url := resolved.URL
	if url == "" {
		url = req.URL
	}
	if len(resolved.Params) > 0 {
		var qs []string
		for _, p := range resolved.Params {
			if p.Enabled {
				qs = append(qs, p.Key+"="+p.Value)
			}
		}
		if len(qs) > 0 {
			url = appendQueryString(url, strings.Join(qs, "&"))
		}
	}
	b.WriteString(" ")
	b.WriteString(shellQuote(url))

	for _, h := range resolved.Headers {
		if !h.Enabled {
			continue
		}
		b.WriteString(" -H ")
		b.WriteString(shellQuote(h.Key + ": " + h.Value))
	}

	if resolved.Body != nil && resolved.Body.Kind != model.BodyNone && resolved.Body.Text != "" {
		flag := " -d "
		if resolved.Body.Kind == model.BodyBinary {
			flag = " --data-binary "
		}
		b.WriteString(flag)
		b.WriteString(shellQuote(resolved.Body.Text))
	}

	return b.String()
}

// takeValue returns the value for a flag at tokens[i]: either the
// "--flag=value" suffix or the following token, plus the index to resume
// scanning from.
func takeValue(tokens []string, i int) (value string, next int, ok bool) {
	tok := tokens[i]
	if idx := strings.Index(tok, "="); idx > 0 && strings.HasPrefix(tok, "--") {
		return tok[idx+1:], i + 1, true
	}
	if i+1 >= len(tokens) {
		return "", i + 1, false
	}
	return tokens[i+1], i + 2, true
}

func parseHeader(raw string) (model.KeyValue, bool) {
	key, value, found := strings.Cut(raw, ":")
	if !found {
		return model.KeyValue{}, false
	}
	return model.KeyValue{Key: strings.TrimSpace(key), Value: strings.TrimSpace(value), Enabled: true}, true
}

func looksLikeURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.Contains(s, "://")
}

func looksLikeJSON(s string) bool {
	trimmed := strings.TrimSpace(s)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}

func parseFormFields(body string) []model.KeyValue {
	var fields []model.KeyValue
	for _, pair := range strings.Split(body, "&") {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		fields = append(fields, model.KeyValue{Key: key, Value: value, Enabled: true})
	}
	return fields
}

func appendQueryString(rawURL, qs string) string {
	if qs == "" {
		return rawURL
	}
	if strings.Contains(rawURL, "?") {
		return rawURL + "&" + qs
	}
	return rawURL + "?" + qs
}

// shellQuote wraps s in single quotes for POSIX-shell-safe copy/paste,
// escaping any embedded single quotes with the standard '\” trick. Values
// with no shell-meaningful characters are left unquoted for readability,
// matching what curl's own "Copy as cURL" output (and Chrome DevTools')
// does for the simple cases.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !needsQuoting(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func needsQuoting(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			continue
		case strings.ContainsRune("-_.:/@%+=", r):
			continue
		default:
			return true
		}
	}
	return false
}

// tokenize is a hand-rolled POSIX-ish shell word splitter: it understands
// single quotes (no escapes inside), double quotes (backslash escapes " \
// $ ` and newline), unquoted backslash escapes, whitespace-separated words,
// and a trailing backslash-newline as a line continuation (curl commands
// copied from docs/DevTools are frequently pasted as multiple lines joined
// with " \\\n"). It intentionally does not implement variable expansion,
// globbing, or command substitution — this is a tokenizer, not a shell.
func tokenize(command string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	hasToken := false

	runes := []rune(command)
	n := len(runes)
	i := 0

	flush := func() {
		if hasToken {
			tokens = append(tokens, cur.String())
			cur.Reset()
			hasToken = false
		}
	}

	for i < n {
		r := runes[i]
		switch {
		case r == '\\' && i+1 < n && runes[i+1] == '\n':
			// Line continuation: drop both characters, don't break the token.
			i += 2

		case r == '\\' && i+1 < n:
			cur.WriteRune(runes[i+1])
			hasToken = true
			i += 2

		case r == '\'':
			hasToken = true
			i++
			for i < n && runes[i] != '\'' {
				cur.WriteRune(runes[i])
				i++
			}
			if i >= n {
				return nil, fmt.Errorf("unterminated single quote")
			}
			i++ // closing quote

		case r == '"':
			hasToken = true
			i++
			for i < n && runes[i] != '"' {
				if runes[i] == '\\' && i+1 < n && strings.ContainsRune(`"\$`+"`\n", runes[i+1]) {
					cur.WriteRune(runes[i+1])
					i += 2
					continue
				}
				cur.WriteRune(runes[i])
				i++
			}
			if i >= n {
				return nil, fmt.Errorf("unterminated double quote")
			}
			i++ // closing quote

		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
			i++

		default:
			cur.WriteRune(r)
			hasToken = true
			i++
		}
	}
	flush()

	return tokens, nil
}
