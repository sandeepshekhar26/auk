package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"apitool/internal/core"
	"apitool/internal/core/model"
)

// fakeIdP is a minimal OAuth2 provider: an authorize endpoint that verifies
// the PKCE challenge arrived and redirects back with a code, and a token
// endpoint that verifies the verifier matches before issuing tokens. It
// checks the exact things a real IdP would refuse on, so a drift in what AUK
// sends fails here rather than against Auth0.
type fakeIdP struct {
	t  *testing.T
	mu sync.Mutex

	// captured from the authorize request
	challenge   string
	state       string
	redirectURI string
	audience    string

	code string

	// knobs
	issueRefresh       bool
	rotateRefresh      bool
	refuseRefresh      bool
	refreshUnavailable bool // 503 temporarily_unavailable — a transient outage
	accessLifetime     time.Duration

	refreshCalls int
	usedRefresh  map[string]bool

	srv *httptest.Server
}

func newFakeIdP(t *testing.T) *fakeIdP {
	f := &fakeIdP{t: t, code: "code-" + t.Name(), issueRefresh: true, accessLifetime: time.Hour, usedRefresh: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", f.authorize)
	mux.HandleFunc("/token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) cfg() model.OAuth2Auth {
	return model.OAuth2Auth{
		GrantType: model.OAuth2GrantAuthorizationCode,
		ClientID:  "auk-test-client",
		AuthURL:   f.srv.URL + "/authorize",
		TokenURL:  f.srv.URL + "/token",
		Scopes:    []string{"openid", "offline_access"},
	}
}

// authorize plays the IdP's half of the front channel: record what the app
// asked for, then redirect the "browser" to the app's loopback callback.
func (f *fakeIdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.challenge = q.Get("code_challenge")
	f.state = q.Get("state")
	f.redirectURI = q.Get("redirect_uri")
	f.audience = q.Get("audience")
	f.mu.Unlock()

	if q.Get("code_challenge_method") != "S256" {
		f.t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("response_type") != "code" {
		f.t.Errorf("response_type = %q, want code", q.Get("response_type"))
	}
	u, _ := url.Parse(q.Get("redirect_uri"))
	cb := u.Query()
	cb.Set("code", f.code)
	cb.Set("state", q.Get("state"))
	u.RawQuery = cb.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (f *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))

	w.Header().Set("Content-Type", "application/json")

	switch form.Get("grant_type") {
	case "authorization_code":
		// A real IdP refuses an exchange whose redirect_uri or client_id does
		// not match the authorization request (RFC 6749 §4.1.3). Enforcing it
		// here keeps the client honest about sending both.
		f.mu.Lock()
		front := f.redirectURI
		f.mu.Unlock()
		if form.Get("redirect_uri") != front {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"redirect_uri mismatch"}`)
			return
		}
		if form.Get("client_id") != "auk-test-client" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_client"}`)
			return
		}
		if form.Get("code") != f.code {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		// The PKCE check that makes an intercepted code useless: the
		// verifier must hash to the challenge captured on the front channel.
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"pkce verification failed"}`)
			return
		}
		resp := map[string]any{
			"access_token": "access-1",
			"token_type":   "Bearer",
			"expires_in":   int(f.accessLifetime.Seconds()),
		}
		if f.issueRefresh {
			resp["refresh_token"] = "refresh-1"
		}
		_ = json.NewEncoder(w).Encode(resp)

	case "refresh_token":
		f.mu.Lock()
		f.refreshCalls++
		n := f.refreshCalls
		f.mu.Unlock()
		if f.refuseRefresh {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"revoked"}`)
			return
		}
		if f.refreshUnavailable {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"temporarily_unavailable"}`)
			return
		}
		got := form.Get("refresh_token")
		if !strings.HasPrefix(got, "refresh-") {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		// Rotation semantics: each refresh token works ONCE. This is what
		// makes persisting the stale token (instead of the rotated one) a
		// test failure rather than a silent pass — reuse is how Auth0-style
		// detection reads theft.
		if f.rotateRefresh {
			f.mu.Lock()
			reused := f.usedRefresh[got]
			f.usedRefresh[got] = true
			f.mu.Unlock()
			if reused {
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"refresh token reuse detected"}`)
				return
			}
		}
		resp := map[string]any{
			"access_token": fmt.Sprintf("access-refreshed-%d", n),
			"token_type":   "Bearer",
			"expires_in":   3600,
		}
		if f.rotateRefresh {
			resp["refresh_token"] = fmt.Sprintf("refresh-%d", n+1)
		}
		_ = json.NewEncoder(w).Encode(resp)

	default:
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"unsupported_grant_type"}`)
	}
}

// browserFollow acts as the user's browser: GET the authorize URL, follow the
// redirect chain to the loopback callback, return the final page.
func browserFollow(t *testing.T) func(string) error {
	return func(authURL string) error {
		t.Helper()
		go func() {
			resp, err := http.Get(authURL)
			if err != nil {
				t.Errorf("browser: %v", err)
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				t.Errorf("callback page status %d: %s", resp.StatusCode, body)
			}
		}()
		return nil
	}
}

func TestAuthCodeSignInEndToEnd(t *testing.T) {
	idp := newFakeIdP(t)
	ap := New()

	res, err := ap.SignInAuthorizationCode(context.Background(), idp.cfg(), browserFollow(t))
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if !res.HasRefresh {
		t.Fatal("HasRefresh = false, want true (IdP issued one)")
	}

	// The redirect target must be loopback — never a routable interface.
	if !strings.HasPrefix(idp.redirectURI, "http://127.0.0.1:") {
		t.Fatalf("redirect_uri = %q, want loopback", idp.redirectURI)
	}

	// And Apply must now attach the token with no further network involved
	// (the IdP would fail the request since we don't handle more grants).
	req, err := ap.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: ptr(idp.cfg())}, reqIn())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	assertBearer(t, req, "access-1")
}

func TestAudienceIsForwardedToTheAuthorizeRequest(t *testing.T) {
	idp := newFakeIdP(t)
	cfg := idp.cfg()
	cfg.Audience = "https://api.example.com"
	ap := New()
	if _, err := ap.SignInAuthorizationCode(context.Background(), cfg, browserFollow(t)); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if idp.audience != "https://api.example.com" {
		t.Fatalf("audience = %q, want it forwarded", idp.audience)
	}
}

// The CSRF core: a callback whose state does not match must not complete,
// abort, or otherwise influence the flow — and the real callback arriving
// afterwards must still succeed.
func TestForgedCallbackCannotCompleteOrAbortTheFlow(t *testing.T) {
	idp := newFakeIdP(t)
	ap := New()

	browser := func(authURL string) error {
		go func() {
			u, _ := url.Parse(authURL)
			redirect := u.Query().Get("redirect_uri")

			// A hostile local page sprays the loopback port first: wrong
			// state, attacker-controlled code.
			forged := redirect + "?code=attacker-code&state=wrong"
			resp, err := http.Get(forged)
			if err == nil {
				if resp.StatusCode != http.StatusForbidden {
					t.Errorf("forged callback got %d, want 403", resp.StatusCode)
				}
				resp.Body.Close()
			}

			// Then the legitimate redirect chain runs.
			resp2, err := http.Get(authURL)
			if err != nil {
				t.Errorf("browser: %v", err)
				return
			}
			resp2.Body.Close()
		}()
		return nil
	}

	if _, err := ap.SignInAuthorizationCode(context.Background(), idp.cfg(), browser); err != nil {
		t.Fatalf("sign in should have completed despite the forged callback: %v", err)
	}
	// The token minted must be the one from the REAL code, which the fake IdP
	// only issues for its own code value — the attacker's never reached
	// exchange (the IdP would have 400'd it).
	req, err := ap.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: ptr(idp.cfg())}, reqIn())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	assertBearer(t, req, "access-1")
}

// The callback page must never reflect request input: whatever the query
// carries, the HTML that renders in the user's browser is the static page.
func TestCallbackPageReflectsNothing(t *testing.T) {
	idp := newFakeIdP(t)
	ap := New()
	payload := `"><script>alert(1)</script>`
	// The SUCCESS page renders after a state-valid callback whose `code` is
	// whatever the IdP redirected with — so make the legitimate code itself
	// hostile. If any part of the query were echoed, this is where it shows.
	idp.code = payload

	browser := func(authURL string) error {
		go func() {
			u, _ := url.Parse(authURL)
			redirect := u.Query().Get("redirect_uri")
			state := "" // unknown to the attacker

			// Probe both the state-fail page and (via the real flow below)
			// the success page.
			resp, err := http.Get(redirect + "?state=" + url.QueryEscape(state) + "&code=" + url.QueryEscape(payload) + "&error=" + url.QueryEscape(payload))
			if err == nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if strings.Contains(string(body), "<script>alert(1)") {
					t.Error("callback page reflected attacker input")
				}
			}
			resp2, err := http.Get(authURL)
			if err == nil {
				body, _ := io.ReadAll(resp2.Body)
				resp2.Body.Close()
				if strings.Contains(string(body), "<script>alert(1)") {
					t.Error("success page reflected attacker input")
				}
			}
		}()
		return nil
	}
	if _, err := ap.SignInAuthorizationCode(context.Background(), idp.cfg(), browser); err != nil {
		t.Fatalf("sign in: %v", err)
	}
}

func TestProviderDenialSurfacesAsAnError(t *testing.T) {
	idp := newFakeIdP(t)
	ap := New()
	browser := func(authURL string) error {
		go func() {
			u, _ := url.Parse(authURL)
			redirect := u.Query().Get("redirect_uri")
			state := u.Query().Get("state")
			resp, err := http.Get(redirect + "?error=access_denied&state=" + url.QueryEscape(state))
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	_, err := ap.SignInAuthorizationCode(context.Background(), idp.cfg(), browser)
	if err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want the provider's refusal surfaced", err)
	}
}

func TestExpiredTokenRefreshesSilentlyAndPersists(t *testing.T) {
	idp := newFakeIdP(t)
	idp.rotateRefresh = true
	store := newFakeSecretStore()
	ap := NewWithSecretStore(store)

	if _, err := ap.SignInAuthorizationCode(context.Background(), idp.cfg(), browserFollow(t)); err != nil {
		t.Fatalf("sign in: %v", err)
	}

	// Corrupt time: force the cached token to look expired.
	key := oauth2Fingerprint(idp.cfg())
	tok := ap.tokens.get(key)
	tok.Expiry = time.Now().Add(-time.Minute)
	ap.tokens.put(key, tok, true)

	req, err := ap.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: ptr(idp.cfg())}, reqIn())
	if err != nil {
		t.Fatalf("apply with expired token: %v", err)
	}
	assertBearer(t, req, "access-refreshed-1")
	if idp.refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", idp.refreshCalls)
	}

	// The rotated refresh token must have been persisted: a NEW applier over
	// the same secret store (an app restart) must refresh with refresh-2.
	tok2 := ap.tokens.get(key)
	tok2.Expiry = time.Now().Add(-time.Minute)
	ap.tokens.put(key, tok2, true)

	ap2 := NewWithSecretStore(store)
	req2, err := ap2.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: ptr(idp.cfg())}, reqIn())
	if err != nil {
		t.Fatalf("apply after restart: %v", err)
	}
	assertBearer(t, req2, "access-refreshed-2")
}

func TestRefusedRefreshDropsTheTokenAndAsksForSignIn(t *testing.T) {
	idp := newFakeIdP(t)
	ap := New()
	if _, err := ap.SignInAuthorizationCode(context.Background(), idp.cfg(), browserFollow(t)); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	key := oauth2Fingerprint(idp.cfg())
	tok := ap.tokens.get(key)
	tok.Expiry = time.Now().Add(-time.Minute)
	ap.tokens.put(key, tok, false)
	idp.refuseRefresh = true

	_, err := ap.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: ptr(idp.cfg())}, reqIn())
	if err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("err = %v, want a sign-in-again message", err)
	}
	// And the dead token is gone, so the UI's status flips to signed-out.
	if st := ap.SignInStatus(idp.cfg()); st.SignedIn {
		t.Fatal("still reports signed in after a refused refresh")
	}
}

func TestApplyNeverLaunchesABrowser(t *testing.T) {
	// No token stored: Apply must fail with the sign-in sentence, not hang or
	// attempt anything interactive.
	idp := newFakeIdP(t)
	ap := New()
	_, err := ap.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: ptr(idp.cfg())}, reqIn())
	if !errors.Is(err, ErrOAuth2NotSignedIn) {
		t.Fatalf("err = %v, want ErrOAuth2NotSignedIn", err)
	}
}

func TestClientCredentialsTokensAreCached(t *testing.T) {
	var mints int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		mints++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"cc-token","token_type":"Bearer","expires_in":3600}`)
	}))
	defer srv.Close()

	cfg := model.OAuth2Auth{ClientID: "id", ClientSecret: "secret", TokenURL: srv.URL}
	ap := New()
	for i := 0; i < 5; i++ {
		req, err := ap.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: &cfg}, reqIn())
		if err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
		assertBearer(t, req, "cc-token")
	}
	if mints != 1 {
		t.Fatalf("token endpoint hit %d times for 5 sends, want 1 (caching)", mints)
	}
}

func TestFingerprintSeparatesWhatWasAuthorized(t *testing.T) {
	base := model.OAuth2Auth{GrantType: model.OAuth2GrantAuthorizationCode, ClientID: "c", AuthURL: "https://a", TokenURL: "https://t", Scopes: []string{"read"}}
	same := base
	if oauth2Fingerprint(base) != oauth2Fingerprint(same) {
		t.Fatal("identical configs produced different fingerprints")
	}
	for name, mutate := range map[string]func(*model.OAuth2Auth){
		"clientID":     func(o *model.OAuth2Auth) { o.ClientID = "other" },
		"scopes":       func(o *model.OAuth2Auth) { o.Scopes = []string{"read", "write"} },
		"tokenURL":     func(o *model.OAuth2Auth) { o.TokenURL = "https://t2" },
		"authURL":      func(o *model.OAuth2Auth) { o.AuthURL = "https://a2" },
		"clientSecret": func(o *model.OAuth2Auth) { o.ClientSecret = "rotated" },
		"grantType":    func(o *model.OAuth2Auth) { o.GrantType = model.OAuth2GrantClientCredentials },
		"audience":     func(o *model.OAuth2Auth) { o.Audience = "https://api" },
	} {
		m := base
		mutate(&m)
		if oauth2Fingerprint(base) == oauth2Fingerprint(m) {
			t.Errorf("changing %s did not change the fingerprint", name)
		}
	}
}

func TestSignOutForgetsTheToken(t *testing.T) {
	idp := newFakeIdP(t)
	store := newFakeSecretStore()
	ap := NewWithSecretStore(store)
	if _, err := ap.SignInAuthorizationCode(context.Background(), idp.cfg(), browserFollow(t)); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	ap.SignOut(idp.cfg())
	if st := ap.SignInStatus(idp.cfg()); st.SignedIn || st.Expired {
		t.Fatalf("status after sign-out = %+v, want empty", st)
	}
	// And a fresh applier over the same store must not resurrect it.
	if st := NewWithSecretStore(store).SignInStatus(idp.cfg()); st.SignedIn {
		t.Fatal("sign-out did not remove the keychain copy")
	}
}

// ---- helpers ----

func ptr[T any](v T) *T { return &v }

func reqIn() core.ResolvedRequest {
	return core.ResolvedRequest{URL: "https://api.example.com/v1/things", Method: "GET"}
}

func assertBearer(t *testing.T, req core.ResolvedRequest, token string) {
	t.Helper()
	for _, h := range req.Headers {
		if h.Key == "Authorization" && h.Value == "Bearer "+token {
			return
		}
	}
	t.Fatalf("no Authorization: Bearer %s header in %+v", token, req.Headers)
}

// fakeSecretStore is an in-memory SecretStore for tests (the real one pops
// macOS keychain dialogs).
type fakeSecretStore struct {
	mu sync.Mutex
	m  map[string]string
}

func newFakeSecretStore() *fakeSecretStore { return &fakeSecretStore{m: map[string]string{}} }

func (f *fakeSecretStore) Get(service, account string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.m[service+"/"+account]
	if !ok {
		return "", errors.New("not found")
	}
	return v, nil
}
func (f *fakeSecretStore) Set(service, account, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[service+"/"+account] = value
	return nil
}
func (f *fakeSecretStore) Delete(service, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, service+"/"+account)
	return nil
}

// ── regression tests for the adversarial-review findings ──

// A transient failure (IdP 5xx, offline, DNS) must NOT destroy the stored
// sign-in. The original code dropped the token on ANY refresh error, so one
// send on a plane threw away a credential that cost a human sign-in.
func TestTransientRefreshFailureKeepsTheSignIn(t *testing.T) {
	idp := newFakeIdP(t)
	ap := New()
	if _, err := ap.SignInAuthorizationCode(context.Background(), idp.cfg(), browserFollow(t)); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	key := oauth2Fingerprint(idp.cfg())
	tok := ap.tokens.get(key)
	tok.Expiry = time.Now().Add(-time.Minute)
	ap.tokens.put(key, tok, false)

	// The IdP has a bad day.
	idp.refreshUnavailable = true
	_, err := ap.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: ptr(idp.cfg())}, reqIn())
	if err == nil || !strings.Contains(err.Error(), "try again") {
		t.Fatalf("err = %v, want a retryable message", err)
	}
	if st := ap.SignInStatus(idp.cfg()); !st.SignedIn {
		t.Fatal("a transient outage destroyed the stored sign-in")
	}

	// The IdP recovers; the SAME stored refresh token must still work. The
	// exact ordinal is not asserted — x/oauth2 probes both client-auth styles
	// on a failure, so the counter's value during the outage is a library
	// detail, not part of this test's claim.
	idp.refreshUnavailable = false
	req, err := ap.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: ptr(idp.cfg())}, reqIn())
	if err != nil {
		t.Fatalf("apply after recovery: %v", err)
	}
	assertBearerPrefix(t, req, "access-refreshed-")
}

func assertBearerPrefix(t *testing.T, req core.ResolvedRequest, prefix string) {
	t.Helper()
	for _, h := range req.Headers {
		if h.Key == "Authorization" && strings.HasPrefix(h.Value, "Bearer "+prefix) {
			return
		}
	}
	t.Fatalf("no Authorization: Bearer %s… header in %+v", prefix, req.Headers)
}

// Concurrent sends sharing one expired sign-in must produce exactly ONE
// refresh round-trip. Without single-flighting, every send replayed the same
// refresh token — which an IdP with rotation + reuse detection (Auth0's
// native-app default) reads as theft, revoking the grant family, and the
// losers' error paths then deleted the winner's fresh token locally too.
func TestConcurrentSendsRefreshExactlyOnce(t *testing.T) {
	idp := newFakeIdP(t)
	idp.rotateRefresh = true // reuse now fails hard, like Auth0
	ap := New()
	if _, err := ap.SignInAuthorizationCode(context.Background(), idp.cfg(), browserFollow(t)); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	key := oauth2Fingerprint(idp.cfg())
	tok := ap.tokens.get(key)
	tok.Expiry = time.Now().Add(-time.Minute)
	ap.tokens.put(key, tok, false)

	const sends = 8
	var wg sync.WaitGroup
	errs := make([]error, sends)
	for i := 0; i < sends; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = ap.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: ptr(idp.cfg())}, reqIn())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("send %d failed: %v", i, err)
		}
	}
	if idp.refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d for %d concurrent sends, want exactly 1", idp.refreshCalls, sends)
	}
	if st := ap.SignInStatus(idp.cfg()); !st.SignedIn {
		t.Fatal("the sign-in did not survive concurrent refresh")
	}
}

// A token inside our 30s skew but outside x/oauth2's own 10s delta must be
// REFRESHED, not served: handing the seed to TokenSource unmodified made the
// library return it as still-valid, hollowing out the skew guarantee.
func TestNearExpiryTokenIsActuallyRefreshed(t *testing.T) {
	idp := newFakeIdP(t)
	ap := New()
	if _, err := ap.SignInAuthorizationCode(context.Background(), idp.cfg(), browserFollow(t)); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	key := oauth2Fingerprint(idp.cfg())
	tok := ap.tokens.get(key)
	tok.Expiry = time.Now().Add(20 * time.Second) // dead to us, alive to x/oauth2
	ap.tokens.put(key, tok, false)

	req, err := ap.Apply(context.Background(), model.AuthConfig{Kind: model.AuthOAuth2, OAuth2: ptr(idp.cfg())}, reqIn())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if idp.refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1 — the near-expiry token was served unrefreshed", idp.refreshCalls)
	}
	assertBearer(t, req, "access-refreshed-1")
}
