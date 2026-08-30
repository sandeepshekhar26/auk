package core

import (
	"strings"

	"apitool/internal/core/model"
)

// applyPathParams substitutes Postman-style `:name` placeholders in a URL's
// PATH with the request's PathParams values. It runs in resolveAndAuthorize
// immediately after template resolution and BEFORE auth is applied, so a
// signing scheme (AWS SigV4, OAuth1) signs the same URL that actually goes
// on the wire, and before the policy chokepoint sees the URL.
//
// Rules, chosen so the URL bar stays a WYSIWYG preview of the send:
//
//   - Only the path is touched. Everything before the first '/' (scheme,
//     userinfo, host, ":port") and everything from the first '?' onward
//     (query string) is copied through byte-for-byte. That's what keeps
//     `example.com:443` and `?filter=:tbd` alone.
//   - A placeholder is a WHOLE segment matching `:[A-Za-z_][A-Za-z0-9_]*`
//     — the same rule the URL bar in RequestEditor.tsx parses with, so the
//     rows the user sees are exactly the ones substituted here. A partial
//     match inside a longer segment (`v:1`, `:443`) is not a placeholder.
//   - A placeholder with no matching row, or a row with an empty value,
//     is left as the literal `:name`. A visible, predictable failure that
//     shows up in the response/history URL beats erroring the send or
//     silently sending an empty segment.
//   - Values are percent-encoded as a single path segment (see
//     escapePathSegment) after the caller has already template-resolved
//     them, so `${userId}` in a path param value works like anywhere else.
//   - KeyValue.Enabled is deliberately IGNORED. Path param rows are derived
//     from the URL rather than added by hand, so the UI gives them no
//     enable/disable checkbox; honoring the flag would make a row created
//     by any other producer (imported YAML, a Go struct literal that leaves
//     the bool at its false zero value) silently stop substituting.
func applyPathParams(protocol model.ProtocolKind, rawURL string, params []model.KeyValue) string {
	// Path placeholders only make sense for protocols addressed by a URL
	// path. gRPC targets are `host:port` + a method header, and WS/SSE URLs
	// are passed to dialers that treat the whole string as an endpoint —
	// none of them should have a colon anywhere in them rewritten.
	if protocol != model.ProtocolHTTP && protocol != model.ProtocolGraphQL {
		return rawURL
	}
	if len(params) == 0 || rawURL == "" {
		return rawURL
	}

	base, query := rawURL, ""
	if i := strings.Index(rawURL, "?"); i >= 0 {
		base, query = rawURL[:i], rawURL[i:]
	}

	// Split off the authority so a placeholder can never rewrite the host.
	// For an absolute URL (scheme://host/path) the path starts at the first
	// '/' AFTER the "//" that introduces the authority — otherwise the host
	// itself sits in the walked segment list, and a literal `:name` in the
	// host position (e.g. https://:gateway/api with a param named "gateway",
	// or a value that happens to look like one) would be percent-encoded as
	// a path segment, corrupting the authority. For a scheme-relative
	// (`//host/path`) or already-relative (`/path`, `path`) URL we fall back
	// to the first '/'.
	pathStart := 0
	if i := strings.Index(base, "://"); i >= 0 {
		afterAuthority := strings.IndexByte(base[i+3:], '/')
		if afterAuthority < 0 {
			return rawURL // authority only, no path
		}
		pathStart = i + 3 + afterAuthority
	} else if strings.HasPrefix(base, "//") {
		afterAuthority := strings.IndexByte(base[2:], '/')
		if afterAuthority < 0 {
			return rawURL
		}
		pathStart = 2 + afterAuthority
	} else if i := strings.IndexByte(base, '/'); i >= 0 {
		pathStart = i
	} else {
		return rawURL
	}
	head, path := base[:pathStart], base[pathStart:]

	values := make(map[string]string, len(params))
	for _, p := range params {
		if p.Key != "" {
			values[p.Key] = p.Value
		}
	}

	segments := strings.Split(path, "/")
	for i, seg := range segments {
		name, ok := pathParamName(seg)
		if !ok {
			continue
		}
		v, ok := values[name]
		if !ok || v == "" {
			continue // leave the literal `:name` visible
		}
		segments[i] = escapePathSegment(v)
	}

	return head + strings.Join(segments, "/") + query
}

// pathParamName reports whether seg is exactly a `:name` placeholder and
// returns the bare name. Mirrors PATH_PARAM_SEGMENT in RequestEditor.tsx.
func pathParamName(seg string) (string, bool) {
	if len(seg) < 2 || seg[0] != ':' {
		return "", false
	}
	for i := 1; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9':
			if i == 1 {
				return "", false // a name can't start with a digit (`:443`)
			}
		default:
			return "", false
		}
	}
	return seg[1:], true
}

// escapePathSegment percent-encodes everything outside RFC 3986's unreserved
// set (ALPHA / DIGIT / "-" / "." / "_" / "~"), byte by byte, so multi-byte
// UTF-8 is encoded per byte as required.
//
// Deliberately stricter than url.PathEscape, which leaves the sub-delims and
// ":"/"@"/"$"/"&"/"+"/","/";"/"=" unescaped — those are legal in a path
// segment, but a value containing one would change how the resulting URL
// parses (a stray ';' or '=' reads as a segment parameter to some servers,
// and a leading ':' would look like another placeholder to this very
// function on a second pass). A path param value is opaque user data, so it
// gets the conservative encoding.
func escapePathSegment(v string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(v))
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}
