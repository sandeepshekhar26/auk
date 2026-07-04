// AWS Signature Version 4 request signing. Implemented against the
// algorithm as documented at
// https://docs.aws.amazon.com/IAM/latest/UserGuide/create-signed-request.html
// and verified against AWS's own published test suite (saibotsivad's mirror
// of the official aws-sig-v4-test-suite fixtures) rather than hand-derived
// expectations — see auth_sigv4_test.go.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// applyAWSSigV4 signs req per AWS SigV4 and returns it with Authorization
// (+ X-Amz-Date, + X-Amz-Security-Token for temporary credentials) headers
// added. now is injected so tests can pin a fixed clock; production calls
// pass time.Now().
func applyAWSSigV4(cfg model.AWSSigV4Auth, req core.ResolvedRequest, now time.Time) (core.ResolvedRequest, error) {
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" || cfg.Region == "" || cfg.Service == "" {
		return req, fmt.Errorf("aws sigv4 auth: accessKeyId, secretAccessKey, region, and service are required")
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		return req, fmt.Errorf("aws sigv4 auth: invalid URL: %w", err)
	}

	// Merge Params into the URL's query NOW, mirroring internal/protocols/
	// http's buildURL exactly. Auth.Apply runs BEFORE buildURL in
	// resolveAndAuthorize, so without this, anything added via the Params
	// tab (rather than typed directly into the URL) would be merged in
	// AFTER signing — a query string the signature never covered. Once
	// merged here, req.Params is cleared so that later buildURL call has
	// nothing left to add (Set() on identical values is a no-op regardless,
	// but clearing removes any doubt).
	q := u.Query()
	for _, p := range req.Params {
		if p.Enabled && p.Key != "" {
			q.Set(p.Key, p.Value)
		}
	}
	u.RawQuery = q.Encode()
	req.Params = nil

	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")

	canonicalURI := awsCanonicalURI(u.Path)
	canonicalQueryString := awsCanonicalQueryString(u.Query())

	var bodyBytes []byte
	if req.Body != nil {
		bodyBytes = []byte(req.Body.Text)
	}
	hashedPayload := hexSHA256(bodyBytes)

	var pairs []headerPair
	for _, h := range req.Headers {
		if h.Enabled && h.Key != "" {
			pairs = append(pairs, headerPair{h.Key, h.Value})
		}
	}
	// Host isn't something the app sends as a literal custom header (Go's
	// http.Client derives it from the URL), but AWS requires it in the
	// signature — added here for canonicalization only, never appended to
	// req.Headers below.
	pairs = append(pairs, headerPair{"Host", u.Host}, headerPair{"X-Amz-Date", amzDate})
	if cfg.SessionToken != "" {
		pairs = append(pairs, headerPair{"X-Amz-Security-Token", cfg.SessionToken})
	}

	canonicalHeaders, signedHeaders := awsCanonicalHeaders(pairs)

	canonicalRequest := strings.Join([]string{
		strings.ToUpper(req.Method),
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		hashedPayload,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/%s/aws4_request", dateStamp, cfg.Region, cfg.Service)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := awsSigningKey(cfg.SecretAccessKey, dateStamp, cfg.Region, cfg.Service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		cfg.AccessKeyID, credentialScope, signedHeaders, signature)

	req.URL = u.String()
	req.Headers = append(req.Headers, model.KeyValue{Key: "X-Amz-Date", Value: amzDate, Enabled: true})
	if cfg.SessionToken != "" {
		req.Headers = append(req.Headers, model.KeyValue{Key: "X-Amz-Security-Token", Value: cfg.SessionToken, Enabled: true})
	}
	req.Headers = append(req.Headers, model.KeyValue{Key: "Authorization", Value: authHeader, Enabled: true})
	return req, nil
}

// awsCanonicalURI re-encodes an already-decoded URL path per AWS's URI-encode
// rules (every byte except A-Z a-z 0-9 - . _ ~, applied per path segment so
// "/" separators are preserved) rather than trusting the standard library's
// own escaping, which AWS's docs explicitly warn can disagree in edge cases.
func awsCanonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		segments[i] = awsURIEncode(seg)
	}
	return strings.Join(segments, "/")
}

// awsCanonicalQueryString URI-encodes every key and value individually, then
// sorts pairs by key and (for repeated keys) by value — sorting happens
// AFTER encoding, per the spec.
func awsCanonicalQueryString(q url.Values) string {
	type kv struct{ k, v string }
	var pairs []kv
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, kv{awsURIEncode(k), awsURIEncode(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

// awsURIEncode implements AWS's UriEncode(): percent-encode every byte except
// unreserved characters (A-Z a-z 0-9 - . _ ~). AWS's docs explicitly say not
// to trust a platform's built-in URI encoder here (e.g. Go's url.QueryEscape
// encodes space as "+", not "%20", and its escaping of "~" has changed across
// versions) — this is a small enough function to just own directly.
func awsURIEncode(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			buf.WriteByte(c)
		} else {
			fmt.Fprintf(&buf, "%%%02X", c)
		}
	}
	return buf.String()
}

// headerPair is a name/value pair awaiting canonicalization — a plain string
// pair rather than model.KeyValue since Host/X-Amz-Date/X-Amz-Security-Token
// need to flow through the same canonicalization without necessarily being
// appended to req.Headers (Host never is; see applyAWSSigV4).
type headerPair struct{ name, value string }

// awsCanonicalHeaders groups headers by lowercased name (preserving each
// name's value-insertion order for the comma-join — duplicate header names
// are NOT re-sorted by value, only the header NAMES are sorted), trims and
// collapses internal whitespace per value, and returns both the
// newline-terminated CanonicalHeaders block and the semicolon-joined
// SignedHeaders list.
func awsCanonicalHeaders(pairs []headerPair) (canonical, signed string) {
	order := make([]string, 0, len(pairs))
	seen := map[string]bool{}
	values := map[string][]string{}
	for _, h := range pairs {
		name := strings.ToLower(strings.TrimSpace(h.name))
		if !seen[name] {
			seen[name] = true
			order = append(order, name)
		}
		values[name] = append(values[name], awsTrimHeaderValue(h.value))
	}
	sort.Strings(order)

	var buf strings.Builder
	for _, name := range order {
		buf.WriteString(name)
		buf.WriteString(":")
		buf.WriteString(strings.Join(values[name], ","))
		buf.WriteString("\n")
	}
	return buf.String(), strings.Join(order, ";")
}

// awsTrimHeaderValue trims leading/trailing whitespace and collapses any
// internal run of whitespace to a single space, per the Trim() function the
// signing spec requires for header values.
func awsTrimHeaderValue(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// awsSigningKey derives the SigV4 signing key via the documented HMAC chain:
// secret -> date -> region -> service -> "aws4_request".
func awsSigningKey(secretKey, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
