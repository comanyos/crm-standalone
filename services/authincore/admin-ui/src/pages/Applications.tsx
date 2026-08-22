import { useEffect, useState } from 'react'
import { api } from '../lib/api'

interface Application {
  id: string; org_id: string; name: string
  client_id: string; client_secret?: string
  redirect_uris: string[]; grant_types: string[]; response_types: string[]
  created_at: string
}

export default function Applications() {
  const [apps, setApps] = useState<Application[]>([])
  const [total, setTotal] = useState(0)
  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState('')
  const [redirectURIs, setRedirectURIs] = useState('')
  const [creating, setCreating] = useState(false)
  const [err, setErr] = useState('')
  const [newApp, setNewApp] = useState<Application | null>(null)
  const [copied, setCopied] = useState(false)

  const load = () =>
    api.get<{ items: Application[]; total: number }>('/api/v1/applications')
      .then(r => { setApps(r.items ?? []); setTotal(r.total ?? 0) })
      .catch(() => {})

  useEffect(() => { load() }, [])

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    setCreating(true)
    try {
      const uris = redirectURIs.split('\n').map(s => s.trim()).filter(Boolean)
      const app = await api.post<Application>('/api/v1/applications', {
        name,
        redirect_uris: uris,
        grant_types: ['authorization_code'],
        response_types: ['code'],
      })
      setNewApp(app)
      setShowCreate(false)
      setName('')
      setRedirectURIs('')
      load()
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'Failed to create application')
    } finally {
      setCreating(false)
    }
  }

  const deleteApp = async (id: string) => {
    if (!confirm('Delete this application? This will break any clients using it.')) return
    await api.delete(`/api/v1/applications/${id}`).catch(() => {})
    load()
  }

  const copySecret = (secret: string) => {
    navigator.clipboard.writeText(secret).then(() => { setCopied(true); setTimeout(() => setCopied(false), 2000) })
  }

  const inp = 'w-full border border-gray-200 rounded px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-blue-500'

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold text-gray-900">Applications <span className="text-gray-400 text-sm font-normal">({total})</span></h1>
          <p className="text-sm text-gray-500 mt-0.5">OIDC client applications that integrate with AuthinCore.</p>
        </div>
        <button onClick={() => setShowCreate(true)}
          className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700">
          + New Application
        </button>
      </div>

      {/* New app secret reveal */}
      {newApp && (
        <div className="bg-yellow-50 border border-yellow-200 rounded-lg p-4 space-y-2">
          <p className="text-sm font-semibold text-yellow-800">Application created — save your client secret now</p>
          <p className="text-xs text-yellow-700">The client secret is shown only once. Copy it before leaving.</p>
          <div className="bg-white border border-yellow-200 rounded p-3 space-y-1.5 text-xs font-mono">
            <div><span className="text-gray-400">Client ID: </span><span className="text-gray-800">{newApp.client_id}</span></div>
            <div className="flex items-center gap-2">
              <span className="text-gray-400">Client secret: </span>
              <span className="text-gray-800">{newApp.client_secret}</span>
              <button onClick={() => copySecret(newApp.client_secret!)}
                className="text-blue-600 hover:underline text-xs">{copied ? 'Copied!' : 'Copy'}</button>
            </div>
          </div>
          <button onClick={() => setNewApp(null)} className="text-xs text-yellow-700 hover:underline">Dismiss</button>
        </div>
      )}

      {showCreate && (
        <div className="fixed inset-0 bg-black/30 flex items-center justify-center z-50">
          <div className="bg-white rounded-xl shadow-xl p-6 w-full max-w-sm">
            <h2 className="text-lg font-semibold text-gray-900 mb-4">New Application</h2>
            {err && <p className="text-sm text-red-500 mb-3">{err}</p>}
            <form onSubmit={create} className="space-y-3">
              <div>
                <label className="text-xs text-gray-500 block mb-1">Application name</label>
                <input required className={inp} value={name} onChange={e => setName(e.target.value)} placeholder="My Web App" />
              </div>
              <div>
                <label className="text-xs text-gray-500 block mb-1">
                  Redirect URIs <span className="text-gray-400">(one per line)</span>
                </label>
                <textarea rows={3} className={inp} value={redirectURIs} onChange={e => setRedirectURIs(e.target.value)}
                  placeholder={'https://myapp.com/callback\nhttp://localhost:3000/callback'} />
              </div>
              <p className="text-xs text-gray-400">Grant type: authorization_code · Response type: code</p>
              <div className="flex gap-2 pt-1">
                <button type="submit" disabled={creating}
                  className="flex-1 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700 disabled:opacity-40">
                  {creating ? 'Creating…' : 'Create'}
                </button>
                <button type="button" onClick={() => { setShowCreate(false); setErr('') }}
                  className="flex-1 py-1.5 border border-gray-200 text-sm rounded hover:bg-gray-50">
                  Cancel
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      <div className="bg-white rounded-lg border border-gray-200 overflow-hidden">
        {apps.length === 0 ? (
          <div className="p-8 text-center text-sm text-gray-400">No applications yet. Create one to get started.</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b border-gray-200">
              <tr>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">Name</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">Client ID</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">Redirect URIs</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">Created</th>
                <th className="px-4 py-3" />
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {apps.map(a => (
                <tr key={a.id} className="hover:bg-gray-50">
                  <td className="px-4 py-3 font-medium text-gray-900">{a.name}</td>
                  <td className="px-4 py-3 font-mono text-xs text-gray-600">{a.client_id}</td>
                  <td className="px-4 py-3 text-xs text-gray-500 max-w-xs truncate">
                    {(a.redirect_uris ?? []).join(', ') || <span className="text-gray-300">—</span>}
                  </td>
                  <td className="px-4 py-3 text-xs text-gray-400">{new Date(a.created_at).toLocaleDateString()}</td>
                  <td className="px-4 py-3 text-right">
                    <button onClick={() => deleteApp(a.id)}
                      className="text-xs text-red-500 hover:text-red-700 hover:underline">
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="bg-gray-50 border border-gray-200 rounded-lg p-4">
        <p className="text-xs font-semibold text-gray-600 mb-1">OIDC endpoints</p>
        <div className="text-xs font-mono text-gray-500 space-y-1">
          <div><span className="text-gray-400">Authorization: </span>/oauth2/authorize</div>
          <div><span className="text-gray-400">Token: </span>/oauth2/token</div>
          <div><span className="text-gray-400">JWKS: </span>/oauth2/.well-known/jwks.json</div>
          <div><span className="text-gray-400">Discovery: </span>/.well-known/openid-configuration</div>
        </div>
      </div>
    </div>
  )
}
