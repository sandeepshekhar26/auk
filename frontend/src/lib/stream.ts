// Frontend lifecycle for live WebSocket/SSE sessions.
//
// The backend (stream.go) buffers frames and only emits a "stream:<id>"
// wake-up per the streaming contract in ./wails.ts — never the payload. So
// this module: starts a session, subscribes to its wake-ups, and on each wake
// PULLS the authoritative batch via DrainStream, appending frames to the store
// for StreamConsole to render. On close it releases the backend session.

import { wails, onStreamWake } from './wails'
import { pushStreamEvent, setActiveStream, clearActiveStream, activeStreams } from './store'
import { flushRequestSave } from './data'
import type { StreamEvent } from '../types'

const cursors = new Map<string, number>() // sessionId -> next cursor
const unsubs = new Map<string, () => void>() // sessionId -> wake unsubscribe
const sessionRequest = new Map<string, string>() // sessionId -> requestId
const draining = new Map<string, boolean>() // sessionId -> a drain loop is running
const pending = new Map<string, boolean>() // sessionId -> a wake arrived mid-drain

// drain is serialized per session: only one loop runs at a time, and any
// wake-up that arrives while it's awaiting DrainStream just sets a pending flag
// so the loop re-drains once more. Without this, two wakes could both read the
// same cursor before it advances and re-append the same frames (observed as
// duplicated console lines).
async function drain(sessionId: string): Promise<void> {
  if (draining.get(sessionId)) {
    pending.set(sessionId, true)
    return
  }
  draining.set(sessionId, true)
  try {
    for (;;) {
      pending.set(sessionId, false)
      const cursor = cursors.get(sessionId) ?? 0
      let res
      try {
        res = await wails.DrainStream(sessionId, cursor)
      } catch {
        teardown(sessionId)
        return
      }
      cursors.set(sessionId, res.cursor)
      for (const frame of res.frames ?? []) {
        pushStreamEvent(frame as unknown as StreamEvent)
      }
      if (res.closed) {
        teardown(sessionId)
        return
      }
      if (!pending.get(sessionId)) break
    }
  } finally {
    draining.set(sessionId, false)
  }
}

// teardown unwires a session locally once it has closed and been fully drained.
// Safe to call more than once. It only clears the request→session mapping if it
// still points at THIS session, so a fast Disconnect-then-Connect on the same
// request (whose old session tears down after the new one starts) can't wipe
// the new session's active state.
function teardown(sessionId: string): void {
  const off = unsubs.get(sessionId)
  if (off) off()
  unsubs.delete(sessionId)
  cursors.delete(sessionId)
  draining.delete(sessionId)
  pending.delete(sessionId)
  const requestId = sessionRequest.get(sessionId)
  if (requestId && activeStreams()[requestId] === sessionId) clearActiveStream(requestId)
  sessionRequest.delete(sessionId)
}

export async function startStream(requestId: string, environmentId: string): Promise<void> {
  // Ensure the backend has this request's latest protocol/URL/body before it
  // resolves the session — the editor's saves are debounced (see data.ts).
  await flushRequestSave(requestId)
  const sessionId = await wails.StartStream(requestId, environmentId)
  cursors.set(sessionId, 0)
  sessionRequest.set(sessionId, requestId)
  unsubs.set(
    sessionId,
    onStreamWake(sessionId, () => {
      drain(sessionId).catch(() => {})
    }),
  )
  setActiveStream(requestId, sessionId)
  // Immediate drain to pick up frames buffered before the subscription (the
  // "connected" meta + any seed message).
  drain(sessionId).catch(() => {})
}

export async function stopStream(requestId: string): Promise<void> {
  const sessionId = activeStreams()[requestId]
  if (!sessionId) return
  // Clear the UI immediately (button flips back to Connect), but keep the wake
  // subscription so the backend's final "disconnected" frame still drains in.
  // The subsequent closed drain runs teardown to fully unwire.
  clearActiveStream(requestId)
  wails.StopStream(sessionId).catch(() => {})
}

export async function sendStreamMessage(requestId: string, text: string): Promise<void> {
  const sessionId = activeStreams()[requestId]
  if (!sessionId) return
  await wails.SendStreamMessage(sessionId, text)
}
