# Export: Workspace → OpenAPI 3.1 (and portable JSON)

**Status:** built · **Package:** `internal/exporter` · **Bindings:** `App.ExportWorkspace` (JSON), `App.ExportWorkspaceOpenAPI` (OpenAPI)

Export is the answer to the single biggest lock-in objection an API-client buyer
has: *"what if this app dies like Paw?"* AUK closes the loop — a collection you
import, validate, and run can be handed back out to a vendor-neutral standard at
any time. There are two export paths:

1. **Portable AUK JSON** (`internal/exporter/exporter.go`) — a full-fidelity dump
   of folders, requests, and environments, tagged `auk-workspace-v1`. Best for
   round-tripping *inside* AUK or moving a workspace between machines.
2. **OpenAPI 3.1** (`internal/exporter/openapi.go`) — a valid, vendor-neutral
   spec that any OpenAPI tool (Swagger UI, code generators, other API clients)
   can consume. This is the anti-lock-in artifact, and the inverse of
   `internal/importer/openapi.go` (import → validate → **export** round-trip).

Both paths read environments through `storage.FileStore.ListEnvironmentsRaw`, so
**no keychain secret value is ever resolved into the file**.

---

## OpenAPI 3.1 export

`exporter.ExportOpenAPI(store, workspaceID) ([]byte, error)` and its YAML twin
`exporter.ExportOpenAPIYAML(...)` build one OpenAPI 3.1 document from a
workspace. The spec BUILDING (`buildOpenAPIDoc`) is a pure function over the
three collections — no I/O, no dialog — so it is exhaustively unit-tested; the
native save dialog lives apart in `App.ExportWorkspaceOpenAPI`
(`app_export.go`).

### What it covers

| OpenAPI element | Source in AUK | Notes |
|---|---|---|
| `info.title` | Workspace name | Falls back to `AUK Workspace`. `info.version` is a fixed `1.0.0`. |
| `servers[]` | Distinct `scheme://host` prefixes across request URLs | A templated `${baseUrl}` host becomes a `{baseUrl}` **server variable**; its `default` is drawn from a matching **non-secret** environment variable, else a neutral placeholder. Constant hosts are emitted verbatim, minus any `user:pass@` userinfo (see the redaction guarantee). |
| `paths` | One entry per distinct URL **path template** | `:id` (Postman-style) and `${var}` path segments both become OpenAPI `{id}` placeholders with a path-level `parameters` entry (`in: path`, `required: true`). |
| query `parameters` | `RequestDef.Params` | `in: query`, name + `{type: string}` schema only — **never the stored value**. A param covered by an `apiKey`-in-query scheme is dropped. |
| header `parameters` | `RequestDef.Headers` | `in: header`, name + schema only. `Authorization`/`Content-Type`/`Accept`/`Content-Length`/`Host`/`Cookie` (OpenAPI-reserved) and hop-by-hop headers are dropped; an `apiKey`-in-header name is dropped (covered by security). |
| `requestBody` | `RequestDef.Body` | `json`→`application/json` (body as example), `form`→`application/x-www-form-urlencoded`, `graphql`→`application/json` (`{query, variables}`), `text`→its `Content-Type` (or `text/plain`), `binary`→`application/octet-stream` (`format: binary`). |
| `operationId` / `summary` / `description` | Request name / name / `Description` | `operationId` is the name camel-joined and de-collided; `summary` is the raw name. |
| `tags` | Folder names | Each operation is tagged with its folder; the top-level `tags[]` lists the folders used. |
| `components.securitySchemes` + per-op `security` | `RequestDef.Auth` | See mapping below. |

### Auth kind → security scheme

| AUK `AuthKind` | OpenAPI scheme | Fields emitted (mechanism only) |
|---|---|---|
| `basic` | `http` | `scheme: basic` |
| `bearer` | `http` | `scheme: bearer` |
| `jwt` | `http` | `scheme: bearer`, `bearerFormat: JWT` |
| `digest` | `http` | `scheme: digest` |
| `apikey` | `apiKey` | `in: header|query`, `name: <key name>` (the header/query **name**, never the value) |
| `oauth2` | `oauth2` | `flows.clientCredentials` with `tokenUrl` + `scopes` (never the client id/secret) |
| `awsSigV4`, `oauth1` | *(omitted)* | No standard OpenAPI scheme; the operation is exported without a `security` entry. |

Schemes are de-duplicated by content, so two requests using the same mechanism
share one scheme; differing `apiKey` names get distinct, name-derived scheme
ids.

### Secret-redaction guarantee

The export **never writes a credential value** into the document:

- Environments are read via `ListEnvironmentsRaw` (never the keychain-resolving
  variant), and server-variable defaults are drawn only from **non-secret**
  variables — any name listed in an environment's `Secrets` is skipped, so a
  keychain value can never become a server URL.
- `securitySchemes` describe the *mechanism* only — no passwords, tokens, client
  secrets, or API-key values.
- Header and query parameters are emitted by **name + schema only**, so a token
  hardcoded in a custom header cannot leak through the parameter list.
- **URL userinfo is stripped.** A request URL is free text, and
  `https://admin:hunter2@api.example.com/v1/things` is a legal one people
  really do paste in from a curl command or an internal tool. The `user:pass@`
  component is removed from every exported `servers[].url` (and from any
  environment value composed into one), keeping scheme, host and port:
  `servers: - url: https://api.example.com`. Paths and examples are unaffected —
  splitting a URL already puts the userinfo entirely in the server half.

The only user data intentionally reproduced is a request **body**, emitted as an
example (from the raw, unresolved body text, so `${var}` references stay
literal) — exactly as the JSON export already does.

This is enforced by `TestExportOpenAPI_NeverLeaksAnyCredential`, which plants a
distinct secret string in every auth kind (bearer token, apiKey value, basic
password, oauth2 client id/secret, digest password, an `Authorization` header
value) plus an environment secret, and asserts none appear anywhere in either
serialization — across two workspaces, one whose credentials live in auth
blocks and one whose credentials live in the **URL**.
`TestExportOpenAPI_RedactsEnvironmentSecret` covers the specific
raw-environment-secret case, and `TestExportOpenAPI_StripsURLUserinfo` the
positive half of the userinfo rule: the credential is gone *and* the host it
was attached to still points somewhere real.

### What it approximates

- **Servers from templated URLs.** A fully-templated host (`${baseUrl}/…`)
  becomes a `{baseUrl}` server variable rather than a resolved absolute URL —
  pragmatic and round-trips with the importer, which reads `servers[0]` back
  into a `baseUrl` variable.
- **Schema inference is light-touch.** A JSON body yields a top-level object
  schema with each field's *type* (string/integer/number/boolean/object/array),
  not a deep recursive JSON-Schema. The **example** carries the detail.
- **One operation per path+method.** OpenAPI allows only one; if two requests
  collide on the same path and method, the first (in a deterministic order)
  wins.
- **Non-HTTP protocols are skipped.** gRPC/WebSocket/SSE requests have no
  path/method shape a REST spec can represent.

---

## Bindings & UI integration

`App.ExportWorkspaceOpenAPI(workspaceID)` (in `app_export.go`, package `main`)
mirrors `App.ExportWorkspace`: it opens a native `SaveFileDialog` defaulting to
`<workspace>.openapi.yaml` with YAML and JSON filters, and writes YAML or JSON
based on the chosen extension. Being a method on `*App`, it is auto-exposed to
the frontend by Wails' reflection binding — no change to `app.go` is needed.

Suggested frontend wiring (not built here): add an **"Export as OpenAPI"** entry
to the command palette and the workspace/activity-rail export menu next to the
existing "Export Workspace" (JSON) action, calling
`ExportWorkspaceOpenAPI(currentWorkspaceId)` and surfacing the returned path in a
toast (empty string = user cancelled).
