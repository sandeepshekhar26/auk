# 11 — HTTP Digest authentication (RFC 7616)

Every other auth kind AUK supports is *compute-once, attach-a-header*: Basic
base64s a string, Bearer pastes a token, SigV4 signs, and `internal/auth`'s
`Apply` bolts the result onto the request before it ever leaves. Digest can't
work that way. Its reply is a hash over a **nonce the server issues**, so the
client has to send the request, read the `401 + WWW-Authenticate: Digest …`
challenge, compute the answer, and send again. That makes Digest a *transport*
concern, and it lives in `internal/protocols/http/digest.go`, not in
`internal/auth`.

This is the auth scheme on-prem enterprise gear actually ships (routers,
storage appliances, IPMI/BMCs, older IIS and Apache realms), which is why it
earns a challenge-response path the rest of the auth code never needed.

## The handshake

```
  ┌────────┐   1. GET /dir (no Authorization)        ┌────────┐
  │  AUK   │ ───────────────────────────────────────▶│ server │
  │        │◀─────────────────────────────────────── │        │
  │        │   401  WWW-Authenticate: Digest realm=…, └────────┘
  │        │        nonce=…, qop="auth", algorithm=SHA-256, opaque=…
  │        │
  │        │   2. GET /dir                            ┌────────┐
  │        │      Authorization: Digest username=…,   │ server │
  │        │      response=<H(HA1:nonce:nc:cnonce:qop:HA2)>, …
  │        │ ───────────────────────────────────────▶│        │
  │        │◀─────────────────────────────────────── │        │
  └────────┘   200 OK                                 └────────┘
```

Bounded to **exactly one** retry. A second 401 means the credentials are wrong;
AUK returns that 401 to the user rather than looping (looping would hammer the
endpoint and can trip account-lockout policies).

## What's supported

| Aspect | Supported |
| --- | --- |
| Algorithms | `MD5`, `SHA-256`, `SHA-512-256`, and each one's `-sess` variant |
| Default when `algorithm` is absent | `MD5` (RFC 7616 §3.3) |
| Quality of protection | `qop=auth`; a `qop` list is honored as long as it contains `auth` |
| Legacy | RFC 2069 no-`qop` construction (`response = H(HA1:nonce:HA2)`) for old appliances |
| `opaque` | echoed back verbatim |
| `userhash=true` | username is sent hashed as `H(username:realm)` (RFC 7616 §3.4.4) |
| Non-ASCII usernames | sent in the RFC 5987 extended `username*=UTF-8''…` field |
| Algorithm agility | if a 401 offers several Digest challenges, the strongest hash AUK can compute wins |
| `nc` (nonce-count) | tracked **per nonce** on the transport; increments across reuses of one nonce |
| `cnonce` | fresh 128-bit `crypto/rand` value per authorization |

### `qop=auth-int` is intentionally not supported

`auth-int` folds `H(entity-body)` into HA2, which means buffering and re-hashing
the whole request body on every attempt for a mode essentially no real server
offers on its own. A challenge that offers **only** `auth-int` is treated as
unanswerable and **passed through as the 401 it is** — the user sees the
server's own `WWW-Authenticate` rather than a silent failure or an invented
transport error. A challenge offering both `auth` and `auth-int` is answered
with `auth`.

## Where it wires in

The whole handshake is an `http.RoundTripper`:

- **`clientWithDigestAuth(base *http.Client, creds) *http.Client`** returns a
  *copy* of the client whose transport is a `digestTransport` wrapping the
  original transport. It copies rather than mutates because `base` may be the
  process-wide shared client (see `clientFor` in `http_tls.go`) — writing its
  `Transport` would race every other in-flight request.

- **`http.Client.Execute`** (in `http.go`) opts in with a single branch, right
  after it picks the client for the request's TLS/proxy settings:

  ```go
  if req.Auth != nil && req.Auth.Kind == model.AuthDigest && req.Auth.Digest != nil {
      httpClient = clientWithDigestAuth(httpClient, *req.Auth.Digest)
  }
  httpResp, err := httpClient.Do(httpReq)
  ```

  Every other auth kind never enters this branch and keeps the untouched
  single-shot `Do`. `ctx` still flows through `httpClient.Do`, so a hung
  handshake cancels like any other request.

- **Transport ordering matters.** `digestTransport` wraps `tracingTransport`
  (digest **outside**, tracing **inside**). Both the 401 probe and the
  authorized retry therefore pass through `tracingTransport` individually and
  each lands in the hop collector as its own hop, so the request debugger's
  chain honestly shows `401 → 200` and `finalHopTiming` describes the hop whose
  body the user actually reads. The other order would blend two round-trips'
  DNS/connect/TTFB numbers into one nonsensical breakdown.

- **`auth.Apply`** has a Digest case that is a deliberate **pass-through**: it
  returns the request untouched (no `Authorization` header) so a Digest request
  flows cleanly through the engine's auth step instead of tripping the
  "not yet implemented" default. The credential attachment happens in the
  transport.

### The pure core is unit-testable

`digestResponse(...)` and `parseChallenges(...)` have no dependency on
`net/http` — challenge string in, or credentials + method + uri in, hex
`response` out — so they're pinned directly to the RFC's published vectors
rather than only exercised through a live socket.

## Body replay across the retry

The authorized retry has to re-send the **same** method, URL, headers, and body
as the probe. In-memory bodies (`bytes.Reader`, `strings.Reader`) already carry
a `GetBody` factory that `net/http` populates, so replay is free. A body that
arrives as an opaque stream has no `GetBody`, and reading it for the first
attempt would consume it forever — so `replayableBody` **buffers it up front,
before the first attempt**, and both attempts read from the buffer. Buffering
before the probe (not after the 401) is the whole point: after the first send
the stream is already gone. The discarded 401 body is drained (capped at 1 MiB)
and closed so its keep-alive connection can carry the retry.

## Credential templating — INTEGRATION NOTE (no engine change required)

Digest `username`/`password` reach `Execute` on `req.Auth.Digest` as **raw
strings**, exactly like `BasicAuth.Username`, `BearerAuth.Token`, and every
other credential field. The templater (`internal/templating`) resolves
`${var}` in the URL, headers, params, path params, and body, but it does **not**
walk `AuthConfig`; `engine.resolveAndAuthorize` hands `auth.Apply` the raw
`*req.Auth`. So Digest is consistent with the whole auth surface today, and it
needs **no** change to `engine.go`.

If per-variable templated credentials become a product decision, it should be
made **once for all auth kinds** — e.g. resolving `AuthConfig`'s string fields
inside `resolveAndAuthorize` right before the `auth.Apply` call — not bolted
onto Digest alone. Nothing in `digest.go` would change: it already reads
whatever strings it is given.

## NTLM — scoped follow-up (not shipped)

NTLM is the other challenge-response scheme on-prem Windows shops use, and it
was scoped for this work but **deliberately not shipped** — it is materially
harder than Digest and shipping it half-done would have risked Digest's
quality. The remaining work, written up honestly:

- **Three messages, not two.** NTLM is Negotiate → Challenge → Authenticate
  (Type 1/2/3), versus Digest's single re-send.
- **Connection affinity.** The three messages must travel on the **same
  keep-alive TCP connection**; the server binds its Type-2 challenge to that
  connection. `digestTransport` is stateless per request and needs no affinity,
  so it can't be reused as-is — NTLM needs a transport that pins one
  `*http.Transport`/connection for the exchange (or drives it below the client).
- **NTLMv2 crypto.** A pure, unit-tested `NTLMv2` message layer:
  NTOWFv2 = `HMAC-MD5(MD4(UTF16LE(password)), UPPER(user)+domain)`, the
  client challenge / blob, `NTProofStr`, and Type-1/2/3 (de)serialization —
  pinned to published `[MS-NLMP]` vectors, the same way `digestResponse` is
  pinned to RFC 7616.
- **Model + UI.** An `NTLMAuth{Username, Password, Domain}` on the shared model
  (the seam), plus a `domain` field in the `AuthConfigForm` sub-form.

Wired the same two/three-shot transport way Digest is, it drops in without
touching the engine — but it is its own body of work.

## Tests

All in `internal/protocols/http/digest_test.go` (plus the RFC-vector block):

- **Challenge parsing** — a table over single/multi-scheme headers, commas
  inside quoted values, escaped quotes, unquoted tokens, bad whitespace around
  `=`, token68 schemes, and the no-`qop` legacy form.
- **Selection rules** — finds Digest among other schemes, case-insensitive
  header name, prefers the strongest algorithm across separate headers, skips
  challenges it can't answer (unknown algorithm, `auth-int`-only).
- **Published vectors** — `digestResponse` pinned to RFC 2617 §3.5 (MD5, with
  HA1/HA2 intermediates) and RFC 7616 §3.9.1 (MD5 and SHA-256).
- **End-to-end against a validating referee** — an independent, separately
  implemented Digest server that *validates the hash* (not just the header's
  presence) across the full algorithm/qop matrix, so a bug shared with
  `digest.go` can't make both halves agree on the wrong answer. Covers wrong
  password → exactly one retry then the 401 surfaces, body replay on the retry,
  buffering a non-replayable stream, non-Digest 401 pass-through,
  `auth-int`-only pass-through, the two-hop debugger chain, per-nonce `nc`
  increment with a fresh `cnonce`, `userhash`, and non-ASCII `username*`.
- **Seam** — `auth.Apply` leaves a Digest request untouched (no `Authorization`
  header, URL unchanged).
