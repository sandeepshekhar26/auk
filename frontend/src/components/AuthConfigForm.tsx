import { Show, createEffect, createSignal, on } from 'solid-js'
import { appState, setAppState } from '../lib/store'
import { wails } from '../lib/wails'
import type { AuthKind } from '../types'

const AUTH_KINDS: { value: AuthKind; label: string }[] = [
  { value: 'none', label: 'No Auth' },
  { value: 'basic', label: 'Basic Auth' },
  { value: 'digest', label: 'Digest Auth' },
  { value: 'bearer', label: 'Bearer Token' },
  { value: 'apikey', label: 'API Key' },
  { value: 'jwt', label: 'JWT' },
  { value: 'oauth2', label: 'OAuth 2.0' },
  { value: 'awsSigV4', label: 'AWS Signature v4' },
  { value: 'oauth1', label: 'OAuth 1.0' },
]

/**
 * The sign-in row for the authorization-code grant: one button that runs the
 * whole browser flow, and an honest status line beside it.
 *
 * Status is looked up from the backend (keychain-backed) rather than kept in
 * frontend state, so it survives restarts and stays correct when two requests
 * share the same IdP config. Re-queried whenever the request or the active
 * environment changes — the config is resolved through templates, so a
 * different environment can mean a different identity provider entirely.
 */
function OAuth2SignInRow(props: { requestId: string }) {
  type SignInStatus = { signedIn: boolean; expiresAt?: string; hasRefresh: boolean; expired?: boolean }
  const [status, setStatus] = createSignal<SignInStatus | null>(null)
  const [busy, setBusy] = createSignal(false)
  const [error, setError] = createSignal<string | null>(null)
  const envId = () => appState.activeEnvironmentId ?? ''

  async function refresh() {
    try {
      setStatus((await wails.OAuth2Status(props.requestId, envId())) as SignInStatus)
    } catch {
      // Incomplete config (no auth URL yet, unresolved template) — nothing to
      // show; the button itself will surface the real error on click.
      setStatus(null)
    }
  }

  createEffect(on([() => props.requestId, envId], () => void refresh()))

  async function signIn() {
    setBusy(true)
    setError(null)
    try {
      await wails.OAuth2SignIn(props.requestId, envId())
      await refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  async function signOut() {
    try {
      setStatus((await wails.OAuth2SignOut(props.requestId, envId())) as SignInStatus)
    } catch {
      /* status re-query below is the fallback */
      void refresh()
    }
  }

  function expiryLabel(): string {
    const at = status()?.expiresAt
    if (!at) return ''
    const mins = Math.round((new Date(at).getTime() - Date.now()) / 60000)
    if (mins <= 0) return status()?.hasRefresh ? 'renews on next send' : 'expired'
    if (mins < 90) return `expires in ${mins}m`
    return `expires in ${Math.round(mins / 60)}h`
  }

  return (
    <div class="mt-1 flex flex-col gap-1.5">
      <div class="flex items-center gap-2">
        <button
          type="button"
          class="rounded-lg bg-accent px-3 py-1.5 text-xs font-semibold text-accent-contrast hover:bg-accent-hover disabled:opacity-60"
          disabled={busy()}
          onClick={() => void signIn()}
        >
          {busy() ? 'Waiting for browser…' : status()?.signedIn ? 'Sign in again' : 'Sign in with browser'}
        </button>
        <Show when={status()?.signedIn}>
          <span class="flex items-center gap-1.5 text-[11px] text-ink-muted">
            <span class="h-1.5 w-1.5 rounded-full bg-accent" />
            Signed in{expiryLabel() ? ` · ${expiryLabel()}` : ''}
          </span>
          <button type="button" class="text-[11px] text-ink-muted hover:text-ink-dim hover:underline" onClick={() => void signOut()}>
            Sign out
          </button>
        </Show>
        <Show when={status()?.expired}>
          <span class="text-[11px] text-warn">Session expired — sign in again</span>
        </Show>
      </div>
      <Show when={error()}>
        <p class="max-w-sm break-words text-[11px] text-danger">{error()}</p>
      </Show>
      <p class="text-[11px] leading-relaxed text-ink-faint">
        Opens your default browser — AUK never sees the password. The token is stored in the macOS
        keychain and refreshed automatically when the provider allows it.
      </p>
    </div>
  )
}

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
  const [proxyOpen, setProxyOpen] = createSignal(false)

  function setKind(kind: AuthKind) {
    setAppState('requests', props.requestIndex, 'authRef', (prev) => ({ ...prev, kind }))
  }

  function setTLS(field: 'clientCertPem' | 'clientKeyPem' | 'customCaPem', value: string) {
    setAppState('requests', props.requestIndex, 'tls', (prev) => ({ ...prev, [field]: value }))
  }

  function setProxyUrl(value: string) {
    setAppState('requests', props.requestIndex, 'proxyUrl', value)
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

  function setDigest(field: 'username' | 'password', value: string) {
    setAppState('requests', props.requestIndex, 'authRef', 'digest', (prev) => ({
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

  function setOAuth2(field: 'clientId' | 'clientSecret' | 'tokenUrl' | 'authUrl' | 'grantType' | 'audience', value: string) {
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

  function setOAuth1(field: 'consumerKey' | 'consumerSecret' | 'token' | 'tokenSecret', value: string) {
    setAppState('requests', props.requestIndex, 'authRef', 'oauth1', (prev) => ({
      consumerKey: prev?.consumerKey ?? '',
      consumerSecret: prev?.consumerSecret ?? '',
      token: prev?.token ?? '',
      tokenSecret: prev?.tokenSecret ?? '',
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

      <Show when={auth().kind === 'digest'}>
        <div class="mt-3 flex max-w-sm flex-col gap-2">
          <Field label="Username">
            <input
              class={inputClass}
              value={auth().digest?.username ?? ''}
              onInput={(e) => setDigest('username', e.currentTarget.value)}
            />
          </Field>
          <Field label="Password">
            <input
              type="password"
              class={inputClass}
              value={auth().digest?.password ?? ''}
              onInput={(e) => setDigest('password', e.currentTarget.value)}
            />
          </Field>
          <p class="text-[11px] text-ink-faint">
            RFC 7616 challenge-response: the request is sent once to collect the server's 401 challenge, then re-sent
            authorized. MD5, SHA-256 and SHA-512-256 (plus -sess variants) with qop=auth; auth-int isn't supported.
          </p>
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
          <Field label="Grant type">
            <select
              class={inputClass}
              value={auth().oauth2?.grantType || 'client_credentials'}
              onChange={(e) => setOAuth2('grantType', e.currentTarget.value)}
            >
              <option value="client_credentials">Client credentials (machine to machine)</option>
              <option value="authorization_code">Authorization code + PKCE (sign in as a user)</option>
            </select>
          </Field>

          <Show when={(auth().oauth2?.grantType || 'client_credentials') === 'authorization_code'}>
            <Field label="Auth URL">
              <input
                class={inputClass}
                placeholder="https://tenant.auth0.com/authorize"
                value={auth().oauth2?.authUrl ?? ''}
                onInput={(e) => setOAuth2('authUrl', e.currentTarget.value)}
              />
            </Field>
          </Show>
          <Field label="Token URL">
            <input
              class={inputClass}
              placeholder="https://auth.example.com/oauth/token"
              value={auth().oauth2?.tokenUrl ?? ''}
              onInput={(e) => setOAuth2('tokenUrl', e.currentTarget.value)}
            />
          </Field>
          <Field label="Client ID">
            <input
              class={inputClass}
              value={auth().oauth2?.clientId ?? ''}
              onInput={(e) => setOAuth2('clientId', e.currentTarget.value)}
            />
          </Field>
          <Field
            label={
              (auth().oauth2?.grantType || 'client_credentials') === 'authorization_code'
                ? 'Client Secret (optional for public clients)'
                : 'Client Secret'
            }
          >
            <input
              type="password"
              class={inputClass}
              value={auth().oauth2?.clientSecret ?? ''}
              onInput={(e) => setOAuth2('clientSecret', e.currentTarget.value)}
            />
          </Field>
          <Field label="Scopes (space-separated)">
            <input
              class={inputClass}
              placeholder="openid profile offline_access"
              value={(auth().oauth2?.scopes ?? []).join(' ')}
              onInput={(e) => {
                const scopes = e.currentTarget.value.split(/\s+/).filter(Boolean)
                setAppState('requests', props.requestIndex, 'authRef', 'oauth2', (prev) => ({
                  ...(prev ?? { clientId: '', clientSecret: '', tokenUrl: '' }),
                  scopes,
                }))
              }}
            />
          </Field>
          <Show when={(auth().oauth2?.grantType || 'client_credentials') === 'authorization_code'}>
            <Field label="Audience (optional; Auth0 APIs need it)">
              <input
                class={inputClass}
                placeholder="https://api.example.com"
                value={auth().oauth2?.audience ?? ''}
                onInput={(e) => setOAuth2('audience', e.currentTarget.value)}
              />
            </Field>

            <OAuth2SignInRow requestId={req().id} />
          </Show>
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

      <Show when={auth().kind === 'oauth1'}>
        <div class="mt-3 flex max-w-sm flex-col gap-2">
          <Field label="Consumer Key">
            <input
              class={inputClass}
              value={auth().oauth1?.consumerKey ?? ''}
              onInput={(e) => setOAuth1('consumerKey', e.currentTarget.value)}
            />
          </Field>
          <Field label="Consumer Secret">
            <input
              type="password"
              class={inputClass}
              value={auth().oauth1?.consumerSecret ?? ''}
              onInput={(e) => setOAuth1('consumerSecret', e.currentTarget.value)}
            />
          </Field>
          <Field label="Access Token (optional)">
            <input
              class={inputClass}
              placeholder="Leave blank for a two-legged / consumer-only request"
              value={auth().oauth1?.token ?? ''}
              onInput={(e) => setOAuth1('token', e.currentTarget.value)}
            />
          </Field>
          <Field label="Token Secret (optional)">
            <input
              type="password"
              class={inputClass}
              value={auth().oauth1?.tokenSecret ?? ''}
              onInput={(e) => setOAuth1('tokenSecret', e.currentTarget.value)}
            />
          </Field>
          <p class="text-[11px] text-ink-faint">HMAC-SHA1 signing per RFC 5849. PLAINTEXT and RSA-SHA1 aren't supported.</p>
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

      {/* Also transport-level and orthogonal to Auth type, same reasoning as
          mTLS above — a manual proxy is independent of whatever auth (or
          none) is selected. System/environment proxy settings apply by
          default regardless; this is only for an explicit per-request
          override. */}
      <div class="mt-3 max-w-sm border-t border-edge pt-3">
        <button
          class="flex w-full items-center gap-1.5 text-left text-[10px] font-semibold uppercase tracking-wide text-ink-faint hover:text-ink-dim"
          onClick={() => setProxyOpen((v) => !v)}
        >
          <span class="w-3 shrink-0">{proxyOpen() ? '▾' : '▸'}</span>
          Proxy
        </button>
        <Show when={proxyOpen()}>
          <div class="mt-3 flex flex-col gap-2">
            <Field label="Proxy URL">
              <input
                class={inputClass}
                placeholder="http://user:pass@proxyhost:8080"
                value={req()?.proxyUrl ?? ''}
                onInput={(e) => setProxyUrl(e.currentTarget.value)}
              />
            </Field>
            <p class="text-[11px] text-ink-faint">Routes this request through a manual HTTP/HTTPS proxy. Leave blank to use the system/environment proxy (if any).</p>
          </div>
        </Show>
      </div>
    </div>
  )
}
