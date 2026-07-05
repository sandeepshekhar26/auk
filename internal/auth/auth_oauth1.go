// OAuth 1.0 request signing (HMAC-SHA1 only — PLAINTEXT and RSA-SHA1 aren't
// implemented). Built directly from RFC 5849 (https://www.rfc-editor.org/rfc/rfc5849);
// see auth_oauth1_test.go for how the implementation is verified.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // required by the OAuth 1.0 spec itself, not a choice
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// applyOAuth1 signs req per RFC 5849 §3 and returns it with an
// `Authorization: OAuth ...` header added. Token/TokenSecret may be empty (a
// two-legged / consumer-only request has no access token yet).
func applyOAuth1(cfg model.OAuth1Auth, req core.ResolvedRequest, now time.Time) (core.ResolvedRequest, error) {
	if cfg.ConsumerKey == "" || cfg.ConsumerSecret == "" {
		return req, fmt.Errorf("oauth1 auth: consumerKey and consumerSecret are required")
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		return req, fmt.Errorf("oauth1 auth: invalid URL: %w", err)
	}

	// Merge Params into the URL's query NOW, mirroring applyAWSSigV4 exactly
	// (see that function's comment for the full reasoning): Auth.Apply runs
	// BEFORE buildURL in resolveAndAuthorize, so without this, anything
	// added via the Params tab would sign against a query string that
	// doesn't match what's actually sent.
	q := u.Query()
	for _, p := range req.Params {
		if p.Enabled && p.Key != "" {
			q.Set(p.Key, p.Value)
		}
	}
	u.RawQuery = q.Encode()
	req.Params = nil

	nonce, err := oauth1Nonce()
	if err != nil {
		return req, fmt.Errorf("oauth1 auth: generating nonce: %w", err)
	}
	oauthParams := oauth1Sign(cfg, req.Method, u, nonce, now)

	req.URL = u.String()
	req.Headers = append(req.Headers, model.KeyValue{Key: "Authorization", Value: oauth1AuthHeader(oauthParams), Enabled: true})
	return req, nil
}

// oauth1Sign is the pure, deterministic core of applyOAuth1 — everything
// that varies (nonce, clock) is passed in explicitly rather than generated
// here, so tests can pin exact values and assert an exact signature, the
// same reason applyAWSSigV4 takes `now` as a parameter instead of calling
// time.Now() itself. u's query must already have Params merged in (the
// caller's job — see applyOAuth1) since this only reads u.Query().
// Returns the full oauth_* parameter set, INCLUDING oauth_signature.
func oauth1Sign(cfg model.OAuth1Auth, method string, u *url.URL, nonce string, now time.Time) url.Values {
	oauthParams := url.Values{
		"oauth_consumer_key":     {cfg.ConsumerKey},
		"oauth_nonce":            {nonce},
		"oauth_signature_method": {"HMAC-SHA1"},
		"oauth_timestamp":        {strconv.FormatInt(now.Unix(), 10)},
		"oauth_version":          {"1.0"},
	}
	if cfg.Token != "" {
		oauthParams.Set("oauth_token", cfg.Token)
	}

	// RFC 5849 §3.4.1.3: the signature covers the OAuth protocol params plus
	// every query-string param. Form-urlencoded BODY params are ALSO
	// supposed to be covered per the spec — deliberately not handled here.
	// This app's OAuth1 use is overwhelmingly query-param/GET-shaped (the
	// APIs that still use OAuth1 today), and correctly detecting a
	// form-urlencoded Content-Type and re-parsing the body without
	// double-decoding is real extra surface area for a case that's rare in
	// practice; a JSON or other non-form body is unaffected either way.
	signingParams := url.Values{}
	for k, vs := range oauthParams {
		signingParams[k] = append(signingParams[k], vs...)
	}
	for k, vs := range u.Query() {
		signingParams[k] = append(signingParams[k], vs...)
	}

	// awsURIEncode/awsCanonicalQueryString (internal/auth/auth_sigv4.go) are
	// reused deliberately, not duplicated: RFC 5849 §3.6's percent-encoding
	// rule (percent-encode everything except A-Za-z0-9-._~) and its
	// §3.4.1.3.2 parameter normalization (encode, then sort by encoded key
	// then encoded value, join pairs with "&") are byte-for-byte the same
	// algorithm AWS SigV4 already needed.
	baseString := strings.ToUpper(method) + "&" + awsURIEncode(oauth1BaseURI(u)) + "&" + awsURIEncode(awsCanonicalQueryString(signingParams))

	signingKey := awsURIEncode(cfg.ConsumerSecret) + "&" + awsURIEncode(cfg.TokenSecret)
	mac := hmac.New(sha1.New, []byte(signingKey))
	mac.Write([]byte(baseString))
	oauthParams.Set("oauth_signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	return oauthParams
}

// oauth1BaseURI builds RFC 5849 §3.4.1.2's "base string URI": lowercased
// scheme + lowercased host (+ port, only when non-default for the scheme) +
// path (root "/" if empty) — no query string, no fragment.
func oauth1BaseURI(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	authority := host
	if port := u.Port(); port != "" {
		isDefaultPort := (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
		if !isDefaultPort {
			authority = host + ":" + port
		}
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return scheme + "://" + authority + path
}

// oauth1AuthHeader builds the `OAuth k="v", k="v", ...` header value.
// Sorted by key for deterministic output (the spec doesn't require a
// specific order, but a stable one makes this testable).
func oauth1AuthHeader(params url.Values) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf(`%s="%s"`, k, awsURIEncode(params.Get(k)))
	}
	return "OAuth " + strings.Join(parts, ", ")
}

// oauth1Nonce returns a random 32-hex-character string — unique per request
// is the only real requirement (RFC 5849 §3.3), not any particular format.
func oauth1Nonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
