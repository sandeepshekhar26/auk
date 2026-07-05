package auth

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// TestOAuth1Sign_MatchesIndependentPythonImplementation verifies the exact
// base string and signature against a from-scratch Python reimplementation
// of RFC 5849 (stdlib hmac/hashlib only, zero shared code — see
// scratchpad/oauth1_verify.py) rather than a memorized/copied fixture,
// matching this session's own precedent for cryptographic code (a
// previously-found discrepancy in a copied AWS SigV4 test fixture was only
// caught this same way).
func TestOAuth1Sign_MatchesIndependentPythonImplementation(t *testing.T) {
	cfg := model.OAuth1Auth{
		ConsumerKey:    "xvz1evFS4wEEPTGEFPHBog",
		ConsumerSecret: "kAcSOqF21Fu85e7zjz7ZN2U4ZkVjF6oR",
		Token:          "370773112-GmHxMAgYyLbNEtIKZeRNFsMKPR9EyMZeS9weJAEb",
		TokenSecret:    "LswwdoUaIvS8ltyTt5jkRh4J50vUPVVHtR2oAAAAAAAAA",
	}
	u, err := url.Parse("https://api.example.com/1/statuses/update.json?status=hello+world")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	nonce := "kYjzVBB8Y0ZFabxSWbWovY3uYSQ2pTgmZeNu2VS4cg"
	now := time.Unix(1318622958, 0)

	got := oauth1Sign(cfg, "GET", u, nonce, now)

	const wantSignature = "tSeIomceq1hq+2xM2e8hQ8V83aQ="
	if got.Get("oauth_signature") != wantSignature {
		t.Fatalf("oauth_signature = %q, want %q (independently computed via scratchpad/oauth1_verify.py)", got.Get("oauth_signature"), wantSignature)
	}
	if got.Get("oauth_consumer_key") != cfg.ConsumerKey {
		t.Errorf("oauth_consumer_key = %q, want %q", got.Get("oauth_consumer_key"), cfg.ConsumerKey)
	}
	if got.Get("oauth_token") != cfg.Token {
		t.Errorf("oauth_token = %q, want %q", got.Get("oauth_token"), cfg.Token)
	}
	if got.Get("oauth_signature_method") != "HMAC-SHA1" {
		t.Errorf("oauth_signature_method = %q, want HMAC-SHA1", got.Get("oauth_signature_method"))
	}
	if got.Get("oauth_timestamp") != "1318622958" {
		t.Errorf("oauth_timestamp = %q, want 1318622958", got.Get("oauth_timestamp"))
	}
	if got.Get("oauth_version") != "1.0" {
		t.Errorf("oauth_version = %q, want 1.0", got.Get("oauth_version"))
	}
}

func TestOAuth1BaseURI(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"no port, https default", "https://api.example.com/path", "https://api.example.com/path"},
		{"explicit default https port dropped", "https://api.example.com:443/path", "https://api.example.com/path"},
		{"explicit default http port dropped", "http://api.example.com:80/path", "http://api.example.com/path"},
		{"non-default port kept", "https://api.example.com:8443/path", "https://api.example.com:8443/path"},
		{"empty path becomes root", "https://api.example.com", "https://api.example.com/"},
		{"query and fragment excluded", "https://api.example.com/path?a=1#frag", "https://api.example.com/path"},
		{"host lowercased", "https://API.Example.COM/path", "https://api.example.com/path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.url, err)
			}
			if got := oauth1BaseURI(u); got != tc.want {
				t.Errorf("oauth1BaseURI(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestApplyOAuth1_RequiresConsumerCredentials(t *testing.T) {
	req := core.ResolvedRequest{Method: "GET", URL: "https://api.example.com/x"}
	if _, err := applyOAuth1(model.OAuth1Auth{}, req, time.Now()); err == nil {
		t.Fatal("want error when consumerKey/consumerSecret are missing")
	}
}

// TestApplyOAuth1_ParamsMergedBeforeSigning proves a query param added via
// the Params tab (req.Params) signs identically to the same param written
// literally into the URL, and that req.Params is cleared afterward —
// mirroring the equivalent AWS SigV4 test for the exact same reason (Params
// wouldn't otherwise be in the query string yet when Auth.Apply runs).
func TestApplyOAuth1_ParamsMergedBeforeSigning(t *testing.T) {
	cfg := model.OAuth1Auth{ConsumerKey: "key", ConsumerSecret: "secret"}
	now := time.Unix(1700000000, 0)

	literal := core.ResolvedRequest{Method: "GET", URL: "https://api.example.com/x?status=hello"}
	viaParams := core.ResolvedRequest{
		Method: "GET", URL: "https://api.example.com/x",
		Params: []model.KeyValue{{Key: "status", Value: "hello", Enabled: true}},
	}

	// Both need the identical nonce to produce an identical signature —
	// call oauth1Sign directly (bypassing applyOAuth1's internal random
	// nonce) via the same merge-then-sign steps applyOAuth1 itself performs.
	mergeParams := func(req core.ResolvedRequest) *url.URL {
		u, _ := url.Parse(req.URL)
		q := u.Query()
		for _, p := range req.Params {
			if p.Enabled && p.Key != "" {
				q.Set(p.Key, p.Value)
			}
		}
		u.RawQuery = q.Encode()
		return u
	}
	uLiteral := mergeParams(literal)
	uViaParams := mergeParams(viaParams)

	const nonce = "fixed-nonce-for-test"
	sigLiteral := oauth1Sign(cfg, "GET", uLiteral, nonce, now).Get("oauth_signature")
	sigViaParams := oauth1Sign(cfg, "GET", uViaParams, nonce, now).Get("oauth_signature")
	if sigLiteral != sigViaParams {
		t.Fatalf("signature via literal query %q != signature via Params tab %q — Params must be merged before signing", sigLiteral, sigViaParams)
	}

	resolved, err := applyOAuth1(cfg, viaParams, now)
	if err != nil {
		t.Fatalf("applyOAuth1: %v", err)
	}
	if len(resolved.Params) != 0 {
		t.Errorf("want req.Params cleared after merging, got %+v", resolved.Params)
	}
	if resolved.URL != "https://api.example.com/x?status=hello" {
		t.Errorf("got URL %q, want the param merged into the query string", resolved.URL)
	}
}

func TestApplyOAuth1_AddsAuthorizationHeader(t *testing.T) {
	cfg := model.OAuth1Auth{ConsumerKey: "key", ConsumerSecret: "secret"}
	req := core.ResolvedRequest{Method: "GET", URL: "https://api.example.com/x"}

	resolved, err := applyOAuth1(cfg, req, time.Now())
	if err != nil {
		t.Fatalf("applyOAuth1: %v", err)
	}
	var authHeader string
	for _, h := range resolved.Headers {
		if h.Key == "Authorization" {
			authHeader = h.Value
		}
	}
	if authHeader == "" {
		t.Fatal("want an Authorization header to be added")
	}
	if !strings.HasPrefix(authHeader, "OAuth ") || !strings.Contains(authHeader, "oauth_signature=") || !strings.Contains(authHeader, `oauth_consumer_key="key"`) {
		t.Errorf("Authorization header looks wrong: %q", authHeader)
	}
}

// TestApplyOAuth1_ThroughContext confirms Apply's switch case wires OAuth1
// correctly end-to-end (not just the internal helper), matching how the
// AuthAWSSigV4 case is exercised in the wider auth_test.go suite.
func TestApplyOAuth1_ThroughContext(t *testing.T) {
	cfg := model.AuthConfig{Kind: model.AuthOAuth1, OAuth1: &model.OAuth1Auth{ConsumerKey: "key", ConsumerSecret: "secret"}}
	req := core.ResolvedRequest{Method: "GET", URL: "https://api.example.com/x"}

	resolved, err := New().Apply(context.Background(), cfg, req)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	found := false
	for _, h := range resolved.Headers {
		if h.Key == "Authorization" {
			found = true
		}
	}
	if !found {
		t.Fatal("want Authorization header from the AuthOAuth1 case in Apply's switch")
	}
}
