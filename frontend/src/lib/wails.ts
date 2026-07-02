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

export const wails = App
export const events = runtime

export function onStreamWake(sessionId: string, handler: () => void): () => void {
  const eventName = `stream:${sessionId}`
  runtime.EventsOn(eventName, handler)
  return () => runtime.EventsOff(eventName)
}
