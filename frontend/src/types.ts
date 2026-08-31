// Shared TS types mirroring Go domain structs in internal/core/model.
// Field names/JSON tags MUST stay in sync with internal/core/model/*.go.

export type ID = string

export interface Workspace {
  id: ID
  name: string
  orderKey: string
}

export interface Folder {
  id: ID
  workspaceId: ID
  parentId: ID | null
  name: string
  orderKey: string
  // Inherited by every request in this folder (and its subfolders) as a
  // layer between the workspace Environment and the request itself — the
  // closest folder wins on a name collision. See core.Engine.folderVariables.
  variables: KeyValue[]
}

export type ProtocolKind = 'http' | 'websocket' | 'grpc' | 'graphql' | 'sse'

export interface RequestDef {
  id: ID
  workspaceId: ID
  folderId: ID | null
  name: string
  // Free-form notes/docs, versioned with the request. See Description in model.go.
  description?: string
  protocol: ProtocolKind
  method: string
  url: string
  headers: KeyValue[]
  params: KeyValue[]
  // Values for `:name` path placeholders in url (e.g. /users/:id) —
  // see PathParams in internal/core/model/model.go.
  pathParams?: KeyValue[] | null
  body: RequestBody | null
  authRef: AuthConfig | null
  perf?: PerfConfig | null
  assertions?: Assertion[] | null
  // A small JS snippet run (internal/scripting, grafana/sobek) after
  // templating+auth but before the request is sent — ctx.request.
  // {method,url,headers,body} to read, ctx.setHeader(name, value) to
  // add/override a header. Empty/absent skips scripting entirely.
  preRequestScript?: string
  // JS run AFTER the response: test()/expect() assertions plus the ability to
  // write variables back to the environment (auth-token chaining). See
  // PostResponseScript in internal/core/model/model.go.
  postResponseScript?: string
  // Per-request transport settings (client cert for mTLS, custom CA, or skip
  // verify) — orthogonal to authRef, since a request can need a client
  // certificate at the TLS layer independent of its Authorization scheme.
  tls?: RequestTLSConfig | null
  // Routes this request through a manual proxy (e.g. "http://host:8080"),
  // independent of tls/authRef for the same reason those are independent
  // of each other.
  proxyUrl?: string | null
  orderKey: string
}

export interface RequestTLSConfig {
  clientCertPem?: string
  clientKeyPem?: string
  customCaPem?: string
  insecureSkipVerify?: boolean
}

export type AssertionSource = 'status' | 'body' | 'header' | 'responseTime'
export type AssertionOperator = 'eq' | 'neq' | 'contains' | 'exists' | 'notExists' | 'lt' | 'gt' | 'matches'

export interface Assertion {
  source: AssertionSource
  path?: string
  name?: string
  operator: AssertionOperator
  value?: string
  enabled: boolean
}

export interface AssertionResult {
  assertion: Assertion
  passed: boolean
  actual: string
  error?: string
}

export type PerfExecutor = 'constant-vus' | 'ramping-vus'

export interface PerfStage {
  duration: string
  target: number
}

export interface PerfThreshold {
  metric: string
  expression: string
}

export interface PerfConfig {
  executor: PerfExecutor
  vus?: number
  duration?: string
  stages?: PerfStage[]
  thresholds?: PerfThreshold[]
}

export interface PerfSamplePoint {
  timeOffsetMs: number
  rps: number
  p95Ms: number
  p99Ms: number
  avgMs: number
  errorRate: number
  activeVUs: number
}

export interface PerfThresholdResult {
  metric: string
  expression: string
  passed: boolean
}

export interface PerfResult {
  requestId: string
  requests: number
  rps: number
  failRate: number
  durationAvgMs: number
  durationMinMs: number
  durationMedMs: number
  durationP90Ms: number
  durationP95Ms: number
  durationMaxMs: number
  thresholdResults: PerfThresholdResult[]
  passed: boolean
  exitCode: number
  wallMs: number
  timestamp: string
  error?: string
}

export interface KeyValue {
  key: string
  value: string
  enabled: boolean
}

export type BodyKind = 'none' | 'json' | 'text' | 'form' | 'binary' | 'graphql'

export interface RequestBody {
  kind: BodyKind
  text?: string
  formFields?: KeyValue[]
  graphqlVariables?: string
}

export type AuthKind = 'none' | 'basic' | 'bearer' | 'apikey' | 'jwt' | 'oauth2' | 'awsSigV4' | 'oauth1' | 'digest'

export interface AuthConfig {
  kind: AuthKind
  basic?: { username: string; password: string }
  bearer?: { token: string }
  apikey?: { key: string; value: string; in: 'header' | 'query' }
  jwt?: { secret: string; algorithm: string; claims: string }
  oauth2?: {
    grantType?: 'client_credentials' | 'authorization_code'
    clientId: string
    clientSecret: string
    tokenUrl: string
    authUrl?: string
    scopes?: string[]
    audience?: string
  }
  awsSigV4?: {
    profile?: string
    accessKeyId: string
    secretAccessKey: string
    region: string
    service: string
    sessionToken?: string
  }
  oauth1?: { consumerKey: string; consumerSecret: string; token?: string; tokenSecret?: string }
  digest?: DigestAuth | null
}

export interface Environment {
  id: ID
  workspaceId: ID
  name: string
  color: string | null
  variables: KeyValue[]
  secrets: string[] // variable names stored in keychain, not inline
}

export interface ResponseData {
  requestId: ID
  status: number
  statusText: string
  headers: KeyValue[]
  bodyBase64: string
  bodySize: number
  timingMs: number
  timestamp: string
  error?: string
  assertionResults?: AssertionResult[]
  // Outcomes of test() calls in the post-response script (see TestResult in
  // internal/core/model/assert.go).
  testResults?: TestResult[] | null
  // console.* output captured from the post-response script.
  scriptLogs?: string[] | null
  // Set when the post-response script itself failed to run — a script that
  // can't run is a FAILED run, not a silent pass.
  scriptError?: string
  // Per-phase breakdown for the FINAL hop (nil for non-HTTP protocols). A
  // phase reading 0 was legitimately skipped (e.g. TLS on plain HTTP, DNS
  // on a reused connection), not unmeasured.
  timing?: TimingBreakdown | null
  // One entry per hop actually sent, only present when the request
  // followed one or more redirects.
  redirectChain?: RedirectHop[] | null
}

export interface TimingBreakdown {
  dnsMs: number
  connectMs: number
  tlsMs: number
  ttfbMs: number
  totalMs: number
}

export interface RedirectHop {
  method: string
  url: string
  status: number
  timingMs: number
}

export interface HistoryEntry {
  id: ID
  requestId: ID
  requestName: string
  method: string
  url: string
  status: number
  timingMs: number
  timestamp: string
}

export interface FolderRunResult {
  requestId: ID
  requestName: string
  response: ResponseData
}

export interface StreamEvent {
  sessionId: ID
  kind: 'ws' | 'sse' | 'grpc' | 'perf'
  direction: 'sent' | 'received' | 'meta' | 'error' | 'done'
  payload: string
  timestamp: string
}

export interface GrpcMethodInfo {
  clientStreaming: boolean
  serverStreaming: boolean
}

export type GitFileChangeStatus = 'added' | 'modified' | 'deleted' | 'renamed' | 'untracked'

export interface GitFileChange {
  path: string
  status: GitFileChangeStatus
}

export interface GitStatus {
  isRepo: boolean
  branch: string
  clean: boolean
  hasRemote: boolean
  files: GitFileChange[]
}

export interface GitCommit {
  hash: string
  message: string
  author: string
  date: string
}

export type McpTransportKind = 'stdio' | 'http'

// A developer-configured target MCP server AUK connects to as a CLIENT to
// debug it — distinct from AUK's own embedded MCP SERVER (Settings → MCP
// Server), which exposes AUK's tools to Claude in the other direction.
export interface McpConnection {
  id: ID
  workspaceId: ID
  name: string
  transport: McpTransportKind
  command?: string
  args?: string[]
  url?: string
  bearerToken?: string
}

export interface McpToolInfo {
  name: string
  title?: string
  description: string
  inputSchema: unknown
  outputSchema?: unknown
  readOnlyHint: boolean
  destructiveHint: boolean
  idempotentHint: boolean
}

export interface McpContentBlock {
  type: 'text' | 'image' | 'audio' | 'unknown'
  text?: string
  mimeType?: string
  dataBase64?: string
}

export interface McpCallResult {
  content: McpContentBlock[]
  structuredContent?: unknown
  isError: boolean
}

export interface CommandItem {
  id: string
  title: string
  subtitle?: string
  shortcut?: string
  group: 'navigation' | 'action' | 'request'
  run: () => void
}

export interface TestResult {
  name: string
  passed: boolean
  error?: string
}

export interface DigestAuth {
  username: string
  password: string
}
