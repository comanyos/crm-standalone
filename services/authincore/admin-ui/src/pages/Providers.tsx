import { useEffect, useState } from 'react'
import { api } from '../lib/api'
import Badge from '../components/Badge'

interface Provider {
  id: string
  name: string
	auth_url: string
	token_url: string
	user_info_url: string
  client_id: string
  client_secret?: string
  scopes?: string[]
  redirect_uri?: string
}

const REDIRECT_BASE = (import.meta.env.VITE_API_URL || 'http://localhost:8080')

const PRESETS: Record<string, Partial<Provider>> = {
  google: {
    name: 'Google',
	auth_url: 'https://accounts.google.com/o/oauth2/v2/auth',
	token_url: 'https://oauth2.googleapis.com/token',
	user_info_url: 'https://openidconnect.googleapis.com/v1/userinfo',
    scopes: ['openid', 'email', 'profile'],
  },
  microsoft: {
    name: 'Microsoft',
	auth_url: 'https://login.microsoftonline.com/common/oauth2/v2.0/authorize',
	token_url: 'https://login.microsoftonline.com/common/oauth2/v2.0/token',
	user_info_url: 'https://graph.microsoft.com/oidc/userinfo',
    scopes: ['openid', 'email', 'profile'],
  },
}

export default function Providers() {
  const [rows, setRows] = useState<Provider[]>([])
  const [err, setErr] = useState('')
  const [saving, setSaving] = useState(false)
  const [form, setForm] = useState<Partial<Provider>>({
	id: '', name: '', auth_url: '', token_url: '', user_info_url: '', client_id: '', client_secret: '',
    redirect_uri: `${REDIRECT_BASE}/self-service/oidc/callback`,
  })

  const load = () =>
    api.get<{ providers: Provider[] }>('/api/v1/oidc-providers')
      .then(r => setRows(r.providers ?? []))
      .catch(e => setErr(e.message))

  useEffect(() => { load() }, [])

  const applyPreset = (key: string) => {
    const p = PRESETS[key] ?? {}
    setForm(f => ({ ...f, id: key, ...p }))
  }

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    setSaving(true)
    await api.post('/api/v1/oidc-providers', {
      ...form,
      scopes: typeof form.scopes === 'string'
        ? (form.scopes as unknown as string).split(/[\s,]+/).filter(Boolean)
        : (form.scopes ?? ['openid', 'email', 'profile']),
    }).catch(e => setErr(e.message))
	setForm(f => ({ ...f, id: '', name: '', auth_url: '', token_url: '', user_info_url: '', client_id: '', client_secret: '' }))
    await load()
    setSaving(false)
  }

  const del = async (id: string) => {
    if (!confirm(`Remove provider "${id}"?`)) return
    await api.delete(`/api/v1/oidc-providers/${id}`).catch(e => setErr(e.message))
    await load()
  }

  const inp = 'w-full border border-gray-200 rounded px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-blue-500'

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-gray-900">Social Login Providers</h1>
        <p className="text-sm text-gray-500 mt-1">Configure OIDC providers for social login (Google, GitHub, etc.)</p>
      </div>
      {err && <p className="text-sm text-red-500 bg-red-50 border border-red-100 rounded p-2">{err}</p>}

      {/* Quick presets */}
      <div className="flex gap-2">
        {Object.keys(PRESETS).map(k => (
          <button key={k} onClick={() => applyPreset(k)}
            className="px-3 py-1 text-sm border border-gray-200 rounded hover:bg-gray-50 capitalize">
            + {k}
          </button>
        ))}
      </div>

      {/* Add form */}
      <form onSubmit={save} className="bg-white border border-gray-200 rounded-lg p-5 space-y-3">
        <h2 className="text-sm font-semibold text-gray-700">Add provider</h2>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="text-xs text-gray-500 block mb-1">Provider ID <span className="text-gray-400">(e.g. google)</span></label>
            <input className={inp} required value={form.id ?? ''} onChange={e => setForm(f => ({ ...f, id: e.target.value }))} placeholder="google" />
          </div>
          <div>
            <label className="text-xs text-gray-500 block mb-1">Display name</label>
            <input className={inp} required value={form.name ?? ''} onChange={e => setForm(f => ({ ...f, name: e.target.value }))} placeholder="Google" />
          </div>
          <div>
			<label className="text-xs text-gray-500 block mb-1">Authorization URL</label>
			<input className={inp} required value={form.auth_url ?? ''} onChange={e => setForm(f => ({ ...f, auth_url: e.target.value }))} placeholder="https://provider.example/authorize" />
          </div>
		  <div>
			<label className="text-xs text-gray-500 block mb-1">Token URL</label>
			<input className={inp} required value={form.token_url ?? ''} onChange={e => setForm(f => ({ ...f, token_url: e.target.value }))} placeholder="https://provider.example/token" />
		  </div>
		  <div>
			<label className="text-xs text-gray-500 block mb-1">UserInfo URL</label>
			<input className={inp} required value={form.user_info_url ?? ''} onChange={e => setForm(f => ({ ...f, user_info_url: e.target.value }))} placeholder="https://provider.example/userinfo" />
		  </div>
		  <div>
            <label className="text-xs text-gray-500 block mb-1">Client ID</label>
            <input className={inp} required value={form.client_id ?? ''} onChange={e => setForm(f => ({ ...f, client_id: e.target.value }))} placeholder="123...apps.googleusercontent.com" />
          </div>
          <div>
            <label className="text-xs text-gray-500 block mb-1">Client Secret</label>
            <input className={inp} type="password" value={form.client_secret ?? ''} onChange={e => setForm(f => ({ ...f, client_secret: e.target.value }))} placeholder="••••••••" />
          </div>
          <div>
            <label className="text-xs text-gray-500 block mb-1">Redirect URI</label>
            <input className={inp} value={form.redirect_uri ?? ''} onChange={e => setForm(f => ({ ...f, redirect_uri: e.target.value }))} />
          </div>
        </div>
        <button disabled={saving} type="submit"
          className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700 disabled:opacity-40">
          {saving ? 'Saving…' : 'Add provider'}
        </button>
      </form>

      {/* Provider list */}
      <div className="bg-white border border-gray-200 rounded-lg overflow-hidden">
        {rows.length === 0 ? (
          <p className="text-sm text-gray-400 p-5">No providers configured yet.</p>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b border-gray-200">
              <tr>
			  {['ID', 'Name', 'Authorization URL', 'Status', ''].map(h => (
                  <th key={h} className="px-4 py-2 text-left text-xs font-medium text-gray-500">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {rows.map(p => (
                <tr key={p.id} className="hover:bg-gray-50">
                  <td className="px-4 py-2 font-mono text-xs">{p.id}</td>
                  <td className="px-4 py-2">{p.name}</td>
				<td className="px-4 py-2 text-gray-500 truncate max-w-xs">{p.auth_url}</td>
                  <td className="px-4 py-2"><Badge label="active" color="green" /></td>
                  <td className="px-4 py-2 text-right">
                    <button onClick={() => del(p.id)}
                      className="text-xs text-red-500 hover:text-red-700">Remove</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Integration guide */}
      <div className="bg-blue-50 border border-blue-100 rounded-lg p-4 text-sm text-blue-800 space-y-1">
        <p className="font-medium">Redirect URI to register with your provider:</p>
        <code className="block bg-white border border-blue-200 rounded px-3 py-1.5 text-xs font-mono">
          {REDIRECT_BASE}/self-service/oidc/callback
        </code>
      </div>
    </div>
  )
}
