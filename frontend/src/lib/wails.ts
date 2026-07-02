// Thin wrapper around the Wails-generated bindings + runtime events.
//
// Streaming contract (see docs/02-architecture.md §6): the Go backend never
// pushes payloads through EventsEmit directly — EventsEmit is a lossy,
// non-backpressured wake-up signal only (Wails issues #2448, #2759). Every
// stream event carries just a sessionId; the frontend then PULLS the
// authoritative, coalesced batch via a binding call. Never trust EventsEmit
// payloads as the source of truth for ordering or completeness.

import * as runtime from '../../wailsjs/runtime/runtime'
import * as App from '../../wailsjs/go/main/App'
import { model } from '../../wailsjs/go/models'

export const wails = App
export const events = runtime

// Wails generates a class per Go struct (with a `convertValues` method)
// for its bound-method parameter/return types, which our hand-written
// plain-object types in ../types.ts don't structurally satisfy. Wrap a
// plain object with e.g. `models.RequestDef.createFrom(draft)` before
// passing it to a binding call that expects one.
export const models = model

export function onStreamWake(sessionId: string, handler: () => void): () => void {
  const eventName = `stream:${sessionId}`
  runtime.EventsOn(eventName, handler)
  return () => runtime.EventsOff(eventName)
}
