// "Copy as <language>" code generation.
//
// Every generator renders from ONE input — a ResolvedSnippet fetched from
// the Go backend (App.ResolveForSnippet) — so cURL/Python/JS/Go can't drift
// from each other, and none of them can drift from what a real Send puts on
// the wire.
//
// This used to be computed client-side from the stored RequestDef, which
// meant the snippet reproduced the DEFINITION rather than the REQUEST:
// `${baseUrl}` and `:id` came out literal, only basic/bearer/apikey auth was
// reproduced (by a hand-rolled reimplementation that could drift from
// internal/auth), JWT/OAuth2/SigV4 degraded to an apologetic comment, and a
// pre-request script's headers were missing entirely. ResolveForSnippet runs
// the engine's real resolve path instead (templating -> path params -> auth
// -> pre-request script), so what you paste is what AUK would send.
import { wails } from './wails'
import { flushRequestSave } from './data'
import type { ProtocolKind } from '../types'

export interface SnippetHeader {
  key: string
  value: string
}

export interface ResolvedSnippet {
  method: string
  url: string
  headers: SnippetHeader[]
  /** null when the request has no body at all (vs. an empty-string body). */
  body: string | null
}

/**
 * Resolves a request into exactly what would be sent, without sending it.
 *
 * Flushes any pending debounced save first: ResolveForSnippet reads the
 * request from the backend store, so without this it would resolve a
 * version up to 400ms stale — i.e. copying right after typing a URL would
 * silently give you the PREVIOUS URL.
 */
export async function resolveSnippet(requestId: string, environmentId: string): Promise<ResolvedSnippet> {
  await flushRequestSave(requestId)
  const r = await wails.ResolveForSnippet(requestId, environmentId)
  return {
    method: r.method,
    url: r.url,
    headers: (r.headers ?? []).map((h) => ({ key: h.key, value: h.value })),
    body: r.hasBody ? r.body : null,
  }
}

/**
 * WS/SSE/gRPC don't fit the single request/response shape these formats
 * generate for, so the menu is disabled rather than offering a "Copy as
 * Python" that can't mean anything for a streaming connection. Mirrors the
 * backstop check in ResolveForSnippet.
 */
export function canCopySnippet(protocol: ProtocolKind | undefined): boolean {
  return protocol === 'http' || protocol === 'graphql'
}

// POSIX single-quoting: everything inside '...' is literal, and an embedded
// single quote is closed, escaped, and reopened ('\'').
function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`
}

// JSON's escaping (\", \\, \n, \r, \t, \uXXXX) is a subset of Python/JS/Go's
// own double-quoted string literal escaping, so JSON.stringify's output is
// valid source syntax in all three languages for the realistic content here
// (header/URL/body text) — one implementation, not three near-duplicates.
function dqStr(s: string): string {
  return JSON.stringify(s)
}

function buildCurl(r: ResolvedSnippet): string {
  // r.method is shell-quoted like every other field: it is NOT constrained
  // to the verb dropdown on every path — a Postman import (ToUpper preserves
  // shell metacharacters) or a git-shared workspace YAML can carry an
  // arbitrary method string, and the generated command is pasted into a
  // shell, so an unquoted method is a command-injection vector.
  const parts = ['curl', '-X', shellQuote(r.method)]
  for (const h of r.headers) parts.push('-H', shellQuote(`${h.key}: ${h.value}`))
  // --data-raw rather than -d: -d strips newlines and would mangle a
  // pretty-printed JSON body, and (unlike --data) it never treats a leading
  // '@' as "read this file".
  if (r.body !== null) parts.push('--data-raw', shellQuote(r.body))
  parts.push(shellQuote(r.url))
  return parts.join(' ')
}

function buildPython(r: ResolvedSnippet): string {
  const lines: string[] = ['import requests', '', `url = ${dqStr(r.url)}`]
  const callArgs: string[] = []
  if (r.headers.length > 0) {
    lines.push('headers = {')
    for (const h of r.headers) lines.push(`    ${dqStr(h.key)}: ${dqStr(h.value)},`)
    lines.push('}')
    callArgs.push('headers=headers')
  }
  if (r.body !== null) {
    lines.push(`data = ${dqStr(r.body)}`)
    callArgs.push('data=data')
  }
  lines.push('', `response = requests.request(${dqStr(r.method)}, url${callArgs.length > 0 ? ', ' + callArgs.join(', ') : ''})`)
  lines.push('print(response.status_code)', 'print(response.text)')
  return lines.join('\n')
}

function buildFetch(r: ResolvedSnippet): string {
  const opts: string[] = [`  method: ${dqStr(r.method)},`]
  if (r.headers.length > 0) {
    opts.push('  headers: {')
    for (const h of r.headers) opts.push(`    ${dqStr(h.key)}: ${dqStr(h.value)},`)
    opts.push('  },')
  }
  if (r.body !== null) opts.push(`  body: ${dqStr(r.body)},`)
  return [
    `fetch(${dqStr(r.url)}, {`,
    ...opts,
    '})',
    '  .then((response) => response.text())',
    '  .then((text) => console.log(text))',
  ].join('\n')
}

function buildGo(r: ResolvedSnippet): string {
  const lines: string[] = ['package main', '', 'import (', '\t"fmt"', '\t"io"', '\t"net/http"']
  if (r.body !== null) lines.push('\t"strings"')
  lines.push(')', '', 'func main() {')
  const bodyExpr = r.body !== null ? `strings.NewReader(${dqStr(r.body)})` : 'nil'
  lines.push(`\treq, err := http.NewRequest(${dqStr(r.method)}, ${dqStr(r.url)}, ${bodyExpr})`, '\tif err != nil {', '\t\tpanic(err)', '\t}')
  for (const h of r.headers) lines.push(`\treq.Header.Set(${dqStr(h.key)}, ${dqStr(h.value)})`)
  lines.push(
    '',
    '\tresp, err := http.DefaultClient.Do(req)',
    '\tif err != nil {',
    '\t\tpanic(err)',
    '\t}',
    '\tdefer resp.Body.Close()',
    '',
    '\tbody, _ := io.ReadAll(resp.Body)',
    '\tfmt.Println(resp.StatusCode)',
    '\tfmt.Println(string(body))',
    '}',
  )
  return lines.join('\n')
}

export interface SnippetFormat {
  id: string
  label: string
  build: (r: ResolvedSnippet) => string
}

export const SNIPPET_FORMATS: SnippetFormat[] = [
  { id: 'curl', label: 'cURL', build: buildCurl },
  { id: 'python', label: 'Python (requests)', build: buildPython },
  { id: 'js', label: 'JavaScript (fetch)', build: buildFetch },
  { id: 'go', label: 'Go (net/http)', build: buildGo },
]
