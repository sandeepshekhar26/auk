// The "Migrate from Postman" data contract + the small pure helpers the
// wizard renders from. Kept out of the component so the shaping rules
// (grouping, labelling, pluralisation) are readable on their own.
//
// Mirrors internal/importer/migration.go — keep the two in sync.

import { wails } from './wails'

export interface MigrationWarning {
  /** The request (or environment / collection) name the warning is about. */
  request: string
  /** One of: script | auth | body | variable | protocol | other. */
  kind: string
  /** Plain-English explanation of what didn't come across. */
  detail: string
}

export interface MigrationFile {
  name: string
  /** "postman" | "postman-dump" | "environment", or "" when unparseable. */
  format: string
  error?: string
  requests: number
}

export interface MigrationReport {
  workspaceId: string
  workspaceName: string
  collections: number
  folders: number
  requests: number
  environments: number
  variables: number
  scriptsTranslated: number
  scriptsPartial: number
  warnings: MigrationWarning[]
  files: MigrationFile[]
}

// MigrateFromPostmanFiles / PostmanInstalled are Go bindings on *App
// (app_migrate.go). Wails binds every exported App method at RUNTIME by
// reflection (they land on window.go.main.App), but the typed wrappers in
// frontend/wailsjs/go/main/App.{d.ts,js} only appear after the next `wails
// dev`/build regenerates them. So we reach them through a locally-typed view
// of the bindings module — same pattern as ResponseViewer.tsx's
// saveResponseBodyBinding and SettingsModal.tsx's mockBindings — which keeps
// tsc and the bundler green until that regeneration runs.
//
// The one addition over those two: we fall back to the runtime object when
// the generated wrapper isn't there yet. The generated wrapper is only a thin
// shim over window.go.main.App.<Method>, and reflection publishes the method
// there as soon as the Go side exists — so this call works against a running
// backend BEFORE the JS is regenerated, instead of throwing "not a function".
// See INTEGRATION NOTES.
interface MigrateBindings {
  MigrateFromPostmanFiles(): Promise<MigrationReport>
  PostmanInstalled(): Promise<boolean>
}

function generatedBindings(): Partial<MigrateBindings> {
  return wails as unknown as Partial<MigrateBindings>
}

function runtimeBindings(): Partial<MigrateBindings> | undefined {
  return (globalThis as unknown as { go?: { main?: { App?: Partial<MigrateBindings> } } }).go?.main?.App
}

const NOT_BOUND =
  'Postman migration is not available in this build yet. Rebuild the app so the migration bindings are generated.'

/**
 * Opens a native multi-select file dialog, migrates every chosen file into a
 * single new workspace, and resolves with the report. Rejects if the user's
 * files could not be migrated at all; a cancelled dialog is the backend's
 * business (it reports zero files rather than failing).
 */
export function migrateFromPostmanFiles(): Promise<MigrationReport> {
  const generated = generatedBindings()
  if (typeof generated.MigrateFromPostmanFiles === 'function') return generated.MigrateFromPostmanFiles()
  const live = runtimeBindings()
  if (typeof live?.MigrateFromPostmanFiles === 'function') return live.MigrateFromPostmanFiles()
  return Promise.reject(new Error(NOT_BOUND))
}

/**
 * Whether Postman's data directory exists on this Mac. Purely a trust signal
 * in the UI, so any failure (including the binding not existing yet) is
 * reported as "not detected" rather than surfacing an error.
 */
export async function postmanInstalled(): Promise<boolean> {
  try {
    const generated = generatedBindings()
    if (typeof generated.PostmanInstalled === 'function') return await generated.PostmanInstalled()
    const live = runtimeBindings()
    if (typeof live?.PostmanInstalled === 'function') return await live.PostmanInstalled()
  } catch {
    /* fall through — absence of a signal is not an error worth showing */
  }
  return false
}

// Warning kinds, in the order they're shown. Scripts and auth come first
// because they're the ones most likely to need a human before the first run.
export const WARNING_KIND_ORDER = ['script', 'auth', 'body', 'variable', 'protocol', 'other'] as const

export const WARNING_KIND_LABEL: Record<string, string> = {
  script: 'Scripts',
  auth: 'Authentication',
  body: 'Request bodies',
  variable: 'Variables',
  protocol: 'Protocols',
  other: 'Other',
}

export const FILE_FORMAT_LABEL: Record<string, string> = {
  postman: 'Postman collection',
  'postman-dump': 'Postman data export',
  environment: 'Postman environment',
}

export function fileFormatLabel(format: string): string {
  return FILE_FORMAT_LABEL[format] ?? (format || 'Unrecognized')
}

/**
 * Groups warnings by kind, in WARNING_KIND_ORDER, with any unrecognized kind
 * appended afterwards so a backend that grows a new kind still renders it
 * rather than silently dropping it.
 */
export function groupWarnings(warnings: MigrationWarning[]): { kind: string; items: MigrationWarning[] }[] {
  const byKind = new Map<string, MigrationWarning[]>()
  for (const w of warnings) {
    const list = byKind.get(w.kind) ?? []
    list.push(w)
    byKind.set(w.kind, list)
  }
  const out: { kind: string; items: MigrationWarning[] }[] = []
  for (const kind of WARNING_KIND_ORDER) {
    const items = byKind.get(kind)
    if (items?.length) {
      out.push({ kind, items })
      byKind.delete(kind)
    }
  }
  for (const [kind, items] of byKind) out.push({ kind, items })
  return out
}

/** "1 request" / "2 requests" — no i18n needed, this app ships English only. */
export function plural(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`
}

/**
 * The one-line result the user reads first. Honest when nothing came across:
 * a big green "Migrated 0 requests" would be a lie of tone.
 */
export function migrationHeadline(report: MigrationReport): string {
  if (report.requests === 0) return 'No requests found in those files'
  if (report.collections > 0) {
    return `Migrated ${plural(report.requests, 'request')} from ${plural(report.collections, 'collection')}`
  }
  return `Migrated ${plural(report.requests, 'request')}`
}
