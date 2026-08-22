import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { identities, api, type Identity } from '../lib/api'
import Table from '../components/Table'
import Badge from '../components/Badge'

export default function Identities() {
  const [rows, setRows] = useState<Identity[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [showCreate, setShowCreate] = useState(false)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [creating, setCreating] = useState(false)
  const [createErr, setCreateErr] = useState('')
  const navigate = useNavigate()

  const load = (p = page) =>
    identities.list(p)
      .then(r => { setRows(r.items ?? []); setTotal(r.total ?? 0) })
      .catch(() => {})

  useEffect(() => { load() }, [page])

  const createIdentity = async (e: React.FormEvent) => {
    e.preventDefault()
    setCreateErr('')
    setCreating(true)
    try {
      await api.post('/api/v1/identities', {
        schema_id: 'default',
        traits: { email },
        credentials: password ? { password: { config: { password } } } : undefined,
      })
      setShowCreate(false)
      setEmail('')
      setPassword('')
      load()
    } catch (e: unknown) {
      setCreateErr(e instanceof Error ? e.message : 'Failed to create')
    } finally {
      setCreating(false)
    }
  }

  const inp = 'w-full border border-gray-200 rounded px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-blue-500'

  return (
    <div>
      <div className="flex items-center justify-between mb-5">
        <h1 className="text-xl font-semibold text-gray-900">Identities <span className="text-gray-400 text-sm font-normal">({total})</span></h1>
        <button onClick={() => setShowCreate(true)}
          className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700">
          + New Identity
        </button>
      </div>

      {/* Create modal */}
      {showCreate && (
        <div className="fixed inset-0 bg-black/30 flex items-center justify-center z-50">
          <div className="bg-white rounded-xl shadow-xl p-6 w-full max-w-sm">
            <h2 className="text-lg font-semibold text-gray-900 mb-4">Create Identity</h2>
            {createErr && <p className="text-sm text-red-500 mb-3">{createErr}</p>}
            <form onSubmit={createIdentity} className="space-y-3">
              <div>
                <label className="text-xs text-gray-500 block mb-1">Email</label>
                <input type="email" required className={inp} value={email} onChange={e => setEmail(e.target.value)} />
              </div>
              <div>
                <label className="text-xs text-gray-500 block mb-1">Password <span className="text-gray-400">(optional)</span></label>
                <input type="password" className={inp} value={password} onChange={e => setPassword(e.target.value)} />
              </div>
              <div className="flex gap-2 pt-1">
                <button type="submit" disabled={creating}
                  className="flex-1 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700 disabled:opacity-40">
                  {creating ? 'Creating…' : 'Create'}
                </button>
                <button type="button" onClick={() => { setShowCreate(false); setCreateErr('') }}
                  className="flex-1 py-1.5 border border-gray-200 text-sm rounded hover:bg-gray-50">
                  Cancel
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      <Table<Identity>
        columns={[
          { key: 'id', header: 'ID', render: r => <span className="font-mono text-xs">{r.id.slice(0,8)}…</span> },
          { key: 'email', header: 'Email', render: r => r.traits?.email ?? '—' },
          { key: 'state', header: 'State', render: r => <Badge label={r.state === 1 ? 'active' : 'inactive'} color={r.state === 1 ? 'green' : 'gray'} /> },
          { key: 'schema_id', header: 'Schema' },
        ]}
        rows={rows}
        onRowClick={r => navigate(`/identities/${r.id}`)}
      />
      <div className="flex justify-between items-center mt-4 text-sm text-gray-500">
        <span>Page {page} · {rows.length} of {total}</span>
        <div className="space-x-2">
          <button disabled={page === 1} onClick={() => setPage(p => p - 1)}
            className="px-3 py-1 rounded border border-gray-200 disabled:opacity-40 hover:bg-gray-50">Prev</button>
          <button disabled={rows.length < 20} onClick={() => setPage(p => p + 1)}
            className="px-3 py-1 rounded border border-gray-200 disabled:opacity-40 hover:bg-gray-50">Next</button>
        </div>
      </div>
    </div>
  )
}
