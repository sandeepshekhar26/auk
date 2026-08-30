import { Show, Switch, Match, createSignal, onMount } from 'solid-js'
import { BrowserOpenURL } from '../../wailsjs/runtime/runtime'
import { licenseStatus, activate, deactivate, refreshLicense } from '../lib/license'

// TODO(licensing): replace with the real Merchant-of-Record checkout URL
// (Lemon Squeezy / Paddle) once chosen. This is a clearly-marked placeholder —
// it does not resolve to a real store.
const CHECKOUT_URL = 'https://checkout.auk.app/buy-TODO'

// LicenseSection renders the licensing UI inside Settings. It shows the current
// status (trial days left / licensed to whom + seat count / trial ended /
// invalid), an activation field with inline feedback, a deactivate control when
// licensed, and a "Buy AUK" link. It reads and drives the shared license signal
// in lib/license.ts, so it stays in sync with the TrialBadge and any gate.
export default function LicenseSection() {
  const [key, setKey] = createSignal('')
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal('')
  const [notice, setNotice] = createSignal('')

  // Freshen status when the panel opens (App.tsx already loads it on mount;
  // this keeps it current if a purchase happened in another window).
  onMount(() => void refreshLicense())

  const st = licenseStatus
  const notLicensed = () => st()?.state !== 'licensed'
  const days = () => st()?.daysRemaining ?? 0
  const dayWord = () => (days() === 1 ? 'day' : 'days')
  const machines = () => `${st()?.machineCount ?? 1}/${st()?.maxMachines ?? 3} machines`
  const thisMachine = () => {
    const id = st()?.machineId ?? ''
    return id.length > 22 ? id.slice(0, 22) + '…' : id
  }

  async function onActivate(e?: Event) {
    e?.preventDefault()
    if (busy()) return
    const value = key().trim()
    if (!value) {
      setError('Enter your license key, or paste your license file.')
      return
    }
    setError('')
    setNotice('')
    setBusy(true)
    try {
      const res = await activate(value)
      if (res.ok) {
        setNotice('Activated — thank you for buying AUK!')
        setKey('')
      } else {
        setError(res.error || 'Activation failed.')
      }
    } finally {
      setBusy(false)
    }
  }

  async function onDeactivate() {
    if (busy()) return
    setError('')
    setNotice('')
    setBusy(true)
    try {
      const res = await deactivate()
      if (!res.ok) setError(res.error || 'Could not deactivate.')
      else setNotice('Deactivated on this machine.')
    } finally {
      setBusy(false)
    }
  }

  function openCheckout() {
    try {
      BrowserOpenURL(CHECKOUT_URL)
    } catch {
      window.open(CHECKOUT_URL, '_blank')
    }
  }

  return (
    <section>
      <h3 class="text-xs font-medium uppercase tracking-wide text-ink-muted">License</h3>

      <div class="mt-2">
        <Switch fallback={<p class="text-sm text-ink-dim">Checking license…</p>}>
          {/* Trial */}
          <Match when={st()?.state === 'trial'}>
            <div class="flex items-center gap-2">
              <span class="inline-flex h-2 w-2 rounded-full bg-accent" />
              <p class="text-sm text-ink">
                <span class="font-medium">Free trial</span>
                <span class="text-ink-dim">
                  {' '}
                  — {days()} {dayWord()} left
                </span>
              </p>
            </div>
            <p class="mt-1 text-xs text-ink-muted">
              Activate a license to keep AUK once the trial ends. No account needed.
            </p>
          </Match>

          {/* Licensed */}
          <Match when={st()?.state === 'licensed'}>
            <div class="flex items-center gap-2">
              <span class="inline-flex h-2 w-2 rounded-full bg-accent" />
              <p class="text-sm text-ink">
                <span class="font-medium">Licensed</span>
                <Show when={st()?.email}>
                  <span class="text-ink-dim"> to {st()!.email}</span>
                </Show>
              </p>
            </div>
            <div class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-ink-muted">
              <Show when={st()?.plan}>
                <span class="capitalize">{st()!.plan}</span>
              </Show>
              <span>{machines()}</span>
            </div>
            <Show when={thisMachine()}>
              <p class="mt-1 font-mono text-[11px] text-ink-faint">This machine: {thisMachine()}</p>
            </Show>

            <Show when={st()?.updatesExpired}>
              <p class="mt-2 rounded border border-warn-edge bg-warn/10 px-2 py-1.5 text-xs text-warn">
                {st()?.message || 'Your 12 months of updates have ended. AUK keeps working.'}
              </p>
            </Show>

            <button
              type="button"
              disabled={busy()}
              onClick={onDeactivate}
              class="mt-3 rounded bg-raised px-2.5 py-1 text-xs font-medium text-ink-dim hover:bg-elevated disabled:opacity-50"
            >
              {busy() ? '…' : 'Deactivate on this machine'}
            </button>
          </Match>

          {/* Trial expired */}
          <Match when={st()?.state === 'trial_expired'}>
            <p class="rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-sm text-danger">
              Your 14-day trial has ended. Activate a license to keep using AUK.
            </p>
          </Match>

          {/* Invalid */}
          <Match when={st()?.state === 'license_invalid'}>
            <p class="rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-sm text-danger">
              {st()?.message || 'This license could not be verified on this machine.'}
            </p>
          </Match>
        </Switch>
      </div>

      {/* Activation + purchase — shown whenever not licensed. */}
      <Show when={notLicensed()}>
        <form class="mt-3 flex flex-col gap-2" onSubmit={onActivate}>
          <input
            type="text"
            value={key()}
            onInput={(e) => setKey(e.currentTarget.value)}
            placeholder="Paste license key or license file"
            spellcheck={false}
            autocomplete="off"
            autocapitalize="off"
            class="w-full rounded border border-edge bg-field px-2 py-1.5 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-accent"
          />
          <div class="flex items-center gap-2">
            <button
              type="submit"
              disabled={busy()}
              class="rounded bg-accent px-3 py-1.5 text-xs font-medium text-accent-contrast hover:bg-accent-hover disabled:opacity-50"
            >
              {busy() ? 'Activating…' : 'Activate'}
            </button>
            <button
              type="button"
              onClick={openCheckout}
              class="rounded px-2 py-1.5 text-xs font-medium text-accent-fg hover:underline"
            >
              Buy AUK — $49 →
            </button>
          </div>
        </form>
      </Show>

      <Show when={error()}>
        <p class="mt-2 rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5 text-xs text-danger">{error()}</p>
      </Show>
      <Show when={notice()}>
        <p class="mt-2 rounded border border-accent/40 bg-accent/10 px-2 py-1.5 text-xs text-accent-fg">{notice()}</p>
      </Show>
    </section>
  )
}
