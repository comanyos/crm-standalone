import { useState } from 'react'
import { useNavigate } from 'react-router-dom'

const defaultReturnOrigins = ['http://localhost:9304', 'http://127.0.0.1:9304']

export function getSafeReturnTo(search: string): string | null {
  const requested = new URLSearchParams(search).get('return_to')
  if (!requested) return null

  try {
    const target = new URL(requested)
    const configuredOrigins = (import.meta.env.VITE_ALLOWED_RETURN_ORIGINS as string | undefined)
      ?.split(',')
      .map(origin => origin.trim())
      .filter(Boolean) ?? []
    const allowedOrigins = new Set([...defaultReturnOrigins, ...configuredOrigins])

    if (!['http:', 'https:'].includes(target.protocol) || target.username || target.password) return null
    return allowedOrigins.has(target.origin) ? target.toString() : null
  } catch {
    return null
  }
}

export default function Login() {
  const nav = useNavigate()
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(false)
  const [mode, setMode] = useState<'login' | 'register'>('login')

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    setLoading(true)
    try {
      const flowType = mode === 'login' ? 'login' : 'registration'
      const flowRes = await fetch(`/self-service/${flowType}`, { credentials: 'include' })
      if (!flowRes.ok) throw new Error(`Failed to init flow: ${flowRes.status}`)
      const flow = await flowRes.json()
      const flowID = flow.id ?? flow.ID

      const submitRes = await fetch(`/self-service/${flowType}?flow=${flowID}`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          method: 'password',
		  body: mode === 'login'
			? { identifier: email, password }
			: { traits: { email }, password, schema_id: 'default' },
        }),
      })
      const result = await submitRes.json()
      if (!submitRes.ok) {
        throw new Error(result.error ?? result.message ?? 'Authentication failed')
      }
      const returnTo = getSafeReturnTo(window.location.search)
      if (returnTo) {
        window.location.assign(returnTo)
        return
      }
      nav('/')
      window.location.reload()
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'Unknown error')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50">
      <div className="bg-white border border-gray-200 rounded-xl p-8 w-full max-w-sm shadow-sm">
        <h1 className="text-xl font-semibold text-gray-900 mb-1">AuthinCore Admin</h1>
        <p className="text-sm text-gray-500 mb-6">
          {mode === 'login' ? 'Sign in to your account' : 'Create admin account'}
        </p>

        {err && <p className="text-sm text-red-500 mb-4 bg-red-50 border border-red-100 rounded p-2">{err}</p>}

        <form onSubmit={submit} className="space-y-4">
          <div>
			<label htmlFor="admin-email" className="text-xs font-medium text-gray-600 block mb-1">Email</label>
			<input id="admin-email" name="email" autoComplete="username" type="email" value={email} onChange={e => setEmail(e.target.value)} required
              className="w-full border border-gray-200 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div>
			<label htmlFor="admin-password" className="text-xs font-medium text-gray-600 block mb-1">Password</label>
			<input id="admin-password" name="password" autoComplete={mode === 'login' ? 'current-password' : 'new-password'} type="password" value={password} onChange={e => setPassword(e.target.value)} required
              className="w-full border border-gray-200 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <button type="submit" disabled={loading}
            className="w-full bg-blue-600 text-white rounded-lg py-2 text-sm font-medium hover:bg-blue-700 disabled:opacity-40">
            {loading ? 'Please wait…' : mode === 'login' ? 'Sign in' : 'Create account'}
          </button>
        </form>

        <p className="mt-4 text-center text-xs text-gray-500">
          {mode === 'login'
			? <>No account? <button type="button" onClick={() => { setMode('register'); setErr('') }} className="text-blue-600 hover:underline">Register</button></>
			: <>Have an account? <button type="button" onClick={() => { setMode('login'); setErr('') }} className="text-blue-600 hover:underline">Sign in</button></>
          }
        </p>
      </div>
    </div>
  )
}
