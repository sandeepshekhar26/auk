import { For, Show, createEffect, createMemo, createSignal, on, onCleanup, onMount } from 'solid-js'
import { migrateModalOpen, setAppState, setMigrateModalOpen } from '../lib/store'
import { loadWorkspaces } from '../lib/data'
import {
  fileFormatLabel,
  groupWarnings,
  migrateFromPostmanFiles,
  migrationHeadline,
  plural,
  postmanInstalled,
  WARNING_KIND_LABEL,
  type MigrationReport,
} from '../lib/migrate'
import { IconChevronRight } from './icons'

// MigrateModal: the "Migrate from Postman" wizard — the first screen a
// Postman refugee touches, so it's built as three deliberate beats rather
// than one file picker:
//
//   1. Export      — the exact click path inside Postman, because "export
//                    your data" is the step people actually get stuck on.
//   2. Choose      — one native multi-select dialog; several collections and
//                    environment files land in ONE workspace.
//   3. Review      — an honest report. The migration succeeded even when it
//                    has warnings, so warnings use the warn/amber token and
//                    collapse when there are many; only a file that failed to
//                    parse gets the danger token.
//
// This is a MIGRATION, not the general Import surface (ImportCurlModal) —
// that one takes a single pasted payload; this one takes a pile of files and
// answers "did all of my stuff come across, and what didn't?".

const STEPS = [
  { n: 1, label: 'Export' },
  { n: 2, label: 'Choose files' },
  { n: 3, label: 'Review' },
] as const

/** Warnings beyond this many collapse behind a disclosure — see the render. */
const WARNINGS_COLLAPSE_THRESHOLD = 6

function Spinner() {
  return (
    <svg class="h-3.5 w-3.5 animate-spin" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" />
      <path class="opacity-90" d="M12 2a10 10 0 0 1 10 10" stroke="currentColor" stroke-width="3" stroke-linecap="round" />
    </svg>
  )
}

/** A Postman click path rendered as chips — skimmable at a glance, and it wraps rather than overflowing. */
function ClickPath(props: { parts: string[] }) {
  return (
    <div class="flex flex-wrap items-center gap-1">
      <For each={props.parts}>
        {(part, i) => (
          <>
            <Show when={i() > 0}>
              <IconChevronRight size={11} class="shrink-0 text-ink-faint" />
            </Show>
            <span
              class="rounded bg-raised px-1.5 py-0.5 text-[11px]"
              classList={{
                'font-medium text-ink': i() === props.parts.length - 1,
                'text-ink-dim': i() !== props.parts.length - 1,
              }}
            >
              {part}
            </span>
          </>
        )}
      </For>
    </div>
  )
}

function Stat(props: { value: number; label: string }) {
  return (
    <div class="min-w-0 px-2 py-2 text-center">
      <div class="text-base font-semibold tabular-nums text-ink">{props.value}</div>
      <div class="truncate text-[10px] uppercase tracking-wide text-ink-faint">{props.label}</div>
    </div>
  )
}

export default function MigrateModal() {
  const [step, setStep] = createSignal<1 | 2 | 3>(1)
  const [running, setRunning] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const [report, setReport] = createSignal<MigrationReport | null>(null)
  const [detected, setDetected] = createSignal(false)
  const [warningsExpanded, setWarningsExpanded] = createSignal(true)
  // The primary action of whichever step is showing. A signal (not a bare
  // ref) so swapping steps re-runs the focus effect with the NEW button.
  const [primaryEl, setPrimaryEl] = createSignal<HTMLButtonElement>()

  const warningGroups = createMemo(() => groupWarnings(report()?.warnings ?? []))
  const warningCount = createMemo(() => report()?.warnings.length ?? 0)

  // Dismissal (Escape, backdrop, ×) is blocked while a migration is in
  // flight: the workspace still gets created, so closing here would strand
  // the user with a new workspace and no report explaining what came across.
  // The button is already showing "Migrating…", so nothing looks frozen.
  function close() {
    if (running()) return
    setMigrateModalOpen(false)
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape' && migrateModalOpen()) close()
  }

  onMount(() => window.addEventListener('keydown', onKeyDown))
  onCleanup(() => window.removeEventListener('keydown', onKeyDown))

  // Opening always starts a fresh run at step 1 (a stale report from a
  // previous visit would be confusing), and re-probes for Postman.
  createEffect(
    on(migrateModalOpen, (open) => {
      if (!open) return
      setStep(1)
      setReport(null)
      setError(null)
      postmanInstalled().then(setDetected)
    }),
  )

  // Focus the step's primary action as it renders, so the whole wizard is
  // Return-Return-Return for a keyboard user.
  createEffect(() => {
    const el = primaryEl()
    if (el && migrateModalOpen()) el.focus()
  })

  async function chooseFiles() {
    setRunning(true)
    setError(null)
    try {
      const result = await migrateFromPostmanFiles()
      setReport(result)
      // A long amber list reads as failure at a glance even when the
      // migration worked, so a big pile starts collapsed.
      setWarningsExpanded(result.warnings.length <= WARNINGS_COLLAPSE_THRESHOLD)
      setStep(3)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setRunning(false)
    }
  }

  // Switching to the migrated workspace: refresh the workspace list, then set
  // the active id — App.tsx's activeWorkspaceId effect loads that workspace's
  // folders/requests/environments, exactly as picking one in WorkspaceSwitcher
  // does. Same refresh path ImportCurlModal uses after importing a collection.
  async function openMigratedWorkspace() {
    const id = report()?.workspaceId
    if (!id) return
    try {
      await loadWorkspaces()
      setAppState('activeWorkspaceId', id)
      close()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <Show when={migrateModalOpen()}>
      <div class="fixed inset-0 z-50 flex items-start justify-center bg-black/50 pt-16" onClick={close}>
        <div
          role="dialog"
          aria-modal="true"
          aria-label="Migrate from Postman"
          class="flex max-h-[82vh] w-full max-w-2xl flex-col overflow-hidden rounded-lg border border-edge-strong bg-field shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <header class="shrink-0 border-b border-edge px-4 py-3">
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <h2 class="text-sm font-semibold text-ink">Migrate from Postman</h2>
                <p class="mt-0.5 text-xs text-ink-muted">
                  Brings your collections, folders, requests, environments and variables across into a new workspace.
                </p>
              </div>
              <button
                aria-label="Close"
                class="shrink-0 rounded px-2 py-0.5 text-sm text-ink-muted hover:bg-raised hover:text-ink focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-40"
                disabled={running()}
                onClick={close}
              >
                ×
              </button>
            </div>

            <ol class="mt-3 flex items-center gap-1.5 text-[11px]">
              <For each={STEPS}>
                {(s, i) => (
                  // The separator lives INSIDE the <li> — an <ol> may only
                  // contain <li> children.
                  <li aria-current={step() === s.n ? 'step' : undefined} class="flex items-center gap-1.5">
                    <Show when={i() > 0}>
                      <IconChevronRight size={11} class="shrink-0 text-ink-faint" />
                    </Show>
                    <span
                      class="flex items-center gap-1.5 rounded px-1.5 py-0.5"
                      classList={{
                        'bg-accent text-accent-contrast font-medium': step() === s.n,
                        'text-ink-faint': step() !== s.n,
                      }}
                    >
                      <span class="tabular-nums opacity-70">{s.n}</span>
                      <span>{s.label}</span>
                    </span>
                  </li>
                )}
              </For>
            </ol>
          </header>

          <div class="min-h-0 flex-1 overflow-y-auto px-4 py-4">
            {/* ---------------------------------------------------------- */}
            {/* Step 1 — get the files out of Postman.                     */}
            {/* ---------------------------------------------------------- */}
            <Show when={step() === 1}>
              <div class="flex flex-col gap-3">
                <Show when={detected()}>
                  <p class="flex items-center gap-2 text-xs text-ink-dim">
                    <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-accent" />
                    Postman detected on this Mac.
                  </p>
                </Show>

                <div class="grid gap-3 sm:grid-cols-2">
                  <section class="min-w-0 rounded-md border border-edge bg-app p-3">
                    <div class="flex items-center gap-2">
                      <h3 class="text-xs font-semibold text-ink">Everything at once</h3>
                      <span class="rounded bg-accent px-1.5 py-0.5 text-[10px] font-medium text-accent-contrast">
                        Recommended
                      </span>
                    </div>
                    <p class="mt-1 text-xs text-ink-muted">In Postman:</p>
                    <div class="mt-1.5">
                      <ClickPath parts={['Settings (⚙)', 'Data', 'Export Data']} />
                    </div>
                    <p class="mt-2 text-xs text-ink-muted">
                      Gives you one JSON file with every collection and environment in it.
                    </p>
                  </section>

                  <section class="min-w-0 rounded-md border border-edge bg-app p-3">
                    <h3 class="text-xs font-semibold text-ink">One collection</h3>
                    <p class="mt-1 text-xs text-ink-muted">Right-click a collection:</p>
                    <div class="mt-1.5">
                      <ClickPath parts={['Export', 'Collection v2.1']} />
                    </div>
                    <p class="mt-2 text-xs text-ink-muted">For environments:</p>
                    <div class="mt-1.5">
                      <ClickPath parts={['Environments', '⋯', 'Export']} />
                    </div>
                  </section>
                </div>

                <p class="text-xs text-ink-faint">
                  Either way works — and you can pick several files at once on the next step.
                </p>
              </div>
            </Show>

            {/* ---------------------------------------------------------- */}
            {/* Step 2 — the native multi-select picker.                   */}
            {/* ---------------------------------------------------------- */}
            <Show when={step() === 2}>
              <div class="flex flex-col gap-3">
                <div>
                  <h3 class="text-xs font-semibold text-ink">Choose everything you exported</h3>
                  <p class="mt-1 text-xs text-ink-muted">
                    Select as many files as you like — collections and environment files together. They merge into one
                    new workspace, and nothing leaves this Mac.
                  </p>
                </div>

                <button
                  ref={setPrimaryEl}
                  class="flex w-full items-center justify-center gap-2 rounded-md bg-accent px-3 py-3 text-sm font-medium text-accent-contrast hover:bg-accent-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed disabled:opacity-60"
                  disabled={running()}
                  onClick={() => void chooseFiles()}
                >
                  <Show when={running()}>
                    <Spinner />
                  </Show>
                  {running() ? 'Migrating…' : 'Choose Postman files…'}
                </button>

                <Show when={running()}>
                  <p class="text-xs text-ink-muted">
                    Reading your collections and translating scripts — a big export can take a few seconds.
                  </p>
                </Show>

                <Show when={error()}>
                  <div class="rounded-md border border-danger-edge bg-danger-bg/40 px-3 py-2">
                    <p class="text-xs font-medium text-danger">Migration failed</p>
                    <p class="mt-1 break-words text-xs text-danger">{error()}</p>
                    <p class="mt-1.5 text-xs text-ink-muted">You can pick the files again to retry.</p>
                  </div>
                </Show>
              </div>
            </Show>

            {/* ---------------------------------------------------------- */}
            {/* Step 3 — the report. The screen that earns the trust.      */}
            {/* ---------------------------------------------------------- */}
            <Show when={step() === 3 ? report() : null}>
              {(r) => {
                return (
                  <div class="flex flex-col gap-4">
                    <div>
                      <h3 class="text-sm font-semibold text-ink">{migrationHeadline(r())}</h3>
                      <p class="mt-0.5 break-words text-xs text-ink-muted">
                        Landed in a new workspace called <span class="font-medium text-ink-dim">{r().workspaceName}</span>.
                      </p>
                    </div>

                    <div class="grid grid-cols-5 divide-x divide-edge overflow-hidden rounded-md border border-edge bg-app">
                      <Stat value={r().collections} label="Collections" />
                      <Stat value={r().folders} label="Folders" />
                      <Stat value={r().requests} label="Requests" />
                      <Stat value={r().environments} label="Environments" />
                      <Stat value={r().variables} label="Variables" />
                    </div>

                    {/* Scripts: a notice, never an error — a partial script
                        still came across, with the untranslatable lines left
                        in place as TODO comments. */}
                    <Show when={r().scriptsTranslated > 0 || r().scriptsPartial > 0}>
                      <section
                        class="rounded-md border p-3"
                        classList={{
                          'border-warn-edge bg-warn/10': r().scriptsPartial > 0,
                          'border-edge bg-app': r().scriptsPartial === 0,
                        }}
                      >
                        <h4 class="text-xs font-semibold text-ink">
                          {plural(r().scriptsTranslated, 'test script')} translated
                        </h4>
                        <Show
                          when={r().scriptsPartial > 0}
                          fallback={
                            <p class="mt-1 text-xs text-ink-muted">
                              Every <span class="font-mono">pm.*</span> script was rewritten into AUK's script API.
                            </p>
                          }
                        >
                          <p class="mt-1 text-xs text-warn">
                            {plural(r().scriptsPartial, 'script')} needed manual attention.
                          </p>
                          <p class="mt-1 text-xs text-ink-muted">
                            Those came across too — the lines that couldn't be translated are left in the script as{' '}
                            <span class="font-mono">// TODO</span> comments, so nothing was silently dropped.
                          </p>
                        </Show>
                      </section>
                    </Show>

                    {/* Warnings: grouped by kind so six auth notes read as
                        ONE thing to deal with rather than six failures, and
                        collapsed past the threshold so a long amber list
                        can't make a successful migration look broken. */}
                    <Show when={warningCount() > 0}>
                      <section class="rounded-md border border-warn-edge bg-warn/10">
                        <button
                          class="flex w-full items-center justify-between gap-2 px-3 py-2 text-left focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
                          aria-expanded={warningsExpanded()}
                          onClick={() => setWarningsExpanded((v) => !v)}
                        >
                          <span class="min-w-0">
                            <span class="text-xs font-semibold text-ink">
                              {plural(warningCount(), 'thing')} to look at
                            </span>
                            <span class="ml-2 text-xs text-ink-muted">
                              Everything else came across cleanly.
                            </span>
                          </span>
                          <IconChevronRight
                            size={14}
                            class={`shrink-0 text-ink-muted transition-transform ${warningsExpanded() ? 'rotate-90' : ''}`}
                          />
                        </button>

                        <Show when={warningsExpanded()}>
                          <div class="flex flex-col gap-3 border-t border-warn-edge px-3 py-2.5">
                            <For each={warningGroups()}>
                              {(group) => (
                                <div class="min-w-0">
                                  <h4 class="text-[10px] font-semibold uppercase tracking-wide text-warn">
                                    {WARNING_KIND_LABEL[group.kind] ?? group.kind} · {group.items.length}
                                  </h4>
                                  <ul class="mt-1 flex flex-col gap-1">
                                    <For each={group.items}>
                                      {(w) => (
                                        <li class="min-w-0 break-words text-xs leading-relaxed">
                                          <span class="font-medium text-ink-dim">{w.request}</span>
                                          <span class="text-ink-faint"> — </span>
                                          {/* whitespace-pre-wrap: a warning detail can carry an
                                              indented code snippet (collection- and folder-level
                                              scripts embed the TRANSLATED body so it can be pasted
                                              straight in), and collapsing that to one line would
                                              make it unusable. */}
                                          <span class="whitespace-pre-wrap break-words text-ink-muted">{w.detail}</span>
                                        </li>
                                      )}
                                    </For>
                                  </ul>
                                </div>
                              )}
                            </For>
                          </div>
                        </Show>
                      </section>
                    </Show>

                    {/* Silence would be ambiguous here ("did it not check?"),
                        so a clean migration says so explicitly. */}
                    <Show when={warningCount() === 0}>
                      <p class="flex items-center gap-2 text-xs text-ink-dim">
                        <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-accent" />
                        No warnings — everything came across cleanly.
                      </p>
                    </Show>

                    <section>
                      <h4 class="text-[10px] font-semibold uppercase tracking-wide text-ink-muted">
                        {plural(r().files.length, 'file')}
                      </h4>
                      <ul class="mt-1.5 flex flex-col gap-1">
                        <For each={r().files}>
                          {(f) => (
                            <li
                              class="flex flex-col gap-1 rounded border px-2 py-1.5"
                              classList={{
                                'border-danger-edge bg-danger-bg/40': !!f.error,
                                'border-edge bg-app': !f.error,
                              }}
                            >
                              <div class="flex items-center gap-2">
                                <span class="min-w-0 flex-1 truncate font-mono text-[11px] text-ink-dim" title={f.name}>
                                  {f.name}
                                </span>
                                <Show
                                  when={!f.error}
                                  fallback={
                                    <span class="shrink-0 text-[10px] font-medium uppercase tracking-wide text-danger">
                                      Could not read
                                    </span>
                                  }
                                >
                                  <span class="shrink-0 rounded bg-raised px-1.5 py-0.5 text-[10px] text-ink-muted">
                                    {fileFormatLabel(f.format)}
                                  </span>
                                  <span class="shrink-0 text-[11px] tabular-nums text-ink-muted">
                                    {plural(f.requests, 'request')}
                                  </span>
                                </Show>
                              </div>
                              {/* Parser errors can be long; own line + wrapping
                                  so one bad file can't widen the dialog. */}
                              <Show when={f.error}>
                                <p class="break-words text-[11px] text-danger">{f.error}</p>
                              </Show>
                            </li>
                          )}
                        </For>
                      </ul>
                    </section>

                    <Show when={error()}>
                      <p class="break-words rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-xs text-danger">
                        {error()}
                      </p>
                    </Show>
                  </div>
                )
              }}
            </Show>
          </div>

          <footer class="flex shrink-0 items-center justify-between gap-2 border-t border-edge px-4 py-3">
            <div class="flex items-center gap-2">
              <Show when={step() === 2}>
                <button
                  class="rounded px-3 py-1.5 text-xs text-ink-muted hover:bg-raised disabled:cursor-not-allowed disabled:opacity-50"
                  disabled={running()}
                  onClick={() => setStep(1)}
                >
                  Back
                </button>
              </Show>
              <Show when={step() === 3}>
                <button
                  class="rounded px-3 py-1.5 text-xs text-ink-muted hover:bg-raised"
                  onClick={() => {
                    setError(null)
                    setStep(2)
                  }}
                >
                  Migrate more files
                </button>
              </Show>
            </div>

            <div class="flex items-center gap-2">
              <Show when={step() !== 3}>
                <button
                  class="rounded px-3 py-1.5 text-xs text-ink-muted hover:bg-raised disabled:cursor-not-allowed disabled:opacity-50"
                  disabled={running()}
                  onClick={close}
                >
                  Cancel
                </button>
              </Show>
              <Show when={step() === 1}>
                <button
                  ref={setPrimaryEl}
                  class="rounded bg-accent px-3 py-1.5 text-xs font-medium text-accent-contrast hover:bg-accent-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                  onClick={() => setStep(2)}
                >
                  I have my export files
                </button>
              </Show>
              <Show when={step() === 3}>
                <button
                  ref={setPrimaryEl}
                  class="rounded bg-accent px-3 py-1.5 text-xs font-medium text-accent-contrast hover:bg-accent-hover focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                  onClick={() => void openMigratedWorkspace()}
                >
                  Open the migrated workspace
                </button>
              </Show>
            </div>
          </footer>
        </div>
      </div>
    </Show>
  )
}
