package main

import "context"

// Cancellation for in-flight one-shot requests, exposed to the frontend.
//
// A GUI Send has no deadline — a request to a slow or hung endpoint would
// otherwise sit on "Sending…" forever with no way out. SendRequest registers
// a per-request cancel func (see app.go); CancelSend fires it, so the
// engine's RunRequest returns with a context-cancelled error that the
// frontend surfaces like any other failed send.

// sendCancel wraps a cancel func so map entries can be compared by pointer
// identity (context.CancelFunc, like every func value, is not comparable).
// See App.sendCancels for why identity matters.
type sendCancel struct{ cancel context.CancelFunc }

// CancelSend aborts the in-flight send for requestID, if one is running. A
// no-op if nothing is in flight for that id (e.g. the request already
// completed between the click and this call), so it's always safe to call.
func (a *App) CancelSend(requestID string) {
	a.sendMu.Lock()
	entry := a.sendCancels[requestID]
	a.sendMu.Unlock()
	if entry != nil {
		entry.cancel()
	}
}
