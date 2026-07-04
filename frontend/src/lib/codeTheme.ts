// Shared CodeMirror syntax-color theme — used by both the request Body
// editor and the response viewer so a request's JSON and its response read
// consistently. Colors reference the same semantic CSS variables the rest
// of the UI uses (index.css), so JSON highlighting flips with the app's
// light/dark theme automatically instead of needing its own light/dark pair.
//
// This is deliberately our own palette, not a copy of any other tool's:
// strings reuse the app's brand green, keys use the cooler "info" blue,
// numbers use "warn" amber, booleans/keywords use a dedicated violet
// (--color-keyword, index.css), and null is dimmed + italic rather than
// alarming — null is "absent", not an error.
import { HighlightStyle } from '@codemirror/language'
import { tags as t } from '@lezer/highlight'

const cssVar = (name: string) => `rgb(var(--color-${name}))`

export const jsonHighlightStyle = HighlightStyle.define([
  { tag: t.propertyName, color: cssVar('info') },
  { tag: t.string, color: cssVar('accent-fg') },
  { tag: t.number, color: cssVar('warn') },
  { tag: t.bool, color: cssVar('keyword'), fontWeight: '600' },
  { tag: t.null, color: cssVar('ink-faint'), fontStyle: 'italic' },
  { tag: [t.punctuation, t.separator], color: cssVar('ink-muted') },
  { tag: [t.squareBracket, t.brace, t.paren], color: cssVar('ink-muted') },
  { tag: t.invalid, color: cssVar('danger'), textDecoration: 'underline wavy' },
])

// A slightly broader mapping for languages with keywords/comments (GraphQL,
// generic text) — a superset of jsonHighlightStyle's rules for when a body
// kind later gets a real grammar attached.
export const codeHighlightStyle = HighlightStyle.define([
  { tag: t.keyword, color: cssVar('keyword'), fontWeight: '600' },
  { tag: t.definitionKeyword, color: cssVar('keyword'), fontWeight: '600' },
  { tag: t.typeName, color: cssVar('warn') },
  { tag: t.variableName, color: cssVar('info') },
  { tag: t.propertyName, color: cssVar('info') },
  { tag: t.string, color: cssVar('accent-fg') },
  { tag: t.number, color: cssVar('warn') },
  { tag: t.bool, color: cssVar('keyword'), fontWeight: '600' },
  { tag: t.null, color: cssVar('ink-faint'), fontStyle: 'italic' },
  { tag: [t.comment, t.lineComment, t.blockComment], color: cssVar('ink-faint'), fontStyle: 'italic' },
  { tag: [t.punctuation, t.separator], color: cssVar('ink-muted') },
  { tag: [t.squareBracket, t.brace, t.paren], color: cssVar('ink-muted') },
  { tag: t.operator, color: cssVar('ink-dim') },
  { tag: t.invalid, color: cssVar('danger'), textDecoration: 'underline wavy' },
])
