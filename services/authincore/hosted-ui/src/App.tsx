import { FormEvent, useEffect, useMemo, useState } from 'react'
import { acceptInvitation, attributes, Flow, flowMessages, flowNodes, getInvitation, group, startFlow, submitFlow } from './api'

const routeKinds: Record<string, { kind: string; title: string; subtitle: string }> = {
  '/login': { kind: 'login', title: 'Welcome back', subtitle: 'Sign in to continue' },
  '/registration': { kind: 'registration', title: 'Create your account', subtitle: 'Start with a secure identity' },
  '/register': { kind: 'registration', title: 'Create your account', subtitle: 'Start with a secure identity' },
  '/recovery': { kind: 'recovery', title: 'Recover your account', subtitle: 'We will help you regain access' },
  '/verification': { kind: 'verification', title: 'Verify your email', subtitle: 'Confirm that this address belongs to you' },
  '/settings': { kind: 'settings', title: 'Security settings', subtitle: 'Manage your sign-in methods' },
}

function setNested(target: Record<string, unknown>, path: string, value: unknown) {
  const parts = path.split('.')
  let cursor = target
  for (const part of parts.slice(0, -1)) {
    const child = cursor[part]
    cursor = typeof child === 'object' && child !== null ? child as Record<string, unknown> : (cursor[part] = {}) as Record<string, unknown>
  }
  cursor[parts.at(-1)!] = value
}

function FlowPage({ config }: { config: { kind: string; title: string; subtitle: string } }) {
  const [flow, setFlow] = useState<Flow | null>(null)
  const [method, setMethod] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    setError('')
    startFlow(config.kind).then(setFlow).catch((e: Error) => setError(e.message))
  }, [config.kind])

  const groups = useMemo(() => {
    if (!flow) return []
    return [...new Set(flowNodes(flow).map(group).filter(Boolean))]
  }, [flow])

  useEffect(() => {
    if (!method && groups.length) setMethod(groups[0])
  }, [groups, method])

  const visibleNodes = flow ? flowNodes(flow).filter(node => group(node) === method && attributes(node).type !== 'submit') : []

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!flow || !method) return
    const body: Record<string, unknown> = {}
    for (const [name, value] of new FormData(event.currentTarget).entries()) setNested(body, name, value)
    if (config.kind === 'registration' && !('schema_id' in body)) body.schema_id = 'default'
    setBusy(true)
    setError('')
    try {
      const result = await submitFlow(config.kind, flow, method, body)
      if ('ID' in result || 'id' in result) {
        setFlow(result as Flow)
      } else {
        const returnTo = new URLSearchParams(location.search).get('return_to')
        location.assign(returnTo && returnTo.startsWith('/') && !returnTo.startsWith('//') ? returnTo : '/')
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Request failed')
    } finally {
      setBusy(false)
    }
  }

  return <Card title={config.title} subtitle={config.subtitle}>
    {groups.length > 1 && <div className="method-tabs" role="tablist" aria-label="Sign-in method">
      {groups.map(item => <button type="button" role="tab" aria-selected={item === method} className={item === method ? 'active' : ''} onClick={() => setMethod(item)} key={item}>{item.replaceAll('_', ' ')}</button>)}
    </div>}
    {flowMessages(flow ?? {}).map((message, index) => <p className={`notice ${message.type ?? message.Type ?? 'info'}`} key={index}>{message.text ?? message.Text}</p>)}
    {error && <p className="notice error" role="alert">{error}</p>}
    {!flow && !error ? <p className="muted">Loading…</p> : <form onSubmit={submit}>
      {visibleNodes.map((node, index) => {
        const attrs = attributes(node)
        const name = String(attrs.name ?? '')
        return <label key={`${name}-${index}`}>
          <span>{String(attrs.label ?? name.split('.').at(-1) ?? '').replaceAll('_', ' ')}</span>
          <input name={name} type={String(attrs.type ?? 'text')} required={Boolean(attrs.required)} maxLength={attrs.maxlength ? Number(attrs.maxlength) : undefined} pattern={attrs.pattern ? String(attrs.pattern) : undefined} placeholder={attrs.placeholder ? String(attrs.placeholder) : undefined} autoComplete={String(attrs.type) === 'password' ? 'current-password' : undefined} />
        </label>
      })}
      {flow && <button className="primary" disabled={busy || !method}>{busy ? 'Please wait…' : config.kind === 'registration' ? 'Create account' : 'Continue'}</button>}
    </form>}
    {config.kind === 'login' && <nav><a href="/registration">Create account</a><a href="/recovery">Forgot password?</a></nav>}
  </Card>
}

function InvitationPage({ token }: { token: string }) {
  const [invite, setInvite] = useState<{ email: string; role: string } | null>(null)
  const [error, setError] = useState('')
  const [done, setDone] = useState(false)
  useEffect(() => { getInvitation(token).then(setInvite).catch((e: Error) => setError(e.message)) }, [token])
  async function accept() {
    if (!invite) return
    try { await acceptInvitation(token, invite.email); setDone(true) } catch (e) { setError(e instanceof Error ? e.message : 'Could not accept invitation') }
  }
  return <Card title="Organization invitation" subtitle={invite ? `You were invited as ${invite.role}` : 'Loading invitation…'}>
    {error && <p className="notice error" role="alert">{error}</p>}
    {done ? <><p className="notice success">Invitation accepted.</p><a className="button-link" href="/login">Sign in</a></> : invite && <><p className="muted">Confirm invitation for <strong>{invite.email}</strong>.</p><button className="primary" onClick={accept}>Accept invitation</button></>}
  </Card>
}

type ConsentRequestView = {
  id: string
  client_id: string
  client_name: string
  scopes: string[]
}

const SCOPE_LABELS: Record<string, string> = {
  openid: 'Confirm your identity',
  email: 'View your email address',
  profile: 'View your basic profile',
  'crm:*': 'Read and manage your CRM data (accounts, contacts, leads, opportunities, activities)',
}

function describeScope(scope: string): string {
  return SCOPE_LABELS[scope] ?? scope
}

// Renders GET /oauth2/consent's JSON payload as a real Allow/Deny screen and
// submits the decision as a plain HTML form POST (not fetch): granting
// consent ends in a same-origin redirect back to /oauth2/authorize and then
// a cross-origin redirect to the client's own redirect_uri, and only an
// actual browser navigation follows both hops correctly.
function ConsentPage() {
  const consentId = useMemo(() => new URLSearchParams(location.search).get('consent_id') ?? '', [])
  const [req, setReq] = useState<ConsentRequestView | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!consentId) {
      setError('This link is missing its consent request. Start signing in again.')
      return
    }
    fetch(`/oauth2/consent?consent_id=${encodeURIComponent(consentId)}`, {
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
      .then(async (r) => {
        if (!r.ok) throw new Error(await r.text() || `Request failed (${r.status})`)
        return r.json() as Promise<ConsentRequestView>
      })
      .then(setReq)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : 'Could not load this request'))
  }, [consentId])

  if (error) {
    return <Card title="Something went wrong" subtitle={error}>
      <a className="button-link" href="/login">Back to sign in</a>
    </Card>
  }
  if (!req) {
    return <Card title="Authorize access" subtitle="Loading…" />
  }

  return <Card title="Authorize access" subtitle={`${req.client_name || req.client_id} is requesting access to your account`}>
    <ul className="scope-list">
      {req.scopes.map(s => <li key={s}>{describeScope(s)}</li>)}
    </ul>
    <form method="POST" action="/oauth2/consent">
      <input type="hidden" name="consent_id" value={consentId} />
      <input type="hidden" name="grant" value="true" />
      <input type="hidden" name="remember" value="true" />
      {req.scopes.map(s => <input key={s} type="hidden" name="scopes" value={s} />)}
      <button className="primary" type="submit">Allow</button>
    </form>
    <form method="POST" action="/oauth2/consent">
      <input type="hidden" name="consent_id" value={consentId} />
      <input type="hidden" name="grant" value="false" />
      <button className="button-link deny" type="submit">Deny</button>
    </form>
  </Card>
}

function Card({ title, subtitle, children }: { title: string; subtitle: string; children?: React.ReactNode }) {
  return <main><section className="card"><div className="brand" aria-hidden="true">A</div><h1>{title}</h1><p className="subtitle">{subtitle}</p>{children}<footer>Protected by AuthinCore</footer></section></main>
}

export default function App() {
  const invite = location.pathname.match(/^\/invitations\/([^/]+)$/)
  if (invite) return <InvitationPage token={decodeURIComponent(invite[1])} />
  if (location.pathname === '/oauth2/consent') return <ConsentPage />
  const config = routeKinds[location.pathname] ?? routeKinds['/login']
  return <FlowPage config={config} />
}
