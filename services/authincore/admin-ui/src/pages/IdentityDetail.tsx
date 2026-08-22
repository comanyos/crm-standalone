import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { identities, sessions, type Identity, type Session } from '../lib/api'
import Badge from '../components/Badge'
import Table from '../components/Table'

export default function IdentityDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const [identity, setIdentity] = useState<Identity | null>(null)
  const [sess, setSess] = useState<Session[]>([])
  const [err, setErr] = useState('')
  const [revoking, setRevoking] = useState(false)
  const [editing, setEditing] = useState(false)
  const [traitsText, setTraitsText] = useState('')
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (!id) return
    identities.get(id).then(i => { setIdentity(i); setTraitsText(JSON.stringify(i.traits, null, 2)) }).catch(e => setErr(e.message))
    sessions.list(id).then(setSess).catch(() => {})
  }, [id])

  const saveTraits = async () => {
    if (!id || !identity) return
    setSaving(true)
    setErr('')
    try {
      const parsed = JSON.parse(traitsText)
      const updated = await identities.patch(id, parsed)
      setIdentity(updated)
      setEditing(false)
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'Invalid JSON or save failed')
    } finally {
      setSaving(false)
    }
  }

  const revokeAll = async () => {
    if (!id) return
    setRevoking(true)
    await sessions.revokeAll(id).catch(e => setErr(e.message))
    sessions.list(id).then(setSess).finally(() => setRevoking(false))
  }

  const deleteIdentity = async () => {
    if (!id || !confirm('Delete this identity?')) return
    await identities.delete(id).catch(e => setErr(e.message))
    navigate('/identities')
  }

  if (err) return <p className="text-red-500">{err}</p>
  if (!identity) return <p className="text-gray-400">Loading…</p>

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <button onClick={() => navigate('/identities')} className="text-sm text-brand-600 hover:underline mb-1">← Identities</button>
          <h1 className="text-xl font-semibold text-gray-900 font-mono">{identity.id}</h1>
        </div>
        <div className="space-x-2">
          <button onClick={revokeAll} disabled={revoking}
            className="px-3 py-1.5 text-sm rounded border border-gray-200 hover:bg-gray-50 disabled:opacity-40">
            {revoking ? 'Revoking…' : 'Revoke all sessions'}
          </button>
          <button onClick={deleteIdentity}
            className="px-3 py-1.5 text-sm rounded bg-red-50 text-red-600 border border-red-200 hover:bg-red-100">
            Delete
          </button>
        </div>
      </div>

      <div className="bg-white rounded-lg border border-gray-200 p-5 space-y-3">
        <div className="flex gap-8">
          <div><span className="text-xs text-gray-400 block">State</span><Badge label={identity.state === 1 ? 'active' : 'inactive'} color={identity.state === 1 ? 'green' : 'gray'} /></div>
          <div><span className="text-xs text-gray-400 block">Schema</span><span className="text-sm">{identity.schema_id}</span></div>
        </div>
        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-xs text-gray-400">Traits</span>
            {!editing
              ? <button onClick={() => setEditing(true)} className="text-xs text-blue-600 hover:underline">Edit</button>
              : <div className="flex gap-2">
                  <button onClick={saveTraits} disabled={saving} className="text-xs text-blue-600 hover:underline disabled:opacity-40">{saving ? 'Saving…' : 'Save'}</button>
                  <button onClick={() => { setEditing(false); setTraitsText(JSON.stringify(identity.traits, null, 2)) }} className="text-xs text-gray-400 hover:underline">Cancel</button>
                </div>
            }
          </div>
          {editing
            ? <textarea value={traitsText} onChange={e => setTraitsText(e.target.value)} rows={6}
                className="w-full bg-gray-50 border border-blue-300 rounded p-3 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-blue-500" />
            : <pre className="bg-gray-50 rounded p-3 text-xs overflow-x-auto">{JSON.stringify(identity.traits, null, 2)}</pre>
          }
        </div>
      </div>

      <div>
        <h2 className="text-sm font-semibold text-gray-700 mb-3">Sessions ({sess.length})</h2>
        <Table<Session>
          columns={[
            { key: 'id', header: 'ID', render: r => <span className="font-mono text-xs">{r.id.slice(0,8)}…</span> },
            { key: 'state', header: 'State', render: r => <Badge label={r.state === 1 ? 'active' : 'inactive'} color={r.state === 1 ? 'green' : 'gray'} /> },
            { key: 'authentication_methods', header: 'Auth method', render: r => r.authentication_methods?.[0]?.method ?? '—' },
            { key: 'expires_at', header: 'Expires', render: r => new Date(r.expires_at).toLocaleDateString() },
          ]}
          rows={sess}
        />
      </div>
    </div>
  )
}
