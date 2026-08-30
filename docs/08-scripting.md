# Scripting

AUK runs two optional JavaScript hooks around every request, both edited in the
request's **Script** tab and both versioned with the request in its YAML file:

| | Runs | Can do |
|---|---|---|
| **Pre-request** | after templating and auth, immediately before the request is dispatched | read the resolved request, add/override headers, read and write variables, log |
| **Post-response** | after the response arrives and after the declarative assertions in the **Assert** tab have been scored | read the response, declare `test()`s, read and write variables, log |

The post-response script is what makes **auth chaining** work: save a token out of
one response and the next request resolves it as `${token}`. That is the canonical
example at the bottom of this page.

Both run on [grafana/sobek](https://github.com/grafana/sobek), a pure-Go JS
interpreter, in a fresh VM per request.

---

## Sandbox guarantees

The interpreter has **no filesystem, no network, and no process access**. There is
no `require`, no `fetch`, no `process`, no `fs` — not disabled, simply never built.
Concretely:

- A script **cannot make its own HTTP call.** This is deliberate and load-bearing:
  every outbound request in AUK passes through one policy chokepoint
  (`PolicyEngine.Authorize`, see [architecture §1.7](02-architecture.md)), and a
  script that could dial out would be a way around it. A pre-request script can
  only change what *the same request* looks like before that check.
- A script **cannot change the response.** A post-response script gets a frozen
  copy; what comes back out of it is tests, variable writes, and log lines.
- A script **cannot write to the app's output.** `console.log` is captured, never
  printed — the GUI, the CLI, and the MCP server own their own streams.
- A script **cannot read or overwrite a secret.** See [Variables](#variables) below.
- Each request gets a **brand-new VM**. Nothing persists between requests, or
  between a request and its chained children, except variables written explicitly
  through `vars.set`.

### Timeouts

| | Limit |
|---|---|
| Pre-request script | **2s** |
| Post-response script | **5s** |

A pre-request script sits in front of the request you are waiting on and its job
is arithmetic; a post-response script is a test suite that parses a body and runs
assertions over it, so it gets longer. Exceeding the limit aborts the script and
records a **script error** (below) — the interrupt is enforced by the runtime and
cannot be caught by a `try`/`catch` inside the script.

---

## Pre-request script API

```js
ctx.request.method      // "POST"          — read-only snapshot, already templated
ctx.request.url         // "https://api.example.com/orders"
ctx.request.headers     // { "Content-Type": "application/json" }
ctx.request.body        // raw body text

// NOTE: the snapshot is templated but SECRET-REDACTED. Anywhere a keychain
// secret was substituted — a header value, the URL, the body, or an
// Authorization header built by an auth kind — the script sees
// "[secret:apiKey]" instead of the value. The request that actually goes on
// the wire carries the real secret; only this read-only view is redacted, so
// the snapshot can never become a way to copy a secret into a plain variable.

ctx.setHeader(name, value)   // add or override a header (case-insensitive)

vars.get(name) / vars.set(name, value) / vars.unset(name)
console.log(...)
```

Everything is already resolved by the time the script runs: `${...}` templates are
expanded, path params substituted, and auth applied. A header the script sets wins
over the one auth added, which is how you override a computed credential.

```js
// Stamp the request with a NON-secret key held in the environment.
var stamp = String(Math.floor(Date.now() / 1000))
ctx.setHeader('X-Timestamp', stamp)
ctx.setHeader('X-Signature', vars.get('signingKey') + ':' + stamp)
```

`signingKey` here must be an ordinary variable — a script cannot read a value
marked **Secret** (`vars.get` returns `undefined`). Real HMAC signing over a
keychain secret belongs in a declarative auth kind (AWS SigV4, OAuth1), not a
script; see [The secrets guard](#the-secrets-guard).

If a pre-request script throws, **the request is not sent** and comes back
unchanged — never half-modified.

`test()` and `expect()` are deliberately *not* available here; there is no
response to assert against yet.

---

## Post-response script API

### `response`

```js
response.status         // 201
response.statusText     // "201 Created"
response.body           // raw response text
response.json()         // parsed body — throws a readable error if it isn't JSON
response.timingMs       // 143
response.size           // body size in bytes

response.headers                        // { "Content-Type": "application/json", ... }
response.headers['Content-Type']        // exactly as the server sent it
response.headers.get('content-type')    // case-insensitive; undefined when absent
response.headers.getAll('set-cookie')   // every value, for repeated headers
```

`response.json()` parses once and caches. On a non-JSON body it throws with the
body's first 120 characters in the message, so an HTML error page announces itself
instead of producing `unexpected token <`.

### `test(name, fn)`

Runs `fn` and records one result: passed if it returns, failed if it throws — with
the thrown message as the failure text. **Each test is caught individually**, so
one failure never stops the rest of the suite from running.

```js
test('creates the order', function () {
  expect(response.status).toBe(201)
  expect(response.json()).toHaveProperty('id')
})
```

Results appear under the response, in the CLI's exit code, and in MCP results —
the same verdict from the same place as the declarative assertions in the Assert
tab. A response passes only if **every** assertion and **every** test passed and
no script error occurred.

### `expect(actual)`

| Matcher | Passes when | Failure message |
|---|---|---|
| `.toBe(v)` | `actual === v` | `expected 404 to be 200` |
| `.toEqual(v)` | deep-equal | `expected {"a":1} to equal {"a":2}` |
| `.toBeTruthy()` | truthy | `expected 0 to be truthy` |
| `.toBeFalsy()` | falsy | `expected "x" to be falsy` |
| `.toContain(v)` | substring, or array member (deep-equal) | `expected [1,2] to contain 9` |
| `.toBeGreaterThan(n)` | `actual > n` | `expected 3 to be greater than 5` |
| `.toBeLessThan(n)` | `actual < n` | `expected 9 to be less than 5` |
| `.toMatch(re)` | regex matches | `expected "abc" to match /[0-9]/` |
| `.toHaveProperty(path)` | path exists | `expected {"a":1} to have property "b"` |
| `.toHaveProperty(path, v)` | path exists and deep-equals `v` | `expected property "a.b" to equal 2, got 1` |

Every matcher negates through **`.not`** (`expect(x).not.toBe(1)`), which flips the
message too (`expected 1 not to be 1`).

Notes:

- `.toMatch` accepts a `RegExp` or a **string treated as a regular expression
  source** — `expect(body).toMatch('^\\{')`. For a plain substring use
  `.toContain`.
- `.toHaveProperty` paths are dotted with optional indices: `'data.items[0].id'`.
- Using a matcher against the wrong kind of value (`expect(3).toContain('a')`) is a
  bug in the test, not a failed assertion: it throws
  `expect(...).toContain needs a string or an array, got number` and `.not` does
  not flip it.

### `console.log(...)`

Captured in order and shown under the response (`console.info/warn/error/debug` are
aliases). Objects are JSON-formatted. Capped at 500 lines per run.

---

## Variables

```js
vars.get(name)          // string, or undefined when unset
vars.set(name, value)   // returns the stored string
vars.unset(name)
```

Available to **both** scripts. The set a script reads is what `${name}` would
have resolved to for this request — the active environment's variables, plus
folder-scoped variables, plus anything a script wrote earlier this session —
**with every secret redacted**. `vars.get` of a secret name returns `undefined`
(see [The secrets guard](#the-secrets-guard)).

### What gets stored

The variable set is a plain string→string map, because that is what `${...}`
templating consumes. Numbers and booleans are stringified; objects and arrays are
JSON-encoded (`vars.set('ids', [1,2])` stores `[1,2]`).

`vars.set(name, undefined)` and `vars.set(name, null)` **throw** rather than
storing an empty string:

```
vars.set("token"): value is undefined, nothing was stored. A path that did not
match the response is the usual cause; use vars.unset("token") to clear it.
```

This is the single most common chaining bug — a response whose shape changed
silently storing `""` and producing a mystery 401 two requests later. Use
`vars.unset` to clear a variable deliberately.

### Where a write lands

In an **interactive** run — a Send in the GUI, a single `auk run`, an MCP call:

1. **With an environment selected**, the write goes into that environment's plain
   variables, through the store — so it survives a restart, shows up in the
   environment editor, and appears in the environment's YAML file as a normal
   git diff.
2. **With no environment selected** (or a store that cannot persist one), the write
   is kept in memory for the rest of the session, scoped to the workspace.

Either way, **the next request resolves it through the normal `${name}` path** —
including the next request of a folder run, which re-reads the environment for
every request. A script-written value takes precedence over the same name in the
environment or a folder: the token this run just minted should beat the stale
literal in the file.

### Where a write lands in a data-driven run

A **data-driven run** (`auk run --data rows.csv`, one iteration per row) is
different, because a CI run must not have side effects on the workspace it
tests:

- A `vars.set` is **run-scoped and never persisted.** It is usable by later
  requests **in the same iteration** — so an intra-iteration `login → token`
  chain still works — but it never touches the stored environment YAML, and it
  is **cleared at every iteration boundary**, so nothing a script wrote in
  iteration *N* is visible to iteration *N+1*.
- **Precedence, weakest to strongest:** environment < outer folder < inner
  folder < **data-row column** < **this iteration's script writes**. The data
  row is the iteration's input; a script write layers on top of it for the rest
  of that iteration.
- The run-scoped layer is private to the run. A concurrent GUI send during a
  data run resolves against the real environment and **does not see the
  iteration's variables** — they belong to the run, not the workspace. A single
  engine runs one data-driven run at a time.

### The secrets guard

A variable listed in an environment's **Secrets** has its value in the OS keychain
and never on disk. A script can neither read nor write one:

- **Read:** `vars.get` of a secret name returns `undefined`. The keychain value
  is redacted from the variable set before it ever reaches the script runtime —
  so a script cannot copy a secret into a plain variable
  (`vars.set('leak', vars.get('apiKey'))` stores the string `"undefined"`, not
  the secret) and cannot exfiltrate it into a header, a body, or a git-committed
  file. This is AUK's "secrets never leave the keychain" promise, enforced.

  The same redaction covers the **pre-request `ctx.request` snapshot**, which is
  the other route to the same plaintext: by the time a pre-request script runs,
  `${apiKey}` has already been substituted into the URL/headers/body and an auth
  kind may have built an `Authorization` header from a secret. Every one of
  those occurrences reads as `[secret:<name>]` in the snapshot. (Found by an
  adversarial review *after* the `vars.get` guard shipped — the guard was real
  but only covered one of the two doors.)

  Belt and braces: a `vars.set` whose **value** contains a known secret is
  refused too, so even a derived copy cannot reach disk.
- **Write:** `vars.set`/`vars.unset` on a secret name throws:

  ```
  vars.set("apiKey"): "apiKey" is a secret in this environment. Its value lives in
  the OS keychain and is never written to disk, so a script may not overwrite or
  delete it.
  ```

  The write guard is the **union of secret names across every environment in the
  workspace**, so a script can never write a name that is secret in *any*
  environment — not even with no environment selected, where the write would
  otherwise land in the session overlay and shadow the real secret once a
  secret-bearing environment is selected. The engine enforces this again when it
  persists, so no scripting implementation can clobber or leak a keychain value.

**Signing that genuinely needs a secret** — an HMAC signature over the request —
must use a **declarative auth kind** (AWS SigV4, OAuth1), which computes the
signature from its own credentials without ever exposing them to a script. A
pre-request script is for *shaping* a request, not for holding a secret.

---

## When a script fails

A test that fails is a normal result. The script itself failing is different: a
**syntax error**, a **timeout**, or a **throw outside any `test()`** records a
*script error* on the response, shown as a banner and making the run fail. A
script that could not run is never reported as a pass — there is no way to know
what it would have checked.

Tests and variable writes that already happened before a later throw are kept:
they are real side effects that really occurred.

---

## Examples

### 1. The auth chain

**Request A — `POST /login`**, post-response script:

```js
test('login succeeded', function () {
  expect(response.status).toBe(200)
})

vars.set('token', response.json().token)
```

**Request B — `GET /me`**, header:

```
Authorization: Bearer ${token}
```

Send A, then B — or run the folder containing both and B picks up the token A
stored. Nothing else to wire up.

### 2. A real test suite

```js
var body = response.json()

test('responds quickly', function () {
  expect(response.timingMs).toBeLessThan(500)
})

test('returns JSON', function () {
  expect(response.headers.get('content-type')).toContain('application/json')
})

test('the order is complete', function () {
  expect(response.status).toBe(201)
  expect(body).toHaveProperty('id')
  expect(body).toHaveProperty('status', 'pending')
  expect(body.items).not.toEqual([])
  expect(body.total).toBeGreaterThan(0)
})

test('no debug fields leaked', function () {
  expect(Object.keys(body)).not.toContain('_internal')
})
```

### 3. Paging through a collection

```js
var page = response.json()

test('page has results', function () {
  expect(page.items.length).toBeGreaterThan(0)
})

if (page.nextCursor) {
  vars.set('cursor', page.nextCursor)
} else {
  // Clear it so the next run starts from the beginning instead of
  // resuming from a cursor that no longer exists.
  vars.unset('cursor')
  console.log('reached the last page')
}
```

with the request's query param `cursor` set to `${cursor}`.
