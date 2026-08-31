/**
 * Workspace export actions.
 *
 * Lifted out of CommandPalette.tsx when the keyboard layer arrived: the
 * command registry (lib/keymap.ts) and the palette both need these, and a
 * component is the wrong owner for an action other code invokes.
 */

import { appState, setLoadError } from './store'
import { wails } from './wails'

// SaveFileDialog returning "" means the user cancelled — a normal outcome,
// not a failure, so only a thrown error surfaces through loadError.
export async function exportActiveWorkspace(): Promise<void> {
  if (!appState.activeWorkspaceId) return
  try {
    await wails.ExportWorkspace(appState.activeWorkspaceId)
  } catch (err) {
    setLoadError(err instanceof Error ? err.message : String(err))
  }
}

// ExportWorkspaceOpenAPI is a Go binding (app_export.go). It auto-binds by
// reflection on the next wails build; reached through a locally-typed view so
// tsc/build stay green until then (same pattern as the mock-server bindings).
export async function exportActiveWorkspaceOpenAPI(): Promise<void> {
  if (!appState.activeWorkspaceId) return
  try {
    await (wails as unknown as { ExportWorkspaceOpenAPI(id: string): Promise<string> }).ExportWorkspaceOpenAPI(
      appState.activeWorkspaceId,
    )
  } catch (err) {
    setLoadError(err instanceof Error ? err.message : String(err))
  }
}
