import { Show, createSignal } from 'solid-js'
import { appState, setAppState } from '../lib/store'
import type { AuthKind } from '../types'

const AUTH_KINDS: { value: AuthKind; label: string }[] = [
  { value: 'none', label: 'No Auth' },
  { value: 'basic', label: 'Basic Auth' },
  { value: 'bearer', label: 'Bearer Token' },
  { value: 'apikey', label: 'API Key' },
  { value: 'jwt', label: 'JWT' },
  { value: 'oauth2', label: 'OAuth 2.0' },
  { value: 'awsSigV4', label: 'AWS Signature v4' },
]

function Field(props: { label: string; children: any }) {
  return (
    <label class="flex flex-col gap-1">
      <span class="text-[10px] font-semibold uppercase tracking-wide text-ink-faint">{props.label}</span>
      {props.children}
    </label>
  )
}

const inputClass =
  'rounded bg-field px-2 py-1 font-mono text-xs text-ink placeholder:text-ink-faint focus:outline-none focus:ring-1 focus:ring-edge-strong'

export default function AuthConfigForm(props: { requestIndex: number }) {
  const req = () => appState.requests[props.requestIndex]
  const auth = () => req()?.authRef ?? { kind: 'none' as AuthKind }
  const tls = () => req()?.tls ?? {}
  const [tlsOpen, setTlsOpen] = createSignal(false)

  function setKind(kind: AuthKind) {
    setAppState('requests', props.requestIndex, 'authRef', (prev) => ({ ...prev, kind }))
  }

  function setTLS(field: 'clientCertPem' | 'clientKeyPem' | 'customCaPem', value: string) {
    setAppState('requests', props.requestIndex, 'tls', (prev) => ({ ...prev, [field]: value }))
  }

  function setInsecureSkipVerify(value: boolean) {
    setAppState('requests', props.requestIndex, 'tls', (prev) => ({ ...prev, insecureSkipVerify: value }))
  }

  function setBasic(field: 'username' | 'password', value: string) {
    setAppState('requests', props.requestIndex, 'authRef', 'basic', (prev) => ({
      username: prev?.username ?? '',
      password: prev?.password ?? '',
      [field]: value,
    }))
  }

  function setBearer(value: string) {
    setAppState('requests', props.requestIndex, 'authRef', 'bearer', { token: value })
  }

  function setApiKey(field: 'key' | 'value' | 'in', value: string) {
    setAppState('requests', props.requestIndex, 'authRef', 'apikey', (prev) => ({
      key: prev?.key ?? '',
      value: prev?.value ?? '',
      in: prev?.in ?? 'header',
      [field]: value,
    }))
  }

  function setJwt(field: 'secret' | 'algorithm' | 'claims', value: string) {
    setAppState('requests', props.requestIndex, 'authRef', 'jwt', (prev) => ({
      secret: prev?.secret ?? '',
      algorithm: prev?.algorithm ?? 'HS256',
      claims: prev?.claims ?? '',
      [field]: value,
    }))
  }

  function setOAuth2(field: 'clientId' | 'clientSecret' | 'tokenUrl', value: string) {
    setAppState('requests', props.requestIndex, 'authRef', 'oauth2', (prev) => ({
      clientId: prev?.clientId ?? '',
      clientSecret: prev?.clientSecret ?? '',
      tokenUrl: prev?.tokenUrl ?? '',
      scopes: prev?.scopes ?? [],
      [field]: value,
    }))
  }

  function setAWSSigV4(field: 'accessKeyId' | 'secretAccessKey' | 'region' | 'service' | 'sessionToken', value: string) {
    setAppState('requests', props.requestIndex, 'authRef', 'awsSigV4', (prev) => ({
      accessKeyId: prev?.accessKeyId ?? '',
      secretAccessKey: prev?.secretAccessKey ?? '',
      region: prev?.region ?? '',
      service: prev?.service ?? '',
      sessionToken: prev?.sessionToken ?? '',
      [field]: value,
    }))
  }

  return (
    <div class="flex h-full flex-col overflow-y-auto p-3">
      <Field label="Auth type">
        <select
          class={`${inputClass} w-56`}
          value={auth().kind}
          onChange={(e) => setKind(e.currentTarget.value as AuthKind)}
        >
          {AUTH_KINDS.map((k) => (
            <option value={k.value}>{k.label}</option>
          ))}
        </select>
      </Field>

      <Show when={auth().kind === 'none'}>
        <p class="mt-3 text-xs text-ink-faint">This request does not send any authorization.</p>
      </Show>

      <Show when={auth().kind === 'basic'}>
        <div class="mt-3 flex max-w-sm flex-col gap-2">
          <Field label="Username">
            <input
              class={inputClass}
              value={auth().basic?.username ?? ''}
              onInput={(e) => setBasic('username', e.currentTarget.value)}
            />
          </Field>
          <Field label="Password">
            <input
              type="password"
              class={inputClass}
              value={auth().basic?.password ?? ''}
              onInput={(e) => setBasic('password', e.currentTarget.value)}
            />
          </Field>
        </div>
      </Show>

      <Show when={auth().kind === 'bearer'}>
        <div class="mt-3 flex max-w-sm flex-col gap-2">
          <Field label="Token">
            <input
              class={inputClass}
              placeholder="${token}"
              value={auth().bearer?.token ?? ''}
              onInput={(e) => setBearer(e.currentTarget.value)}
            />
          </Field>
        </div>
      </Show>

      <Show when={auth().kind === 'apikey'}>
        <div class="mt-3 flex max-w-sm flex-col gap-2">
          <Field label="Key">
            <input
              class={inputClass}
              value={auth().apikey?.key ?? ''}
              onInput={(e) => setApiKey('key', e.currentTarget.value)}
            />
          </Field>
          <Field label="Value">
            <input
              class={inputClass}
              value={auth().apikey?.value ?? ''}
              onInput={(e) => setApiKey('value', e.currentTarget.value)}
            />
          </Field>
          <Field label="Add to">
            <select
              class={inputClass}
              value={auth().apikey?.in ?? 'header'}
              onChange={(e) => setApiKey('in', e.currentTarget.value)}
            >
              <option value="header">Header</option>
              <option value="query">Query Param</option>
            </select>
          </Field>
        </div>
      </Show>

      <Show when={auth().kind === 'jwt'}>
        <div class="mt-3 flex max-w-sm flex-col gap-2">
          <Field label="Secret">
            <input
              class={inputClass}
              value={auth().jwt?.secret ?? ''}
              onInput={(e) => setJwt('secret', e.currentTarget.value)}
            />
          </Field>
          <Field label="Algorithm">
            <input
              class={inputClass}
              placeholder="HS256"
              value={auth().jwt?.algorithm ?? ''}
              onInput={(e) => setJwt('algorithm', e.currentTarget.value)}
            />
          </Field>
          <Field label="Claims (JSON)">
            <textarea
              class={`${inputClass} h-24 resize-y`}
              placeholder='{"sub": "user-id"}'
              value={auth().jwt?.claims ?? ''}
              onInput={(e) => setJwt('claims', e.currentTarget.value)}
            />
          </Field>
        </div>
      </Show>

      <Show when={auth().kind === 'oauth2'}>
        <div class="mt-3 flex max-w-sm flex-col gap-2">
          <Field label="Client ID">
            <input
              class={inputClass}
              value={auth().oauth2?.clientId ?? ''}
              onInput={(e) => setOAuth2('clientId', e.currentTarget.value)}
            />
          </Field>
          <Field label="Client Secret">
            <input
              type="password"
              class={inputClass}
              value={auth().oauth2?.clientSecret ?? ''}
              onInput={(e) => setOAuth2('clientSecret', e.currentTarget.value)}
            />
          </Field>
          <Field label="Token URL">
            <input
              class={inputClass}
              placeholder="https://auth.example.com/oauth/token"
              value={auth().oauth2?.tokenUrl ?? ''}
              onInput={(e) => setOAuth2('tokenUrl', e.currentTarget.value)}
            />
          </Field>
          <p class="text-[11px] text-ink-faint">Client-credentials grant only. Scopes editing coming later.</p>
        </div>
      </Show>

      <Show when={auth().kind === 'awsSigV4'}>
        <div class="mt-3 flex max-w-sm flex-col gap-2">
          <Field label="Access Key ID">
            <input
              class={inputClass}
              placeholder="AKIA…"
              value={auth().awsSigV4?.accessKeyId ?? ''}
              onInput={(e) => setAWSSigV4('accessKeyId', e.currentTarget.value)}
            />
          </Field>
          <Field label="Secret Access Key">
            <input
              type="password"
              class={inputClass}
              value={auth().awsSigV4?.secretAccessKey ?? ''}
              onInput={(e) => setAWSSigV4('secretAccessKey', e.currentTarget.value)}
            />
          </Field>
          <div class="flex gap-2">
            <Field label="Region">
              <input
                class={inputClass}
                placeholder="us-east-1"
                value={auth().awsSigV4?.region ?? ''}
                onInput={(e) => setAWSSigV4('region', e.currentTarget.value)}
              />
            </Field>
            <Field label="Service">
              <input
                class={inputClass}
                placeholder="execute-api, s3, es…"
                value={auth().awsSigV4?.service ?? ''}
                onInput={(e) => setAWSSigV4('service', e.currentTarget.value)}
              />
            </Field>
          </div>
          <Field label="Session token (optional)">
            <input
              class={inputClass}
              placeholder="Only for temporary/STS credentials"
              value={auth().awsSigV4?.sessionToken ?? ''}
              onInput={(e) => setAWSSigV4('sessionToken', e.currentTarget.value)}
            />
          </Field>
          <p class="text-[11px] text-ink-faint">Signs the request per AWS Signature Version 4 (Authorization + X-Amz-Date headers).</p>
        </div>
      </Show>

      {/* Transport-level, not an Authorization scheme — a request can need a
          client certificate independent of whatever auth type is selected
          above (or none), so this section is always available regardless of
          Auth type. Collapsed by default since most requests never need it. */}
      <div class="mt-6 max-w-sm border-t border-edge pt-3">
        <button
          class="flex w-full items-center gap-1.5 text-left text-[10px] font-semibold uppercase tracking-wide text-ink-faint hover:text-ink-dim"
          onClick={() => setTlsOpen((v) => !v)}
        >
          <span class="w-3 shrink-0">{tlsOpen() ? '▾' : '▸'}</span>
          Client certificate (mTLS)
        </button>
        <Show when={tlsOpen()}>
          <div class="mt-3 flex flex-col gap-2">
            <Field label="Client certificate (PEM)">
              <textarea
                class={`${inputClass} h-20 resize-y`}
                placeholder="-----BEGIN CERTIFICATE-----"
                value={tls().clientCertPem ?? ''}
                onInput={(e) => setTLS('clientCertPem', e.currentTarget.value)}
              />
            </Field>
            <Field label="Client private key (PEM)">
              <textarea
                class={`${inputClass} h-20 resize-y`}
                placeholder="-----BEGIN PRIVATE KEY-----"
                value={tls().clientKeyPem ?? ''}
                onInput={(e) => setTLS('clientKeyPem', e.currentTarget.value)}
              />
            </Field>
            <Field label="Custom CA certificate (PEM, optional)">
              <textarea
                class={`${inputClass} h-16 resize-y`}
                placeholder="For self-signed or internal servers"
                value={tls().customCaPem ?? ''}
                onInput={(e) => setTLS('customCaPem', e.currentTarget.value)}
              />
            </Field>
            <label class="flex items-center gap-2 rounded border border-danger-edge bg-danger-bg/40 px-2 py-1.5">
              <input
                type="checkbox"
                checked={tls().insecureSkipVerify ?? false}
                onChange={(e) => setInsecureSkipVerify(e.currentTarget.checked)}
              />
              <span class="text-xs text-danger">Disable TLS certificate verification (insecure — testing only)</span>
            </label>
          </div>
        </Show>
      </div>
    </div>
  )
}
