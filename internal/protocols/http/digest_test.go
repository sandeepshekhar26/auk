package http

import (
	"context"
	"crypto/md5" //nolint:gosec // the protocol under test mandates it
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"apitool/internal/auth"
	"apitool/internal/core"
	"apitool/internal/core/model"
	"apitool/internal/templating"
)

// ---------------------------------------------------------------------------
// The referee.
//
// Go's stdlib ships no Digest server, so the only way to prove the client
// computes RFC 7616 correctly is to write a server that VALIDATES the hash
// rather than one that just looks for an Authorization header. Everything
// below is deliberately an INDEPENDENT implementation — its own header
// parser, its own hash selection — so a bug shared with digest.go can't make
// both halves agree on the wrong answer. The referee itself was checked from
// the outside by running this same logic as a standalone server and pointing
// `curl --digest` at it: curl's answers are accepted and a wrong password is
// rejected, so it is a fair judge. TestDigestResponse_MatchesPublishedRFCVectors
// at the bottom of this file closes the loop against the RFC's own numbers.
// ---------------------------------------------------------------------------

type digestReferee struct {
	realm     string
	nonce     string
	opaque    string
	algorithm string // "" → the challenge omits algorithm (client must default to MD5)
	qop       string // "" → an RFC 2069 challenge with no qop at all
	userhash  bool
	user      string
	pass      string

	mu       sync.Mutex
	attempts []refereeAttempt
}

type refereeAttempt struct {
	method string
	body   string
	authz  string
	params map[string]string
	valid  bool
}

func (s *digestReferee) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	authz := r.Header.Get("Authorization")

	att := refereeAttempt{method: r.Method, body: string(body), authz: authz}
	if strings.HasPrefix(authz, "Digest ") {
		att.params = parseRefereeParams(strings.TrimPrefix(authz, "Digest "))
		att.valid = s.validate(r, att.params)
	}
	s.mu.Lock()
	s.attempts = append(s.attempts, att)
	s.mu.Unlock()

	if !att.valid {
		w.Header().Set("WWW-Authenticate", s.challenge())
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("nope"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok:" + string(body)))
}

func (s *digestReferee) challenge() string {
	parts := []string{fmt.Sprintf("realm=%q", s.realm), fmt.Sprintf("nonce=%q", s.nonce)}
	if s.qop != "" {
		parts = append(parts, fmt.Sprintf("qop=%q", s.qop))
	}
	if s.opaque != "" {
		parts = append(parts, fmt.Sprintf("opaque=%q", s.opaque))
	}
	if s.algorithm != "" {
		parts = append(parts, "algorithm="+s.algorithm)
	}
	if s.userhash {
		parts = append(parts, "userhash=true")
	}
	return "Digest " + strings.Join(parts, ", ")
}

func (s *digestReferee) validate(r *http.Request, p map[string]string) bool {
	if p["realm"] != s.realm || p["nonce"] != s.nonce {
		return false
	}
	if p["opaque"] != s.opaque {
		return false
	}
	if p["uri"] != r.URL.RequestURI() {
		return false
	}

	wantUser := s.user
	if s.userhash {
		if p["userhash"] != "true" {
			return false
		}
		wantUser = refereeHash(s.algorithm, s.user+":"+s.realm)
	}
	if refereeUsername(p) != wantUser {
		return false
	}

	ha1 := refereeHash(s.algorithm, s.user+":"+s.realm+":"+s.pass)
	if strings.HasSuffix(strings.ToUpper(s.algorithm), "-SESS") {
		ha1 = refereeHash(s.algorithm, ha1+":"+s.nonce+":"+p["cnonce"])
	}
	ha2 := refereeHash(s.algorithm, r.Method+":"+p["uri"])

	var want string
	if s.qop == "" {
		// RFC 2069 legacy: no qop/nc/cnonce may be present.
		if p["qop"] != "" || p["nc"] != "" || p["cnonce"] != "" {
			return false
		}
		want = refereeHash(s.algorithm, ha1+":"+s.nonce+":"+ha2)
	} else {
		if p["qop"] != "auth" || p["nc"] == "" || p["cnonce"] == "" {
			return false
		}
		want = refereeHash(s.algorithm, ha1+":"+s.nonce+":"+p["nc"]+":"+p["cnonce"]+":"+p["qop"]+":"+ha2)
	}
	return want == p["response"]
}

func (s *digestReferee) recorded() []refereeAttempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]refereeAttempt(nil), s.attempts...)
}

// refereeHash is the server's own H(); "" and anything unrecognised is MD5,
// matching RFC 7616 §3.3's default.
func refereeHash(algorithm, s string) string {
	switch strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(algorithm)), "-SESS") {
	case "SHA-256":
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	case "SHA-512-256":
		sum := sha512.Sum512_256([]byte(s))
		return hex.EncodeToString(sum[:])
	default:
		sum := md5.Sum([]byte(s)) //nolint:gosec // protocol-mandated
		return hex.EncodeToString(sum[:])
	}
}

// refereeUsername resolves either the plain `username` field or the RFC 5987
// extended `username*` form.
func refereeUsername(p map[string]string) string {
	if u, ok := p["username"]; ok {
		return u
	}
	ext, ok := p["username*"]
	if !ok {
		return ""
	}
	_, encoded, found := strings.Cut(ext, "''")
	if !found || !strings.HasPrefix(strings.ToUpper(ext), "UTF-8") {
		return ""
	}
	var out []byte
	for i := 0; i < len(encoded); i++ {
		if encoded[i] == '%' && i+2 < len(encoded) {
			b, err := hex.DecodeString(encoded[i+1 : i+3])
			if err != nil {
				return ""
			}
			out = append(out, b...)
			i += 2
			continue
		}
		out = append(out, encoded[i])
	}
	return string(out)
}

// parseRefereeParams is the server side's own auth-param parser — separate
// from parseChallenges on purpose (see the block comment above).
func parseRefereeParams(s string) map[string]string {
	out := map[string]string{}
	var cur strings.Builder
	inQuotes := false
	flush := func() {
		item := strings.TrimSpace(cur.String())
		cur.Reset()
		key, value, found := strings.Cut(item, "=")
		if !found {
			return
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
		}
		out[strings.ToLower(strings.TrimSpace(key))] = value
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			inQuotes = !inQuotes
			cur.WriteByte(c)
		case c == ',' && !inQuotes:
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mustParseURL is the origin a digestTransport is bound to (see
// clientWithDigestAuth) for tests that build the client directly.
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func digestRequestDef(user, pass string) model.RequestDef {
	return model.RequestDef{
		ID:   "req-digest",
		Auth: &model.AuthConfig{Kind: model.AuthDigest, Digest: &model.DigestAuth{Username: user, Password: pass}},
	}
}

func execDigest(t *testing.T, method, url, user, pass string, body *model.RequestBody) model.ResponseData {
	t.Helper()
	resp, err := New().Execute(context.Background(), nil, digestRequestDef(user, pass), core.ResolvedRequest{
		Method: method,
		URL:    url,
		Body:   body,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return resp
}

// ---------------------------------------------------------------------------
// End-to-end: the client's answer satisfies a validating server
// ---------------------------------------------------------------------------

func TestDigest_AlgorithmMatrix_AuthenticatesAgainstValidatingServer(t *testing.T) {
	for _, tc := range []struct {
		name      string
		algorithm string
		qop       string
	}{
		{"MD5", "MD5", "auth"},
		{"SHA-256", "SHA-256", "auth"},
		{"SHA-512-256", "SHA-512-256", "auth"},
		{"MD5-sess", "MD5-sess", "auth"},
		{"SHA-256-sess", "SHA-256-sess", "auth"},
		{"algorithm omitted defaults to MD5", "", "auth"},
		{"qop list containing auth", "SHA-256", "auth,auth-int"},
		{"RFC 2069 legacy (no qop)", "MD5", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := &digestReferee{
				realm: "testrealm@host.com", nonce: "dcd98b7102dd2f0e8b11d0f600bfb0c093",
				opaque: "5ccc069c403ebaf9f0171e9517f40e41", algorithm: tc.algorithm, qop: tc.qop,
				user: "Mufasa", pass: "Circle Of Life",
			}
			srv := httptest.NewServer(ref)
			defer srv.Close()

			resp := execDigest(t, http.MethodGet, srv.URL+"/dir/index.html", "Mufasa", "Circle Of Life", nil)
			if resp.Status != http.StatusOK {
				t.Fatalf("expected 200 after answering the challenge, got %d (err=%q)", resp.Status, resp.Error)
			}

			attempts := ref.recorded()
			if len(attempts) != 2 {
				t.Fatalf("expected exactly 2 server hits (challenge + authorized retry), got %d", len(attempts))
			}
			if attempts[0].authz != "" {
				t.Errorf("first attempt should be unauthenticated, carried %q", attempts[0].authz)
			}
			if !attempts[1].valid {
				t.Errorf("second attempt did not validate: %q", attempts[1].authz)
			}

			p := attempts[1].params
			if p["opaque"] != ref.opaque {
				t.Errorf("opaque not round-tripped: got %q want %q", p["opaque"], ref.opaque)
			}
			if p["uri"] != "/dir/index.html" {
				t.Errorf("uri = %q, want the request-target /dir/index.html", p["uri"])
			}
			if tc.algorithm != "" && p["algorithm"] != tc.algorithm {
				t.Errorf("algorithm = %q, want the server's own spelling %q", p["algorithm"], tc.algorithm)
			}
			if tc.qop == "" {
				if p["nc"] != "" || p["cnonce"] != "" || p["qop"] != "" {
					t.Errorf("RFC 2069 mode must not send qop/nc/cnonce, got %v", p)
				}
			} else {
				if p["qop"] != "auth" {
					t.Errorf("qop = %q, want auth", p["qop"])
				}
				if p["nc"] != "00000001" {
					t.Errorf("nc = %q, want 00000001 on the first use of a nonce", p["nc"])
				}
				if len(p["cnonce"]) < 16 {
					t.Errorf("cnonce = %q, want a substantial random value", p["cnonce"])
				}
			}
		})
	}
}

func TestDigest_WrongPasswordSurfacesThe401AfterExactlyOneRetry(t *testing.T) {
	ref := &digestReferee{
		realm: "ops", nonce: "n-abc", opaque: "op-1", algorithm: "SHA-256", qop: "auth",
		user: "admin", pass: "correct-horse",
	}
	srv := httptest.NewServer(ref)
	defer srv.Close()

	resp := execDigest(t, http.MethodGet, srv.URL+"/secure", "admin", "wrong-password", nil)
	if resp.Status != http.StatusUnauthorized {
		t.Fatalf("expected the second 401 to reach the user, got %d", resp.Status)
	}

	attempts := ref.recorded()
	if len(attempts) != 2 {
		t.Fatalf("expected exactly 2 attempts (no retry loop on a repeated 401), got %d", len(attempts))
	}
	if attempts[1].authz == "" {
		t.Error("the retry should still have carried an Authorization header")
	}
	if attempts[1].valid {
		t.Error("referee accepted a wrong password — the test server is not validating")
	}
}

func TestDigest_BodyIsReplayedOnTheRetry(t *testing.T) {
	ref := &digestReferee{realm: "api", nonce: "n-body", opaque: "o", algorithm: "SHA-256", qop: "auth", user: "u", pass: "p"}
	srv := httptest.NewServer(ref)
	defer srv.Close()

	payload := `{"name":"widget","qty":3}`
	resp := execDigest(t, http.MethodPost, srv.URL+"/items", "u", "p",
		&model.RequestBody{Kind: model.BodyJSON, Text: payload})

	if resp.Status != http.StatusOK {
		t.Fatalf("expected 200, got %d (err=%q)", resp.Status, resp.Error)
	}
	attempts := ref.recorded()
	if len(attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(attempts))
	}
	for i, att := range attempts {
		if att.body != payload {
			t.Errorf("attempt %d body = %q, want %q — the body must survive the challenge round-trip", i+1, att.body, payload)
		}
		if att.method != http.MethodPost {
			t.Errorf("attempt %d method = %q, want POST", i+1, att.method)
		}
	}
}

// A body handed to the transport as an opaque stream has no GetBody, so it
// can only be replayed if digestTransport buffers it BEFORE the first attempt.
func TestDigest_BuffersNonReplayableBodyBeforeFirstAttempt(t *testing.T) {
	ref := &digestReferee{realm: "api", nonce: "n-stream", opaque: "o", algorithm: "MD5", qop: "auth", user: "u", pass: "p"}
	srv := httptest.NewServer(ref)
	defer srv.Close()

	payload := "streamed-bytes"
	// The embedded-interface wrapper hides *strings.Reader from
	// http.NewRequest's type switch, so GetBody is left nil.
	opaqueBody := struct{ io.Reader }{strings.NewReader(payload)}
	req, err := http.NewRequest(http.MethodPut, srv.URL+"/stream", opaqueBody)
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("precondition failed: this body was supposed to be non-replayable")
	}

	client := clientWithDigestAuth(&http.Client{}, model.DigestAuth{Username: "u", Password: "p"}, mustParseURL(t, srv.URL))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	for i, att := range ref.recorded() {
		if att.body != payload {
			t.Errorf("attempt %d body = %q, want %q", i+1, att.body, payload)
		}
	}
}

func TestDigest_NonDigest401PassesThroughUntouched(t *testing.T) {
	var hits int
	var sawAuthorization bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "" {
			sawAuthorization = true
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="corp"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	resp := execDigest(t, http.MethodGet, srv.URL, "u", "p", nil)
	if resp.Status != http.StatusUnauthorized {
		t.Fatalf("expected the 401 to pass through, got %d", resp.Status)
	}
	if hits != 1 {
		t.Errorf("expected no retry for a non-Digest challenge, server saw %d hits", hits)
	}
	if sawAuthorization {
		t.Error("credentials must not be sent in answer to a Basic challenge")
	}
}

func TestDigest_AuthIntOnlyChallengeIsNotAnswered(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("WWW-Authenticate", `Digest realm="r", nonce="n", qop="auth-int", algorithm=MD5`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	resp := execDigest(t, http.MethodGet, srv.URL, "u", "p", nil)
	if resp.Status != http.StatusUnauthorized {
		t.Fatalf("expected the 401 to reach the user, got %d", resp.Status)
	}
	if hits != 1 {
		t.Errorf("an unanswerable challenge must not be retried, server saw %d hits", hits)
	}
}

// The challenge and the authorized retry are two real round-trips, and the
// request debugger should say so — see digestTransport's ordering comment.
func TestDigest_ChallengeAndRetryBothAppearInTheHopChain(t *testing.T) {
	ref := &digestReferee{realm: "r", nonce: "n-hops", opaque: "o", algorithm: "SHA-256", qop: "auth", user: "u", pass: "p"}
	srv := httptest.NewServer(ref)
	defer srv.Close()

	resp := execDigest(t, http.MethodGet, srv.URL+"/x", "u", "p", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.Status)
	}
	if len(resp.RedirectChain) != 2 {
		t.Fatalf("expected 2 hops (401 challenge, 200 retry), got %d: %+v", len(resp.RedirectChain), resp.RedirectChain)
	}
	if resp.RedirectChain[0].Status != http.StatusUnauthorized || resp.RedirectChain[1].Status != http.StatusOK {
		t.Errorf("hop statuses = %d, %d; want 401 then 200", resp.RedirectChain[0].Status, resp.RedirectChain[1].Status)
	}
	if resp.Timing == nil {
		t.Fatal("expected a timing breakdown for the final (authorized) hop")
	}
}

func TestDigest_NonceCountIncrementsPerNonceAndCnonceIsFresh(t *testing.T) {
	ref := &digestReferee{realm: "r", nonce: "sticky-nonce", opaque: "o", algorithm: "SHA-256", qop: "auth", user: "u", pass: "p"}
	srv := httptest.NewServer(ref)
	defer srv.Close()

	client := clientWithDigestAuth(&http.Client{}, model.DigestAuth{Username: "u", Password: "p"}, mustParseURL(t, srv.URL))
	for i := 0; i < 2; i++ {
		resp, err := client.Get(srv.URL + "/x")
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: got %d", i+1, resp.StatusCode)
		}
	}

	attempts := ref.recorded()
	if len(attempts) != 4 {
		t.Fatalf("expected 4 hits (challenge+retry, twice), got %d", len(attempts))
	}
	first, second := attempts[1].params, attempts[3].params
	if first["nc"] != "00000001" || second["nc"] != "00000002" {
		t.Errorf("nc did not increment across uses of one nonce: %q then %q", first["nc"], second["nc"])
	}
	if first["cnonce"] == "" || first["cnonce"] == second["cnonce"] {
		t.Errorf("each authorization needs its own fresh cnonce, got %q and %q", first["cnonce"], second["cnonce"])
	}
}

func TestDigest_UserhashSendsHashedUsername(t *testing.T) {
	ref := &digestReferee{
		realm: "api", nonce: "n-uh", opaque: "o", algorithm: "SHA-256", qop: "auth",
		userhash: true, user: "jsmith", pass: "secret",
	}
	srv := httptest.NewServer(ref)
	defer srv.Close()

	resp := execDigest(t, http.MethodGet, srv.URL+"/x", "jsmith", "secret", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("expected 200, got %d (err=%q)", resp.Status, resp.Error)
	}
	p := ref.recorded()[1].params
	if p["username"] == "jsmith" {
		t.Error("userhash=true must send the hashed username, not the plain one")
	}
	if p["userhash"] != "true" {
		t.Errorf("userhash echo = %q, want true", p["userhash"])
	}
}

func TestDigest_NonASCIIUsernameUsesExtendedField(t *testing.T) {
	ref := &digestReferee{realm: "api", nonce: "n-utf8", opaque: "o", algorithm: "SHA-256", qop: "auth", user: "jäsmith", pass: "secret"}
	srv := httptest.NewServer(ref)
	defer srv.Close()

	resp := execDigest(t, http.MethodGet, srv.URL+"/x", "jäsmith", "secret", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("expected 200, got %d (err=%q)", resp.Status, resp.Error)
	}
	p := ref.recorded()[1].params
	if _, plain := p["username"]; plain {
		t.Error("a non-ASCII username must go in username*, not the quoted username field")
	}
	if got := p["username*"]; !strings.HasPrefix(got, "UTF-8''") {
		t.Errorf("username* = %q, want the RFC 5987 UTF-8'' form", got)
	}
}

// ---------------------------------------------------------------------------
// Challenge parsing
// ---------------------------------------------------------------------------

func TestParseChallenges_Table(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header string
		want   []rawChallenge
	}{
		{
			name:   "single digest challenge",
			header: `Digest realm="testrealm@host.com", qop="auth", nonce="abc", opaque="xyz"`,
			want: []rawChallenge{{scheme: "Digest", params: map[string]string{
				"realm": "testrealm@host.com", "qop": "auth", "nonce": "abc", "opaque": "xyz",
			}}},
		},
		{
			name:   "commas inside a quoted value do not split the challenge",
			header: `Digest realm="Acme, Inc. \"Prod\"", nonce="a,b,c", qop="auth,auth-int"`,
			want: []rawChallenge{{scheme: "Digest", params: map[string]string{
				"realm": `Acme, Inc. "Prod"`, "nonce": "a,b,c", "qop": "auth,auth-int",
			}}},
		},
		{
			name:   "two schemes in one header",
			header: `Basic realm="corp", Digest realm="dig", nonce="n1", algorithm=SHA-256`,
			want: []rawChallenge{
				{scheme: "Basic", params: map[string]string{"realm": "corp"}},
				{scheme: "Digest", params: map[string]string{"realm": "dig", "nonce": "n1", "algorithm": "SHA-256"}},
			},
		},
		{
			name:   "scheme with a token68 credential and no params",
			header: `Negotiate, Digest realm="r", nonce="n"`,
			want: []rawChallenge{
				{scheme: "Negotiate", params: map[string]string{}},
				{scheme: "Digest", params: map[string]string{"realm": "r", "nonce": "n"}},
			},
		},
		{
			name:   "RFC 7235 example: unknown scheme with unquoted and escaped params",
			header: `Newauth realm="apps", type=1, title="Login to \"apps\"", Basic realm="simple"`,
			want: []rawChallenge{
				{scheme: "Newauth", params: map[string]string{"realm": "apps", "type": "1", "title": `Login to "apps"`}},
				{scheme: "Basic", params: map[string]string{"realm": "simple"}},
			},
		},
		{
			name:   "unquoted token values and extra whitespace",
			header: `Digest  realm=plain ,  qop=auth ,  stale=TRUE , algorithm = MD5-sess`,
			want: []rawChallenge{{scheme: "Digest", params: map[string]string{
				"realm": "plain", "qop": "auth", "stale": "TRUE", "algorithm": "MD5-sess",
			}}},
		},
		{
			name:   "digest challenge with no qop at all (RFC 2069)",
			header: `Digest realm="legacy", nonce="n", opaque="o"`,
			want: []rawChallenge{{scheme: "Digest", params: map[string]string{
				"realm": "legacy", "nonce": "n", "opaque": "o",
			}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseChallenges(tc.header)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseChallenges(%q)\n got %+v\nwant %+v", tc.header, got, tc.want)
			}
		})
	}
}

func TestDigestChallengeFrom_SelectionRules(t *testing.T) {
	t.Run("finds the digest challenge among other schemes", func(t *testing.T) {
		h := http.Header{"Www-Authenticate": {`Basic realm="corp", Digest realm="r", nonce="n", qop="auth"`}}
		ch, ok := digestChallengeFrom(h)
		if !ok || ch.realm != "r" || ch.nonce != "n" {
			t.Fatalf("got %+v ok=%v", ch, ok)
		}
	})

	t.Run("header name case does not matter", func(t *testing.T) {
		h := http.Header{"www-authenticate": {`Digest realm="r", nonce="n", qop="auth"`}}
		if _, ok := digestChallengeFrom(h); !ok {
			t.Fatal("a non-canonical header key hid the challenge")
		}
	})

	t.Run("prefers the strongest algorithm across separate headers", func(t *testing.T) {
		h := http.Header{"Www-Authenticate": {
			`Digest realm="r", nonce="weak", qop="auth", algorithm=MD5`,
			`Digest realm="r", nonce="strong", qop="auth", algorithm=SHA-256`,
		}}
		ch, ok := digestChallengeFrom(h)
		if !ok || ch.nonce != "strong" {
			t.Fatalf("expected the SHA-256 challenge, got %+v", ch)
		}
	})

	t.Run("skips challenges it cannot answer", func(t *testing.T) {
		h := http.Header{"Www-Authenticate": {
			`Digest realm="r", nonce="unknown-alg", qop="auth", algorithm=SHA3-512`,
			`Digest realm="r", nonce="int-only", qop="auth-int", algorithm=SHA-256`,
			`Digest realm="r", nonce="usable", qop="auth", algorithm=MD5`,
		}}
		ch, ok := digestChallengeFrom(h)
		if !ok || ch.nonce != "usable" {
			t.Fatalf("expected the answerable MD5 challenge, got %+v ok=%v", ch, ok)
		}
	})

	t.Run("stale and userhash flags are parsed", func(t *testing.T) {
		h := http.Header{"Www-Authenticate": {`Digest realm="r", nonce="n", qop="auth", stale=true, userhash=TRUE`}}
		ch, _ := digestChallengeFrom(h)
		if !ch.stale || !ch.userhash {
			t.Fatalf("stale=%v userhash=%v, want both true", ch.stale, ch.userhash)
		}
	})

	t.Run("no challenge at all", func(t *testing.T) {
		if _, ok := digestChallengeFrom(http.Header{}); ok {
			t.Fatal("found a challenge in an empty header set")
		}
	})
}

// ---------------------------------------------------------------------------
// The other half of the seam: auth.Apply must leave a Digest request alone.
// ---------------------------------------------------------------------------

func TestAuthApply_DigestPassesThroughUntouched(t *testing.T) {
	in := core.ResolvedRequest{Method: http.MethodGet, URL: "https://example.com/x"}
	cfg := model.AuthConfig{Kind: model.AuthDigest, Digest: &model.DigestAuth{Username: "u", Password: "p"}}

	out, err := auth.New().Apply(context.Background(), cfg, in)
	if err != nil {
		t.Fatalf("Apply must not reject the digest kind (the transport handles it): %v", err)
	}
	for _, h := range out.Headers {
		if strings.EqualFold(h.Key, "Authorization") {
			t.Errorf("Apply must not precompute an Authorization header for digest, got %q", h.Value)
		}
	}
	if out.URL != in.URL {
		t.Errorf("URL changed: %q → %q", in.URL, out.URL)
	}
}

// ---------------------------------------------------------------------------
// Published test vectors.
//
// The referee above proves the client and the server agree; these prove the
// client agrees with the RFC itself, which no amount of self-consistency can.
// ---------------------------------------------------------------------------

func TestDigestResponse_MatchesPublishedRFCVectors(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		algorithm                                string
		realm, nonce, username, password, cnonce string
		method, uri, nc, qop                     string
		wantHA1, wantHA2, wantResponse           string
	}{
		{
			// RFC 2617 §3.5, the canonical worked example every Digest
			// implementation is checked against.
			name:      "RFC 2617 §3.5 (MD5)",
			algorithm: "MD5",
			realm:     "testrealm@host.com",
			nonce:     "dcd98b7102dd2f0e8b11d0f600bfb0c093",
			username:  "Mufasa",
			password:  "Circle Of Life",
			cnonce:    "0a4f113b",
			method:    "GET", uri: "/dir/index.html", nc: "00000001", qop: "auth",
			wantHA1:      "939e7578ed9e3c518a452acee763bce9",
			wantHA2:      "39aff3a2bab6126f332b942af96d3366",
			wantResponse: "6629fae49393a05397450978507c4ef1",
		},
		{
			// RFC 7616 §3.9.1 — the same request answered with both offered
			// algorithms. Note the password is "Circle of Life" here (lower
			// case "of"), unlike RFC 2617's.
			name:      "RFC 7616 §3.9.1 (MD5)",
			algorithm: "MD5",
			realm:     "http-auth@example.org",
			nonce:     "7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v",
			username:  "Mufasa",
			password:  "Circle of Life",
			cnonce:    "f2/wE4q74E6zIJEtWaHKaf5wv/H5QzzpXusqGemxURZJ",
			method:    "GET", uri: "/dir/index.html", nc: "00000001", qop: "auth",
			wantResponse: "8ca523f5e9506fed4657c9700eebdbec",
		},
		{
			name:      "RFC 7616 §3.9.1 (SHA-256)",
			algorithm: "SHA-256",
			realm:     "http-auth@example.org",
			nonce:     "7ypf/xlj9XXwfDPEoM4URrv/xwf94BcCAzFZH4GiTo0v",
			username:  "Mufasa",
			password:  "Circle of Life",
			cnonce:    "f2/wE4q74E6zIJEtWaHKaf5wv/H5QzzpXusqGemxURZJ",
			method:    "GET", uri: "/dir/index.html", nc: "00000001", qop: "auth",
			wantResponse: "753927fa0e85d155564e2e272a28d1802ca10daf4496794697cf8db5856cb6c1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, sess, ok := hashFor(tc.algorithm)
			if !ok {
				t.Fatalf("hashFor(%q) reported the algorithm as unsupported", tc.algorithm)
			}
			ch := digestChallenge{realm: tc.realm, nonce: tc.nonce, algorithm: tc.algorithm}

			if tc.wantHA1 != "" {
				if got := h(tc.username + ":" + tc.realm + ":" + tc.password); got != tc.wantHA1 {
					t.Errorf("HA1 = %s, want %s", got, tc.wantHA1)
				}
			}
			if tc.wantHA2 != "" {
				if got := h(tc.method + ":" + tc.uri); got != tc.wantHA2 {
					t.Errorf("HA2 = %s, want %s", got, tc.wantHA2)
				}
			}
			got := digestResponse(h, sess, tc.username, tc.password, ch, tc.method, tc.uri, tc.cnonce, tc.nc, tc.qop)
			if got != tc.wantResponse {
				t.Errorf("response = %s, want %s", got, tc.wantResponse)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FINDING 2 [CRITICAL]: Digest must hash the RESOLVED credentials.
//
// auth.Apply's digest case is a deliberate no-op, so the templated copy
// ResolveAuth produces used to die there and this file read the RAW
// model.RequestDef instead — hashing the literal string "${digestPassword}"
// and failing against every real server. core.ResolvedRequest.Auth is the seam
// that carries the resolved copy down to the transport.
// ---------------------------------------------------------------------------

// digestEngineStore is the smallest core.Store that can drive one request
// against one environment.
type digestEngineStore struct {
	req model.RequestDef
	env model.Environment
}

func (s digestEngineStore) GetRequest(model.ID) (model.RequestDef, error) { return s.req, nil }
func (s digestEngineStore) GetEnvironment(model.ID) (*model.Environment, error) {
	e := s.env
	return &e, nil
}
func (s digestEngineStore) SaveResponse(model.ResponseData) error  { return nil }
func (s digestEngineStore) AppendHistory(model.HistoryEntry) error { return nil }
func (s digestEngineStore) LookupRequestByName(model.ID, string) (model.RequestDef, error) {
	return model.RequestDef{}, fmt.Errorf("no request lookup in this fixture")
}
func (s digestEngineStore) ListFolders(model.ID) []model.Folder { return nil }

// TestDigest_TemplatedCredentialsResolveBeforeHashing runs the whole engine —
// real templater, real auth applier, real HTTP protocol — against the
// validating referee. `${digestPassword}` must authenticate.
func TestDigest_TemplatedCredentialsResolveBeforeHashing(t *testing.T) {
	const user, pass = "Mufasa", "Circle Of Life"
	ref := &digestReferee{
		realm: "testrealm@host.com", nonce: "n-templated", opaque: "o",
		algorithm: "SHA-256", qop: "auth", user: user, pass: pass,
	}
	srv := httptest.NewServer(ref)
	defer srv.Close()

	stored := &model.AuthConfig{Kind: model.AuthDigest, Digest: &model.DigestAuth{
		Username: "${digestUser}", Password: "${digestPassword}",
	}}
	store := digestEngineStore{
		req: model.RequestDef{
			ID: "r1", WorkspaceID: "ws1", Name: "Digest", Protocol: model.ProtocolHTTP,
			Method: http.MethodGet, URL: srv.URL + "/dir/index.html", Auth: stored,
		},
		env: model.Environment{ID: "env1", WorkspaceID: "ws1", Variables: []model.KeyValue{
			{Key: "digestUser", Value: user, Enabled: true},
			{Key: "digestPassword", Value: pass, Enabled: true},
		}},
	}

	engine := core.NewEngine(store, nil, auth.New(), nil)
	engine.Templater = templating.New(engine)
	engine.RegisterProtocol(New())

	resp, err := engine.RunRequest(context.Background(), "s1", "r1", "env1", "gui", core.NoopSink{})
	if err != nil {
		t.Fatalf("RunRequest: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("a templated Digest password must authenticate, got %d (err=%q)", resp.Status, resp.Error)
	}
	attempts := ref.recorded()
	if len(attempts) != 2 || !attempts[1].valid {
		t.Fatalf("expected challenge + valid retry, got %d attempts (last valid=%v)", len(attempts), attempts[len(attempts)-1].valid)
	}
	if refereeUsername(attempts[1].params) != user {
		t.Errorf("username sent = %q, want the RESOLVED %q", refereeUsername(attempts[1].params), user)
	}
	// The store's own AuthConfig is a pointer; resolution must never rewrite it.
	if stored.Digest.Password != "${digestPassword}" || stored.Digest.Username != "${digestUser}" {
		t.Errorf("the stored credentials were mutated: %+v", stored.Digest)
	}
}

// TestDigest_ResolvedAuthBeatsTheRawStoredAuth pins the precedence rule on its
// own: with both present, the protocol uses resolved.Auth. Without it the raw
// `${pw}` would be hashed and the server would reject the answer.
func TestDigest_ResolvedAuthBeatsTheRawStoredAuth(t *testing.T) {
	ref := &digestReferee{realm: "r", nonce: "n-precedence", opaque: "o", algorithm: "MD5", qop: "auth", user: "ada", pass: "real-pw"}
	srv := httptest.NewServer(ref)
	defer srv.Close()

	raw := model.RequestDef{ID: "r1", Auth: &model.AuthConfig{
		Kind: model.AuthDigest, Digest: &model.DigestAuth{Username: "${u}", Password: "${pw}"},
	}}
	resp, err := New().Execute(context.Background(), nil, raw, core.ResolvedRequest{
		Method: http.MethodGet, URL: srv.URL + "/x",
		Auth: &model.AuthConfig{
			Kind: model.AuthDigest, Digest: &model.DigestAuth{Username: "ada", Password: "real-pw"},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("resolved.Auth must win over the raw stored auth, got %d", resp.Status)
	}
}

// ---------------------------------------------------------------------------
// FINDING 4 [MAJOR]: Digest credentials must not follow a redirect.
//
// http.Client follows redirects by default, so a legitimate host could 302 to
// an attacker's, which answers with a Digest challenge whose realm and nonce
// IT chose — and the client would hand over the username plus a digest over
// attacker-controlled inputs. See RoundTrip's ORIGIN BINDING note.
// ---------------------------------------------------------------------------

func TestDigest_CrossHostRedirectChallengeIsNotAnswered(t *testing.T) {
	var (
		mu           sync.Mutex
		attackerHits int
		stolen       string
	)
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attackerHits++
		if v := r.Header.Get("Authorization"); v != "" {
			stolen = v
		}
		mu.Unlock()
		// A challenge entirely of the attacker's choosing.
		w.Header().Set("WWW-Authenticate", `Digest realm="attacker-chosen", nonce="attacker-nonce", qop="auth", algorithm=MD5`)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("give me your hash"))
	}))
	defer attacker.Close()

	legit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/loot", http.StatusFound)
	}))
	defer legit.Close()

	resp := execDigest(t, http.MethodGet, legit.URL+"/start", "alice", "hunter2", nil)

	mu.Lock()
	defer mu.Unlock()
	if stolen != "" {
		t.Fatalf("Digest credentials were answered to a redirect target: %q", stolen)
	}
	if attackerHits != 1 {
		t.Errorf("the redirect target must be hit exactly once (no authorized retry), got %d", attackerHits)
	}
	if resp.Status != http.StatusUnauthorized {
		t.Errorf("the unanswered 401 must surface to the user, got %d", resp.Status)
	}
}

// The rule is about ORIGIN, not about redirects: a redirect that stays on the
// host the user addressed still authenticates, so guarded endpoints reached
// via an in-host redirect keep working.
func TestDigest_SameHostRedirectChallengeIsStillAnswered(t *testing.T) {
	ref := &digestReferee{realm: "r", nonce: "n-same-host", opaque: "o", algorithm: "SHA-256", qop: "auth", user: "u", pass: "p"}
	mux := http.NewServeMux()
	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/guarded", http.StatusFound)
	})
	mux.Handle("/guarded", ref)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp := execDigest(t, http.MethodGet, srv.URL+"/start", "u", "p", nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("a same-origin redirect must still authenticate, got %d (err=%q)", resp.Status, resp.Error)
	}
	attempts := ref.recorded()
	if len(attempts) != 2 || !attempts[1].valid {
		t.Fatalf("expected challenge + valid retry at /guarded, got %d attempts", len(attempts))
	}
}

func TestSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, a, b string
		want       bool
	}{
		{"identical", "https://api.test/x", "https://api.test/y", true},
		{"default port is implied", "https://api.test/x", "https://api.test:443/x", true},
		{"http default port is implied", "http://api.test/x", "http://api.test:80/x", true},
		{"case-insensitive host and scheme", "HTTPS://API.test/x", "https://api.TEST/x", true},
		{"different host", "https://api.test/x", "https://evil.test/x", false},
		{"subdomain is a different host", "https://api.test/x", "https://www.api.test/x", false},
		{"different port", "http://127.0.0.1:8080/x", "http://127.0.0.1:9090/x", false},
		{"different scheme", "https://api.test/x", "http://api.test/x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := mustParseURL(t, tc.a), mustParseURL(t, tc.b)
			if got := sameOrigin(a, b); got != tc.want {
				t.Errorf("sameOrigin(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
	if sameOrigin(nil, mustParseURL(t, "https://api.test")) || sameOrigin(mustParseURL(t, "https://api.test"), nil) {
		t.Error("a nil URL can never match an origin")
	}
}
