# Migrate from Postman

**Status:** built (backend) · **Package:** `internal/importer` (`migrate.go`, `postmandump.go`, `pmscript.go`) · **Bindings:** `App.MigrateFromPostmanFiles`, `App.MigrateFromPostmanContent`, `App.PostmanInstalled` (`app_migrate.go`)

Importing one collection answers *"can this app read my file?"*. A **migration**
answers the question that actually decides the purchase: *"if I leave Postman,
does my stuff come across — all of it, and what doesn't?"*

So a migration is bigger than an import in four ways:

1. **Many files at once.** A whole Postman account (Settings → Data → Export
   Data), or a handful of hand-exported collections and environments, or both.
2. **One workspace.** Everything merges into a single AUK workspace, with each
   collection as a top-level folder, instead of N disconnected workspaces.
3. **Scripts are translated.** Postman's `pm.*` test and pre-request scripts are
   rewritten into AUK's script API ([docs/08-scripting.md](08-scripting.md)) —
   the part a switcher cannot do by hand in an afternoon.
4. **An honest report.** Everything approximated or dropped is listed, by
   request name, with what to do about it.

The last one is the load-bearing design decision. A migration that silently
drops a test script or an unsupported auth type is worse than one that says so:
the user finds out in CI three weeks later instead of on day one.

---

## Exporting from Postman

**One collection** — sidebar → hover the collection → **⋯ (View more actions)**
→ **Export** → *Collection v2.1 (recommended)* → **Export**. You get
`<name>.postman_collection.json`.

**One environment** — **Environments** in the left sidebar → hover it → **⋯** →
**Export**. You get `<name>.postman_environment.json`.

**Everything at once** — **⚙ (Settings)** → **Settings** → **Data** →
**Export Data** → **Export**. Postman writes one JSON file holding every
collection and environment (some versions wrap it in a `.zip`: unzip it first
and hand AUK the `.json` inside).

Drop any mixture of those files into AUK's **Migrate from Postman** picker — it
is multi-select, and the three shapes are auto-detected per file.

---

## What migrates 1:1

Every collection still goes through the *same* parser a single-collection import
uses (`ParsePostman`), so nothing about single-collection fidelity is
re-implemented or second-guessed here.

| Postman | AUK | Notes |
|---|---|---|
| Collection | A top-level **folder** named after the collection | So several collections coexist in one workspace without their trees interleaving. |
| Folders (nested) | Folders (nested) | Order and nesting preserved. |
| Requests | Requests | Method, URL, headers (incl. disabled ones), body. |
| `{{var}}` | `${var}` | Everywhere: URL, headers, body, form fields, auth fields. `{{$randomInt}}` → `${randomInt}`. |
| Path variables (`/users/:id` + `variable[]`) | URL + **PathParams** rows, pre-filled | |
| Raw / urlencoded / form-data bodies | Text-or-JSON / form bodies | JSON is detected from the body itself. |
| Basic, Bearer, API key auth | The same auth kinds | |
| Collection variables | An environment named *"\<Collection\> variables"* | Renamed from `Default` so several merged collections stay distinguishable. |
| Environment exports | Environments | Including `enabled: false` rows. |
| Request descriptions | `RequestDef.Description` | |
| Item ordering | Order keys | Re-minted across the merged set (see below). |

---

## What is approximated

- **Secret environment values.** A Postman export carries `"type": "secret"`
  values **in plaintext**. AUK keeps secret values in the macOS keychain and
  never on disk, so importing the value would quietly downgrade the user's
  security posture on their first minute in the app. The **name** is imported
  into `Environment.Secrets` with an empty row; the **value** is not, and the
  report names every one of them. Paste them in once in the environment editor.
- **Scripts.** Translation is line-oriented and best-effort (see the table
  below). Anything unrecognized survives as a commented-out line — never
  silently dropped.
- **`pm.response.to.have.header('X')`** becomes
  `expect(response.headers.get('X')).toBeTruthy()` — an existence check, which
  is what Postman's assertion means, expressed with AUK's matchers.
- **Non-HTTP requests** (`wss://`, `grpc://`, Postman WebSocket/Socket.IO
  items) import as HTTP requests with URL, headers and body intact. AUK has
  first-class WebSocket/gRPC/GraphQL/SSE protocols — switch the request's
  protocol and it is ready. The report flags each one.

---

## What cannot come across

| Postman | Why | What the report says |
|---|---|---|
| `pm.sendRequest(...)` | **The sandbox rule.** AUK scripts cannot make HTTP calls, deliberately: every outbound request passes through one policy chokepoint (`PolicyEngine.Authorize`), and a script that could dial out would be a way around it. | The call is commented out in the script, and the warning explains how to model it instead: make it its OWN request, and chain the two with `vars.set('token', …)` in one and `${token}` in the next. |
| Secret VALUES | They live in the OS keychain, never on disk. | Each secret is named so you can paste it in once. |
| `async` / `await` / `done` callbacks | AUK's runtime records an async test callback as a **failure** — there is no way to await one honestly. | Commented out, with the reason. |
| `require()`, `setTimeout`, `setInterval` | No module loader and no timers in the sandbox. | Commented out, with the reason. |
| `postman.setNextRequest(...)` | AUK has no run-order override; a folder run sends requests in tree order. | Commented out, with the reason. |
| Collection-level and folder-level scripts | AUK has no collection/folder script hook — a script attached there would have to be silently duplicated onto every request, which is not the same thing. | A warning per collection/folder, **with the translated script inline** so you can paste it into the requests that need it. |
| Auth kinds beyond basic / bearer / API key | Not carried by the collection importer. | A warning per request naming the Postman auth type. AUK itself supports basic, bearer, API key, JWT, OAuth2 (client credentials), OAuth1, AWS SigV4 and Digest — set it in the Auth tab. |
| GraphQL and file bodies | The importer maps raw/urlencoded/form-data bodies; a Postman `file` body only records a *path*, not the contents. | A warning per request. |

---

## Script translation

Postman `test` scripts become AUK's **post-response** script; `prerequest`
scripts become the **pre-request** script.

| Postman | AUK |
|---|---|
| `pm.test("n", function () {…})` / `pm.test("n", () => {…})` | `test("n", () => {…})` |
| `pm.response.to.have.status(200)` | `expect(response.status).toBe(200)` |
| `pm.response.to.have.header("H")` | `expect(response.headers.get("H")).toBeTruthy()` |
| `pm.response.code` | `response.status` |
| `pm.response.status` | `response.statusText` |
| `pm.response.json()` | `response.json()` |
| `pm.response.text()` | `response.body` |
| `pm.response.responseTime` | `response.timingMs` |
| `pm.response.responseSize` | `response.size` |
| `pm.response.headers.get(…)` | `response.headers.get(…)` |
| `pm.expect(x)` | `expect(x)` |
| `.to.eql(y)` / `.to.deep.equal(y)` | `.toEqual(y)` |
| `.to.equal(y)` / `.to.be.equal(y)` | `.toBe(y)` |
| `.to.be.true` / `.to.be.false` | `.toBeTruthy()` / `.toBeFalsy()` |
| `.to.be.null` / `.to.be.undefined` | `.toBe(null)` / `.toBe(undefined)` |
| `.to.include(y)` / `.to.contain(y)` | `.toContain(y)` |
| `.to.have.property(p)` | `.toHaveProperty(p)` |
| `.to.be.above(n)` / `.below(n)` (and `.gt`/`.lt`) | `.toBeGreaterThan(n)` / `.toBeLessThan(n)` |
| `.to.match(re)` | `.toMatch(re)` |
| `.to.not.…` / `.not.to.…` | `.not.…` |
| `pm.environment.set/get/unset` | `vars.set/get/unset` |
| `pm.collectionVariables.*`, `pm.globals.*`, `pm.variables.get` | `vars.*` (AUK has one variable set) |
| `pm.request.headers.add({key, value})` *(pre-request only)* | `ctx.setHeader(key, value)` |
| `console.log(…)` | unchanged |
| `postman.setEnvironmentVariable(k, v)` *(v1)* | `vars.set(k, v)` |
| `postman.getEnvironmentVariable(k)` *(v1)* | `vars.get(k)` |
| `tests["n"] = expr;` *(v1)* | `test("n", () => { expect(expr).toBeTruthy() })` |
| `responseCode.code` / `responseBody` *(v1)* | `response.status` / `response.body` |

### Before / after

```js
// Postman
pm.test("Status code is 200", function () {
    pm.response.to.have.status(200);
});
pm.environment.set("token", pm.response.json().token);
pm.sendRequest("https://auth.example.com/refresh", function (err, res) {
    pm.environment.set("token", res.json().token);
});
```

```js
// AUK
test("Status code is 200", () => {
    expect(response.status).toBe(200);
});
vars.set("token", response.json().token);
// TODO(auk-migrate): pm.sendRequest cannot be translated: AUK scripts cannot make HTTP calls (every request goes through one policy chokepoint). Model this as its own request and chain it with vars.set/${var}.
// pm.sendRequest("https://auth.example.com/refresh", function (err, res) {
//     pm.environment.set("token", res.json().token);
// });
```

### The two safety rules

Translation is **line-oriented and best-effort**. Two rules keep that from
producing something worse than no translation at all:

1. **A migrated script never fails to parse.** An untranslatable line that
   *opens* a block takes its whole block into the comment with it, so no
   closing brace is orphaned. An untranslatable line that *closes* a block it
   did not open (`} else if (pm.info…) {`) would, if commented out in place,
   silently merge two branches into one and still compile — so the translator
   refuses to splice and preserves the whole script as comments instead.
   Finally, every finished script is compiled with **grafana/sobek**, the same
   interpreter that will run it; if it does not parse, the original is kept as
   comments under a header explaining the manual port.
2. **A line is translated only if nothing is left over.** If any `pm.`,
   `postman.` or chai `.to.` residue survives the rewrite, the line is treated
   as untranslated rather than half-rewritten — a half-rewritten line would run
   and fail at runtime with a confusing message, which is exactly the silent
   breakage this feature exists to prevent.

Everything commented out is marked `TODO(auk-migrate)` — grep the Script tab
for it; that is the worklist.

---

## Merging and ordering

- Each collection becomes a **top-level folder**; its own folders and requests
  are re-parented under it, so several collections never interleave.
- **Order keys are re-minted across the whole merged set** from one monotonic
  minter. Each collection's own keys restart at `000001`, so merging without
  re-minting would collide. Sorting a collection's nodes by their original key
  recovers `ParsePostman`'s tree-walk order (the keys are zero-padded and
  monotonic, so string order *is* mint order), and re-minting in that sequence
  keeps folders and requests interleaved exactly as Postman had them while
  making every key globally unique. Keys never end in `0`, per
  `storage.OrderKeyBetween`'s invariant.
- **A file that fails to parse does not fail the migration.** Its error is
  recorded in `MigrationReport.Files[i].Error` and the rest still import — a
  switcher dragging in twelve files should not lose eleven of them to one bad
  one. The same applies to one bad collection inside a data dump.

---

## The report

`importer.MigrationReport` (see `internal/importer/migration.go`) is what the UI
renders verbatim:

| Field | Meaning |
|---|---|
| `workspaceId` / `workspaceName` | The workspace the migration landed in. The id is stamped by the app layer after persisting. |
| `collections`, `folders`, `requests`, `environments`, `variables` | What came across. `folders` includes the one folder minted per collection. |
| `scriptsTranslated` | Postman scripts rewritten into AUK's API. |
| `scriptsPartial` | The **subset** of those with at least one line left as a commented TODO. |
| `warnings[]` | `{request, kind, detail}` — `kind` is one of `script`, `auth`, `body`, `variable`, `protocol`, `other`. Empty means a fully clean migration. |
| `files[]` | `{name, format, error, requests}` per input file. `format` is `postman`, `postman-dump` or `environment`; `error` set means the file could not be read. |

---

## Code layout

| File | Role |
|---|---|
| `internal/importer/postmandump.go` | Detects and parses the Export Data dump (shape-tolerant: probes the plausible keys, then falls back to a structural walk) and the standalone environment export, incl. the secret rule. |
| `internal/importer/pmscript.go` | `pm.*` → AUK script translation, plus the parse-safety machinery. |
| `internal/importer/migrate.go` | `MigrateFromPostman([]NamedContent)` — detection, merge, re-minting, the parallel collection scan that reads the `event[]`/`description` fields `ParsePostman` ignores, and the report. Pure; no I/O. |
| `app_migrate.go` | The untestable shell: native multi-select dialog, file reads, and persistence through `a.store`. |

The split is the same one `app_export.go` uses, and for the same reason: the
dialog opens a real macOS panel, so **no test may call
`MigrateFromPostmanFiles`**. Everything worth testing is exercised through
`MigrateFromPostman` over in-memory content.
