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
}

export type ProtocolKind = 'http' | 'websocket' | 'grpc' | 'graphql' | 'sse'

export interface RequestDef {
  id: ID
  workspaceId: ID
  folderId: ID | null
  name: string
  protocol: ProtocolKind
  method: string
  url: string
  headers: KeyValue[]
  params: KeyValue[]
  body: RequestBody | null
  authRef: AuthConfig | null
  orderKey: string
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

export type AuthKind = 'none' | 'basic' | 'bearer' | 'apikey' | 'jwt' | 'oauth2'

export interface AuthConfig {
  kind: AuthKind
  basic?: { username: string; password: string }
  bearer?: { token: string }
  apikey?: { key: string; value: string; in: 'header' | 'query' }
  jwt?: { secret: string; algorithm: string; claims: string }
  oauth2?: { clientId: string; clientSecret: string; tokenUrl: string; scopes?: string[] }
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

export interface StreamEvent {
  sessionId: ID
  kind: 'ws' | 'sse' | 'grpc' | 'perf'
  direction: 'sent' | 'received' | 'meta'
  payload: string
  timestamp: string
}

export interface CommandItem {
  id: string
  title: string
  subtitle?: string
  shortcut?: string
  group: 'navigation' | 'action' | 'request'
  run: () => void
}
