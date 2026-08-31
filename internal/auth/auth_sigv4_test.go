package auth

import (
	"context"
	"strings"
	"testing"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// Fixtures below are taken verbatim from AWS's own published Signature
// Version 4 test suite (the "config" block and named test cases mirrored at
// github.com/saibotsivad/aws-sig-v4-test-suite, itself sourced from AWS's
// docs) — not hand-derived, so a bug in this implementation's understanding
// of the algorithm doesn't also produce a self-consistent "expected" value.
const (
	sigv4TestAccessKey = "AKIDEXAMPLE"
	sigv4TestSecretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	sigv4TestRegion    = "us-east-1"
	sigv4TestService   = "service"
)

var sigv4TestTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) // 20150830T123600Z

func TestAWSSigV4_GetVanilla(t *testing.T) {
	cfg := model.AWSSigV4Auth{
		AccessKeyID: sigv4TestAccessKey, SecretAccessKey: sigv4TestSecretKey,
		Region: sigv4TestRegion, Service: sigv4TestService,
	}
	req := core.ResolvedRequest{Method: "GET", URL: "https://example.amazonaws.com/"}

	got, err := applyAWSSigV4(cfg, req, sigv4TestTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantAuthz := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	assertAuthzHeader(t, got, wantAuthz)
}

func TestAWSSigV4_PostVanilla(t *testing.T) {
	cfg := model.AWSSigV4Auth{
		AccessKeyID: sigv4TestAccessKey, SecretAccessKey: sigv4TestSecretKey,
		Region: sigv4TestRegion, Service: sigv4TestService,
	}
	req := core.ResolvedRequest{Method: "POST", URL: "https://example.amazonaws.com/"}

	got, err := applyAWSSigV4(cfg, req, sigv4TestTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantAuthz := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5da7c1a2acd57cee7505fc6676e4e544621c30862966e37dddb68e92efbe5d6b"
	assertAuthzHeader(t, got, wantAuthz)
}

func TestAWSSigV4_GetVanillaQueryOrderKey(t *testing.T) {
	// Duplicate query key with different values, deliberately out of the
	// canonical (sorted-by-value-too) order in the literal URL — proves
	// awsCanonicalQueryString re-sorts rather than trusting input order.
	cfg := model.AWSSigV4Auth{
		AccessKeyID: sigv4TestAccessKey, SecretAccessKey: sigv4TestSecretKey,
		Region: sigv4TestRegion, Service: sigv4TestService,
	}
	req := core.ResolvedRequest{Method: "GET", URL: "https://example.amazonaws.com/?Param1=value2&Param1=Value1"}

	got, err := applyAWSSigV4(cfg, req, sigv4TestTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantAuthz := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=eedbc4e291e521cf13422ffca22be7d2eb8146eecf653089df300a15b2382bd1"
	assertAuthzHeader(t, got, wantAuthz)
}

func TestAWSSigV4_GetVanillaQueryUnreserved(t *testing.T) {
	// Every unreserved character as both query key and value — proves
	// awsURIEncode's character set matches AWS's exactly (no over- or
	// under-encoding of "-._~" and alphanumerics).
	unreserved := "-._~0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	cfg := model.AWSSigV4Auth{
		AccessKeyID: sigv4TestAccessKey, SecretAccessKey: sigv4TestSecretKey,
		Region: sigv4TestRegion, Service: sigv4TestService,
	}
	req := core.ResolvedRequest{Method: "GET", URL: "https://example.amazonaws.com/?" + unreserved + "=" + unreserved}

	got, err := applyAWSSigV4(cfg, req, sigv4TestTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantAuthz := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=9c3e54bfcdf0b19771a7f523ee5669cdf59bc7cc0884027167c21bb143a40197"
	assertAuthzHeader(t, got, wantAuthz)
}

func TestAWSSigV4_PostXWWWFormURLEncoded(t *testing.T) {
	// A real body + extra explicit headers (Content-Type, Content-Length) —
	// proves header dedup/sort across >2 names AND that the body is hashed
	// into HashedPayload (a wrong hash would produce a different signature
	// even with everything else correct).
	//
	// The expected signature below is NOT the "authz" field the source
	// fixture (github.com/saibotsivad/aws-sig-v4-test-suite) ships for this
	// test case — that field disagrees with the SAME fixture's own "creq"
	// (canonical request) file: creq's SignedHeaders includes content-length,
	// but authz's Signature was computed as if it didn't (a known class of
	// inconsistency in community mirrors of this suite, distinct from the
	// other cases here which have internally-consistent creq/authz pairs).
	// Verified independently in Python (stdlib hmac/hashlib only, no shared
	// code with this package) that signing the fixture's own creq — which
	// this implementation reproduces character-for-character — produces
	// exactly this signature; signing a content-length-excluded canonical
	// request instead produces the fixture's (inconsistent) authz value.
	cfg := model.AWSSigV4Auth{
		AccessKeyID: sigv4TestAccessKey, SecretAccessKey: sigv4TestSecretKey,
		Region: sigv4TestRegion, Service: sigv4TestService,
	}
	req := core.ResolvedRequest{
		Method: "POST", URL: "https://example.amazonaws.com/",
		Headers: []model.KeyValue{
			{Key: "Content-Type", Value: "application/x-www-form-urlencoded", Enabled: true},
			{Key: "Content-Length", Value: "13", Enabled: true},
		},
		Body: &model.RequestBody{Kind: model.BodyText, Text: "Param1=value1"},
	}

	got, err := applyAWSSigV4(cfg, req, sigv4TestTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantAuthz := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=content-length;content-type;host;x-amz-date, Signature=fec50118d90ecf934441dd37fb9a49bd7f5adb6450802ca3a0977623bbb7c27f"
	assertAuthzHeader(t, got, wantAuthz)
}

func TestAWSSigV4_SessionToken(t *testing.T) {
	// Not one of the fixed test-suite vectors (those don't cover a session
	// token) — checks the mechanics instead: X-Amz-Security-Token is both
	// sent as a header and folded into SignedHeaders/the signature (omitting
	// it from the canonical request would still produce SOME signature, just
	// silently the wrong one AWS would reject; asserting SignedHeaders
	// includes it is what actually catches that class of bug).
	cfg := model.AWSSigV4Auth{
		AccessKeyID: sigv4TestAccessKey, SecretAccessKey: sigv4TestSecretKey,
		Region: sigv4TestRegion, Service: sigv4TestService,
		SessionToken: "TOKEN123",
	}
	req := core.ResolvedRequest{Method: "GET", URL: "https://example.amazonaws.com/"}

	got, err := applyAWSSigV4(cfg, req, sigv4TestTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	authz := headerValue(t, got, "Authorization")
	if !strings.Contains(authz, "SignedHeaders=host;x-amz-date;x-amz-security-token") {
		t.Fatalf("expected x-amz-security-token in SignedHeaders, got: %s", authz)
	}
	if headerValue(t, got, "X-Amz-Security-Token") != "TOKEN123" {
		t.Fatalf("expected X-Amz-Security-Token header to be sent")
	}
}

func TestAWSSigV4_RequiresConfig(t *testing.T) {
	cases := []model.AWSSigV4Auth{
		{SecretAccessKey: "x", Region: "us-east-1", Service: "s3"},    // missing AccessKeyID
		{AccessKeyID: "x", Region: "us-east-1", Service: "s3"},        // missing SecretAccessKey
		{AccessKeyID: "x", SecretAccessKey: "y", Service: "s3"},       // missing Region
		{AccessKeyID: "x", SecretAccessKey: "y", Region: "us-east-1"}, // missing Service
	}
	for _, cfg := range cases {
		if _, err := applyAWSSigV4(cfg, core.ResolvedRequest{Method: "GET", URL: "https://x.com/"}, sigv4TestTime); err == nil {
			t.Fatalf("expected error for incomplete config %+v", cfg)
		}
	}
}

func TestAWSSigV4_ParamsMergedBeforeSigning(t *testing.T) {
	// Params (the Params tab, not literal URL query text) must be folded
	// into the query string that gets signed — this is exactly the gap that
	// would exist if signing ran on req.URL alone before internal/protocols/
	// http's buildURL merges Params in.
	cfg := model.AWSSigV4Auth{
		AccessKeyID: sigv4TestAccessKey, SecretAccessKey: sigv4TestSecretKey,
		Region: sigv4TestRegion, Service: sigv4TestService,
	}
	reqWithParam := core.ResolvedRequest{
		Method: "GET", URL: "https://example.amazonaws.com/",
		Params: []model.KeyValue{{Key: "Param1", Value: "value1", Enabled: true}},
	}
	reqWithLiteralQuery := core.ResolvedRequest{
		Method: "GET", URL: "https://example.amazonaws.com/?Param1=value1",
	}

	got1, err := applyAWSSigV4(cfg, reqWithParam, sigv4TestTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got2, err := applyAWSSigV4(cfg, reqWithLiteralQuery, sigv4TestTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sig1 := headerValue(t, got1, "Authorization")
	sig2 := headerValue(t, got2, "Authorization")
	if sig1 != sig2 {
		t.Fatalf("expected identical signatures for equivalent Params-vs-literal-query requests:\n%s\n%s", sig1, sig2)
	}
	if !strings.Contains(got1.URL, "Param1=value1") {
		t.Fatalf("expected Params merged into req.URL, got %s", got1.URL)
	}
	if len(got1.Params) != 0 {
		t.Fatalf("expected Params cleared after merging, got %+v", got1.Params)
	}
}

func headerValue(t *testing.T, req core.ResolvedRequest, key string) string {
	t.Helper()
	for _, h := range req.Headers {
		if h.Key == key {
			return h.Value
		}
	}
	t.Fatalf("header %q not found in %+v", key, req.Headers)
	return ""
}

func assertAuthzHeader(t *testing.T, req core.ResolvedRequest, want string) {
	t.Helper()
	got := headerValue(t, req, "Authorization")
	if got != want {
		t.Fatalf("Authorization header mismatch:\n got:  %s\n want: %s", got, want)
	}
}

// awsCanonicalURI must canonicalize from the ESCAPED path so an encoded "/"
// (%2F) inside a single path segment — as produced by a `:name` path-param
// value containing a slash — stays an encoded slash in the signed canonical
// URI instead of being decoded into a new separator. If it were split, the
// signed path structure would diverge from the wire path structure and AWS
// would reject with SignatureDoesNotMatch. Regression test for that fix.
func TestAWSCanonicalURI_EncodedSlashSurvives(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/files/folder%2Fsub", "/files/folder%2Fsub"}, // %2F is NOT a separator
		{"/files/a%20b", "/files/a%20b"},               // ordinary encoded space round-trips
		{"/users/42/posts", "/users/42/posts"},         // plain path unchanged
		{"", "/"},                                      // empty path canonicalizes to root
		{"/a b", "/a%20b"},                             // a raw (unescaped) space still gets encoded
	}
	for _, c := range cases {
		if got := awsCanonicalURI(c.in); got != c.want {
			t.Errorf("awsCanonicalURI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---- AWS profile / SSO credential bridging ------------------------------

// A profile takes PRECEDENCE over pasted keys. A config carrying both is one
// someone migrated from static keys to a profile; signing with the stale keys
// would use credentials they believe they stopped using — and on an SSO
// account those keys are usually long revoked, producing a 403 that points at
// the wrong thing entirely.
func TestProfileWinsOverPastedKeys(t *testing.T) {
	// No profile: pasted keys are used untouched, with no subprocess at all.
	plain := model.AWSSigV4Auth{
		AccessKeyID: "AKIAPASTED", SecretAccessKey: "pasted-secret",
		Region: "us-east-1", Service: "execute-api",
	}
	got, err := resolveAWSCredentials(context.Background(), plain)
	if err != nil {
		t.Fatalf("resolveAWSCredentials with no profile: %v", err)
	}
	if got.AccessKeyID != "AKIAPASTED" || got.SecretAccessKey != "pasted-secret" {
		t.Fatalf("pasted keys were altered: %+v", got)
	}

	// With a profile set, the CLI is consulted — so on a machine without the
	// AWS CLI the call must FAIL rather than silently falling back to the
	// stale pasted keys.
	withProfile := plain
	withProfile.Profile = "does-not-exist-" + t.Name()
	got, err = resolveAWSCredentials(context.Background(), withProfile)
	if err == nil {
		if got.AccessKeyID == "AKIAPASTED" {
			t.Fatal("a named profile silently fell back to the pasted access key")
		}
		t.Skipf("this machine has an AWS profile named %q; nothing to assert", withProfile.Profile)
	}
	if !strings.Contains(err.Error(), withProfile.Profile) && !strings.Contains(err.Error(), "AWS CLI") {
		t.Errorf("err = %v; it should name the profile or the missing CLI", err)
	}
}

// Credentials are cached until shortly before expiry: a 50-request folder run
// must not spawn the AWS CLI fifty times.
func TestAWSCredentialsAreCachedUntilExpiry(t *testing.T) {
	cache := &awsCredCache{m: map[string]awsCachedCred{}}

	live := awsCredentials{
		AccessKeyID: "AKIALIVE", SecretAccessKey: "s",
		Expiration: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	cache.put("p", live)
	if got, ok := cache.get("p"); !ok || got.AccessKeyID != "AKIALIVE" {
		t.Fatalf("a live credential was not cached: %+v ok=%v", got, ok)
	}

	// Inside the skew window it must be treated as already gone, so it cannot
	// expire between signing and reaching AWS.
	cache.put("soon", awsCredentials{
		AccessKeyID: "AKIASOON", SecretAccessKey: "s",
		Expiration: time.Now().Add(20 * time.Second).UTC().Format(time.RFC3339),
	})
	if _, ok := cache.get("soon"); ok {
		t.Error("a credential expiring inside the skew window was served")
	}

	// No expiry reported (a static profile) means cache indefinitely.
	cache.put("static", awsCredentials{AccessKeyID: "AKIASTATIC", SecretAccessKey: "s"})
	if _, ok := cache.get("static"); !ok {
		t.Error("a non-expiring credential was dropped from the cache")
	}
}
