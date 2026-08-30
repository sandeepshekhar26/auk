# 10 — Mock server: your recorded responses, served on localhost

A frontend team is usually blocked on one of two things: the API doesn't
exist yet, or it's down. AUK already holds the thing that unblocks them — the
responses the backend actually returned, recorded the last time someone hit
each endpoint. The mock server just puts those on a port.

```
Settings → Mock Server → port 8725 → Start
```

```bash
# your frontend's .env
VITE_API_BASE=http://127.0.0.1:8725
```

That's the whole setup. No fixture files, no schema, no separate mocking tool
that drifts from reality — the mock **is** the last real response, so it's
correct by construction and it updates the moment anyone re-sends the request.

---

## 1. Where the data comes from

The store keeps each request's last response (`storage.FileStore.LastResponse`
— the same cache `response('Other Request').body` chaining reads). The mock
server is a pure reader over that plus the workspace's saved requests.

A saved request becomes a mock route when **both** hold:

| Condition | Why |
| --- | --- |
| protocol is `http` or `graphql` | A WebSocket/SSE/gRPC endpoint has no replayable request→response pair over plain HTTP. |
| it has a recorded response with a real status | A never-sent request has nothing to serve; a *failed* send records status `0` plus an error, which is a failure, not a mock. |

So the workflow is: **send it once in AUK, and it's mocked.** A request that
isn't mocked says so, in JSON, with that exact instruction (see §5).

> **Session scope.** The last-response cache is in memory (see the
> `internal/storage` package comment — the durable response archive is a
> separate follow-up). Responses recorded before the app was last restarted
> are not available to the mock; re-send once and they are. A folder run
> re-records every request in a folder in one click, which is the fastest way
> to warm a whole mock.

## 2. Route derivation

For each qualifying request, the route is `METHOD` + the **path portion** of
its URL.

**Path extraction** (`mockserver.ExtractPath`) drops everything a local mock
can't route on:

| Saved URL | Route path |
| --- | --- |
| `https://api.example.com/users` | `/users` |
| `http://localhost:3000/api/v1/users` | `/api/v1/users` |
| `https://api.example.com/search?q=hello` | `/search` |
| `${baseUrl}/v1/users/${userId}` | `/v1/users/${userId}` |
| `https://api.example.com/users/:id` | `/users/:id` |
| `https://api.example.com/users/` | `/users` |
| `https://api.example.com` | `/` |

- The **query string and fragment are dropped**. Routing ignores the query
  entirely — `/search?q=a` and `/search?q=b` are one saved request with one
  recorded response, so they're one route.
- A URL whose first component isn't a scheme or a `/` has that component
  treated as an **authority**, which is what makes the overwhelmingly common
  `${baseUrl}/users` resolve to `/users` (and `localhost:8080/health` to
  `/health`). The trade-off is explicit: a variable holding a path *prefix*
  rather than a base URL loses its first segment. Base-URL-in-a-variable is
  the dominant convention and the one worth optimizing for.
- A trailing slash is trimmed, so `/users/` and `/users` are the same route.

**Wildcards.** A path segment is a wildcard if it contains a `${...}`
templating reference or is a whole-segment `:name` path parameter — the same
rule `core.applyPathParams` and the URL bar use, so the path-param rows the
user sees in the editor are exactly the segments that become wildcards. A
wildcard matches **any single segment**, never several:

```
/users/:id        matches  /users/42        not  /users/42/posts
/v1/${tenant}/orders  matches  /v1/acme/orders
```

**Method.** Upper-cased as saved. A blank method defaults to `POST` for
GraphQL requests and `GET` for everything else.

## 3. Matching

- **Most specific wins**: the candidate with the **fewest wildcards**. A
  recorded `/users/me` beats `/users/:id` for a request to `/users/me`.
- Ties break on the **leftmost literal** — a route that pins the earlier
  segment is the more specific one — then on the stable sort order the route
  table is built in, so the choice never depends on map iteration.
- `HEAD` falls back to the `GET` recording (net/http suppresses the body;
  `Content-Length` still reports the real size). Nobody records a HEAD request
  by hand.

**The route table is rebuilt from the store on every incoming request.** That
isn't laziness, it's the feature: re-send a request in AUK and the very next
hit on the mock serves the new response — new status, new body, new headers —
with no restart. Save a brand-new request, send it, and its route appears the
same way.

## 4. Replay fidelity

The recorded response goes back out as-is, with a short, deliberate list of
exceptions:

| Header | Treatment |
| --- | --- |
| `Content-Length` | **Recomputed** from the bytes actually written. |
| `Transfer-Encoding`, `Connection`, `Keep-Alive`, `TE`, `Trailer`, `Upgrade`, `Proxy-*` | **Dropped** — hop-by-hop headers describe the original connection (RFC 9110 §7.6.1) and `chunked` on a response net/http frames itself is actively corrupting. |
| `Date` | **Dropped**, regenerated fresh. Replaying a timestamp from whenever the request was recorded would feed browser cache heuristics a lie. |
| `Access-Control-*` | **Dropped** — this server owns CORS. Copying a recorded one would emit *two* `Access-Control-Allow-Origin` values, which every browser rejects outright: the one failure mode that would break the exact use case this feature exists for. |
| `Content-Encoding` | **Kept.** net/http's transport removes it when it transparently decompresses, so if it survived into the recording the stored bytes really are still encoded. |
| Everything else | Copied verbatim, **repeats preserved** (`Set-Cookie` above all). |

Status code and body are byte-exact. `1xx`, `204`, and `304` get no body and
no `Content-Length`, per spec. A recording whose body isn't decodable returns
`502` with a "re-send the request in AUK" hint rather than serving garbage.

Every response carries **`X-AUK-Mock: 1`** — replays, 404s, 405s, preflights —
so a confused frontend can tell "this came from the mock" from "this came from
the real API" with one glance at devtools.

## 5. CORS, 404, 405

### The origin allowlist

The mock replays **real recorded traffic** — access tokens, refresh tokens,
`Set-Cookie` headers, customer records: whatever the team actually sent through
AUK. A browser hands a cross-origin response body to a script only when the
server says to, so the header this endpoint sets is the only thing between that
data and every page the developer has open. An `Access-Control-Allow-Origin: *`
would mean an ad frame or a random CodePen in another tab could do
`fetch('http://127.0.0.1:8725/oauth/token').then(r => r.text())` and read the
result. So the mock runs an **origin allowlist**, not a wildcard:

| Request | Response |
| --- | --- |
| `Origin: http://localhost:5173` (or any loopback origin, any port) | Origin **reflected** in `Access-Control-Allow-Origin`; full CORS headers |
| `Origin: https://evil.test` (anything non-loopback) | **No `Access-Control-*` headers at all** — the browser blocks the read |
| `Origin: null` (sandboxed iframe, `file://`) | **No CORS headers** — `null` is the origin every opaque context shares |
| No `Origin` at all (curl, server-side fetch, same-origin) | **No CORS headers** — nothing is negotiating |
| `Host:` header that isn't loopback | **`403`**, before routing (DNS-rebinding defense) |

An origin counts as **loopback** when it is a bare `scheme://host[:port]` with
scheme `http` or `https` (https so `vite --https` works) and the host is the
literal name `localhost`, an IP in `127.0.0.0/8`, or `::1`. Nothing else —
`sub.localhost` and `localhost.evil.test` are both rejected, and any origin
carrying a path, query, fragment or userinfo is rejected rather than salvaged.
That covers Vite, CRA, Next and every other local dev server, which is the
entire real use case.

For an allowed origin:

```
Access-Control-Allow-Origin: http://localhost:5173   ← reflected, never *
Access-Control-Allow-Methods: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS
Access-Control-Allow-Headers: <echoes Access-Control-Request-Headers, else Content-Type, Authorization, Accept>
Access-Control-Expose-Headers: Content-Type, Content-Length, Allow, X-AUK-Mock
Access-Control-Max-Age: 600
Vary: Origin
Vary: Access-Control-Request-Headers
```

Three details worth naming:

- **`Access-Control-Allow-Credentials` is never sent.** The mock authenticates
  nobody, so a page has no business attaching the developer's cookies to it.
  Reflecting an origin is only safe *because* credentials stay off.
- **`Expose-Headers` is an explicit short list, not `*`.** `*` made every
  *replayed* header script-readable — a recorded API's `X-Auth-Token`,
  `X-Customer-Id`, rate-limit internals. The frontend this exists for needs the
  content type, the length, the 405's `Allow`, and the mock marker.
- **`Vary: Origin` is always sent**, including on the no-headers answer, so no
  cache can serve a localhost-allowed reply to a page that was never allowed.

CORS is still present on **error** responses for an allowed origin — a frontend
must be able to *read* the 404, not have it hidden behind a CORS failure.

### DNS-rebinding defense

Binding to `127.0.0.1` constrains the *route*, not the *name*. An attacker can
point `mock.evil.test` at `127.0.0.1`, serve a page from that name, and their
script's requests arrive here looking same-origin — no `Origin` header, so no
CORS check to fail. So every request's **`Host` header must itself be
loopback** (`localhost`, `127.0.0.0/8`, `::1`, with or without a port).
Anything else gets a `403` *before routing*, so a rebound request never reaches
the store and never gets the 404/405/200 path-enumeration signal either.

### Preflight

An `OPTIONS` carrying `Access-Control-Request-Method` is answered `204` by the
server itself and never routed — including for paths the mock has never heard
of, so the browser surfaces the real 404 instead of a misleading CORS error. A
*plain* `OPTIONS` still routes normally, so an `OPTIONS` endpoint that was
actually recorded works. A preflight from a **disallowed** origin still gets
`204` but with **no allow headers**, so it clears nothing.

**No match** → `404` with a JSON body that tells you what to do:

```json
{"error":"no mock for GET /api/nothing","hint":"send the request once in AUK to record a mock"}
```

**Known path, wrong method** → `405` with `Allow`:

```
Allow: DELETE, POST
{"error":"no mock for PUT /users","hint":"recorded for this path: DELETE, POST"}
```

## 6. Layout and bindings

| Piece | Role |
| --- | --- |
| `internal/mockserver/route.go` | Path extraction, wildcard compilation, specificity matching. Pure functions over a read-only `Store` interface. |
| `internal/mockserver/server.go` | The loopback `http.Server`: CORS, preflight, routing, replay. |
| `app_mockserver.go` | Wails bindings + the one-per-process server handle. |

Bindings:

```go
StartMockServer(workspaceID string, port int) (MockStatus, error)
StopMockServer() MockStatus
MockServerStatus() MockStatus
MockServerRoutes() []MockRoute
```

`MockStatus{Running, Port, WorkspaceID, Routes, Error}`; `Routes` is a live
count, and `MockServerRoutes` returns the list the Settings panel renders
(method-colored badge, path, recorded status).

The shape deliberately mirrors `internal/mcpserver`'s embedded HTTP endpoint —
loopback-only `http.Server`, `Start`/`Stop`, a status struct rendered in
Settings. The one difference is auth: the MCP endpoint is bearer-gated because
it can *drive* the app, while this one is a read-only replay of data the user
explicitly recorded and explicitly chose to serve. Its cross-origin exposure is
gated by the loopback origin allowlist in §5 instead of by a token.

**One server per process.** It binds a single fixed TCP port and AUK runs one
`App` per process, so the handle lives in package-level vars under a mutex
rather than an `*App` field; two concurrent mocks would only fight over the
port. Starting with the same workspace and port while already running is a
no-op; starting with a *different* workspace or port stops the old listener
and rebinds, so the button never leaves the label saying one thing while the
port serves another.

**Port.** Default `8725` (one above the MCP server's `8724`, keeping AUK's two
local endpoints adjacent). Fixed rather than ephemeral so a frontend's `.env`
keeps working across restarts, and remembered in `~/.auk/settings.yaml` as
`mockPort` — but only once it's proven bindable, since remembering a port that
failed would just reoffer the same failure next launch.

**Never auto-started.** Unlike the MCP server (which has an enabled-flag
startup), the mock only runs when the user presses Start: silently binding a
port on every launch is a surprise, and the workspace to serve isn't known
until one is selected. Only the port is persisted, not the running state.

**Loopback only**, always — `127.0.0.1`, never `:port`. A mock full of a
team's real recorded API responses should not be reachable from whatever
network the laptop is on. Paired with the `Host`-header check in §5, so a name
an attacker controls that *resolves* to `127.0.0.1` can't reach it either.
