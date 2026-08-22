import { useEffect, useState } from 'react'
import { api } from '../lib/api'

interface Role {
  id: string; org_id: string; key: string; display_name: string; created_at: string
}

export default function Roles() {
  const [roles, setRoles] = useState<Role[]>([])
  const [total, setTotal] = useState(0)
  const [showCreate, setShowCreate] = useState(false)
  const [key, setKey] = useState('')
  const [displayName, setDisplayName] = useState('')
  const [creating, setCreating] = useState(false)
  const [err, setErr] = useState('')

  const load = () =>
    api.get<{ items: Role[]; total: number }>('/api/v1/roles')
      .then(r => { setRoles(r.items ?? []); setTotal(r.total ?? 0) })
      .catch(() => {})

  useEffect(() => { load() }, [])

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    setCreating(true)
    try {
      await api.post('/api/v1/roles', { key, display_name: displayName })
      setShowCreate(false)
      setKey('')
      setDisplayName('')
      load()
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'Failed to create role')
    } finally {
      setCreating(false)
    }
  }

  const deleteRole = async (id: string) => {
    if (!confirm('Delete this role? All assignments will be removed.')) return
    await api.delete(`/api/v1/roles/${id}`).catch(() => {})
    load()
  }

  const inp = 'w-full border border-gray-200 rounded px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-blue-500'

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold text-gray-900">Roles <span className="text-gray-400 text-sm font-normal">({total})</span></h1>
          <p className="text-sm text-gray-500 mt-0.5">Define roles and assign them to identities.</p>
        </div>
        <button onClick={() => setShowCreate(true)}
          className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700">
          + New Role
        </button>
      </div>

      {showCreate && (
        <div className="fixed inset-0 bg-black/30 flex items-center justify-center z-50">
          <div className="bg-white rounded-xl shadow-xl p-6 w-full max-w-sm">
            <h2 className="text-lg font-semibold text-gray-900 mb-4">Create Role</h2>
            {err && <p className="text-sm text-red-500 mb-3">{err}</p>}
            <form onSubmit={create} className="space-y-3">
              <div>
                <label className="text-xs text-gray-500 block mb-1">Key <span className="text-gray-400">(e.g. admin, viewer)</span></label>
                <input required className={inp} value={key} onChange={e => setKey(e.target.value.toLowerCase().replace(/\s/g, '_'))} placeholder="admin" />
              </div>
              <div>
                <label className="text-xs text-gray-500 block mb-1">Display name</label>
                <input className={inp} value={displayName} onChange={e => setDisplayName(e.target.value)} placeholder="Administrator" />
              </div>
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
        {roles.length === 0 ? (
          <div className="p-8 text-center text-sm text-gray-400">No roles defined yet. Create your first role above.</div>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b border-gray-200">
              <tr>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">Key</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">Display name</th>
                <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">Created</th>
                <th className="px-4 py-3" />
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {roles.map(r => (
                <tr key={r.id} className="hover:bg-gray-50">
                  <td className="px-4 py-3">
                    <span className="font-mono text-xs bg-gray-100 text-gray-700 px-2 py-0.5 rounded">{r.key}</span>
                  </td>
                  <td className="px-4 py-3 text-gray-800">{r.display_name || <span className="text-gray-400">—</span>}</td>
                  <td className="px-4 py-3 text-gray-400 text-xs">{new Date(r.created_at).toLocaleDateString()}</td>
                  <td className="px-4 py-3 text-right">
                    <button onClick={() => deleteRole(r.id)}
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

      <div className="bg-blue-50 border border-blue-100 rounded-lg p-4">
        <p className="text-xs font-semibold text-blue-700 mb-1">Assigning roles to identities</p>
        <p className="text-xs text-blue-600">Use the API: <code className="bg-blue-100 px-1 rounded">POST /api/v1/roles/assign</code> with <code className="bg-blue-100 px-1 rounded">{'{ identity_id, role_id }'}</code> — or open an identity and assign roles from there (coming soon).</p>
      </div>
    </div>
  )
}
