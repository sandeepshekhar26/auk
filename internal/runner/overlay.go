package runner

import (
	"apitool/internal/core"
)

// SyntheticEnvironmentID is retained for backward compatibility only. The
// data-driven runner used to pass this reserved id as the environment id when
// the user selected none, so the (now-removed) store overlay had something to
// materialize the iteration's columns into. It no longer does: a data-driven
// run with no --env now passes an EMPTY environment id and carries the row's
// columns through a run-scoped variable layer (core.RunScopedVars), which
// mergedEnvironment merges even with no environment loaded.
//
// The constant stays because the CLI reporter (internal/reporters) filters it
// out of its "environment: …" line and an older RunSummary could still carry
// it. Namespaced with a colon so it can never collide with a real (uuid)
// environment id.
const SyntheticEnvironmentID = "auk:data-iteration"

// varOverlay is intentionally vestigial. The data-driven runner used to swap
// engine.Store for a WRAPPER that layered each iteration's row onto
// GetEnvironment/ListFolders. That design was removed because it:
//
//   - embedded the narrow core.Store INTERFACE, silently dropping every
//     optional interface the engine relies on — lastResponseStore (so
//     response() chaining re-fired the target on every reference),
//     PutEnvironment/ListEnvironmentsRaw (so vars.set could not persist);
//   - mutated the SHARED engine.Store field with no synchronization, a real
//     data race against any concurrent send, plus a leaked overlay on
//     overlapping runs.
//
// Iteration variables now ride through core.RunScopedVars — mutex-guarded and
// threaded through context — and the engine's Store is never wrapped or
// mutated. The type is kept solely so the regression checks asserting the
// store was NEVER wrapped (`engine.Store.(*varOverlay)` must be false) still
// have a type to name; nothing constructs it.
type varOverlay struct{ core.Store }
