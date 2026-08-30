package http

import (
	"bytes"
	"crypto/md5" //nolint:gosec // RFC 7616's mandatory-to-implement algorithm; the wire protocol picks it, not us.
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"apitool/internal/core/model"
)

// HTTP Digest Access Authentication (RFC 7616, superseding RFC 2617/2069).
//
// Digest is the one auth scheme in this app that CANNOT be a header computed
// up front by internal/auth: the reply is a hash over a server-issued nonce,
// so the client has to send the request, collect the 401 + WWW-Authenticate
// challenge, and re-send. That makes it a transport concern, which is why it
// lives here as an http.RoundTripper wrapper rather than as an auth.Apply
// case (see model.DigestAuth's comment).
//
// Supported: algorithm MD5, SHA-256, SHA-512-256 and their -sess variants;
// qop=auth; and the qop-less RFC 2069 legacy construction that old
// appliances still emit. NOT supported: qop=auth-int — it hashes the entity
// body into A2, which forces the whole body to be buffered and re-hashed per
// attempt for a mode essentially no real server offers alone; a challenge
// offering only auth-int is passed through as the 401 it is, so the user sees
// the server's own WWW-Authenticate rather than a silent failure.

// digestTransport wraps a base RoundTripper and answers Digest challenges.
//
// TRANSPORT ORDERING: this wraps tracingTransport (digest OUTSIDE, tracing
// INSIDE) — see clientWithDigestAuth. Both attempts therefore pass through
// tracingTransport individually, so the unauthorized 401 and the authorized
// retry each land in the hop collector as their own hop: the request
// debugger's chain shows "401 → 200", which is the truth of what went over
// the wire, and finalHopTiming still describes the hop whose body the user
// is reading. The other order (tracing outside) would collapse the pair into
// one hop and blend two round-trips' DNS/connect/TTFB numbers into a single
// nonsensical breakdown.
type digestTransport struct {
	base     http.RoundTripper
	username string
	password string

	mu sync.Mutex
	// origin is the scheme+host+port this transport will answer challenges
	// for — see the ORIGIN BINDING note on RoundTrip. Guarded by mu because
	// it can be bound lazily on the first hop.
	origin *url.URL
	// nc counts requests sent per nonce value (RFC 7616 §3.4.1 nonce-count).
	// Keyed by nonce because the count is scoped to the nonce, not to the
	// connection: a fresh challenge restarts at 1.
	nc map[string]uint32
}

// clientWithDigestAuth returns a copy of base whose transport answers Digest
// challenges with these credentials, and ONLY at origin — the URL of the
// request the caller is about to make (see RoundTrip's ORIGIN BINDING note).
// Pass nil to have the transport bind itself to the first request it carries,
// which is right for a client used for a single origin but wrong for one
// reused across hosts; every caller inside this package passes the URL.
//
// It copies rather than mutates because base may be the Client's long-lived
// shared *http.Client (see clientFor) — writing to its Transport would race
// every other in-flight request. The copy keeps the same cookie jar, timeout,
// and redirect policy.
func clientWithDigestAuth(base *http.Client, creds model.DigestAuth, origin *url.URL) *http.Client {
	inner := base.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	withDigest := *base
	withDigest.Transport = &digestTransport{
		base:     inner,
		username: creds.Username,
		password: creds.Password,
		origin:   canonicalOrigin(origin),
	}
	return &withDigest
}

// canonicalOrigin reduces a URL to the scheme+host pair an origin comparison
// needs, copied so nothing later mutates the caller's URL through it.
func canonicalOrigin(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}
}

// sameOrigin compares scheme, host and port, filling in the scheme's default
// port so `https://api.test` and `https://api.test:443` are one origin.
// Hostname and scheme compare case-insensitively, as the URL grammar requires.
func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		originPort(a) == originPort(b)
}

func originPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// answersFor reports whether a challenge from this hop's URL may be answered,
// binding the origin on the first hop when the caller supplied none.
//
// It must be consulted BEFORE the hop is sent, not after the 401 comes back:
// consulted afterwards, a first hop that is not a 401 (a 302, say) would leave
// the transport unbound, and the attacker's 401 would then become the "first"
// URL it binds to — the exact hole this closes.
func (t *digestTransport) answersFor(u *url.URL) bool {
	if u == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.origin == nil {
		t.origin = canonicalOrigin(u)
		return true
	}
	return sameOrigin(t.origin, u)
}

// RoundTrip sends the request, and if it comes back 401 with a Digest
// challenge we can answer, sends it exactly once more with an Authorization
// header. Exactly once: a second 401 is the server telling us the credentials
// are wrong, and looping on that would hammer the endpoint (and, against
// account-lockout policies, do real damage) instead of showing the user the
// 401 they need to see. Anything that isn't an answerable Digest challenge —
// a non-401, a Basic/Bearer/NTLM 401, an algorithm or qop we don't implement —
// is returned untouched.
//
// ORIGIN BINDING: a challenge is answered ONLY when the hop that received it
// is at the same scheme+host+port as the request this transport was configured
// for. http.Client follows redirects by default and calls RoundTrip once per
// hop, so without this check a legitimate host could 302 to an attacker's
// host, which answers with a Digest challenge carrying a realm and nonce IT
// chose — and the client would immediately hand over the username plus a
// digest computed over attacker-controlled inputs, which is offline dictionary
// material against the user's password. curl draws the same line and requires
// an explicit --location-trusted to cross it. On a mismatch the 401 is
// returned unanswered, which is both correct (that host never authenticated
// us) and what the user needs to see.
func (t *digestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Decided before the hop goes out — see answersFor.
	answerable := t.answersFor(req.URL)

	first, replayBody, err := replayableBody(req)
	if err != nil {
		return nil, err
	}

	resp, err := t.base.RoundTrip(first)
	if err != nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	if !answerable {
		return resp, nil
	}

	challenge, ok := digestChallengeFrom(resp.Header)
	if !ok {
		return resp, nil
	}
	authorization, err := t.authorizationFor(challenge, req.Method, req.URL.RequestURI())
	if err != nil {
		// We understood the header but can't answer it. Surfacing the server's
		// own 401 (challenge intact) tells the user more than an invented
		// transport error would.
		return resp, nil
	}

	retry := req.Clone(req.Context())
	retry.Header.Set("Authorization", authorization)
	if replayBody != nil {
		body, err := replayBody()
		if err != nil {
			return resp, nil
		}
		retry.Body = body
		retry.GetBody = replayBody
	}

	drainAndClose(resp)
	return t.base.RoundTrip(retry)
}

// replayableBody guarantees the request body can be sent twice, and returns
// the request to use for the first attempt plus the factory for the retry's
// body (nil when there is no body).
//
// http.NewRequest already populates GetBody for the in-memory body types the
// app uses (bytes.Reader/Buffer, strings.Reader), so the common path costs
// nothing. A body that arrived as an opaque stream — anything another caller
// hands this transport — has no GetBody, and reading it for attempt 1 would
// consume it forever, so it is buffered up front and BOTH attempts read from
// the buffer. Buffering before the first attempt (not after the 401) matters:
// after the first attempt the stream is already gone.
func replayableBody(req *http.Request) (*http.Request, func() (io.ReadCloser, error), error) {
	if req.Body == nil || req.Body == http.NoBody {
		return req, nil, nil
	}
	if req.GetBody != nil {
		return req, req.GetBody, nil
	}

	buf, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, nil, fmt.Errorf("digest auth: buffering request body for a possible retry: %w", err)
	}
	get := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf)), nil }

	// Clone rather than mutate: RoundTrippers must not modify the caller's
	// request (beyond consuming its body, which we just did).
	first := req.Clone(req.Context())
	first.Body, _ = get()
	first.GetBody = get
	if first.ContentLength <= 0 {
		// The length is knowable now, so send it instead of falling back to
		// chunked encoding — some of the legacy servers that speak Digest
		// handle chunked request bodies badly.
		first.ContentLength = int64(len(buf))
	}
	return first, get, nil
}

// drainAndClose consumes the discarded challenge response so its connection
// can be reused for the retry. The cap keeps a pathological multi-megabyte
// 401 body from being read just to be thrown away; giving up on connection
// reuse in that case is the cheaper trade.
func drainAndClose(resp *http.Response) {
	if resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// digestChallenge is one parsed `WWW-Authenticate: Digest ...` challenge.
type digestChallenge struct {
	realm     string
	nonce     string
	opaque    string
	algorithm string
	qop       []string
	stale     bool
	userhash  bool
}

// digestChallengeFrom finds the best answerable Digest challenge across every
// WWW-Authenticate header on the response.
//
// A 401 may carry several challenges — several headers, several
// comma-separated challenges in one header, or several Digest challenges that
// differ only by algorithm (RFC 7616 §3.7 explicitly encourages a server to
// offer both a modern and a legacy digest). Challenges we cannot answer
// (unknown algorithm, or a qop list without "auth") are skipped rather than
// attempted, and among the rest the strongest hash wins; ties keep the first
// one the server listed.
func digestChallengeFrom(h http.Header) (digestChallenge, bool) {
	var best digestChallenge
	bestRank := -1
	for name, values := range h {
		// http.Header keys are canonicalized by net/http, but a header map
		// assembled by hand (tests, other transports) may not be — compare
		// case-insensitively so the challenge is never missed over spelling.
		if !strings.EqualFold(name, "WWW-Authenticate") {
			continue
		}
		for _, value := range values {
			for _, raw := range parseChallenges(value) {
				if !strings.EqualFold(raw.scheme, "Digest") {
					continue
				}
				candidate := challengeFromParams(raw.params)
				rank, ok := algorithmRank(candidate.algorithm)
				if !ok {
					continue
				}
				if _, usable := candidate.pickQop(); !usable {
					continue
				}
				if rank > bestRank {
					best, bestRank = candidate, rank
				}
			}
		}
	}
	return best, bestRank >= 0
}

func challengeFromParams(params map[string]string) digestChallenge {
	ch := digestChallenge{
		realm:     params["realm"],
		nonce:     params["nonce"],
		opaque:    params["opaque"],
		algorithm: params["algorithm"],
		stale:     strings.EqualFold(strings.TrimSpace(params["stale"]), "true"),
		userhash:  strings.EqualFold(strings.TrimSpace(params["userhash"]), "true"),
	}
	for _, item := range strings.Split(params["qop"], ",") {
		if item = strings.TrimSpace(item); item != "" {
			ch.qop = append(ch.qop, item)
		}
	}
	return ch
}

// pickQop reports the quality-of-protection to use. An empty qop with ok=true
// is the RFC 2069 legacy construction (no qop/nc/cnonce in the reply), which
// is what a challenge that omits qop entirely asks for. ok=false means the
// server offered qop values but none we implement (i.e. auth-int only).
func (c digestChallenge) pickQop() (string, bool) {
	if len(c.qop) == 0 {
		return "", true
	}
	for _, q := range c.qop {
		if strings.EqualFold(q, "auth") {
			return "auth", true
		}
	}
	return "", false
}

// algorithmRank scores the algorithms we can compute so the strongest offered
// challenge wins; -sess variants rank with their base hash. An unknown
// algorithm is not ok, which is how an unanswerable challenge gets skipped.
func algorithmRank(algorithm string) (int, bool) {
	switch baseAlgorithm(algorithm) {
	case "MD5":
		return 1, true
	case "SHA-256":
		return 2, true
	case "SHA-512-256":
		return 3, true
	}
	return 0, false
}

func baseAlgorithm(algorithm string) string {
	name := strings.ToUpper(strings.TrimSpace(algorithm))
	if name == "" {
		// RFC 7616 §3.3: an absent algorithm means MD5.
		return "MD5"
	}
	return strings.TrimSuffix(name, "-SESS")
}

// hashFor returns H() for the challenge's algorithm plus whether the -sess
// variant was requested.
func hashFor(algorithm string) (h func(string) string, sess bool, ok bool) {
	sess = strings.HasSuffix(strings.ToUpper(strings.TrimSpace(algorithm)), "-SESS")
	switch baseAlgorithm(algorithm) {
	case "MD5":
		return func(s string) string {
			sum := md5.Sum([]byte(s)) //nolint:gosec // protocol-mandated
			return hex.EncodeToString(sum[:])
		}, sess, true
	case "SHA-256":
		return func(s string) string {
			sum := sha256.Sum256([]byte(s))
			return hex.EncodeToString(sum[:])
		}, sess, true
	case "SHA-512-256":
		return func(s string) string {
			sum := sha512.Sum512_256([]byte(s))
			return hex.EncodeToString(sum[:])
		}, sess, true
	}
	return nil, false, false
}

// authorizationFor builds the Authorization header value answering ch for a
// request with this method and request-target (RFC 7616 §3.4).
func (t *digestTransport) authorizationFor(ch digestChallenge, method, uri string) (string, error) {
	h, sess, ok := hashFor(ch.algorithm)
	if !ok {
		return "", fmt.Errorf("digest auth: unsupported algorithm %q", ch.algorithm)
	}
	qop, ok := ch.pickQop()
	if !ok {
		return "", fmt.Errorf("digest auth: server offered only qop=%q, which is not supported", strings.Join(ch.qop, ","))
	}
	cnonce, err := newCnonce()
	if err != nil {
		return "", err
	}

	params := []string{
		usernameParam(h, t.username, ch),
		quotedParam("realm", ch.realm),
		quotedParam("nonce", ch.nonce),
		quotedParam("uri", uri),
	}
	if qop == "" {
		params = append(params, quotedParam("response",
			digestResponse(h, sess, t.username, t.password, ch, method, uri, cnonce, "", "")))
	} else {
		nc := fmt.Sprintf("%08x", t.nextNC(ch.nonce))
		params = append(params,
			quotedParam("response",
				digestResponse(h, sess, t.username, t.password, ch, method, uri, cnonce, nc, qop)),
			// qop/nc/algorithm are unquoted tokens per the RFC's ABNF.
			"qop="+qop,
			"nc="+nc,
			quotedParam("cnonce", cnonce),
		)
	}
	if ch.algorithm != "" {
		// Echoed exactly as the server spelled it — some implementations
		// string-compare this field.
		params = append(params, "algorithm="+ch.algorithm)
	}
	if ch.opaque != "" {
		params = append(params, quotedParam("opaque", ch.opaque))
	}
	if ch.userhash {
		params = append(params, "userhash=true")
	}
	return "Digest " + strings.Join(params, ", "), nil
}

// digestResponse is the RFC 7616 §3.4.1 "response" value — the whole point of
// the scheme, kept as a pure function of its inputs so it can be pinned
// directly to the RFC's published test vectors rather than only exercised
// through a live handshake.
//
//	HA1        = H( username : realm : password )
//	HA1 (sess) = H( HA1 : nonce : cnonce )
//	HA2        = H( method : uri )
//	response   = H( HA1 : nonce : nc : cnonce : qop : HA2 )   // qop=auth
//	response   = H( HA1 : nonce : HA2 )                       // RFC 2069, no qop
func digestResponse(h func(string) string, sess bool, username, password string, ch digestChallenge, method, uri, cnonce, nc, qop string) string {
	ha1 := h(username + ":" + ch.realm + ":" + password)
	if sess {
		// -sess binds A1 to this nonce/cnonce pair so a captured A1 can't be
		// replayed against a later challenge.
		ha1 = h(ha1 + ":" + ch.nonce + ":" + cnonce)
	}
	ha2 := h(method + ":" + uri)
	if qop == "" {
		return h(ha1 + ":" + ch.nonce + ":" + ha2)
	}
	return h(ha1 + ":" + ch.nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
}

// usernameParam renders the username field. Three cases, all from RFC 7616
// §3.4.4: the server asked for a hashed username (userhash=true); the
// username has non-ASCII characters, which a quoted-string cannot carry
// safely, so it goes in the RFC 5987 extended `username*` field; or the plain
// quoted form. Note that A1 always uses the REAL username regardless —
// userhash only changes what is transmitted.
func usernameParam(h func(string) string, username string, ch digestChallenge) string {
	if ch.userhash {
		return quotedParam("username", h(username+":"+ch.realm))
	}
	if isASCII(username) {
		return quotedParam("username", username)
	}
	return "username*=UTF-8''" + rfc5987Encode(username)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7e || s[i] < 0x20 {
			return false
		}
	}
	return true
}

// rfc5987Encode percent-encodes everything outside attr-char (RFC 5987 §3.2)
// — url.QueryEscape can't be used here, it encodes a space as "+".
func rfc5987Encode(s string) string {
	const unreserved = "!#$&+-.^_`|~"
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.IndexByte(unreserved, c) >= 0:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// nextNC returns the 1-based count of requests sent with this nonce.
func (t *digestTransport) nextNC(nonce string) uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.nc == nil {
		t.nc = make(map[string]uint32, 1)
	}
	t.nc[nonce]++
	return t.nc[nonce]
}

// newCnonce is the client's own nonce: 128 bits from crypto/rand, hex-encoded.
// It must be unpredictable — it is what stops a server (or anyone who can see
// the traffic) from steering the client into recomputing a digest it has
// already precomputed a rainbow table for.
func newCnonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("digest auth: generating cnonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func quotedParam(name, value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
	return name + `="` + escaped + `"`
}

// rawChallenge is one `<scheme> <params>` unit of a WWW-Authenticate header,
// before any scheme-specific interpretation. Param names are lower-cased.
type rawChallenge struct {
	scheme string
	params map[string]string
}

// parseChallenges splits one WWW-Authenticate header value into its
// challenges (RFC 7235 §4.1). The grammar is genuinely ambiguous to a naive
// splitter: challenges are comma-separated, but so are the auth-params INSIDE
// a challenge, and a quoted param value may itself contain commas. The rule
// applied here is the standard disambiguation — split on commas outside
// quotes, then a segment whose text before its first "=" contains whitespace
// starts a NEW challenge ("Digest realm=..."), while a bare "key=value"
// segment is another param of the challenge already in progress.
//
//	Basic realm="a", Digest realm="b,c", qop="auth", Negotiate
//	└─ challenge ─┘  └────────── challenge ────────┘  └─ chal ─┘
func parseChallenges(header string) []rawChallenge {
	var out []rawChallenge
	current := -1
	for _, segment := range splitOutsideQuotes(header, ',') {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		head := segment
		if eq := strings.IndexByte(segment, '='); eq >= 0 {
			// TrimRight, not TrimSpace: RFC 7235 allows "bad whitespace"
			// around the "=" of an auth-param, and `algorithm = MD5` must
			// stay a param rather than look like a new scheme named
			// "algorithm". Trimming only the right keeps head a prefix of
			// segment, so the index below is valid in both.
			head = strings.TrimRight(segment[:eq], " \t")
		}
		if space := strings.IndexAny(head, " \t"); space >= 0 {
			out = append(out, rawChallenge{scheme: head[:space], params: map[string]string{}})
			current = len(out) - 1
			// The remainder is either this scheme's first auth-param or a
			// token68 credential blob (Negotiate/NTLM); addParam ignores the
			// latter, since it has no "=".
			addParam(out[current].params, strings.TrimSpace(segment[space+1:]))
			continue
		}
		if !strings.Contains(segment, "=") {
			// A scheme with no parameters at all, e.g. a bare "Negotiate".
			out = append(out, rawChallenge{scheme: segment, params: map[string]string{}})
			current = len(out) - 1
			continue
		}
		if current < 0 {
			continue // a param before any scheme — malformed, skip it
		}
		addParam(out[current].params, segment)
	}
	return out
}

func addParam(params map[string]string, segment string) {
	eq := strings.IndexByte(segment, '=')
	if eq < 0 {
		return
	}
	key := strings.ToLower(strings.TrimSpace(segment[:eq]))
	if key == "" {
		return
	}
	if _, exists := params[key]; exists {
		return // first value wins, so a duplicate can't overwrite the real one
	}
	params[key] = unquoteParam(segment[eq+1:])
}

// splitOutsideQuotes splits on sep, ignoring separators inside a quoted-string
// (and inside a quoted-pair such as \"). Quotes are left in place for
// unquoteParam to strip.
func splitOutsideQuotes(s string, sep byte) []string {
	var parts []string
	var buf strings.Builder
	inQuotes, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case inQuotes && c == '\\':
			escaped = true
		case c == '"':
			inQuotes = !inQuotes
		case c == sep && !inQuotes:
			parts = append(parts, buf.String())
			buf.Reset()
			continue
		}
		buf.WriteByte(c)
	}
	return append(parts, buf.String())
}

// unquoteParam strips the surrounding quotes from a quoted-string value and
// resolves quoted-pairs. Unquoted token values (algorithm=MD5, stale=true)
// pass through untouched.
func unquoteParam(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return value
	}
	inner := value[1 : len(value)-1]
	if !strings.Contains(inner, `\`) {
		return inner
	}
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) {
			i++
		}
		b.WriteByte(inner[i])
	}
	return b.String()
}
