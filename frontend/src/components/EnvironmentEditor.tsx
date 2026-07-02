import { For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from 'solid-js'
import { appState, setAppState, environmentEditorOpen, setEnvironmentEditorOpen } from '../lib/store'
import { wails } from '../lib/wails'
import { model } from '../../wailsjs/go/models'
import type { Environment, KeyValue } from '../types'

// Secret values never live on the Environment object that gets rendered/serialized
// alongside plain variables — they're tracked here, per environment id + var key,
// only long enough to hand off to the CreateEnvironment/UpdateEnvironment bindings
// (which take the secret values as a separate Record<string, string> bound for the
// OS keychain). Closing or saving clears this out of memory.
type SecretValues = Record<string, string>

export default function EnvironmentEditor() {
  const [selectedId, setSelectedId] = createSignal<string | null>(null)
  const [secretValues, setSecretValues] = createSignal<SecretValues>({})
  const [saving, setSaving] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  // Ids minted by createEnvironment() in this session, not yet round-tripped
  // through CreateEnvironment. Used to pick Create vs Update on save.
  const [pendingNewIds, setPendingNewIds] = createSignal<Set<string>>(new Set())

  const workspaceEnvironments = createMemo(() =>
    appState.environments.filter((e) => e.workspaceId === appState.activeWorkspaceId),
  )

  const selected = createMemo(() => workspaceEnvironments().find((e) => e.id === selectedId()) ?? null)

  function close() {
    setEnvironmentEditorOpen(false)
    setSelectedId(null)
    setSecretValues({})
    setError(null)
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === 'Escape' && environmentEditorOpen()) close()
  }

  onMount(() => window.addEventListener('keydown', onKeyDown))
  onCleanup(() => window.removeEventListener('keydown', onKeyDown))

  createEffect(() => {
    if (environmentEditorOpen() && !selectedId() && workspaceEnvironments().length > 0) {
      setSelectedId(workspaceEnvironments()[0].id)
    }
  })

  function createEnvironment() {
    if (!appState.activeWorkspaceId) return
    const env: Environment = {
      id: crypto.randomUUID(),
      workspaceId: appState.activeWorkspaceId,
      name: 'New Environment',
      color: null,
      variables: [],
      secrets: [],
    }
    setAppState('environments', (envs) => [...envs, env])
    setPendingNewIds((ids) => new Set(ids).add(env.id))
    setSelectedId(env.id)
    setSecretValues({})
  }

  function renameEnvironment(id: string, name: string) {
    setAppState('environments', (e) => e.id === id, 'name', name)
  }

  async function deleteEnvironment(id: string) {
    const env = appState.environments.find((e) => e.id === id)
    if (!env) return
    if (!confirm(`Delete environment "${env.name}"? This cannot be undone.`)) return
    setAppState('environments', (envs) => envs.filter((e) => e.id !== id))
    setPendingNewIds((ids) => {
      const next = new Set(ids)
      next.delete(id)
      return next
    })
    if (selectedId() === id) setSelectedId(null)
  }

  function addVariable(envId: string) {
    setAppState('environments', (e) => e.id === envId, 'variables', (vars) => [
      ...vars,
      { key: '', value: '', enabled: true } satisfies KeyValue,
    ])
  }

  function updateVariable(envId: string, index: number, patch: Partial<KeyValue>) {
    setAppState('environments', (e) => e.id === envId, 'variables', index, patch)
  }

  function removeVariable(envId: string, index: number) {
    const env = appState.environments.find((e) => e.id === envId)
    const key = env?.variables[index]?.key
    setAppState('environments', (e) => e.id === envId, 'variables', (vars) => vars.filter((_, i) => i !== index))
    if (key) {
      setAppState('environments', (e) => e.id === envId, 'secrets', (secrets) => secrets.filter((s) => s !== key))
      setSecretValues((sv) => {
        const next = { ...sv }
        delete next[secretKey(envId, key)]
        return next
      })
    }
  }

  function secretKey(envId: string, varKey: string) {
    return `${envId}:${varKey}`
  }

  function isSecret(env: (typeof appState.environments)[number], varKey: string) {
    return env.secrets.includes(varKey)
  }

  function toggleSecret(envId: string, index: number, checked: boolean) {
    const env = appState.environments.find((e) => e.id === envId)
    if (!env) return
    const varKey = env.variables[index]?.key
    if (!varKey) return

    if (checked) {
      const currentValue = env.variables[index]?.value ?? ''
      setSecretValues((sv) => ({ ...sv, [secretKey(envId, varKey)]: currentValue }))
      setAppState('environments', (e) => e.id === envId, 'variables', index, 'value', '')
      setAppState('environments', (e) => e.id === envId, 'secrets', (secrets) =>
        secrets.includes(varKey) ? secrets : [...secrets, varKey],
      )
    } else {
      setAppState('environments', (e) => e.id === envId, 'secrets', (secrets) => secrets.filter((s) => s !== varKey))
      setSecretValues((sv) => {
        const next = { ...sv }
        delete next[secretKey(envId, varKey)]
        return next
      })
    }
  }

  function updateSecretValue(envId: string, varKey: string, value: string) {
    setSecretValues((sv) => ({ ...sv, [secretKey(envId, varKey)]: value }))
  }

  function renameVariableKey(envId: string, index: number, oldKey: string, newKey: string) {
    updateVariable(envId, index, { key: newKey })
    const env = appState.environments.find((e) => e.id === envId)
    if (!env || !env.secrets.includes(oldKey) || oldKey === newKey) return
    setAppState('environments', (e) => e.id === envId, 'secrets', (secrets) =>
      secrets.map((s) => (s === oldKey ? newKey : s)),
    )
    setSecretValues((sv) => {
      const next = { ...sv }
      const val = next[secretKey(envId, oldKey)]
      delete next[secretKey(envId, oldKey)]
      if (val !== undefined) next[secretKey(envId, newKey)] = val
      return next
    })
  }

  async function save(envId: string) {
    const env = appState.environments.find((e) => e.id === envId)
    if (!env) return
    setSaving(true)
    setError(null)
    try {
      const isNew = pendingNewIds().has(envId)
      const payload = new model.Environment({
        id: env.id,
        workspaceId: env.workspaceId,
        name: env.name,
        color: env.color,
        variables: env.variables,
        secrets: env.secrets,
      })
      const secretMap: SecretValues = {}
      for (const key of env.secrets) {
        secretMap[key] = secretValues()[secretKey(envId, key)] ?? ''
      }
      if (isNew) {
        await wails.CreateEnvironment(payload, secretMap)
        setPendingNewIds((ids) => {
          const next = new Set(ids)
          next.delete(envId)
          return next
        })
      } else {
        await wails.UpdateEnvironment(payload, secretMap)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Show when={environmentEditorOpen()}>
      <div
        class="fixed inset-0 z-50 flex items-center justify-center bg-black/50"
        onClick={close}
      >
        <div
          class="flex h-[32rem] w-full max-w-3xl overflow-hidden rounded-lg border border-neutral-700 bg-neutral-900 shadow-2xl"
          onClick={(e) => e.stopPropagation()}
        >
          <div class="flex w-56 flex-col border-r border-neutral-800 bg-neutral-925">
            <div class="flex items-center justify-between border-b border-neutral-800 px-3 py-2">
              <span class="text-xs font-semibold uppercase tracking-wide text-neutral-500">Environments</span>
              <button
                class="rounded px-1.5 py-0.5 text-sm text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200"
                onClick={createEnvironment}
                title="New environment"
              >
                +
              </button>
            </div>
            <div class="flex-1 overflow-y-auto py-1">
              <For
                each={workspaceEnvironments()}
                fallback={<p class="px-3 py-3 text-xs text-neutral-600">No environments yet</p>}
              >
                {(env) => (
                  <button
                    class="flex w-full items-center justify-between px-3 py-1.5 text-left text-sm"
                    classList={{
                      'bg-neutral-800 text-neutral-100': selectedId() === env.id,
                      'text-neutral-400 hover:bg-neutral-800/60 hover:text-neutral-200': selectedId() !== env.id,
                    }}
                    onClick={() => {
                      setSelectedId(env.id)
                      setSecretValues({})
                    }}
                  >
                    <span class="truncate">{env.name}</span>
                    <span class="ml-2 text-xs text-neutral-600">{env.variables.length}</span>
                  </button>
                )}
              </For>
            </div>
          </div>

          <div class="flex flex-1 flex-col">
            <div class="flex items-center justify-between border-b border-neutral-800 px-4 py-2">
              <span class="text-sm font-medium text-neutral-200">Manage environments</span>
              <button
                class="rounded px-2 py-1 text-xs text-neutral-500 hover:bg-neutral-800 hover:text-neutral-200"
                onClick={close}
              >
                Close (Esc)
              </button>
            </div>

            <Show
              when={selected()}
              fallback={
                <div class="flex flex-1 flex-col items-center justify-center gap-2 text-neutral-600">
                  <p class="text-sm">No environment selected</p>
                  <button
                    class="rounded bg-emerald-600 px-3 py-1 text-sm font-medium text-white hover:bg-emerald-500"
                    onClick={createEnvironment}
                  >
                    Create environment
                  </button>
                </div>
              }
            >
              {(env) => (
                <div class="flex flex-1 flex-col overflow-hidden">
                  <div class="flex items-center gap-2 border-b border-neutral-800 px-4 py-2">
                    <input
                      class="flex-1 rounded bg-neutral-900 px-2 py-1 text-sm text-neutral-100 focus:outline-none focus:ring-1 focus:ring-neutral-600"
                      value={env().name}
                      onInput={(e) => renameEnvironment(env().id, e.currentTarget.value)}
                      placeholder="Environment name"
                    />
                    <button
                      class="rounded px-2 py-1 text-xs text-red-400 hover:bg-red-950/40"
                      onClick={() => deleteEnvironment(env().id)}
                    >
                      Delete
                    </button>
                  </div>

                  <div class="flex-1 overflow-y-auto px-4 py-3">
                    <div class="grid grid-cols-[1fr_1fr_auto_auto] items-center gap-x-2 gap-y-1.5 text-xs">
                      <span class="text-neutral-500">Key</span>
                      <span class="text-neutral-500">Value</span>
                      <span class="text-neutral-500">Secret</span>
                      <span />
                      <For each={env().variables}>
                        {(v, i) => (
                          <>
                            <input
                              class="rounded bg-neutral-900 px-2 py-1 font-mono text-xs text-neutral-200 focus:outline-none focus:ring-1 focus:ring-neutral-600"
                              value={v.key}
                              placeholder="VAR_NAME"
                              onInput={(e) => renameVariableKey(env().id, i(), v.key, e.currentTarget.value)}
                            />
                            <Show
                              when={isSecret(env(), v.key)}
                              fallback={
                                <input
                                  class="rounded bg-neutral-900 px-2 py-1 font-mono text-xs text-neutral-200 focus:outline-none focus:ring-1 focus:ring-neutral-600"
                                  value={v.value}
                                  placeholder="value"
                                  onInput={(e) => updateVariable(env().id, i(), { value: e.currentTarget.value })}
                                />
                              }
                            >
                              <input
                                type="password"
                                class="rounded border border-amber-900/40 bg-neutral-900 px-2 py-1 font-mono text-xs text-neutral-200 focus:outline-none focus:ring-1 focus:ring-amber-700"
                                value={secretValues()[secretKey(env().id, v.key)] ?? ''}
                                placeholder="secret value (kept out of the file)"
                                onInput={(e) => updateSecretValue(env().id, v.key, e.currentTarget.value)}
                              />
                            </Show>
                            <label class="flex items-center justify-center" title="Store in OS keychain, not in plain text">
                              <input
                                type="checkbox"
                                class="accent-emerald-600"
                                checked={isSecret(env(), v.key)}
                                onChange={(e) => toggleSecret(env().id, i(), e.currentTarget.checked)}
                              />
                            </label>
                            <button
                              class="rounded px-1.5 py-1 text-xs text-neutral-500 hover:bg-neutral-800 hover:text-red-400"
                              onClick={() => removeVariable(env().id, i())}
                              title="Remove variable"
                            >
                              ✕
                            </button>
                          </>
                        )}
                      </For>
                    </div>

                    <button
                      class="mt-3 rounded px-2 py-1 text-xs text-neutral-400 hover:bg-neutral-800 hover:text-neutral-200"
                      onClick={() => addVariable(env().id)}
                    >
                      + Add variable
                    </button>

                    <Show when={env().secrets.length > 0}>
                      <p class="mt-3 text-xs text-neutral-600">
                        Secret values are never written into the environment file — they're sent to the OS
                        keychain when you save, keyed by variable name.
                      </p>
                    </Show>
                  </div>

                  <div class="flex items-center justify-between border-t border-neutral-800 px-4 py-2">
                    <Show when={error()}>
                      <span class="text-xs text-red-400">{error()}</span>
                    </Show>
                    <div class="ml-auto flex items-center gap-2">
                      <button
                        class="rounded bg-emerald-600 px-3 py-1 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-50"
                        disabled={saving()}
                        onClick={() => save(env().id)}
                      >
                        {saving() ? 'Saving…' : 'Save'}
                      </button>
                    </div>
                  </div>
                </div>
              )}
            </Show>
          </div>
        </div>
      </div>
    </Show>
  )
}
