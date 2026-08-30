# 09 — CI runner: failing a build with AUK

An API client that can't fail a build is a developer toy. This is the piece
that makes AUK a **test runner**: point it at a folder, get a non-zero exit
code and a JUnit report your CI already knows how to read.

```bash
apitool-cli run-folder <folderID> --workspace-dir ./collection --env staging \
  --reporter cli --reporter junit --reporter-out results.xml
```

Nothing here is a second execution path. The runner drives the *same*
`core.Engine.RunRequest` chokepoint the GUI's Send button uses, built by the
*same* `internal/appcore.NewEngine` (docs/02-architecture.md §1) — so a
request that passes on someone's laptop behaves identically in CI, including
auth, folder variables, pre-request scripts, post-response `test()`s and the
Dispatch policy check. What CI adds is scope (a folder, a workspace), data
iteration, reports, and an exit code.

Layout:

| Package | Role |
| --- | --- |
| `internal/runner` | headless run engine: traversal, iterations, verdicts. No Wails, no reporters. |
| `internal/reporters` | pure formatters: JUnit XML, JSON, console. Never decide pass/fail. |
| `cmd/cli` | flags, reporter wiring, process exit code. |

---

## 1. Commands

```
apitool-cli run           <requestID>   [flags]   run one request
apitool-cli run-folder    <folderID>    [flags]   run a folder and every subfolder
apitool-cli run-workspace [workspaceID] [flags]   run every request in a workspace
apitool-cli help
```

- `run` is unchanged from the original smoke-test runner: it still prints
  status/timing/headers/body/assertions. Pass any `--reporter` and it emits
  reports instead.
- `run-workspace`'s id is optional when the directory holds exactly one
  workspace (the normal case) — CI configs shouldn't have to hardcode a uuid.
- Ids come from the sidebar (right-click → Copy ID) or straight out of the
  YAML files, which are the source of truth on disk.

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--workspace-dir DIR` | `$AUK_WORKSPACE_DIR`, else `$PWD` | workspace root (the directory containing `workspaces/`) |
| `--env ID` | none | environment to resolve `${variables}` against |
| `--data FILE` | none | CSV/JSON data file — run the target once per row |
| `--iterations N` | 1 | repeat count; **with `--data`, stop after N rows** |
| `--reporter NAME` | `cli` | `cli` \| `junit` \| `json`. Repeatable. |
| `--reporter-out PATH` | stdout | output file for the preceding `--reporter` (or `NAME=PATH`). Repeatable. |
| `--bail` | off | stop at the first failed request |
| `--timeout DUR` | `60s` | per-request timeout; `0` disables |
| `--delay DUR` | `0` | pause between requests, e.g. `250ms` — for rate-limited APIs |

Flag order never matters: `run-folder <id> --bail --env=x` and
`run-folder --bail --env=x <id>` are the same command (see `reorderFlags` in
`cmd/cli/main.go` — Go's `flag` package stops parsing at the first positional
argument, which used to silently drop flags).

Selecting `--reporter junit` **replaces** the default console reporter (same
as Newman). Ask for both if you want both:
`--reporter cli --reporter junit --reporter-out results.xml`.

### Execution order

A folder run executes depth-first in the order the sidebar tree shows: the
folder's own requests by `orderKey`, then each subfolder by `orderKey`,
recursed. A workspace run starts with unfoldered requests, then each root
folder's subtree. Requests run **sequentially** — API tests routinely depend
on order (log in, then use the token), and a parallel runner would break
`response('Other request')` chaining.

---

## 2. Exit codes

| Code | Meaning | Typical cause |
| --- | --- | --- |
| `0` | every request passed | ✅ green build |
| `1` | at least one request failed | a failed assertion, a failed `test()`, a post-response script that threw, a transport error, or a >= 400 response on a request with no assertions |
| `2` | the run could not start or complete | unknown request/folder/workspace id, unreadable or malformed data file, unknown reporter, unwritable report path, cancelled run |

Both `1` and `2` fail a CI build; the split tells you *whether the API broke
or the pipeline did*.

### What counts as a failure

One rule, in one place — `runner.Verdict` — shared by the exit code, the
console ✓/✗, and the JUnit failure count, so they can never disagree:

1. A transport/engine error (connection refused, unresolved `${variable}`,
   blocked by policy) always fails.
2. Otherwise `model.ResponseData.Passed()` decides: any failed assertion, any
   failed script `test()`, or a post-response script that could not run.
3. A request that declares **no checks at all** is treated as a smoke test:
   HTTP >= 400 fails it. As soon as a request declares one assertion or test,
   those checks are the whole verdict — so a deliberate `status eq 404`
   assertion passes on a 404.

A run that executed **zero requests** is a failure, not a pass. "I ran
nothing" must never green-light a build.

---

## 3. Reporters

### JUnit XML (`--reporter junit`)

The CI lingua franca — Jenkins (`junit` step), GitLab
(`artifacts:reports:junit`), GitHub Actions (any test-reporter action),
CircleCI and Bitbucket all parse it.

- one `<testsuite>` per request (per iteration),
- one `<testcase>` per **check** — an assertion or a script `test()` — so the
  build annotation says *`status eq 200` failed*, not *"request 3 failed"*,
- `<failure>` for a check that failed, `<error>` for a request that never
  completed,
- a request with no checks still emits one synthetic `request completed`
  testcase, so a bare smoke-test folder can turn a build red instead of
  reporting "0 tests",
- data-driven runs suffix the suite name with `[iteration N]` so each row is
  distinguishable.

Everything is written through `encoding/xml`, so `<`, `&` and `"` in a
failure message are escaped and the report still parses.

Real output from a failing run (`xmllint --noout` clean):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<testsuites name="AUK — folder f-smoke" tests="4" failures="2" errors="0" time="0.004">
  <testsuite name="Smoke / Health check" tests="2" failures="0" errors="0" time="0.002" timestamp="2026-08-30T13:42:54Z">
    <testcase name="status eq 200" classname="Smoke / Health check" time="0.002"></testcase>
    <testcase name="body.status eq ok" classname="Smoke / Health check" time="0.002"></testcase>
  </testsuite>
  <testsuite name="Smoke / API version" tests="1" failures="1" errors="0" time="0.000" timestamp="2026-08-30T13:42:54Z">
    <testcase name="body.query.v eq v2 &lt;prod&gt; &amp; &#34;beta&#34;" classname="Smoke / API version" time="0.000">
      <failure message="expected body.query.v eq v2 &lt;prod&gt; &amp; &#34;beta&#34;, actual: v1" type="AssertionFailure">GET ${baseUrl}/echo?v=${apiVersion}&#xA;status 200&#xA;expected body.query.v eq v2 &lt;prod&gt; &amp; &#34;beta&#34;, actual: v1</failure>
    </testcase>
  </testsuite>
  <testsuite name="Smoke / Users / Fetch user" tests="1" failures="1" errors="0" time="0.000" timestamp="2026-08-30T13:42:54Z">
    <testcase name="body.query.user neq from-environment" classname="Smoke / Users / Fetch user" time="0.000">
      <failure message="expected body.query.user neq from-environment, actual: from-environment" type="AssertionFailure">GET ${baseUrl}/echo?user=${user}&#xA;status 200&#xA;expected body.query.user neq from-environment, actual: from-environment</failure>
    </testcase>
  </testsuite>
</testsuites>
```

`failure/@type` is one of `AssertionFailure`, `TestFailure`, `ScriptError`,
`RequestFailure`; `error/@type` is `RequestError`.

### JSON (`--reporter json`)

The full structured summary, for dashboards, flaky-test trackers, and
anything that wants more than pass/fail. `schemaVersion` is bumped only on a
**breaking** change (a removed or retyped field); new optional fields do not
bump it, so `schemaVersion == 1` is safe to pin.

```jsonc
{
  "tool": "auk",
  "schemaVersion": 1,
  "target": "folder f-smoke",       // what was run
  "environmentId": "env-ci",        // omitted when no --env
  "dataFile": "users.csv",          // omitted when no --data
  "startedAt": "2026-08-30T13:42:54Z",
  "durationMs": 4,
  "iterations": 1,
  "passed": false,                  // the run verdict == (exit code 0)
  "bailed": false,                  // true when --bail cut the run short
  "summary": {
    "requests": 3, "requestsPassed": 1, "requestsFailed": 2,
    "checks": 4,   "checksPassed": 2,   "checksFailed": 2
  },
  "results": [
    {
      "iteration": 1,
      "requestId": "r-health",
      "requestName": "Health check",
      "folderPath": ["Smoke"],      // always an array, never null
      "method": "GET",
      "url": "${baseUrl}/health",   // as configured, before templating
      "status": 200,
      "statusText": "OK",
      "durationMs": 2,
      "passed": true,
      "reason": "",                 // why it failed; omitted when passed
      "error": "",                  // transport/engine error; omitted when absent
      "scriptError": "",            // post-response script that could not run
      "checks": [
        { "name": "status eq 200", "kind": "assertion", "passed": true }
      ]
    }
  ]
}
```

`checks[].kind` is `assertion` (declarative), `test` (a script `test()`),
`script` (the script itself failed), or `request` (the synthetic check for a
request that declared none). A failed check adds `"message"`.

### Console (`--reporter cli`)

The default. Deliberately un-colored — CI log viewers mangle ANSI escapes,
and ✓/✗ already carry the signal.

```
AUK · folder f-users  ·  env env-ci  ·  data users.csv (3 iteration(s))

Iteration 1
  ✓ Smoke / Users / Fetch user  ·  GET 200  ·  4ms
Iteration 2
  ✓ Smoke / Users / Fetch user  ·  GET 200  ·  3ms
Iteration 3
  ✗ Smoke / Users / Fetch user  ·  GET 500  ·  9ms
      ✗ status eq 200 — expected status eq 200, actual: 500

──────────────────────────────────────────────────────────────
  requests   3 (2 passed, 1 failed)
  checks     3 (2 passed, 1 failed)
  duration   1.2s
  FAILED — 1 of 3 request(s) failed
```

### Writing several reports at once

Paths bind to reporters **by position**, or explicitly as `NAME=PATH`:

```bash
# positional
--reporter junit --reporter-out results.xml --reporter json --reporter-out results.json

# explicit (order-independent), console still on stdout
--reporter cli --reporter junit --reporter json \
  --reporter-out junit=results.xml --reporter-out json=results.json
```

A reporter with no path writes to stdout (`-` also means stdout). Missing
parent directories are created. A report that cannot be written is fatal
(exit 2) — a job that silently produced no JUnit file would report "no tests"
and pass.

---

## 4. Data-driven runs

`--data FILE` runs the **whole target once per row**, exposing that row's
columns as `${variables}`. Rows stream one at a time, so a 10k-row CSV is
never loaded into memory.

### CSV

First row is the header; each later row is one iteration.

```csv
user,plan,expectedStatus
ada,pro,200
grace,"enterprise, annual",200
alan,free,402
```

- quoted fields with commas, embedded newlines and `""` escapes work
  (`encoding/csv`),
- a leading UTF-8 BOM (Excel) is stripped — otherwise the first column would
  be named `﻿user` and `${user}` would resolve to nothing,
- header names are whitespace-trimmed,
- a row with the wrong number of fields is a hard error naming the file and
  line: silently padding it would run a green build against variables that
  were never set,
- `.tsv` is the same thing, tab-separated.

### JSON

An array of objects:

```json
[
  { "user": "ada",   "plan": "pro",        "retries": 3 },
  { "user": "grace", "plan": "enterprise", "retries": 0 }
]
```

Numbers keep their literal text (`10000000000000000001` and `3.50` survive —
no float round-trip), `true`/`false` become `"true"`/`"false"`, `null` becomes
`""`, and a nested object/array becomes compact JSON so a request body can
embed it with `${payload}`.

The format is chosen by extension (`.csv`, `.tsv`, `.json`), falling back to
sniffing the first non-whitespace byte, so an extension-less fixture works.
A file with a header but no data rows is an error, not a zero-iteration pass.

### Variable precedence

Weakest to strongest:

```
environment  <  folder (outer)  <  folder (inner)  <  data-file row
```

A data column **overrides** an environment variable or a folder variable of
the same name; any name the row does not define falls through to the folder
chain and then the environment. Verified end-to-end in
`internal/runner/runner_test.go`
(`TestE2EDataFileOverridesEnvironmentAndFolder`).

`--env` is optional for a data-driven run: with no environment selected, the
runner passes a reserved internal environment id
(`auk:data-iteration`) that exists only to carry the row's columns.

### Iteration count

- `--data` alone: one iteration per row.
- `--data` + `--iterations N`: the first `min(N, rows)` rows.
- `--iterations N` with no data file: run the target N times (a crude soak
  test).

---

## 5. GitHub Actions

Copy-pasteable. Commit the workspace directory (`workspaces/**` YAML — the
git-friendly storage format is the whole point) next to your code.

```yaml
name: API tests

on:
  push:
    branches: [main]
  pull_request:

jobs:
  api-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'

      # Build the headless runner. No Wails, no GUI toolkit, no CGO —
      # internal/core is Wails-free by design, which is what makes this
      # a plain `go build` on a stock Linux runner.
      - name: Build the AUK runner
        run: go build -o auk-cli ./cmd/cli

      # Secrets: a one-row data file is the injection point (see "Secrets in
      # CI" below) — nothing secret is ever committed to the collection.
      - name: Materialize secrets
        run: printf 'apiToken\n%s\n' "$API_TOKEN" > /tmp/auk-secrets.csv
        env:
          API_TOKEN: ${{ secrets.API_TOKEN }}

      - name: Run the smoke folder
        env:
          SMOKE_FOLDER_ID: f-smoke
          STAGING_ENV_ID: env-staging
        run: |
          ./auk-cli run-folder "$SMOKE_FOLDER_ID" \
            --workspace-dir ./collection \
            --env "$STAGING_ENV_ID" \
            --data /tmp/auk-secrets.csv \
            --reporter cli \
            --reporter junit --reporter-out results.xml \
            --reporter json  --reporter-out results.json

      # Always publish the report, especially on failure.
      - name: Publish JUnit report
        if: always()
        uses: mikepenz/action-junit-report@v5
        with:
          report_paths: results.xml
          detailed_summary: true

      - name: Upload raw artifacts
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: auk-results
          path: |
            results.xml
            results.json
```

The step fails the job on its own: a failed assertion exits 1, so no
`continue-on-error` gymnastics and no parsing of stdout are needed.

### Secrets in CI

AUK keeps secret VALUES out of git by design (docs/02-architecture.md §7):
they live in the OS keychain, which a CI runner does not have. The supported
way to get a secret into a headless run today is a **one-row data file**,
written from the job's secret store and deleted with the runner:

```bash
printf 'apiToken\n%s\n' "$API_TOKEN" > /tmp/auk-secrets.csv
./auk-cli run-folder f-smoke --workspace-dir ./collection --data /tmp/auk-secrets.csv
```

Requests reference it as `${apiToken}`, and because a data row outranks the
environment (§4), the same collection can carry a harmless placeholder value
for local work. Combine secrets and iteration data in one file by adding the
column to every row.

There is deliberately no `${env(VAR)}` template function yet — see
"Integration notes" at the bottom.

Data-driven variant — same collection, a matrix of users:

```yaml
      - name: Regression sweep over users.csv
        run: |
          ./auk-cli run-folder f-checkout \
            --workspace-dir ./collection \
            --env env-staging \
            --data ./collection/fixtures/users.csv \
            --delay 200ms \
            --reporter cli \
            --reporter junit --reporter-out results-regression.xml
```

### GitLab CI

```yaml
api-tests:
  image: golang:1.25
  script:
    - go build -o auk-cli ./cmd/cli
    - ./auk-cli run-folder f-smoke --workspace-dir ./collection --env env-staging
        --reporter cli --reporter junit --reporter-out results.xml
  artifacts:
    when: always
    reports:
      junit: results.xml
```

### Jenkins

```groovy
sh 'go build -o auk-cli ./cmd/cli'
sh './auk-cli run-folder f-smoke --workspace-dir ./collection --env env-staging \
      --reporter cli --reporter junit --reporter-out results.xml'
junit 'results.xml'
```

---

## 6. Recipes

**Fail fast on a long suite** — `--bail` stops at the first failure, so a
broken deploy gate doesn't wait out 300 requests:

```bash
apitool-cli run-workspace --workspace-dir ./collection --env env-prod --bail
```

**Be kind to a rate-limited API** — `--delay 250ms` between requests,
`--timeout 10s` per request:

```bash
apitool-cli run-folder f-api --workspace-dir ./collection --delay 250ms --timeout 10s
```

**One command for every stage** — set `AUK_WORKSPACE_DIR` once in the job
environment and drop `--workspace-dir` everywhere.

**Local pre-push check** — the exact command CI runs, so a red pipeline is
never a surprise:

```bash
go build -o /tmp/auk-cli ./cmd/cli && /tmp/auk-cli run-folder f-smoke --env env-local
```

---

## 7. Using the runner as a library

`internal/runner` is deliberately independent of the CLI, so the GUI's "Run
folder" button, a future MCP `run_collection` tool, or a scheduled job can
reuse it verbatim:

```go
engine, store, err := appcore.NewEngine(dir)
summary, err := runner.RunFolder(ctx, engine, store, folderID, runner.Options{
    EnvironmentID: envID,
    DataFile:      "users.csv",
    Bail:          true,
})
_ = reporters.JUnit{}.Report(w, summary)
if !summary.Passed() { /* fail the caller */ }
```

`RunSummary` carries per-request results (name, id, folder path, status,
duration, flattened checks, and the raw `model.ResponseData`), plus the
tallies every reporter renders.

**One implementation note worth knowing:** iteration variables are injected
by wrapping the engine's `Store` for the duration of the run
(`internal/runner/overlay.go`). The wrapper layers the current row onto what
the engine already reads — the environment it loads and the folder chain it
walks — which is why a data column can outrank both without `core/engine.go`
knowing that data files exist. The engine is mutated in place and restored
when the run ends (verified by the e2e tests), so a run is **not** safe to
execute concurrently against a shared `*core.Engine`.

---

## 8. Integration notes / known gaps

- **`${env(VAR)}`** — the templater (`internal/templating`) has no function
  that reads a process environment variable, so CI secrets go through a data
  file (§5). A one-line `e.funcs["env"]` registration would make
  `${env(API_TOKEN)}` work everywhere `${...}` is honored; it belongs to
  whoever owns the templating package, not the runner.
- **The GUI's `App.RunFolder`** sorts a folder's whole subtree by `orderKey`
  in one flat pass, which interleaves subfolder requests with their parent's
  when the per-folder key sequences overlap (they normally do — keys are
  minted per folder). The CI runner walks the tree depth-first instead, which
  is what the sidebar actually shows. Pointing `app.go` at
  `runner.Plan`/`runner.Run` would remove that divergence and give the GUI
  bail/delay/data-file support for free.
- **Parallelism** is deliberately absent: requests are ordered and chained.
  A future `--parallel N` would need the store overlay in `overlay.go` to
  become per-run rather than per-engine.
