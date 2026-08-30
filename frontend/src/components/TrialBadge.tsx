import { Show } from 'solid-js'
import { licenseStatus } from '../lib/license'

// TrialBadge is a small status pill for the top strip. It reads the shared
// license signal and renders itself:
//
//   trial (plenty of time)  → "Trial · 9 days"     (neutral)
//   trial (3 days or fewer)  → "Trial · 2 days"     (amber, urgency)
//   trial_expired            → "Trial expired"      (danger)
//   license_invalid          → "License problem"    (danger)
//   licensed / not loaded    → nothing
//
// Self-contained and reactive. Pass onClick to make it open Settings (or wherever
// the license UI lives); without it the pill is non-interactive.
export default function TrialBadge(props: { onClick?: () => void }) {
  const descriptor = (): { label: string; tone: 'neutral' | 'warn' | 'danger' } | null => {
    const st = licenseStatus()
    if (!st) return null
    switch (st.state) {
      case 'trial': {
        const n = st.daysRemaining
        const label = `Trial · ${n} ${n === 1 ? 'day' : 'days'}`
        return { label, tone: n <= 3 ? 'warn' : 'neutral' }
      }
      case 'trial_expired':
        return { label: 'Trial expired', tone: 'danger' }
      case 'license_invalid':
        return { label: 'License problem', tone: 'danger' }
      case 'licensed':
      default:
        return null
    }
  }

  const interactive = () => typeof props.onClick === 'function'

  return (
    <Show when={descriptor()}>
      {(d) => (
        <span
          role={interactive() ? 'button' : undefined}
          tabindex={interactive() ? 0 : undefined}
          aria-label={interactive() ? `${d().label} — open license settings` : d().label}
          onClick={() => props.onClick?.()}
          onKeyDown={(e) => {
            if (interactive() && (e.key === 'Enter' || e.key === ' ')) {
              e.preventDefault()
              props.onClick?.()
            }
          }}
          class="inline-flex select-none items-center gap-1.5 rounded-full border px-2 py-0.5 text-[11px] font-medium"
          classList={{
            'border-edge bg-field text-ink-dim': d().tone === 'neutral',
            'border-warn-edge bg-warn/10 text-warn': d().tone === 'warn',
            'border-danger-edge bg-danger-bg/50 text-danger': d().tone === 'danger',
            'cursor-pointer transition hover:brightness-110 focus-visible:outline focus-visible:outline-2 focus-visible:outline-accent':
              interactive(),
          }}
        >
          <span class="h-1.5 w-1.5 rounded-full bg-current opacity-70" />
          {d().label}
        </span>
      )}
    </Show>
  )
}
